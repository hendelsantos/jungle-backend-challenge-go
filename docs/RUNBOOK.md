# Runbook operacional — Jungle Backend Go

> Procedimentos de diagnóstico e resposta para os modos de falha conhecidos.
> Nenhum procedimento aqui altera saldos, ledger ou eventos; correção financeira
> manual exige decisão humana e trilha auditável (nunca silenciosa).

## Sinais principais (Prometheus por réplica)

- `jungle_outbox_pending` / `jungle_outbox_oldest_age_seconds` — eventos
  comprometidos aguardando publicação no broker.
- `jungle_wager_dlq_total` / `jungle_wager_sqs_redeliveries_total` — fila
  morta e reentregas do consumidor SQS.
- `jungle_wager_technical...`: `jungle_wager_operations_total{kind,status}`,
  `jungle_wager_idempotent_replays_total`, `jungle_wager_idempotency_conflicts_total`,
  `jungle_wager_processing_duration_seconds{kind}` (histograma),
  `jungle_wager_db_retries_total{reason}`, `jungle_wallet_reconciliations_total`,
  `jungle_wallet_reconciliation_divergences_total`.
- `jungle_pgpool_*` — conexões totais/ociosas/aquisição média por réplica.

## Alertas recomendados

| Alerta | Condição sugerida | Ação |
| --- | --- | --- |
| Outbox atrasada | `max(jungle_outbox_oldest_age_seconds) > 300` | §4 |
| Erros técnicos crescendo | soma de `jungle_outbox_publish_failures_total` + falhas 5xx por minuto > limiar | §2/§5 |
| DLQ crescendo | `delta(jungle_wager_dlq_total[10m]) > 0` persistente | §3 |
| Divergência financeira | `jungle_wallet_reconciliation_divergences_total` incrementa | §6 (P0: gatilho humano já previsto) |
| Pool saturado | `jungle_pgpool_acquired_conns == jungle_pgpool_total_conns` por > 2 min com `acquire_duration > 1s` | §7 |
| Réplica instável | reinícios repetidos (`restarts` do orquestrador) | §8 |

## 1. PostgreSQL indisponível

**Sintomas:** `/health/ready` 503 com `checks.postgres != ok`; logs
`ping database` falhando no start; requests 503.

**Diagnóstico:** `docker compose ps`; `docker compose logs postgres`;
conexões (`pg_stat_activity`).

**Resposta:** reiniciar `postgres` (`docker compose restart postgres`); o gate de dependências do Compose vale na inicialização, não bloqueia
tráfego de processos já iniciados. Pós-recuperação:
conferir `/health/ready` em todas as réplicas e rodar `cmd/verifywallet` nas
últimas carteiras ativas para confirmar integridade.

## 2. Keycloak (IdP) indisponível

**Sintomas:** 401 em massa com `UNAUTHENTICATED`; startup trava em discovery.

**Resposta:** `docker compose restart keycloak`; app não serve sem IdP
(propositado). Depois: verificar emissão de token `client_credentials` de
`provider-a-client`; auditar falhas espúrias em `jungle_wager_operations_total`.

## 3. LocalStack/SQS indisponível

**Sintomas:** readiness 503 (`checks.sqs`); consumidor/publisher registram
erros `sendMessage/Receive` com backoff.

**Resposta:** `docker compose restart localstack`; readiness fica 503 até recuperar filas; o gateway local não remove
réplicas automaticamente. **Limitação real do
ambiente de desenvolvimento, verificada empiricamente em 13/10/2026:** o
LocalStack Community não tem persistência (`PERSISTENCE=1` não persiste filas
na edição Community — restart de prova apagou a fila; persistência é recurso
Pro). Ensaio com registros antes/depois: 2 eventos publicados e confirmados na
outbox (`published_at` preenchido) e visíveis na fila → restart somente do
LocalStack → `ListQueues` vazio, readiness 503, banco intacto → restart das
APIs → filas recriadas, readiness 200, **fila de eventos com 0 mensagens: os
2 eventos publicados foram perdidos do broker**. O banco de outbox permanece
auditável; republicação automática desses eventos não é o desenho — a
garantia at-least-once cobre perdas ANTES do ack, não a perda do armazenamento
do broker depois. SQS gerenciado tem garantias próprias de durabilidade, que não foram
validadas neste ambiente. Não afirmar "não há perda" ao reiniciar o
broker de desenvolvimento. Conferir §4 para o caso de eventos pendentes
(pré-ack).

## 4. Outbox acumulada

**Diagnóstico:** `jungle_outbox_pending`, `oldest_age_seconds`; contagem via
`SELECT count(*) FROM outbox_events WHERE published_at IS NULL`.

**Resposta:** (a) identificar a causa — publisher parado (réplica derrubada),
broker fora (§3), over-eng cumulativa do produtor (§7); (b) sem produção nova,
medir a taxa de dreno com `OUTBOX_WORKERS` (3 por padrão ≈ 408 ev/s total em
3 réplicas); (c) NUNCA marcar eventos manualmente como publicados nem apagar
linhas: republicação segura é o mecanismo projetado (mesmo `eventId`,
dedup at-least-once pelo consumidor).

## 5. Mensagens na DLQ

**Diagnóstico:** receber as mensagens da DLQ com `awslocal sqs receive-message`
(no Compose de dev) e inspecionar o envelope: `INVALID_ENVELOPE`,
`UNAUTHORIZED_PRODUCER`, `INBOX_CONFLICT` ou tentativas esgotadas.

**Resposta:** mensagens de causa permanente só serão reiniciadas após correção
da causa real (assinatura/envelope) via redrive manual — cada redrive é
idempotente graças à inbox. Registar em ticket de incidente a contagem e o
motivo; não é permitido deletar a fila para "sumir" com o problema.

## 6. Divergência financeira

**Diagnóstico:** alerta dispara quando `consistent=false` em
`POST /wallets/:id/reconciliation` (janela `REPEATABLE READ READ ONLY`,
`difference = stored − computed`).

**Resposta:** congelar operações da carteira afetada (revogar credencial do
provedor se necessário), coletar `wallet_ledger_entries` ordenados, as transações com `result_*` e o
ledger completo para investigação. NENHUMA correção automática existe no
sistema por design — decisão manual com trilha auditável (a correção entra
como NOVO lançamento, nunca `UPDATE` no ledger; revisão humana documentada).

## 7. Pool PostgreSQL saturado

**Sintomas:** `jungle_pgpool_acquired_conns == jungle_pgpool_total_conns`
sustentado; `acquire_duration_ms` elevado; p95 subindo sem CPU na app.

**Resposta:** aumentou `PG_POOL_MAX_CONNS` por réplica (24 é o default do
Computed A/B; 3×24=72 < 100 con Postgres default `max_connections`);
alternativa: reduzir réplicas temporariamente. Lembre: mais pool além de 24
piora (A/B: c36/c48 degradaram por saturação do Postgres).

## 8. Réplica instável

**Sintomas:** reinícios em loop de uma réplica; readiness oscilando.

**Resposta:** `docker compose logs api-<n>`; remover a réplica do gateway
(retirar do `upstream`) até diagnóstico; as duas restante mantêm a integridade
(SQ. SKIP LOCKED e índices únicos decidem no banco). Kills literais são
exercícios cobertos (`scripts/fault.sh`).

## Limites de atuação

- Nenhum procedimento altera o financial ledger (append-only físico).
- Operações manuais de correção exigem: (1) evidência de divergência, (2)
  decisão registrada como novo lançamento auditável, (3) reconciliação pós-opera
  constatando `difference=0`.
- O environment local (Compose) é descartável: `docker compose down -v`
  apaga dados de desenvolvimento — nunca executado contra banco externo.

## Evidência atual do broker — 14/10/2026

`scripts/broker-probe.py` comprovou em container exclusivo com volume,
PERSISTENCE=1 e ENFORCE_IAM=1: envio anônimo sob política Deny recebeu 200;
após restart, GetQueueUrl retornou QueueDoesNotExist. Configuração consultada
na [documentação LocalStack](https://docs.localstack.cloud/aws/customization/configuration-options/)
e [persistência](https://docs.localstack.cloud/aws/developer-tools/snapshots/persistence/).
Não foi encontrada configuração gratuita funcional para a imagem 4.14.0 usada.

`scripts/broker-recovery.py` (apenas com FAULT_DISPOSABLE=1 e projeto explícito)
separa os casos: broker pausado → readiness 503 e dois eventos persistidos ainda
pendentes → unpause → ambos publicados. Depois, 14 mensagens visíveis e 38
outboxes publicadas → restart do broker → filas ausentes/readiness 503 → restart
das APIs → readiness 200, zero mensagens e as mesmas 38 publicadas. **Exit 2**
é a reprovação de durabilidade esperada nesse ambiente; não é teste aprovado.
Banco pausado também levou readiness a 503; unpause recuperou prontidão.

O gateway local não bloqueia tráfego com base na readiness. Uma dependência
indisponível não implica que Compose vá parar/reiniciar APIs existentes.
Recuperar publicação pendente é diferente de recuperar armazenamento perdido
pelo broker após confirmação. Nenhuma garantia absoluta sobre AWS foi validada.

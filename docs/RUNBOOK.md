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

**Resposta:** reiniciar `postgres` (`docker compose restart postgres`); o
health gate do compose segura as réplicas (sem hidden writes). Pós-recuperação:
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

**Resposta:** `docker compose restart localstack`; o startup re-provisiona as
filas (idempotente). A outbox **não produz perda**: eventos ficam pendentes
(at-least-once) e drenam após a volta. Conferir §4.

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

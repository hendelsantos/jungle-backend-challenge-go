# Conformidade com o desafio oficial

Fonte normativa: [README original preservado](reference/README_DESAFIO.md), commit
`bdd9e70b65db9b374ee070bd3a4768661a888997`; [procedência](reference/SOURCE.md).
Revisão vigente: **14/10/2026**, implementação `56d5d76` e relatório
[AUDITORIA_FINAL_ENTREGA.md](AUDITORIA_FINAL_ENTREGA.md). **NO-GO para
conformidade integral:** A01/A02 permanecem pendentes no ambiente local.
As tabelas históricas abaixo não sobrepõem a matriz vigente.

## Matriz vigente de requisitos

| Exigência | Implementação e evidência atual | Estado |
| --- | --- | --- |
| §2 OIDC externo e isolamento | Keycloak real, tokens inválidos/expirados/audience, roles, isolamento de replay/consulta; integração sem skips | Atendido nos ensaios |
| §2 autorização do broker | Cadeia AWS sem fallback + HMAC; política Deny não aplicada no LocalStack; provisionador separado e dois clientes runtime; políticas ainda são templates no LocalStack | **Parcial — A01** |
| §3 preservação de eventos em falhas | Pendentes durante indisponibilidade se recuperam; restart do broker perde mensagens aceitas após ACK | **Não atendido localmente — A02** |
| §4 Go/Fx/PG/SQS/Compose/migrations | Build, vet, race, migrations reais; três processos independentes | Atendido nos ensaios |
| §§5–6 Money, saldo, ledger e atomicidade | Centavos int64, overflow, constraints, append-only, rollback de saldo/ledger/outbox/inbox | Atendido nos ensaios |
| §7 cinco operações e compensação | BET/WIN/LOSS/REFUND/ROLLBACK, ADR 0001, códigos estáveis; integração real | Atendido nos ensaios |
| §7 referências | Chegada posterior, TTL, lock real/relógio defasado, backoff e restart cruzando canais | Atendido nos ensaios |
| §§8–9 concorrência e idempotência | Smoke: 50 duplicatas→1 débito/49 replays; 80+80 sobre100→saldo20/1 rejeição; três APIs; HTTP↔SQS real | Atendido nos ensaios |
| §9 contratos/health/reconciliação | API autenticada, erros distintos, limites em bytes, snapshot, readiness 503 por PG/SQS | Atendido nos ensaios |
| §§6.5/10 inbox e delete pós-commit | Transação única, replay real após SIGKILL na janela E, redrive à DLQ e shutdown | Atendido nos ensaios |
| §11 outbox pós-commit e publishers | Claim/lease, backoff, concorrência; janela F: dois envios aceitos, eventId/hash idênticos e ACK na tentativa2 | Atendido, exceto perda pós-ACK do broker |
| §12 observabilidade | Resultados HTTP/SQS/referências, replays, retry, DLQ/falhas, duração, backlog e divergências; collectors por handler | Atendido com counters best-effort por processo |
| §13 testes reais e race | Gates versionados recusam skips; PostgreSQL, Keycloak e LocalStack reais | Atendido nos ensaios |
| §15 reprodução e documentação | Checkout limpo de 56d5d76: migrations 4/clean, três APIs saudáveis, smoke/fault E/F/H e SIGTERM com exit 0; guia e runbook | Atendido nos ensaios; [evidência](audit/2026-10-14/README.md) |

Opcional: carga implementada, com regressão curta e auditoria SQL. Partidas
dobradas, tracing e dashboards não entregues. Usuário SQL de runtime separado
não foi implantado no Compose; isso é limite operacional, não prova de menor
privilégio em produção. Nenhum serviço pago/AWS real foi usado.

## Requisitos e evidências da revisão anterior

A tabela abaixo preserva o registro anterior. Para o parecer vigente e as ressalvas por requisito, consultar a matriz atual acima e a auditoria final vinculada no início.

“Implementado na camada” descreve código e testes existentes nessa camada. “Parcial”
significa que falta uma parte do requisito ou sua demonstração no ambiente exigido.
Os caminhos de testes são reproduzíveis pelos comandos do [README](../README.md).

| Exigência oficial | Situação | Implementação/evidência e trabalho restante |
| --- | --- | --- |
| §4: Go, módulos, Fx e domínio independente | Implementado na camada | `go.mod`, `Dockerfile`, `internal/bootstrap` e `internal/domain`; testes de composição e lifecycle. Handlers financeiros e `securityModule`/`databaseModule` na composição Fx; consumidor SQS também composto com Fx; publisher pendente. |
| §6.1: dinheiro exato, escala, moedas e overflow | Implementado na camada | `internal/domain/money`, testes e fuzzing; centavos `int64`, JSON monetário em string. Decoder HTTP rejeita números/`null` na fronteira — nenhum caminho passa por `float64`. |
| §§5–6: carteira, estados, abertura e ledger | Implementado na camada | Domínio, `CreateWallet`, migrations e `create_wallet_integration_test.go`; abertura positiva atômica, zero sem lançamentos, ledger append-only; abertura exposta por `POST /wallets` com token interno. |
| §7: cinco operações, regras de referência e compensação | Implementado na camada | `ProcessWager`, políticas do domínio e `process_wager_integration_test.go`; incluindo LOSS sem movimento e insuficiência de reversão com código distinto; agora exercitadas também por HTTP autenticado. |
| §§5, 9: idempotência persistente e replay histórico | Implementado na camada | Hash, índices únicos, conflito e resultado original testados no caso de uso com PostgreSQL e agora por HTTP (replay com `idempotentReplay:true` e saldo original, `409` nos dois conflitos). HTTP e SQS com mesma identidade (duas ordens e concorrência) exercitados com IdP/broker reais; reinicialização literal com pendências e kill -9 das réplicas reproduzidos em `scripts/fault.sh`. |
| §§5, 8: concorrência por carteira e ausência de lost updates | Implementado na camada | `FOR UPDATE`, versão; 80+80 e 50 duplicatas por goroutines, HTTP **e agora por três processos reais**: réplicas independentes da entrega Compose atrás do gateway (21/18/13 requisições por réplica, um único débito, 49 replays idempotentes, par 200/422 distinto, saldo e reconciliação exatos). |
| §15: entrega a partir de checkout limpo | Implementado na camada | `docker compose -f docker-compose.yml up --build` de checkout limpo (verificado após `down -v`): PostgreSQL, Keycloak e LocalStack saudáveis, job `migrate` (golang-migrate) aplicando o esquema, três réplicas api atrás do gateway nginx em loopback (gateway 9080 / IdP 59080). `scripts/smoke.sh` reproduz tokens reais → abertura → 50 duplicatas → conflito 200/422 → saldo 10.00 → reconciliação `consistent:true`. Credenciais fictícias (jungle/jungle-local, chaves de desenvolvimento), nada publicado fora de loopback. |
| §§5, 11: atomicidade financeira e eventos | Implementado na camada | Carteira, transação, ledger e outbox compartilham commit; erro SQL intermediário confirma rollback integral. Inbox compartilha o commit, com rollback real por falha SQL testado. Publicação pós-commit com claim/lease e failpoint publish/ack verificados com LocalStack e PostgreSQL reais; kill -9 das réplicas com eventos não confirmados drena o backlog pós-reinício (`scripts/fault.sh`, Teste F). |
| §§6.3, 7: referência ausente, backoff e limite de espera | Implementado na camada | `PENDING_REFERENCE` persistido, exposto por HTTP (`202` sem saldo inventado) e retomado pelo worker distribuído: claim `FOR UPDATE SKIP LOCKED`, backoff exponencial 1s→5min em `attempt_count`/`next_attempt_at`, TTL `REFERENCE_TTL` (default 24h) com rejeição `REFERENCE_NOT_FOUND` e evento. Chegada posterior resolve; falha transitória rola back o claim; dois workers em goroutines não duplicam (PostgreSQL real; três processos comprovação via réplicas). O Teste H de `scripts/fault.sh` conclui um `PENDING_REFERENCE` HTTP com a referência arriving pelo SQS (cruzamento de canais) após reinício. |
| §2: IdP externo, autenticação e isolamento por provedor | Implementado na camada | Keycloak real (realm `jungle`, `client_credentials`), `oidc.Verifier` com JWKS/RS256/iss/aud/exp/nbf/typ/azp; roles `provider`/`wallet:manage`/`wallet:read`; `provider_id` do token nunca do body. Integração HTTP+IdP+PG real: token ausente/malformado/tampered/audience errada/expirado → `401`; sem role → `403`; isolamento em reads/replays/conflitos sem efeitos financeiros. SQS valida HMAC por provedor antes do caso de uso; assinatura ausente/forjada ou corpo de outro provedor vai à DLQ. Broker local não comprova IAM de produção. |
| §9: operações e consultas HTTP | Implementado na camada | Nove rotas oficiais expostas reutilizando `CreateWallet`/`ProcessWager`/queries; dinheiro em string, `Idempotency-Key` respeitada, respostas distinguíveis (400/401/403/404/409/422/202/503) documentadas no README e verificadas por integração real. Ver tabela abaixo. |
| §9: reconciliação em visão consistente | Implementado na camada | `ReconcileWallet`, snapshot SQL, `query_integration_test.go` e endpoint interno verificado na integração HTTP e no smoke; divergência conta em `jungle_wallet_reconciliation_divergences_total`. Nunca corrige saldo automaticamente. |
| §9: health público | Implementado na camada | `/health/live` e `/health/ready` públicos; ping PostgreSQL + três filas SQS. Integração remove fila real e verifica 503, sem detalhes internos. |
| §§2, 10: SQS, FIFO, DLQ, redrive e controle de acesso | Implementado na camada | LocalStack Community 4.14.0 sem conta/token; três filas FIFO provisionadas pelo job administrativo antes do startup; redrive após três recebimentos, retry 1s/2s/4s, HMAC vinculado ao provedor. Integrações reais de DLQ, tentativas e assinatura. Deduplicação financeira permanece no PostgreSQL. |
| §§6.5, 10: inbox e consumidor transacional | Implementado na camada | `ConsumeWagerMessage` reutiliza `ProcessWager` e sua transação; SHA-256 dos bytes do envelope, conclusão no mesmo commit. Erro SQL na inbox reverte domínio/ledger/outbox; duplicata, identidade reescrita, perda da resposta de commit e reentrega testadas com broker/PG reais. Interrupção literal pendente. |
| §11: publisher concorrente e recuperação da outbox | Implementado na camada | Publicação pós-commit: claim `SKIP LOCKED` com lease por token, backoff 1s→5min, ack/reschedule condicionados ao token, lease abandonado recuperado (`attempts=2`), republicação com mesmo `eventId` e payload do snapshot, dois/quatro publishers concorrentes sem duplicação, tudo com LocalStack FIFO e PostgreSQL reais (failpoint determinístico para o crash) e kill -9 das réplicas drenando o backlog pós-reinício (`scripts/fault.sh`). |
| §§4, 10: shutdown e kills de workers | Implementado na camada | Lifecycle HTTP/pool/OIDC, worker de referências com cancelamento observável e lote em andamento commitando ou rolando back; consumidor SQS e publisher da outbox canceláveis. **Kills literais verificados**: `docker compose kill -s KILL api` nas três réplicas com aposta SQS em voo (reentrega sem débito duplicado), com eventos não confirmados na outbox (drenagem pós-reinício com mesmos eventIds) e reinício com PENDING_REFERENCE retomada (Testes E/F/H por `scripts/fault.sh`). |
| §12: observabilidade | Implementado na camada | Logs JSON (`slog`) com correlationID por request e, no consumidor, messageId/transactionId/status/replay; `GET /metrics` expõe registro Prometheus dedicado: `jungle_wager_operations_total{kind,status}`, `idempotent_replays`, `idempotency_conflicts`, `processing_duration_seconds{kind}` (histograma), `db_retries_total{reason}`, `sqs_redeliveries_total`, `dlq_total`, `wallet_reconciliations_total`, `reconciliation_divergences_total` e o conjunto `jungle_outbox_*` (pendência/atraso sempre do banco, counters por processo). Sem labels de alta cardinalidade; verificadas no Compose de entrega com operações e replay reais. |
| §§13, 15: migrations e ambiente reproduzível | Implementado na camada | Runner operacional `cmd/migrate` verificado com PostgreSQL real; `compose.dev.yaml` para testes e `docker-compose.yml` de entrega (app + migrate job + IdP + filas + gateway) subindo de checkout limpo, verificado nesta entrega com `docker compose up --build`; filas provisionadas pelo comando administrativo; suíte de falhas (`scripts/smoke.sh`, `scripts/fault.sh`) contra o ambiente de entrega. |
| §13: integração real e falhas | Implementado na camada | PostgreSQL, Keycloak e LocalStack reais: HTTP/SQS, inbox, retry/DLQ, reentrega, falhas injetadas de commit, falha publish/ack da outbox, três processos independentes (smoke) e kills literais com reinício/pendência (fault.sh). |
| §15 (complemento): mensagem assinada reproduzível | Implementado na camada | `scripts/fault.sh` publica envelope assinado com `cmd/send-wager` no broker do Compose de entrega e valida consumo/reentrega após kill -9; smoke cobre a rota HTTP end-to-end. |

## Contratos HTTP que devem ser entregues

| Método e rota oficial | Acesso | Estado atual |
| --- | --- | --- |
| `POST /wallets` | Serviço interno autorizado | Implementado (`wallet:manage`); integração real com 201/403 |
| `GET /wallets/:walletId` | Serviço interno autorizado | Implementado (`wallet:read`); integração real |
| `GET /wallets/:walletId/ledger?cursor=...&limit=50` | Serviço interno autorizado | Implementado (`wallet:read`) com cursor validado; integração real |
| `POST /wallets/:walletId/reconciliation` | Serviço interno autorizado | Implementado (`wallet:manage`); divergência conta em `jungle_wallet_reconciliation_divergences_total` |
| `POST /wagering/transactions` | Provedor autenticado correspondente | Implementado (role `provider` + `provider_id` do token); 200/202/422/409/400/401/403 verificados com IdP real |
| `GET /wagering/transactions/:transactionId` | Provedor do registro | Implementado com filtro de provedor; `404` fora do escopo sem vazar existência |
| `GET /providers/:providerId/wagering/transactions/:externalTransactionId` | Provedor correspondente | Implementado; divergência de rota vs identidade é `403` antes da consulta |
| `GET /health/live` | Público | Implementado |
| `GET /health/ready` | Público | Implementado com ping PostgreSQL e consultas às três filas SQS; 503 por fila removida |

O `providerId` autorizado vem da identidade validada; body, rota e chave de
idempotência não concedem acesso — inclusive em respostas de replay, conflito e
recurso inexistente. Isso está verificado por testes com Keycloak real.

## Critérios para fechar o próximo incremento

Fase 6: fechamento para entrega final.

1. Revisão final de eliminatórias, endpoints, `.env.example` e secrets.
2. Rerun completo das verificações (gofmt/vet/unitários `-race`/integração `-race`) + smoke + faults a partir da entrega final.

### Revisão final da entrega (12/10/2026)

Auditoria seção por seção contra o snapshot oficial: eliminatórias do `14`
todas com evidência na tabela acima (autenticação/autorização reais, sem ponto
flutuante monetário, sem saldo negativo, sem duplicidade, idempotência em
banco, sem dependência de instância única, publicação após commit, ledger
append-only e uso de infraestrutura real); stack obrigatória confirmada (Go
`1.27.2` em `go.mod`/Dockerfile, Fx, `net/http`, Keycloak, PostgreSQL, SQS/
LocalStack, Compose e migrations com reversão); observabilidade completa
(logs exigidos e métricas por status/duplicatas/retries/DLQ/conflitos/atraso/
latência/divergência); verificação obrigatória do §13 coberta (unitários,
integração real, autenticação, 1–8 de concorrência e recuperação via smoke +
fault.sh, incluindo três processos e cruzamento HTTP/SQS com reinício).

Reprodução limpa desta revisão: `docker compose down -v` + `docker compose up
--build` → smoke OK e faults OK; `gofmt` limpo, `go vet ./...` e
`go vet -tags=integration ./...` limpos, `go test -race ./...` (14 pacotes) e
`go test -race -tags=integration ./...` sem falhas; `make build` OK.
`.env.example` cobre todas as variáveis de config; nenhuma credencial real.
Seguem opcionais e explicitamente não entregues: partidas dobradas, tracing,
dashboards e testes de carga.

## Decisões permitidas e limites de aceitação

- BRL-only é permitido pelo §6.1; o domínio carrega moeda e testa incompatibilidades.
- `pgx`, SQL explícito, locking pessimista e arquitetura hexagonal atendem às opções do
  enunciado. O domínio permanece independente de infraestrutura.
- A política de compensação direta única está documentada na [ADR 0001](adr/0001-compensacao-direta.md),
  conforme a exigência de explicar combinações de REFUND e ROLLBACK sem devolução dupla.
- Partidas dobradas, tracing e carga são opcionais. Não precedem obrigações pendentes.
- Nenhum requisito eliminatório do §14 pode ficar pendente na entrega final. A existência
  de testes locais não comprova autorização, funcionamento distribuído ou recuperação.

## Verificação desta revisão

O snapshot do enunciado e o README do clone local têm o mesmo SHA-256. Foram
inspecionados requisitos, implementação e testes existentes; `go vet ./...`,
`go vet -tags=integration ./...`, `go test ./...` e `go test -race ./...` passaram.
Integração executada nesta revisão contra PostgreSQL 17 e Keycloak 26.8 reais
(`compose.dev.yaml`): `go test -race -tags=integration ./... -count=1` passou em
todos os pacotes — suíte HTTP autenticada (tokens `client_credentials` reais,
401/403/404/409/422/202/400, isolamento entre provedores, token expirado do IdP,
disputa 80+80 por HTTP, readiness) e suíte do worker de referências (reagenda,
resolve após chegada da referência, rejeita por TTL, dois workers concorrentes
sem duplicação, falha transitária com claim devolvido). A verificação do publisher
da outbox está detalhada abaixo; três processos continuam sem evidência de execução.

### Verificação do publisher da outbox (12/10/2026)

Integração com PostgreSQL 17 e LocalStack reais, `-race`, schema e filas isolados
por teste: claim exclusivo com incremento de tentativas; token alheio não
confirma nem reagenda (`ErrOutboxLeaseLost`); ack próprio zera a pendência;
lease abandonado é recuperado com `attempts=2` e não é liberado pelo token
antigo; reschedule grava `PUBLISH_FAILED` e respeita o backoff. End-to-end com
fila FIFO real: evento comprometido entregue com corpo idêntico ao snapshot do
banco, `MessageDeduplicationId=eventId`, `MessageGroupId=aggregateId`; crash
simulado entre envio e confirmação é recuperado com o mesmo `eventId` e payload
(a janela de deduplicação FIFO suprime a repetição — a identidade estável foi
comprovada); quatro publishers concorrentes entregam dois eventos uma única vez
cada. Kill literal de processo e três instâncias seguem pendentes (Fase 6).

### Revisão do backoff de referências (12/10/2026)

O backoff limita o expoente antes do deslocamento de bits: 1s, 2s, …, 256s e teto de 5min a partir da décima tentativa. Isso evita overflow de `time.Duration` em pendências duradouras; TTL e regras de reagendamento permanecem iguais. Teste cobre limites, 1.000 tentativas e o maior `int` da plataforma.


### Verificação do consumidor SQS (12/10/2026)

`make check`, `go vet -tags=integration ./...`, validação do Compose e
`go test -race -tags=integration ./... -count=1` passaram. Infraestrutura:
PostgreSQL 17 (banco de verificação separado), Keycloak 26.8.0 e LocalStack
Community 4.14.0 em containers reais; nenhum caso de integração ignorado por
falta de ambiente (pacotes sem arquivos de teste não contam como casos).

- Mesmo envelope reentregue com outro dedup ID: inbox única, débito único.
- Mesma aposta por HTTP autenticado e SQS, duas ordens e concorrência: saldo 75,
  versão 2, ledger reconciliado, dois lançamentos e quatro eventos (incluindo abertura).
- CHECK da inbox falha após escritas financeiras: rollback integral; exatamente
  três execuções antes do redrive real à DLQ.
- Commit real concluído seguido de erro injetado (resultado desconhecido) ou
  falha antes de delete: reentrega efetiva, replay e ausência de novo débito.
- Envelope inválido, assinatura ausente, provedor divergente, OPENING e valor
  inválido: DLQ sem efeitos; rejeição financeira terminal conclui inbox sem DLQ.
- Reescrita de `messageId`: rollback da nova operação e DLQ.
- Shutdown durante SQL ativo: rollback e receipt imediatamente disponível.
- Provisionamento corrige redrive existente; readiness retorna 503 ao remover fila.

Nota de fechamento (13/10/2026): a publicação efetiva dos eventos, os publishers
múltiplos (A/B de workers), as métricas exigidas, a execução com três processos
reais, as interrupções/reinícios literais (`scripts/fault.sh`) e o Compose de
entrega reproduzível estão implementados e evidenciados nas seções acima.

### A01 — progresso da configuração de credenciais (13/10/2026)

O cliente deixou de substituir a cadeia AWS por `test/test`. O par fictício
agora é explícito apenas nos exemplos/ambiente local. Testes do adaptador
comprovam resolução por ambiente, perfil e endpoint de role de container,
inclusão do token temporário e nenhum envio SQS quando não há credenciais.
Esses testes usam servidores HTTP de teste para isolar a seleção da identidade;
não equivalem à validação IAM em infraestrutura real.

**Não encerra A01 nem A02/A03.** A autorização do broker exige evidência real
de rejeição de chamadas sem assinatura, com assinatura inválida e de ações
fora das permissões do produtor. As evidências antigas de conformidade total
continuam condicionadas à auditoria independente.

Validação deste incremento: vet, testes com `-race`, build e integração real
completa aprovados (606 casos/subtestes, zero falhas/ignores). A primeira
execução expôs a precondição de lease de 50 ms; incorporada a correção local
com expiração controlada no banco e 20 repetições aprovadas.
[Evidências, comandos e limites](audit/2026-10-13/sqs-credentials.md).

### Checkpoint das correções locais — 13/10/2026

As alterações de carga, métricas e instrumentação de falhas foram versionadas
por solicitação do usuário, sem declarar fechamento da auditoria. Políticas
IAM são exemplos não aplicados; perda de eventos no restart do broker local
continua documentada. Pausas configuráveis não comprovam sozinhas as janelas
exigidas: falta verificar commit/delete e publish/ack do evento-alvo, reentrega
e tentativas. O callback `OnConclusion` ainda não está conectado no bootstrap
e é chamado dentro da transação; não comprova contagem de conclusões duráveis.
Soak e scripts de kill não foram reexecutados neste checkpoint.

Verificações do checkpoint: `make check`, vet com tag integration, build,
Compose, shell, documentação e sondas k6 aprovados (sonda negativa: exit 99
esperado); `govulncheck@v1.9.0`: nenhuma vulnerabilidade encontrada. Integração
com PostgreSQL, Keycloak e LocalStack reais, `-race -count=1`: 606
casos/subtestes aprovados, zero falhas e zero casos ignorados.

### Revisão atual — 14/10/2026

[A matriz final de entrega](AUDITORIA_FINAL_ENTREGA.md) passa a concentrar o
estado corrente. A01 permanece parcial: templates IAM revisados contra as
chamadas, mas não aplicados. No incremento de retomada, o provisionamento foi
separado do runtime e consumidor/publisher passaram a usar clientes e perfis
independentes. Controle HMAC não equivale a autorização SQS.

A03: E/F/H agora comprovados com SIGKILL nos pontos bloqueados e comparação de
identidades/digest. A07: lock real, relógio defasado e TTL pós-lock passam. A09:
dois ciclos Fx reais passam sem collectors retendo banco encerrado. A11:
conclusão observada pós-commit, replay separado e falha de DLQ distinta.
[Evidências desta rodada](audit/2026-10-14/README.md).

A04–A06/A08/A10: instrumentos, regressões e resultados atuais em
[PERFORMANCE_REPORT.md](PERFORMANCE_REPORT.md) e [evidências](audit/2026-10-14/README.md).
A02 segue **não atendido no restart do broker**: 14 mensagens aceitas perdidas;
publicações pendentes durante indisponibilidade temporária se recuperam.
A01: envio anônimo aceito mesmo com política Deny e ENFORCE_IAM=1.

Retomada do provisionamento: runtime sem criação/reparo de filas, perfis
independentes e job administrativo validados; integração real com race:
635 testes/subtestes, zero falhas/skips. Smoke aprovado em três APIs. Novo
restart confirmou perda de 14 mensagens aceitas, portanto A02 permanece aberto.
[Evidências deste incremento](audit/2026-10-14/sqs-runtime.md).

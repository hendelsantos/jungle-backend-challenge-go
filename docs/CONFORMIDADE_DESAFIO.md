# Conformidade com o desafio oficial

Fonte normativa: [README original preservado](reference/README_DESAFIO.md), commit
`bdd9e70b65db9b374ee070bd3a4768661a888997`; [procedência](reference/SOURCE.md).
Revisão de 12/10/2026 sobre a implementação local das Fases 3, 4 e primeiro incremento da Fase 5.

**A entrega completa ainda não está pronta.** O plano define a ordem dos incrementos;
o enunciado define o comportamento e as evidências exigidas. Uma fase interna concluída
não significa que o requisito integrado correspondente foi demonstrado.

## Requisitos e evidências

“Implementado na camada” descreve código e testes existentes nessa camada. “Parcial”
significa que falta uma parte do requisito ou sua demonstração no ambiente exigido.
Os caminhos de testes são reproduzíveis pelos comandos do [README](../README.md).

| Exigência oficial | Situação | Implementação/evidência e trabalho restante |
| --- | --- | --- |
| §4: Go, módulos, Fx e domínio independente | Implementado na camada | `go.mod`, `Dockerfile`, `internal/bootstrap` e `internal/domain`; testes de composição e lifecycle. Handlers financeiros e `securityModule`/`databaseModule` na composição Fx; consumidor SQS também composto com Fx; publisher pendente. |
| §6.1: dinheiro exato, escala, moedas e overflow | Implementado na camada | `internal/domain/money`, testes e fuzzing; centavos `int64`, JSON monetário em string. Decoder HTTP rejeita números/`null` na fronteira — nenhum caminho passa por `float64`. |
| §§5–6: carteira, estados, abertura e ledger | Implementado na camada | Domínio, `CreateWallet`, migrations e `create_wallet_integration_test.go`; abertura positiva atômica, zero sem lançamentos, ledger append-only; abertura exposta por `POST /wallets` com token interno. |
| §7: cinco operações, regras de referência e compensação | Implementado na camada | `ProcessWager`, políticas do domínio e `process_wager_integration_test.go`; incluindo LOSS sem movimento e insuficiência de reversão com código distinto; agora exercitadas também por HTTP autenticado. |
| §§5, 9: idempotência persistente e replay histórico | Implementado na camada | Hash, índices únicos, conflito e resultado original testados no caso de uso com PostgreSQL e agora por HTTP (replay com `idempotentReplay:true` e saldo original, `409` nos dois conflitos). HTTP e SQS com mesma identidade (duas ordens e concorrência) exercitados com IdP/broker reais. Falta reinicialização literal de processos. |
| §§5, 8: concorrência por carteira e ausência de lost updates | Parcial | `FOR UPDATE`, versão, testes 80+80 (goroutines e HTTP), 50 duplicatas e carteiras independentes com conexões reais. Falta repetir com pelo menos três processos independentes. |
| §§5, 11: atomicidade financeira e eventos | Implementado na camada | Carteira, transação, ledger e outbox compartilham commit; erro SQL intermediário confirma rollback integral. Inbox compartilha o commit, com rollback real por falha SQL testado. Publicação pós-commit com claim/lease e failpoint publish/ack verificados com LocalStack e PostgreSQL reais. Falta kill literal de processos. |
| §§6.3, 7: referência ausente, backoff e limite de espera | Implementado na camada | `PENDING_REFERENCE` persistido, exposto por HTTP (`202` sem saldo inventado) e retomado pelo worker distribuído: claim `FOR UPDATE SKIP LOCKED`, backoff exponencial 1s→5min em `attempt_count`/`next_attempt_at`, TTL `REFERENCE_TTL` (default 24h) com rejeição `REFERENCE_NOT_FOUND` e evento. Chegada posterior resolve; falha transitória rola back o claim; dois workers em goroutines não duplicam (PostgreSQL real; não são dois processos). Falta a retomada cruzada com o consumidor SQS (Fase 5). |
| §2: IdP externo, autenticação e isolamento por provedor | Implementado na camada | Keycloak real (realm `jungle`, `client_credentials`), `oidc.Verifier` com JWKS/RS256/iss/aud/exp/nbf/typ/azp; roles `provider`/`wallet:manage`/`wallet:read`; `provider_id` do token nunca do body. Integração HTTP+IdP+PG real: token ausente/malformado/tampered/audience errada/expirado → `401`; sem role → `403`; isolamento em reads/replays/conflitos sem efeitos financeiros. SQS valida HMAC por provedor antes do caso de uso; assinatura ausente/forjada ou corpo de outro provedor vai à DLQ. Broker local não comprova IAM de produção. |
| §9: operações e consultas HTTP | Implementado na camada | Nove rotas oficiais expostas reutilizando `CreateWallet`/`ProcessWager`/queries; dinheiro em string, `Idempotency-Key` respeitada, respostas distinguíveis (400/401/403/404/409/422/202/503) documentadas no README e verificadas por integração real. Ver tabela abaixo. |
| §9: reconciliação em visão consistente | Parcial | `ReconcileWallet`, snapshot SQL, `query_integration_test.go` e endpoint interno verificado na integração HTTP (consistent=true). Falta métrica de divergência; log existe. Nunca corrige saldo automaticamente. |
| §9: health público | Implementado na camada | `/health/live` e `/health/ready` públicos; ping PostgreSQL + três filas SQS. Integração remove fila real e verifica 503, sem detalhes internos. |
| §§2, 10: SQS, FIFO, DLQ, redrive e controle de acesso | Implementado na camada | LocalStack Community 4.14.0 sem conta/token; três filas FIFO provisionadas no startup; redrive após três recebimentos, retry 1s/2s/4s, HMAC vinculado ao provedor. Integrações reais de DLQ, tentativas e assinatura. Deduplicação financeira permanece no PostgreSQL. |
| §§6.5, 10: inbox e consumidor transacional | Implementado na camada | `ConsumeWagerMessage` reutiliza `ProcessWager` e sua transação; SHA-256 dos bytes do envelope, conclusão no mesmo commit. Erro SQL na inbox reverte domínio/ledger/outbox; duplicata, identidade reescrita, perda da resposta de commit e reentrega testadas com broker/PG reais. Interrupção literal pendente. |
| §11: publisher concorrente e recuperação da outbox | Implementado na camada | Publicação pós-commit: claim `SKIP LOCKED` com lease por token, backoff 1s→5min, ack/reschedule condicionados ao token, lease abandonado recuperado (`attempts=2`), republicação com mesmo `eventId` e payload do snapshot, dois/quatro publishers concorrentes sem duplicação, tudo com LocalStack FIFO e PostgreSQL reais (failpoint determinístico para o crash). Falta kill literal do processo. |
| §§4, 10: shutdown de workers | Parcial | Lifecycle HTTP/pool/OIDC e worker de referências com cancelamento observável (polls param; lote em andamento commita ou rola back; suíte HTTP sobe/derruba a app com worker). Consumidor SQS e publisher da outbox também canceláveis: publisher cancela I/O e libera leases conhecidos com cleanup limitado. Falta kill literal de processos. |
| §12: observabilidade | Parcial | Logs JSON de bootstrap/HTTP e correlation ID por request com echo no response; métricas `jungle_outbox_*` (pendência, atraso, claims, publicações, falhas, retries, leases perdidas) expostas em `/metrics`, gauges consultados no banco (query falha → 503). Faltam rastreabilidade completa das operações (messageId, transactionId) e métricas financeiras exigidas (operações, replays, conflitos, duração, DLQ, divergências). |
| §§4, 13, 15: migrations e ambiente reproduzível | Parcial | SQL `up/down` e harness de testes existem; `compose.dev.yaml` sobe PostgreSQL+Keycloak+LocalStack; realm importado e filas provisionadas pelo app. Faltam runner operacional e Compose de entrega com app/migrations. |
| §13: integração real e falhas | Parcial | PostgreSQL, Keycloak e LocalStack reais: HTTP/SQS, inbox, retry/DLQ, reentrega, falhas injetadas de commit e falha publish/ack da outbox. Faltam três processos, interrupções literais e reinício. |
| §15: entrega a partir de checkout limpo | Pendente | Reproduzir `docker compose up --build`, tokens, chamadas HTTP, mensagens, testes e falhas usando apenas instruções versionadas. |

## Contratos HTTP que devem ser entregues

| Método e rota oficial | Acesso | Estado atual |
| --- | --- | --- |
| `POST /wallets` | Serviço interno autorizado | Implementado (`wallet:manage`); integração real com 201/403 |
| `GET /wallets/:walletId` | Serviço interno autorizado | Implementado (`wallet:read`); integração real |
| `GET /wallets/:walletId/ledger?cursor=...&limit=50` | Serviço interno autorizado | Implementado (`wallet:read`) com cursor validado; integração real |
| `POST /wallets/:walletId/reconciliation` | Serviço interno autorizado | Implementado (`wallet:manage`); métrica de divergência pendente |
| `POST /wagering/transactions` | Provedor autenticado correspondente | Implementado (role `provider` + `provider_id` do token); 200/202/422/409/400/401/403 verificados com IdP real |
| `GET /wagering/transactions/:transactionId` | Provedor do registro | Implementado com filtro de provedor; `404` fora do escopo sem vazar existência |
| `GET /providers/:providerId/wagering/transactions/:externalTransactionId` | Provedor correspondente | Implementado; divergência de rota vs identidade é `403` antes da consulta |
| `GET /health/live` | Público | Implementado |
| `GET /health/ready` | Público | Implementado com ping PostgreSQL e consultas às três filas SQS; 503 por fila removida |

O `providerId` autorizado vem da identidade validada; body, rota e chave de
idempotência não concedem acesso — inclusive em respostas de replay, conflito e
recurso inexistente. Isso está verificado por testes com Keycloak real.

## Critérios para fechar o próximo incremento

Fase 6: Compose de entrega e três processos independentes.

1. Runner de migrations operacional e Compose de entrega reproduzível a partir de checkout limpo (app + IdP + PostgreSQL + filas + gateway).
2. Kill literal de processos nos pontos críticos (consumidor após commit antes do delete; publisher após envio antes do ack) e reinício com pendências retomadas.
3. Três processos independentes com pools próprios, paralelismo por carteira e reconciliação final; métricas financeiras restantes.

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

Ainda faltam publicação efetiva dos eventos, dois publishers, métricas, execução
com três processos, interrupção/reinício literal e reprodução do Compose final.

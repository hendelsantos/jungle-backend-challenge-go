# Conformidade com o desafio oficial

Fonte normativa: [README original preservado](reference/README_DESAFIO.md), commit
`bdd9e70b65db9b374ee070bd3a4768661a888997`; [procedência](reference/SOURCE.md).
Revisão de 12/10/2026 sobre a implementação local das Fases 3 e 4.

**A entrega completa ainda não está pronta.** O plano define a ordem dos incrementos;
o enunciado define o comportamento e as evidências exigidas. Uma fase interna concluída
não significa que o requisito integrado correspondente foi demonstrado.

## Requisitos e evidências

“Implementado na camada” descreve código e testes existentes nessa camada. “Parcial”
significa que falta uma parte do requisito ou sua demonstração no ambiente exigido.
Os caminhos de testes são reproduzíveis pelos comandos do [README](../README.md).

| Exigência oficial | Situação | Implementação/evidência e trabalho restante |
| --- | --- | --- |
| §4: Go, módulos, Fx e domínio independente | Implementado na camada | `go.mod`, `Dockerfile`, `internal/bootstrap` e `internal/domain`; testes de composição e lifecycle. Handlers financeiros e `securityModule`/`databaseModule` na composição Fx; workers SQS ainda não. |
| §6.1: dinheiro exato, escala, moedas e overflow | Implementado na camada | `internal/domain/money`, testes e fuzzing; centavos `int64`, JSON monetário em string. Decoder HTTP rejeita números/`null` na fronteira — nenhum caminho passa por `float64`. |
| §§5–6: carteira, estados, abertura e ledger | Implementado na camada | Domínio, `CreateWallet`, migrations e `create_wallet_integration_test.go`; abertura positiva atômica, zero sem lançamentos, ledger append-only; abertura exposta por `POST /wallets` com token interno. |
| §7: cinco operações, regras de referência e compensação | Implementado na camada | `ProcessWager`, políticas do domínio e `process_wager_integration_test.go`; incluindo LOSS sem movimento e insuficiência de reversão com código distinto; agora exercitadas também por HTTP autenticado. |
| §§5, 9: idempotência persistente e replay histórico | Implementado na camada | Hash, índices únicos, conflito e resultado original testados no caso de uso com PostgreSQL e agora por HTTP (replay com `idempotentReplay:true` e saldo original, `409` nos dois conflitos). Falta SQS e reinicialização de processos. |
| §§5, 8: concorrência por carteira e ausência de lost updates | Parcial | `FOR UPDATE`, versão, testes 80+80 (goroutines e HTTP), 50 duplicatas e carteiras independentes com conexões reais. Falta repetir com pelo menos três processos independentes. |
| §§5, 11: atomicidade financeira e eventos | Parcial | Carteira, transação, ledger e outbox compartilham commit; erro SQL intermediário confirma rollback integral. Inbox e publicação após commit ainda pendentes. |
| §§6.3, 7: referência ausente, backoff e limite de espera | Implementado na camada | `PENDING_REFERENCE` persistido, exposto por HTTP (`202` sem saldo inventado) e retomado pelo worker distribuído: claim `FOR UPDATE SKIP LOCKED`, backoff exponencial 1s→5min em `attempt_count`/`next_attempt_at`, TTL `REFERENCE_TTL` (default 24h) com rejeição `REFERENCE_NOT_FOUND` e evento. Chegada posterior resolve; falha transitória rola back o claim; duas instâncias não duplicam (testes com PostgreSQL real). Falta a retomada cruzada com o consumidor SQS (Fase 5). |
| §2: IdP externo, autenticação e isolamento por provedor | Implementado na camada | Keycloak real (realm `jungle`, `client_credentials`), `oidc.Verifier` com JWKS/RS256/iss/aud/exp/nbf/typ/azp; roles `provider`/`wallet:manage`/`wallet:read`; `provider_id` do token nunca do body. Integração HTTP+IdP+PG real: token ausente/malformado/tampered/audience errada/expirado → `401`; sem role → `403`; isolamento em reads/replays/conflitos sem efeitos financeiros. Falta aplicá-lo também ao consumidor SQS (Fase 5). |
| §9: operações e consultas HTTP | Implementado na camada | Nove rotas oficiais expostas reutilizando `CreateWallet`/`ProcessWager`/queries; dinheiro em string, `Idempotency-Key` respeitada, respostas distinguíveis (400/401/403/404/409/422/202/503) documentadas no README e verificadas por integração real. Ver tabela abaixo. |
| §9: reconciliação em visão consistente | Parcial | `ReconcileWallet`, snapshot SQL, `query_integration_test.go` e endpoint interno verificado na integração HTTP (consistent=true). Falta métrica de divergência; log existe. Nunca corrige saldo automaticamente. |
| §9: health público | Parcial | `/health/live` e `/health/ready` (ping PostgreSQL, público, sem detalhes internos) implementados e testados. Falta o check de SQS no ready com o módulo de mensageria. |
| §§2, 10: SQS, FIFO, DLQ, redrive e controle de acesso | Pendente | Provisionar LocalStack/MiniStack, credenciais/políticas e fronteira de confiança do produtor. Deduplicação FIFO não substitui a do banco. |
| §§6.5, 10: inbox e consumidor transacional | Parcial | Tabela de inbox existe; faltam processamento, hash da mensagem, retry/DLQ e remoção da fila somente após commit. |
| §11: publisher concorrente e recuperação da outbox | Parcial | Tabela e eventos tipados persistidos; faltam publicação, claim/lease, backoff, recuperação e republicação mantendo `eventId`. |
| §§4, 10: shutdown de workers | Parcial | Lifecycle HTTP/pool/OIDC e worker de referências com cancelamento observável (polls param; lote em andamento commita ou rola back; suíte HTTP sobe/derruba a app com worker). Falta o mesmo para o consumidor SQS e publisher (Fase 5). |
| §12: observabilidade | Parcial | Logs JSON de bootstrap/HTTP e correlation ID por request com echo no response. Faltam rastreabilidade completa das operações (messageId, transactionId), métricas exigidas e diagnóstico de retries, DLQ e atraso da outbox. |
| §§4, 13, 15: migrations e ambiente reproduzível | Parcial | SQL `up/down` e harness de testes existem; `compose.dev.yaml` sobe PostgreSQL+Keycloak com realm importado. Faltam runner operacional, Compose de entrega com app/filas, LocalStack e instruções completas de reversão. |
| §13: integração real e falhas | Parcial | PostgreSQL e Keycloak reais em uso (HTTP autenticado verificado). Faltam SQS real, três processos, interrupções após commit/publicação, reentrega e reinicializações. |
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
| `GET /health/ready` | Público | Implementado com ping PostgreSQL; check de SQS pendente |

O `providerId` autorizado vem da identidade validada; body, rota e chave de
idempotência não concedem acesso — inclusive em respostas de replay, conflito e
recurso inexistente. Isso está verificado por testes com Keycloak real.

## Critérios para fechar o próximo incremento

Fase 5: SQS, inbox e outbox publisher.

1. LocalStack/MiniStack com `wager-transactions.fifo`, DLQ com redrive e fila de eventos;
   consumidor compartilhando `ProcessWager` com inbox concluída no mesmo commit;
   `DeleteMessage` somente após commit; shutdown seguro do consumidor.
2. Publisher da outbox com claim/lease, backoff, recuperação de trabalho abandonado e
   republicação preservando `eventId`; incluir check de SQS no readiness.
3. Testes de falha entre commit/publicação/ack, reentrega, DLQ e cross-channel HTTP+SQS.

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
sem duplicação, falha transitária com claim devolvido). SQS, outbox publisher e
três processos continuam sem evidência de execução.

### Revisão do backoff de referências (12/10/2026)

O backoff limita o expoente antes do deslocamento de bits: 1s, 2s, …, 256s e teto de 5min a partir da décima tentativa. Isso evita overflow de `time.Duration` em pendências duradouras; TTL e regras de reagendamento permanecem iguais. Teste cobre limites, 1.000 tentativas e o maior `int` da plataforma.

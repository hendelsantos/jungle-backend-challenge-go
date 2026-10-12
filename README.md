# Jungle Backend Go

Solução em desenvolvimento para o [desafio de processamento distribuído de apostas](https://github.com/junglegaming/backend-challenge-go).

**Estado: Fases 0, 1 e 2 concluídas; Fase 3 concluída; Fase 4 (worker de referências) implementada e verificada com PostgreSQL real.** `ProcessWager` trata BET, WIN, LOSS, REFUND e ROLLBACK com lock por carteira, idempotência persistente e gravação de saldo, transação, ledger e outbox no mesmo commit. Os contratos HTTP oficiais estão expostos com autenticação OIDC (`client_credentials`), isolamento entre provedores e operações internas restritas; `GET /health/ready` verifica o PostgreSQL. Operações com referência ausente ficam `PENDING_REFERENCE` e um worker distribuído as retoma com backoff exponencial e TTL, concluindo, reagendando ou rejeitando com `REFERENCE_NOT_FOUND`.

Ainda faltam SQS/inbox/publisher, Compose de entrega completo, métricas e a demonstração com três processos independentes. O acompanhamento está em [PLANO_SISTEMA_JUNGLE_GO.md](PLANO_SISTEMA_JUNGLE_GO.md). Prazo interno: **14/10/2026**.

A [matriz de conformidade](docs/CONFORMIDADE_DESAFIO.md) relaciona as exigências oficiais à implementação, aos testes e às pendências. O README oficial prevalece sobre o plano interno; a entrega só estará concluída com os requisitos obrigatórios demonstrados no ambiente exigido.

## Executar localmente

Pré-requisitos: Go **1.27.2**, Git e Make. Docker é necessário para executar a imagem; Docker Compose será usado nas próximas fases. O Go pode baixar automaticamente a toolchain indicada em `go.mod` quando `GOTOOLCHAIN=auto`.

```sh
go mod download
make run
```

Em outro terminal:

```sh
curl --fail http://localhost:8080/health/live
# {"status":"ok"}
```

Encerre com `Ctrl+C` ou `SIGTERM`. O servidor aguarda requisições em andamento por até 10 segundos.

## Configuração

| Variável | Padrão | Valores |
| --- | --- | --- |
| `HTTP_ADDR` | `:8080` | Host e porta TCP entre 1 e 65535; exemplo `127.0.0.1:8081` |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `DATABASE_URL` | vazia | Connection string PostgreSQL (scheme `postgres://` ou DSN keyword/value). Vazia: a aplicação sobe apenas com health; definida: URL inválida ou banco inacessível impedem a inicialização |
| `OIDC_ISSUER` | vazia | URL pública do realm no IdP (ex.: `http://localhost:58080/realms/jungle`). Definida: habilita os endpoints de negócio e torna `DATABASE_URL` obrigatória |
| `OIDC_AUDIENCE` | vazia | Audience exigida nos access tokens (ex.: `jungle-api`). Obrigatória junto com `OIDC_ISSUER` |
| `REFERENCE_TTL` | `24h` | Prazo máximo de espera por uma referência ausente; esgotado, a operação é rejeitada com `REFERENCE_NOT_FOUND` |
| `REFERENCE_WORKER_INTERVAL` | `1s` | Intervalo de polling do worker de referências (ativo quando `DATABASE_URL` está definida) |

A aplicação lê variáveis do processo. `.env` não é carregado automaticamente. Para usar o exemplo local:

```sh
cp .env.example .env
set -a
. ./.env
set +a
make run
```

Configuração inválida ou porta ocupada impede a inicialização. `/health/live` indica que o processo responde; `/health/ready` (registrado quando há banco) faz um ping real no PostgreSQL com prazo curto. O check de SQS será acrescentado com o módulo de mensageria.

## Dependências locais de desenvolvimento

`compose.dev.yaml` sobre PostgreSQL 17 e Keycloak 26.8 com o realm `jungle` importado (dois provedores, serviço interno e clientes de teste negativos; segredos fictícios):

```sh
docker compose -f compose.dev.yaml up -d   # postgres :55432, keycloak :58080
```

O schema é aplicado pelo harness de testes; para a aplicação, use o `TEST_DATABASE_URL` do exemplo abaixo ou aplique as migrations no banco destino.

## API autenticada

Com `OIDC_ISSUER`/`OIDC_AUDIENCE` configurados, os endpoints de negócio exigem access tokens do IdP. Obtenha um token real por `client_credentials`:

```sh
TOKEN=$(curl -s http://localhost:58080/realms/jungle/protocol/openid-connect/token \
  -d grant_type=client_credentials \
  -d client_id=provider-a-client \
  -d client_secret=local-provider-a-client-secret | jq -r .access_token)
```

Fluxo completo (moeda sempre em string decimal; `Idempotency-Key` obrigatória no envio de operações):

```sh
# Serviço interno (wallet-internal-client) abre a carteira:
INTERNAL=$(curl -s http://localhost:58080/realms/jungle/protocol/openid-connect/token \
  -d grant_type=client_credentials -d client_id=wallet-internal-client \
  -d client_secret=local-wallet-internal-client-secret | jq -r .access_token)
curl -s -X POST http://localhost:8080/wallets -H "Authorization: Bearer $INTERNAL" \
  -H 'Content-Type: application/json' \
  -d '{"playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","initialBalance":{"amount":"100.00","currency":"BRL"}}'
# -> 201 {"id":...,"playerId":...,"balance":{"amount":"100.00","currency":"BRL"},"version":1}

# Provedor autenticado envia uma aposta:
curl -s -X POST http://localhost:8080/wagering/transactions \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: provider-a:transaction-123' \
  -d '{"providerId":"provider-a","externalTransactionId":"transaction-123","playerId":"...","walletId":"...","roundId":"round-987","gameId":"fortune-chimp","kind":"BET","money":{"amount":"25.00","currency":"BRL"}}'
# -> 200 {"transactionId":...,"status":"PROCESSED","balance":{"amount":"75.00","currency":"BRL"},"idempotentReplay":false}

# Consultas do provedor e leitura interna:
curl -s http://localhost:8080/wagering/transactions/<transactionId> -H "Authorization: Bearer $TOKEN"
curl -s http://localhost:8080/providers/provider-a/wagering/transactions/transaction-123 -H "Authorization: Bearer $TOKEN"
curl -s "http://localhost:8080/wallets/<walletId>/ledger?limit=50" -H "Authorization: Bearer $INTERNAL"
curl -s -X POST http://localhost:8080/wallets/<walletId>/reconciliation -H "Authorization: Bearer $INTERNAL"
```

O `providerId` autorizado vem do claim `provider_id` do token validado; corpo, rota ou chave que apontem para outro provedor recebem `403` sem efeito financeiro. Operações internas de carteira exigem os roles `wallet:manage`/`wallet:read` do serviço interno.

Respostas distinguíveis do contrato (corpo sempre traz `error.code` estável e `correlationId`):

| Situação | HTTP | Código |
| --- | --- | --- |
| JSON/valor/payload inválido, `OPENING` externo | 400 | `INVALID_REQUEST` / `INVALID_WALLET_SCOPE` |
| Token ausente, inválido, expirado ou audience errada | 401 | `UNAUTHENTICATED` |
| Role ausente, provedor divergente da identidade | 403 | `FORBIDDEN` |
| Recurso inexistente ou fora do escopo do provedor | 404 | `NOT_FOUND` |
| Chave/ID externo reutilizados com conteúdo divergente | 409 | `CONFLICT` |
| Rejeição definitiva de negócio (`INSUFFICIENT_FUNDS` etc.) | 422 | corpo com `failureCode` |
| Pendência de referência | 202 | sem `balance` inventado |
| Indisponibilidade transitória sem commit | 503 | `UNAVAILABLE`, repetir com a mesma identidade |

Erros de contrato e autorização nunca produzem efeito financeiro; a suíte de integração verifica saldo, transações, ledger e outbox inalterados nesses casos.

## Verificar e compilar

```sh
make check        # go vet, go test e go test -race
make fmt          # gofmt
make build        # bin/server
```

Os testes atuais verificam configuração, contrato de liveness, composição Fx, porta ocupada, liberação do listener, conclusão de requisições durante shutdown, gate do módulo de banco, invariantes de `Money`, `Wallet`, `WagerTransaction`, `WalletLedgerEntry` e eventos de domínio. Não são testes de integração financeira nem substituem os cenários obrigatórios com infraestrutura real.

Os testes de integração (transações do `UnitOfWork`, casos de uso `CreateWallet` e `ProcessWager`, leituras com cursor, reconciliação e proteções de schema) exigem PostgreSQL real (migrations aplicadas pelo próprio harness) e usam a build tag `integration` (pulam sem a variável de conexão; nenhum mock substitui o banco):

```sh
docker run -d --name jungle-pg -e POSTGRES_PASSWORD=verify -p 127.0.0.1:55432:5432 postgres:17-alpine
# Aguarde pg_isready indicar que o banco aceita conexões.
docker exec jungle-pg pg_isready
TEST_DATABASE_URL='postgres://postgres:verify@127.0.0.1:55432/postgres?sslmode=disable' \
  go test -race -tags=integration ./... -count=2
docker rm -f jungle-pg
```

A suíte HTTP autenticada (`internal/adapters/http`) adicionalmente exige Keycloak real com o realm importado; sem `TEST_OIDC_ISSUER` ela pula:

```sh
docker compose -f compose.dev.yaml up -d
TEST_OIDC_ISSUER='http://localhost:58080/realms/jungle' \
TEST_DATABASE_URL='postgres://jungle:jungle-local@127.0.0.1:55432/jungle?sslmode=disable' \
  go test -race -tags=integration ./internal/adapters/http/ -count=1 -v
```

Para exercitar parsing e aritmética monetária com entradas geradas automaticamente:

```sh
go test ./internal/domain/money -cover
go test ./internal/domain/money -run '^$' -fuzz '^FuzzArithmetic$' -fuzztime=10s -parallel=2
go test ./internal/domain/money -run '^$' -fuzz '^FuzzParseDecimal$' -fuzztime=10s -parallel=2
go test ./internal/domain/wallet -cover
go test ./internal/domain/wallet -run '^$' -fuzz '^FuzzMovementSequence$' -fuzztime=10s -parallel=2
go test ./internal/domain/wagering -cover
go test ./internal/domain/wagering -run '^$' -fuzz '^FuzzBetRefundRollback$' -fuzztime=10s -parallel=2
```

`Money` usa centavos em `int64` e recebe valores externos como `{"amount":"25.90","currency":"BRL"}`. As regras, limites e erros estão em [ARCHITECTURE.md](ARCHITECTURE.md#money--primeiro-incremento-da-fase-1).

`Wallet` mantém identidade, jogador, saldo, versão e timestamps em campos privados. Criação inicia na versão 1; reidratação preserva o estado persistido; crédito e débito validam moeda e limites antes de alterar o agregado. Rejeições e valores zero preservam saldo, versão e horário de atualização. A aplicação coordena escritas por `SELECT ... FOR UPDATE` e grava o ledger no mesmo commit; a demonstração com três processos continua pendente.

`WagerTransaction` implementa os estados e as regras de `BET`, `WIN`, `LOSS`, `REFUND`, `ROLLBACK` e abertura interna `OPENING`. O domínio avalia referências, impede reprocessamento de estados terminais e mantém o resultado original persistido para replay. A política de compensação está na [ADR 0001](docs/adr/0001-compensacao-direta.md). Idempotência durável e atomicidade SQL estão implementadas; retry de referências ainda está pendente.

`WalletLedgerEntry` é um tipo-valor imutável com um lançamento por movimentação efetiva: direção, quantia positiva, saldo anterior e posterior consistentes (`balanceAfter = balanceBefore ± amount`) e timestamp em UTC. Não há setters; criação e reidratação coincidem. Unicidade `(wallet_id, transaction_id)` e proteção contra edição/exclusão são impostas pelo schema PostgreSQL.

Os eventos tipados (`WagerTransactionProcessed`, `WagerTransactionRejected`, `WalletBalanceChanged`, `WagerTransactionPendingReference`) são envelopes imutáveis com `eventId`, `eventType`, `aggregateId` derivado dos dados, `correlationId`, `causationId` opcional, `occurredAt` em UTC, `version` por tipo e payload tipado; `MarshalJSON` fixa o contrato do payload da outbox com dinheiro em string decimal. A gravação na `outbox_events` é atômica com as operações financeiras; a publicação pós-commit via SQS ainda será implementada.

## Worker de referências pendentes

Com `DATABASE_URL` definida, todo processo executa um worker que retoma operações `PENDING_REFERENCE`:

- **Claim distribuído**: `SELECT ... FOR UPDATE SKIP LOCKED` sobre as linhas vencidas; múltiplas instâncias disputam sem bloquear nem duplicar.
- **Backoff exponencial** (1s → 5min) gravado em `attempt_count`/`next_attempt_at`; o prazo (`reference_deadline_at`, criação + `REFERENCE_TTL`) é fixado na primeira retomada.
- **Desfechos**: referência chegou e elegível → `PROCESSED` com saldo/ledger/eventos no mesmo commit; referência ainda pendente → reagendamento sem efeitos; referência terminal sem sucesso → `REJECTED` (`REFERENCE_NOT_PROCESSED`); prazo esgotado → `REJECTED` (`REFERENCE_NOT_FOUND`) com evento de rejeição, sem tocar a carteira.
- **Falha transitória**: rollback integral do claim deixa a pendência elegível de novo; nenhuma pendência depende da memória de um processo.
- **Shutdown**: `SIGTERM` cancela novos polls; o lote em andamento commita ou rola back, e o Fx encerra o worker antes de fechar o pool.

`WagerTransaction` continua expondo a consulta de pendências por `GET /wagering/transactions/:id`.

## ProcessWager: garantias verificadas

- Duas BETs de `80.00` sobre `100.00`: uma processada, uma rejeitada, saldo `20.00` e um débito.
- 50 chamadas concorrentes da mesma BET: uma operação, um débito e dois eventos; 49 replays.
- Replay retorna saldo/versão originais, mesmo após novas movimentações. Mesma chave com conteúdo diferente ou mesmo ID externo com outra chave gera conflito.
- LOSS e rejeições não geram ledger; REFUND/ROLLBACK concorrentes não compensam a mesma referência duas vezes.
- Erro SQL na segunda gravação da outbox desfaz carteira, transação, ledger e primeiro evento.
- Uma carteira progride enquanto outra está bloqueada; reconciliação confirma os saldos após os cenários concorrentes e de reversão.

Os testes usam infraestrutura real: a suíte HTTP valida tokens válidos, ausentes, malformados, com assinatura alterada, audience errada, sem role, expirados (cliente com vida curta no realm), isolamento entre provedores (inclusive em replays e conflitos) e a ausência de efeitos financeiros em acessos não autorizados. **Ainda não demonstram três instâncias independentes**, comunicação HTTP/SQS nem reinício de processos. Testes sem `TEST_DATABASE_URL`/`TEST_OIDC_ISSUER` são ignorados, não aprovados. As decisões de hash, retries e transação estão em [ARCHITECTURE.md](ARCHITECTURE.md#processwager--processamento-transacional-compartilhado).

## Docker

```sh
make docker-build
docker run --rm --name jungle-backend-local -p 127.0.0.1:8080:8080 --env-file .env.example jungle-backend-go:local
```

Em outro terminal, consulte o health e encerre:

```sh
curl --fail http://localhost:8080/health/live
docker stop --time 15 jungle-backend-local
```

A imagem usa build multi-stage e executa com usuário sem privilégios. O Compose com PostgreSQL, Keycloak e LocalStack será adicionado nas etapas correspondentes do plano.

## Organização e próximos passos

```text
cmd/server/                        entrada do processo
internal/application/             portas e casos de uso (command e query)
internal/bootstrap/               configuração, composição Fx e lifecycle
internal/adapters/http/           servidor, contrato HTTP e autorização
internal/adapters/oidc/           verificação de access tokens do IdP
internal/adapters/postgres/       pool pgx, UnitOfWork, repositórios e reconciliador
internal/domain/money/            valor monetário exato e testes
internal/domain/wallet/           agregado de carteira e testes
internal/domain/wagering/         transações, estados e políticas
internal/domain/ledger/           lançamento imutável do ledger e testes
internal/domain/events/          eventos tipados de domínio e testes
internal/testsupport/             harness de integração (build tag integration)
migrations/                       schema versionado up/down (golang-migrate)
deployments/                      realm Keycloak de desenvolvimento
compose.dev.yaml                  dependências locais (PostgreSQL + Keycloak)
docs/adr/                         decisões de arquitetura
docs/reference/                   snapshot do enunciado e procedência
references/                       clone local do desafio (ignorado pelo Git)
```

As decisões e limites estão em [ARCHITECTURE.md](ARCHITECTURE.md). O próximo incremento inicia SQS/inbox/outbox publisher e o worker de referências, conforme as Fases 4 e 5; o consumidor SQS chamará o mesmo `ProcessWager` já usado pela API HTTP.

O módulo e o remoto `origin` usam o repositório [hendelsantos/jungle-backend-challenge-go](https://github.com/hendelsantos/jungle-backend-challenge-go).

Cada incremento segue o fluxo: implementação, verificações pertinentes, atualização da documentação, commit e push para `origin/main`. Essa rotina está registrada em [AGENTS.md](AGENTS.md).

## Enunciado original

O clone solicitado fica em `references/backend-challenge-go`, com histórico e remoto próprios. Uma [cópia de referência](docs/reference/README_DESAFIO.md) é versionada junto à solução; sua [procedência](docs/reference/SOURCE.md) registra o commit consultado.

Para recriar o clone local em outro checkout:

```sh
git clone https://github.com/junglegaming/backend-challenge-go.git references/backend-challenge-go
git -C references/backend-challenge-go checkout bdd9e70b65db9b374ee070bd3a4768661a888997
```

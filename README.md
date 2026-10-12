# Jungle Backend Go

Solução em desenvolvimento para o [desafio de processamento distribuído de apostas](https://github.com/junglegaming/backend-challenge-go).

**Estado: Fases 0, 1 e 2 concluídas; processamento financeiro da Fase 3 implementado na aplicação.** O servidor HTTP com Uber Fx já oferece liveness e shutdown. O domínio financeiro, as migrations, a abertura atômica, as consultas com cursor, a reconciliação e `ProcessWager` foram testados com PostgreSQL real. `ProcessWager` trata BET, WIN, LOSS, REFUND e ROLLBACK com lock por carteira, idempotência persistente e gravação de saldo, transação, ledger e outbox no mesmo commit.

Ainda faltam endpoints de negócio autenticados, Keycloak/OIDC, SQS/inbox/publisher, worker de referências, Compose, readiness e runner de migrations. Referências ausentes ficam persistidas em `PENDING_REFERENCE`, mas ainda não são retomadas automaticamente. O acompanhamento está em [PLANO_SISTEMA_JUNGLE_GO.md](PLANO_SISTEMA_JUNGLE_GO.md). Prazo interno: **14/10/2026**.

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
| `DATABASE_URL` | vazia | Connection string PostgreSQL (scheme `postgres://` ou DSN keyword/value). Vazia: a aplicação sobe sem banco enquanto não há endpoints de negócio; definida: URL inválida ou banco inacessível impedem a inicialização |

A aplicação lê variáveis do processo. `.env` não é carregado automaticamente. Para usar o exemplo local:

```sh
cp .env.example .env
set -a
. ./.env
set +a
make run
```

Configuração inválida ou porta ocupada impede a inicialização. `/health/live` apenas indica que o processo responde; `/health/ready` será implementado com as verificações reais de PostgreSQL e SQS.

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

## ProcessWager: garantias verificadas

- Duas BETs de `80.00` sobre `100.00`: uma processada, uma rejeitada, saldo `20.00` e um débito.
- 50 chamadas concorrentes da mesma BET: uma operação, um débito e dois eventos; 49 replays.
- Replay retorna saldo/versão originais, mesmo após novas movimentações. Mesma chave com conteúdo diferente ou mesmo ID externo com outra chave gera conflito.
- LOSS e rejeições não geram ledger; REFUND/ROLLBACK concorrentes não compensam a mesma referência duas vezes.
- Erro SQL na segunda gravação da outbox desfaz carteira, transação, ledger e primeiro evento.
- Uma carteira progride enquanto outra está bloqueada; reconciliação confirma os saldos após os cenários concorrentes e de reversão.

Os testes usam conexões reais e goroutines em um processo por pacote. **Ainda não demonstram três instâncias independentes**, comunicação HTTP/SQS, reinício de processos ou autorização. Testes sem `TEST_DATABASE_URL` são ignorados, não aprovados. As decisões de hash, retries e transação estão em [ARCHITECTURE.md](ARCHITECTURE.md#processwager--processamento-transacional-compartilhado).

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
internal/adapters/http/           servidor e contrato HTTP
internal/adapters/postgres/       pool pgx, UnitOfWork, repositórios e reconciliador
internal/domain/money/            valor monetário exato e testes
internal/domain/wallet/           agregado de carteira e testes
internal/domain/wagering/         transações, estados e políticas
internal/domain/ledger/           lançamento imutável do ledger e testes
internal/domain/events/          eventos tipados de domínio e testes
internal/testsupport/             harness de integração (build tag integration)
migrations/                       schema versionado up/down (golang-migrate)
docs/adr/                         decisões de arquitetura
docs/reference/                   snapshot do enunciado e procedência
references/                       clone local do desafio (ignorado pelo Git)
```

As decisões e limites estão em [ARCHITECTURE.md](ARCHITECTURE.md). O próximo incremento inicia os endpoints financeiros e a autenticação OIDC com Keycloak, conforme a Fase 3. HTTP e SQS deverão chamar o mesmo `ProcessWager`; o `providerId` precisará vir da identidade autorizada na fronteira de entrada.

O módulo e o remoto `origin` usam o repositório [hendelsantos/jungle-backend-challenge-go](https://github.com/hendelsantos/jungle-backend-challenge-go).

Cada incremento segue o fluxo: implementação, verificações pertinentes, atualização da documentação, commit e push para `origin/main`. Essa rotina está registrada em [AGENTS.md](AGENTS.md).

## Enunciado original

O clone solicitado fica em `references/backend-challenge-go`, com histórico e remoto próprios. Uma [cópia de referência](docs/reference/README_DESAFIO.md) é versionada junto à solução; sua [procedência](docs/reference/SOURCE.md) registra o commit consultado.

Para recriar o clone local em outro checkout:

```sh
git clone https://github.com/junglegaming/backend-challenge-go.git references/backend-challenge-go
git -C references/backend-challenge-go checkout bdd9e70b65db9b374ee070bd3a4768661a888997
```

# Jungle Backend Go

Solução em desenvolvimento para o [desafio de processamento distribuído de apostas](https://github.com/junglegaming/backend-challenge-go).

**Estado: Fases 0 e 1 concluídas; Fase 2 quase completa.** Existe um servidor HTTP com Uber Fx, configuração validada, logs JSON, liveness e shutdown; o domínio financeiro completo com testes; migrations versionadas com trigger append-only e constraints verificados automaticamente; `pgx` com pool e `UnitOfWork` transacional; e os casos de uso `CreateWallet` (abertura atômica com `OPENING`, ledger e outbox no mesmo commit), `GetWallet`, `ListLedger` (cursor opaco, ordenação estável) e `ReconcileWallet` (snapshot `REPEATABLE READ READ ONLY`) — todos testados contra PostgreSQL real. Ainda não há endpoints de negócio, autenticação, SQS, readiness, runner de migrations integrado ou `ProcessWager`. O acompanhamento está em [PLANO_SISTEMA_JUNGLE_GO.md](PLANO_SISTEMA_JUNGLE_GO.md).

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

Os testes de integração (transações do `UnitOfWork`, caso de uso `CreateWallet`, leituras com cursor, reconciliação e proteções de schema) exigem PostgreSQL real (migrations aplicadas pelo próprio harness) e usam a build tag `integration` (pulam sem a variável de conexão; nenhum mock substitui o banco):

```sh
docker run -d --name jungle-pg -e POSTGRES_PASSWORD=verify -p 127.0.0.1:55432:5432 postgres:17-alpine
TEST_DATABASE_URL='postgres://postgres:verify@127.0.0.1:55432/postgres?sslmode=disable' \
  go test -tags=integration ./internal/...
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

`Wallet` mantém identidade, jogador, saldo, versão e timestamps em campos privados. Criação inicia na versão 1; reidratação preserva o estado persistido; crédito e débito validam moeda e limites antes de alterar o agregado. Rejeições e valores zero preservam saldo, versão e horário de atualização. As garantias entre processos e a gravação do ledger serão implementadas com PostgreSQL.

`WagerTransaction` implementa os estados e as regras de `BET`, `WIN`, `LOSS`, `REFUND`, `ROLLBACK` e abertura interna `OPENING`. O domínio avalia referências, impede reprocessamento de estados terminais e mantém o resultado original para a futura persistência. A política de compensação está na [ADR 0001](docs/adr/0001-compensacao-direta.md). Idempotência durável, retry de referências e atomicidade SQL ainda estão pendentes.

`WalletLedgerEntry` é um tipo-valor imutável com um lançamento por movimentação efetiva: direção, quantia positiva, saldo anterior e posterior consistentes (`balanceAfter = balanceBefore ± amount`) e timestamp em UTC. Não há setters; criação e reidratação coincidem. Unicidade `(wallet_id, transaction_id)` e proteção contra edição/exclusão serão impostas pelo schema PostgreSQL.

Os eventos tipados (`WagerTransactionProcessed`, `WagerTransactionRejected`, `WalletBalanceChanged`, `WagerTransactionPendingReference`) são envelopes imutáveis com `eventId`, `eventType`, `aggregateId` derivado dos dados, `correlationId`, `causationId` opcional, `occurredAt` em UTC, `version` por tipo e payload tipado; `MarshalJSON` fixa o contrato do payload da outbox com dinheiro em string decimal. A gravação na `outbox_events` e a publicação pós-commit serão implementadas com PostgreSQL e SQS.

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

As decisões e limites estão em [ARCHITECTURE.md](ARCHITECTURE.md). O próximo incremento é o caso de uso `ProcessWager` (BET/WIN/LOSS/REFUND/ROLLBACK com lock por carteira, idempotência persistente e hash canônico); os endpoints HTTP autenticados vêm na Fase 3. As demais pastas serão criadas quando tiverem implementação.

O módulo e o remoto `origin` usam o repositório [hendelsantos/jungle-backend-challenge-go](https://github.com/hendelsantos/jungle-backend-challenge-go).

Cada incremento segue o fluxo: implementação, verificações pertinentes, atualização da documentação, commit e push para `origin/main`. Essa rotina está registrada em [AGENTS.md](AGENTS.md).

## Enunciado original

O clone solicitado fica em `references/backend-challenge-go`, com histórico e remoto próprios. Uma [cópia de referência](docs/reference/README_DESAFIO.md) é versionada junto à solução; sua [procedência](docs/reference/SOURCE.md) registra o commit consultado.

Para recriar o clone local em outro checkout:

```sh
git clone https://github.com/junglegaming/backend-challenge-go.git references/backend-challenge-go
git -C references/backend-challenge-go checkout bdd9e70b65db9b374ee070bd3a4768661a888997
```

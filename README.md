# Jungle Backend Go

Solução do [desafio de processamento distribuído de apostas](https://github.com/junglegaming/backend-challenge-go) da Jungle Gaming: backend financeiro distribuído em Go com integridade demonstrável.

**Estado: Fases 0–6 implementadas; auditoria independente com pendências obrigatórias abertas.** `ProcessWager` trata BET, WIN, LOSS, REFUND e ROLLBACK com lock por carteira, idempotência persistente e gravação de saldo, transação, ledger e outbox no mesmo commit. Os contratos HTTP oficiais estão expostos com autenticação OIDC (`client_credentials`), isolamento entre provedores e operações internas restritas; `GET /health/ready` verifica PostgreSQL e, quando configurado, SQS. Operações com referência ausente ficam `PENDING_REFERENCE` e um worker distribuído as retoma com backoff exponencial e TTL, concluindo, reagendando ou rejeitando com `REFERENCE_NOT_FOUND`. Eventos comprometidos são publicados pós-commit na fila de eventos por um publisher com claim/lease, backoff e republicação com mesmo `eventId`.

A [auditoria independente de 13/10/2026](docs/AUDITORIA_INDEPENDENTE_2026-10-13.md), sobre `e11e325`, atribuiu **80/100**, com três achados P1 e dez P2. Os testes E/F/H estão em `scripts/fault.sh`, mas as janelas de interrupção ainda precisam de comprovação controlada. O “GO” de [`docs/QA_RELATORIO_FINAL.md`](docs/QA_RELATORIO_FINAL.md) é histórico e deve ser lido com as ressalvas da nova revisão. `GET /metrics` (via gateway) expõe o registro Prometheus: operações por kind/status, replays idempotentes, conflitos, histograma de duração, retries de transação, reentregas, DLQ, divergências de reconciliação e pendência/atraso da outbox (com três réplicas cada scrape reflete o processo atingido; consolidação exige agregação externa). O acompanhamento está em [PLANO_SISTEMA_JUNGLE_GO.md](PLANO_SISTEMA_JUNGLE_GO.md). Prazo interno: **14/10/2026**.

A [matriz de conformidade](docs/CONFORMIDADE_DESAFIO.md) relaciona as exigências oficiais à implementação, aos testes e às pendências. O README oficial prevalece sobre o plano interno; a entrega só estará concluída com os requisitos obrigatórios demonstrados no ambiente exigido.

## Executar localmente

Pré-requisitos: Go **1.27.2**, Git e Make. Docker e Docker Compose são necessários para as dependências e os testes de integração. O Go pode baixar automaticamente a toolchain indicada em `go.mod` quando `GOTOOLCHAIN=auto`.

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
| `SQS_ENDPOINT_URL` | vazia | Endpoint LocalStack, ex.: `http://localhost:4566`; habilita o consumidor e exige banco e chaves de produtores |
| `SQS_REGION` | `us-east-1` | Região do broker; credenciais vêm da cadeia padrão do SDK AWS |
| `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` | sem fallback | No ambiente local, fornecer explicitamente `test` / `test`; perfis e roles também são resolvidos pelo SDK |
| `AWS_SESSION_TOKEN` | opcional | Token de credenciais temporárias; deixar vazio no exemplo local `test` / `test` |
| `SQS_PROVIDER_KEYS` | obrigatória com SQS | Objeto JSON `providerId → chave HMAC` (mínimo 32 bytes por chave) |
| `SQS_QUEUE_PREFIX` | vazio | Opcional para testes isolados: até 32 letras, dígitos, `_` ou `-`; nomes oficiais por padrão |

A aplicação lê variáveis do processo. `.env` não é carregado automaticamente. Para usar o exemplo local:

```sh
cp .env.example .env
set -a
. ./.env
set +a
make run
```

Configuração inválida ou porta ocupada impede a inicialização. `/health/live` indica que o processo responde; `/health/ready` (registrado quando há banco) faz um ping real no PostgreSQL com prazo curto. Com SQS configurado, o mesmo endpoint consulta as três filas e retorna `503` se uma dependência falhar.

## Dependências locais de desenvolvimento

`compose.dev.yaml` sobe PostgreSQL 17, LocalStack Community 4.14.0 (sem conta/token AWS ou LocalStack) e Keycloak 26.8 com o realm `jungle` importado (dois provedores, serviço interno e clientes de teste negativos; segredos fictícios):

```sh
docker compose -f compose.dev.yaml up -d   # postgres :55432, keycloak :58080, LocalStack :4566
```
Neste incremento o harness aplica as migrations reais em um banco vazio. O runner
oficial é o comando `migrate`:

```sh
export DATABASE_URL='postgres://jungle:jungle-local@localhost:55432/jungle?sslmode=disable'
make migrate-up          # aplica todas as migrations pendentes; reexecutar é seguro
make migrate-version     # mostra a versão registrada (`none` = histórico vazio)
export OIDC_ISSUER=http://localhost:58080/realms/jungle
export OIDC_AUDIENCE=jungle-api
make run
```

`make migrate-down N=1` é um rollback **destrutivo** da última migration —
NÃO está no fluxo acima de propósito: usar apenas em banco de desenvolvimento
disponível e reaplicar com `make migrate-up` antes de iniciar a aplicação
(remover a migration de inbox/outbox deixa o runtime sem as estruturas
financeiras).

O harness também cria registros de teste com identidades únicas. A aplicação usa
`DATABASE_URL`; `TEST_DATABASE_URL` é exclusivo dos testes.

## Entrega com Compose (três processos)

A partir de um checkout limpo, um comando sobe o ambiente completo de entrega —
PostgreSQL, job de migrations (golang-migrate), Keycloak com o realm `jungle`,
LocalStack SQS, três réplicas independentes da aplicação e o gateway nginx —
isolado do ambiente de desenvolvimento por projeto e por portas de loopback:

```sh
docker compose up --build -d   # `-f docker-compose.yml` é o arquivo padrão do diretório
docker compose -f docker-compose.yml ps    # gateway, api-1/2/3, keycloak, localstack, postgres: healthy
```

Portas publicadas (apenas loopback):
- gateway `9080` → três réplicas (upstream resolvido por request no DNS do Docker);
- IdP `59080` → Keycloak publico da máquina; o issuer validado pelas réplicas é
  `http://keycloak:8080/realms/jungle` (divisão dev permitida pelo §6.2);
- PostgreSQL não é publicado.

O `scripts/smoke.sh` reproduz o cenário oficial de ponta a ponta contra essa
instância: tokens `client_credentials` reais, abertura de `100.00 BRL`,
**50 BETs idênticas de `50.00` concorrentes via gateway → exatamente um débito
e 49 replays idempotentes**, **par concorrente de BETs distintas `40.00` →
`200`/`422 INSUFFICIENT_FUNDS`**, saldo final `10.00`, três lançamentos no
ledger e reconciliação `consistent:true`:

```sh
GATEWAY_URL=http://localhost:9080 KEYCLOAK_URL=http://localhost:59080 bash scripts/smoke.sh
```

Os logs por container comprovam as três réplicas na rota (`docker logs
jungle-delivery-api-1|2|3`). As chaves SQS, segredos do Keycloak e credenciais
do banco são exclusivos de desenvolvimento — nunca publicar o ambiente. Para
zerar, `docker compose -f docker-compose.yml down -v`.

## Testes de carga com k6

A suíte de performance em `tests/load/` mede o ambiente de entrega com três
réplicas reais e fecha cada cenário com verificação financeira **independente**
(SQL somente leitura via `cmd/verifywallet`, publicado em `127.0.0.1:59532`).
Pré-requisitos e execução em [`tests/load/README.md`](tests/load/README.md);
resultados reais, tabelas p50/p95/p99 e análise de gargalos em
[docs/PERFORMANCE_REPORT.md](docs/PERFORMANCE_REPORT.md):

```sh
cd tests/load
k6 run scenarios/smoke.js          # saneamento: aborta se o ambiente não estiver saudável
k6 run scenarios/ramp-up.js        # 10→25→50→100 VUs com carteiras exclusivas por VU
k6 run scenarios/contention.js     # 50 BETs de 30.00 em burst → 33/17, saldo 10.00 auditável
k6 run scenarios/idempotency.js    # 50 idênticas → 1 débito, 49 replays
k6 run scenarios/multiwallet.js    # ciclo BET/WIN/LOSS/REFUND/ROLLBACK por carteira
k6 run scenarios/soak.js           # 15 min com reconciliação periódica ao vivo
```

## Falhas literais (Testes E/F/H)

Use um projeto Compose **descartável e isolado**, com `COMPOSE_PROJECT_NAME`
explícito e as portas correspondentes em `GATEWAY_URL`, `KEYCLOAK_URL` e `SQS_URL`.
Após subir o ambiente:

```sh
FAULT_DISPOSABLE=1 bash scripts/fault.sh
```

O script compila uma imagem separada com a tag Go `faulttest`; a imagem normal
recusa a configuração dos failpoints. E bloqueia após commit da inbox e antes
do delete, exige o registro SQL e mata os três processos; após reinício exige
`inboxReplay=true`, mesmo messageId e receive count maior que um. F bloqueia
após SendMessage aceito, verifica lease ainda válida e ACK ausente, mata os
processos e exige tentativa 2 após expiração natural, mesmo eventId e SHA-256
do payload nas duas chamadas aceitas. A deduplicação FIFO pode suprimir a
segunda entrega. H reinicia com referência pendente e conclui por HTTP→SQS.
Saldo, ledger e reconciliação são conferidos. Janela não alcançada reprova o
script. A saída registra IDs, tentativas e evidências; o EXIT restaura a imagem
normal. O broker local está publicado somente em loopback (porta padrão 49570).

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
docker compose -f compose.dev.yaml up -d postgres
TEST_DATABASE_URL='postgres://jungle:jungle-local@127.0.0.1:55432/jungle?sslmode=disable' \
  go test -race -tags=integration ./internal/adapters/postgres ./internal/application/... -count=1
```

A suíte HTTP autenticada (`internal/adapters/http`) adicionalmente exige Keycloak real com o realm importado; sem `TEST_OIDC_ISSUER` ela pula:

```sh
docker compose -f compose.dev.yaml up -d
TEST_OIDC_ISSUER='http://localhost:58080/realms/jungle' \
TEST_SQS_ENDPOINT=http://localhost:4566 \
AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_SESSION_TOKEN= \
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

`Wallet` mantém identidade, jogador, saldo, versão e timestamps em campos privados. Criação inicia na versão 1; reidratação preserva o estado persistido; crédito e débito validam moeda e limites antes de alterar o agregado. Rejeições e valores zero preservam saldo, versão e horário de atualização. A aplicação coordena escritas por `SELECT ... FOR UPDATE` e grava o ledger no mesmo commit; a demonstração com três processos independentes está nos scripts reproduzíveis do Compose de entrega.

`WagerTransaction` implementa os estados e as regras de `BET`, `WIN`, `LOSS`, `REFUND`, `ROLLBACK` e abertura interna `OPENING`. O domínio avalia referências, impede reprocessamento de estados terminais e mantém o resultado original persistido para replay. A política de compensação está na [ADR 0001](docs/adr/0001-compensacao-direta.md). Idempotência durável e atomicidade SQL estão implementadas; o worker de referências retoma pendências persistidas.

`WalletLedgerEntry` é um tipo-valor imutável com um lançamento por movimentação efetiva: direção, quantia positiva, saldo anterior e posterior consistentes (`balanceAfter = balanceBefore ± amount`) e timestamp em UTC. Não há setters; criação e reidratação coincidem. Unicidade `(wallet_id, transaction_id)` e proteção contra edição/exclusão são impostas pelo schema PostgreSQL.

Os eventos tipados (`WagerTransactionProcessed`, `WagerTransactionRejected`, `WalletBalanceChanged`, `WagerTransactionPendingReference`) são envelopes imutáveis com `eventId`, `eventType`, `aggregateId` derivado dos dados, `correlationId`, `causationId` opcional, `occurredAt` em UTC, `version` por tipo e payload tipado; `MarshalJSON` fixa o contrato do payload da outbox com dinheiro em string decimal. A gravação na `outbox_events` é atômica com as operações financeiras; a publicação pós-commit na fila de eventos é detalhada no [ARCHITECTURE.md](ARCHITECTURE.md#outbox-publisher--fase-5-incremento-2).

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

Os testes usam infraestrutura real: a suíte HTTP valida tokens válidos, ausentes, malformados, com assinatura alterada, audience errada, sem role, expirados (cliente com vida curta no realm), isolamento entre provedores (inclusive em replays e conflitos) e a ausência de efeitos financeiros em acessos não autorizados. As três instâncias independentes (réplicas atrás do gateway), os reinícios literais e o cruzamento HTTP↔SQS são exercitados pelos scripts reproduzíveis [`scripts/smoke.sh`](scripts/smoke.sh) e [`scripts/fault.sh`](scripts/fault.sh) contra o Compose de entrega. HTTP/SQS usam broker e IdP reais. Testes sem `TEST_DATABASE_URL`/`TEST_OIDC_ISSUER`/`TEST_SQS_ENDPOINT` são ignorados, não aprovados. As decisões de hash, retries e transação estão em [ARCHITECTURE.md](ARCHITECTURE.md#processwager--processamento-transacional-compartilhado).

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
internal/adapters/sqs/            consumidor FIFO, assinatura e provisionamento
internal/adapters/oidc/           verificação de access tokens do IdP
internal/adapters/postgres/       pool pgx, UnitOfWork, repositórios e reconciliador
internal/domain/money/            valor monetário exato e testes
internal/domain/wallet/           agregado de carteira e testes
internal/domain/wagering/         transações, estados e políticas
internal/domain/ledger/           lançamento imutável do ledger e testes
internal/domain/events/          eventos tipados de domínio e testes
internal/testsupport/             harness de integração (build tag integration)
migrations/                       schema versionado up/down (golang-migrate)
deployments/                      realm Keycloak de desenvolvimento e gateway
compose.dev.yaml                  dependências locais (PostgreSQL + Keycloak + LocalStack)
tests/load/                       suíte k6 com verificação financeira independente
docs/adr/                         decisões de arquitetura (ADR 0001/0002)
docs/CONFORMIDADE_DESAFIO.md      matriz requisito → implementação → evidência
docs/QA_RELATORIO_FINAL.md        auditoria de QA e ciclos (veredito GO)
docs/PERFORMANCE_REPORT.md        resultados reais dos testes de carga k6
docs/ENGINEERING_EXCELLENCE_REPORT.md  relatório executivo com A/B
docs/ARCHITECTURE_DIAGRAMS.md     diagramas Mermaid dos fluxos reais
docs/RUNBOOK.md                   operação e resposta a falhas
docs/SECURITY_REVIEW.md           revisão de segurança e ameaças
docs/reference/                   snapshot do enunciado e procedência
references/                       clone local do desafio (ignorado pelo Git)
```

As decisões e limites estão em [ARCHITECTURE.md](ARCHITECTURE.md). O publisher da outbox publica pós-commit com claim/lease, backoff e republicação com mesmo `eventId`, verificado com LocalStack e PostgreSQL reais; o próximo incremento é o Compose de entrega e a demonstração com três processos. O consumidor SQS já chama o mesmo `ProcessWager` da API HTTP.

O módulo e o remoto `origin` usam o repositório [hendelsantos/jungle-backend-challenge-go](https://github.com/hendelsantos/jungle-backend-challenge-go).

Cada incremento segue o fluxo: implementação, verificações pertinentes, atualização da documentação, commit e push para `origin/main`. Essa rotina está registrada em [AGENTS.md](AGENTS.md).

## Enunciado original

O clone solicitado fica em `references/backend-challenge-go`, com histórico e remoto próprios. Uma [cópia de referência](docs/reference/README_DESAFIO.md) é versionada junto à solução; sua [procedência](docs/reference/SOURCE.md) registra o commit consultado.

Para recriar o clone local em outro checkout:

```sh
git clone https://github.com/junglegaming/backend-challenge-go.git references/backend-challenge-go
git -C references/backend-challenge-go checkout bdd9e70b65db9b374ee070bd3a4768661a888997
```

### Revisão do backoff de referências (12/10/2026)

O backoff limita o expoente antes do deslocamento de bits: 1s, 2s, …, 256s e teto de 5min a partir da décima tentativa. Isso evita overflow de `time.Duration` em pendências duradouras; TTL e regras de reagendamento permanecem iguais. Teste cobre limites, 1.000 tentativas e o maior `int` da plataforma.

## Consumidor SQS — primeiro incremento da Fase 5

Depois de subir as dependências e aplicar as migrations conforme acima, habilite
SQS no mesmo processo da API (mantenha `DATABASE_URL`, `OIDC_ISSUER` e
`OIDC_AUDIENCE` dos exemplos anteriores):

```sh
export AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_SESSION_TOKEN=
export SQS_ENDPOINT_URL=http://localhost:4566
export SQS_PROVIDER_KEYS='{"provider-a":"local-provider-a-signing-key-32-bytes-minimum","provider-b":"local-provider-b-signing-key-32-bytes-minimum"}'
make run
```

O startup provisiona automaticamente `wager-transactions.fifo`,
`wager-transactions-dlq.fifo` e `wager-events.fifo`, corrigindo redrive e
visibilidade em filas existentes. Não exige conta AWS nem token LocalStack.
O Compose de desenvolvimento (`compose.dev.yaml`) sobe as dependências de
teste; o Compose de entrega (`docker-compose.yml`) já inclui a aplicação em
três réplicas, gateway nginx, migrations automáticas pelo job `migrate`
(Keycloak e PostgreSQL e LocalStack reais). Se a porta estiver ocupada, use
`JUNGLE_SQS_PORT=14566` no Compose e ajuste
`SQS_ENDPOINT_URL`/`TEST_SQS_ENDPOINT` para `http://localhost:14566`.

Salve o envelope oficial em `/tmp/wager.json`, usando carteira e jogador criados
pelo endpoint interno. Todos os valores monetários são strings:

```json
{
  "messageId": "msg-exemplo-001",
  "type": "WagerTransactionRequested",
  "occurredAt": "2026-10-12T12:00:00Z",
  "data": {
    "providerId": "provider-a",
    "externalTransactionId": "bet-exemplo-001",
    "idempotencyKey": "provider-a:bet-exemplo-001",
    "playerId": "SUBSTITUA-PELO-UUID-DO-JOGADOR",
    "walletId": "SUBSTITUA-PELO-UUID-DA-CARTEIRA",
    "roundId": "round-1",
    "gameId": "fortune-chimp",
    "kind": "BET",
    "money": {"amount": "25.00", "currency": "BRL"}
  }
}
```

Em outro terminal, o produtor de desenvolvimento assina e envia o corpo sem
alterá-lo (a variável abaixo contém apenas a chave daquele provedor):

```sh
AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_SESSION_TOKEN= \
SQS_ENDPOINT_URL=http://localhost:4566 \
SQS_PROVIDER_KEY=local-provider-a-signing-key-32-bytes-minimum \
go run ./cmd/send-wager -provider provider-a -file /tmp/wager.json

# Inspecionar filas/DLQ no broker local:
docker compose -f compose.dev.yaml exec localstack awslocal sqs list-queues
# Copie a URL da DLQ retornada:
docker compose -f compose.dev.yaml exec localstack awslocal sqs receive-message \
  --queue-url URL-DA-DLQ --message-attribute-names All
```

A assinatura HMAC-SHA256 vai nos atributos SQS `providerId` e `signature`; o
provedor autenticado precisa corresponder ao corpo. Chaves diferentes isolam os
provedores. O LocalStack fica em porta de loopback; sua configuração local não
comprova autorização IAM. Não exponha esse broker de desenvolvimento como um
serviço de produção. Os valores publicados aqui são exclusivamente de teste.

`MessageGroupId` é o UUID da carteira. O produtor usa SHA-256 do corpo como
`MessageDeduplicationId`; a deduplicação por conteúdo do broker fica desativada.
A deduplicação financeira é permanente no PostgreSQL, independente da janela
FIFO: a mesma aposta por HTTP e SQS produz um único efeito. Use `messageId`
globalmente único (preferencialmente UUID) e reenvie os mesmos bytes para replay
da inbox. Alterar espaços também altera seu hash; o hash financeiro separado
continua canônico e independente dos metadados de transporte.

Política operacional:

- Long polling de 10s; um item por chamada; visibilidade de 30s; processamento e
  confirmação limitados a 10s. Uma falha retorna para retry em 1s, 2s e 4s;
  após três recebimentos, o redrive do broker encaminha à DLQ.
- Envelope inválido, assinatura ausente/divergente, operação inválida ou conflito
  de identidade são permanentes: copiar à DLQ e só então remover da entrada.
  Falha nessa cópia conserva a mensagem original. Carteira ainda inexistente e
  falhas de infraestrutura seguem retry limitado. Rejeição de negócio confirmada
  (`REJECTED`) é terminal, persistida na inbox e removida sem ir à DLQ.
- Carteira, operação, ledger, outbox e conclusão da inbox compartilham um commit.
  `DeleteMessage` só ocorre depois dele. Resultado de commit desconhecido conserva
  a mensagem; reentrega consulta a identidade persistida sem novo débito.
- `SIGTERM` cancela polling e trabalho ativo; transações não confirmadas fazem
  rollback e receipts conhecidos têm visibilidade liberada. Um receive cancelado
  cujo receipt não chegou pode aguardar os 30s de visibilidade para reaparecer.

Testes reproduzíveis (PostgreSQL e Keycloak também precisam estar ativos):

```sh
make check
go vet -tags=integration ./...
TEST_DATABASE_URL='postgres://jungle:jungle-local@localhost:55432/jungle?sslmode=disable' \
TEST_OIDC_ISSUER=http://localhost:58080/realms/jungle \
TEST_SQS_ENDPOINT=http://localhost:4566 \
AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_SESSION_TOKEN= \
go test -race -tags=integration ./... -count=1
```

As suítes usam filas exclusivas e só excluem filas que criam; não fazem purge das
filas de desenvolvimento. Verificam HTTP+SQS com IdP real, redelivery, rollback
por erro SQL na inbox, commit confirmado com resposta perdida, rejeição terminal,
conflitos, autenticação, DLQ por três tentativas, readiness e shutdown com rollback.
Injeção de falhas na fronteira do commit **não equivale a matar/reiniciar processos**:
os kills literais de processos são exercitados por [`scripts/fault.sh`](scripts/fault.sh)
(Testes E/F/H com `docker compose kill -s KILL api` nas três réplicas) e a
demonstração de paralelismo entre instâncias está em [`scripts/smoke.sh`](scripts/smoke.sh).

## Outbox publisher — segundo incremento da Fase 5

Eventos comprometidos no mesmo commit financeiro são publicados na
`wager-events.fifo` por um worker em loop de 1s que reivindica um evento por vez:

- **Claim** `FOR UPDATE SKIP LOCKED` com `clock_timestamp()` (tempo do banco,
  sem relógio de host) e lease pareado `leased_by`/`leased_until`; `attempts`
  incrementa. O token do claim identifica a reivindicação, não o processo.
- **Envio sem transação aberta:** a chamada SQS ocorre entre duas escritas SQL
  curtas; nenhum lock de linha fica aberto durante I/O de rede.
- **Confirmação condicionada ao token:** `published_at` só grava com o token
  dono do lease; token vencido vira `ErrOutboxLeaseLost` e o lease expira, outra
  instância assume o evento (`attempts` sobe a 2).
- **Falha de publicação:** backoff exponencial 1s→5min gravado em
  `next_attempt_at` com `last_error`; o evento nunca é descartado.
- **Payload imutável:** o corpo enviado é o snapshot `payload_json` do banco;
  a republicação usa o mesmo `eventId`. Entrega é at-least-once — o consumidor
  do evento deve deduplicar pelo `eventId` além da janela FIFO.
- **Métricas:** `/metrics` expõe `jungle_outbox_*` (pendência, atraso, claims,
  publicações, falhas, retries, leases perdidas); gauges consultam o PostgreSQL
  e falha de consulta vira 503, nunca atraso falso.

Evidência de integração (LocalStack e PostgreSQL reais, filas e schema isolados
por teste): entrega com corpo/`MessageDeduplicationId`/`MessageGroupId` corretos;
crash simulado entre envio e confirmação recuperado com o mesmo `eventId`
(a janela de deduplicação FIFO suprime a repetição); quatro publishers
concorrentes entregam dois eventos uma única vez cada; lease abandonado
recuperado sem interferência do token antigo. Os kills literais de processo
e a demonstração com três instâncias estão em `scripts/fault.sh` e
`scripts/smoke.sh` (Fase 6, incremento 4).

## Engineering decisions & trade-offs

- **PostgreSQL como única fonte da verdade.** Memória, SQS FIFO e correlações em HTTP servem o plano de controle; a integridade financeira (identidade, saldo, ledger, ilog/outbox) é garantida por schema (índices únicos, CHECKs, triggers) — isso sobrevive a processos, replicação e reinício.
- **Locking pessimista por linha.** As carteiras são objetos concorrentes por natureza; `SELECT ... FOR UPDATE` no contexto da mesma transação do débito evita lost updates com ordem simples e previsível — carteiras diferentes travam linhas diferentes e progridem em paralelo.
- **Idempotência persistente (não em memória).** Dois índices únicos por provedor: `idem-potency_key` e `external_transaction_id`. O vencedor do INSERT processa; os perdedores leem o resultado persistido, inclusive o saldo no momento do resultado (`result_balance_cents`, `result_wallet_version`), inclusive após reinício.
- **Inbox e outbox no mesmo commit.** Duplicação entre HTTP e SQS é resolvida pela inbox `(consumer, messageId, hash)` commitada junto; a outbox garante "nunca publicar antes do commit" e produz eventos com `eventId` estável.
- **Entrega at-least-once (idadeamento honesto).** SQS/I O dedup tem janela limitada; o correto é REPUBLICAR o mesmo `eventId` e delegar deduplicação ao consumidor fora da janela FIFO (at-least inclusive). Nada aqui promete "exactly once".
- **Dimensão de pool (A/B real, 13/10).** Perna limpa 100 VUs: `max(4, ncpu)=12` conns: 344–355 rps, p95 393–404 ms, p99 586–614 ms, espera de aquisição ~44 s média; MaxConns=24: 341 rps (par), p95 389 ms, p99 542–561 ms, espera ~9 s. Mais conns (36/48) **piorou** (saturação do Postgres; 3×36 > 100 con max_connections). Adotado **PG_POOL_MAX_CONNS=24** no Compose de entrega com override por env.
- **Outbox publishers múltiplos (drain medido).** Backlog isolado de ~27k eventos: 1 worker/réplica drenou em 147,7 ev/s; 3 workers/réplica em **408,7–421,5 ev/s** (2,8×), sem violar leases (`FOR UPDATE SKIP LOCKED`, ack por token, sem transação aberta no I/O). Adotado **OUTBOX_WORKERS=3** no Compose; os tests de leases/falha publicam/ack do editor já cobrem os dois modos.
- **Gargalos observados (com evidência):** além do pool, o publisher de outbox não acompanha produção extrema (100 VUs geram ~700 ev/s de pico; com 1 worker por réplica o dreno isolado mediu 147,7 ev/s). Com 3 workers por réplica, o dreno medido (408–421 ev/s) passa a acompanhar a produção moderada. Alternativas rejeitadas: cache de saldo em memória (corretude não aceita), verificação check-then-act fora do banco (perde para os índices únicos) e OTel distribuído (diferencial opcional; fora do escopo do prazo).
- **Próximos passos em produção (fora do escopo do desafio):** alertas contínuos em `jungle_outbox_*` e divergências, dashboards com esses regimes, pool dimensionado por load-test em hardware dedicado, segredos em vault com rotação, revisão de incidentes e runbook ensaiado.


## Checkpoint das correções locais (13/10/2026)

Os cenários k6 agora possuem thresholds para reprovar checks falhos e usam
contadores agregados; as expectativas monetárias usam BigInt. O soak usa
`constant-vus` com duração configurada. Isso não atualiza os resultados das
campanhas anteriores. As pausas `JUNGLE_TEST_POST_COMMIT_PAUSE` e
`JUNGLE_TEST_PUBLISH_ACK_PAUSE` destinam-se aos ensaios de interrupção e ficam
vazias na operação normal. A conformidade permanece parcial; ver os limites
registrados em [CONFORMIDADE_DESAFIO.md](docs/CONFORMIDADE_DESAFIO.md).

### Revisão final em andamento — 14/10/2026

A matriz atual está em [AUDITORIA_FINAL_ENTREGA.md](docs/AUDITORIA_FINAL_ENTREGA.md).
As políticas IAM são templates: o servidor local ainda usa uma única identidade
para provisionar, consumir e publicar. A separação de papéis e as negações do
broker não foram comprovadas operacionalmente; HMAC protege a autenticidade do
pedido financeiro, não impede apagar mensagens no broker.

Regressão automatizada sem skips: `scripts/quality-gates.sh` exige as três
variáveis `TEST_DATABASE_URL`, `TEST_OIDC_ISSUER` e `TEST_SQS_ENDPOINT` e executa
os gates com `-race -count=1/2`. A [regressão de carga](tests/load/README.md)
verifica todas as carteiras por SQL em snapshot. Os probes
`scripts/broker-probe.py` e `scripts/broker-recovery.py` registram limites reais
do broker; exit 2 no segundo significa durabilidade reprovada, não aprovação.

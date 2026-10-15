# Jungle Backend Go

[![ci](https://github.com/hendelsantos/jungle-backend-challenge-go/actions/workflows/ci.yml/badge.svg)](https://github.com/hendelsantos/jungle-backend-challenge-go/actions/workflows/ci.yml)

Solução do [desafio de processamento distribuído de apostas](https://github.com/junglegaming/backend-challenge-go) da Jungle Gaming: backend financeiro distribuído em Go com integridade demonstrável.

A aplicação implementa BET, WIN, LOSS, REFUND e ROLLBACK, dinheiro exato,
idempotência persistente, ledger append-only, OIDC/Keycloak, inbox/outbox,
worker de referências e três processos Go independentes. HTTP/SQS compartilham
o caso de uso; PostgreSQL confirma os efeitos financeiros atomicamente.

## Status do desafio (14/10/2026)

**Parecer: GO COM RESSALVAS — sem declaração de conformidade integral.**

| Escopo | Estado | Evidência |
| --- | --- | --- |
| Núcleo financeiro, concorrência, recuperação de processos, carga | ✅ Comprovado na regressão de 14/10 | [auditoria final](docs/AUDITORIA_FINAL_ENTREGA.md) |
| A01 — autorização do broker (credenciais/políticas aplicadas) | ⚠️ Pendente | LocalStack não aplica IAM; MiniStack e ElasticMQ reprovados em POC ([veredito local](docs/AUDITORIA_A01_A02_LOCAL_FINAL.md)) |
| A02 — mensagens aceitas sobrevivem ao restart do broker | ⚠️ Pendente | LocalStack/MiniStack perdem mensagens; ElasticMQ prova durabilidade mas falha em A01 |

### O que NÃO foi possível demonstrar — leia antes de avaliar

Transparência deliberada: estes itens **não** estão comprovados e não há
resultado inventado para nenhum deles.

| Limitação | Por quê | Caminho de encerramento |
| --- | --- | --- |
| **A01 — o broker rejeitar operações não autorizadas** | O enunciado (§19) exige controle por credenciais e políticas **do broker**. Dos brokers locais gratuitos testados: LocalStack Community não avalia IAM (enforcement é recurso Pro); MiniStack aceita anônimo e chaves builtin forjadas; ElasticMQ não autentica. Não há como provar sem um broker que aplique políticas de verdade | Homologação em AWS SQS gerenciado **já preparada** (roles IAM via STS, políticas de recurso, probes versionados): [HOMOLOGACAO_AWS.md](docs/HOMOLOGACAO_AWS.md) — pendente apenas de uma conta AWS autorizada |
| **A02 — mensagens aceitas sobreviverem ao restart do broker** | LocalStack (com `PERSISTENCE=1`) e MiniStack (snapshot no shutdown) perderam mensagens aceitas — reproduzido com contagem exata. ElasticMQ provou durabilidade até sob SIGKILL, mas falha em A01 e em compatibilidade | Mesmo caminho da A01 (SQS gerenciado); a **recuperação da aplicação** já é comprovada (inbox/outbox no PostgreSQL), mas ela não ressuscita o que o broker perdeu após o ACK |
| Testes contra AWS real | Nenhuma conta AWS disponível/autorizada; nenhum serviço pago foi usado | Idem — procedimento, custos (centavos) e limpeza documentados |
| Soak de 15 min nesta revisão | Não reexecutado na rodada final; ensaio de 60s e regressões de carga anteriores estão publicados | `tests/load/scenarios/soak.js` está pronto para rodar |
| Segregação de usuários SQL (runtime × migrations) | Compose local usa o usuário administrativo; triggers protegem DML, não DDL de administrador | Exigir usuário separado em produção; nota em [SECURITY_REVIEW.md](docs/SECURITY_REVIEW.md) |
| Rotação de segredos, backup/restauração de produção | Fora do ambiente local do desafio | [RUNBOOK.md](docs/RUNBOOK.md) registra os limites operacionais |
| Partidas dobradas, tracing OpenTelemetry, dashboards | Opcionais do enunciado, explicitamente fora do escopo do prazo | — |
| "Exactly-once" | Não é prometido em lugar nenhum: entrega é at-least-once; a deduplicação financeira durável é a inbox no PostgreSQL | Arquitetura intencional — ver [ARCHITECTURE.md](ARCHITECTURE.md) |

**Mitigações já existentes para A01/A02 no ambiente local** (não são prova dos
requisitos, apenas reduzem exposição): broker publicado somente em loopback,
HMAC por provedor impede efeito financeiro de mensagem forjada, inbox
persistente impede efeito duplo, provisionamento separado do runtime e clientes
SDK com identidades independentes.

A [auditoria final](docs/AUDITORIA_FINAL_ENTREGA.md) traz a matriz A01–A13,
comandos, evidências e limites; a [conformidade](docs/CONFORMIDADE_DESAFIO.md)
relaciona os requisitos oficiais. Consulte também o
[guia de entrevista](docs/GUIA_ENTREVISTA_JUNGLE.md).

## Índice

- [Status do desafio](#status-do-desafio-14102026) — inclui **[o que não foi possível demonstrar](#o-que-não-foi-possível-demonstrar--leia-antes-de-avaliar)**
- [Demonstração reproduzível](#demonstração-reproduzível)
- [Executar localmente](#executar-localmente)
- [Configuração](#configuração)
- [Dependências locais de desenvolvimento](#dependências-locais-de-desenvolvimento)
- [Entrega com Compose (três processos)](#entrega-com-compose-três-processos)
- [Mensageria SQS](#mensageria-sqs)
- [API autenticada](#api-autenticada)
- [Testes](#testes)
- [Arquitetura e decisões](#arquitetura-e-decisões)
- [Organização do repositório](#organização-do-repositório)
- [Documentação](#documentação)
- [Enunciado original](#enunciado-original)

## Demonstração reproduzível

Go 1.27.2, Docker Compose, Make, curl, jq e Python 3 instalados; k6 é necessário
somente para carga. Da raiz de um checkout limpo, em um terminal Bash:

```sh
export COMPOSE_PROJECT_NAME=jungle-demo-$(date +%Y%m%d%H%M%S)
export GATEWAY_URL=http://localhost:9080
export KEYCLOAK_URL=http://localhost:59080
export SQS_URL=http://localhost:49570
docker compose -f docker-compose.yml up --build -d
docker compose -f docker-compose.yml ps
bash scripts/smoke.sh
FAULT_DISPOSABLE=1 bash scripts/fault.sh
curl --fail "$GATEWAY_URL/health/ready"
```

Smoke exige 50 respostas para a mesma aposta (um efeito, 49 replays), saldo e
ledger reconciliados; depois testa literalmente duas apostas de 80.00 sobre
100.00, uma rejeição e saldo final 20.00. Fault executa SIGKILL controlado em
E/F e restart com pendência H, imprimindo IDs e evidências. O primeiro build
leva mais tempo; não prometer duração fixa da demonstração. Todos os segredos
no Compose são fictícios e exclusivos de desenvolvimento.

Ao terminar, **somente para esse projeto descartável**, a limpeza destrutiva
é `docker compose -f docker-compose.yml down -v`; apaga seu volume de teste.
Para manter os dados, use `stop`. Nunca executar limpeza contra um projeto
preexistente.

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
| `SQS_ENABLED` | `auto` | Gate da mensageria: `auto`/vazio ativa SQS quando `SQS_ENDPOINT_URL` está definida; `true` força (endpoint vazio = AWS SQS gerenciado da região — homologação); `false` desliga mesmo com endpoint |
| `SQS_ENDPOINT_URL` | vazia | Endpoint LocalStack, ex.: `http://localhost:4566`; no modo `auto` habilita o consumidor e exige banco e chaves de produtores |
| `SQS_CONSUMER_PROFILE` / `SQS_PUBLISHER_PROFILE` | vazios | Perfis AWS independentes do consumidor e publisher; vazios usam a cadeia padrão. Em ambiente com IAM, configure identidades restritas distintas |
| `SQS_PROVISIONER_PROFILE` | vazio | Perfil exclusivo do comando administrativo `provision-sqs` |
| `SQS_REGION` | `us-east-1` | Região do broker; credenciais vêm da cadeia padrão do SDK AWS |
| `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` | sem fallback | No ambiente local, fornecer explicitamente `test` / `test`; perfis e roles também são resolvidos pelo SDK |
| `AWS_SESSION_TOKEN` | opcional | Token de credenciais temporárias; deixar vazio no exemplo local `test` / `test` |
| `SQS_PROVIDER_KEYS` | obrigatória com SQS | Objeto JSON `providerId → chave HMAC` (mínimo 32 bytes por chave) |
| `SQS_QUEUE_PREFIX` | vazio | Opcional para testes isolados: até 32 letras, dígitos, `_` ou `-`; nomes oficiais por padrão |
| `PG_POOL_MAX_CONNS` | default pgx | 1–100; o Compose de entrega adota 24 (ver ADR 0002) |
| `OUTBOX_WORKERS` | `1` | 1–16 publishers concorrentes por réplica; o Compose de entrega adota 3 |

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

O runner oficial de schema é o comando `migrate`:

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
descartável e reaplicar com `make migrate-up` antes de iniciar a aplicação
(remover a migration de inbox/outbox deixa o runtime sem as estruturas
financeiras).

A aplicação usa `DATABASE_URL`; `TEST_DATABASE_URL` é exclusivo dos testes.

## Entrega com Compose (três processos)

A partir de um checkout limpo, um comando sobe o ambiente completo de entrega —
PostgreSQL, job de migrations (golang-migrate), Keycloak com o realm `jungle`,
LocalStack SQS, job administrativo `provision-sqs`, três réplicas independentes da aplicação e o gateway nginx —
isolado do ambiente de desenvolvimento por projeto e por portas de loopback:

```sh
docker compose up --build -d   # `-f docker-compose.yml` é o arquivo padrão do diretório
docker compose -f docker-compose.yml ps    # gateway, api-1/2/3, keycloak, localstack, postgres: healthy
```

Portas publicadas (apenas loopback):

- gateway `9080` → três réplicas (upstream resolvido por request no DNS do Docker);
- IdP `59080` → Keycloak público da máquina; o issuer validado pelas réplicas é
  `http://keycloak:8080/realms/jungle` (divisão dev permitida pelo §6.2);
- PostgreSQL `59532` em loopback, para o verificador independente.

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

## Mensageria SQS

### Provisionamento separado

A API não cria nem configura filas. `cmd/provision-sqs` usa a cadeia AWS do SDK
Go v2 e cria/reconcilia DLQ, entrada e eventos FIFO. No Compose, o job termina
com exit 0 antes das três APIs (`service_completed_successfully`); erro impede
seu início. Reexecutar o comando preserva as filas e reconcilia os atributos.

Para desenvolvimento fora do Compose de entrega, após subir `compose.dev.yaml`:

```sh
SQS_ENDPOINT_URL=http://localhost:4566 SQS_REGION=us-east-1 \
AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_SESSION_TOKEN= \
  go run ./cmd/provision-sqs
```

O runtime resolve URLs e valida FIFO, deduplicação explícita, redrive para a
DLQ e visibilidade de 30s; infraestrutura ausente/incompatível impede startup.
O consumidor consulta entrada/DLQ; o publisher consulta eventos com outro
cliente SDK. Perfis explícitos permitem separar as identidades mesmo no mesmo
processo Go. Perfis vazios preservam a cadeia padrão; dois clientes sem perfis
distintos podem resolver a mesma identidade e não comprovam menor privilégio.
As políticas de quatro papéis estão em `deployments/sqs/iam-policies.json`.

### Brokers avaliados (A01/A02)

O Compose LocalStack fornece identidades fictícias explícitas em configurações
locais, sem arquivo de credenciais versionado. **LocalStack Community não aplica
essas permissões**: separação do código não encerra A01. A perda de mensagens
após restart continua A02; recriar filas exige reexecutar o provisionador e
não recupera mensagens já confirmadas. Não remover volumes para recuperar.

| Broker local gratuito | A01 (autorização) | A02 (durabilidade) | Parecer |
| --- | --- | --- | --- |
| LocalStack Community 4.14.0 | ❌ sem enforcement | ❌ perde mensagens no restart | mantido na entrega |
| MiniStack 1.5.27 | ❌ aceita anônimo/builtin forjado | ❌ SIGKILL perde tudo (snapshot no shutdown) | [reprovado](docs/AUDITORIA_MINISTACK_FINAL.md) |
| ElasticMQ 1.7.1 | ❌ não autentica | ✅ H2 por eventos sobrevive a SIGKILL | [reprovado p/ A01 + compat.](docs/AUDITORIA_A01_A02_LOCAL_FINAL.md) |

Para encerrar A01/A02 com evidência real existe a **homologação em AWS SQS
gerenciado**: ambiente isolado em `compose.homolog.yaml` (PostgreSQL e Keycloak
locais, broker gerenciado, sem endpoint hardcoded — `SQS_ENABLED=true` e o SDK
resolve a região), quatro roles IAM de menor privilégio assumidas via STS,
políticas de recurso nas filas e probes de aceitação versionados. Nenhum
recurso AWS é criado sem autorização explícita do operador; procedimento,
recursos, custos e limpeza em [docs/HOMOLOGACAO_AWS.md](docs/HOMOLOGACAO_AWS.md)
e [docs/AUDITORIA_AWS_SQS_FINAL.md](docs/AUDITORIA_AWS_SQS_FINAL.md)
(**pendente de execução autorizada**).

### Enviar uma aposta por SQS em desenvolvimento

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

O produtor de desenvolvimento assina e envia o corpo sem alterá-lo:

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

### Política operacional do consumidor

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

## Testes

### Verificar e compilar

```sh
make check        # go vet, go test e go test -race
make fmt          # gofmt
make build        # bin/server, bin/migrate e bin/provision-sqs
```

Os testes unitários verificam configuração, contrato de liveness, composição Fx, porta ocupada, liberação do listener, conclusão de requisições durante shutdown, gate do módulo de banco, invariantes de `Money`, `Wallet`, `WagerTransaction`, `WalletLedgerEntry` e eventos de domínio. Não são testes de integração financeira nem substituem os cenários obrigatórios com infraestrutura real.

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

### Integração real (PostgreSQL + Keycloak + SQS)

Os testes de integração usam a build tag `integration`, infraestrutura real
(sem mocks; testes sem `TEST_DATABASE_URL`/`TEST_OIDC_ISSUER`/`TEST_SQS_ENDPOINT`
são ignorados, não aprovados) e filas exclusivas por fixture:

```sh
docker compose -f compose.dev.yaml up -d
make check
go vet -tags=integration ./...
TEST_DATABASE_URL='postgres://jungle:jungle-local@localhost:55432/jungle?sslmode=disable' \
TEST_OIDC_ISSUER=http://localhost:58080/realms/jungle \
TEST_SQS_ENDPOINT=http://localhost:4566 \
AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_SESSION_TOKEN= \
go test -race -tags=integration ./... -count=1
```

Verificam HTTP+SQS com IdP real, redelivery, rollback por erro SQL na inbox,
commit confirmado com resposta perdida, rejeição terminal, conflitos,
autenticação, DLQ por três tentativas, readiness e shutdown com rollback.
Injeção de falhas na fronteira do commit **não equivale a matar/reiniciar
processos**: os kills literais são exercitados por
[`scripts/fault.sh`](scripts/fault.sh) e a demonstração de paralelismo entre
instâncias está em [`scripts/smoke.sh`](scripts/smoke.sh).
`scripts/quality-gates.sh` exige as três variáveis e executa os gates com
`-race -count=1/2`.

### Testes de carga com k6

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

### Falhas literais (Testes E/F/H)

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

## Arquitetura e decisões

### ProcessWager: garantias verificadas

- Duas BETs de `80.00` sobre `100.00`: uma processada, uma rejeitada, saldo `20.00` e um débito.
- 50 chamadas concorrentes da mesma BET: uma operação, um débito e dois eventos; 49 replays.
- Replay retorna saldo/versão originais, mesmo após novas movimentações. Mesma chave com conteúdo diferente ou mesmo ID externo com outra chave gera conflito.
- LOSS e rejeições não geram ledger; REFUND/ROLLBACK concorrentes não compensam a mesma referência duas vezes.
- Erro SQL na segunda gravação da outbox desfaz carteira, transação, ledger e primeiro evento.
- Uma carteira progride enquanto outra está bloqueada; reconciliação confirma os saldos após os cenários concorrentes e de reversão.

`Money` usa centavos em `int64` e recebe valores externos como `{"amount":"25.90","currency":"BRL"}`; regras, limites e erros estão em [ARCHITECTURE.md](ARCHITECTURE.md#money--primeiro-incremento-da-fase-1). `Wallet` mantém identidade, jogador, saldo, versão e timestamps em campos privados; a aplicação coordena escritas por `SELECT ... FOR UPDATE` e grava o ledger no mesmo commit. `WagerTransaction` implementa os estados e as regras dos cinco tipos externos mais a abertura interna `OPENING`; a política de compensação está na [ADR 0001](docs/adr/0001-compensacao-direta.md). `WalletLedgerEntry` é um tipo-valor imutável: direção, quantia positiva, saldo anterior/posterior consistentes e timestamp UTC; unicidade `(wallet_id, transaction_id)` e proteção contra edição/exclusão vêm do schema PostgreSQL. Os eventos tipados têm `eventId`, `correlationId` e payload com dinheiro em string; a gravação na `outbox_events` é atômica com as operações financeiras.

### Worker de referências pendentes

Com `DATABASE_URL` definida, todo processo executa um worker que retoma operações `PENDING_REFERENCE`:

- **Claim distribuído**: `SELECT ... FOR UPDATE SKIP LOCKED` sobre as linhas vencidas; múltiplas instâncias disputam sem bloquear nem duplicar.
- **Backoff exponencial** (1s → 5min) gravado em `attempt_count`/`next_attempt_at`; o prazo (`reference_deadline_at`, criação + `REFERENCE_TTL`) é fixado na primeira retomada.
- **Desfechos**: referência chegou e elegível → `PROCESSED` com saldo/ledger/eventos no mesmo commit; referência ainda pendente → reagendamento sem efeitos; referência terminal sem sucesso → `REJECTED` (`REFERENCE_NOT_PROCESSED`); prazo esgotado → `REJECTED` (`REFERENCE_NOT_FOUND`) com evento de rejeição, sem tocar a carteira.
- **Falha transitória**: rollback integral do claim deixa a pendência elegível de novo; nenhuma pendência depende da memória de um processo.
- **Shutdown**: `SIGTERM` cancela novos polls; o lote em andamento commita ou rola back, e o Fx encerra o worker antes de fechar o pool.

`WagerTransaction` continua expondo a consulta de pendências por `GET /wagering/transactions/:id`.

### Outbox publisher

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

Detalhes e evidências de integração: [ARCHITECTURE.md](ARCHITECTURE.md#outbox-publisher--fase-5-incremento-2).

### Engineering decisions & trade-offs

- **PostgreSQL como única fonte da verdade.** Memória, SQS FIFO e correlações em HTTP servem o plano de controle; a integridade financeira (identidade, saldo, ledger, inbox/outbox) é garantida por schema (índices únicos, CHECKs, triggers) — isso sobrevive a processos, replicação e reinício.
- **Locking pessimista por linha.** As carteiras são objetos concorrentes por natureza; `SELECT ... FOR UPDATE` no contexto da mesma transação do débito evita lost updates com ordem simples e previsível — carteiras diferentes travam linhas diferentes e progridem em paralelo.
- **Idempotência persistente (não em memória).** Dois índices únicos por provedor: `idempotency_key` e `external_transaction_id`. O vencedor do INSERT processa; os perdedores leem o resultado persistido, inclusive o saldo no momento do resultado (`result_balance_cents`, `result_wallet_version`), inclusive após reinício.
- **Inbox e outbox no mesmo commit.** Duplicação entre HTTP e SQS é resolvida pela inbox `(consumer, messageId, hash)` commitada junto; a outbox garante "nunca publicar antes do commit" e produz eventos com `eventId` estável.
- **Entrega at-least-once (modelo honesto).** SQS dedup tem janela limitada; o correto é republicar o mesmo `eventId` e delegar deduplicação ao consumidor fora da janela FIFO. Nada aqui promete "exactly once".
- **Dimensão de pool (A/B real, 13/10).** Perna limpa 100 VUs: `max(4, ncpu)=12` conns: 344–355 rps, p95 393–404 ms, p99 586–614 ms, espera de aquisição ~44 s média; MaxConns=24: 341 rps (par), p95 389 ms, p99 542–561 ms, espera ~9 s. Mais conns (36/48) **piorou** (saturação do Postgres). Adotado **PG_POOL_MAX_CONNS=24** no Compose de entrega com override por env.
- **Outbox publishers múltiplos (drain medido).** Backlog isolado de ~27k eventos: 1 worker/réplica drenou em 147,7 ev/s; 3 workers/réplica em **408,7–421,5 ev/s** (2,8×), sem violar leases. Adotado **OUTBOX_WORKERS=3** no Compose.
- **Gargalos observados (com evidência):** além do pool, o publisher de outbox não acompanha produção extrema (100 VUs geram ~700 ev/s de pico; com 1 worker por réplica o dreno isolado mediu 147,7 ev/s). Com 3 workers por réplica, o dreno medido (408–421 ev/s) passa a acompanhar a produção moderada. Alternativas rejeitadas: cache de saldo em memória (corretude não aceita), verificação check-then-act fora do banco (perde para os índices únicos) e OTel distribuído (diferencial opcional; fora do escopo do prazo).
- **Próximos passos em produção (fora do escopo do desafio):** alertas contínuos em `jungle_outbox_*` e divergências, dashboards, pool dimensionado por load-test em hardware dedicado, segredos em vault com rotação, revisão de incidentes e runbook ensaiado.

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

A imagem usa build multi-stage e executa com usuário sem privilégios. O Compose de entrega inclui PostgreSQL, Keycloak, LocalStack e três APIs.

## Organização do repositório

```text
cmd/server/                         entrada do processo
cmd/migrate/                        migrations golang-migrate (job de entrega)
cmd/provision-sqs/                  provisionamento administrativo das filas
cmd/send-wager/                     produtor de desenvolvimento (envelope assinado)
cmd/verifywallet/                   verificador financeiro SQL somente leitura
internal/application/               portas e casos de uso (command e query)
internal/bootstrap/                 configuração, composição Fx e lifecycle
internal/adapters/http/             servidor, contrato HTTP e autorização
internal/adapters/sqs/              consumidor FIFO, assinatura e provisionamento
internal/adapters/oidc/             verificação de access tokens do IdP
internal/adapters/postgres/         pool pgx, UnitOfWork, repositórios e reconciliador
internal/domain/money/              valor monetário exato e testes
internal/domain/wallet/             agregado de carteira e testes
internal/domain/wagering/           transações, estados e políticas
internal/domain/ledger/             lançamento imutável do ledger e testes
internal/domain/events/             eventos tipados de domínio e testes
internal/testsupport/               harness de integração (build tag integration)
migrations/                         schema versionado up/down (golang-migrate)
deployments/                        realm Keycloak, gateway, políticas IAM e configs de POC
tests/load/                         suíte k6 com verificação financeira independente
tests/broker/                       probes de aceitação de broker (MiniStack/ElasticMQ)
tests/homolog/                      probes de homologação AWS SQS (tag awshomolog)
docker-compose.yml                  entrega: PG + Keycloak + LocalStack + 3 APIs + gateway
compose.dev.yaml                    dependências locais de desenvolvimento
compose.homolog.yaml                homologação com AWS SQS gerenciado (opt-in)
compose.ministack.yaml              POC MiniStack isolada (reprovada)
compose.elasticmq.yaml              POC ElasticMQ isolada (reprovada p/ A01)
```

## Documentação

| Documento | Conteúdo |
| --- | --- |
| [ARCHITECTURE.md](ARCHITECTURE.md) | decisões, garantias e limites arquiteturais |
| [docs/CONFORMIDADE_DESAFIO.md](docs/CONFORMIDADE_DESAFIO.md) | matriz requisito → implementação → evidência |
| [docs/AUDITORIA_FINAL_ENTREGA.md](docs/AUDITORIA_FINAL_ENTREGA.md) | auditoria final com matriz A01–A13 |
| [docs/AUDITORIA_A01_A02_LOCAL_FINAL.md](docs/AUDITORIA_A01_A02_LOCAL_FINAL.md) | veredito local de A01/A02 (brokers avaliados) |
| [docs/AUDITORIA_MINISTACK_FINAL.md](docs/AUDITORIA_MINISTACK_FINAL.md) | POC MiniStack (reprovada) |
| [docs/HOMOLOGACAO_AWS.md](docs/HOMOLOGACAO_AWS.md) | procedimento de homologação AWS SQS |
| [docs/SECURITY_REVIEW.md](docs/SECURITY_REVIEW.md) | revisão de segurança e ameaças |
| [docs/RUNBOOK.md](docs/RUNBOOK.md) | operação e resposta a falhas |
| [docs/PERFORMANCE_REPORT.md](docs/PERFORMANCE_REPORT.md) | resultados reais dos testes de carga k6 |
| [docs/ARCHITECTURE_DIAGRAMS.md](docs/ARCHITECTURE_DIAGRAMS.md) | diagramas Mermaid dos fluxos reais |
| [docs/GUIA_ENTREVISTA_JUNGLE.md](docs/GUIA_ENTREVISTA_JUNGLE.md) | roteiro de demonstração e entrevista |
| [docs/adr/](docs/adr/) | decisões de arquitetura (ADR 0001/0002) |
| [docs/reference/](docs/reference/) | snapshot do enunciado e procedência |

O histórico completo de incrementos, evidências intermediárias e perguntas de
estudo está em [PLANO_SISTEMA_JUNGLE_GO.md](PLANO_SISTEMA_JUNGLE_GO.md) e em
[docs/audit/](docs/audit/). O módulo e o remoto `origin` usam o repositório
[hendelsantos/jungle-backend-challenge-go](https://github.com/hendelsantos/jungle-backend-challenge-go).

## Enunciado original

O clone solicitado fica em `references/backend-challenge-go` (ignorado pelo Git),
com histórico e remoto próprios. Uma [cópia de referência](docs/reference/README_DESAFIO.md)
é versionada junto à solução; sua [procedência](docs/reference/SOURCE.md) registra
o commit consultado.

Para recriar o clone local em outro checkout:

```sh
git clone https://github.com/junglegaming/backend-challenge-go.git references/backend-challenge-go
git -C references/backend-challenge-go checkout bdd9e70b65db9b374ee070bd3a4768661a888997
```

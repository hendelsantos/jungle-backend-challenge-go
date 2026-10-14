# Retomada — provisionamento SQS separado (14/10/2026)

Base: `74ab41b`, com alterações locais de provisionamento ainda sem commit.
Não foi localizado um arquivo de retomada no projeto; o trabalho seguiu o último
checkpoint do plano, preservando e completando o incremento local.

## Mudança e limites

`cmd/provision-sqs` cria/reconcilia as três filas. O servidor apenas descobre e
valida filas existentes; erros impedem sua inicialização pelo Fx. Consumidor e
publisher têm clientes SDK e perfis independentes. O Compose fornece perfis
fictícios e aguarda o job administrativo. Região omitida no comando usa
`us-east-1`, como a configuração da aplicação.

Testes de contrato verificam os perfis usados nas assinaturas, inclusive com
credencial administrativa ambiente presente, e propagação de erros. Integração
real verifica ausência de filas, reprovisionamento idempotente, preservação de
mensagem existente, recusa de configuração inválida sem alterá-la, reparo
administrativo e readiness após exclusão de fila.

Não houve mudança nas regras financeiras. **A01 continua parcial e A02 aberto**:
identidades distintas no cliente não aplicam IAM no broker; recriar filas não
recupera mensagens perdidas. Não foram usados AWS real ou serviços pagos.

## Verificações desta execução

Projetos exclusivos: `jungle-retomada-dev` (`compose.dev.yaml`) e
`jungle-retomada-delivery` (`docker-compose.yml`), ambos com volumes novos.
PostgreSQL 17, Keycloak 26.8.0 e LocalStack 4.14.0 reais.

| Verificação | Resultado |
| --- | --- |
| `make check` | exit 0: vet, unitários e race |
| `go vet -tags=integration ./...` | exit 0 |
| `make build` | exit 0, inclui provisionador |
| `gofmt`, `git diff --check`, Compose dev/entrega, doccheck, sintaxe shell/Python e JSON IAM | aprovados |
| `go test -race -tags=integration ./... -count=1 -json` | exit 0; 635 testes/subtestes pass, 0 fail, 0 skip |
| `go run ./cmd/provision-sqs` com região omitida, duas vezes | ambas exit 0, broker real |
| `docker compose -p jungle-retomada-delivery up --build -d` | exit 0; migrate/provision-sqs exit 0; três APIs saudáveis, PIDs distintos |
| `bash scripts/smoke.sh` | exit 0; 50 duplicatas, 1 débito/49 replays; cenário 80+80 sobre 100 termina com 20.00/1 rejeição |
| Gitleaks sobre diff staged | exit 0, nenhum vazamento detectado |
| `scripts/broker-recovery.py` no projeto descartável | exit 2: durabilidade reprovada, detalhes abaixo |

A contagem inclui testes pais e subtestes; pacotes sem testes não são casos
ignorados. A primeira execução da integração foi prematura: PostgreSQL ainda
inicializando e IdP ainda indisponível. Também revelou o teste antigo que
exigia região explícita; ele foi ajustado para validar a região padrão na
assinatura sem acessar broker externo. O resultado aprovado acima corresponde
à repetição completa após as dependências ficarem prontas.

Comando da integração aprovada:

```sh
TEST_DATABASE_URL='postgres://jungle:jungle-local@localhost:55432/jungle?sslmode=disable' \
TEST_OIDC_ISSUER=http://localhost:58080/realms/jungle \
TEST_SQS_ENDPOINT=http://localhost:4566 \
AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_SESSION_TOKEN= \
go test -race -tags=integration ./... -count=1 -json
```

## Recuperação com provisionador explícito

```sh
COMPOSE_PROJECT_NAME=jungle-retomada-delivery FAULT_DISPOSABLE=1 \
python3 scripts/broker-recovery.py
```

1. Broker pausado: readiness 503, dois eventos pendentes no PostgreSQL.
2. Broker retomado: ambos publicados, nenhuma pendência.
3. Antes do restart: 14 mensagens visíveis e 14 eventos publicados no banco.
4. Após restart: filas ausentes, readiness 503.
5. Job administrativo explícito e restart das APIs: readiness 200, zero
   mensagens visíveis, os mesmos 14 registros publicados. Perda: 14 mensagens.
6. PostgreSQL pausado: readiness 503; após retomar, readiness 200.

O exit 2 é uma reprovação da garantia de durabilidade, não um teste aprovado.
Logs locais: `/tmp/jungle-retomada-*` (não versionados). Kills E/F/H, carga,
govulncheck e IAM negativo não foram reexecutados neste incremento; suas
evidências anteriores não são apresentadas como novas execuções.

Próximo incremento: demonstrar autorização e durabilidade em um broker local
permitido pelo enunciado, avaliando MiniStack antes de qualquer migração.
Exigir negações reais por identidade e preservação após restart; não fechar
A01/A02 apenas por configuração ou testes de contrato SDK.

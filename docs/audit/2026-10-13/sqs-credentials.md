# Credenciais SQS — incremento de 13/10/2026

Base versionada: `f3c61c3`. Escopo: preservar a cadeia padrão AWS, explicitar
credenciais fictícias no ambiente local e estabilizar uma precondição temporal
no teste de lease da outbox. A01 continua aberto no broker.

## Evidências

- Regressão contra o adaptador local anterior, carregado por `go test -overlay`:
  perfil compartilhado e role de container não assinavam com a identidade
  selecionada; ausência de credenciais enviava a requisição com o fallback.
  Os três casos falharam como esperado antes da correção.
- Adaptador corrigido: ambiente, perfil compartilhado e endpoint de credenciais
  de container assinam com a identidade selecionada e incluem o token temporário.
  Ausência de credenciais produz erro, com zero requisições ao endpoint SQS.
  São testes unitários com `httptest`, não uma prova de autorização do broker.
- Integração SQS na árvore de trabalho: `-race`, exit 0 (66,137 s).
- Snapshot isolado dos arquivos selecionados para commit, sem as outras edições
  locais: vet normal e com tag integration, testes `-race -count=1`, build dos
  comandos, validação Compose, sintaxe shell e `scripts/doccheck.sh` aprovados.
- Primeira integração completa do snapshot: 605 casos/subtestes aprovados e uma
  falha em `TestOutboxDeliveryAbandonedLeaseAndRetryIntegration`: a lease de
  50 ms já havia expirado antes da consulta que a supunha ativa. Não foi uma
  falha de autenticação nem de exclusão de uma lease ainda válida.
- Incorporada a correção local do teste: lease de 30 s para a exclusão; expiração
  explícita no PostgreSQL para a recuperação; erros das consultas também falham
  o teste. O teste passou 20 vezes consecutivas com banco real e `-race`.
- Integração completa repetida no snapshot corrigido: **606 casos/subtestes
  aprovados, zero falhas e zero casos ignorados**, exit 0. A contagem inclui pais
  e subtestes, não representa 606 cenários independentes. Pacotes sem testes não
  são contados como casos ignorados.

## Reprodução

Infraestrutura real utilizada: PostgreSQL 17, Keycloak 26.8.0 e LocalStack
4.14.0 do ambiente de auditoria existente; tabelas/filas isoladas pelos testes.
Não houve reinício ou limpeza dos containers de entrega.

```sh
go test -race ./internal/adapters/sqs -run TestSQSClient -count=1

TEST_DATABASE_URL='postgres://jungle:jungle-local@localhost:56432/jungle?sslmode=disable' \
  go test -race -tags=integration ./internal/adapters/postgres \
  -run '^TestOutboxDeliveryAbandonedLeaseAndRetryIntegration$' -count=20

AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_SESSION_TOKEN= \
TEST_DATABASE_URL='postgres://jungle:jungle-local@localhost:56432/jungle?sslmode=disable' \
TEST_SQS_ENDPOINT=http://localhost:14566 \
TEST_OIDC_ISSUER=http://localhost:59081/realms/jungle \
  go test -race -tags=integration ./... -count=1 -json
```

As portas acima pertencem ao ambiente usado nesta validação. Os comandos padrão
para `compose.dev.yaml` estão no README. Credenciais mostradas são fictícias.

## Limites

Não foram demonstrados IAM de produção, bloqueio de chamadas anônimas pelo
broker, persistência após reinício do broker ou novas interrupções literais.
O controle de expiração por SQL testa o predicado de recuperação; não substitui
uma interrupção de processo. A01–A03 continuam abertos. A alteração do scanner
na CI, métricas e carga permanecem fora deste incremento.

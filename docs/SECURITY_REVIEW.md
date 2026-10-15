# Revisão de segurança — Jungle Backend Go

> Auditoria de segurança contínua (última: 14/10/2026).
> Ferramentas: Gitleaks (histórico completo), govulncheck no CI, revisão manual
> do parecer de autenticação/autorização/contratos.

## 1. Superfície e ameaças (STRIDE-resumed)

| Ameaça | Vetor | Controle implementado | Evidência |
| --- | --- | --- | --- |
| Falsificação de mensagem SQS | produtor forjando `providerId`/payload | HMAC-SHA256 por provedor obrigatório vinculado ao corpo; envelho inválido/permanent → DLQ; `OPENING` sempre rejeitada | `sqs/auth_test.go`, integração com broker real |
| Acesso entre provedores | JWT de provedor A operando na carteira B | `provider_id` vem do token (nunca do body); rota/consulta/replay/conflito isolados; `403`/`404` sem vazamento | `api_integration_test.go` (validação por banco: saldo/ledger/outbox intactos) |
| Replay de requisições | reuso da mesma chave/corpo | identidade persistente `(provider, idempotency_key)` e `(provider, external_id)`; replay legítimo devolve resultado armazenado; conteúdo divergente → `409` | `process_wager_integration_test.go` |
| Token inválido/expirado/audience errada | JWT falsificado ou de outro realm | JWKS com rotação, RS256 apenas, `iss/aud/exp/nbf/typ/azp`, clock padrão do go-oidc; cliente de 3 s da vida real teste expiração | integração real com Keycloak |
| Vazamento de credenciais | segredos no repositório | Gitleaks na CI + histórico completo (`no leaks found` 25 commits); segredos de desenvolvimento marcados como exclusivos do compose local | `ci.yml`, `docs/QA_RELATORIO_FINAL.md` |
| Dependências vulneráveis | CVEs em módulos Go | govulncheck no workflow com versão fixa | CI (fast job) |
| Exposição de métricas/logs | `/metrics` e logs contendo dados sensíveis | `/metrics` no loopback com labels enumerados (kind/status/reason); logs JSON sem tokens/salarios/PAN — correlation/message/transaction/wallet/provider ids apenas | `internal/observability`, revisão de log calls |
| Uso indevido de endpoints de administração | manipulação de carto/ledger via API | sem rota de mutação financeira além dos contratos oficiais; ledger físico append-only (triggers); runtime local usa o usuário administrativo do Compose; segregação de permissões SQL ainda não implantada | triggers de migration + matriz |
| Fila de eventos consumida repetidamente | eventos duplicados fiscal | eventId estável + dedup FIFO + responsabilidade do consumidor externo documentada (at-least-once); o sistema nunca promete exactly-once | `ARCHITECTURE.md` §outbox |

## 2. Credenciais e segredos

- **Produção:** nenhuma credencial real no repositório. Segredos reais devem
  ser providenciados pelo operador (vault/env do ambiente) e nunca comitados.
- **Desenvolvimento:** PostgreSQL `jungle/jungle-local`, segredos do realm
  `jungle` (`*-client-secret`) e chaves HMAC (`local-provider-*-signing-key`)
  são **exclusivos do Compose de desenvolvimento**, marcados em `README.md`,
  `.env.example` e nos provisionings. Gitleaks acompanha o histórico.
- **Escopo da verificação:** o scanner cobre os arquivos e o histórico do
  repositório; não constitui auditoria das credenciais da máquina do desenvolvedor.

## 3. Fronteira de confiança da mensageria

Mensagens SQS **não são autenticação**: o consumidor valida a assinatura HMAC
e vincula a assinatura ao `providerId` do corpo; ainda com produtor falso, as
suas regras do domínio restringem o alcance (nunca `OPENING`; escopos
player/wallet/round/valor/moeda verificados).

**Autorização do broker (limitação verificada em 13/10/2026).** O enunciado
requer acesso à mensageria controlado por credenciais e políticas do broker
(§2). No ambiente local isso **não é demonstrável**: LocalStack Community
4.14.0 não avalia IAM — sondas registraram HTTP 200 para `ListQueues`,
`SendMessage`, `ReceiveMessage` e `DeleteQueue` **sem header `Authorization`**
e com **assinatura forjada**, e `ENFORCE_IAM=1` não altera esse resultado
(enforcement de IAM é recurso Pro, conforme a documentação oficial do
LocalStack). Mitigações efetivas no ambiente de desenvolvimento: (1) o broker
é publicado somente em `127.0.0.1` (loopback), inalcançável de fora da
máquina; (2) a assinatura HMAC por provedor impede que uma mensagem forjada
produza efeito financeiro, mesmo que alguém a insira na fila; (3) a inbox
persistente impede efeito duplo de mensagens reinseridas.

**Camada de produção (artefato verificável, não testado localmente).** A
aplicação autentica no broker pela cadeia de credenciais AWS
padrão do SDK, sem fallback para `test/test` (ambiente, perfis e roles).
O Compose local fornece o par fictício explicitamente; isso não habilita IAM
no LocalStack Community. As políticas IAM de menor privilégio por papel —
produtor só envia à fila de entrada, consumidor só recebe/apaga/altera
visibilidade da entrada e envia à DLQ, publisher só publica na fila de
eventos, administração separada — e a política de recurso das filas com
`Deny` a qualquer principal não designado estão versionadas em
[`deployments/sqs/iam-policies.json`](../deployments/sqs/iam-policies.json).
Verificação prevista em AWS real (`aws iam simulate-principal-policy` ou
LocalStack Pro); **não alegamos que essa política foi testada no ambiente
local.**

**Credenciais do cliente (incremento de 13/10/2026).** O adaptador usa a cadeia
padrão do SDK AWS, sem fallback para `test/test`. O Compose, a CI e os comandos
locais fornecem o par fictício explicitamente. Testes isolados verificam
ambiente, perfil compartilhado, role de container, token temporário e falha
sem envio quando não existe identidade. Isso corrige a escolha da identidade
no cliente; não aplica políticas no broker nem encerra A01.

## 4. Procedimentos de resposta

Ver `RUNBOOK.md` (PostgreSQL/Keycloak/LocalStack indisponíveis, outbox
acumula, incidentes DLQ, divergências, pool e restrita réplica. Nenhuma ação
de correção financeira silenciosa: toda correção exige evidência, decisão
humana registrada e novo lançamento como trilha auditável.

## 5. Pendências e follow-up

- [ ] Rotação de segredos de dev ao encerrar o desafio (nenhum em produção).
- [ ] Avaliar `go-sec%`/`staticcheck` como job adicional da CI (P3).
- [ ] Assinatura detached das imagens de entrega se for publicar imagens (P3).

## Revisão A01 — 14/10/2026

Os papéis IAM são **templates, não configuração aplicada no LocalStack**.
Provisionamento foi separado em `cmd/provision-sqs`. O runtime usa dois clientes
SDK com perfis opcionais independentes; o consumidor lê entrada/DLQ e o publisher
lê/publica eventos. Nenhum deles cria/altera/exclui filas durante inicialização.
O Compose local usa perfis fictícios distintos; isso não habilita IAM no broker.
O provisionador só precisa CreateQueue, SetQueueAttributes, GetQueueUrl e
GetQueueAttributes sobre as três filas. DeleteQueue/PurgeQueue/ListQueues não
são necessários. O produtor precisa GetQueueUrl e SendMessage na entrada;
consumer recebe/exclui/altera visibilidade na entrada e envia à DLQ; publisher
envia eventos. Os dois últimos consultam atributos/URLs somente das suas filas.
A resource policy exemplificada não é aplicada pelo comando. Testes de contrato
HTTP do SDK demonstram seleção de perfis/propagação de AccessDenied, sem serem
prova de enforcement IAM real. A01 permanece parcial no LocalStack Community.

## Evidências da revisão final

Em 14/10, SendMessage anônimo com política Deny foi aceito pelo broker local
com ENFORCE_IAM=1. Não foi demonstrado bypass do OIDC ou da assinatura HMAC.
A01 permanece parcial. `govulncheck@v1.9.0` concluiu sem vulnerabilidades
alcançáveis reportadas. Gitleaks passou após exceção exata de uma chave
fictícia de httptest; a configuração não desabilita a regra genérica.
Ver [evidências e limites](audit/2026-10-14/README.md).

CI de implementação `56d5d76` aprovado nos dois jobs em 14/10:
[run 38536103961](https://github.com/hendelsantos/jungle-backend-challenge-go/actions/runs/38536103961).
Gitleaks reexecutado: 45 commits, nenhum vazamento detectado. Três alertas da
varredura de todo o diretório eram chaves de idempotência sintéticas em saídas
k6 ignoradas pelo Git; nenhuma exceção genérica adicionada. Os caminhos
versionados não incluem .env, binários, resultados temporários ou clone oficial.

## Avaliação MiniStack — 14/10/2026

O substituto local candidato (MiniStack 1.5.27, POC isolada, imagem com digest
fixado, `AUTH=true`, `PERSIST_STATE=1`) foi avaliado com probes de aceitação
versionados (`tests/broker`, tag `ministackpoc`) e **reprovado**: ver o
[parecer completo](AUDITORIA_MINISTACK_FINAL.md). Pontos de segurança
observados: (1) para usuários IAM nomeados o broker aplica políticas de verdade
(negações cross-queue com 403 e `SignatureDoesNotMatch` com segredo errado) —
controle que o LocalStack Community não tem; (2) o caminho root embutido aceita
requisições **anônimas** e AccessKeyID builtin (`test`, account-id) com segredo
**errado** com HTTP 200, tornando-o inaceitável como broker autorizador;
(3) a persistência é snapshot-no-shutdown: SIGKILL perde todas as mensagens
aceitas desde o último shutdown graceful. Migração interrompida; o MiniStack
não entra no Compose de entrega. Se usado para desenvolvimento, manter
restrito ao loopback. A CI apenas compila os probes; nunca os executa como
aprovação.

## Homologação AWS SQS — 14/10/2026 (preparada, não executada)

O caminho de encerramento de A01/A02 é a homologação contra o **SQS gerenciado
real**: quatro roles IAM de menor privilégio assumidas via STS (sem access keys
de longa duração), políticas de recurso `Deny` nas filas para qualquer
principal fora dos papéis, e probes de aceitação (`tests/homolog`, tag
`awshomolog`, somente compilados na CI) que registram a decisão real do broker.
Provisionamento via scripts com confirmação explícita (`JUNGLE_HOMOLOG_CONFIRM`),
limpeza restrita ao prefixo de homologação, nenhum segredo versionado. Detalhes
em [HOMOLOGACAO_AWS.md](HOMOLOGACAO_AWS.md) e
[AUDITORIA_AWS_SQS_FINAL.md](AUDITORIA_AWS_SQS_FINAL.md). **Nenhum recurso AWS
foi criado neste incremento; a execução aguarda autorização do operador.**

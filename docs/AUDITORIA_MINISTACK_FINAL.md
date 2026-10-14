# Auditoria final MiniStack — 14/10/2026

**Parecer: NÃO ADEQUADO. Migração interrompida.** O MiniStack 1.5.27 foi
avaliado em prova de conceito isolada e **reprovou os dois gates obrigatórios**:
o broker aceita operações anônimas e com credenciais builtin forjadas (A01), e
perde todas as mensagens aceitas quando o processo morre sem shutdown graceful
(A02). Nenhum teste foi enfraquecido para aprovar; nenhum resultado foi inventado.
A infraestrutura de entrega (LocalStack) permanece inalterada.

Base da avaliação: `150ac92f53a98ab680d7a138ef23cd172d56dfbb`, `main`. Nenhuma
mudança no núcleo financeiro. Contexto: [viabilidade inicial](VIABILIDADE_MINISTACK.md)
e [matriz A01–A13](AUDITORIA_FINAL_ENTREGA.md) (A01 parcial, A02 aberto).

## Ambiente da prova

Projeto Docker exclusivo e descartável `jungle-ministack-poc-1410-2021`,
imagem `ministackorg/ministack:1.5.27@sha256:4cb4f8c5c1b8e58e9a37af1c80f243de5b5cecf2e7b0461086d0dc7781d6642c`
(digest conferido), porta `127.0.0.1:14567`, `AUTH=true`, `PERSIST_STATE=1`,
`STATE_DIR=/var/lib/ministack` em volume próprio, `MINISTACK_REGION=us-east-1`,
conta `000000000000`. O Compose da prova
([`compose.ministack.yaml`](../compose.ministack.yaml)) nunca sobrepõe o Compose
de entrega. Probes em [`tests/broker/ministack_test.go`](../tests/broker/ministack_test.go)
(build tag `ministackpoc`), executadas com AWS SDK for Go v2 real (SigV4),
transporte restrito a loopback e observação do HTTP status de cada operação.
Scripts: `scripts/test-sqs-iam.sh` e `scripts/test-sqs-persistence.sh`.

## A01 — Autorização: **REPROVADO** (14 aprovados, 3 reprovações impeditivas)

Identidades IAM reais criadas via API (CreateUser/PutUserPolicy/CreateAccessKey)
com políticas de menor privilégio; fila de entrada, DLQ e eventos FIFO criadas
pelo provisionador. Matriz observada (HTTP e código de erro reportados pelo
broker em cada chamada):

| Principal | Ação | Recurso | Esperado | Observado | Veredito |
| --- | --- | --- | --- | --- | --- |
| provisioner | CreateQueue (DLQ) | entrada-dlq | Permitido | 200 | ✅ |
| provisioner | CreateQueue (entrada) | entrada | Permitido | 200 | ✅ |
| provisioner | CreateQueue (eventos) | eventos | Permitido | 200 | ✅ |
| provisioner | SetQueueAttributes | entrada | Permitido | 200 | ✅ |
| producer | SendMessage | entrada | Permitido | 200 | ✅ |
| producer | ReceiveMessage | entrada | Negado | 403 `AccessDeniedException` | ✅ |
| consumer | ReceiveMessage | entrada | Permitido | 200 | ✅ |
| producer | DeleteMessage | entrada | Negado | 403 `AccessDeniedException` | ✅ |
| consumer | DeleteMessage | entrada | Permitido | 200 | ✅ |
| publisher | SendMessage | eventos | Permitido | 200 | ✅ |
| publisher | SendMessage | entrada | Negado | 403 `AccessDeniedException` | ✅ |
| consumer | ReceiveMessage | eventos | Negado (cross-queue) | 403 `AccessDeniedException` | ✅ |
| consumer | DeleteQueue | entrada | Negado | 403 `AccessDeniedException` | ✅ |
| unknown-key | ReceiveMessage | entrada | Negado | 403 `InvalidClientTokenId` | ✅ |
| producer com segredo errado | SendMessage | entrada | Negado | 403 `SignatureDoesNotMatch` | ✅ |
| **anônimo** | SendMessage | entrada | **Negado** | **200** | ❌ |
| **builtin `test` + segredo errado** | SendMessage | entrada | **Negado** | **200** | ❌ |
| **account-id `000000000000` + segredo errado** | SendMessage | entrada | **Negado** | **200** | ❌ |

**Leitura honesta:** para usuários IAM nomeados o broker **aplica a política de
verdade** — nega cross-queue, nega ação fora da policy e valida a assinatura
SigV4 (`SignatureDoesNotMatch` com segredo errado). Isso é mais do que o
LocalStack Community faz. Porém, o caminho bypass embutido (`is_root_access_key`)
trata AccessKeyID vazio, `test`, chaves configuradas no ambiente e IDs de conta
como root **sem validar a assinatura**: um requester anônimo (sem header
`Authorization`) e um impostor com AccessKeyID `test` ou o número da conta
recebem **HTTP 200 em operações proibidas**. Pelo critério do desafio — qualquer
HTTP 200 em operação proibida reprova — **A01 não é corrigida pelo MiniStack**.
Os bypasses são comportamento do broker, não configuração; `AUTH=true` está
ativo na prova. Nenhum remendo no broker foi tentado.

## A02 — Persistência: **REPROVADO** (graceful OK, crash perde tudo)

Três mensagens enviadas com identidade conhecida e recebidas uma vez sem
DeleteMessage (simula crash pós-commit e pré-ack):

### Restart graceful (compose restart -t 30) — aprovado

| Mensagem | MessageId | GroupId | DedupId | Recuperação |
| --- | --- | --- | --- | --- |
| visible-A | `3b178f99-7c6c-420e-8579-63df734efc45` | ordered | `de5ed3f7-43dd-415c-9ef5-1b8f0e1d8eab` | corpo/grupo/dedup idênticos, `ApproximateReceiveCount=2` preservado |
| visible-B | `be7ceb62-2c57-4fe5-9e65-03b5a2ef12aa` | ordered | `1022ae10-53f2-4b80-9699-8473a47e3431` | idem, recebida após A (FIFO preservado) |
| inflight-C | `f5bd3e70-d221-41f0-a85f-c3891ec94afd` | inflight | `54baa74c-4751-4857-942d-d264cd74cacc` | idem, `receives=1` |

A mensagem em voo não voltou a ser visível antes do timeout original (a fila não
"resetou" a visibilidade no restart), a ordem FIFO foi mantida e a deduplicação
sobreviveu: reenvio com o mesmo MessageDeduplicationId devolveu o MessageId
original e nenhuma nova entrega. **Durabilidade graceful existe.**

### SIGKILL (docker compose kill -s KILL, exit 137 confirmado) — **perda total**

Mensagens aceitas (HTTP 200 em todas): `4d157b97-1fa4-4af4-9a12-83a3e859402f`
(A), `e33e46d0-0bb3-4717-9056-929f0703eebd` (B), `485d57c9-1f3e-4aed-8c60-6de984236d60`
(C). Após o kill e restart: a fila **continuou existindo** (`GetQueueUrl`
resolveu a mesma URL — sem reprovisionamento), porém **0 das 3 mensagens foram
recuperadas em 40s de polling**. Resultado registrado pelo probe:
`accepted=3 recovered=0 lost=3`.

**Mecanismo confirmado:** a persistência é um snapshot gravado no shutdown
graceful. O teste forçou um snapshot-base **antes** de enviar; após o SIGKILL,
o broker restaurou exatamente esse baseline: filas presentes, mensagens
posteriores perdidas. Um crash de processo (OOM, kill do node, pânico) perde
tudo que foi aceito desde o último shutdown graceful — o cenário exato que o
desafio exige sobreviver (mensagem aceita pelo broker deve permanecer
disponível). **A02 não é corrigida pelo MiniStack.**

## DLQ/redrive e FIFO — funciona, com uma divergência

Com leases de visibilidade positivos (representativos do runtime real, que usa
lease), a mensagem `poison` (`aa4e26e3-293a-4321-bd80-173b0eba43b5`) foi
entregue 3 vezes pela fila de entrada, removida da fonte na 4ª tentativa e
encontrada na DLQ após restart graceful com corpo, MessageId, GroupId
(`dlq-group`) e DedupId (`56984a0c-1e21-4a60-a0ea-b0f1b21688e0`) idênticos.
**Divergência registrada:** `VisibilityTimeout=0` no ReceiveMessage **não é
honrado** — o broker aplica o default da fila (30s) e a mensagem não é
reentregue imediatamente, ao contrário do SQS real. O runtime do projeto nunca
usa 0; a divergência está documentada no probe e no teste.

## Regressão financeira — NÃO EXECUTADA (bloqueio)

A integração ao aplicativo (Fase 5) e a regressão completa (Fase 6) **não foram
executadas por decisão**: a missão só autoriza integrar o MiniStack se A01 e A02
passarem, e ambos reprovaram. Nenhum cenário financeiro (BET/WIN/LOSS/REFUND/
ROLLBACK, duplicatas, 80+80, E/F/H, três instâncias) foi alterado ou executado
contra o MiniStack. O núcleo financeiro e o Compose de entrega permanecem
intactos e continuam sendo o caminho validado.

## Conclusão por pendência

| Pendência | Classificação | Fundamento |
| --- | --- | --- |
| A01 — autorização do broker | **NÃO RESOLVIDA via MiniStack** | HTTP 200 para anônimo e para AccessKeyId builtin (`test`, account-id) com segredo errado, mesmo com `AUTH=true` e políticas de menor privilégio aplicadas. |
| A02 — persistência após restart | **NÃO RESOLVIDA via MiniStack** | SIGKILL perde 100% das mensagens aceitas desde o último shutdown graceful (`accepted=3 recovered=0 lost=3`); snapshot-no-shutdown não é durabilidade. |
| Migração do Compose de entrega | **INTERROMPIDA** | Critério da missão: não substituir a infraestrutura principal sem A01+A02 aprovados. |

O MiniStack **não é adequado** para encerrar A01/A02 neste projeto. Seus méritos
reais (enforcement IAM para usuários nomeados superior ao LocalStack Community,
FIFO/dedup/DLQ funcionais, restart graceful preservando estado) ficam
registrados acima, mas os gates obrigatórios reprovaram. Alternativas restam as
já registradas: LocalStack Pro (pago, não autorizado), AWS real (não autorizado)
ou aceitar a limitação documentada — A01/A02 permanecem pendentes no ambiente
local.

## Reprodução

```bash
export POC="jungle-ministack-poc-$(date +%m%d-%H%M)"
MINISTACK_POC_PROJECT="$POC" MINISTACK_POC_PORT=14567 \
  docker compose -f compose.ministack.yaml -p "$POC" up -d --wait
MINISTACK_POC_PROJECT="$POC" MINISTACK_POC_ENDPOINT=http://127.0.0.1:14567 \
  MINISTACK_POC_DISPOSABLE=1 scripts/test-sqs-iam.sh          # espera: FAIL (3 gates)
MINISTACK_POC_PROJECT="$POC" MINISTACK_POC_ENDPOINT=http://127.0.0.1:14567 \
  MINISTACK_POC_DISPOSABLE=1 scripts/test-sqs-persistence.sh   # espera: FAIL (sigkill)
docker compose -f compose.ministack.yaml -p "$POC" stop       # preserve evidence volume
```

Cada execução exige um **novo** projeto (o probe de persistência reinicia o
broker e consome o estado). A CI apenas compila/veta os probes
(`go vet -tags=ministackpoc` + `-run '^$'`); a aprovação de aceitação nunca
ocorre na CI. Os bypasses de root são um risco de qualquer ambiente que exponha
o MiniStack: mantê-lo restrito ao loopback se for usado para desenvolvimento.

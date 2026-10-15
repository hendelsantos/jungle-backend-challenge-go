# Auditoria final A01/A02 — alternativas locais gratuitas — 14/10/2026

**Parecer: A01 NÃO RESOLVIDO e A02 NÃO RESOLVIDO no ambiente local, sem
solução local gratuita viável encontrada.** Todas as alternativas compatíveis
com SQS FIFO e Docker local foram investigadas; as viáveis foram provadas com
execução real. Nenhum teste foi enfraquecido; nenhum resultado foi inventado;
o núcleo financeiro e a infraestrutura de entrega (LocalStack) permanecem
intocados. O único caminho comprovável continua sendo a homologação em AWS SQS
gerenciado, já preparada e pendente de conta/autorização
([HOMOLOGACAO_AWS.md](HOMOLOGACAO_AWS.md), [AUDITORIA_AWS_SQS_FINAL.md](AUDITORIA_AWS_SQS_FINAL.md)).

Base: `main` em `781b239`. Sem AWS real, serviços pagos ou credenciais de
produção. Nenhum volume/container pré-existente foi removido; os projetos de
POC (`jungle-ministack-poc-*`, `jungle-elasticmq-poc-*`) têm containers parados
e volumes preservados como evidência.

## Requisitos literais do enunciado

[README_DESAFIO.md](reference/README_DESAFIO.md):

- §19: "O acesso à mensageria deve ser controlado por **credenciais e políticas
  do broker**, preservando as validações de domínio no consumidor." → A01.
- §400: "Execute PostgreSQL, o IdP e **LocalStack ou MiniStack** em containers
  reais. Verifique migrations, constraints, imutabilidade do ledger,
  atomicidade financeira, inbox, reentrega, outbox concorrente, retry, DLQ e
  **recuperação após reinicialização**." → A02 (durabilidade lida em conjunto
  com o cenário de falha do broker reproduzido na auditoria).

## Versões e configurações investigadas

| Alternativa | Versão/configuração | Status |
| --- | --- | --- |
| LocalStack Community | 4.14.0 (release mais recente, 26/02/2026), `PERSISTENCE=1` + volume, `ENFORCE_IAM=1` | testado em produção de evidência anterior |
| MiniStack | 1.5.27 (release mais recente, 13/10/2026), `AUTH=true`, `PERSIST_STATE=1`, `STATE_DIR` + volume | POC executada em 14/10 |
| ElasticMQ | 1.7.1 (release mais recente, 05/04/2026), `messages-storage.enabled=true` (H2 por eventos) + volume, `sqs-limits=strict` | POC executada em 14/10 |
| goaws (Admiral-Piett) | master, MIT, push 05/10/2026 | análise documental |
| LocalStack Pro / AWS real | — | **excluídos** (pago / sem conta autorizada) |

Changelogs conferidos: LocalStack 4.14.0 não adiciona enforcement de IAM em
Community nem correção de persistência SQS; MiniStack 1.5.27 corrige SigV4 no
API Gateway, não no SQS, e não altera persistência. **Não existe versão mais
nova de nenhum dos dois com evidência de correção** — descartada a repetição
de tentativas sem justificativa técnica.

## A01 — testes de autorização realmente executados

Probes versionados (`tests/broker`, tags `ministackpoc`/`elasticmqpoc`),
observando o **veredito HTTP do broker** (não o erro do cliente):

### MiniStack 1.5.27 (`scripts/test-sqs-iam.sh`, 14/10)

17 observações: 14 aprovadas (negações reais cross-queue 403
`AccessDeniedException`, `SignatureDoesNotMatch` para usuário nomeado com
segredo errado, `InvalidClientTokenId` para chave desconhecida), **3 falhas
impeditivas**:

- anônimo → `SendMessage` **HTTP 200**;
- `AccessKeyID=test` com segredo errado → **HTTP 200** (bypass root builtin);
- `AccessKeyID=<account-id>` com segredo errado → **HTTP 200**.

### ElasticMQ 1.7.1 (`go test -tags=elasticmqpoc -run TestElasticMQAuthz`, 14/10)

- credenciais estáticas → HTTP 200 (esperado);
- **anônimo → HTTP 200**;
- **chave desconhecida → HTTP 200**;
- **segredo incorreto → HTTP 200**.

ElasticMQ **não autentica nem autoriza**: qualquer requisição é aceita. Não há
motor de políticas por identidade/fila (README oficial usa `x/x` como
credencial nos exemplos). goaws não documenta enforcement de credenciais nem
políticas (clone de dev-test, armazenamento em memória).

**A01: NÃO RESOLVIDO.** Nenhum broker local gratuito testado rejeita todas as
operações proibidas exigidas (anônimo, credencial forjada, cross-queue,
administração indevida). Separar clientes SDK, escrever JSON de política que o
broker ignora, autenticar só na API Go, HMAC e loopback são mitigações já
existentes — não prova de autorização do broker.

## A02 — persistência/recuperação: o que foi testado de fato

Seis cenários, sem confundir recuperação da aplicação com durabilidade do
broker:

| # | Cenário | Evidência (broker) |
| --- | --- | --- |
| 1 | Reinício da **aplicação** | E/H de `scripts/fault.sh`: redelivery sem débito duplo; broker não envolvido |
| 2 | Consumidor morto **antes do DeleteMessage** | E: mesma mensagem reentregue, inbox única, saldo exato |
| 3 | Publisher morto **após envio, antes do ACK da outbox** | F: attempts=2, mesmo eventId/payload |
| 4 | Indisponibilidade **temporária** do broker | 38 publicados após retorno; 2 pendentes recuperados (auditoria 14/10) |
| 5 | **Graceful** restart do broker | LocalStack: **perdeu 14 mensagens aceitas**; MiniStack: preservou 3/3 íntegras; ElasticMQ: preservou 3/3 |
| 6 | **SIGKILL** no broker | MiniStack: **perdeu 3/3** (`accepted=3 recovered=0`); ElasticMQ: **preservou 3/3** |

### ElasticMQ — evidência de durabilidade (14/10)

`messages-storage.enabled=true` grava eventos em H2 em runtime. Resultados
(`tests/broker`, tag `elasticmqpoc`, volume nomeado):

- graceful: 3/3 mensagens recuperadas com id/corpo/grupo/dedup idênticos,
  `ApproximateReceiveCount` preservado;
- SIGKILL (exit 137 confirmado): **3/3 mensagens recuperadas**, fila íntegra
  sem reprovisionamento.

**Durabilidade local gratuita existe e foi provada** — somente no ElasticMQ.

### Desvios de compatibilidade do ElasticMQ observados (mesma POC)

1. `SetQueueAttributes(ContentBasedDeduplication)` → `InvalidAttributeName`
   (400); no AWS real o atributo é configurável. O provisionador do projeto
   reconcilia esse atributo e falharia sem alteração — não alteramos o contrato.
2. Redrive à DLQ não preservou a identidade de forma consistente: numa execução
   o `MessageId` mudou; noutra, o `MessageDeduplicationId` foi reescrito com o
   `MessageId` da cópia.
3. Ordem observada na recuperação pós-restart (B, C, A) sugere desvio de
   bloqueio FIFO por grupo enquanto a mensagem anterior está em voo; não
   verificado com probe dedicado.

## Comparação das alternativas (Fase 3)

| Critério | LocalStack Community 4.14.0 | MiniStack 1.5.27 | ElasticMQ 1.7.1 | goaws |
| --- | --- | --- | --- | --- |
| SQS FIFO | ✅ | ✅ | ✅ (ver observação de ordenação) | parcial |
| Credenciais/políticas reais | ❌ (IAM enforcement é Pro) | ⚠️ usuários nomeados sim; **anônimo/builtin aceitos** | ❌ nenhum controle | ❌ não documentado |
| Durabilidade após falha abrupta | ❌ perde mensagens | ❌ snapshot só no shutdown | ✅ H2 por eventos | ❌ em memória |
| DLQ/redrive | ✅ | ✅ | ⚠️ altera identidade no redrive | parcial |
| Dedup/ordenação | ✅ | ✅ | ⚠️ ver desvios | parcial |
| AWS SDK Go v2 | ✅ | ✅ | ⚠️ rejeita `ContentBasedDeduplication` em `SetQueueAttributes` | parcial |
| Docker Compose | ✅ | ✅ | ✅ | ✅ |
| Manutenção/licença | ativa/Apache-2.0 | ativa/MIT | ativa/Apache-2.0 | ativa/MIT |
| Impacto no código existente | zero (atual) | migração rejeitada | exigiria enfraquecer o reconcile do provisionador | incompatível |

## Justificativas de aceite/rejeição

- **LocalStack Community 4.14.0:** mantido na entrega (suporte total dos
  testes existentes), mas **não resolve A01** (sem enforcement) **nem A02**
  (perda comprovada com `PERSISTENCE=1`). Não há versão mais nova com correção.
- **MiniStack 1.5.27:** rejeitado por execução — bypass root aceita
  anônimo/forjado (A01) e SIGKILL perde tudo (A02). Última versão já testada.
- **ElasticMQ 1.7.1:** **durabilidade A02 comprovada**, mas **A01 impossível**
  (não autentica), divergências de compatibilidade (reconcile do provisionador,
  identidade no redrive, suspeita de ordenação FIFO) e não é um dos brokers
  nomeados pelo enunciado (§400). Rejeitado como substituto da entrega.
- **goaws:** sem enforcement de credenciais/políticas e sem persistência
  documentada; rejeitado por capacidade.
- **Proxy SigV4 próprio à frente do broker:** não implementa SQS, mas seria um
  componente de segurança crítico mantido pelo projeto — a autenticação deixaria
  de ser **do broker** (leitura literal do §19), com risco de manutenção e
  superfície de ataque novos. Rejeitado: seria uma falsa solução para o
  requisito.
- **LocalStack Pro / AWS real:** fora das restrições (pago/sem conta). A
  homologação AWS SQS já está preparada para quando houver autorização.

## Impacto sobre a conformidade

| Achado | Classificação final local | Fundamento |
| --- | --- | --- |
| A01 — autorização do broker | **NÃO RESOLVIDO** (local) | nenhum broker gratuito testado rejeita todas as operações proibidas; matrizes acima |
| A02 — durabilidade do broker | **NÃO RESOLVIDO** (local, nos brokers do enunciado) | LocalStack/MiniStack perdem mensagens aceitas; durabilidade existe só no ElasticMQ, inviável pelos motivos acima |

Nada aqui altera a entrega: o Compose principal segue em LocalStack, os
testes/regressões existentes estão preservados, e **não se declara conformidade
integral**. A prova definitiva de A01/A02 depende do SQS gerenciado
(homologação preparada; pendente de conta AWS autorizada).

## Reprodução da POC ElasticMQ

```bash
export POC="jungle-elasticmq-poc-$(date +%H%M)"
ELASTICMQ_POC_PROJECT="$POC" docker compose -f compose.elasticmq.yaml -p "$POC" up -d
ELASTICMQ_POC_PROJECT="$POC" ELASTICMQ_POC_ENDPOINT=http://127.0.0.1:19324 \
  ELASTICMQ_POC_DISPOSABLE=1 go test -race -tags=elasticmqpoc ./tests/broker -count=1 -v
# esperado: TestElasticMQAuthz FALHA (sem autenticação), Durability PASSA,
# DLQ FALHA (identidade alterada no redrive)
ELASTICMQ_POC_PROJECT="$POC" docker compose -f compose.elasticmq.yaml -p "$POC" stop
```

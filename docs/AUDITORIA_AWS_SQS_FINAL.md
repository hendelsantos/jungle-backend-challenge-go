# Auditoria AWS SQS — A01/A02 (homologação)

**Estado: PREPARADA, NÃO EXECUTADA.** A infraestrutura de homologação está
pronta e validada localmente, mas **nenhum recurso AWS foi criado**: a criação
depende de autorização explícita do operador. Este relatório será atualizado
com as evidências reais assim que a homologação for executada; nenhuma linha
abaixo constitui prova de execução.

Contexto: o LocalStack Community não aplica IAM e perde mensagens no restart;
o MiniStack 1.5.27 foi reprovado em POC ([auditoria](AUDITORIA_MINISTACK_FINAL.md)).
A homologação usa o **SQS gerenciado real**, conforme
[HOMOLOGACAO_AWS.md](HOMOLOGACAO_AWS.md). Núcleo financeiro inalterado.

## A01 — Autorização (planejado; evidências pendentes)

**Identidades (roles STS, sem access keys de longa duração):**

| Papel | Prefixo de permissões | Política renderizada de |
| --- | --- | --- |
| `<prefixo>jungle-queue-admin` | CreateQueue, SetQueueAttributes, GetQueueAttributes, GetQueueUrl nas 3 filas | `deployments/sqs/iam-policies.json` |
| `<prefixo>jungle-wager-producer` | SendMessage + GetQueueUrl somente na entrada; Deny explícito de receber/excluir/administrar | idem |
| `<prefixo>jungle-wager-consumer` | Receive/Delete/ChangeVisibility na entrada; SendMessage à DLQ; Deny de administração | idem |
| `<prefixo>jungle-event-publisher` | SendMessage + GetQueueUrl + GetQueueAttributes somente em eventos; Deny de administração | idem |

Política de recurso em cada fila: `Deny` a qualquer principal fora dos quatro
papéis (`aws:PrincipalArn`).

**Teste automatizado:** `scripts/test-aws-sqs-iam.sh` →
`tests/homolog` (tag `awshomolog`), executando com STS AssumeRole real e
registrando HTTP status + código de erro **do broker** por operação:

- Produtor: envia ✅ / recebe ❌ / exclui ❌.
- Consumidor: recebe ✅ / exclui ✅ / DeleteQueue ❌ / lê fila de eventos ❌ (cross-queue).
- Publisher: publica eventos ✅ / envia à entrada ❌ (cross-queue).
- Anônimo ❌ / chave desconhecida ❌ / segredo adulterado ❌.
- Provisionador: CreateQueue ✅ / SetQueueAttributes ✅.

> Resultado: **PENDENTE** — será registrado aqui com a matriz observada.

## A02 — Durabilidade e recuperação (planejado; evidências pendentes)

**Nível broker** (`scripts/test-aws-sqs-durability.sh`):

1. Envio com identidade conhecida (corpo, MessageGroupId, MessageDeduplicationId).
2. Recebimento sem DeleteMessage (crash antes do ack).
3. Ausência de reentrega antes do visibility timeout (visibilidade honrada).
4. Reentrega após o timeout com MessageId/corpo/atributos idênticos e
   ApproximateReceiveCount=2.
5. Deduplicação FIFO na janela (mesmo MessageId, sem segunda entrega).
6. Redrive à DLQ após 3 recebimentos, identidade preservada.

**Nível aplicação** (`scripts/test-aws-sqs-app.sh`, com PostgreSQL de
homologação):

1. Consumidor em processo recebe aposta assinada da fila AWS → 1 débito.
2. Crash antes do DeleteMessage (recebimento cru sem delete) → reentrega pelo
   broker → inbox replay → **nenhum novo débito**; saldo/ledger reconciliados.
3. Janela outbox: publish aceito, ACK interrompido (PostSendPause) →
   republicação com mesmo eventId e bytes idênticos (at-least-once).

**Drills de processo** (`scripts/fault-homolog.sh`, somente réplicas da app;
o broker gerenciado nunca é morto): Testes E/F/H — kill antes de DeleteMessage,
kill entre publish e ACK da outbox, referência pendente cruzando HTTP→SQS.

> Resultado: **PENDENTE** — será registrado aqui com IDs e evidências reais.

**Limites da garantia (honesto):** o SQS é at-least-once; a janela de
deduplicação FIFO é de 5 minutos; a deduplicação financeira durável é a inbox
no PostgreSQL. A outbox não recupera mensagens que o broker confirmou e depois
perdeu (o ACK já foi registrado) — essa perda é responsabilidade do serviço
gerenciado, que oferece durabilidade de fila gerenciada pela AWS. Não há
promessa de exactly-once.

## Regressão (planejada)

Após autorização e subida do `compose.homolog.yaml`:

- `make check`, `gofmt`, `go vet`, `go test -race ./... -count=2`.
- Integração LocalStack (inalterada) + homologação AWS: cinco operações,
  50 duplicatas, 80+80 sobre 100, três instâncias, idempotência HTTP/SQS,
  reconciliação independente.
- `scripts/test-aws-sqs-iam.sh`, `test-aws-sqs-durability.sh`,
  `test-aws-sqs-app.sh`, `fault-homolog.sh`.

> Resultados: **PENDENTES** — serão registrados com comandos e saídas reais.

## Procedimento de autorização

1. Renderizar e revisar: `scripts/aws-homolog-render.sh`.
2. Autorizar a criação (operador): `JUNGLE_HOMOLOG_CONFIRM=APPLY-HOMOLOG scripts/aws-homolog-apply.sh`.
3. Executar os testes acima.
4. Limpeza restrita: `JUNGLE_HOMOLOG_CONFIRM=CLEANUP-HOMOLOG scripts/aws-homolog-cleanup.sh`.
5. Registrar as evidências neste documento e na matriz final.

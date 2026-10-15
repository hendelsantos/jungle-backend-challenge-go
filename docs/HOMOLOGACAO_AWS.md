# Homologação AWS SQS — A01/A02

Procedimento para validar autorização (A01) e durabilidade (A02) contra o
**SQS gerenciado real**, sem substituir o ambiente local de desenvolvimento
(LocalStack) e **sem criar nenhum recurso AWS sem autorização explícita**.

O MiniStack foi avaliado e reprovado (ver
[AUDITORIA_MINISTACK_FINAL.md](AUDITORIA_MINISTACK_FINAL.md)): aceita requisições
anônimas/builtin forjadas e perde mensagens em SIGKILL. O LocalStack Community
não aplica IAM e perde mensagens no restart. Portanto a homologação usa o
serviço gerenciado da AWS.

## Arquitetura de homologação

| Componente | Onde roda | Observação |
| --- | --- | --- |
| PostgreSQL | Docker local | volume `jungle-homolog-postgres`, porta `127.0.0.1:59542` |
| Keycloak | Docker local | realm `jungle`, porta `127.0.0.1:59090` |
| 3 réplicas da API | Docker local | gateway em `127.0.0.1:9090` |
| **SQS FIFO** | **AWS gerenciado** | endpoint resolvido pelo SDK da região; sem `SQS_ENDPOINT_URL` |
| Provisionamento | job único | perfil `*-jungle-queue-admin` |
| Consumidor/publisher | runtime | perfis `*-jungle-wager-consumer` / `*-jungle-event-publisher` |

A ativação é explícita: `compose.homolog.yaml` exige `SQS_ENABLED=true` e um
prefixo de fila de homologação. Sem essas variáveis nada sobe contra a AWS.

## Pré-requisitos (máquina do operador)

1. AWS CLI v2 instalada e autenticada com uma **identidade base** sua
   (perfil padrão ou `AWS_PROFILE`). O projeto não instala nem configura isso.
2. Permissões mínimas dessa identidade base:
   `iam:CreateRole`, `iam:DeleteRole`, `iam:GetRole`, `iam:PutRolePolicy`,
   `iam:DeleteRolePolicy`, `sqs:CreateQueue`, `sqs:DeleteQueue`,
   `sqs:SetQueueAttributes`, `sqs:GetQueueAttributes`, `sqs:GetQueueUrl`,
   `sts:AssumeRole`.
3. `python3` (renderização) e Docker Compose.

Nenhuma credencial é versionada. Os perfis AWS residem fora do repositório,
no diretório apontado por `JUNGLE_HOMOLOG_AWS_DIR` (montado somente leitura).

## Recursos que serão criados (revise antes de autorizar)

Renderize localmente (não chama a AWS):

```bash
export JUNGLE_HOMOLOG_ACCOUNT=<12 digitos>       # sua conta AWS
export JUNGLE_HOMOLOG_REGION=sa-east-1           # ou a região escolhida
export JUNGLE_HOMOLOG_QUEUE_PREFIX=homolog-<voce>-   # termina com '-'
scripts/aws-homolog-render.sh
```

O renderizador produz, em `.homolog-rendered/` (não versionado):

- **4 roles** `<prefixo>jungle-wager-producer|wager-consumer|event-publisher|queue-admin`
  com trust para o ARN de `JUNGLE_HOMOLOG_ASSUME_BY` (padrão: root da conta) e
  políticas inline de menor privilégio derivadas de `deployments/sqs/iam-policies.json`;
- **3 filas FIFO** `<prefixo>wager-transactions[.fifo]`, `-dlq` e `-events`
  com `RedrivePolicy` (maxReceiveCount 3 → DLQ) e `VisibilityTimeout=30s`;
- **política de recurso em cada fila** negando qualquer principal fora dos
  quatro papéis (prova de autorização no broker, não só no cliente).

Custo potencial: **IAM não tem custo**. SQS FIFO é cobrado por requisição
(pricing vigente: ~US$ 0,50 por milhão; 1M/mo grátis no free tier). Uma
campanha de homologação típica (testes de autorização + durabilidade + carga
curta) fica na faixa de **centavos a US$ 0,01–0,05**. Verifique o pricing
atual da sua região antes de aplicar.

## Aplicação (SOMENTE com sua autorização explícita)

```bash
export JUNGLE_HOMOLOG_CONFIRM=APPLY-HOMOLOG
scripts/aws-homolog-apply.sh
```

O script imprime cada ação, reconcilia políticas em roles já existentes e se
recusa a alterar recursos fora do prefixo. Ao final, ele emite o snippet de
`~/.aws/config` com os três perfis a assumir (`role_arn` + `source_profile`).

## Execução dos testes de homologação

```bash
export JUNGLE_HOMOLOG_AWS_DIR=~/.aws/jungle-homolog
export JUNGLE_HOMOLOG_PROVIDER_KEYS='{"provider-a":"<hmac-key-32+>","provider-b":"<hmac-key-32+>"}'
export JUNGLE_HOMOLOG_PROJECT=jungle-homolog-<voce>

docker compose -f compose.homolog.yaml up --build -d --wait

# A01 — matriz de autorização real contra o broker gerenciado
scripts/test-aws-sqs-iam.sh

# A02 — durabilidade do broker (redelivery, DLQ, dedup) e recuperação da app
scripts/test-aws-sqs-durability.sh
scripts/test-aws-sqs-app.sh

# Falha controlada: mata apenas os processos das réplicas (nunca o broker AWS)
JUNGLE_HOMOLOG_DISPOSABLE=1 scripts/fault-homolog.sh
```

## Limpeza (restrita aos recursos de homologação)

```bash
export JUNGLE_HOMOLOG_CONFIRM=CLEANUP-HOMOLOG
scripts/aws-homolog-cleanup.sh
```

Assume a role `*-jungle-queue-admin` para apagar as três filas (a política de
recurso bloqueia outras identidades) e depois remove as quatro roles e suas
políticas inline. Lista e confirma cada nome antes de agir; nada fora do
prefixo/base é tocado.

## Riscos e mitigações

| Risco | Mitigação |
| --- | --- |
| Custo residual por fila esquecida | prefixo obrigatório `homolog-*`; cleanup restrito; filas e roles são de baixo custo |
| Role com privilégio excessivo | políticas inline de menor privilégio renderizadas do template já revisado; trust restrito ao ARN do operador |
| Política de recurso trava o operador | cleanup assume a role de administração da homologação |
| Confundir com produção | nome do projeto Compose, prefixo de fila e roles carregam `homolog-`; nenhum recurso compartilhado com a entrega local |
| Credencial vazada | nenhum segredo versionado; diretório AWS externo montado somente leitura; HMAC de provedor fornecido em tempo de execução |

## Limites honestos

- Homologação **não é** prova de carga nem de produção.
- A deduplicação FIFO tem janela de 5 minutos; a deduplicação financeira
  durável continua no PostgreSQL (inbox), como na arquitetura atual.
- O teste de falha não mata o broker (serviço gerenciado); prova-se a
  janela commit→delete e publish→ack matando **os processos da aplicação**.

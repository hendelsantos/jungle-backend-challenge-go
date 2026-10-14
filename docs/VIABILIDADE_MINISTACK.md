# Viabilidade inicial do MiniStack — 14/10/2026

Base Jungle: `150ac92f53a98ab680d7a138ef23cd172d56dfbb`, main limpa,
sincronizada com origin/main após fetch. Nenhuma mudança financeira autorizada
ou necessária nesta investigação. LocalStack e seus volumes serão preservados.

## Proveniência e versão

O site oficial aponta para [ministackorg/ministack](https://github.com/ministackorg/ministack).
A [licença MIT](https://github.com/ministackorg/ministack/blob/v1.5.27/LICENSE)
está no repositório. Há atividade recente: release
[v1.5.27 de 13/10/2026](https://github.com/ministackorg/ministack/releases/tag/v1.5.27),
commit `30d7013e7d97093a4f44a1c0217f5e865de8fd30`.
Isso comprova atividade observada, não maturidade nem garantia de manutenção.

Imagem oficial obtida: `ministackorg/ministack:1.5.27`, digest
`sha256:4cb4f8c5c1b8e58e9a37af1c80f243de5b5cecf2e7b0461086d0dc7781d6642c`.
O Compose de prova fixa tag e digest, publica somente loopback e não monta o
socket Docker. O clone de inspeção fica fora do repositório Jungle.

## Capacidades anunciadas versus verificação necessária

| Item | Inspeção documental/código da versão selecionada |
| --- | --- |
| SQS FIFO | implementação distingue filas `.fifo`; validar ordenação real |
| MessageGroupId / MessageDeduplicationId | campos armazenados e devolvidos; cache de deduplicação; validar reprodução |
| VisibilityTimeout | mensagem guarda instante de visibilidade; validar mensagem em voo após restart |
| ReceiveMessage / DeleteMessage | handlers disponíveis; validar com SDK Go |
| GetQueueAttributes / SetQueueAttributes | handlers disponíveis; reutilizar o provisionador/adaptador |
| DLQ / redrive | encaminhamento por contagem de recebimentos; validar conteúdo e identidade |
| SigV4 | release adicionou verificação para IAM/STS; root tem exceção explícita |
| IAM | avaliador presente sob AUTH=true; precisa de matriz positiva/negativa real |
| Identidades independentes | IAM oferece usuários, access keys e políticas; criar fixtures só em memória |
| Persistência | snapshot no shutdown e carga no startup; não equivale a gravação antes do ACK |
| SDK Go v2 | SQS JSON e Query implementados; compatibilidade ainda exige execução |
| Limitações | root especial e snapshot no shutdown são riscos impeditivos potenciais |

Fontes de implementação: [SQS](https://github.com/ministackorg/ministack/blob/v1.5.27/ministack/services/sqs.py),
[IAM evaluator](https://github.com/ministackorg/ministack/blob/v1.5.27/ministack/core/iam_evaluator.py),
[persistência](https://github.com/ministackorg/ministack/blob/v1.5.27/ministack/core/persistence.py),
[lifecycle](https://github.com/ministackorg/ministack/blob/v1.5.27/ministack/app.py).
A [página oficial SQS](https://ministack.org/docs/services/sqs) também registra
ausência de métricas CloudWatch aproximadas e sensibilidade temporal dos delays.

## Decisão antes da prova

**Não aprovar migração.** Há implementação suficiente para uma prova isolada,
mas dois riscos concretos: `is_root_access_key` considera vazio, `test`, chave
configurada e IDs de conta como root; a assinatura desses callers não segue a
validação de IAM users. A persistência de SQS é coletada no shutdown.

`AUTH=true`, `PERSIST_STATE=1` e `STATE_DIR` foram conferidos no código da tag.
Somente habilitar essas opções não encerra A01/A02. A prova deverá tentar envio
anônimo, assinatura inválida, acesso fora da política e perda de estado em
SIGKILL, além do restart normal. Se houver aceite proibido ou perda confirmada,
interromper a migração e registrar o bloqueio; não remendar o broker nem
enfraquecer o teste para aprová-lo.

## Resultado da prova executada (14/10/2026)

Os dois riscos acima se confirmaram em execução real; ver o parecer completo em
[AUDITORIA_MINISTACK_FINAL.md](AUDITORIA_MINISTACK_FINAL.md). Resumo: IAM é
aplicado para usuários nomeados (inclusive cross-queue e `SignatureDoesNotMatch`),
mas envio **anônimo** e AccessKeyId builtin (`test`, account-id) com segredo
errado receberam HTTP 200 — A01 reprovada. Restart graceful preservou 3/3
mensagens íntegras, mas **SIGKILL perdeu as 3 mensagens aceitas** (snapshot
somente no shutdown) — A02 reprovada. **Migração interrompida** conforme o
critério desta viabilidade; o Compose de entrega permanece com LocalStack.

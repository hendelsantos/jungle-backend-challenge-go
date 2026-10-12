# Conformidade com o desafio oficial

Fonte normativa: [README original preservado](reference/README_DESAFIO.md), commit
`bdd9e70b65db9b374ee070bd3a4768661a888997`; [procedência](reference/SOURCE.md).
Revisão de 12/10/2026 sobre a implementação `d95c7b2`.

**A entrega completa ainda não está pronta.** O plano define a ordem dos incrementos;
o enunciado define o comportamento e as evidências exigidas. Uma fase interna concluída
não significa que o requisito integrado correspondente foi demonstrado.

## Requisitos e evidências

“Implementado na camada” descreve código e testes existentes nessa camada. “Parcial”
significa que falta uma parte do requisito ou sua demonstração no ambiente exigido.
Os caminhos de testes são reproduzíveis pelos comandos do [README](../README.md).

| Exigência oficial | Situação | Implementação/evidência e trabalho restante |
| --- | --- | --- |
| §4: Go, módulos, Fx e domínio independente | Implementado na camada | `go.mod`, `Dockerfile`, `internal/bootstrap` e `internal/domain`; testes de composição e lifecycle. Handlers financeiros e workers ainda precisam entrar nessa composição. |
| §6.1: dinheiro exato, escala, moedas e overflow | Implementado na camada | `internal/domain/money`, testes e fuzzing; centavos `int64`, JSON monetário em string. Novos transportes devem preservar essas garantias. |
| §§5–6: carteira, estados, abertura e ledger | Implementado na camada | Domínio, `CreateWallet`, migrations e `create_wallet_integration_test.go`; abertura positiva atômica, zero sem lançamentos, ledger append-only. |
| §7: cinco operações, regras de referência e compensação | Implementado na camada | `ProcessWager`, políticas do domínio e `process_wager_integration_test.go`; incluindo LOSS sem movimento e insuficiência de reversão com código distinto. |
| §§5, 9: idempotência persistente e replay histórico | Parcial | Hash, índices únicos, conflito e resultado original testados no caso de uso com PostgreSQL. Faltam HTTP/SQS e reinicialização de processos. |
| §§5, 8: concorrência por carteira e ausência de lost updates | Parcial | `FOR UPDATE`, versão, testes 80+80, 50 duplicatas e carteiras independentes com conexões reais. Falta repetir com pelo menos três processos independentes. |
| §§5, 11: atomicidade financeira e eventos | Parcial | Carteira, transação, ledger e outbox compartilham commit; erro SQL intermediário confirma rollback integral. Inbox e publicação após commit ainda pendentes. |
| §§6.3, 7: referência ausente, backoff e limite de espera | Parcial | `PENDING_REFERENCE` e evento persistidos. Faltam worker, TTL/tentativas, reagendamento, chegada posterior, expiração e recuperação após reinício. |
| §2: IdP externo, autenticação e isolamento por provedor | Pendente | Implementar OIDC/Keycloak e testes reais. Filtro por `provider_id` no repositório não é autorização. |
| §9: operações e consultas HTTP | Pendente | Casos de uso financeiros existem; rotas, DTOs, respostas/erros e autorização ainda não. Ver tabela de endpoints abaixo. |
| §9: reconciliação em visão consistente | Parcial | `ReconcileWallet`, snapshot SQL e `query_integration_test.go`; faltam endpoint, log e métrica de divergência. Nunca corrigir saldo automaticamente. |
| §9: health público | Parcial | `/health/live` implementado e testado. Falta `/health/ready` verificar PostgreSQL e SQS. |
| §§2, 10: SQS, FIFO, DLQ, redrive e controle de acesso | Pendente | Provisionar LocalStack/MiniStack, credenciais/políticas e fronteira de confiança do produtor. Deduplicação FIFO não substitui a do banco. |
| §§6.5, 10: inbox e consumidor transacional | Parcial | Tabela de inbox existe; faltam processamento, hash da mensagem, retry/DLQ e remoção da fila somente após commit. |
| §11: publisher concorrente e recuperação da outbox | Parcial | Tabela e eventos tipados persistidos; faltam publicação, claim/lease, backoff, recuperação e republicação mantendo `eventId`. |
| §§4, 10: shutdown de workers | Pendente | Lifecycle HTTP/pool existe; implementar e testar interrupção de polling e conclusão/liberação do trabalho dos workers. |
| §12: observabilidade | Parcial | Logs JSON de bootstrap/HTTP existem. Faltam rastreabilidade das operações, métricas exigidas e diagnóstico de reconciliação, retries, DLQ e atraso da outbox. |
| §§4, 13, 15: migrations e ambiente reproduzível | Parcial | SQL `up/down` e harness de testes existem. Faltam runner operacional, instruções completas de reversão, Compose e provisionamento automático de IdP/filas. |
| §13: integração real e falhas | Parcial | PostgreSQL real já usado. Faltam IdP/SQS reais, três processos, interrupções após commit/publicação, reentrega e reinicializações. |
| §15: entrega a partir de checkout limpo | Pendente | Reproduzir `docker compose up --build`, tokens, chamadas HTTP, mensagens, testes e falhas usando apenas instruções versionadas. |

## Contratos HTTP que devem ser entregues

| Método e rota oficial | Acesso | Estado atual |
| --- | --- | --- |
| `POST /wallets` | Serviço interno autorizado | Caso de uso pronto; endpoint pendente |
| `GET /wallets/:walletId` | Serviço interno autorizado | Caso de uso pronto; endpoint pendente |
| `GET /wallets/:walletId/ledger?cursor=...&limit=50` | Serviço interno autorizado | Cursor e consulta prontos; endpoint pendente |
| `POST /wallets/:walletId/reconciliation` | Serviço interno autorizado | Caso de uso pronto; endpoint e observabilidade pendentes |
| `POST /wagering/transactions` | Provedor autenticado correspondente | Caso de uso pronto; endpoint pendente |
| `GET /wagering/transactions/:transactionId` | Provedor do registro | Consulta autorizada e endpoint pendentes |
| `GET /providers/:providerId/wagering/transactions/:externalTransactionId` | Provedor correspondente | Leitura SQL interna existe; consulta autorizada e endpoint pendentes |
| `GET /health/live` | Público | Implementado |
| `GET /health/ready` | Público | Pendente |

Qualquer acesso interno adicional às consultas de transação precisa de permissão
explícita documentada. O `providerId` autorizado deve vir da identidade validada;
body, rota e chave de idempotência não podem conceder acesso. Isso se aplica também
a respostas de replay, conflito e recurso inexistente.

## Critérios para fechar o próximo incremento

Fase 3: endpoints financeiros e OIDC, divididos em incrementos verificáveis.

1. Provisionar Keycloak com identidades locais de dois provedores e um serviço interno;
   obter tokens reais via `client_credentials`.
2. Validar assinatura, issuer, audience, validade temporal e permissões. Testar token
   ausente, inválido e expirado com o IdP real.
3. Associar provedor à identidade validada e impedir acesso cruzado, inclusive replay;
   operações de carteira ficam restritas ao serviço interno.
4. Expor os contratos oficiais preservando dinheiro em string, `Idempotency-Key`
   recebida e o caso de uso compartilhado; documentar respostas distinguíveis para
   entrada inválida, conflito, rejeição, pendência e indisponibilidade.
5. Testar a integração HTTP + IdP + PostgreSQL e ausência de efeitos/acesso em chamadas
   não autorizadas antes de considerar concluída a Fase 3.

## Decisões permitidas e limites de aceitação

- BRL-only é permitido pelo §6.1; o domínio carrega moeda e testa incompatibilidades.
- `pgx`, SQL explícito, locking pessimista e arquitetura hexagonal atendem às opções do
  enunciado. O domínio permanece independente de infraestrutura.
- A política de compensação direta única está documentada na [ADR 0001](adr/0001-compensacao-direta.md),
  conforme a exigência de explicar combinações de REFUND e ROLLBACK sem devolução dupla.
- Partidas dobradas, tracing e carga são opcionais. Não precedem obrigações pendentes.
- Nenhum requisito eliminatório do §14 pode ficar pendente na entrega final. A existência
  de testes locais não comprova autorização, funcionamento distribuído ou recuperação.

## Verificação desta revisão

O snapshot do enunciado e o README do clone local têm o mesmo SHA-256.
Foram inspecionados requisitos, implementação e testes existentes; `make check`
(`go vet`, `go test` e `go test -race`) passou. As evidências PostgreSQL mencionadas
acima são da execução do incremento `d95c7b2`, registrada no plano; essa integração
não foi reexecutada nesta revisão documental. IdP, SQS e três processos continuam
sem evidência de execução.

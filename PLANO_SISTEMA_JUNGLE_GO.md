# Jungle Gaming — Plano Mestre de Implementação Backend Go

> **Documento de trabalho para VS Code**  
> **Desafio:** Processamento Distribuído de Apostas em Go  
> **Origem:** <https://github.com/junglegaming/backend-challenge-go>  
> **Data do planejamento:** 12/10/2026  
> **Estado:** Fase 0 concluída; Fase 1 iniciada com `Money`. Validações e progresso registrados na seção 13. Demais itens da Fase 1 e Fases 2–6 pendentes.

---

## 0. Como usar este arquivo

Este documento transforma o README oficial do desafio em um plano executável. Coloque-o na raiz do repositório da solução como `PLANO_SISTEMA_JUNGLE_GO.md` e marque as tarefas à medida que código e testes comprovarem o resultado. **O README do desafio continua sendo a fonte normativa**: se houver conflito, releia o enunciado original.

**Objetivo:** entregar uma solução reproduzível, segura e explicável, não apenas uma demonstração que funciona em um único processo.

**Regras de trabalho com IA no VS Code**
1. Implementar uma tarefa pequena por vez e mostrar os arquivos modificados.
2. Explicar a intenção, invariantes e impacto de cada mudança.
3. Executar testes relevantes após cada incremento; nunca alegar que um teste passou sem executá-lo.
4. Não substituir PostgreSQL, SQS nem Keycloak por mocks nos testes de integração obrigatórios.
5. Não usar `float32`/`float64` em nenhum fluxo monetário, nem indireto na conversão de JSON.
6. Não inventar requisitos, credenciais, resultados de testes ou desempenho medido.
7. Preservar migrations e decisões existentes; alterações de schema após aplicação exigem nova migration.
8. Evitar abstrações desnecessárias: código idiomático em Go, domínio independente das bibliotecas de infraestrutura.
9. Manter `README.md` e `ARCHITECTURE.md` atualizados conforme a implementação evolui.
10. Se o escopo for reduzido por prazo, registrar a lacuna claramente: **não declarar atendido um requisito eliminatório incompleto**.

---

## 1. Visão de produto e escopo obrigatório

Construir um backend em Go capaz de abrir carteiras e processar operações `BET`, `WIN`, `LOSS`, `REFUND` e `ROLLBACK` por HTTP e SQS. Os dois caminhos devem compartilhar os mesmos casos de uso e persistência, protegendo a integridade monetária mesmo sob reentrega, concorrência, reinicialização ou falhas após commits.

### 1.1 Entidades centrais

- **Money:** valor em centavos `int64` e código de moeda, encapsulado, imutável no uso.
- **Wallet:** `id`, `playerId`, `currency`, saldo, `version`, criação e atualização.
- **WagerTransaction:** origem interna (`OPENING`) ou externa; metadados da operação; estado, referência, `failureCode`, resultado financeiro persistido.
- **WalletLedgerEntry:** lançamento imutável para cada movimentação efetiva.
- **Inbox:** deduplicação de mensagem/consumidor e marca de tratamento concluído.
- **Outbox:** evento persistido dentro do commit financeiro e publicado posteriormente.

### 1.2 Garantias inegociáveis

- Um único débito para uma operação repetida em HTTP, SQS ou ambos.
- Nenhum saldo negativo, inclusive com três processos independentes.
- Eventos publicados somente depois de commit, com IDs estáveis para republicação.
- Ledger append-only e reconciliação contra saldo armazenado.
- `PENDING_REFERENCE` retomado por worker após reinicialização, com prazo/tentativas e rejeição definitiva.
- Autenticação real por IdP externo e autorização por provedor; operações internas protegidas.
- Dinheiro sem ponto flutuante e sem arredondamentos silenciosos.
- Ambiente local com PostgreSQL, Keycloak e LocalStack/MiniStack reais.

### 1.3 Critérios oficiais de avaliação

| Critério | Pontos |
|---|---:|
| Integridade financeira | 20 |
| Concorrência | 20 |
| Idempotência | 15 |
| Mensageria e recuperação | 15 |
| Modelagem e arquitetura | 10 |
| Testes | 10 |
| Observabilidade | 5 |
| Documentação | 5 |
| **Total** | **100** |

**Eliminatórios citados no README:** ausência de autenticação efetiva; acesso não autorizado; uso de float para dinheiro; saldo negativo; movimentação duplicada; idempotência só em memória; necessidade de uma única instância; publicar antes do commit; ausência de ledger auditável; usar apenas mocks no lugar da infraestrutura real nos testes.

---

## 2. Arquitetura proposta

**Estilo:** arquitetura hexagonal enxuta / ports and adapters, com aplicação modularizada em Uber Fx.

```text
      [Clientes HTTP autenticados]      [Produtores SQS autorizados]
                   |                              |
          [JWT + autorização]              [SQS FIFO + DLQ]
                   |                              |
          [HTTP handlers]                  [SQS consumer]
                   \                              /
                    \                            /
                     [ProcessWager (caso de uso)]
                               |
           +-------------------+-------------------+
           |                   |                   |
       [Domínio]       [Unit of Work SQL]    [Políticas de evento]
           |                   |
           |              [PostgreSQL]
           |          wallet/tx/ledger/inbox/outbox
           |                   |
           |         +---------+----------+
           |         |                    |
           |   [Reference Worker]   [Outbox Publisher]
           |         |                    |
           +---------+                  [SQS de eventos]
```

**Regra:** o domínio nunca importa `fx`, `pgx`, `net/http`, SDK AWS, Keycloak ou infraestrutura. Handlers e workers apenas adaptam entradas; lógica financeira compartilhada fica nos casos de uso.

### 2.1 Tecnologias escolhidas

| Responsabilidade | Escolha | Justificativa |
|---|---|---|
| Linguagem | Go (fixar versão estável verificada durante bootstrap) | Segurança de tipos; boa concorrência; stack exigida |
| DI e lifecycle | `go.uber.org/fx` | Exigência do desafio |
| HTTP | `net/http` (Go 1.22+ com roteamento moderno, se versão suportar) | Poucas dependências |
| Persistência | `pgx/v5`, SQL explícito, `pgxpool` | Locks, isolamento e transações visíveis |
| Migrations | `golang-migrate` com arquivos SQL up/down | Versionamento reproduzível |
| Autenticação | Keycloak OIDC + `client_credentials` | IdP externo, sem senhas locais |
| Mensageria | AWS SDK Go v2 + SQS FIFO + LocalStack | At-least-once; DLQ; ambiente local |
| Logs | `log/slog` em JSON | Campos estruturados |
| Métricas | Prometheus Go client | Métricas pedidas pelo enunciado |
| IDs | UUID v7 via biblioteca Go adequada | Identidades únicas e estáveis |
| Testes | `testing`, integração com containers reais | Comportamento verificável |
| Orquestração | Docker Compose (+ gateway HTTP para 3 réplicas) | Reprodutibilidade |

**Não iniciar pela infraestrutura inteira:** primeiro o núcleo `Money`/`Wallet` com testes; em seguida banco e API autenticada; depois mensageria/recuperação.

---

## 3. Estrutura de diretórios sugerida

```text
jungle-backend-go/
├── cmd/
│   └── server/
│       └── main.go
├── internal/
│   ├── domain/
│   │   ├── money/
│   │   │   ├── money.go
│   │   │   └── money_test.go
│   │   ├── wallet/
│   │   │   ├── wallet.go
│   │   │   └── wallet_test.go
│   │   ├── wagering/
│   │   │   ├── transaction.go
│   │   │   ├── transitions.go
│   │   │   ├── policy.go
│   │   │   └── transaction_test.go
│   │   ├── ledger/
│   │   │   └── entry.go
│   │   └── events/
│   │       └── events.go
│   ├── application/
│   │   ├── ports/
│   │   │   ├── repositories.go
│   │   │   ├── unit_of_work.go
│   │   │   └── messaging.go
│   │   ├── command/
│   │   │   ├── create_wallet.go
│   │   │   ├── process_wager.go
│   │   │   ├── retry_reference.go
│   │   │   └── reconcile_wallet.go
│   │   └── query/
│   │       ├── get_wallet.go
│   │       ├── list_ledger.go
│   │       └── get_transaction.go
│   ├── adapters/
│   │   ├── http/
│   │   │   ├── server.go
│   │   │   ├── middleware_auth.go
│   │   │   ├── wallet_handlers.go
│   │   │   ├── wagering_handlers.go
│   │   │   ├── dto.go
│   │   │   └── errors.go
│   │   ├── postgres/
│   │   │   ├── pool.go
│   │   │   ├── unit_of_work.go
│   │   │   ├── wallet_repository.go
│   │   │   ├── wager_repository.go
│   │   │   ├── ledger_repository.go
│   │   │   ├── inbox_repository.go
│   │   │   └── outbox_repository.go
│   │   ├── sqs/
│   │   │   ├── client.go
│   │   │   ├── consumer.go
│   │   │   └── publisher.go
│   │   └── oidc/
│   │       └── verifier.go
│   ├── workers/
│   │   ├── references.go
│   │   └── outbox.go
│   ├── observability/
│   │   ├── logging.go
│   │   └── metrics.go
│   └── bootstrap/
│       ├── config.go
│       ├── modules.go
│       └── lifecycle.go
├── migrations/
│   ├── 000001_create_wallets.up.sql
│   ├── 000001_create_wallets.down.sql
│   ├── 000002_create_wager_transactions.up.sql
│   ├── 000002_create_wager_transactions.down.sql
│   ├── 000003_create_ledger.up.sql
│   ├── 000003_create_ledger.down.sql
│   ├── 000004_create_inbox_outbox.up.sql
│   └── 000004_create_inbox_outbox.down.sql
├── deploy/
│   ├── keycloak/realm.json
│   ├── localstack/init-queues.sh
│   └── gateway/nginx.conf
├── tests/
│   ├── integration/
│   ├── e2e/
│   └── fault/
├── scripts/
│   ├── smoke.sh
│   ├── concurrency.sh
│   └── fault-injection.sh
├── docker-compose.yml
├── Dockerfile
├── Makefile
├── .env.example
├── .gitignore
├── go.mod
├── go.sum
├── README.md
├── ARCHITECTURE.md
└── PLANO_SISTEMA_JUNGLE_GO.md
```

A árvore é sugestão; manter pacotes pequenos, com dependências apontando para dentro. Não criar arquivos vazios apenas para cumprir a árvore.

---

## 4. Dinheiro e invariantes do domínio

### 4.1 `Money`

Representação imutável por campos privados (`cents int64`, `currency string`). A API usa sempre objeto JSON com `amount` **string**, não número.

Regras:
- Aceitar somente código de moeda explicitamente suportado, mantendo a moeda em todo objeto e registro. No incremento `Money`, o domínio reconhece `BRL` e `USD` (ISO 4217, escala dois) para testar incompatibilidade entre valores válidos; a futura aplicação será restrita a `BRL`. Decisão registrada em `ARCHITECTURE.md`.
- Entrada externa: `"0.00"`, `"25.00"`, `"25.90"` válidos; rejeitar vazio, `NaN`, infinito, expoente, mais de duas casas, negativo, caracteres estranhos e overflow.
- Política de normalização escolhida: **aceitar `"1"`, `"1.0"` e `"1.00"` como equivalentes**, sempre canonicalizar para `"1.00"`; registrar isso em `ARCHITECTURE.md`. Se simplificar, pode optar por aceitar somente duas casas, desde que documentado e testado; não alternar política após integração.
- Internamente, saldos nunca são negativos; diferenças de reconciliação e cálculos intermediários podem sê-lo.
- Todas as operações (`Add`, `Sub`, `Negate`, `Compare`) verificam moeda e overflow; `Negate(math.MinInt64)` é erro.
- `Zero(currency)`, `ParseDecimal`, `FromMinorUnits`, `FormatDecimal`, `Equal` e acesso somente leitura.
- Na reidratação, validar invariantes sem executar débito, crédito nem eventos.

**Atenção de implementação:** não fazer `strconv.ParseFloat` nem decodificar `amount` em `any` que resulte em `float64`. Fazer parsing textual da parte inteira e decimal, checando limites de `int64` antes da multiplicação/soma.

### 4.2 `Wallet`

- Um único `(player_id, currency)`; identidade UUID, moeda, saldo, versão (inicial `1`).
- Construtor de nova carteira diferente de `Rehydrate(...)`.
- `Debit` impede negativo; `Credit` impede overflow; ambas exigem mesma moeda.
- Apenas mudança efetiva do saldo incrementa `version`; `LOSS` não modifica saldo ou versão.
- Alterações efetivas exigem lançamento financeiro na mesma transação SQL.
- Abertura com zero cria somente carteira (`version = 1`). Abertura positiva cria `OPENING` processado, ledger de crédito, e dois eventos na outbox no mesmo commit (version continua `1`).

### 4.3 `WagerTransaction` e estados

```text
PENDING  ---------> PROCESSED    (terminal)
   | \------------> REJECTED     (terminal)
   |  \-----------> FAILED       (terminal)
   +--------------> PENDING_REFERENCE
                         |   \
                         |    \--> REJECTED (prazo/tentativas esgotados)
                         +--------> PROCESSED (após referência válida)
                         +--------> REJECTED (referência inválida/terminal)
                         +--------> FAILED (falha permanente auditável)
```

- `PENDING`: operação em avaliação. No caminho síncrono normal, criação e conclusão ocorrem no mesmo commit; `PENDING` não precisa ficar exposto.
- `PENDING_REFERENCE`: persistido quando referência requerida está ausente ou ainda não está `PROCESSED`; worker faz retry.
- `PROCESSED`, `REJECTED` e `FAILED` são terminais, inclusive após replay.
- Rejeições de negócio são erros tipados/códigos estáveis; não usar `panic`.
- `FAILED` reserva-se a erro permanente de infraestrutura diagnosticável quando possível persistir auditoria; **indisponibilidade transitória do PostgreSQL/SQS não vira automaticamente `FAILED`**: tentar novamente com segurança.

### 4.4 Políticas por operação

| Kind | Valor exigido | Movimentação | Regras adicionais |
|---|---|---|---|
| `BET` | Positivo | Débito | Saldo suficiente |
| `WIN` | Positivo | Crédito | Referência a aposta opcional; se informada, validar pertencimento |
| `LOSS` | Zero | Nenhuma | Não cria ledger, não altera versão; emite evento Processed |
| `REFUND` | Positivo | Crédito | Referencia `BET` processada; valor integral igual |
| `ROLLBACK` | Positivo | Inverso | Referencia `BET`, `WIN` ou `REFUND` processado; valor integral igual |
| `OPENING` | Zero ou positivo | Crédito se positivo | Somente operação interna de criação de carteira |

**Validação de referência:** buscar por `(provider_id, reference_external_transaction_id)` e conferir provedor, jogador, carteira, moeda e rodada; não aceitar referência de outra carteira/rodada/provedor. Quando `WIN` incluir referência opcional, validar também a aposta/rodada. `REFUND`/`ROLLBACK` sem referência: erro de contrato.

**Política de compensação (registrar como ADR):** cada transação processada pode receber **uma única compensação bem-sucedida direta** (`REFUND` ou `ROLLBACK`, conforme elegibilidade), independentemente do tipo. Isso proíbe tanto dois reembolsos de uma BET quanto um REFUND e um ROLLBACK da mesma BET. Um `ROLLBACK` de `REFUND` é permitido (desfaz o crédito do REFUND), mas continua sendo operação nova, com seu próprio lançamento, e exige saldo suficiente; não reabilita automaticamente a possibilidade de um segundo REFUND direto da BET original. Um ROLLBACK de WIN debita, e usa código de insuficiência de saldo diferente do `BET`.

**Códigos sugeridos de falha** (definir documento definitivo na implementação):
- `INSUFFICIENT_FUNDS` — aposta sem saldo.
- `REVERSAL_INSUFFICIENT_FUNDS` — reversão debitadora sem saldo.
- `REFERENCE_NOT_FOUND` — referência expirada sem aparecer.
- `REFERENCE_NOT_PROCESSED` — referência terminou sem sucesso.
- `REFERENCE_SCOPE_MISMATCH` — incompatibilidade entre operação e referência.
- `REFERENCE_AMOUNT_MISMATCH` — valor diferente do original.
- `ALREADY_REVERSED` — referência já compensada diretamente.
- `INVALID_OPERATION_AMOUNT` — valor incompatível com kind.
- `WALLET_CURRENCY_MISMATCH` — moeda incompatível.

Distinguir falhas de **contrato** (`400`), **autorização** (`401/403`), **idempotência** (`409`) e **rejeição de negócio persistida** (`422` ou contrato equivalente).

---

## 5. Banco de dados e migrations

### 5.1 Tabelas e constraints (especificação)

**`wallets`**
- `id uuid PK`, `player_id uuid NOT NULL`, `currency char(3) NOT NULL`, `balance_cents bigint NOT NULL`, `version bigint NOT NULL`, timestamps UTC.
- `UNIQUE(player_id, currency)`.
- `CHECK(balance_cents >= 0)`, `CHECK(version >= 1)`.
- Se o desafio for BRL-only, validar `currency = 'BRL'` na aplicação e, se apropriado, no schema.

**`wager_transactions`**
- `id uuid PK`, `wallet_id uuid NOT NULL FK`, `player_id uuid NOT NULL`, `kind`, `status`, `amount_cents bigint NOT NULL`, `currency`, timestamps.
- Externas: `provider_id`, `external_transaction_id`, `idempotency_key`, `payload_hash`, `round_id`, `game_id` obrigatórios.
- Internas `OPENING`: campos externos nulos; identidade única estável por carteira; origem marcada `INTERNAL`.
- `reference_external_transaction_id` e `reference_transaction_id` quando aplicáveis; `failure_code` para rejeição/falha.
- `result_balance_cents`, `result_wallet_version` e status persistido permitem replay do resultado original.
- `attempt_count`, `next_attempt_at` e `reference_deadline_at` para `PENDING_REFERENCE`.
- Índices únicos parciais para `UNIQUE(provider_id, idempotency_key)` e `UNIQUE(provider_id, external_transaction_id)` em operações externas; índices para estado/próxima tentativa.
- `UNIQUE(wallet_id)` quando `kind = 'OPENING'`; distinção interna/externa reforçada por `CHECK` de combinações de campos.
- Índice único **parcial** sobre `reference_transaction_id` para operações `REFUND`/`ROLLBACK` com `status = 'PROCESSED'` impede duas compensações diretas da mesma transação.
- `CHECK` para `LOSS` zero, outros tipos externos positivos e `OPENING` não negativo; moedas válidas; estados válidos.

**`wallet_ledger_entries`**
- `id uuid PK`, `wallet_id FK`, `transaction_id FK`, `direction`, `amount_cents`, `balance_before_cents`, `balance_after_cents`, `created_at`.
- `UNIQUE(wallet_id, transaction_id)`; todas as quantias positivas.
- `CHECK(balance_before_cents >= 0 AND balance_after_cents >= 0)`.
- `CHECK((direction='CREDIT' AND balance_after_cents=balance_before_cents+amount_cents) OR (direction='DEBIT' AND balance_after_cents=balance_before_cents-amount_cents))` — operações que excedem `BIGINT` geram erro SQL.
- Trigger de proteção que rejeita `UPDATE` e `DELETE`, além de política contra `TRUNCATE` para o papel da aplicação. Usuário da aplicação não deve ter privilégio para desabilitar triggers/alterar schema.

**`inbox_messages`**
- `consumer_name`, `message_id`, `payload_hash`, `received_at`, `completed_at`, `transaction_id` opcional.
- `PRIMARY KEY(consumer_name, message_id)`; deduplicação também compara hash em reentregas.
- O registro de conclusão deve ocorrer no mesmo commit dos efeitos duráveis, se existirem. Evitar gravar uma inbox como concluída antes do processamento.

**`outbox_events`**
- `event_id uuid PK`, `aggregate_id uuid`, `event_type`, `payload_json jsonb`, `occurred_at`, `attempts`, `next_attempt_at`, `published_at`, `leased_by`, `leased_until`, `last_error`, `created_at`.
- Índices para eventos ainda não publicados e prazo do próximo envio; payload criado uma única vez, sem regenerar `eventId` em retry.
- Opcional: `aggregate_version` para consumidores identificarem ordem lógica de saldo.

### 5.2 Transação financeira atômica

Para operações elegíveis, **um único `pgx.Tx`** deverá:
1. Confirmar/criar identidade durável da operação e resolver replays/conflitos.
2. Bloquear a linha de `wallets` com `SELECT ... FOR UPDATE`.
3. Resolver e, quando necessário, bloquear a referência (ordem de locks documentada; tratamento de deadlocks).
4. Aplicar regra de negócio ao agregado reidratado.
5. Gravar mudança do saldo com checagem de versão e condições de não negatividade.
6. Inserir `WalletLedgerEntry` se houve movimento, ou nenhum se `LOSS`/rejeição.
7. Marcar `WagerTransaction` com estado e `result_balance_cents`/`result_wallet_version` originais.
8. Persistir eventos tipados na `outbox_events`.
9. Em entrada SQS, gravar a conclusão da inbox na **mesma transação**.
10. `COMMIT` — só depois responder sucesso ou permitir `DeleteMessage` no SQS.

Se falhar antes do commit: `ROLLBACK`; não publicar evento nem confirmar a mensagem. Se o cliente perder a resposta após commit, replay deve recuperar o resultado sem novos efeitos.

**Exemplo de SQL de atualização defensiva** (ilustrativo; o código final deve tratar também crédito, retorno e erros):

```sql
BEGIN;
SELECT balance_cents, version
FROM wallets
WHERE id = $1
FOR UPDATE;

UPDATE wallets
SET balance_cents = balance_cents - $2,
    version = version + 1,
    updated_at = NOW()
WHERE id = $1
  AND balance_cents >= $2
  AND version = $3
RETURNING balance_cents, version;
-- Exigir exatamente uma linha; inserir ledger/tx/outbox ainda neste BEGIN.
COMMIT;
```

Não calcular saldo financeiro confiável em cache, sessão HTTP ou mutex local. Carteiras independentes devem usar locks de **linhas diferentes** e progredir simultaneamente. Em `40P01` (deadlock) ou `40001` (serialization failure), retentar **toda** a transação com limite, jitter e respeito ao `context.Context`.

### 5.3 Abertura e reconciliação

**Abertura** dentro de uma transação: inserir carteira `version=1`; se `initialBalance > 0`, gravar `OPENING` terminal, ledger de crédito de zero para saldo inicial e dois eventos de outbox. Uma segunda abertura do mesmo `(player_id,currency)` retorna conflito e não duplica crédito.

**Reconciliação:** transação `REPEATABLE READ READ ONLY` para obter carteira e ledger em um snapshot consistente. Saldo reconstruído = `SUM(CREDIT) - SUM(DEBIT)` incluindo `OPENING`. Diferença = saldo armazenado − calculado. Não modificar carteira. Usar soma SQL que evite overflow silencioso (`NUMERIC` intermediário e validação na conversão, se necessário); registrar divergência em JSON log e métrica.

**Cursor do ledger:** ordenação estável `(created_at, id)` e cursor opaco base64url com posição (e, se necessário, versão), sem depender de `OFFSET`.

### 5.4 Migrations e segurança de acesso

- SQL `up`/`down` versionado, ordem por dependências e rollback documentado.
- Usuário de migração separado do usuário de runtime.
- Runtime: permissões mínimas sobre tabelas; sem `ALTER TABLE`, `DROP`, `TRUNCATE` ou desabilitar triggers.
- Teste de integração deve tentar `UPDATE`/`DELETE` do ledger usando credenciais de runtime e confirmar bloqueio.
- Constraints protegem condições locais; a igualdade global entre saldo e soma do ledger depende também da transação e é monitorada por reconciliação.

---

## 6. Segurança: Keycloak, JWT e escopo de provedor

### 6.1 Identidades de teste (criadas automaticamente)

- `provider-a-client`: role `provider` e claim confiável `provider_id=provider-a`.
- `provider-b-client`: role `provider` e claim `provider_id=provider-b`.
- `wallet-internal-client`: role `wallet:manage` e `wallet:read` (ou scopes equivalentes), com acesso às operações internas.
- `wager-read-internal-client`, se necessário para operadores autorizados, com permissões explícitas.

Usar `client_credentials`, realm e clientes provisionados via import JSON/entrypoint; segredos **apenas locais em `.env.example`** como exemplos não sensíveis, nunca valores de produção.

### 6.2 Verificação por requisição

- Assinatura via JWKS do IdP, algoritmo permitido e rotação de chaves.
- Verificar `iss`, `aud` (ou audience esperada configurada), `exp`, `nbf`, client/role/scope.
- O `providerId` **autorizado vem do token validado**, nunca apenas do body.
- `POST /wagering/transactions`: se body `providerId` divergente da identidade, `403` sem acesso/efeito.
- `GET /providers/:providerId/...`: verificar o parâmetro contra o token antes de qualquer consulta.
- `GET /wagering/transactions/:transactionId`: consultar com filtro de provedor autorizado; evitar vazamento de existência de outros provedores (usar `404` para recurso fora de escopo quando apropriado).
- Replays e conflitos também respeitam autorização; não retornar resultados de outro provedor.
- `/wallets`, `/wallets/:id`, ledger e reconciliação reservados à identidade interna.
- `/health/live` e `/health/ready` públicos, sem dados confidenciais. `/metrics` em porta/rede interna.

**Ambiente local:** se URL pública do issuer difere do endereço interno do Keycloak no Compose, separar configuração de issuer validado da URL interna de JWKS (somente no ambiente de desenvolvimento), mantendo validação rigorosa do `iss`.

### 6.3 Autorização da mensageria

Usar credenciais/políticas de broker para limitar produtores/consumidores às filas permitidas. Na produção, privilégios IAM mínimos; no LocalStack explicar as limitações de simulação. Mensagens não são autenticação OIDC: conferir tipo, esquema, provedor permitido e referências de domínio; não aceitar `OPENING`. Se produtores externos publicarem na mesma fila, documentar a fronteira de confiança e exigir integração de ingresso que associe provedor à identidade real; **o campo `providerId` do payload sozinho não comprova a identidade do remetente**.

---

## 7. HTTP: contrato e comportamento

### 7.1 Endpoints

| Método e rota | Permissão | Responsabilidade |
|---|---|---|
| `POST /wallets` | Interno | Criar carteira com ou sem `OPENING` |
| `GET /wallets/:walletId` | Interno | Saldo/versão atuais |
| `GET /wallets/:walletId/ledger` | Interno | Cursor + `limit` |
| `POST /wallets/:walletId/reconciliation` | Interno | Reconstituir saldo sem alteração |
| `POST /wagering/transactions` | Provedor autenticado | Processar ou pendenciar operação |
| `GET /wagering/transactions/:transactionId` | Provedor do registro ou interno | Consultar status/código |
| `GET /providers/:providerId/wagering/transactions/:externalTransactionId` | Provedor correspondente ou interno | Consultar por ID externo |
| `GET /health/live` | Público | Processo ativo |
| `GET /health/ready` | Público | Dependências PostgreSQL e SQS disponíveis |

### 7.2 Resposta e códigos (proposta a documentar)

- `201` abertura realizada; `409` abertura duplicada.
- `200` operação processada com `transactionId`, `status`, `balance`, `idempotentReplay`.
- `202` pendência `PENDING_REFERENCE`, com ID/status; replay pendente não cria outra pendência.
- `422` `REJECTED` por regra de negócio, incluindo `failureCode`, transaction ID e resultado persistido.
- `400` JSON/valor/payload inválido ou kind `OPENING` externo.
- `401` token ausente/inválido/expirado; `403` escopo incorreto.
- `409` idempotency key reutilizada com conteúdo divergente, ou mesmo ID externo com outra key.
- `503` indisponibilidade temporária sem commit confirmado, com política segura de retry.
- `404` recurso ausente ou fora do escopo de acesso na consulta.

Incluir `correlationId` e código estável no corpo de erro. Quando o commit for incerto do ponto de vista do cliente, ele deve repetir com **mesma chave e mesmo conteúdo**; o servidor resolve pelo registro persistente.

### 7.3 Idempotência entre HTTP e SQS

Dois índices únicos: `(provider_id, idempotency_key)` e `(provider_id, external_transaction_id)`.

- Mesma key + mesmo hash canônico + mesmo ID externo → replay; não alterar saldo, ledger, versão nem outbox.
- Mesma key + conteúdo diferente → `409`.
- Mesmo ID externo + key diferente → `409`, mesmo se conteúdo for idêntico.
- Mesmo conjunto vindo de HTTP/SQS produz o mesmo hash e compartilha o mesmo registro de negócio.
- No replay `PROCESSED`, devolver **o saldo armazenado quando a operação terminou**, não o saldo atual.

**Hash:** normalizar entrada validada (incluindo `providerId`, `externalTransactionId`, `playerId`, `walletId`, `roundId`, `gameId`, `kind`, `amount` no formato `xx.yy`, `currency` e referência opcional), serializar como JSON canônico com chaves em ordem lexicográfica, aplicar SHA-256 em UTF-8. Excluir `Idempotency-Key`, `messageId`, timestamps do transporte e metadados HTTP/SQS. Definir política de campos ausentes vs `null`, normalizações e tratamento de chaves repetidas. Usar DTO tipado/strings para não introduzir `float64`.

**Concorrência de duplicatas:** confiar no PostgreSQL `UNIQUE`, não em consulta "check then insert" sem proteção. Uma transação rival espera o conflito de unicidade ser resolvido; só uma executa a alteração financeira. Tratar erros de conflito, lock e retry de maneira diferenciada.

---

## 8. Inbox, SQS, referências e outbox

### 8.1 Entrada SQS

Filas:
- `wager-transactions.fifo` — recebimento das solicitações.
- `wager-transactions-dlq.fifo` — falhas permanentes ou recebimentos esgotados.
- `wager-events.fifo` — publicação dos eventos da outbox (nome proposto; documentar).

**Mensagem:** envelope com `messageId`, `type=WagerTransactionRequested`, `occurredAt` e `data` como no enunciado. `data.idempotencyKey` é obrigatória. `MessageGroupId=walletId` como otimização de ordem por carteira e `MessageDeduplicationId=messageId`; ambos **não substituem** constraints e idempotência do banco.

Fluxo do consumidor:
1. `ReceiveMessage` long polling com `context.Context` e timeout.
2. Validar envelope, JSON e campos de negócio; rejeitar `OPENING`.
3. Abrir SQL tx; consultar/inserir inbox `(consumer_name, message_id)` e validar o hash.
4. Invocar o mesmo caso de uso `ProcessWager` que a entrada HTTP, **dentro da mesma Unit of Work**.
5. Persistir saldo/tx/ledger/outbox (ou pendência/rejeição) e `inbox.completed_at` no mesmo commit.
6. Somente depois do commit, chamar `DeleteMessage`. Se morrer antes do delete, receberá de novo e não duplicará o débito.
7. Erro transitório → rollback + sem `DeleteMessage`, visibility timeout e retry com backoff; mensagens permanentemente inválidas e tentativas esgotadas → DLQ conforme política documentada.

**Cuidado:** mensagem inválida antes de abrir transação precisa de política própria que preserve rastreabilidade; diferenciar descarte injustificado de envio à DLQ. O IdP também não pode ser ignorado nas rotas HTTP por existir um caminho SQS.

### 8.2 Referências que ainda não chegaram

- `REFUND` e `ROLLBACK` são aceitas como `PENDING_REFERENCE` se a referência ainda não chegou.
- Uma `WIN` com referência opcional, quando informada, também deve respeitar a validação do vínculo; adotar explicitamente comportamento de espera se a BET referenciada ainda não existir.
- Gravar `next_attempt_at`, `attempt_count`, `reference_deadline_at` e evento `WagerTransactionPendingReference` na outbox, tudo no commit.
- Worker em todas as instâncias disputa candidatos vencidos com `FOR UPDATE SKIP LOCKED`, em transações curtas; reprocessamento chama o caso de uso sem gerar nova identidade financeira.
- Exemplo de política inicial configurável: backoff exponencial com limite, jitter e **TTL de 24 horas**. Número exato de tentativas e TTL são decisões do projeto, não valores impostos pelo README.
- Referência encontrada em `PROCESSED` e escopo/valor corretos → processar; referência ainda em estado não terminal → reagendar; referência `REJECTED`/`FAILED` → `REFERENCE_NOT_PROCESSED`; referência inexistente após TTL → `REFERENCE_NOT_FOUND` e evento de rejeição.
- Worker interrompido após commit não pode perder pendências; todas ficam no banco.
- Não registrar um `PENDING` aceito sem alguma forma durável de retomada; se optar por processar integralmente no mesmo commit, não há `PENDING` intermediário exposto nesse caminho.

### 8.3 Outbox transacional

Eventos exigidos:
- `WagerTransactionProcessed`, inclusive `LOSS` e `OPENING` interno.
- `WagerTransactionRejected`, para rejeição definitiva.
- `WalletBalanceChanged`, apenas se saldo mudou, inclusive abertura positiva.
- `WagerTransactionPendingReference`, ao entrar em espera.

Envelope tipado: `eventId`, `eventType`, `aggregateId`, `correlationId`, `causationId?`, `occurredAt` RFC3339 UTC, `version`, `data` tipado. `WalletBalanceChanged` leva `walletId`, `transactionId`, `direction`, `money`, `balanceBefore`, `balanceAfter`, `walletVersion`.

Fluxo do publisher:
1. Selecionar eventos pendentes/reclamáveis usando `FOR UPDATE SKIP LOCKED` em transação curta.
2. Registrar lease (`leased_by`, `leased_until`) e incrementar controle de tentativas; commitar claim.
3. Enviar para `wager-events.fifo` com `MessageDeduplicationId=eventId` e `MessageGroupId=aggregateId`/`walletId` conforme contrato.
4. Confirmar `published_at` no banco; se o processo morrer entre envio e confirmação, haverá **republicação segura com mesmo `eventId`**.
5. Tratar falha de SQS com backoff/jitter, lease expira, outra instância assume; monitorar idade da outbox.

Apenas o ID é exatamente estável: entrega da outbox ao SQS é **at-least-once**, não exatamente uma vez. O consumidor externo deve deduplicar `eventId`, inclusive além da janela de deduplicação FIFO. Não prometer ordenação global entre publishers; o `walletVersion` permite ao consumidor identificar progressão lógica.

---

## 9. Uber Fx e shutdown correto

Usar módulos `fx.Module("config", ...)`, `fx.Module("database", ...)`, `fx.Module("security", ...)`, `fx.Module("application", ...)`, `fx.Module("http", ...)`, `fx.Module("workers", ...)` e `fx.Invoke(...)` para registrar servidor e workers, com construtores e dependências explícitas.

**`fx.Lifecycle`**:
- `OnStart`: validar configuração e dependências, preparar listener, iniciar servidor/consumidores/publishers com contextos derivados.
- `OnStop`: primeiro impedir novas requisições e novos polls do SQS; depois aguardar transações em andamento; se exceder prazo, cancelar e liberar visibilidade das mensagens para redelivery.
- Esperar goroutines via `WaitGroup`, logar finalização e fechar pool/clientes **após** os componentes que os utilizam.
- Readiness fica falsa antes do desligamento; liveness só indica processo vivo.
- Testes reais verificam iniciar/parar Fx sem goroutines presas ou conexões usadas após fechamento.

---

## 10. Observabilidade

**Logs JSON via `slog`:** `correlationId`, `messageId`, `transactionId`, `walletId`, `providerId`, `operationKind`, `status`, duração e código de erro quando presentes. Não logar tokens, senhas, cabeçalhos de autorização, dados pessoais sensíveis ou payloads financeiros completos.

**Métricas Prometheus:**
- `wager_operations_total{kind,status}`
- `wager_idempotent_replays_total`
- `wager_idempotency_conflicts_total`
- `wager_processing_duration_seconds`
- `wager_db_retries_total{reason}`
- `wager_sqs_redeliveries_total`
- `wager_dlq_total`
- `wager_outbox_pending`
- `wager_outbox_oldest_age_seconds`
- `wager_outbox_publish_retries_total`
- `wallet_reconciliation_divergences_total`

Evitar `walletId`, `playerId`, `providerId` dinâmicos como labels de alta cardinalidade em métricas, se desnecessários.

**Health:** `/health/live` responde status do processo; `/health/ready` verifica ping real no PostgreSQL e acesso ao SQS com timeout curto e sem bloquear outros requests.

---

## 11. Testes obrigatórios — matriz de aceitação

### 11.1 Unitários

- [x] `Money`: `0.00`, `1`, `1.0`, `1.00`, overflow, underflow, `MinInt64`, diferentes moedas, valores inválidos, JSON string, negação/comparação.
- [ ] `Wallet`: criação, reidratação, débito/crédito, saldo zero/insuficiente, overflow, versão e moedas diferentes.
- [ ] `WagerTransaction`: transições válidas, terminal imutável, pending reference, códigos estáveis.
- [ ] `BET`: positivo, insuficiência de saldo.
- [ ] `WIN`: positivo, crédito e vínculo opcional validado.
- [ ] `LOSS`: exclusivamente `0.00`, sem ledger e sem incremento de versão.
- [ ] `REFUND`: BET processada, mesma moeda/rodada/valor, compensação única.
- [ ] `ROLLBACK`: inversão de BET/WIN/REFUND, insuficiência de saldo de reversão.
- [ ] `OPENING`: somente origem interna, metadados externos nulos, zero vs positivo.
- [ ] Hash canônico HTTP/SQS igual, payload diferente com mesma chave em conflito.

### 11.2 Integração real

- [ ] Subir PostgreSQL + Keycloak + LocalStack via Compose; aplicar e reverter migrations em ambiente de teste.
- [ ] Testar constraints, triggers de imutabilidade, unicidade da carteira, saldo não negativo, abertura única.
- [ ] Testar `client_credentials` real, token ausente/inválido/expirado, isolamento provider-a/provider-b, replay de outro provedor bloqueado.
- [ ] Testar atomicidade: falha depois de gravar ledger mas antes de commit → rollback de tudo.
- [ ] Testar inbox com mesmo `messageId` e hash igual; reentrega com hash diferente detectada.
- [ ] Testar processamento da mesma transação via HTTP e SQS sem novo movimento.
- [ ] Testar inbox/outbox e comunicação real com SQS, redrive para DLQ, eventos tipados.
- [ ] Testar outbox com dois publishers, lease abandonado e evento com mesmo ID republicado.
- [ ] Testar `PENDING_REFERENCE` após reinicialização; chegada posterior e expiração.
- [ ] Testar Fx startup/shutdown (workers encerrando corretamente).

### 11.3 Concorrência e falhas (critérios oficiais)

- [ ] **Teste A:** 50 requisições paralelas da MESMA BET → um débito e um ledger.
- [ ] **Teste B:** saldo `100.00`, duas BET distintas de `80.00` simultâneas → uma processada, uma rejeitada, saldo `20.00`, um débito.
- [ ] **Teste C:** carteiras distintas processadas simultaneamente sem lock global.
- [ ] **Teste D:** repetir testes de concorrência com **pelo menos 3 processos independentes**, cada um com seu pool de conexão.
- [ ] **Teste E:** matar consumidor após commit e antes de `DeleteMessage`; reentrega não duplica.
- [ ] **Teste F:** matar publisher depois de SQS aceitar o evento e antes de marcar publicado; replay mantém `eventId`.
- [ ] **Teste G:** refund/rollback chega antes da referência; depois resolver ou rejeitar após TTL.
- [ ] **Teste H:** reiniciar todas as instâncias; resultado idempotente, pendências e finanças continuam íntegros.
- [ ] **Teste I:** concorrer HTTP/SQS pelo mesmo external ID/key; um único resultado persistido.
- [ ] **Teste J:** verificar reconciliação ledger = saldo final após cada cenário.
- [ ] **Teste K:** `go test -race` nos testes aplicáveis.

**Instrumentação de falhas:** preferir failpoints **somente nos testes**, em pontos explícitos (`after_commit_before_sqs_delete`, `after_sqs_publish_before_outbox_ack`), nunca ativáveis em produção. Testes precisam confirmar efeitos no banco e recebimentos duplicados, não apenas comparar código HTTP.

**Execução:** unitários com `go test ./...` e `go test -race ./...`; integração por `go test -tags=integration ./...` e `go test -race -tags=integration ./...` após subir dependências, se adotar build tags; também `go vet ./...`, `gofmt` e smoke tests reproduzíveis.

---

## 12. Docker Compose e ambiente de desenvolvimento

Serviços planejados:

1. `postgres`: volume persistente, healthcheck e credenciais locais de exemplo.
2. `keycloak`: realm importado e três identidades de teste; healthcheck.
3. `localstack`: serviço SQS, criação automática de fila de entrada, DLQ com redrive e fila de eventos.
4. `api`: backend Go, sem mapeamento fixo de porta host para permitir escala horizontal.
5. `gateway`: Nginx ou outra camada local, mapeamento da porta HTTP host e balanceamento entre réplicas.
6. `migrate`: job de migrations (ou comando separado reproduzível).
7. `test-runner` (opcional): executa testes de integração/falhas dentro da rede Compose.

**Importante:** para três instâncias, não mapear a mesma porta host de `api` três vezes. Usar gateway ou três serviços/portas distintas. Configurar pool de conexão por réplica sem esgotar `max_connections` do PostgreSQL. Cuidar da diferença entre URL pública do Keycloak (`iss`) e endereços de container na rede Compose.

`.env.example` deverá conter, sem segredos reais: `DATABASE_URL`, `HTTP_ADDR`, `OIDC_ISSUER`, `OIDC_AUDIENCE`, `OIDC_JWKS_URL` (se necessário em desenvolvimento), `SQS_ENDPOINT`, `AWS_REGION`, URLs/nomes das filas, intervalos de polling, retry, TTL, `LOG_LEVEL` e prazo de shutdown.

---

## 13. Fases e entregáveis (ordem de implementação)

### Fase 0 — Preparação do repositório e ambiente
- [x] Instalar/verificar `go version`, Docker Compose e Git.
- [x] Criar repositório separado; adicionar o README original apenas como referência (não editar a fonte oficial).
- [x] Criar `go.mod` com versão exata, `.gitignore`, `.env.example`, `Makefile` e Dockerfile multi-stage.
- [x] `cmd/server` com Fx, configuração validada e `/health/live`.
- [x] Primeiro commit e `go test ./...` limpo.

**Pronto quando:** projeto compila; servidor responde health; DI inicializa e encerra.

**Registro de execução — 12/10/2026:** solução iniciada na raiz, com clone oficial intacto em
`references/backend-challenge-go` e snapshot versionado em `docs/reference/README_DESAFIO.md`
(commit de origem `bdd9e70b65db9b374ee070bd3a4768661a888997`). Go 1.27.2 fixado no módulo e Dockerfile;
Fx 1.24.0. `go test ./...`, `go test -race ./...`, `go vet ./...` e build nativo executados com sucesso.
Smoke do binário confirmou HTTP 200, logs JSON e saída zero após SIGTERM.
Build Docker e smoke em container também passaram: HTTP 200, usuário sem privilégios e saída zero no encerramento.
O próximo incremento é `Money`, conforme seção 16.3. Não há endpoints de negócio nem infraestrutura financeira implementados.

### Fase 1 — Domínio financeiro isolado
- [x] Criar `Money` e testes de parsing/overflow/moeda.
- [ ] Criar `Wallet` e testes de débito/crédito/versão.
- [ ] Criar transação, estados e políticas para seis kinds.
- [ ] Criar ledger entry e eventos tipados sem dependência de infraestrutura.
- [ ] Revisar invariantes e rodar `go test -race ./...`.

**Pronto quando:** todas as regras de negócio têm testes unitários e não usam SQL nem framework.

**Incremento Money — 12/10/2026:** implementado em `internal/domain/money`, sem dependências
de infraestrutura. Inclui construtores, parsing decimal, aritmética protegida, comparação,
formatação e JSON com valores em string. `make check` passou (`go vet`, testes e `-race`);
`go test ./internal/domain/money -cover` registrou 100% de cobertura de instruções.
Fuzzing por 10s em cada alvo passou: 516.908 execuções de aritmética e 552.245 de parsing,
comparadas com oráculos exatos de `math/big`. Esses números não são benchmarks nem provas
de correção completa. O próximo incremento é `Wallet`; os demais itens desta fase permanecem pendentes.

### Fase 2 — Persistência transacional e abertura
- [ ] Criar migrations: wallets, transactions, ledger, inbox, outbox.
- [ ] Definir repositórios e `UnitOfWork` baseada em `pgx.Tx`.
- [ ] Criar abertura positiva/zero de carteira atômica, com `OPENING` quando exigido.
- [ ] Testar `UNIQUE`, `CHECK`, trigger imutável e rollback de erros.
- [ ] Criar reconciliação em snapshot e leitura de ledger cursor.

**Pronto quando:** schema protege invariantes e abertura/reconciliação passam com PostgreSQL real.

### Fase 3 — OIDC, endpoints e operação síncrona
- [ ] Provisionar Keycloak; validar tokens, issuer/audience e roles.
- [ ] Restringir operações internas e escopo por provedor.
- [ ] Criar endpoints e contrato JSON/erros.
- [ ] Implementar `BET`, `WIN`, `LOSS` e idempotência persistente.
- [ ] Implementar `REFUND`, `ROLLBACK` e unicidade de compensações.
- [ ] Armazenar saldo/versão originais para replay.
- [ ] Testar as apostas `80.00+80.00` e 50 replays concorrentes no PG real.

**Pronto quando:** HTTP autenticado cumpre operações financeiras sem saldo negativo ou débito duplicado.

### Fase 4 — Referências pendentes
- [ ] Persistir `PENDING_REFERENCE` com deadline/backoff.
- [ ] Worker distribuído com `SKIP LOCKED` e shutdown seguro.
- [ ] Rejeitar referência não encontrada, inválida, terminal ou mismatched conforme política.
- [ ] Testar chegada posterior, expiração e reinicialização.

**Pronto quando:** nenhuma referência pendente depende da memória de um processo.

### Fase 5 — SQS, inbox e outbox
- [ ] LocalStack com entrada FIFO, DLQ, redrive, fila de eventos.
- [ ] Consumidor SQS com inbox e UnitOfWork compartilhada.
- [ ] `DeleteMessage` somente após commit; retry e erros permanentes.
- [ ] Publisher da outbox com concorrência, lease/backoff e event IDs estáveis.
- [ ] Testar cross-channel HTTP/SQS, falha após commit, falha entre publish/ack.

**Pronto quando:** todas as garantias de at-least-once e recuperação passam nos containers reais.

### Fase 6 — Três instâncias, observabilidade e entrega
- [ ] Expor três processos reais e confirmar paralelismo por carteira.
- [ ] Testar interrupções, retries, DLQ, auditoria e reconciliação final.
- [ ] JSON logging, métricas, liveness/readiness.
- [ ] Documentar `README.md`, `ARCHITECTURE.md`, ADRs e scripts de reprodução.
- [ ] Executar `gofmt`, `go vet ./...`, `go test ./...`, `go test -race ./...` e integração.
- [ ] Revisar eliminações, endpoints, `.env.example` e secrets ausentes.

**Pronto quando:** alguém consegue clonar, subir com Compose, obter tokens, fazer operações e reproduzir testes sem intervenção manual oculta.

---

## 14. Arquivos de documentação na entrega

### `README.md` — manual operacional

- Visão geral e pré-requisitos.
- Versões utilizadas (Go, Docker, Compose, imagens).
- Instruções de `docker compose up --build`.
- Configuração e exemplo de variáveis, credenciais apenas de teste.
- Provisionamento do Keycloak, token `client_credentials` e filas.
- Migrations `up` e `down` com avisos sobre perda de dados.
- Chamadas HTTP completas (`curl`) para abertura, BET, WIN, LOSS, REFUND, ROLLBACK, leituras e reconciliação.
- Publicação de mensagem SQS; consulta de eventos e DLQ.
- Testes unitários, integração, três processos, falhas e `go test -race`.
- Troubleshooting, shutdown e limitações conhecidas.

### `ARCHITECTURE.md` — decisões técnicas

- Diagrama, responsabilidades de pacotes, Fx modules e lifecycle.
- Modelo Money, representação `int64` e normalização exata.
- Modelo de dados e constraints; transação SQL e lock order.
- Política de idempotência, hash canônico e replay histórico.
- Política de reversões (inclusive `REFUND` de BET e `ROLLBACK` de REFUND).
- Máquina de estados, TTL e reprocessamento de referências.
- Inbox, outbox, eventos e garantias **at-least-once**.
- Autorização OIDC e confiança dos produtores SQS.
- Shutdown, observabilidade, decisões/alternativas e riscos.
- Limitações, interpretações e itens não concluídos.

---

## 15. Riscos e decisões a revisar antes de implementar

| Risco | Solução/experimento recomendado |
|---|---|
| Race entre HTTP e SQS | Índices únicos + uma UnitOfWork + teste de concorrência real |
| Saldo desatualizado em replay | Guardar `result_balance_cents` e versão no registro da transação |
| Perda de evento após commit | Outbox na mesma transação do ledger |
| Dois publishers enviarem igual | Lease/claim + mesmo eventId; consumidor deduplica |
| Referência futura | `PENDING_REFERENCE` durável + worker `SKIP LOCKED` + TTL |
| REFUND + ROLLBACK da mesma BET | Unicidade de compensação direta no DB e regras de domínio |
| Deadlock entre locks da carteira e referência | Ordem de aquisição consistente e retry limitado da transação |
| Corrupção manual do ledger | Trigger, roles de banco restritas e reconciliação |
| Autorização HTTP incompleta | Testes reais cross-provider e replay; scope interno separado |
| Mensagem SQS falsificando providerId | IAM/política de producer e fronteira de ingresso, além de regras de domínio |
| Keycloak issuer na rede Docker | Fixar issuer público e validação estrita; JWKS interno local controlado |
| Teste só com mocks | Compose real + suites marcadas para integração/falha |
| Go/Fx novos para o candidato | Começar simples, revisar cada commit e explicar decisões |

---

## 16. Primeiras ações ao abrir no VS Code

### 16.1 Verificar o computador

```bash
go version
docker --version
docker compose version
git --version
```

### 16.2 Criar projeto (diretório escolhido pelo candidato)

```bash
mkdir jungle-backend-go
cd jungle-backend-go
git init
go mod init github.com/SEU_USUARIO/jungle-backend-go
mkdir -p cmd/server internal/domain/money
code .
```

Trocar `SEU_USUARIO` pelo perfil de GitHub antes do primeiro commit. Fixar Go conforme versão instalada e suportada pelas bibliotecas; Dockerfile deve usar a mesma versão.

### 16.3 Primeiro incremento no VS Code

1. Criar `internal/domain/money/money.go` com `Money` e construtores seguros.
2. Criar `internal/domain/money/money_test.go` com tabela de casos (incluindo overflow e moeda incompatível).
3. Rodar `go test ./...` e `go test -race ./...`.
4. Só depois partir para `Wallet` e começar a conectar PostgreSQL.

### 16.4 Prompt de retomada para usar com assistente de código

> Estamos implementando a solução do desafio Jungle Gaming `backend-challenge-go`. Leia primeiro `PLANO_SISTEMA_JUNGLE_GO.md` e o README oficial. Siga a ordem de fases e não pule requisitos eliminatórios. **Tarefa atual:** [INSERIR TAREFA]. Antes de alterar código, explique os arquivos envolvidos, invariantes e teste de aceitação. Implemente somente essa tarefa com Go idiomático, arquitetura simples e testes. Nunca use ponto flutuante para valores financeiros; mantenha domínio independente de infraestrutura. Ao terminar, mostre o diff, comandos realmente executados, resultados observados e pendências. Não diga que algo foi testado sem execução.

---

## 17. Definição de sucesso final

A entrega será considerada pronta para revisão quando for possível:

1. Subir o ambiente com `docker compose up --build` a partir de checkout limpo.
2. Obter tokens reais do Keycloak e comprovar isolamento entre provedores.
3. Abrir uma carteira de `100.00 BRL` e observar `OPENING`/ledger/outbox.
4. Enviar duas BETs diferentes de `80.00 BRL` em paralelo: **uma `PROCESSED`, outra `REJECTED`, saldo `20.00`, um débito**.
5. Repetir a mesma operação 50 vezes por HTTP e SQS, sem novos efeitos.
6. Reembolsar/reverter integralmente com referência válida, impedir dupla compensação e registrar cada correção no ledger.
7. Processar reversão antes da referência, manter pendência após reinício e concluir/rejeitar por política documentada.
8. Matar processos em pontos críticos e recuperar inbox/outbox sem perder operações ou eventos.
9. Rodar ao menos três instâncias com conexões/memória independentes e confirmar progresso paralelo entre carteiras.
10. Reconstruir os saldos a partir do ledger e verificar diferença zero nos cenários sem injeção de corrupção.
11. Executar testes, race detector, vet e instruções de reprodução com resultados verificáveis.
12. Explicar todas as principais escolhas no `ARCHITECTURE.md` e, se necessário, oralmente.

**Última lembrança:** o objetivo é construir garantias demonstráveis. Uma versão menor mas íntegra, claramente documentada, é melhor do que uma versão aparentemente completa com código que duplica dinheiro ou expõe transações de outros provedores. Porém os requisitos eliminatórios do enunciado precisam ser atendidos para a entrega final.

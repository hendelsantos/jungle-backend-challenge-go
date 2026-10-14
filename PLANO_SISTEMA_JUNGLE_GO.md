# Jungle Gaming — Plano Mestre de Implementação Backend Go

> **Documento de trabalho para VS Code**  
> **Desafio:** Processamento Distribuído de Apostas em Go  
> **Origem:** <https://github.com/junglegaming/backend-challenge-go>  
> **Data do planejamento:** 12/10/2026  
> **Estado:** Fases 0, 1 e 2 concluídas; Fases 3 e 4 concluídas: `ProcessWager`, endpoints financeiros autenticados por OIDC/Keycloak verificados com IdP e PostgreSQL reais, readiness com ping do banco e worker distribuído de referências pendentes (`SKIP LOCKED`, backoff, TTL) verificado com PostgreSQL real. Migrations verificadas com trigger/CHECK automatizados, `pgx` (pool + `UnitOfWork`), abertura atômica, leitura de carteira, ledger com cursor e reconciliação em snapshot. Validações e progresso na seção 13. Fase 5 com consumidor SQS/inbox e publisher da outbox (claim/lease, backoff, republicação com mesmo `eventId`, falha publish/ack) e Fase 6 concluídas: Compose de entrega com três réplicas atrás do gateway, kills literais (`scripts/smoke.sh`/`scripts/fault.sh`), migrations com runner (`cmd/migrate`), otimizações com A/B (pool pgx 24 conns e 3 publishers por réplica, evidências em `docs/PERFORMANCE_REPORT.md`/ADR 0002), métricas obrigatórias, CI e documentação consolidada. Diferenciais opcionais (tracing, dashboards, partidas dobradas) fora do escopo.

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
11. Após cada incremento validado, atualizar a documentação, criar commit e enviar para `origin/main` em `hendelsantos/jungle-backend-challenge-go`, conforme autorização do usuário. Preservar o histórico remoto e confirmar o resultado do push.

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
- [x] `Wallet`: criação, reidratação, débito/crédito, saldo zero/insuficiente, overflow, versão e moedas diferentes.
- [x] `WagerTransaction`: transições válidas, terminal imutável, pending reference, códigos estáveis.
- [x] `BET`: positivo, insuficiência de saldo.
- [x] `WIN`: positivo, crédito e vínculo opcional validado.
- [x] `LOSS`: exclusivamente `0.00`, sem ledger e sem incremento de versão.
- [x] `REFUND`: BET processada, mesma moeda/rodada/valor, compensação única (política e integração SQL verificadas).
- [x] `ROLLBACK`: inversão de BET/WIN/REFUND, insuficiência de saldo de reversão.
- [x] `OPENING`: somente origem interna, metadados externos nulos, zero vs positivo, com ledger/eventos atômicos.
- [x] Hash canônico HTTP/SQS igual, payload diferente com mesma chave em conflito (integração com IdP/broker reais).

### 11.2 Integração real

- [x] Subir PostgreSQL + Keycloak + LocalStack via Compose; migrations aplicadas/revertidas (`compose.dev.yaml` + `cmd/migrate`).
- [x] Testar constraints, triggers de imutabilidade, unicidade da carteira, saldo não negativo, abertura única (integração PostgreSQL real).
- [x] Testar `client_credentials` real, token ausente/inválido/expirado, isolamento provider-a/provider-b, replay de outro provedor bloqueado.
- [x] Testar atomicidade: falha SQL na segunda escrita da outbox, depois do ledger → rollback de tudo.
- [x] Testar inbox com mesmo `messageId` e hash igual; reentrega com hash diferente detectada (integração SQS).
- [x] Testar processamento da mesma transação via HTTP e SQS sem novo movimento (duas ordens + concorrência).
- [x] Testar inbox/outbox e comunicação real com SQS, redrive para DLQ, eventos tipados.
- [x] Testar outbox com dois publishers, lease abandonado e evento com mesmo ID republicado (integração real com LocalStack e schema isolado; failpoint determinístico para o crash).
- [x] Testar `PENDING_REFERENCE` após reinicialização; chegada posterior e expiração (`scripts/fault.sh` Teste H + integração do worker).
- [x] Testar Fx startup/shutdown (workers encerrando corretamente; `bootstrap/lifecycle_test.go`).

### 11.3 Concorrência e falhas (critérios oficiais)

- [x] **Teste A:** 50 requisições paralelas da MESMA BET → um débito e um ledger.
- [x] **Teste B:** saldo `100.00`, duas BET distintas de `80.00` simultâneas → uma processada, uma rejeitada, saldo `20.00`, um débito.
- [x] **Teste C:** carteiras distintas processadas simultaneamente sem lock global.
- [x] **Teste D:** testes de concorrência com **pelo menos 3 processos independentes** (réplicas do Compose de entrega; distribuição 21/18/13 nos logs).
- [x] **Teste E:** consumidor morto entre commit e `DeleteMessage` (failpoint de integração + kill -9 via `scripts/fault.sh`).
- [x] **Teste F:** matar publisher depois de SQS aceitar o evento e antes de marcar publicado; replay mantém `eventId` (simulação determinística do crash entre claim e ack, com recuperação e republicação do mesmo `eventId` verificadas com LocalStack real; kill literal de processo fica para a Fase 6).
- [x] **Teste G:** refund/rollback antes da referência; resolução posterior e rejeição por TTL (integração do worker + `scripts/fault.sh`).
- [x] **Teste H:** reiniciar todas as instâncias; resultado idempotente, pendências e finanças íntegros (`scripts/fault.sh`).
- [x] **Teste I:** HTTP/SQS concorrentes pelo mesmo external ID/key; um único resultado persistido (integração com broker real).
- [x] **Teste J:** reconciliação ledger = saldo final verificada após cada cenário (integração + verificador independente `cmd/verifywallet`).
- [x] **Teste K:** `go test -race` nos testes aplicáveis.

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

`.env.example` deverá conter, sem segredos reais: `DATABASE_URL`, `HTTP_ADDR`, `LOG_LEVEL`, `OIDC_ISSUER`, `OIDC_AUDIENCE`, `REFERENCE_TTL`, `REFERENCE_WORKER_INTERVAL`, `SQS_ENDPOINT_URL`, `SQS_REGION`, `SQS_QUEUE_PREFIX`, `SQS_PROVIDER_KEYS` (nomes conforme `LoadConfig`) e, opcionalmente, `PG_POOL_MAX_CONNS`/`OUTBOX_WORKERS`.

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
- [x] Criar `Wallet` e testes de débito/crédito/versão.
- [x] Criar transação, estados e políticas para seis kinds.
- [x] Criar ledger entry e eventos tipados sem dependência de infraestrutura.
- [x] Revisar invariantes e rodar `go test -race ./...`.

**Pronto quando:** todas as regras de negócio têm testes unitários e não usam SQL nem framework.**Incremento Money — 12/10/2026:** implementado em `internal/domain/money`, sem dependências
de infraestrutura. Inclui construtores, parsing decimal, aritmética protegida, comparação,
formatação e JSON com valores em string. `make check` passou (`go vet`, testes e `-race`);
`go test ./internal/domain/money -cover` registrou 100% de cobertura de instruções.
Fuzzing por 10s em cada alvo passou: 516.908 execuções de aritmética e 552.245 de parsing,
comparadas com oráculos exatos de `math/big`. Esses números não são benchmarks nem provas
de correção completa. O próximo incremento é `Wallet`; os demais itens desta fase permanecem pendentes.

**Incremento Wallet — 12/10/2026:** implementado em `internal/domain/wallet`, com estado
encapsulado, UUIDs não nulos, timestamps UTC, criação na versão 1 e reidratação sem
reaplicar movimentos. Débito/crédito validam moeda, saldo e limites monetários/de versão;
erros e operações de valor zero preservam todo o estado. `make check` e `make build`
passaram. A cobertura de instruções do pacote foi 98,3%; o fuzzing de sequências passou
com 252.946 execuções em 10s. Unicidade, concorrência entre processos e ledger atômico
continuam pendentes de persistência. Próximo incremento: transação, estados e políticas.

**Incremento WagerTransaction — 12/10/2026:** implementado em `internal/domain/wagering`,
com construtores externo/interno, reidratação validada, estados terminais imutáveis,
resultado financeiro por valor e políticas para os seis tipos. Referências ausentes ou
pendentes suspendem o processamento; escopo/tipo/valor incompatível ou referência
terminal sem sucesso rejeitam a operação. Compensação direta única definida na ADR 0001.
`make check` e `make build` passaram; cobertura de instruções de 97,1% no pacote.
Fuzzing BET → REFUND → ROLLBACK passou com 308.540 execuções em 10s.
Não há ainda idempotência persistente, coordenação SQL, worker de retry ou garantias distribuídas.

**Incremento WalletLedgerEntry e eventos tipados — 12/10/2026:** implementado em
`internal/domain/ledger` e `internal/domain/events`, sem dependências de infraestrutura.
O lançamento é um tipo-valor imutável cujo construtor valida `balanceAfter = balanceBefore ± amount`
conforme a direção, quantia positiva, saldos não negativos, moedas coerentes, UUIDs não nulos e
timestamp em UTC. Os eventos são envelopes imutáveis com `eventId`, `eventType`, `aggregateId`
derivado dos dados, `correlationId`, `causationId` opcional (`uuid.Nil`), `occurredAt` em UTC,
`version` por tipo e payloads tipados; `MarshalJSON` fixa o contrato do payload da outbox
(dinheiro como string decimal, `causationId` omitido quando ausente). `gofmt`, `go vet ./...`,
`go test ./...` e `go test -race ./...` passaram. Cobertura de instruções: 100,0% em `ledger`
e 95,0% em `events` (restantes: marcadores de interface e ramos de defesa interna do
`Validate` já cobertos em parte via literais no teste do pacote). Unicidade
`(wallet_id, transaction_id)`, trigger append-only, tabela `outbox_events` e persistência
continuam pendentes da Fase 2. Próximo incremento: revisão final de invariantes da Fase 1.

**Revisão final da Fase 1 — 12/10/2026:** revisão de invariantes e verificação completa do
domínio. `gofmt`, `go vet ./...`, `go test ./...`, `go test -race ./...` e `make build`
passaram. Cobertura de instruções por pacote: `money` 100,0%, `ledger` 100,0%, `wallet`
98,3%, `wagering` 97,1%, `events` 95,0% (restantes em `events`: marcadores de interface
no-op e ramos de defesa interna do `Validate`). Evidências dos critérios eliminatórios:
nenhum `float32`/`float64` em qualquer arquivo Go do módulo; nenhum import de `fx`, `pgx`,
`net/http`, SDK AWS, Keycloak ou `database/sql` em `internal/domain`; `panic` ausente do
código de produção do domínio (apenas em helpers de teste); nenhum TODO/FIXME pendente.
Fuzzing executado por 10s em cada alvo sem falhas: aritmética de `money` 544.741 execuções,
parsing de `money` 358.871, sequência de movimentos de `wallet` 273.871 e
BET→REFUND→ROLLBACK de `wagering` 297.859; nenhum corpus novo gerado. Esses números não
são benchmarks nem prova de correção completa. A Fase 1 está concluída; o próximo
incremento inicia a Fase 2 com as migrations de `wallets`, `wager_transactions`,
`wallet_ledger_entries`, `inbox_messages` e `outbox_events`.

### Fase 2 — Persistência transacional e abertura
- [x] Criar migrations: wallets, transactions, ledger, inbox, outbox.
- [x] Definir repositórios e `UnitOfWork` baseada em `pgx.Tx`, incluindo `FOR UPDATE`, escritas financeiras e leituras de idempotência/referências.
- [x] Criar abertura positiva/zero de carteira atômica, com `OPENING` quando exigido.
- [x] Testar `UNIQUE`, `CHECK`, trigger imutável e rollback de erros.
- [x] Criar reconciliação em snapshot e leitura de ledger cursor.

**Pronto quando:** schema protege invariantes e abertura/reconciliação passam com PostgreSQL real.

**Incremento migrations — 12/10/2026:** criadas as migrations versionadas `up`/`down`
(convenção golang-migrate) em `migrations/`: `000001_create_wallets`,
`000002_create_wager_transactions`, `000003_create_ledger` e `000004_create_inbox_outbox`.
O schema materializa o PLANO §5.1: checks de saldo/versão/moeda e `UNIQUE(player_id, currency)`
em `wallets`; máquina de estados coerente em `wager_transactions` (presença de
`failure_code` equivalente a REJECTED/FAILED, par de resultado, pureza dos estados
pendentes, PROCESSED com resultado, FAILED sem resultado, resultado de OPENING igual à
quantia e versão 1), combinação interna/externa de metadados, formato do `payload_hash`,
valor por kind, índices únicos parciais de idempotência `(provider_id, idempotency_key)` e
`(provider_id, external_transaction_id)`, `UNIQUE(wallet_id) WHERE kind='OPENING'`,
unicidade de compensação direta (ADR 0001) em `reference_transaction_id WHERE REFUND/ROLLBACK
PROCESSED`, FK própria com proibição de autorreferência e índice do worker de referências;
aritmética, positividade e unicidade `(wallet_id, transaction_id)` em `wallet_ledger_entries`
com triggers `BEFORE UPDATE OR DELETE` e `BEFORE TRUNCATE` garantindo append-only;
`PK(consumer_name, message_id)` na inbox e outbox com tipos válidos, lease pareado e índice
parcial de pendentes. Verificação executada em container efêmero `postgres:17-alpine`:
aplicação das quatro `up`, fluxo positivo completo (carteira BRL + USD, OPENING, BET, REFUND,
ledger, inbox, outbox), 41 blocos de violação com asserção do nome exato da constraint
(três casos com proteções sobrepostas aceitam qualquer uma delas) e reversão completa das
quatro `down` deixando o schema vazio. O item "Testar UNIQUE, CHECK, trigger imutável..."
permanece aberto até virar teste de integração Go com infraestrutura real, assim como a
integração do runner (golang-migrate), alvos de Makefile, separação de usuários de
migração/runtime e Compose. Próximo incremento: dependência `pgx`, pool e `UnitOfWork`.

**Incremento pool e UnitOfWork — 12/10/2026:** adicionada a dependência `github.com/jackc/pgx/v5`
v5.11.0 (`pgxpool`, transações explícitas, SQL visível). O contrato da aplicação nasceu em
`internal/application/ports`: `UnitOfWork.WithinTx(ctx, fn)` — commit quando `fn` retorna nil,
rollback em erro/cancelamento e **aninhamento que une à transação externa** (um caso de uso,
um commit). A implementação em `internal/adapters/postgres` carrega a `pgx.Tx` no `context`
(`TxFromContext` para os futuros repositórios do mesmo pacote), faz rollback com
`context.WithoutCancel` e preserva o erro original do caso de uso; asserção de compile-time
garante a porta. O pool valida a URL via `pgxpool.ParseConfig` (aceita scheme e DSN
keyword/value), é construído sem conectar (lazy) e fixa `MaxConnLifetime` 30m,
`MaxConnIdleTime` 5m e `HealthCheckPeriod` 1m; contagem de conexões fica nos padrões do
pgxpool até o Compose definir dimensionamento por réplica. Bootstrap: `DATABASE_URL`
opcional nesta etapa (sem ela a aplicação sobe só com HTTP); módulo `database` condicional
com ping de 5s no `OnStart` (falha antes de servir) e `Close` no `OnStop`; adaptador continua
sem importar Fx. Descoberta registrada: no Fx 1.24, `fx.New` executa os invokes na construção,
então URL inválida falha já em `app.Err()` — fail-fast melhor que o esperado. Verificações:
`gofmt`, `go vet ./...`, `go test ./...`, `go test -race ./...` limpos; testes de integração
com build tag `integration` (pulam sem `TEST_DATABASE_URL`, nunca usam mocks) executados
contra `postgres:17-alpine` real: ping, commit persiste, rollback em erro, rollback em
cancelamento de contexto, aninhado une e erro interno derruba o externo — todos PASS, também
com `-race`. Retry de deadlock (40P01/40001) fica para o incremento do ProcessWager, onde
reexecutar o caso de uso é seguro. Repositórios ficam para o incremento da abertura, seguindo
a regra de não criar interfaces antes dos casos de uso.

**Incremento abertura atômica — 12/10/2026:** primeiro caso de uso (`internal/application/command`)
com repositórios reais. Portas em `internal/application/ports`: `WalletRepository`,
`WagerTransactionRepository`, `LedgerRepository` e `OutboxRepository` recebem snapshots do
domínio e exigem transação ativa; `ErrWalletAlreadyExists` traduz a violação de
`UNIQUE(player_id, currency)` (SQLSTATE 23505 pelo nome da constraint). `CreateWallet`
valida entrada (BRL-only da aplicação, quantia não negativa, IDs não nulos), cria a carteira
na versão 1 e, para saldo positivo, grava `OPENING` processado (o agregado confirma saldo
existente sem creditar de novo), lançamento de crédito 0→saldo e os eventos
`WagerTransactionProcessed` + `WalletBalanceChanged` na outbox — tudo em um commit do
`UnitOfWork`; saldo zero cria apenas a carteira, conforme o contrato. Identidades UUID v7
com fallback v4; relógio injetado. Harness `internal/testsupport` (tag `integration`) cria
pool próprio (sem importar o adaptador, evitando ciclo), aplica as migrations reais uma vez
por banco (`to_regclass` para idempotência entre binários de teste) e nunca reseta dados.
Verificações: `gofmt`, `go vet ./...`, `go vet -tags=integration ./...`, `go test ./...` e
`go test -race ./...` limpos; integração com PostgreSQL real passou nos dois pacotes e com
`-race`: abertura positiva persiste carteira+OPENING+ledger+2 eventos; zero não gera nada
financeiro; duplicata retorna `ErrWalletAlreadyExists` sem efeitos adicionais; falha no meio
(ledger injetado) faz rollback de tudo; jogadores independentes recebem carteiras
independentes. Cobertura unitária: 83,6% em `command` (fakes de orquestração; semântica
transacional e SQL são da suíte de integração), 41,0% em `postgres` unitário e 55,1% com
integração (ramos restantes são mapeamentos de erro SQL). Próximo incremento: leitura de
carteira/ledger com cursor e reconciliação em snapshot (Fase 2, itens 4–5) ou runner de
migrations/Compose.

**Incremento leituras, cursor e reconciliação — 12/10/2026:** casos de uso de leitura em
`internal/application/query`: `GetWallet`, `ListLedger` (valida `limit` 1–100 com padrão 50;
carteira inexistente distingue 404 de ledger vazio) e `ReconcileWallet`. Portas de leitura
em `ports/queries.go` (`WalletReader`, `LedgerReader`, `ReconciliationReader` +
`ReconciliationReport`). Implementações em `internal/adapters/postgres`: leituras se unem à
transação do contexto quando existe (preparando o `FOR UPDATE` do ProcessWager) ou usam o
pool; ledger pagina com cursor opaco base64url de `(created_at, id)`, ordenação estável com
tie-break por uuid, `LIMIT n+1` para detectar próxima página, sem `OFFSET`, e reidrata cada
linha por `ledger.New` (linha corrompida é erro, nunca silêncio); a moeda vem por JOIN da
carteira (tabela de ledger não tem coluna de moeda por design). Reconciliação em transação
`REPEATABLE READ READ ONLY`: saldo e soma do ledger no mesmo snapshot, soma em `NUMERIC`
convertida com exato `math/big` e checagem de faixa antes de `FromMinorUnits` (overflow é
`ErrLedgerSumOutOfRange`, auditable); `difference = stored − calculated`; nunca altera o
saldo. Item 4 da Fase 2 automatizado em Go: trigger append-only rejeita `UPDATE`/`DELETE`/
`TRUNCATE` com a entrada intacta após as tentativas, e `CHECK` de saldo negativo devolve
23514. Verificações: `gofmt`, `go vet ./...`, `go vet -tags=integration ./...`,
`go test ./...` limpos; integração PostgreSQL real com `-count=2` (mesmo banco, provando
idempotência do harness e unicidade por execução) e `-race` em todos os pacotes internos —
todos PASS: paginação com tie-break de mesmo timestamp, cursor inválido →
`ErrInvalidCursor`, carteira ausente → `ErrWalletNotFound`, reconciliação consistente
(abertura), zerada (0 lançamentos), divergente (stored 500,00 vs calculado 400,00,
difference 100,00, saldo preservado) e ausente. O harness de migrations ganhou advisory
lock transacional (`pg_advisory_xact_lock`) porque binários de teste paralelos disputavam
a criação do schema. Cobertura unitária de `query`: 100,0%. Próximo incremento: caso de
uso `ProcessWager` (BET/WIN/LOSS/REFUND/ROLLBACK com `FOR UPDATE`, idempotência persistente
e hash canônico).

### Fase 3 — OIDC, endpoints e operação síncrona

**Controle de aderência — 12/10/2026:** consultar [docs/CONFORMIDADE_DESAFIO.md](docs/CONFORMIDADE_DESAFIO.md)
antes de fechar cada incremento. O README oficial prevalece sobre este plano. ProcessWager
concluído não conclui a API, autenticação ou demonstração distribuída. O próximo incremento
segue os critérios de OIDC/Keycloak e contratos HTTP da matriz; os requisitos do desafio
só são encerrados com evidência no nível exigido.

- [x] Provisionar Keycloak; validar tokens, issuer/audience e roles.
- [x] Restringir operações internas e escopo por provedor.
- [x] Criar endpoints e contrato JSON/erros.
- [x] Implementar `BET`, `WIN`, `LOSS` e idempotência persistente (caso de uso; HTTP/OIDC ainda pendentes).
- [x] Implementar `REFUND`, `ROLLBACK` e unicidade de compensações (caso de uso).
- [x] Armazenar saldo/versão originais para replay.
- [x] Testar as apostas `80.00+80.00` e 50 chamadas concorrentes no PG real (goroutines; três processos demonstrados depois na Fase 6).

**Pronto quando:** HTTP autenticado cumpre operações financeiras sem saldo negativo ou débito duplicado.

**Incremento ProcessWager — 12/10/2026:** concluído o caso de uso compartilhado das cinco
operações externas, reaproveitando as alterações locais existentes. Corrigidos os erros
de compilação encontrados no baseline e fixtures de teste inconsistentes. Hash SHA-256
sobre JSON tipado ordenado, normalização monetária, deduplicação por provedor/key e
provedor/ID externo, replay histórico e `SELECT ... FOR UPDATE` com nova consulta de
idempotência após o lock. Saldo, transação, ledger e outbox compartilham um commit;
LOSS/rejeições não produzem ledger. Referências ausentes são persistidas, ainda sem
worker/TTL. Retries limitados da transação para deadlock/serialização e disputa nos índices
de identidade; aplicação BRL-only. Composição Fx e nenhum arquivo de migration alterado.

Verificações: `make check`, `go vet -tags=integration ./...`, `make build` e integração
contra PostgreSQL 17 real com `go test -race -tags=integration ./... -count=2`, sem testes
ignorados. Foram verificados os cinco tipos, três tipos de referência de ROLLBACK,
rejeição de reversão sem saldo, referências incompatíveis, replay de pendência,
replay histórico após WIN, erro SQL na outbox com rollback integral, 50 chamadas
duplicadas, disputa 80+80, compensações concorrentes e progresso de carteira independente.
Teste adicional com barreira força disputa da mesma identidade em carteiras diferentes
e valida a conversão da violação única em conflito após retry.
Os testes A/B/C estão demonstrados com goroutines e conexões reais; D (três processos),
HTTP/SQS, reinicializações e recuperação ainda estão pendentes. Prazo interno de entrega: **14/10/2026**.

**Incremento endpoints autenticados e OIDC — 12/10/2026:** concluída a parte HTTP da
Fase 3, reaproveitando as alterações locais existentes. `internal/adapters/oidc`
 valida access tokens reais do Keycloak (go-oidc v3, JWKS com rotação, RS256,
iss/aud/exp/nbf, `typ=Bearer`, `azp`); `internal/adapters/http` expõe os nove contratos
oficiais reutilizando `CreateWallet`, `ProcessWager` e as queries, com decoder JSON
estrito (apenas objetos/strings; números, `null`, duplicatas e campos extras rejeitados
na fronteira), `Idempotency-Key` obrigatória usada como recebida, correlation ID
validado/gerado e ecoado, timeout por request e respostas distinguíveis
(201/200/202/400/401/403/404/409/422/503) com `error.code` estável. Autorização:
roles `provider`+`provider_id` (claim do token, nunca do body) para apostas,
`wallet:manage`/`wallet:read` para operações internas; isolamento entre provedores
inclusive em replays, conflitos e consultas (`403`/`404` sem vazamento). `GET
/health/ready` público faz ping real no PostgreSQL (check SQS entra com a mensageria).
Realm `jungle` provisionado em `deployments/keycloak` (provedores a/b, serviço interno,
clientes negativos de teste com audience errada e vida curta); `compose.dev.yaml` sobe
PostgreSQL 17 + Keycloak 26.8. `GET /wagering/transactions/:id` exposto apenas ao
provedor do registro; acesso interno adicional exige permissão explícita futura.
Verificações: `gofmt`, `go vet ./...`, `go vet -tags=integration ./...`,
`go test ./...`, `go test -race ./...` limpos; integração real com PostgreSQL e
Keycloak (`go test -race -tags=integration ./... -count=1`) passou em todos os
pacotes, incluindo a suíte HTTP: tokens ausente/malformado/tampered/audience
errada/expirado (cliente de 3s) → `401`; sem role e interno em aposta → `403`;
isolamento a/b em rota, consulta, replay e conflito sem efeitos financeiros
(saldo/transações/ledger/outbox conferidos no banco); cinco operações com replay
histórico (saldo original), LOSS sem ledger, REFUND+ROLLBACK, pendência `202`
consultável; `409` nos dois conflitos; `422` com `INSUFFICIENT_FUNDS` e replay da
rejeição; oito contratos inválidos → `400` sem efeitos; disputa 80+80 por HTTP
(uma 200, uma 422, saldo 20.00); readiness `200`. Concorrência HTTP é
intra-processo com banco real. Próximo incremento: worker de referências (Fase 4)
e SQS/inbox/publisher (Fase 5).

### Fase 4 — Referências pendentes
- [x] Persistir `PENDING_REFERENCE` com deadline/backoff.
- [x] Worker distribuído com `SKIP LOCKED` e shutdown seguro.
- [x] Rejeitar referência não encontrada, inválida, terminal ou mismatched conforme política.
- [x] Testar chegada posterior, expiração e retomada por nova chamada ao worker.
- [x] Demonstrada a recuperação por reinicialização literal de processos (Teste H de `scripts/fault.sh`).

**Pronto quando:** nenhuma referência pendente depende da memória de um processo.

**Incremento worker de referências — 12/10/2026:** concluída a Fase 4. Novo comando
`RetryDueReferences` (`internal/application/command/retry_reference.go`) retoma
pendências vencidas com claim `FOR UPDATE SKIP LOCKED` (porta
`PendingReferenceClaimer` no `WagerTransactionReader`), backoff exponencial
1s→5min gravado em `attempt_count`/`next_attempt_at` e TTL `REFERENCE_TTL`
(default 24h) materializado como `reference_deadline_at` na primeira retomada.
Desfechos no mesmo commit: `PROCESSED` com saldo/ledger/eventos idênticos ao fluxo
síncrono (mesmo `transactionId`), `REJECTED` com código estável e evento
(`REFERENCE_NOT_FOUND` por expiração sem tocar a carteira; `REFERENCE_NOT_PROCESSED`
para referência terminal sem sucesso) ou reagendamento sem efeitos. Falha transitória
rola back o claim e a pendência volta a ficar elegível; deadlock/serialização repetem
a transação como no `ProcessWager`. `UpdateWagerTransaction` reescreve só colunas
mutáveis — identidade da operação é imutável. Worker `internal/workers/references.go`
(method value como dependência, sem interface artificial) faz polling configurável
(`REFERENCE_WORKER_INTERVAL`, default 1s, lotes de 50) com cancelamento observável;
bootstrap registra hooks no Fx e encerra o worker antes de fechar o pool. Novas
configurações `REFERENCE_TTL`/`REFERENCE_WORKER_INTERVAL` validadas no LoadConfig.
Verificações: `gofmt`, `go vet ./...`, `go vet -tags=integration ./...`,
`go test ./...`, `go test -race ./...` limpos (11 pacotes); integração PostgreSQL 17
real com `-race`: reagenda (attempts=1, next futuro, deadline persistido), resolve
após a BET chegar (saldo 100.00, versão 3, 3 lançamentos, referência resolvida),
rejeita por TTL com evento e carteira intacta, dois workers concorrentes retomam
8 pendências uma única vez (soma de tentativas 8), falha transitária devolve o claim
e a mesma pendência é concluída na chamada seguinte. Suíte HTTP continua passando
com o worker ativo no lifecycle da aplicação Fx completa. Cross-channel com SQS e
reinício literal de três processos ficam para a Fase 5/6. Próximo incremento:
SQS/inbox/consumidor com LocalStack (Fase 5).

### Fase 5 — SQS, inbox e outbox
- [x] LocalStack com entrada FIFO, DLQ, redrive, fila de eventos.
- [x] Consumidor SQS com inbox e UnitOfWork compartilhada.
- [x] `DeleteMessage` somente após commit; retry e erros permanentes.
- [x] Publisher da outbox com concorrência, lease/backoff e event IDs estáveis.
- [x] Testar cross-channel HTTP/SQS e falha injetada após commit com broker real.
- [x] Falha entre publicação e confirmação (Teste F) com failpoint determinístico e broker real; republicação mantém `eventId`.
- [x] Interrupção literal do consumidor e do publisher (Testes E/F com kill -9 em `scripts/fault.sh`).

**Pronto quando:** todas as garantias de at-least-once e recuperação passam nos containers reais.

### Fase 6 — Três instâncias, observabilidade e entrega
- [x] Runner de migrations operacional (`cmd/migrate` com golang-migrate, Makefile) verificado com PostgreSQL real.
- [x] Compose de entrega reproduzível a partir de checkout limpo (app + migrations + IdP + filas + gateway), verificado com `down -v` + `up --build` + smoke.
- [x] Expor três processos reais e confirmar paralelismo por carteira (réplicas do Compose atrás do gateway; Teste A/B executados entre processos).
- [x] Kills literais e reinício com dados verificados por `scripts/fault.sh` no Compose de entrega (Testes E/F/H: kill -9 com aposta SQS em voo sem débito duplicado; kill com eventos não reconhecidos na outbox com drenagem pós-reinício; reinício com PENDING_REFERENCE retomada pelo worker e concluída após a BET chegar).
- [x] JSON logging (`slog`) e métricas financeiras requisitadas (`jungle_wager_*`, `jungle_wallet_*`, histograma de duração) expostas em `/metrics` e verificadas no Compose de entrega; liveness/readiness mantidos.
- [x] Documentar `README.md`, `ARCHITECTURE.md`, ADRs e scripts de reprodução — README com entrega/queues/smoke/faults, ARCHITECTURE com decisões por incremento e revisão final registrada.
- [x] Executar `gofmt`, `go vet ./...` (`-tags=integration`), `go test -race ./...` e integração `-race `-count=1 — executados na revisão final, limpos.
- [x] Revisar eliminações do §14, endpoints oficiais, `.env.example` e secrets — auditoria por ferramenta e matriz atualizada (ver "Revisão final de entrega").

**Pronto quando:** alguém consegue clonar, subir com Compose, obter tokens, fazer operações e reproduzir testes sem intervenção manual oculta.

**Incremento runner de migrations — 12/10/2026 (Fase 6, item 1):** comando
`cmd/migrate` com `golang-migrate/v4` v4.19.1 e driver `pgx5` (pgx/v5 stdlib já
presente no módulo): `up [N]`, `down N`, `version` e `force V`, DSN em
`DATABASE_URL` com conversão de scheme preservando parâmetros de conexão, ping
rápido com timeout antes de abrir o histórico e `ErrNoChange` tratado como
"história já alvo". Makefile ganha `migrate-up`, `migrate-down N=` e
`migrate-version`; Dockerfile constrói `bin/migrate` e copia `migrations/`
(direção ao Compose de entrega).

Verificações: `gofmt`, `go vet ./cmd/migrate`, testes unitários (conversão de
scheme e validação de argumentos) e integração real
(`go test -tags=integration ./cmd/migrate`) que cria e descarta um banco
temporário por execução: `up` aplica as quatro migrations com `version=4` limpo,
`up` repetido é no-op, `down 3`/`up 1` alternam versões e rollback total deixa o
histórico vazio limpo. No banco de desenvolvimento (schema aplicado pelo harness
sem histórico do golang-migrate), `up` se recusou corretamente a reaplicar e
`force 4` registrou o estado atual sem tocar dados — `version` mostra 4 e `up`
seguinte foi no-op. Compose de entrega e três processos concluídos no incremento seguinte da Fase 6.

### Fase 6, incremento 2 — Compose de entrega e três processos (12/10/2026)

`docker-compose.yml` (projeto isolado `jungle-delivery`, loopback apenas) sobe
PostgreSQL 17, Keycloak 26.8 (issuer interno `http://keycloak:8080`, porta de
management 9000 para healthcheck, realm `jungle` importado), LocalStack SQS
4.14.0, job `migrate` (o mesmo binário do runner, `depends_on` PostgreSQL
saudável), três réplicas `api` com pools independentes e healthcheck
`/health/live`, e gateway nginx (`deployments/gateway/nginx.conf`) que resolve
o upstream por request no DNS do Docker para rodas entre réplicas. Portas
publica: gateway `9080` e Keycloak `59080` (loopback), evitando colisão com o
`compose.dev.yaml`; overridable por `JUNGLE_DELIVERY_OIDC_PORT`/
`JUNGLE_DELIVERY_GATEWAY_PORT`.

Contrato HTTP aplicado ao script: `scripts/smoke.sh` usa a chave de
idempotência apenas no header `Idempotency-Key` (body com `idempotencyKey` é
contrato inválido — decoder estrito rejeita `400`). Fluxo do smoke: token
`client_credentials` real (wallet-internal e provider-a), abertura de carteira
100.00, **50 BETs idênticas de 50.00 concorrentes via gateway** → exatamente 1
processada + 49 replays idempotentes (`idempotentReplay:true` com o resultado
armazenado), par concorrente de BETs distintas de 40.00 → 200/422
(`INSUFFICIENT_FUNDS`), saldo final 10.00, **3 linhas de ledger**,
reconciliação `consistent:true`.

Execução limpa real: `docker compose down -v` + `up --build` em ~1 minuto,
todas as réplicas healthy. Distribuição provada nos logs por container:
21/18/13 requisições completadas em api-1/2/3 — três processos independentes
com pools próprios. `/metrics` acessível via gateway (200, gauges outbox).
`down -v` remove volumes e redes.

### Fase 6, incremento 3 — métricas financeiras (12/10/2026)

Observabilidade migrada para `client_golang/prometheus`: registro dedicado
(`observability.Prometheus`, sem métricas de runtime Go) servido em
`GET /metrics`; gauges de outbox viraram um `Collector` que consulta o
PostgreSQL a cada scrape — consulta falha vira métrica inválida na saída, não
zero velho. Conjuntos são singletons por processo (`sync.Once`), evitando
duplo registro; deltas, não valores absolutos, nas asserções de teste.

Métricas requisitadas e conectadas: `jungle_wager_operations_total{kind,status}`
(fechadas no handler HTTP, replay conta como resposta com status persistido),
`jungle_wager_idempotent_replays_total`, `jungle_wager_idempotency_conflicts_total` (409),
`jungle_wager_processing_duration_seconds{kind}` (histograma), `jungle_wager_db_retries_total{reason}` (retry de transação no ProcessWager),
`jungle_wager_sqs_redeliveries_total` (`ApproximateReceiveCount` > 1),
`jungle_wager_dlq_total` (cópia à DLQ) e `jungle_wallet_reconciliations_total`
/ `jungle_wallet_reconciliation_divergences_total`. Sem labels de alta
cardinalidade (kind/status/reason limitados).

Verificações: `gofmt`, `go vet ./...`, `go test -race ./...`,
`go vet -tags=integration ./...` e integração `-race` passaram (fixture do
publisher ajustado para `testutil.ToFloat64` e deltas). No Compose de entrega
reconstruído: BET processada + replay, `operations_total{BET,PROCESSED}=2`,
`replays=1`, histograma com 2 observações, gauges outbox em 0 — evidência em
produção local.

### Fase 6, incremento 4 — kills literais e reinício com dados (12/10/2026)

`scripts/fault.sh` executa três drills contra o Compose de entrega, todos com
asserções financeiras por HTTP (saldo, ledger, status externo, reconciliação):
LocalStack publicado em loopback (`JUNGLE_DELIVERY_SQS_PORT`, default 49570)
para o produtor assinante `cmd/send-wager`; a fila de entrada é resolvida via
`get-queue-url` (o esquema de URL do LocalStack muda entre versões).

**Teste E (kill -9 com aposta em voo):** wallet de 100.00, envelope assinado
publicado, aguardada a marcação not-visible (quando observável), `docker
compose kill -s KILL api` nas três réplicas e `up -d api`; após a volta, a
reentrega conclui a operação uma única vez (saldo 75.00, dois lançamentos,
reconciliação consistente) — commit perdido ou preservado, ambos recobram pelo
registro durável. **Teste F (kill com eventos não reconhecidos):** BET por HTTP
seguida de kill imediato; após a volta o publisher drena o backlog
(`jungle_outbox_pending` em 0) com os mesmos eventIds. **Teste H (reinício com
pendência):** REFUND de 25.00 referenciando um BET futuro → `202
PENDING_REFERENCE`; `restart api`; a BET chega depois; o worker conclui o
REFUND (saldo 100.00, `PROCESSED`, reconciliado).

Restrições de identidade aplicadas pelo domínio durante a elaboração do script
(e usadas como aprender de novo): player e wallet devem combinar
(`ErrWalletScope` → DLQ permanente), e a referência exige rodada, moeda e
provedor coerentes — duas rodadas divergentes deixam a pendência reagendando
em vez de concluir; o script hoje casa exatamente as identidades. O
"not observed in flight" ocorre quando a combinação morte-caça consuma a
mensagem antes da marcação; o drill continua válido e cobre canal de voo
simulado na integração determinística anterior. Pendências: revisão final.

Verificações: `gofmt`, `go vet ./...`, `go test -race ./...`,
`go vet -tags=integration ./...` e `go test -race -tags=integration ./...`
(regras de migrations e publisher) passaram neste incremento; smoke acima. Pendências: kill literal de contêineres nos pontos críticos,
métricas financeiras restantes (operações/replays/conflitos/duração/DLQ/
divergência) e revisão final de eliminações/secrets.

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

### Revisão do backoff de referências (12/10/2026)

O backoff limita o expoente antes do deslocamento de bits: 1s, 2s, …, 256s e teto de 5min a partir da décima tentativa. Isso evita overflow de `time.Duration` em pendências duradouras; TTL e regras de reagendamento permanecem iguais. Teste cobre limites, 1.000 tentativas e o maior `int` da plataforma.


### Fase 5, incremento 1 — SQS/inbox (12/10/2026)

Concluído o consumidor transacional, provisionamento local automático no startup,
redrive após três recebimentos, assinatura por provedor, readiness SQS e shutdown.
Validação: `make check`, `go vet -tags=integration ./...` e suíte completa
`go test -race -tags=integration ./... -count=1` com PostgreSQL, Keycloak e LocalStack
reais passaram. Testes de integração não foram ignorados por falta de ambiente.
Filas isoladas por teste, sem purge de filas de desenvolvimento. Evidências e
limites detalhados no README, arquitetura e matriz de conformidade.

Na sequência este incremento foi concluído: publisher da outbox (claim/lease,
backoff, eventos estáveis, dois publishers e falhas publish/ack) — ver o registro
da Fase 5, incremento 2 abaixo.

### Fase 5, incremento 2 — publisher da outbox (12/10/2026)

Concluída a publicação pós-commit. O comando `PublishNext`
(`internal/application/command/publish_outbox.go`) reivindica exatamente um evento
por vez: claim `FOR UPDATE SKIP LOCKED` com `clock_timestamp()` (sem relógio de
host) incrementa `attempts` e grava lease pareado `leased_by`/`leased_until`
(token de claim, não de processo — lease vencido não pode confirmar nem
reagendar); a chamada de rede ao SQS ocorre **sem transação nem lock de linha
abertos**; `MarkPublished` confirma `published_at` e `ScheduleRetry` grava
`last_error` + `next_attempt_at` com backoff exponencial 1s→5min, ambas
condicionadas ao token. Falha conhecida é liberada com cleanup limitado mesmo
após shutdown/timeout (`context.WithoutCancel`), e o worker cancela o I/O sem
abandonar o lease. Payload é o snapshot imutável do banco: republicação reenvia
o mesmo `eventId` (at-least-once; deduplicação do evento é responsabilidade do
consumidor, além da janela FIFO). Worker drena até 50 claims por tick com
cancelamento observável; métricas de processo (`jungle_outbox_*`) expostas em
`/metrics`, com gauges de pendência/atraso consultados no PostgreSQL (query
falha vira 503, nunca atraso falso). Bootstrap registra worker e métricas no
módulo messaging.

Verificações: `gofmt`, `go vet ./...`, `go vet -tags=integration ./...`,
`go test ./...` e `go test -race ./...` limpos; integração com PostgreSQL 17 e
LocalStack reais e `-race`: claim exclusivo com incremento de tentativas, ack
com token alheio vira `ErrOutboxLeaseLost`, ack próprio publica e zera pendência,
lease abandonado é recuperado com `attempts=2` e não pode ser liberado pelo
token antigo, reschedule grava `PUBLISH_FAILED` e respeita o backoff; end-to-end
com fila FIFO real: evento comprometido chega com corpo idêntico ao snapshot,
`MessageDeduplicationId=eventId` e `MessageGroupId=aggregateId`, crash entre
envio e confirmação é recuperado com o mesmo `eventId`/payload (FIFO suprime a
repetição na janela de deduplicação; identidade comprovada), quatro publishers
concorrentes entregam dois eventos uma única vez cada. Filas e schema isolados
por teste (`IsolatedPostgresPool`). Na sequência: kills literais (E/F), Compose de
entrega com três réplicas e auditoria de fechamento — ver registros da Fase 6.

### Revisão final de entrega — 12/10/2026

Auditoria contra o enunciado oficial (snapshot `docs/reference`), seção por
seção, com constatações e correções. Prova por ferramenta: `float32`/`float64`
só em gauges de métrica — nunca em valor monetário; `panic` ausente do código
de produção; domínio sem imports de infraestrutura; nenhuma credencial real no
repositório; `.env.example` cobre as 11 variáveis lidas por `LoadConfig`;
nenhum TODO/FIXME pendente.

Correções da revisão:
1. `fault.sh`: o Teste H publica o BET referenciado **pelo SQS** (cruzamento de
   canais exigido pelo §13) e espera o status do REFUND em `PROCESSED` — o loop
   anterior quebrava no saldo de abertura (100.00) e reportaria conclusão por
   afobação; acrescentadas as asserções de pendência intocada pelo restart e de
   três lançamentos (abertura + débito + crédito).
2. Resíduos da matriz de conformidade eliminados (linhas marcavam pendências
   já resolvidas por kills literais, métrica de divergência e cruzamento de
   canais).

Reprodução limpa executada nesta revisão (`docker compose down -v` + `docker
compose up --build`): smoke OK (51 idênticas → 1 débito + 49 replays, par
200/422 distinto, saldo 10.00, 3 lançamentos, reconciliação) e faults OK (E/F/H
com kill -9 das três réplicas, reinício com `PENDING_REFERENCE` intacta e
conclusão cruzada HTTP→SQS). Verificações: `gofmt` limpo; `go vet ./...`;
`go test -race ./...` (14 pacotes ok); `go vet -tags=integration ./...`;
`go test -race -tags=integration ./...` sem falhas; `make build` e imagens
reconstruídas. Eliminatórias do §14 conferidas uma a uma contra evidência
referenciada na matriz de conformidade.


## Revisão independente de qualidade — 13/10/2026

Auditoria sobre `e11e325`, solicitada após a evolução da carga/documentação: [parecer e backlog](docs/AUDITORIA_INDEPENDENTE_2026-10-13.md). Nota **80/100**, três achados P1 e dez P2; implementação não alterada nesta etapa. As declarações anteriores de conclusão ficam condicionadas ao fechamento dos achados. Próximo incremento: A01–A02 (autorização e durabilidade do broker), seguido da comprovação controlada de interrupções A03. Demais correções devem seguir a ordem do relatório, uma parte validada e enviada ao GitHub por vez.

### A01, incremento 1 — identidade do cliente SQS (13/10/2026)

Removida a imposição de credenciais estáticas no adaptador. A cadeia padrão
AWS passa a resolver ambiente, perfis e roles; o ambiente local fornece
`test/test` explicitamente no Compose, na CI e nos comandos documentados.
Testes de regressão verificam assinatura com ambiente/perfil/role de container,
preservação do token temporário e falha sem envio quando não há identidade.
O teste executado contra a versão local anterior reproduziu as três falhas
(perfil, role de container e ausência de identidade).

**A01 permanece aberto:** resolver a identidade do cliente não comprova
negação no broker. Próximo incremento: autorização efetiva por identidade/fila
com testes negativos reais; depois A02 e A03. As alterações locais de carga,
pausas de interrupção, métricas e scanner ainda são incrementos separados.

Validação deste incremento: vet, testes com `-race`, build e integração real
completa aprovados (606 casos/subtestes, zero falhas/ignores). A primeira
execução expôs a precondição de lease de 50 ms; incorporada a correção local
com expiração controlada no banco e 20 repetições aprovadas.
[Evidências, comandos e limites](docs/audit/2026-10-13/sqs-credentials.md).

### Checkpoint das correções locais — 13/10/2026

Versionamento solicitado pelo usuário das alterações pendentes: gates e
classificação k6, aritmética BigInt nas expectativas, soak por duração,
instrumentação para interrupções, métricas, exemplos IAM, runbook e scanner.
Este checkpoint não encerra os achados da auditoria. A01/A02 seguem abertos;
A03 exige reprodução das interrupções e validação das precondições; A11 exige
conectar o callback de referências e contabilizar conclusões após o commit.
Relatórios de desempenho anteriores não representam uma nova campanha.

Verificações do checkpoint: `make check`, vet com tag integration, build,
Compose, shell, documentação e sondas k6 aprovados (sonda negativa: exit 99
esperado); `govulncheck@v1.9.0`: nenhuma vulnerabilidade encontrada. Integração
com PostgreSQL, Keycloak e LocalStack reais, `-race -count=1`: 606
casos/subtestes aprovados, zero falhas e zero casos ignorados.

### Missão final — diagnóstico A01 (14/10/2026)

HEAD inicial limpo e sincronizado em `6c04217`. Matriz inicial em
`docs/AUDITORIA_FINAL_ENTREGA.md`. Revisão dos templates IAM corrigiu ações
faltantes do produtor/admin/readiness; explicitada a ausência de separação
operacional no runtime. A01 continua parcial: cadeia AWS e HMAC são controles
efetivos; não substituem autorização do broker. Sem AWS real ou serviços pagos.

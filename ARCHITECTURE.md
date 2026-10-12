# Arquitetura da solução

## Estado e direção

As Fases 0 e 1 estão concluídas: `Money`, `Wallet`, `WagerTransaction`, `WalletLedgerEntry` e os eventos tipados de domínio, com invariantes revisadas e verificadas. A Fase 2 está concluída: schema, pool, transações, abertura, leituras e reconciliação. `ProcessWager` implementa a parte financeira da Fase 3; endpoints e autenticação vêm a seguir. A direção é uma arquitetura hexagonal enxuta: domínio independente, casos de uso compartilhados por HTTP e SQS, adaptadores para entrada e persistência, e composição centralizada.

Dependências planejadas:

```text
HTTP / SQS -> aplicação -> domínio
PostgreSQL / mensageria -> contratos definidos pela aplicação
bootstrap -> composição dos componentes concretos
```

Não criamos interfaces, repositórios ou pastas vazias antes de seus casos de uso.

As seções por incremento preservam o histórico das decisões. A seção [ProcessWager](#processwager--processamento-transacional-compartilhado) registra as garantias atuais e supera as pendências históricas de processamento e idempotência.

## Decisões implementadas na Fase 0

- **Go 1.27.2** fixado em `go.mod` e no estágio de compilação do Dockerfile; dependências registradas em `go.sum`.
- **Uber Fx 1.24.0** concentra injeção e lifecycle em `internal/bootstrap`. Módulos de configuração, logging e HTTP são compostos por construtores.
- **`net/http`** implementa o transporte em `internal/adapters/http`. O adaptador não depende de Fx. `cmd/server` apenas inicia a aplicação.
- **Configuração por ambiente**, validada antes da abertura do listener. Apenas opções utilizadas estão presentes em `.env.example`.
- **`slog` em JSON** recebe logs da aplicação, de Fx e erros internos do servidor HTTP.
- **Timeouts HTTP**: cabeçalhos em 5s, leitura em 10s, escrita em 15s e conexões ociosas em 60s.
- **Startup observável**: a abertura da porta acontece no `OnStart`, retornando erro ao Fx se falhar. Uma falha inesperada de `Serve` solicita encerramento com código 1.
- **Shutdown**: `SIGINT`/`SIGTERM` iniciam o `OnStop`; `Shutdown` interrompe novas conexões e aguarda requisições em andamento por até 10s. Se o prazo esgotar, `Close` força o fechamento. Fx tem orçamento total de 15s. O Docker deve conceder pelo menos 15s para parada.
- **Liveness público**: `GET /health/live` retorna `200` com `{"status":"ok"}`. Readiness depende da infraestrutura futura e não está exposto neste incremento.
- **Referência preservada**: o repositório oficial está clonado separadamente e uma cópia intacta do README é versionada em `docs/reference` com o SHA consultado.

Referências de composição: [servidor HTTP com Fx](https://uber-go.github.io/fx/get-started/http-server.html) e [lifecycle](https://uber-go.github.io/fx/lifecycle.html).

## Money — primeiro incremento da Fase 1

`internal/domain/money` usa apenas a biblioteca padrão. O valor guarda `cents int64` e `currency string` em campos privados. Soma, subtração e negação retornam novos valores, sem alterar os operandos. A cópia de um `Money` é independente. Não há setters nem conversão entre moedas.

**Moedas:** o tipo reconhece explicitamente `BRL` e `USD`, ambas com escala dois, para permitir comparação e testes reais de incompatibilidade entre valores válidos. Esta é uma precisão do plano inicial: a restrição BRL-only será da aplicação, ainda não implementada. Não aceitamos códigos arbitrários, minúsculos ou com espaços. Valores em moedas diferentes não podem ser somados, subtraídos, comparados ou considerados iguais; a operação retorna `ErrCurrencyMismatch`.

**Limites:** os valores internos vão de `-92233720368547758.08` a `92233720368547758.07`. Os construtores de entrada externa aceitam apenas a parte não negativa desse intervalo. `FromMinorUnits` aceita todo `int64`, inclusive diferenças negativas de reconciliação; ele não representa por si só a regra de saldo não negativo da futura carteira.

**Parsing e normalização:** `ParseDecimal` aceita dígitos ASCII, parte inteira obrigatória e parte fracionária opcional com uma ou duas casas. `1`, `1.0`, `1.00` e `0001.00` representam o mesmo valor; `FormatDecimal` devolve `1.00`. São rejeitados sinais (inclusive `-0.00` e `+1`), espaços, vírgulas, expoentes, NaN, infinito, `1.`, `.1` e escala excedente. Não há arredondamento. O parser acumula dígitos com checagem de limite antes de multiplicar e somar. O futuro hash de idempotência deverá usar essa representação canônica.

**Aritmética:** `Add` e `Sub` verificam overflow e underflow antes do cálculo. A subtração não nega o segundo operando, pois `MinInt64 - MinInt64` é válido mesmo que a negação isolada de `MinInt64` não seja. `Negate(MinInt64)` retorna erro. `Compare` usa comparação direta, sem subtração. A formatação cobre inclusive `MinInt64` sem tentar negá-lo diretamente.

**JSON:** `MarshalJSON` gera `amount` como string com duas casas e inclui `currency`. `ParseJSON` constrói um valor externo, exigindo strings e rejeitando campos desconhecidos e conteúdo adicional após o objeto. `json.Unmarshal` usa o mesmo construtor e só substitui o destino após validação completa; em erro, o valor anterior permanece intacto. Essa operação de decodificação é a exceção explícita à API de operações por valor. A serialização pode produzir negativos para resultados internos; o contrato de entrada continua rejeitando-os. A reidratação de diferenças negativas deve usar `FromMinorUnits`.

**Valor não inicializado:** `Money{}` é inválido. Use `Zero(currency)` para zero monetário. `Validate`, aritmética, comparação e serialização rejeitam o valor não inicializado. `MinorUnits` e `Currency` são apenas acessores; nas fronteiras da aplicação, valide antes de usá-los.

Erros classificáveis por `errors.Is`:

| Erro | Significado |
| --- | --- |
| `ErrInvalidMoney` | Valor não inicializado ou receptor nulo na decodificação |
| `ErrUnsupportedCurrency` | Código fora da lista suportada |
| `ErrInvalidAmount` | Formato decimal externo inválido |
| `ErrOverflow` | Parsing ou aritmética fora dos limites de `int64` |
| `ErrCurrencyMismatch` | Operandos em moedas diferentes |
| `ErrInvalidJSON` | Estrutura, tipos JSON, campos extras ou conteúdo adicional inválidos |

Erros dos campos em JSON preservam as classificações de moeda, formato e overflow. Os testes cobrem extremos numéricos, valores inválidos, normalização, moedas distintas, preservação dos operandos e contrato JSON. O fuzzing compara a aritmética com `math/big.Int` e o parsing aceito com `math/big.Rat`; essas bibliotecas são oráculos de teste, não a representação de produção.

## Wallet — segundo incremento da Fase 1

`internal/domain/wallet` encapsula UUID da carteira, UUID do jogador, saldo `Money`, versão `int64` e instantes de criação/atualização. A moeda é derivada do saldo, evitando dois campos que possam divergir. O agregado aceita as moedas suportadas por `Money`; a restrição de entrada a BRL continua reservada à futura aplicação.

**Identidade e relógio:** os construtores recebem IDs e horários; não geram identidades nem consultam `time.Now`. Usamos `github.com/google/uuid v1.6.0` como tipo de valor, rejeitando UUID nulo. A geração de UUID v7 ficará no caso de uso de criação. Essa biblioteca de identidade não introduz dependência de HTTP, Fx ou persistência no domínio. [Documentação do tipo UUID](https://pkg.go.dev/github.com/google/uuid@v1.6.0#UUID).

**Criação e reidratação:** `New` recebe saldo inicial não negativo, define versão 1 e ambos os timestamps no instante fornecido. `Rehydrate(State)` valida e copia um snapshot, preservando saldo e versão, sem reaplicar crédito nem gerar eventos. Ambos usam as mesmas validações de estado. Um saldo inicial positivo não incrementa versão; a futura abertura transacional terá de criar `OPENING`, ledger e outbox no mesmo commit. Isso ainda não é executado pelo agregado.

**Movimentações:** `Debit` e `Credit` recebem `Money` e horário. Rejeitam valor não inicializado, moeda incompatível, quantia negativa e horário inválido. Débito exige saldo suficiente; crédito usa a proteção de overflow de `Money`. Somente depois de todas as validações são atribuídos novo saldo, versão incrementada e horário de atualização. A versão não pode ultrapassar `MaxInt64`. Qualquer rejeição preserva o estado inteiro. Zero é aceito como operação sem efeito, inclusive na versão máxima, mas ainda exige moeda e horário válidos. A política de `LOSS` pertence à futura transação de aposta, não a este agregado.

**Tempo:** criação e atualização devem ser não nulos e atualização não pode anteceder criação. Movimentações aceitam o mesmo instante da última atualização, mas rejeitam instantes anteriores. Todos os horários são armazenados em UTC, preservando o instante recebido. Operações sem mudança de saldo preservam também `updatedAt`. A aplicação precisará fornecer uma fonte consistente de tempo para evitar regressões entre processos.

**Encapsulamento:** acessores e `Snapshot` retornam cópias. Alterar o snapshot ou decodificar JSON sobre uma cópia do saldo não altera a carteira. `Validate`, `Debit` e `Credit` rejeitam tanto `Wallet{}` quanto receptor nulo. Os acessores pressupõem instância construída e validada.

**Erros:** `errors.Is` distingue `ErrInvalidWallet`, `ErrInvalidIdentity`, `ErrNegativeBalance`, `ErrInvalidVersion`, `ErrInvalidTime`, `ErrNegativeAmount`, `ErrInsufficientFunds` e `ErrVersionOverflow`. Erros de moeda, valor monetário inválido e overflow preservam os sentinelas de `Money`. A tradução para códigos de aposta, incluindo insuficiência em reversões, será responsabilidade das políticas/casos de uso.

**Limites desta etapa:** uma instância do agregado pertence a uma execução de caso de uso e não deve ser compartilhada entre goroutines. Não há locks em memória nem garantia de concorrência distribuída neste pacote. Unicidade de `(player_id, currency)`, proteção contra lost updates e atomicidade com ledger/outbox dependem da próxima etapa de persistência. O teste de dois débitos sucessivos de 80 sobre 100 valida o saldo local; não substitui o cenário oficial concorrente com três processos.

Os testes exercitam criação zero/positiva, reidratação, estado inválido, moedas distintas, débito até zero, insuficiência, overflow monetário e de versão, timestamps e independência das cópias. O fuzzing executa sequências de movimentos contra um saldo calculado com `math/big.Int`, verificando preservação de estado em rejeições e atualização de versão em sucessos.

## WagerTransaction — terceiro incremento da Fase 1

`internal/domain/wagering` mantém o pedido, o estado, as datas, a referência interna resolvida, o código de falha e o resultado financeiro em estado privado. `Snapshot` e `Result` devolvem cópias, sem referências mutáveis ao agregado. IDs e horário são fornecidos pelo chamador.

**Entrada:** `NewExternal` exige IDs não nulos, provedor, ID externo, chave de idempotência, hash, rodada, jogo, tipo e `Money` válido. O hash deve ter o formato SHA-256 hexadecimal minúsculo; o cálculo canônico e a detecção de conflitos ainda não estão implementados e serão responsabilidade da aplicação. Ele nunca deverá ser confiado ao cliente. Campos textuais obrigatórios não podem ser vazios ou conter apenas espaços; não são normalizados silenciosamente.

`BET`, `WIN`, `REFUND` e `ROLLBACK` exigem valor positivo; `LOSS` exige zero. Quantias inválidas são erros de contrato (`ErrInvalidAmount`) antes de criar a transação. `REFUND`/`ROLLBACK` exigem referência; `WIN` permite referência opcional a BET. Referências em BET/LOSS, referências vazias compostas de espaços e autorreferência externa são rejeitadas como contrato inválido.

**Abertura:** `OPENING` é proibido no construtor externo. `NewOpening` recebe identidade, carteira, jogador e quantia sem os metadados externos. A origem interna é determinada por `Kind == OPENING`. Sua identidade estável deverá ser reutilizada pelo caso de uso e protegida por unicidade no banco. O domínio permite valor zero, mas a criação de carteira com zero deverá omitir OPENING, ledger e eventos financeiros. Ao processar uma abertura positiva, a carteira deve já conter exatamente esse saldo, versão 1 e instante de criação correspondente; não se credita novamente. Ledger de abertura e eventos serão criados na próxima etapa, com confirmação SQL posterior.

**Transições implementadas:**

```mermaid
stateDiagram-v2
    [*] --> PENDING
    PENDING --> PROCESSED
    PENDING --> REJECTED
    PENDING --> FAILED
    PENDING --> PENDING_REFERENCE
    PENDING_REFERENCE --> PENDING_REFERENCE: referência ainda pendente
    PENDING_REFERENCE --> PROCESSED
    PENDING_REFERENCE --> REJECTED
    PENDING_REFERENCE --> FAILED
```

`Process` é permitido somente em PENDING e PENDING_REFERENCE. PROCESSED, REJECTED e FAILED são terminais: uma nova chamada retorna `ErrInvalidTransition` sem alterar carteira ou transação. O futuro caso de uso de replay deverá devolver `Result`, sem chamar `Process` novamente. `Rehydrate` valida combinações de estado, resultado, referência e código sem executar movimentações. Datas são UTC e transições não aceitam instantes anteriores à última atualização.

**Processamento:** `Process(wallet, reference, at)` recebe dados já carregados pela aplicação. Ele valida carteira/jogador e trabalha sobre uma cópia da carteira. Apenas após validar o próximo estado da transação copia o saldo calculado para o agregado original. Erros retornados deixam ambos os agregados intactos. Rejeição de negócio é uma avaliação concluída: retorna `nil`, define REJECTED e `FailureCode`, preservando a carteira. O chamador deve sempre examinar `Status`; `nil` não significa necessariamente PROCESSED.

Quando uma carteira foi consultada, conclusões e rejeições guardam saldo/versão observados. Movimentos posteriores não alteram esse resultado. Uma rejeição por moeda incompatível guarda o saldo real na moeda da carteira. `Reject(code, at)`, usado por exemplo na expiração de referência, pode finalizar sem resultado financeiro quando não houve leitura da carteira; a API futura deverá representar essa ausência sem inventar um saldo zero.

**Referências:** são conferidos ID externo, provedor, carteira, jogador, moeda e rodada, além de impedir autorreferência interna. Não há uma restrição adicional de mesmo jogo: o enunciado exige mesma rodada. WIN e REFUND referenciam BET; ROLLBACK referencia BET, WIN ou REFUND. Referência ausente ou ainda pendente resulta em PENDING_REFERENCE sem efeito financeiro. Referência que terminou rejeitada/falha causa rejeição definitiva. WIN pode pagar valor diferente da aposta; REFUND e ROLLBACK exigem valor integral igual.

A [ADR 0001](docs/adr/0001-compensacao-direta.md) define uma única compensação direta bem-sucedida. `Reference.Compensated` representa uma consulta à persistência; não deve vir de HTTP/SQS. A referência original não é modificada. Falta implementar a coordenação SQL e o índice único que tornarão essa política segura entre processos.

**Movimentação:** BET debita; WIN e REFUND creditam; LOSS mantém inclusive versão e timestamp da carteira; ROLLBACK de BET credita; ROLLBACK de WIN/REFUND debita. Insuficiência na reversão tem código diferente da aposta. Overflow monetário é rejeição auditável. Estouro de versão ou estado inválido retorna erro sem efeitos; não é convertido automaticamente em falha de infraestrutura.

| Código persistível | Situação |
| --- | --- |
| `INSUFFICIENT_FUNDS` | BET sem saldo suficiente |
| `REVERSAL_INSUFFICIENT_FUNDS` | Reversão debitadora sem saldo |
| `REFERENCE_NOT_FOUND` | Prazo/tentativas esgotados, decidido pelo futuro worker |
| `REFERENCE_NOT_PROCESSED` | Referência terminou sem sucesso |
| `REFERENCE_SCOPE_MISMATCH` | Referência de outro escopo/ID ou autorreferência interna |
| `REFERENCE_AMOUNT_MISMATCH` | Compensação não integral |
| `INVALID_REFERENCE_KIND` | Tipo da referência incompatível |
| `ALREADY_REVERSED` | Referência já compensada diretamente |
| `WALLET_CURRENCY_MISMATCH` | Moeda da operação diferente da carteira |
| `BALANCE_OVERFLOW` | Crédito ultrapassaria o limite monetário |
| `PERMANENT_INFRASTRUCTURE_FAILURE` | Falha permanente explicitamente diagnosticada |

`FailPermanent` é uma transição explícita para FAILED. Falhas transitórias de PostgreSQL/SQS devem manter a operação passível de retry; este pacote não acessa infraestrutura nem classifica erros de I/O. A aplicação deverá distinguir essas situações antes de chamar a transição.

**Limites:** o processamento atual é de domínio em memória, sem ledger/eventos nem publicação. A aplicação terá de confirmar carteira, transação, ledger, inbox e outbox em uma única transação SQL. Não há ainda hash canônico, idempotência durável, contador de tentativas, deadline, backoff ou worker de referências. Reidratar uma pendência é testado; recuperação após reiniciar processos e banco continua pendente. Os agregados não devem ser compartilhados entre goroutines.

## WalletLedgerEntry e eventos tipados — quarto incremento da Fase 1

`internal/domain/ledger` e `internal/domain/events` fecham a base de domínio da Fase 1 sem nenhuma dependência de infraestrutura.

**Ledger:** `Entry` é um tipo-valor imutável, como `Money`. Campos privados: identidade do lançamento, carteira, transação, direção (`DEBIT`/`CREDIT`), quantia, saldo anterior, saldo posterior e instante em UTC. Não há setters nem transições; por isso um único construtor serve tanto para criação quanto para reidratação. O construtor valida a exigência do enunciado: `balanceAfter = balanceBefore ± amount` conforme a direção, calculado com `Money.Add`/`Sub` (moeda e overflow checados); quantia obrigatoriamente positiva (LOSS e rejeições não geram lançamento); saldos não negativos; UUIDs não nulos; timestamp não nulo. Erros classificáveis por `errors.Is`: `ErrInvalidEntry`, `ErrInvalidDirection`, `ErrInvalidTime`, `ErrNonPositiveAmount`, `ErrNegativeBalance` e `ErrBalanceMismatch`, preservando os sentinelas de `Money`. O domínio não tem JSON próprio aqui: o DTO de leitura do ledger será do adaptador HTTP. Unicidade `(wallet_id, transaction_id)`, trigger contra `UPDATE`/`DELETE` e política contra `TRUNCATE` são responsabilidades do schema PostgreSQL (Fase 2).

**Eventos:** quatro tipos concretos — `WagerTransactionProcessed` (inclusive `LOSS` e `OPENING` interno), `WagerTransactionRejected`, `WalletBalanceChanged` e `WagerTransactionPendingReference`. `Envelope` é um tipo-valor imutável com `eventID`, `eventType`, `aggregateID`, `correlationID`, `causationID` opcional (`uuid.Nil` significa ausência; `CausationID()` devolve `(id, ok)`), `occurredAt` em UTC, `version` do payload por tipo (hoje 1) e `data` tipado. O `aggregateID` é **derivado dos dados** — transação nos eventos de transação, carteira em `WalletBalanceChanged` — portanto o envelope não pode divergir do payload; `Validate` confere essa coerência como defesa adicional. Kinds permitidos por evento: `Processed` aceita os seis (inclusive `OPENING`); `Rejected` apenas os cinco externos (`OPENING` nunca rejeita); `PendingReference` apenas `WIN`/`REFUND`/`ROLLBACK` (únicos que carregam referência externa). Regras de quantia por kind não são revalidadas aqui: quem emite o evento é um agregado que já as impôs; o evento valida estrutura, não política.

**WalletBalanceChanged:** o payload espelha o lançamento do ledger (`walletId`, `transactionId`, `direction`, `money`, `balanceBefore`, `balanceAfter`, `walletVersion`). O ID do lançamento não pertence ao evento, então não é possível reusar `ledger.Entry`; a invariante aritmética é revalidada localmente (poucas linhas, custo fixo) porque o evento inválido deve ser irrepresentável, não corrigido depois.

**JSON da outbox:** `Envelope.MarshalJSON` define agora o contrato do payload persistido em `outbox_events`: chaves `eventId`, `eventType`, `aggregateId`, `correlationId`, `causationId` (omitido quando ausente), `occurredAt` em RFC 3339 UTC, `version` e `data`, com dinheiro em string decimal via `Money.MarshalJSON`. Marshal valida o envelope e rejeita o valor zero. Não há `UnmarshalJSON`: o caminho de escrita é a outbox e a desserialização pertence aos consumidores.

**Limites desta etapa:** eventos são construídos em memória pelo futuro caso de uso; a gravação na `outbox_events` no mesmo commit SQL, o publisher com lease/backoff e a publicação somente após commit continuam pendentes. Os testes unitários cobrem construtores, kinds por evento, invariante de saldo em `WalletBalanceChanged`, envelope inconsistente e contrato JSON; cobertura de instruções: 100,0% em `ledger` e 95,0% em `events` (restam marcadores de interface e ramos internos de defesa). Nada aqui substitui os testes de integração com PostgreSQL real.

## Persistência — schema PostgreSQL (Fase 2, primeiro incremento)

Quatro migrations `up`/`down` em `migrations/` seguem a convenção do golang-migrate (runner ainda não integrado). Cada decisão do domínio tem contraparte no schema, e cada restrição foi verificada em container efêmero `postgres:17-alpine` com asserção do nome exato da constraint violada.

**Wallets:** `CHECK(balance_cents >= 0)`, `CHECK(version >= 1)`, moeda em lista explícita (`BRL`, `USD` — o schema aceita o que o domínio aceita; a restrição BRL-only da aplicação vem depois), `CHECK(updated_at >= created_at)` e índice único `(player_id, currency)`. Sem `DEFAULT` de horário: a aplicação fornece instantes UTC explícitos, coerente com os construtores do domínio.

**Wager_transactions:** a máquina de estados do domínio está codificada em `CHECK`s independentes — presença de `failure_code` é equivalente lógica a `status IN ('REJECTED','FAILED')`; resultado sempre em par (`result_balance_cents`/`result_wallet_version`) não negativo; `PROCESSED` exige resultado; `FAILED` proíbe; estados pendentes são puros (sem resultado, código ou referência resolvida); `OPENING` processado exige resultado igual à quantia com versão 1. Metadados externos são todos obrigatórios e não vazios para kinds externos e todos nulos para `OPENING`; `payload_hash` segue `^[0-9a-f]{64}$`; valor por kind (`LOSS` zero, externos positivos, `OPENING` não negativo); referência externa obrigatória em `REFUND`/`ROLLBACK`, proibida em `BET`/`LOSS`/`OPENING`, opcional em `WIN`; `reference_transaction_id` tem FK própria e proibição de autorreferência. Idempotência durável por dois índices únicos parciais (`(provider_id, idempotency_key)` e `(provider_id, external_transaction_id)` sobre origem externa); `UNIQUE(wallet_id) WHERE kind='OPENING'` impede crédito inicial duplicado; `UNIQUE(reference_transaction_id) WHERE kind IN ('REFUND','ROLLBACK') AND status='PROCESSED'` materializa a ADR 0001 (uma única compensação direta bem-sucedida, seja REFUND ou ROLLBACK); índice parcial por `next_attempt_at` serve o worker de referências.

**Ledger:** `UNIQUE(wallet_id, transaction_id)`, quantia positiva, saldos não negativos e `CHECK` aritmético por direção; soma além de `BIGINT` gera erro SQL no insert. Append-only por dois triggers: `BEFORE UPDATE OR DELETE` por linha e `BEFORE TRUNCATE` por statement, ambos levantando exceção. Isso bloqueia qualquer papel que não possa desabilitar triggers; a separação entre usuário de migração e usuário de runtime (sem `ALTER`/`DROP`/`TRUNCATE`/desabilitar trigger) vem com o ambiente Compose.

**Inbox e outbox:** inbox com `PRIMARY KEY(consumer_name, message_id)` e `CHECK(completed_at >= received_at)`; a conclusão compartilhará a transação SQL do tratamento. Outbox com tipos de evento válidos, `attempts >= 0`, `next_attempt_at NOT NULL` (evento nasce elegível), lease pareado (`leased_by`/`leased_until` juntos ou ausentes), índice parcial de pendentes por `next_attempt_at` e índice por `(aggregate_id, occurred_at)`.

**Verificação:** aplicação das quatro `up`, fluxo positivo completo e 41 blocos de violação (cada um afirma o nome da constraint que rejeitou), seguidos das quatro `down` deixando o schema vazio. Três casos têm proteções naturalmente sobrepostas (kind desconhecido também viola a regra de quantia; estado não terminal com código viola duas regras; direção desconhecida também viola a aritmética) e a asserção aceita qualquer uma delas — o PostgreSQL não garante ordem entre `CHECK`s.

**Pendências:** runner golang-migrate com alvos de Makefile e Compose; usuários distintos de migração e runtime; testes de integração Go com PostgreSQL real (o item de testar constraints/trigger do plano exige automação, não só verificação manual); repositórios e `UnitOfWork` com `pgx`.

## pgx — pool e UnitOfWork (Fase 2, segundo incremento)

`github.com/jackc/pgx/v5` (v5.11.0) com SQL explícito; `sqlc` e ORM ficam de fora. A primeira porta da aplicação vive em `internal/application/ports`: `UnitOfWork.WithinTx(ctx, fn)` — commit quando `fn` retorna nil, rollback em erro ou cancelamento, e **aninhamento que une à transação externa**. Um caso de uso e seus helpers compartilham sempre um commit; nenhuma função aninhada commita sozinha.

**Implementação (`internal/adapters/postgres`):** a `pgx.Tx` ativa viaja no `context`; repositórios futuros deste pacote a obtêm por `TxFromContext`, sem vazar `pgx` pela aplicação. O rollback usa `context.WithoutCancel` (cancelamento não pode impedir a limpeza) e o erro original do caso de uso é preservado — `errors.Is` do chamador continua funcionando; falha de rollback é agregada com `errors.Join`. Asserção de compile-time amarra o adaptador à porta. O pool é construído sem conectar (primeira conexão é lazy); `pgxpool.ParseConfig` valida a URL (aceita scheme `postgres://` e DSN keyword/value) — valores inválidos derrubam a inicialização. Padrões fixos: `MaxConnLifetime` 30m, `MaxConnIdleTime` 5m, `HealthCheckPeriod` 1m; contagem de conexões segue o padrão do pgxpool até o Compose definir dimensionamento por réplica sem esgotar `max_connections`.

**Composição:** `DATABASE_URL` é opcional nesta etapa — vazia, a aplicação sobe apenas com HTTP (não há endpoints de negócio); definida, o módulo `database` entra com ping de 5s no `OnStart` (banco inacessível impede o serviço de aceitar tráfego) e `Close` no `OnStop`. No Fx 1.24, `fx.New` executa os invokes na construção, então URL inválida falha já em `app.Err()`, antes de qualquer hook. O adaptador não importa Fx; hooks vivem no bootstrap, como no HTTP.

**Testes:** unitários cobrem parsing/config e o gate do módulo; os transacionais (commit, rollback por erro, rollback por cancelamento, aninhado une, erro interno derruba o externo) exigem PostgreSQL real, usam build tag `integration` e pulam sem `TEST_DATABASE_URL` — nenhum mock substitui o banco. **Retry de deadlock** (`40P01`/`40001` com limite e jitter) fica para o incremento do `ProcessWager`, onde reexecutar o caso de uso é seguro; o `UnitOfWork` atual nunca reexecuta `fn` por conta própria.

## Abertura atômica de carteira — caso de uso e repositórios (Fase 2, terceiro incremento)

`internal/application/command` nasce com `CreateWallet`, e `internal/application/ports` ganha as quatro portas de escrita: `WalletRepository`, `WagerTransactionRepository`, `LedgerRepository` e `OutboxRepository`. Elas recebem snapshots do domínio (não DTOs) e **exigem transação ativa** — os repositórios recusam escritas fora do `UnitOfWork`, codificando a regra "estado financeiro nunca commita sozinho".

**Casos de uso:** `CreateWallet` valida a entrada na fronteira da aplicação — BRL-only (o domínio aceita BRL/USD; a restrição da aplicação é permitida pelo enunciado), quantia não negativa e IDs não nulos. O saldo positivo grava, em um único commit: carteira versão 1, `OPENING` em `PROCESSED` (o agregado confirma o saldo existente — nunca credita duas vezes), lançamento de crédito 0→saldo e os eventos `WagerTransactionProcessed` + `WalletBalanceChanged` na outbox; o `causationId` de ambos é a transação de abertura. Saldo zero grava apenas a carteira, como o contrato exige. Identidades são UUID v7 com fallback v4 (entropia insuficiente não derruba o caso de uso; o requisito é unicidade) e o relógio é injetado.

**Repositórios:** SQL explícito com colunas nomeadas; a `pgx.Tx` vem do contexto. O mapeamento de erros é pelo par SQLSTATE + nome da constraint: `23505` em `wallets_player_currency_uidx` → `ports.ErrWalletAlreadyExists` (futuro 409). Metadados externos ausentes viram `NULL`; `HasResult` decide o par de colunas de resultado. O payload da outbox é o JSON imutável do envelope; `next_attempt_at` nasce igual a `occurred_at` (elegível imediatamente), `attempts` em zero.

**Harness de integração (`internal/testsupport`, tag `integration`):** cria o pool direto via `pgxpool` (não importa o adaptador, evitando ciclo de importação com os testes do próprio adaptador), aplica as migrations reais uma única vez por banco (`to_regclass('public.wallets')` como guarda — múltiplos binários de teste compartilham o schema sem recriá-lo) e nunca reseta dados: cada teste usa identidades únicas. Sem `TEST_DATABASE_URL` os testes pulam; com ela, nada é mockado.

**Testes:** unitários do caso de uso com fakes de orquestração (ordem das escritas, zero vs positivo, conflito interrompe antes das escritas financeiras, erro tardio propaga, entradas inválidas não tocam portas) — 83,6% de cobertura unitária; os fakes não substituem infraestrutura, só verificam orquestração, e a semântica transacional/SQL é toda da suíte de integração. Integração contra PostgreSQL real: abertura positiva persiste carteira + `OPENING` + ledger + 2 eventos; zero não gera nada financeiro; duplicata retorna `ErrWalletAlreadyExists` sem efeitos; **falha no meio do caso de uso (ledger injetado) faz rollback de tudo**; jogadores independentes recebem carteiras independentes. Cobertura `postgres`: 41,0% unitária / 55,1% com integração (ramos restantes são tradução de erros SQL ainda não exercitados). Nada ainda cobre concorrência entre processos — os cenários oficiais (duas apostas de 80, 50 duplicatas) exigem o `ProcessWager` e três instâncias.



## Leituras, cursor e reconciliação (Fase 2, quarto incremento)

`internal/application/query` agrega os casos de uso de leitura — `GetWallet`, `ListLedger` e `ReconcileWallet` — e `ports/queries.go` define as portas correspondentes (`WalletReader`, `LedgerReader`, `ReconciliationReader`), com `ErrWalletNotFound`, `ErrInvalidCursor` e `ErrLedgerSumOutOfRange` classificáveis para os futuros códigos HTTP.

**Leituras:** os readers se unem à transação do contexto quando existe uma (preparando o `SELECT ... FOR UPDATE` do futuro `ProcessWager`) ou usam o pool direto. Wallets reidratam pelo construtor do domínio — linha corrompida é erro, nunca aceitação silenciosa. Ledger pagina com cursor **opaco base64url** de `(created_at, id)`: ordenação estável com tie-break por uuid quando os timestamps coincidem, comparação por linha no PostgreSQL, `LIMIT n+1` para detectar próxima página e nenhum `OFFSET`. Cada entrada de ledger é reconstruída por `ledger.New`, revalidando a aritmética na leitura. A moeda vem por JOIN com a carteira: a tabela de ledger não tem coluna de moeda por decisão de schema, e o join impede adivinhação.

**Reconciliação:** transação `REPEATABLE READ READ ONLY` — saldo armazenado e soma do ledger saem do **mesmo snapshot**, imune a movimentos concorrentes, e o modo leitura garante que a reconciliação jamais altera o saldo (correções exigem novos lançamentos). A soma usa `SUM` em `NUMERIC` (não transborda) convertida com `math/big` e checagem de faixa antes de `FromMinorUnits`; um total fora do intervalo representável retorna `ErrLedgerSumOutOfRange` — falha auditable em vez de truncamento. `difference` é o saldo armazenado menos o reconstruído, podendo ser negativo (permitido em `Money` para diferenças).

**Proteções automatizadas:** os testes de integração agora exercitam em Go o trigger append-only (`UPDATE`, `DELETE` e `TRUNCATE` rejeitados, entrada intacta depois) e o `CHECK` de saldo negativo (SQLSTATE 23514), fechando o item 4 da Fase 2 que antes dependia só da verificação manual das migrations.

**Harness:** binários de teste diferentes correm em paralelo e disputavam a criação do schema; a aplicação das migrations agora ocorre sob `pg_advisory_xact_lock` com verificação `to_regclass`, dentro de uma transação — o schema aparece atomicamente e apenas uma vez. Os dados nunca são resetados (o trigger proíbe apagar o ledger), então os testes usam identidades únicas por execução; a suíte roda com `-count=2` no mesmo banco para provar isso.

## ProcessWager — processamento transacional compartilhado

`internal/application/command/process_wager.go` coordena as cinco operações externas através das portas existentes. O domínio continua responsável por valores, movimentos, rejeições e referências; o adaptador PostgreSQL executa SQL explícito. O módulo Fx da aplicação fornece `ProcessWagerDeps`, com teste de resolução do grafo. Não existe dependência de HTTP/SQS no caso de uso.

**Transação e concorrência:** a UnitOfWork abre uma transação READ COMMITTED. Primeiro procura as identidades por provedor. Sem replay, bloqueia apenas a linha da carteira com `SELECT ... FOR UPDATE`, consulta novamente a idempotência após a espera e avalia o domínio com o saldo atualizado. Atualiza saldo/versão usando também `WHERE version = expectedVersion`. Insere a transação, o lançamento apenas se houve movimento e os eventos na mesma transação SQL. LOSS e rejeições têm evento de resultado, mas não alteram saldo/versão nem criam ledger. Nenhum evento é publicado nesta etapa.

A ordem é carteira → leitura da referência → escritas. Referências são consultadas sem adquirir outro lock de carteira: somente referências da mesma carteira podem movimentar saldo, logo todos os escritores válidos dessa referência já estão serializados. O índice único de compensação direta reforça a regra. Carteiras distintas avançam independentemente; não existe mutex global na aplicação. O relógio é lido depois do lock, truncado a microssegundos e limitado inferiormente ao `updated_at` observado, evitando regressão por espera ou pequeno desvio entre relógios. Não se usa timestamp para decidir a ordem financeira: lock e versão fazem isso.

**Hash e identidades:** a aplicação calcula SHA-256, em hexadecimal minúsculo, do JSON UTF-8 de um DTO tipado, com chaves declaradas em ordem lexicográfica:

```text
amount, currency, externalTransactionId, gameId, kind, playerId,
providerId, referenceExternalTransactionId (quando presente), roundId, walletId
```

`amount` é uma string normalizada para duas casas por `Money.FormatDecimal`; UUIDs são strings canônicas. `json.Marshal` produz o documento compacto, com o escape padrão do Go (inclusive `<`, `>` e `&`); não há normalização Unicode, de caixa ou de espaços de identificadores. Textos inválidos em UTF-8 ou com NUL são recusados. Referência vazia é omitida. Chave de idempotência, correlation ID e metadados de transporte não participam do hash. Um vetor fixo independente verifica o digest; formas decimais equivalentes e alterações dos campos de negócio também são testadas. A implementação usa JSON ordenado deste contrato, sem afirmar conformidade geral com RFC 8785.

O futuro DTO HTTP/SQS deve converter ausência da referência para string vazia e rejeitar `null`, chaves repetidas e campos desconhecidos antes de chamar a aplicação. Esses parsers de transporte ainda não existem. Ambos usarão este mesmo cálculo; o cliente nunca fornece o hash confiável.

`(provider_id, idempotency_key)` com hash igual retorna o estado persistido; hash diferente retorna `ErrIdempotencyConflict`. `(provider_id, external_transaction_id)` sob outra chave também é conflito, sem criar aliases. Para operações terminais o resultado é o saldo/versão original, sem reprocessar regras e sem novas escritas. A aplicação permanece **BRL-only**, inclusive para carteiras acessadas pelo comando; domínio/schema continuam aceitando USD para testes de moeda. Assim, o resultado persistido e a moeda da operação não divergem.

**Retries:** o adaptador traduz SQLSTATE 40P01/40001 e 23505 somente dos dois índices de identidade para `ports.ErrRetryableTransaction`. O comando reexecuta toda a UnitOfWork, até quatro tentativas, com espera exponencial de base 25ms e jitter de até 25ms; o `context` interrompe a espera. Violações da outbox, checks, falhas de conexão ou commit ambíguo não são repetidos automaticamente. O chamador pode reenviar com a mesma identidade para consultar o resultado durável.

No futuro consumidor SQS, a UnitOfWork externa da inbox será dona do commit e do retry completo. O aninhamento apenas compartilha a transação; não deve tentar recuperar uma transação SQL abortada repetindo só o comando interno. Falhas transitórias não são convertidas em `FAILED` nem publicadas como rejeições de negócio.

**Referências:** ausentes ou ainda pendentes geram `PENDING_REFERENCE` e um evento durável sem ledger. Escopo, tipo, valor ou resultado terminal incompatível geram rejeição estável conforme o domínio. Replay de pendência apenas consulta o estado. `attempt_count`, `next_attempt_at` e `reference_deadline_at` continuam sem política operacional; TTL, claim, agendamento e retomada após reinício pertencem à Fase 4. Nenhuma migration existente foi alterada.

**Evidências:** PostgreSQL 17 real, sem substituto em memória: BET, WIN, LOSS, REFUND, ROLLBACK de BET/WIN/REFUND, insuficiência de saldo de reversão, conflitos, referências incompatíveis e pendentes; 50 duplicatas simultâneas; disputa 80+80 sobre 100; compensações concorrentes; disputa da mesma identidade entre carteiras diferentes; carteira independente avançando durante lock. Um erro SQL 23505 na segunda inserção da outbox verifica rollback inclusive do primeiro evento e permite nova execução limpa. Reconciliação confirma o saldo em cenários de concorrência e reversões. Trocar os readers verifica leitura durável, mas não equivale a reiniciar três processos. Esses testes distribuídos permanecem pendentes.

**Go e explicação para entrevista:** interfaces nas portas permitem que o comando dependa de operações necessárias, sem conhecer `pgx`. `context.Context` transporta cancelamento e, dentro do adaptador, a transação ativa. `%w`, `errors.Is` e `errors.As` preservam e classificam causas sem comparar mensagens. Snapshots são valores; cada chamada reidrata seus próprios agregados. Nos testes, goroutines iniciadas por um canal disputam conexões PostgreSQL e `sync.WaitGroup` aguarda a conclusão. Uma explicação curta: “O banco serializa operações por carteira; a chave única impede duplicações; saldo, ledger e eventos são confirmados juntos. O replay lê o resultado original, e a outbox permitirá publicar depois do commit.”

## Trabalho ainda pendente

- Endpoints financeiros com Keycloak/OIDC, isolamento por provedor e autorização das operações internas. Filtro por provedor no repositório não substitui autenticação.
- Worker de referências com TTL/backoff e recuperação após reinício.
- SQS/inbox, outbox publisher com lease, DLQ e testes de falhas entre commit, publicação e confirmação.
- Runner de migrations, Compose reproduzível, readiness, métricas e demonstração com três processos independentes.

As seções anteriores registram a evolução por incremento; as garantias atuais do processamento estão descritas acima. A entrega completa continua pendente.

# Arquitetura da solução

## Estado e direção

A Fase 0 está concluída e a Fase 1 começou com `Money`. A direção é uma arquitetura hexagonal enxuta: domínio independente, casos de uso compartilhados por HTTP e SQS, adaptadores para entrada e persistência, e composição centralizada. A precisão monetária está implementada no tipo; as garantias transacionais e distribuídas ainda estão pendentes.

Dependências planejadas:

```text
HTTP / SQS -> aplicação -> domínio
PostgreSQL / mensageria -> contratos definidos pela aplicação
bootstrap -> composição dos componentes concretos
```

Não criamos interfaces, repositórios ou pastas vazias antes de seus casos de uso.

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

## Decisões previstas, ainda não implementadas

O plano mestre detalha as escolhas que serão confirmadas com código e testes:

- `Wallet` com criação/reidratação separadas, saldo não negativo, débito/crédito e versão.
- PostgreSQL com `pgx`, SQL explícito e transação única para estado financeiro, ledger, inbox e outbox; constraints protegendo invariantes.
- Coordenação por carteira entre processos; idempotência persistente e resultado original disponível para replay.
- Compensação direta única por transação, referências pendentes duráveis e retries limitados.
- Keycloak/OIDC, escopo por provedor e operações internas autorizadas.
- SQS com inbox, outbox após commit e recuperação de publicação com IDs estáveis.

Esses itens não estão concluídos. Migrations, ambiente Compose, endpoints de negócio, métricas e testes distribuídos serão acrescentados nas fases seguintes. O próximo incremento do domínio será `Wallet`, usando `Money` para preservar as invariantes de saldo e moeda.

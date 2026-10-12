# Arquitetura da solução

## Estado e direção

Esta primeira entrega implementa somente a Fase 0 do plano. A direção é uma arquitetura hexagonal enxuta: domínio independente, casos de uso compartilhados por HTTP e SQS, adaptadores para entrada e persistência, e composição centralizada. Ainda não existem garantias financeiras implementadas.

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

## Decisões previstas, ainda não implementadas

O plano mestre detalha as escolhas que serão confirmadas com código e testes:

- `Money` imutável, centavos em `int64`, JSON decimal em string e normalização de `1`, `1.0` e `1.00` para `1.00`; rejeição de escala excedente e overflow.
- PostgreSQL com `pgx`, SQL explícito e transação única para estado financeiro, ledger, inbox e outbox; constraints protegendo invariantes.
- Coordenação por carteira entre processos; idempotência persistente e resultado original disponível para replay.
- Compensação direta única por transação, referências pendentes duráveis e retries limitados.
- Keycloak/OIDC, escopo por provedor e operações internas autorizadas.
- SQS com inbox, outbox após commit e recuperação de publicação com IDs estáveis.

Esses itens não estão concluídos. Migrations, ambiente Compose, endpoints de negócio, métricas e testes distribuídos serão acrescentados nas fases seguintes. O primeiro incremento do domínio será `Money` com testes unitários de precisão e limites.

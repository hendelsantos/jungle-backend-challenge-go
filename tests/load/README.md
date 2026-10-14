# Suíte de performance com k6

Estes cenários medem o backend de entrega com carga real e independente, e
cada um fecha com validação financeira. Os cenários criam carteiras e movimentações: os dados
são fictícios, o alvo é `127.0.0.1`, e a suíte usa os mesmos contratos
autenticados de qualquer cliente.

## Pré-requisitos

```sh
docker compose up --build -d          # ambiente de entrega com 3 réplicas
k6 version                            # v2.3.0 usado nos experimentos (k6 CLI)
```

- Gateway `http://localhost:9080`, IdP `http://localhost:59080` (defaults).
- Tokens são obtidos por `client_credentials` nos clientes de desenvolvimento.
- O verificador independente usa banco com SQL somente leitura
  (`127.0.0.1:59532`, publicado pelo Compose de entrega).

## Cenários

| Cenário | Arquivo | O que prova |
| --- | --- | --- |
| A. Smoke | `scenarios/smoke.js` | saneamento: abre carteira, BET, replay, saldo, ledger, reconciliação; aborta se `/health/ready` falhar |
| B. Ramp-up | `scenarios/ramp-up.js` | 10→25→50→100 VUs com rampas; uma carteira por VU (carteiras nunca compartilhadas) |
| B2. Nível único | `RAMP_LEVEL=25 k6 run scenarios/ramp-up.js` | um nível fixo de VUs para tabular latência por patamar |
| C. Contenção | `scenarios/contention.js` | 50 BETs distintas de 30.00 rap simultâneas em carteira de 1000.00 → 33 aceitas / 17 rejeitadas, saldo 10.00 |
| D. Idempotência | `scenarios/idempotency.js` | 50 requisições idênticas simultâneas → 1 processada, 49 replays, 1 débito |
| E. Carteiras independentes | `scenarios/multiwallet.js` | ciclo BET→WIN→LOSS→REFUND→ROLLBACK por carteira; saldo volta exatamente, ledger com shape exato |
| F. Soak | `scenarios/soak.js` | 15 min de carga moderada com reconciliação periódica ao vivo |

## Executando

```sh
mkdir -p tests/load/results
cd tests/load
k6 run --summary-export results/smoke.json scenarios/smoke.js
k6 run --summary-export results/ramp-up.json scenarios/ramp-up.js
k6 run --summary-export results/contention.json scenarios/contention.js
k6 run --summary-export results/idempotency.json scenarios/idempotency.js
k6 run --summary-export results/multiwallet.json scenarios/multiwallet.js
k6 run --summary-export results/soak.json scenarios/soak.js
```

Cada cenário imprime checks financeiros no teardown e exporta `results/*.json`.
Interromper com Ctrl-C é seguro a qualquer momento; o Ctrl-C duplo força a saída.

## Verificação independente (obrigatória pós-carga)

O verificador **não confia** no campo `consistent` da API: audita por SQL
somente leitura a soma do ledger, a cadeia de saldos, a contagem por kind/status
(pendências), e o saldo persistido. Exemplos conforme cada cenário:

```sh
# Execute a partir da raiz do repositório.
SURL='postgres://jungle:jungle-local@127.0.0.1:59532/jungle?sslmode=disable'
go build -o bin/verifywallet ./cmd/verifywallet

# Contenção (uma carteira): 33 BETs aceitas, 17 rejeitadas, saldo 10.00, 34 linhas
./bin/verifywallet -url "$SURL" -final 10.00 -rows 34 \
  -counts "OPENING=1,BET=33" -rejections "BET=17" WALLET_ID

# Idempotência: um débito em 100.00 → 50.00, 2 linhas, 1 única BET
./bin/verifywallet -url "$SURL" -final 50.00 -rows 2 \
  -counts "OPENING=1,BET=1" WALLET_ID

# Multiwallet: cada carteira volta ao saldo de abertura com shape exato
./bin/verifywallet -url "$SURL" -final 400.00 -rows 9 \
  -counts "OPENING=1,BET=2,WIN=2,LOSS=2,REFUND=2,ROLLBACK=2" WALLET_ID...
```

Os `WALLET_ID` s podem vir da API (respostas de abertura ou `SELECT out =
...`) — TAO DENTRO do projeto forensics via Compose apenas em loopback.

## Segurança e limites

- O alvo é sempre localhost/Compose isolado. Não há direção externa.
- Thresholds cumprem metas experimentais do relatório (p95<500ms em carga
  moderada, erros técnicos < 1%) e são ajustados por variável de ambiente.
- 422/409 são classificados separadamente. Todos os checks financeiros têm
  threshold obrigatório; qualquer check falho reprova. A taxa financeira usa
  somente wagers; chamadas auxiliares têm taxa própria.
- Os saldos de abertura dos pools (`WALLET_POOL_OPENING`, 100000.00) são
  fictícios e irreais e são usados somente para medir latência sob carga.
- [`results/`] arquivos são dados de teste; não versionados.
- Ajustes por variáveis: `SOAK_DURATION`, `SOAK_VUS`,
  `WALLET_POOL_SIZE`, `RAMP_LEVEL`, `RAMP_HOLD`, `BARRIER_MS`, entre outros no
  `config.js`.

## Regressão reproduzível dos instrumentos

Da raiz, com Compose isolado já pronto:

```sh
k6 run tests/load/probes/classifier-probe.js
# Sonda propositalmente negativa: deve sair 99, não zero.
k6 run tests/load/probes/gate-negative-probe.js

GATEWAY_URL=http://localhost:9080 KEYCLOAK_URL=http://localhost:59080 \
DATABASE_URL='postgres://jungle:jungle-local@127.0.0.1:59532/jungle?sslmode=disable' \
bash scripts/load-regression.sh
```

O runner executa smoke, idempotência padrão e 2×1.00, contenção padrão e
2×1.00 sobre 100.00, 25 carteiras com dois ciclos completos, rampa de 25 VUs
por 30s mais 10s de descida e ensaio sustentado **de 60 segundos** com 25 VUs.
Registra duração real e resumo JSON com p50/p95/p99. Cada cenário passa pelo
verificador SQL independente em **todas** as carteiras criadas, incluindo
expectativas exatas para contenção/idempotência/multiwallet. O verificador usa
REPEATABLE READ READ ONLY; expectativas de contagem final exigem carga concluída.
Use `RESULTS_DIR` para escolher o diretório dos artefatos (padrão `/tmp`).
O ensaio de 60s não substitui uma campanha de endurance de 15 minutos.

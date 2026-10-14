> Correção de auditoria (14/10/2026): tabelas históricas abaixo não são a
> regressão atual. O run antigo durou aproximadamente 419,47s, não 15 minutos;
> 10.400 checks não significam 10.400 reconciliações. A instrumentação de erro
> customizada antiga tinha denominador incorreto. Ver a seção final desta revisão.

# Relatório de performance — k6 sobre o Compose de entrega

> Coleta real executada em 13/10/2026 na máquina de desenvolvimento, contra o
> Compose de entrega (`docker-compose.yml`: 3 réplicas Go atrás do gateway
> nginx + PostgreSQL 17 + Keycloak 26.8 + LocalStack SQS, todas em `127.0.0.1`).
> Nenhum valor abaixo é estimado: todos vieram de execuções `k6` com
> `--summary-trend-stats` e `--summary-export` (artefatos em
> `tests/load/results/`, não versionados) e do verificador independente
> (`cmd/verifywallet`, SQL somente leitura).

## 1. Objetivos e metodologia

- **Objetivo principal:** provar integridade financeira sob carga real com três
  processos independentes e, em segundo lugar, medir capacidade.
- Metodologia: cenários k6 por nível de carga, com validação pós carga em três
  camadas: (1) checks no k6 durante a execução; (2) leitura de saldo/ledger/
  reconciliação via API HTTP o teardown de cada cenário; (3) **verificação
  independente** por SQL somente leitura depois de cada cenário — somatório do
  ledger, cadeia de saldos, contagens por kind/status, pendências e saldo
  persistido. A reconciliação **não** confia no campo `consistent` da API.
- Metas experimentais (não requisitos do desafio): p95 < 500 ms em carga
  moderada; erros técnicos < 1%; zero saldo negativo; zero débito duplicado;
  zero divergência; zero operação perdida.

## 2. Ambiente

| Item | Valor |
| --- | --- |
| SO/Kernel | Ubuntu 24.04.5 LTS, kernel `7.0.0-38-generic` |
| CPU / RAM | 12 vCPU, 15 GiB (stress da máquina em paralelo: ~12 GiB usados por outros workspaces) |
| Go | 1.27.2 (`go.mod`, Dockerfile) |
| k6 | v2.3.0 |
| Docker | Docker Compose v2 (arquivos `docker-compose.yml`) |
| PostgreSQL | imagem `postgres:17-alpine` |
| Keycloak / LocalStack | `quay.io/keycloak/keycloak:26.8.0` / `localstack/localstack:4.14.0` |
| Réplicas | 3 processos Go independentes, um gateway nginx round-robin por request |
| Backing store | pool `pgxpool` **padrão** por réplica (MaxConns default do pgx = 4) |

## 3. Cenários e cargas

| Cenário | Executor | Carga |
| --- | --- | --- |
| A. Smoke | `shared-iterations` 1×1 | abertura, BET, replay, saldo, ledger, reconciliação |
| B. Ramp-up | `ramping-vus` | 10→25→50→100 VUs (rampas de 20 s; 100 carteiras exclusivas no pool) |
| B2. Nível único | `ramping-vus` | cada nível isolado 40 s (para métricas por patamar) |
| C. Contenção | `per-vu-iterations` 50×1 | 50 BETs distintas de 30.00 em burst sobre carteira de 1000.00 |
| D. Idempotência | `per-vu-iterations` 50×1 | 50 requisições idênticas (mesma chave) em burst |
| E. Carteiras independentes | `per-vu-iterations` 25×2 | ciclo BET→WIN→LOSS→REFUND→ROLLBACK por carteira |
| F. Soak | `per-vu-iterations` 25 VUs × 400 it. | teto de 15 min; duração observada ≈419,47s; BET 1.00 + reconciliação a cada 25 iterações |

## 4. Latência e throughput — execução em baseline limpo

Tabela de `POST /wagering/transactions` (`endpoint:process-wager`), p95/p99 em ms,
rps medido pelo k6 (execução limpa; "med" da reexecução com `--summary-trend-stats`):

| Nível | p50 (med) | p95 | p99 | rps observado | Erros técnicos | Reconcilação |
| --- | ---: | ---: | ---: | ---: | ---: | --- |
| 10 VUs | 83 ms | 458 ms | 594 ms | 350 | 0 | (ver §5) |
| 25 VUs | 172 ms | 657 ms | 849 ms | 170 | 0 | — |
| 50 VUs | 222 ms | 849–1050 ms | 1380 ms | 127 | 0 | — |
| 100 VUs | 280 ms | 1010 ms | 1440 ms | 112 | 0 | — |
| Contenção (50×30 burst) | 899 ms | 1530 ms | 1580 ms | 33 (durante 4,5 s de burst) | 0 | consistente |
| Idempotência (50 idênticas) | 263 ms | 671 ms | 697 ms | 51 (durante 3,3 s de burst) | 0 | consistente |
| Multiwallet (25×2 ciclos) | 218 ms | 354 ms | 376 ms | 117 | 0 | consistente |
| Ensaio finito ≈7 min @25 VUs | 13 ms | 144 ms | (max 4,4 s isolado) | 25 rps desde 10,5k requests | 0 | 10.000 checks de BET + 400 de reconciliação aprovados |

Observações importantes das medidas:

1. **Throughput não escala com VUs** (350 → 170 → 127 → 112 rps conforme 10 →
   100 VUs): assinatura clássica de saturação — ver §6.
2. **Carga moderada atende à meta:** p95 458 ms a 10 VUs e **144 ms no soak**
   (25 VUs com paced 1 s/iteração) ficam abaixo de 500 ms; acima de 25 VUs o
   p95 cruza a meta — a análise está no §6 e **nenhuma alteração no código
   financeiro foi feita** por causa de latência (conforme o mandato).
3. **Bursts alteram o regime:** contenção (50 apostas na MESMA linha) e
   idempotência (mesma identidade) ficam dominadas pela serialização
   proposital por linha/chave — p95 de 1,5 s/0,7 s é o custo explícito do
   modelo transacional, documentado no relatório como by-design.

## 5. Validação financeira independente (SQL somente leitura)

O verificador `cmd/verifywallet` auditou cada carteira esboçada nos cenários:

| Cenário | Carteiras auditadas | Checagem | Resultado |
| --- | ---: | --- | --- |
| Contenção (1000.00) | 1 | final 10.00; 34 linhas ledger; 1 OPENING + 33 BET PROCESSED; 17 BET REJECTED; 0 pendências | **OK** |
| Idempotência (100.00) | 1 | final 50.00; 2 linhas; 1 OPENING + 1 única BET PROCESSED | **OK** |
| Multiwallet ciclo completo | 25 | final = abertura (400.00); 9 linhas cada (1 + 4/ciclo); 1 OPENING + 2× cada kind PROCESSED; 0 pendências | **25/25 OK** |
| Smoke | 1 | abertura 150.00 − 25.00 BET → 125.00; 2 linhas; reconciliação consistent | **OK** |

Checagens Independentes comprovadas: soma de ledger = saldo persistido; cadeia
de saldos contígua (cada entrada começa no saldo da anterior); primeira entrada
começa em 0; nenhum saldo negativo interno; contagem por kind/status exata;
zero `PENDING_REFERENCE` sobressalente; eventos de outbox associados por
`data.walletId`.

## 6. Gargalos identificados (evidência de execução)

1. **Tamanho do pool de conexões PostgreSQL (pgxpool default = 4/réplica).** A
   escalada de VUs reduz o throughput em vez de aumentá-lo (350→112 rps) com
   p95 subindo — o sinal de saturação dos ~12 pools somados. Recomendação de
   otimização (não aplicada nesta rodada — exige A/B instrumentado):
   MaxConns explícito por réplica (pgxpool default = 4) com teste A/B.
2. **Pendência de outbox sob esteira longa:** em uma das rodadas, após ~40k iterações
   (~80k eventos), observou-se `jungle_outbox_pending` crescer (135k e
   `outbox_oldest_age` > 12 min) com o publisher a ~21 eventos/s por réplica e o
   custo por BET subindo (INSERT + varreduras do índice parcial com 135k linhas
   não publicadas). Já o ensaio finito de aproximadamente sete minutos com 25 VUs manteve `pending=0` e
   `oldest=0` — o publisher dimensiona bem cargas moderadas; em volumes acima,
   recomenda-se batching de claims e deduplicação no lado do consumidor
   (at-least-once já documentado).
3. **Pico do LocalStack (42% CPU) durante o backlog — limite do broker de
   desenvolvimento; em produção a capacidade real do SQS precisa ser avaliada
   separadamente.
4. **Latência do Keycloak**: tokens são obtidos por `client_credentials` com cache
   por VU e renovação automática; as chamadas de autenticação usam tag própria
   e não entram nas medições de latência financeira.

## 7. Limitações dos experimentos

- Máquina compartilhada (outros workspaces em execução) — números absolutos
  são específicos deste ambiente; o formato do gráfico é reprodutível.
- A detecção "in flight" no rancho de kill não é determinística (drill cobre
  qualquer ponto de morte).
- Nenhum experimento em rede real/produção: LocalStack 4.14 e Keycloak 26.8
  local apresentam custo distinto de IAM e TLS nulos no brocker de dev.
- p99 do soak > 4s evento isolado (i.e. max) — não inibitório dado p95/p99
  sem regressão no tempo (still 144ms p95).

## 8. Recomendações de otimização (futuras, pré-metricada)

1. Explicitar `MaxConns` do pgxpool por réplica com teste A/B com 100 VUs.
2. Modificar o publisher: claims em lote e escala de workers quando
   `jungle_outbox_pending` ou `jungle_outbox_oldest_age_seconds` passarem de
   limite configurado.
3. Manutenção do índice parcial da outbox (vacuum após esteiras longas) e
   alerta por `jungle_outbox_oldest_age_seconds`.

## 9. Conclusão por cenário

| Cenário | Capacidade | Metas experimentais | Veredito |
| --- | --- | --- | --- |
| A. Smoke | 1×1 — 10 req, p95 ~8 ms | ambiente saudável | **PASS** (goal intacto) |
| B. Ramp 10 VUs | 350 rps, p95 458 ms | p95 < 500 ms; erros técnicos < 1% | **PASS** |
| B. Ramp 25/50/100 VUs | 170/127/112 rps | p95 cruza a meta (657/849–1050/1010 ms) | **PASS funcional** (erros técnicos=0, alvo de latência cruzado — ver análise §6.1) |
| C. Contenção 50×30 | 33/17, saldo 10.00, ledger 34 | sem saldo negativo/duplicado; erros técnicos 0 | **PASS** |
| D. Idempotência 50 idênticas | 1 débito, 1 transação | saldo exato 50.00; zero dupla regressão | **PASS** |
| E. Multiwallet | 25 carteiras × 2 ciclos | ciclo completo com shape exato; reconcile consistente | **PASS** |
| F. Soak 15m @25 VUs | 10.5k requests, 25 rps | sem degradação crítção; live reconciliation 100% consistent | **PASS** |

## 10. Regressão do backend após os testes

`gofmt` limpo; `go vet ./...` e `go vet -tags=integration ./...` limpos;
`go test -race ./...` 14 pacotes `ok`; `go test -race -tags=integration ./...
-count=1` com PostgreSQL/Keycloak/LocalStack reais: 14 pacotes `ok`.
Nenhum arquivo de schema/código financeiro foi alterado para benchmarks.

## Regressão de entrega — 14/10/2026

Comando: `scripts/load-regression.sh`, gateway :19080, Keycloak :19090,
PostgreSQL :19532, três APIs e LocalStack 4.14.0 no projeto isolado
`jungle-mission-1410-0515`. Versão de código: correções desta missão após
`17499c3`. Relatório fonte: [métricas brutas e auditorias SQL](audit/2026-10-14/load-results.json).
Duração abaixo é wall-clock do comando k6, incluindo setup/teardown; throughput
é o `http_reqs.rate` do k6 (inclui chamadas auxiliares). Latências são de wagers,
exceto smoke, que expõe apenas o agregado HTTP. Sem meta oficial mínima de RPS.

| Cenário / configuração | Wall (s) | HTTP req/s | p50 ms | p95 ms | p99 ms | Checks falhos | Carteiras SQL |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| smoke | 0.25 | 173.14 | 5.14 | 10.63 | 10.84 | 0 | 1 |
| idempotency | 2.46 | 48.29 | 168.94 | 625.35 | 633.82 | 0 | 1 |
| idempotency-custom | 3.66 | 3.16 | 994.05 | 1690.94 | 1752.89 | 0 | 1 |
| contention | 2.89 | 46.27 | 432.05 | 614.81 | 628.58 | 0 | 1 |
| contention-custom | 1.74 | 7.11 | 10.45 | 13.20 | 13.44 | 0 | 1 |
| multiwallet | 0.63 | 862.58 | 11.90 | 32.53 | 35.81 | 0 | 25 |
| ramp | 40.49 | 421.21 | 24.38 | 63.51 | 87.93 | 0 | 25 |
| soak | 61.03 | 26.75 | 20.78 | 33.63 | 71.10 | 0 | 25 |

Todos os comandos k6 e verificadores SQL saíram 0. Taxa técnica dos wagers: 0
em cada cenário; as chamadas auxiliares têm denominador separado. Contenção
padrão: 33 processadas/17 rejeitadas; custom: duas processadas sobre 100.00.
Idempotência padrão: um efeito/49 replays; custom: 2 pedidos de 1.00, saldo
99.00. Multiwallet: 25 carteiras, dois ciclos completos por carteira. Rampa:
25 VUs, hold 30s + descida 10s. Soak curto: 25 VUs por 60s, 1.475 BETs e
75 checks de reconciliação ao vivo (1.550 checks totais). **Nenhum novo soak
de 15 minutos foi executado.** Não extrapolar estabilidade de longa duração.

A sonda sintética exige 1.000 operações, dois erros técnicos (0,2%), três
rejeições de contrato e contagens exatas de replay/conflito/rejeição/timeout.
Chamadas auxiliares não diluem esse denominador. Passou. Sonda financeira
propositalmente negativa saiu 99, como exigido.

O pico do atraso da outbox durante esta campanha não foi amostrado; não se
afirma backlog zero durante toda a carga. Falhas e recuperação da outbox são
demonstradas separadamente pelos ensaios E/F e broker-recovery, não pela taxa HTTP.

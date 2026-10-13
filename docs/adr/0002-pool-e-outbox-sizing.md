# ADR 0002 — Dimensionamento de pool pgx e publishers da outbox

**Status:** Aceito (13/10/2026) · **Contexto:** desafio Jungle Gaming; evidência A/B real no Compose de entrega
(com 3 réplicas, PostgreSQL 17, LocalStack) e no `docs/PERFORMANCE_REPORT.md`/QA.

## Decisão

1. `PG_POOL_MAX_CONNS=24` por réplica (env `PG_POOL_MAX_CONNS` configurável).
2. `OUTBOX_WORKERS=3` publishers por réplica (env `OUTBOX_WORKERS`), default no código continua 1.

## Evidência

**Pool (pernas limpas, nível 100 VUs, execução isolada):**

| Config | rps | p95 | p99 | espera média de aquisição (pgxpool) |
| --- | ---: | ---: | ---: | ---: |
| default (max(4, ncpu)=12) | 344–355 | 393–404 ms | 586–614 ms | ~44 s |
| 24 conns | 341 | 389 ms | 542–561 ms | ~9 s |
| 36/48 conns | degradado (saturação do Postgres; 3×36 > `max_connections`) | p99 até 3,5 s | — | — |

**Outbox (backlog isolado ~21–27k eventos, produção parada):**

| Workers/réplica | Dreno medido |
| --- | ---: |
| 1 (3 publishers total) | 147,7 ev/s |
| 3 (9 publishers total) | 408,7 e 421,5 ev/s (2 rodadas) |

## Consequências

- Throughput financeiro não depende do pool nos níveis testados; o ganho de 24 conns é
  p99 (~10%) e saibação de esperas (4–5× menor) — sem custo relevante (72 conns totais < 100).
- Pools maiores que 24 foram rejeitados: o Postgres entrou em saturação (p99 3,5 s) —
  o limite real é o servidor, não o pool.
- Publisher com 3 workers/réplica drena 4× em 12s (the "média-pausa" produtiva);
  as garantias at-least-once: claim exclusivo `FOR UPDATE SKIP LOCKED`, lease/token
  por evento, ack condicional; sem transação SQL aberta durante o I/O SQS; crash
  recuperado por failpoint (mesmo `eventId` republicado).
- Reversível: qualquer valor volta ao env por override, sem código novo.

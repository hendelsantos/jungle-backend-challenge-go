# Engineering Excellence Report — Jungle Backend Go

> Data: 13–14/10/2026 · Commits: `83e974b` (otimizações), `097db8b` (documentação)
> Todas as tabelas neste relatório vêm de execuções reais registradas nesta sessão;
> comando e log estão referenciados por cenário ou no `tests/load/results/` (não versionado)
> e nas seções de evidência abaixo e em `docs/QA_RELATORIO_FINAL.md`.

## 1. Resumo do sistema

Backend Go para processamento distribuído de apostas (BET/WIN/LOSS/REFUND/ROLLBACK)
por HTTP (Keycloak) e SQS, com PostgreSQL como fonte de verdade, transações
financeiras atômicas (saldo+ledger+transação+outbox+inbox no mesmo commit),
worker de referências pendentes com TTL e publisher de outbox at-least-once.
Deploy local em 3 réplicas Go com gateway nginx (`docker compose up --build`).

## 2. Arquitetura

Diagramas completos em `docs/ARCHITECTURE_DIAGRAMS.md` (containers, BET HTTP,
SQS/inbox, outbox, três réplicas, recuperação). Resumo: hexagonal enxuta;
domínio sem infraestrutura; casos de uso compartilhados por HTTP e SQS;
Fx com lifecycle de workers (cancelamento observável, shutdown ordenado).

## 3. Requisitos obrigatórios (estado final)

Todas as eliminatórias do §14 do enunciado permanecem APROVADAS com evidência
(matriz em `docs/CONFORMIDADE_DESAFIO.md` + QA CI report). Regressão completa executada após as otimizações deste ciclo (veja §7).

## 4. Testes funcionais e de falhas

- `go test -race ./...`: 14/14 pacotes `ok` (pós-change).
- `go test -race -tags=integration ./... -count=1` com PG/Keycloak/LocalStack
  reais: 14/14 `ok`.
- `scripts/smoke.sh`: 51 idênticas → 1 débito + 49 replays; par 200/422; saldo
  e reconciliação exatos.
- `scripts/fault.sh` (Testes E/F/H literais com kill -9 das três réplicas):
  aprovados — inclusive com `OUTBOX_WORKERS=3` ativo.

## 5. Segurança

Gitleaks full-history: `no leaks found` (25–28 commits). govulncheck no CI.
Análise de ameaças e fronteira de confiança SQS em `docs/SECURITY_REVIEW.md`;
runbook de falhas em `docs/RUNBOOK.md`.

## 6. Performance — A/B de pool e outbox com resultado

### 6.1 pgxpool (nível 100 VUs, pernas limpas — cada execução com banco novo)

| Config (conns/réplica) | rps | p50 | p95 | p99 | espera média de aquisição (pgxpool) | tech |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| default (max(4,ncpu)=12) leg A | 354.9 | 56 ms | 393 ms | 586 ms | 44.4 s | 0 |
| 12 leg B | 344.2 | 63 ms | 404 ms | 614 ms | 45.1 s | 0 |
| 24 leg A | 341.3 | 65 ms | 389 ms | 542 ms | 9.1 s | 0 |
| 24 leg B (confirmatoria: c24 primeira) | 355.9 | 57 ms | 380 ms | 561 ms | 5.8 s | 0 |
| 36 | 141.7 | 123 ms | 702 ms | 3520 ms | 1.7 s* | 1 |
| 48 | 113.1 | 245 ms | 1040 ms | 1430 ms | 57.6 ms | 1 |

*36/48: saturação do Postgres (3×36 > 100 `max_connections`, contagens
confladas), rejeitados como default.

**Decisão (justificada):** adotar `PG_POOL_MAX_CONNS=24`. Throughput é par;
p99 melhora ~10% e a espera média de aquisição cai ~4–5×, sem custo
(72 < 100 conexões totais). Riscos: memória por conn (desprezível nesses
valores), mais protagonismo de conns no Postgres (monitorado por
`jungle_pgpool_*`).

### 6.2 Outbox publisher (drain isolado, backlog ~21–27k, produção parada)

| Workers/réplica | Dreno medido | Tempo |
| --- | ---: | ---: |
| 1 (3 publishers no total) | 147.7 ev/s | 184 s |
| 3 (9 publishers no total) leg A | 408.7 ev/s | 51 s |
| 3 leg B (reprodutibilidade) | 421.5 ev/s | 51 s |

**Decisão:** `OUTBOX_WORKERS=3` no Compose. Preserva: claim exclusivo
(Skip LOCKED + lease/token por evento), nenhum lock SQL durante I/O SQS,
ack condicional e republicação com o mesmo `eventId` — testes de integração
de dois publishers e falha publish/ack continuam aprovados; kills literais
re-executados após o change.

### 6.3 Baseline da carga (nível limpo: tabela por VU; cap física)

p50/p95/p99 por nível (default pool, execução limpa): 10 VUs 56/393/586 ms
(350 rps); 25 VUs: 172/657/869 ms (170 rps); 50 VUs: 222/849–1050/1380 ms
(127 rps); 100 VUs: 280/1010/1440 ms (112 rps). Soak 15 m @25 VUs: p50 13 ms,
p95 144 ms, **zero erros técnicos**, reconciliação ao vivo 10400/10400
consistentes e outbox sempre drenada (números completos no
`docs/PERFORMANCE_REPORT.md`).

## 7. Integridade financeira pós-mudança

- Verificador independente (SQL somente leitura) por carteira nos cenários:
  contenção OK (33/17, 10.00, 34 linhas); idempotência OK (1 débito, 50.00);
  multiwallet 25/25 OK (shape exato por ciclo).
- Regressão financeira pós-pool/publisher: **nenhuma regra alterada**; as
  mudanças envolvem contagem de conexões e concorrência de publishers — ambas
  sob as garantias transacionais e de lease já registradas.

## 8. Gargalos e próximos passos (com produção real)

- **Produção extrema:** a 100 VUs a produção de eventos (~700 ev/s de pico)
  supera a capacidade de publicação medida (~420 ev/s com 3 workers) — o backlog
  cresce em pico contínuo de alta carga, porém sem violar a entrega
  (at-least-once, drenado após o pico). Em produção real: mais publishers,
  exigência de SLA de fila e/ou rate limiting do produtor.
- **PostgreSQL físico** é o limite expansível além do pool (evidência das pernas 36/48). Tuning avançado fica fora do escopo (shared_buffers, WAL, índices, hardware dedicado).
- **Batching de publicação** (claim de múltiplos eventos) — rejeitado nesta
  rodada: o ganho já obtido cobre cargas moderadas e o risco não compensa sobre
  um mecanismo com recuperação já comprovada.

## 9. Demonstração (7 minutos)

1. `docker compose up --build -d` (1 min) — três réplicas e o job de migrations.
2. `scripts/smoke.sh` (1,5 min) — BET + replay + conflito + saldo + reconciliação.
3. `docker logs api-1|2|3 | grep -c 'wager request completed'` — mostra todos
   os três processos funcionando.
4. `scripts/fault.sh` (3 min) — kills E/F/H: reentrega sem duplicação, drenagem
   do publisher e `PENDING_REFERENCE` concluída com a BET chegando pelo SQS.
5. `curl /metrics` — mostre as métricas do pool/outbox: `jungle_pgpool_*`,
   `jungle_outbox_pending` zero, contadores financeiros.

O texto preparado para a entrevista está na seção 8 de QA final:
`docs/QA_RELATORIO_FINAL.md` (10 perguntas com respostas defensáveis).

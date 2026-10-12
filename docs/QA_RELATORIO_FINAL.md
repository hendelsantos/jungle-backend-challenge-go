# Relatório de QA final — Jungle Backend Go

> Data: 13/10/2026 · Commit auditado: `114118e` · Branch `main`, sem divergência com `origin/main`
> Metodologia: auditoria contra o enunciado oficial (snapshot em `docs/reference/README_DESAFIO.md`),
> prova por execução real (unitários, integração com PostgreSQL/Keycloak/LocalStack reais,
> Compose de entrega e reprodução limpa em clone separado) e varredura de segurança no
> histórico completo com Gitleaks.

## 1. Matriz de auditoria

Classificação (APROVADO/PARCIAL/FALHOU/NÃO TESTADO) e gravidade do maior problema
encontrado (P0 eliminatório, P1 crítico, P2 importante, P3 opcional).

| Requisito do enunciado | Classificação | Evidência |
| --- | --- | --- |
| Autenticação OIDC real nos endpoints de negócio | APROVADO | go-oidc + Keycloak 26.8 (`client_credentials`); 401 para token ausente/malformado/assinatura alterada/audience errada/expirado (`api_integration_test.go`) |
| Autorização e isolamento por provedor | APROVADO | role `provider` + `provider_id` do token; isolamento em rota, consulta, replay e conflito sem efeitos financeiros |
| Operações internas restritas | APROVADO | `wallet:manage`/`wallet:read`; cliente de provedor em rota interna → 403 |
| Dinheiro exato sem float no fluxo monetário | APROVADO (P3: `float64` apenas em gauges de métrica, não monetário — identificado por grep; parse textual em dinheiro) | `internal/domain/money` com 100% de cobertura e fuzzing exato contra oráculos `math/big` |
| Atômico: carteira+transação+ledger+outbox+inbox | APROVADO | mesmo commit; erro SQL intermediário confirma rollback integral (`process_wager_integration_test.go`) |
| Ledger append-only | APROVADO | triggers `BEFORE UPDATE/DELETE/TRUNCATE` verificadas por integração real |
| Sem dupla compensação | APROVADO | índice único parcial + ADR 0001 + testes concorrentes de REFUND/ROLLBACK |
| Idempotência persistente + replay histórico | APROVADO | `UNIQUE(provider,idempotency_key)` e `UNIQUE(provider,external_id)`; replay devolve saldo do momento do resultado original |
| Hash canônico HTTP = SQS | APROVADO | SHA-256 sobre JSON tipado ordenado; mesma identidade nos dois canais ("command first then sqs replays", `integration_test.go`) |
| Commit de resultado desconhecido | APROVADO | `TestSQSUnknownCommitResultReplaysIntegration`; replay idempotente recupera o estado duvidoso |
| Sem publicação antes do commit | APROVADO | outbox no mesmo commit; failpoint derruba antes do envio e o banco permanece íntegro |
| Sem saldo negativo por concorrência | APROVADO | 80+80 (goroutines, HTTP e três réplicas), `CHECK` no schema |
| Três instâncias independentes | APROVADO (P2: detecção "in flight" no drill não é determinística — comentada no script e coberta por failpoints) | Compose de entrega + gateway; distribuição 21/18/13 nos logs das réplicas |
| 50 idênticas → um único débito | APROVADO | smoke: 51 respostas, `processed=1 replays=49`, saldo exato |
| Duas de 80.00 concorrentes → 1/1 | APROVADO | suíte HTTP de integração + smoke |
| HTTP e SQS no mesmo instante | APROVADO | identidade compartilhada nos dois canais nas duas ordens, com concorrência (`integration_test.go`) |
| Carteiras independentes sem lock global | APROVADO | progresso de carteiras distintas com locks por linha |
| REFUND/ROLLBACK antes da referência | APROVADO | `PENDING_REFERENCE` (202) + worker + Teste H de `fault.sh` (cruzamento HTTP→SQS com restart) |
| Interrupção após commit antes do delete | APROVADO | failpoint de integração + kill -9 literal (`fault.sh`, Teste E) |
| Expiração de lease e dois publishers | APROVADO | `outbox_delivery_integration_test.go`; quatro publishers entregando dois eventos exatamente uma vez |
| Republicação com `eventId` estável | APROVADO | deduplicação FIFO + payload imutável; recebimento com mesmo dedup/groupId verificado |
| Retries, DLQ e redrive | APROVADO | redrive após três recebimentos; cópia à DLQ preserva o envelope; assinatura forjada → DLQ |
| `PENDING_REFERENCE` após reinício + TTL | APROVADO | `fault.sh` Teste H + rejeição por expiração com evento auditável |
| Graceful shutdown sem perda de trabalho | APROVADO | listeners liberados, rollback de transação ativa e visibilidade liberada (`lifecycle_test.go`, `TestSQSockets...`) |
| Migrations up/down reproduzíveis | APROVADO | `cmd/migrate` (inclui adoção de schema pré-existente via `force`) + Makefile + integração de ciclo completo |
| Compose desde checkout limpo | APROVADO | reproduzido em clone separado em `/tmp` com portas por override, sem configuração não documentada |
| Observabilidade — logs exigidos | APROVADO | correlationId/providerId/walletId/transactionId/messageId sem dados sensíveis |
| Observabilidade — métricas exigidas | APROVADO | registro Prometheus dedicado (operações por kind/status, replays, conflitos, histograma de latência, retries de transação, redeliveries, DLQ, outbox pendência/atraso, divergências), sem alta cardinalidade |
| Documentação | APROVADO | README operacional, ADRs, matriz de conformidade, arquitetura por incremento |
| Segredos | APROVADO | Gitleaks no histórico completo: `no leaks found` + varredura manual |
| Diferenciais opcionais (partidas dobradas, tracing, dashboards, carga) | NÃO TESTADO — explicitamente não entregue | registrado como limitação em `ARCHITECTURE.md` e na matriz de conformidade |

## 2. Achados desta rodada de QA

1. **P2 — produtor fora do ambiente clonado:** no clone de QA, `scripts/fault.sh` usa a
   `SQS_URL` default do ambiente de entrega principal; ao reproduzir em ambiente com portas
   alternativa, passe `SQS_URL` (documentado no script).
2. **P2 — janela "in flight" não determinística:** o intervalo receive/commit/delete do
   consumidor pode ser menor que a observação de `ApproximateNumberOfMessagesNotVisible`;
   o drill permanece válido (asserções absorvem qualquer ponto de morte) e a janela exata
   é coberta pelos failpoints de integração.
3. **Nenhum P0 ou P1 aberto.** Os defeitos encontrados em incrementos anteriores desta
   sessão (cruzamento de canais no Teste H, loop de espera que quebrava no saldo de
   abertura, métricas nulas no bootstrap causando 504 dos workers) foram corrigidos e
   têm regressão coberta por scripts/integração.

## 3. Comandos executados nesta rodada (comando → resultado)

- `gofmt -l .` → vazio
- `go vet ./...` e `go vet -tags=integration ./...` → limpos
- `go test -race ./...` → 14 pacotes `ok`, zero falhas; testes de integração pulam apenas
  sem as variáveis `TEST_*` (comportamento documentado no README — nenhum skip oculto)
- `go test -race -tags=integration ./... -count=1` com PostgreSQL/Keycloak/LocalStack
  reais → todos os pacotes `ok`
- `make build` → `bin/server` e `bin/migrate`
- Clone limpo em `/tmp`: `docker compose up --build` → todos healthy em ~1 min,
  migrations aplicadas pelo job, `scripts/smoke.sh` e `scripts/fault.sh` (E/F/H)
  aprovados no clone; ambiente clonado destruído no final
- Cobertura: `money` 100,0%, `wallet` 98,3%, `wagering` 97,1%, `command` 73,1–75,2%
  (unit+integração), `query` 67,7% — invariantes financeiras cobertas primeiro,
  não a porcentagem global
- Fuzzing (sessões prévias de 10s por alvo): parsing/aritmética de `money` vs `math/big`,
  sequências de `wallet` e BET→REFUND→ROLLBACK de `wagering` — sem falhas

## 4. Auditoria de segurança — resultado

- Gitleaks no histórico completo (24 commits): **no leaks found**.
- Varredura manual por AKIA/chave privada/`ghp_`/hex de 40+ caracteres: apenas SHAs de commit.
- URLs com usuário/senha: somente valores fictícios identificáveis (`jungle:jungle-local`,
  `u:p@h:1/db` em testes e documentação).
- A chave de API usada nesta sessão de trabalho (TypeSafe AI) nunca entrou no repositório
  (zero ocorrências no histórico; reside apenas no `~/.bashrc` da máquina local).
- Chaves de desenvolvimento (`local-provider-*-signing-key`, segredos do realm `jungle`,
  credenciais do PostgreSQL fictício) são exclusivas do ambiente local e estão marcadas
  como apenas de desenvolvimento no README, `.env.example` e provisionamento.

## 5. Estimativa de pontuação (estimativa técnica — não prometemos a nota oficial)

| Critério | Peso | Estimativa | Justificativa |
| --- | ---: | ---: | --- |
| Integridade financeira | 20 | 19 | invariantes todas comprovadas, incluindo ROLLBACK de REFUND com saldo |
| Concorrência | 20 | 18 | três réplicas reais com kills; detalhe: contadores por processo exigem agregação externa |
| Idempotência | 15 | 14 | hash/índices/replay/conflito; persistente entre canais e restarts |
| Mensageria e recuperação | 15 | 14 | inbox/outbox/retries/DLQ/shutdown com broker real + kills literais |
| Modelagem e arquitetura | 10 | 9 | hexagonal enxuta, Fx modules, SQL explícito, ADR |
| Testes | 10 | 9 | unitários + integração real + scripts reproduzíveis |
| Observabilidade | 5 | 5 | logs e métricas exigidas completas |
| Documentação | 5 | 4 | README operacional e decisões explicadas; sem diagramas gráficos |
| **Total estimado** | **100** | **92** | faixa plausível 88–95 conforme o revisor |

## 6. Recomendação

**GO para entrega.** Nenhum requisito eliminatório pendente; todos os critérios de
"pronto para entrega" da missão estão satisfeitos com evidência reproduzível. Os itens
não entregues são os próprios diferenciais opcionais, declarados ostensivamente.

## 7. Demonstração sugerida (5–10 minutos)

1. `docker compose up --build -d`; `docker compose ps` — três réplicas healthy e o job de
   migrations `Exited(0)` (1–2 min).
2. `scripts/smoke.sh` — abertura 100.00, 51 BETs idênticas → 1 débito + 49 replays com
   `idempotentReplay:true`, par concorrente 200/422, saldo 10.00, três lançamentos e
   reconciliação `consistent:true` (2 min).
3. `docker logs jungle-delivery-api-1|2|3 | grep -c 'wager request completed'` — a
   distribuição prova três processos servindo a mesma rota (1 min).
4. `scripts/fault.sh` — narrar os três drills: kill -9 com mensagem SQS em voo (reentrega
   sem débito duplicado), kill com outbox não reconhecida (publisher drena o backlog) e
   restart com `PENDING_REFERENCE` que sobrevive e conclui com a BET chegando pelo SQS
   (3 min).
5. `curl localhost:9080/metrics | grep jungle_` — mostrar as métricas exigidas após a
   execução dos cenários (1 min).

## 8. Dez perguntas de entrevista (com respostas que você consegue defender)

1. **Por que `int64` em centavos e nunca float para dinheiro?** Float não representa
   decimais exatamente e acumula erro de arredondamento e comparação. Centavos em `int64`
   tornam a aritmética exata com overflow verificado (ex.: `Negate(MinInt64)` é erro); no
   transporte o JSON é string decimal, parseada textualmente — decodificação nunca passa
   por `float64`.

2. **Como se garante um único débito com 50 requisições idênticas?** Identidade durável:
   índices únicos `(provider_id, idempotency_key)` e `(provider_id, external_transaction_id)`.
   O vencedor do INSERT processa; os perdedores consultam o resultado persistido (replay) —
   nada fica em memória, então nenhuma réplica recalcula.

3. **Sem `SELECT ... FOR UPDATE` ainda haveria lost update?** Sim. O débito trava a linha da
   carteira dentro da transação do caso de uso; a atualização é condicional à versão e ao
   saldo disponível. Carteiras distintas travam linhas diferentes e progridem em paralelo —
   sem lock global.

4. **Por que `FOR UPDATE SKIP LOCKED` no worker?** N instâncias disputam pendências sem se
   bloquear: cada claim leva linhas próprias e o conflito sobra para outra instância. O lease
   evita trabalho duplicado e a expiração recupera processos mortos — tanto no worker de
   referências quanto no publisher da outbox.

5. **O que acontece se o processo morrer depois do commit e antes do `DeleteMessage`?**
   A mensagem reaparece (SQS é at-least-once). No novo recebimento, a inbox
   `(consumer_name, message_id)` com hash decide: replay devolve o resultado persistido
   porque `completed_at` foi gravado no mesmo commit; nenhum novo débito é possível.

6. **Exactly-once já seria possível anunciando a deduplicação FIFO?** Não. O broker deduplica
   `EventId` dentro de uma janela, e o publisher republica a mesma identidade — o consumidor
   externo deve deduplicar pelo `eventId` além da janela. A interface honesta é
   **at-least-once** com idempotência como camada de efetividade única.

7. **Como se calibra expiração vs. espera de referência?** `reference_deadline_at` (TTL
   configurável, default 24h) é materializado no primeiro claim do worker; sem vencimento,
   backoff exponencial com expoente saturado e teto de 5min. Expiração → rejeição
   `REFERENCE_NOT_FOUND` auditável com evento persistido. Os valores são política do projeto,
   não do enunciado.

8. **Quais claims do token autorizam — e o que o body não pode fazer?** `iss/aud/exp/nbf/
   typ/azp` via JWKS com rotação; role `provider` mais `provider_id` — sempre do token,
   nunca do body; rota divergente da identidade → 403 antes de qualquer consulta; operações
   internas exigem `wallet:manage`/`wallet:read`; a chave de idempotência vem do header e
   não concede acesso.

9. **Qual é a fronteira de confiança do SQS?** Mensagem não é autenticação: o produtor
   assina HMAC e o consumidor vincula assinatura↔`providerId` do corpo; contra produtores
   falsos, a validação de escopo do domínio restringe (não aceita `OPENING`). IAM de
   produção é outra camada que o LocalStack não prova — a limitação está documentada.

10. **Como o Fx gerencia os ciclos e por que isso importa?** `fx.Module`/`Provide`/`Invoke`
    com lifecycle `OnStart/OnStop`: validação fail-fast antes de servir; workers param antes
    do pool fechar; transações em andamento concluem ou rolam back com prazo; os kills
    literais comprovam que nenhum trabalho fica órfão do processo.

## 9. Ciclos de QA executados

### Ciclo 1 — 13/10/2026 (commit auditado `114118e`)
Vitórias completas de: gofmt/vet (com e sem `-tags=integration`), `go test -race ./...`
(14 pacotes), integração `-race -count=1` com PostgreSQL/Keycloak/LocalStack reais,
`make build`, reprodutibilidade limpa em `/tmp` (compose up + smoke + faults,
ambiente clonado destruído), Gitleaks (`no leaks found`), cobertura e fuzzing registrados.

### Ciclo 2 — 13/10/2026 (pós-`b1b42ef`)
- `gofmt -l .` → vazio; `go vet ./...` e `go vet -tags=integration ./...` → limpos.
- `go test -race ./...` → 14 pacotes `ok`.
- `go test -race -tags=integration ./... -count=1` → 14 pacotes `ok` (nenhuma falha,
  nenhum skip silencioso).
- Compose de entrega recriado do zero (`down -v` + `up --build`) → réplicas healthy;
  `scripts/smoke.sh` OK; `scripts/fault.sh` (E/F/H, incluindo cruzamento HTTP→SQS)
  OK; Gitleaks pós-relatório (25 commits) → `no leaks found`.
- Sondagem pós-bateria: `/metrics` mostra contadores do publisher coerentes com a
  recuperação dos drills (claimed/retry sem confirmation_failures), `outbox_pending`
  e `oldest_age` zero, readiness 200.

Estado após o ciclo 2: implementação congelada; nenhum P0/P1 aberto; recomendação GO
mantida.

# Auditoria final de entrega — 14/10/2026

**Parecer: NO-GO para conformidade integral do desafio.** Nenhum erro financeiro
foi reproduzido na regressão executada, mas A01/A02 continuam sem atender aos
requisitos de autorização do broker (§2) e preservação de eventos (§3).
O ambiente aceita operações anônimas e perde mensagens já aceitas após restart.
Documentar a limitação não a transforma em requisito atendido.

**Implementação avaliada:** `56d5d76d400791f95d807b30b5def5104b316617`, `main`.
Este relatório/documentação é consolidado no commit seguinte; consultar `git
log -1` para o commit documental final. Sem reescrita de histórico.
Fonte normativa: [enunciado preservado](reference/README_DESAFIO.md).

## Matriz final A01–A13

| Achado | Estado | Implementação, evidência e limite |
| --- | --- | --- |
| A01 | **PARCIAL** | Cadeia AWS sem fallback, token temporário, perfis/roles e HMAC testados. Provisionamento separado em `cmd/provision-sqs`; runtime usa clientes/perfis independentes para consumidor e publisher. Templates IAM corrigidos contra chamadas. Autorização real falhou no broker local, inclusive sob Deny. Separar clientes não comprova aplicação de permissões; não testada em AWS/plano pago. MiniStack 1.5.27 avaliado em POC isolada ([auditoria](AUDITORIA_MINISTACK_FINAL.md)): aplica IAM para usuários nomeados, mas aceita anônimo e chaves builtin forjadas — reprovado, migração interrompida. Homologação AWS SQS **preparada** (`compose.homolog.yaml`, roles STS, probes `tests/homolog`): pendente de autorização/execução. |
| A02 | **ABERTO** | Persistência+volume em Community 4.14.0 não preservaram fila. No Compose, 14 mensagens aceitas foram perdidas; 38 registros continuaram publicados. Dois eventos pendentes durante pausa recuperaram normalmente. Readiness 503/200 comprovada. MiniStack 1.5.27 reprovado na mesma POC: graceful preserva 3/3 mensagens íntegras, SIGKILL perde todas as aceitas (`accepted=3 recovered=0 lost=3`). Homologação AWS SQS **preparada** (durabilidade de broker + recuperação de app + drills E/F/H): pendente de autorização/execução. |
| A03 | **CORRIGIDO E TESTADO** | Build exclusiva faulttest; E/F bloqueiam no ponto crítico. SQL antes do SIGKILL; inbox replay com mesmo messageId; F com lease expirada naturalmente, attempts=2, eventId/digest iguais. H cruzando HTTP→SQS passou. |
| A04 | **CORRIGIDO E TESTADO** | Todos os checks financeiros têm gate; negativos saem 99. Idempotência 2×1.00 e contenção com apenas duas apostas usam expectativas configuradas. SQL verifica todas as carteiras do runner. |
| A05 | **CORRIGIDO E TESTADO** | Uma amostra por wager, counters agregados, taxa auxiliar separada. Sonda exige classes e contagens exatas de 200/400/401/403/409/422/500/timeout: dois erros em 1.000 operações. |
| A06 | **CORRIGIDO E TESTADO** | constant-vus por duração; ensaio atual 60s (61,03s wall-clock). Relato antigo corrigido para ≈419,47s e 400 reconciliações. Novo soak de 15min não executado. |
| A07 | **CORRIGIDO E TESTADO** | Relógio atualizado após lock, updated_at como piso e TTL reavaliado. Contenção PostgreSQL real, relógio defasado, TTL e recuperação, sem movimento duplicado. |
| A08 | **CORRIGIDO E TESTADO** | Limite de 512 bytes nos campos de wager; fronteiras UTF-8 unitárias; HTTP 400 e SQS DLQ para 513 bytes/multibyte/12.000 bytes sem efeito financeiro. Envelope messageId limitado a 256 bytes. |
| A09 | **CORRIGIDO E TESTADO** | Gauges por handler/store/pool, counters por processo. Dois handlers simultâneos e ciclos Fx reais repetidos com count2, sem registro duplicado ou dependência antiga. |
| A10 | **CORRIGIDO E TESTADO** | Parsers rejeitam inválidos/overflow/contagens negativas. REPEATABLE READ READ ONLY por carteira; teste real com WIN entre consultas preserva snapshot anterior. |
| A11 | **CORRIGIDO E TESTADO** | Referência emite conclusão pós-commit, bootstrap conectado; retries inbox/referência, duração HTTP/SQS com labels limitados, DLQ confirmada vs falha; replay não é novo efeito. Counters best-effort podem perder incremento em crash. |
| A12 | **CORRIGIDO E TESTADO** | Tutorial separado de rollback, smoke literal 80+80, comandos de gates/carga/probes versionados. Checkout limpo iniciou três APIs saudáveis; smoke/E/F/H e encerramento aprovados. Docs distinguem garantias e limites. |
| A13 | **CORRIGIDO E TESTADO** | govulncheck v1.9.0 concluiu sem vulnerabilidades reportadas. Gitleaks: 45 commits sem vazamentos detectados, com exceção exata de fixture fictícia. CI de 56d5d76 aprovado nos dois jobs, incluindo integração real. |

## Verificações executadas

Incremento posterior de provisionamento separado: [evidências da retomada](audit/2026-10-14/sqs-runtime.md).
A01 continua parcial; A02 continua aberto. A tabela abaixo preserva a campanha
anterior; não confundir seus resultados com a nova execução.

Infraestrutura real e exclusiva: `jungle-final-dev` (PostgreSQL :16432,
Keycloak :18080, LocalStack :14566), `jungle-mission-1410-0515` (gateway :19080,
IdP :19090, PG :19532, SQS :19570). Sem AWS real ou serviço pago.

| Comando/ensaio | Resultado efetivamente observado |
| --- | --- |
| make check | exit 0 |
| go vet -tags=integration ./... | exit 0 |
| go test -race ./... -count=2 | exit 0 |
| go test -race -tags=integration ./... -count=1 -json | exit 0; 627 testes/subtestes pass; 0 fail/skip |
| go test -race -tags=integration ./... -count=2 -json | exit 0; 1.254 testes/subtestes pass; 0 fail/skip |
| docker compose -f docker-compose.yml config -q | exit 0 |
| scripts/doccheck.sh e sintaxe shell | aprovados |
| scripts/smoke.sh | 50 duplicatas→1 débito/49 replays;80+80 sobre100→20.00/1 débito/1 rejeição; replay preservado |
| scripts/fault.sh | E/F/H aprovados em três processos reais; IDs/digest/lease/ledger registrados |
| scripts/broker-probe.py | envio anônimo aceito sob Deny; fila perdida com PERSISTENCE=1 e volume |
| scripts/broker-recovery.py | exit 2 por perda pós-ACK; recuperação pré-ACK e readiness PG/SQS comprovadas |
| scripts/load-regression.sh | oito cenários k6 + auditoria SQL de todas as carteiras aprovados |
| probes/classifier-probe.js | exit 0, oráculo exato de classes/denominadores |
| probes/gate-negative-probe.js | exit 99 esperado por check financeiro propositalmente errado |
| govulncheck@v1.9.0 | exit 0, No vulnerabilities found |

Contagem de testes inclui pais/subtestes e repetições, não cenários independentes.
Pacotes sem arquivos de teste não são skips. Os gates versionados recusam skips
na integração e exigem as três variáveis TEST_*; comando sem dependências não
é evidência de integração real.

[Evidências e falhas intermediárias](audit/2026-10-14/README.md),
[resultados de carga/SQL](audit/2026-10-14/load-results.json) e
[análise de performance](PERFORMANCE_REPORT.md). Outbox pendente em consulta
após carga: 0; pico durante a campanha não foi medido. Não inferir ausência de
backlog durante toda a carga. Logs completos temporários ficaram em
`/tmp/jungle-final-20261014`; scripts e evidências consolidadas são versionados.

## Checkout limpo, encerramento e CI

Clone local limpo de `56d5d76`, projeto exclusivo `jungle-proof-1410-0535`,
portas 19180/19190/19632/19670, imagem normal `jungle-backend-go:proof-1410`.
`up --build -d` aplicou migrations até versão 4, `dirty=false`; três APIs
saudáveis com PIDs distintos. Smoke passou antes e depois de E/F/H. No segundo
smoke, as 52 requisições de apostas da carteira de duplicidade/conflito foram
distribuídas em 17/15/20 entre os processos. A carteira do cenário literal
80+80 terminou em 20.00, com um débito e uma rejeição.

Depois da restauração da imagem normal por `fault.sh`, readiness retornou 200.
`docker compose stop -t 30 api` acionou hooks Fx de HTTP, OIDC, publisher,
consumidor, referências e banco; os três processos encerraram com exit 0,
sem OOM ou SIGKILL. Os SIGKILL anteriores eram exclusivamente as injeções E/F.

[GitHub Actions de 56d5d76](https://github.com/hendelsantos/jungle-backend-challenge-go/actions/runs/38536103961):
`completed/success`, job de qualidade/segredos e job de integração aprovados.
A falha anterior de Gitleaks em `17499c3` permanece registrada no histórico;
não foi apagada nem classificada como sucesso.

Gitleaks no histórico: 45 commits, nenhum vazamento detectado. A varredura de
todo o diretório também examinou resultados locais ignorados pelo Git: três
alertas em `tests/load/results/idempotency*.json` eram `setup_data.key` no
formato `idem-bet-<timestamp>`, gerado pelo cenário k6; não são credenciais.
Não se adicionou exceção genérica. Nenhum `.env`, binário, resultado temporário
ou clone oficial consta dos caminhos versionados revisados.

## Requisitos oficiais demonstrados

1. PostgreSQL real: dinheiro exato, cinco operações, compensações, constraints,
   ledger imutável, locks por carteira e reconciliação em snapshot.
2. Três APIs independentes: duplicidade e disputa financeira pelo gateway;
   carteiras distintas progridem em paralelo (integração e carga multiwallet).
3. HTTP/SQS: mesma identidade nas duas ordens e simultaneamente; inbox e
   efeitos financeiros no mesmo commit, falha SQL desfaz tudo.
4. Outbox: registro no commit financeiro, publicação pós-commit, publishers
   concorrentes, lease e retry com mesmos eventId/bytes. Interrupções E/F reais.
5. Referências: chegada posterior, expiração, restart e conclusão pelo outro
   canal; não há aceite intermediário PENDING sem retomada durável.
6. Keycloak real: tokens inválidos/expirados/audience errada, roles e isolamento
   entre provedores sem efeitos financeiros; mensagens SQS inválidas em DLQ.
7. Redrive após tentativas esgotadas e encerramento de workers com rollback/
   liberação de visibilidade; lifecycle Fx encerra recursos na ordem correta.

Acesso ao broker e retenção após seu restart são exceções obrigatórias
explicitamente não atendidas. HMAC não impede alguém de consumir/apagar
mensagens do broker. Semântica at-least-once não significa exactly-once.

## Demonstração à Jungle Gaming

Executar o bloco “Demonstração reproduzível” do [README](../README.md):
Compose num projeto exclusivo → smoke → FAULT_DISPOSABLE=1 fault → readiness.
Mostrar três processos, uma aposta/49 replays, cenário 80+80, E/F/H e IDs nos
logs/SQL. Apresentar a matriz e demonstrar a limitação com broker-recovery
somente no projeto descartável; exit 2 significa reprovação de durabilidade.

Para entrevista, usar [GUIA_ENTREVISTA_JUNGLE.md](GUIA_ENTREVISTA_JUNGLE.md).
Não apresentar o GO ou as notas estimadas dos relatórios históricos como
parecer atual. Nenhuma nota de avaliação foi inventada nesta revisão.

## Pendências e testes não executados

- Autorização operacional com identidades separadas e broker que aplique IAM.
  Templates não são implantação; runtime agora apenas resolve filas e usa clientes separados.
  O substituto local candidato (MiniStack 1.5.27) foi avaliado em POC isolada e
  **reprovou os gates A01/A02** ([auditoria MiniStack](AUDITORIA_MINISTACK_FINAL.md));
  migração interrompida. Homologação em AWS SQS gerenciado **preparada**
  (ver [HOMOLOGACAO_AWS.md](HOMOLOGACAO_AWS.md)); execução pendente de autorização.
- Durabilidade do broker local após restart. Não demonstrada uma configuração
  gratuita compatível; nenhum serviço pago/AWS real foi autorizado ou usado.
  MiniStack confirmou durabilidade apenas em shutdown graceful; SIGKILL perde
  as mensagens aceitas desde o último snapshot.
- Campanha de endurance de 15min nesta revisão, testes em AWS gerenciada,
  rotação de segredos em produção e backup/restauração de produção não executados.
- Usuários SQL separados para runtime/migrations não implementados no Compose;
  o usuário local administrativo pode contornar triggers com DDL. Testes provam
  imutabilidade para DML, não blindagem contra administração do banco.
- Métricas por processo não constituem contabilidade durável; agregação externa,
  tracing, dashboard e partidas dobradas continuam fora do escopo entregue.

## Histórico do diagnóstico inicial

Fonte normativa: [enunciado](reference/README_DESAFIO.md). Base inicial:
`6c04217498eb9809077ee7213724f6342d18e1f4`, branch `main`, árvore limpa,
`git fetch origin` concluído e `HEAD...origin/main = 0/0`.
Esta revisão não herda aprovações dos relatórios históricos.

## Matriz inicial do HEAD (inspeção, antes da nova regressão)

“CORRIGIDO SEM EVIDÊNCIA” significa código corretivo encontrado, ainda sem
execução naquele momento. A matriz final está no início deste documento.

| Achado | Estado inicial | Constatação no HEAD |
| --- | --- | --- |
| A01 | PARCIAL | Cadeia AWS sem fallback e HMAC presentes; IAM é exemplo não aplicado; runtime da base auditada também provisiona filas com o mesmo cliente. |
| A02 | ABERTO | Compose sem persistência; outbox publicada não recupera perda posterior do broker. |
| A03 | PARCIAL | Pausas de 12s não comprovam ack pendente; F pode observar só reentrega do primeiro envio; falta verificar SQL e logs por identidade. |
| A04 | PARCIAL | Thresholds e BigInt presentes; contention calcula vencedores sem limitar ao número de apostas. |
| A05 | PARCIAL | Classificador agrega Rate/Counter; sonda não cobre 401/403/timeout nem exige contagens exatas. |
| A06 | CORRIGIDO SEM EVIDÊNCIA | Executor constant-vus por duração; falta nova execução cronometrada. |
| A07 | CORRIGIDO SEM EVIDÊNCIA | Relógio atualizado após lock e limitado pelo updated_at; falta regressão de contenção/TTL. |
| A08 | CORRIGIDO SEM EVIDÊNCIA | Limites em bytes presentes; revalidar HTTP/SQS e UTF-8. |
| A09 | PARCIAL | Pool retargetado; collector de outbox continua preso ao primeiro store. |
| A10 | PARCIAL | Parser estrito presente; consultas do verificador ainda sem snapshot único. |
| A11 | PARCIAL | SQS conta resultados; callback de referências dentro da transação e desligado no bootstrap; duração ausente no SQS. |
| A12 | PARCIAL | Rollback separado; ainda há afirmações antigas de conclusão e recuperação nas instruções. |
| A13 | CORRIGIDO SEM EVIDÊNCIA | CI fixa govulncheck v1.9.0; scanner e status remoto ainda não executados nesta rodada. |

Ambiente: apenas Compose descartável, projetos `jungle-final-dev` e
`jungle-mission-1410-0515`. Sem AWS real, LocalStack pago ou alteração de volumes
preexistentes. Logs temporários em `/tmp/jungle-final-20261014`; evidências
consolidadas serão versionadas em `docs/audit/2026-10-14/`.

Nota de isolamento: o nome inicial `jungle-final-delivery` colidiu com um
projeto parado (volume de 13/10). O primeiro `up` recriou containers e executou
o job de migration (exit 0); ao detectar a data do volume, o projeto foi
parado, sem remoção do volume. Os ensaios usam o novo projeto exclusivo.

## Progresso validado

A03/A07/A09/A11: correções e regressões direcionadas aprovadas; ver
[evidências](audit/2026-10-14/README.md). A matriz inicial acima permanece
como diagnóstico da base, não como estado final.

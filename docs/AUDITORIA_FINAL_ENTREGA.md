# Auditoria final de entrega — 14/10/2026

Fonte normativa: [enunciado](reference/README_DESAFIO.md). Base inicial:
`6c04217498eb9809077ee7213724f6342d18e1f4`, branch `main`, árvore limpa,
`git fetch origin` concluído e `HEAD...origin/main = 0/0`.
Esta revisão não herda aprovações dos relatórios históricos.

## Matriz inicial do HEAD (inspeção, antes da nova regressão)

“CORRIGIDO SEM EVIDÊNCIA” significa código corretivo encontrado, ainda sem
execução nesta revisão. A matriz final será registrada abaixo após os ensaios.

| Achado | Estado inicial | Constatação no HEAD |
| --- | --- | --- |
| A01 | PARCIAL | Cadeia AWS sem fallback e HMAC presentes; IAM é exemplo não aplicado; runtime também provisiona filas com o mesmo cliente. |
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

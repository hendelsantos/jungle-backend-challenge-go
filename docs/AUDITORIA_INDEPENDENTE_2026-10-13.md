# Auditoria independente de qualidade e aderência — 13/10/2026

**Base auditada:** `e11e32525562850715c306ca926e02c0a9b1e72a`.
**Fonte normativa:** [enunciado preservado](reference/README_DESAFIO.md), commit oficial `bdd9e70b65db9b374ee070bd3a4768661a888997`.
**Parecer:** **80/100 — boa implementação financeira, entrega ainda não aprovada sem ressalvas.** Há três achados P1 e dez P2 abertos. Nenhuma correção de implementação foi feita nesta auditoria.

O sistema demonstrou precisão monetária, atomicidade, isolamento HTTP, idempotência persistente e concorrência entre três processos. Entretanto, o controle de acesso do broker não atende integralmente ao §2; o reinício do broker local perde eventos já publicados; e os scripts de interrupção não comprovam as janelas de falha que afirmam comprovar. Também há defeitos no worker de referências, na validação de entradas, nas métricas e nos próprios verificadores.

Não foi observado saldo negativo nem movimentação duplicada nos experimentos desta revisão. Isso não significa ausência de bugs. A nota é um julgamento técnico baseado nos pesos do §14, não uma certificação, nem uma previsão da nota da banca. Requisitos obrigatórios pendentes não são compensados pela pontuação ou por testes de carga, que são opcionais no enunciado.

Esta revisão qualifica o “GO” de [QA_RELATORIO_FINAL.md](QA_RELATORIO_FINAL.md). O relatório anterior permanece como registro histórico, não como comprovação suficiente para encerrar os achados abaixo.

## 1. Nota pelos critérios oficiais

| Critério | Nota / peso | Justificativa |
| --- | ---: | --- |
| Integridade financeira | 20 / 20 | Money exato, domínio e constraints, ledger append-only, atomicidade e reconciliação aprovados nos testes executados; nenhum defeito financeiro reproduzido no núcleo. |
| Concorrência | 18 / 20 | Disputas reais distribuídas corretas; horário anterior ao lock pode interromper a retomada de referências (A07). |
| Idempotência | 15 / 15 | Identidades persistentes, conflito, resultado histórico e cruzamento HTTP/SQS exercitados; 50 duplicatas reais convergiram. |
| Mensageria e recuperação | 8 / 15 | Inbox/outbox e leases bem estruturados; acesso ao broker, durabilidade local e evidência de interrupção precisam revisão (A01–A03). |
| Modelagem e arquitetura | 9 / 10 | Domínio separado de adaptadores, Fx e transações explícitas; fronteiras de validação/configuração e registro global de métricas ainda frágeis. |
| Testes | 5 / 10 | Integração real extensa passa, mas há falsos positivos, instrumentação incorreta, repetição com panic e scanner que não conclui (A03–A06, A09–A10, A13). |
| Observabilidade | 3 / 5 | Health, logs e outbox úteis; métricas financeiras não cobrem os canais de forma equivalente (A11). |
| Documentação | 2 / 5 | Ampla e com comandos úteis, porém conclusões exageram a evidência; soak, recuperação do broker e sequência de migrations precisam correção (A02, A03, A06, A12). |
| **Total** | **80 / 100** | **Revisar os bloqueadores antes de declarar conformidade integral.** |

## 2. Verificações realizadas e limites

A base estava limpa no início. A auditoria usou um Compose separado, `jungle-quality-audit`, com gateway `19080`, Keycloak `19090`, PostgreSQL `19532` e LocalStack `19570`. Os containers de entrega do usuário não foram reiniciados. As sondas Go foram carregadas por `-overlay`, sem modificar os arquivos de implementação ou a suíte existente.

| Verificação | Resultado observado |
| --- | --- |
| `make check` | Exit 0: vet, testes e testes com race detector. |
| `go vet -tags=integration ./...` | Exit 0. |
| `go test -race -tags=integration ./... -count=1 -json` com PG, Keycloak e LocalStack reais | Exit 0; **597 casos/subtestes passaram**, nenhum `fail` ou `skip` entre eventos de teste. A contagem inclui pais e subtestes, não significa 597 cenários independentes. |
| Construção e inicialização do Compose isolado | Exit 0; três APIs saudáveis, gateway e dependências reais. |
| R$ 100,00 + duas BETs concorrentes de R$ 80,00 via gateway | Uma `200/PROCESSED`, uma `422/INSUFFICIENT_FUNDS`; saldo R$ 20,00, versão 2, dois lançamentos, reconciliação consistente. |
| 50 BETs iguais de R$ 50,00 sobre R$ 100,00 via gateway | 50 respostas 200, uma identidade, 49 replays, saldo R$ 50,00, versão 2, dois lançamentos. Logs das três réplicas: **19/18/13** respostas desse cenário. |
| Repetição do teste HTTP/SQS com `-count=2` | Exit 1, panic de registro duplicado de collector (A09). |
| Sondas negativas k6, entrada longa, broker e parsers | Confirmaram A01, A02, A04, A05, A07, A08 e A10. |
| `go run golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...` | Exit 1; scanner terminou em panic, sem parecer de vulnerabilidades (A13). |

[Evidências e instruções das sondas](audit/2026-10-13/README.md) foram versionadas. Logs completos de execução também ficaram em `/tmp/jungle-audit-*` durante a sessão; os arquivos temporários não são necessários para ler as evidências versionadas.

**Limites:** não foi repetida toda a campanha longa de carga, nem realizado um novo soak de 15 minutos. O resultado anterior disponível em `tests/load/results/soak-clean.json` foi inspecionado e um extrato foi preservado. Não foi executada uma nova varredura Gitleaks do histórico nesta revisão. Não se certificam IAM de produção, disponibilidade de serviços externos ou ausência de todas as vulnerabilidades. O script completo `fault.sh` não foi reexecutado: a revisão identificou que suas precondições não estabelecem as janelas alegadas. Testes reais de lease/reentrega da suíte Go foram executados, mas não equivalem automaticamente a matar um processo no ponto exato.

## 3. Matriz de aderência

| Exigência | Parecer desta revisão | Evidência / ressalva |
| --- | --- | --- |
| §2 OIDC externo, escopo do provedor e carteiras internas | Atende nos testes executados | Keycloak real; casos de ausência, invalidez, expiração, roles e isolamento na integração. |
| §2 credenciais e políticas do broker | **Não atende integralmente** | Operações SQS sem Authorization aceitas; A01. HMAC protege o corpo financeiro, não o acesso às filas. |
| §§4–6 Go, Fx, domínio, Money, Wallet e ledger | Atende nos testes executados | Build, lifecycle, Money, persistência, constraints e atomicidade; A09 afeta repetição no mesmo processo. |
| §7 BET/WIN/LOSS/REFUND/ROLLBACK e referências | Parcial | Regras exercitadas; retomada pode falhar por horário anterior ao lock, A07. |
| §§5, 8 concorrência por carteira, três processos | Atende nos cenários executados | Locks SQL e versões; 80+80 e 50 duplicatas via gateway; carteira/ledger reconciliados. |
| §9 HTTP, idempotência e consultas | Atende nos fluxos principais, com defeito de validação | Replay histórico, conflitos e isolamento; entrada longa vira 503 permanente, A08. |
| §§6.5, 10 inbox, retry, DLQ e delete após commit | Implementado, evidência de interrupção incompleta | Integrações reais passam; Teste E não comprova commit antes do kill e delete depois, A03. |
| §§3, 11 outbox pós-commit e recuperação | Parcial | Lease, concorrência e republicação simulada passam; perda no restart local e Teste F insuficiente, A02–A03. |
| §12 logs, métricas e health | Parcial | Cobertura desigual de canais e registry global, A09/A11. |
| §13 testes de integração, race e recuperação | Parcial | Infraestrutura real e muitos cenários aprovados; faltam gates confiáveis e interrupções controladas. |
| §§14–15 carga opcional e entrega reproduzível | Parcial | Compose sobe; conclusões de carga e algumas instruções operacionais não são confiáveis, A04–A06/A12/A13. |

A auditoria não encontrou evidência para declarar eliminatórias como “dinheiro em float”, “saldo negativo” ou “idempotência em memória”. O acesso sem autorização ao broker exige atenção especial diante do §2 e do critério de acesso não autorizado do §14; não se demonstrou bypass do OIDC nem da assinatura financeira HMAC.

## 4. Achados abertos

Prioridades usadas aqui: **P1** bloqueia o parecer de entrega sem ressalvas por requisito obrigatório ou perda demonstrada; **P2** exige correção por comportamento incorreto ou evidência enganosa. São prioridades desta auditoria, não categorias oficiais do enunciado. Cada item permanece aberto até a reprodução inversa demonstrar a correção.

### A01 — P1 — Broker aceita operações sem credenciais/políticas de autorização

**Local:** `docker-compose.yml:54`; `internal/adapters/sqs/sqs.go:44` e provisionamento das filas. **Requisito:** §2.

O SDK usa credenciais fixas `test/test`, não há política de acesso provisionada e o ambiente não aplica autorização do broker. Em chamadas HTTP sem header `Authorization`, o broker respondeu 200 a `ListQueues`, `CreateQueue`, `SendMessage`, `ReceiveMessage`, `DeleteMessage` e `DeleteQueue`. As mutações foram feitas somente em uma fila descartável de auditoria. A fila oficial de eventos também retornou atributos sem uma `Policy` de acesso.

**Impacto:** quem alcança o broker pode consumir/apagar mensagens independentemente da assinatura do corpo. A exposição em loopback limita o alcance, mas não estabelece separação entre produtores, consumidores e administradores. HMAC impede forjar uma operação financeira válida; não impede remover a mensagem antes que o consumidor a receba.

**Fechamento:** provisionar e aplicar autorização compatível com o ambiente escolhido, separar permissões por papel/fila e demonstrar negação de acesso sem credenciais e de `Receive/Delete` para produtor. Explicitar qualquer limitação residual; não marcar o requisito atendido apenas porque o corpo está assinado.

### A02 — P1 — Reinício do LocalStack perde eventos publicados e não recupera prontidão sozinho

**Local:** `docker-compose.yml:54` (sem persistência configurada); `docs/RUNBOOK.md:52`; provisionamento SQS no startup da aplicação. **Requisitos:** §§3, 11 e 15.

**Reprodução isolada:** havia 13 eventos visíveis na fila de eventos e 13 registros da outbox com `published_at`, zero pendentes. Após `docker compose -p jungle-quality-audit restart localstack`, `ListQueues` retornou vazio e a fila retornou `QueueDoesNotExist`; readiness 503. Após reiniciar as APIs, filas foram recriadas e readiness voltou a 200, mas a fila ficou vazia e os 13 registros continuaram publicados, sem republicação automática. Nenhum consumidor externo de eventos foi executado.

**Impacto:** perda efetiva na entrega local de eventos confirmados e já aceitos pelo broker, apesar de os registros continuarem auditáveis no banco. O runbook afirma que reiniciar somente o LocalStack reprovisiona filas e que não há perda; isso não ocorreu. Outbox pendente protege falhas antes do reconhecimento da publicação, não a perda posterior do armazenamento do broker.

**Fechamento:** configurar/verificar durabilidade real do broker local, corrigir o procedimento de recuperação e comprovar que eventos ainda não consumidos sobrevivem ao restart. Não presumir que adicionar um volume basta sem testar o suporte de persistência da imagem/edição usada. Este achado é do Compose entregue; não afirma que o serviço gerenciado AWS SQS tenha a mesma perda.

### A03 — P1 — Testes de kill podem aprovar sem atingir as janelas exigidas

**Local:** `scripts/fault.sh:76`, `:131` e `:156`; `internal/adapters/sqs/outbox_publish_integration_test.go:183`. **Requisitos:** §§10–11 e §13, itens 5–6.

O parser `awk '/NotVisible/{print $NF}'` devolve `"1"` para o atributo JSON string. O `case` posterior classifica esse valor como não numérico. Mesmo sem observar mensagem em voo, o Teste E prossegue com “killing anyway”. Observar mensagem invisível, por si só, também não comprovaria **commit concluído e DeleteMessage ainda não executado**. Uma aposta inteiramente concluída antes do kill pode satisfazer o saldo final.

O Teste F espera um segundo após a operação e mata as APIs com eventos apenas “possibly unacknowledged”. Verifica o backlog global zerado, sem estabelecer que o evento-alvo foi publicado antes do kill e ainda não reconhecido na outbox, nem comparar o evento entregue antes/depois. O teste Go de republicação tem valor: omite o ack, espera a lease e verifica payload/eventId/attempts no broker real. Porém, ele simula abandono no mesmo processo; não sustenta sozinho a declaração documental de interrupção literal nessa janela.

**Fechamento:** corrigir parsing; sincronizar um processo real em pontos observáveis após commit/antes de delete e após publish/antes de ack; matar somente depois da precondição; exigir reentrega, identidade estável e único efeito financeiro. Falhar o teste se não atingiu a janela. Não é evidência de corrupção financeira existente: é uma lacuna de comprovação obrigatória.

### A04 — P2 — Checks financeiros do k6 falham sem reprovar a execução

**Local:** `tests/load/scenarios/idempotency.js:28` e `:67`; thresholds dos demais cenários.

Com `IDEMPOTENCY_REQUESTS=2 IDEMPOTENCY_AMOUNT=1.00`, o sistema debitou corretamente R$ 1,00 e ficou com R$ 99,00. O teardown exige R$ 50,00 fixos, registrou `checks_failed=1` de nove checks, mas **o k6 saiu com código 0**. Esse experimento prova que o gate aceita uma asserção financeira falhando; não indica saldo errado no backend.

As verificações usam `check(...)`/`console.error` sem thresholds correspondentes. Configurações anunciadas como ajustáveis também conflitam com resultados esperados fixos. O cenário multiwallet inspeciona apenas uma carteira no teardown, sem gate integrado do verificador SQL para todas.

**Fechamento:** checks financeiros devem determinar exit não zero; separar checks de negócio e infraestrutura; calcular expectativas a partir da configuração com aritmética exata; verificar todas as carteiras relevantes. Acrescentar um teste negativo do próprio gate. Conforme a [documentação de checks do k6](https://grafana.com/docs/k6/latest/using-k6/checks/), checks falhos isoladamente não reprovam o comando.

### A05 — P2 — Taxa de erro técnico e contadores da carga estão incorretos

**Local:** `tests/load/helpers/classify.js:4`, `:10`, `:32`; `tests/load/helpers/wager.js:7`, `:31`; `tests/load/scenarios/soak.js:59`.

`Rate.add(1)` só ocorre em status 0/5xx; sucessos nunca adicionam zero e outros erros classificados como técnicos não alimentam a taxa. Sonda com **999 sucessos e um 500** mostrou `financial_technical_error_rate=100% (1/1)`, quando a taxa sobre as mil operações seria 0,1%. No VU, contadores eram 999/1; no teardown, 0/0. Objetos JS comuns não agregam estado entre VUs e teardown.

`sendWager` importa `noteTechnical`, mas não o chama. Soak/multiwallet não classificam todos os wagers; assim, 5xx nesses caminhos não alimentam a Rate. Nos caminhos que chamam o classifier depois do helper, os objetos podem contar 5xx duas vezes. `res.error_code` também pode classificar 409 como técnico no helper, contrariando a separação anunciada.

**Fechamento:** uma classificação por resposta, `Rate.add(boolean)` para toda operação e `Counter` do k6 para agregação. Validar 200/409/422/400/401/403/500/timeout com oráculo conhecido. A semântica está documentada em [Rate](https://grafana.com/docs/k6/latest/javascript-api/k6-metrics/rate/) e [variáveis globais por VU](https://grafana.com/docs/k6/latest/examples/use-global-variables/).

### A06 — P2 — Soak e documentação superestimam duração e reconciliações

**Local:** `tests/load/scenarios/soak.js:20`; `docs/PERFORMANCE_REPORT.md:49`, `:65`, `:149`; `docs/ENGINEERING_EXCELLENCE_REPORT.md:85`.

O executor é `per-vu-iterations`, 400 iterações por VU, com `maxDuration=15m`. Isso impõe um teto, não 15 minutos de carga sustentada. O artefato local existente registra 10.527 requests a 25,096119/s: duração inferida **419,47 s, cerca de sete minutos**, incompatível com a descrição “Soak 15 min”. Não foi realizado um novo soak nesta auditoria.

O mesmo JSON registra **10.000 checks de BET e 400 checks de reconciliação**, todos aprovados. Portanto, os 10.400 checks não são 10.400 reconciliações. O artefato também mostra `http_req_failed=0`; a instrumentação customizada defeituosa não autoriza afirmar que esse run teve erros HTTP ocultos.

**Fechamento:** usar duração efetiva ou documentar corretamente o executor finito; preservar configuração, duração e resultados brutos; distinguir requests, operações e reconciliações. Não é necessário perseguir RPS mínimo: o enunciado não estabelece essa meta.

### A07 — P2 — Worker de referência usa timestamp anterior à aquisição do lock

**Local:** `internal/application/command/retry_reference.go:97`, `:121`, `:143` e `:155`.

`now` é capturado antes do claim/lock; o ajuste considera `item.State.UpdatedAt`, mas não `locked.UpdatedAt`. Outro escritor pode atualizar a carteira enquanto o worker espera o lock. Uma sonda com referência válida e carteira atualizada apenas **um microssegundo depois** do horário capturado reproduziu `processed=0` e `resume pending reference: wallet timestamps must be nonzero and ordered`.

**Impacto:** rollback e interrupção do lote de retomada; a pendência fica para nova tentativa. Sob escrita contínua ou diferença de relógio, a conclusão pode atrasar repetidamente. Não foi demonstrada perda financeira nem expiração indevida em produção.

**Fechamento:** definir o instante efetivo depois de adquirir o lock e respeitar os timestamps persistidos, mantendo a semântica do TTL. Testar espera real por lock, relógio defasado e resolução posterior sem abortar por horário obsoleto.

### A08 — P2 — Identificador externo longo causa erro permanente apresentado como retry transitório

**Local:** `internal/application/command/process_wager.go:377`; `internal/adapters/http/json.go:17`; índices de identidade das migrations.

Uma requisição autenticada com `externalTransactionId` hexadecimal aleatório de 12.000 caracteres, dentro do limite HTTP de 64 KiB, retornou **503 UNAVAILABLE**, orientando repetir a mesma identidade/conteúdo. O PostgreSQL registrou `index row requires 12024 bytes, maximum size is 8191`. As validações textuais não impõem limite compatível com os índices.

**Impacto:** entrada que nunca terá sucesso é tratada como indisponibilidade temporária. No fluxo assíncrono, falhas equivalentes podem consumir retries até a DLQ. O efeito SQS desta entrada específica não foi reproduzido; o 503 HTTP foi.

**Fechamento:** definir limites em bytes para campos indexados e documentá-los; validar HTTP e SQS antes da persistência, incluindo multibyte; responder erro permanente de entrada. O rollback preservou a transação financeira no experimento.

### A09 — P2 — Registry global de métricas impede reinicialização no mesmo processo

**Local:** `internal/adapters/http/outbox_metrics.go:49`, `:54`, `:64`.

Com a infraestrutura real disponível:

```sh
go test -race -tags=integration ./internal/adapters/http \
  -run '^TestAuthenticatedHTTPAndSQSIntegration$' -count=2
```

O comando falha com `panic: duplicate metrics collector registration attempted`. O collector do pool é registrado a cada montagem no registry global. A outbox usa `sync.Once`, que evita duplicação, mas prende o collector à dependência da primeira aplicação.

**Impacto:** testes repetidos e múltiplos ciclos Fx no mesmo processo quebram; reutilização pode consultar recursos já encerrados. Reiniciar um container cria outro processo e não reproduz, por si só, esse panic.

**Fechamento:** dar escopo adequado ao registry/collectors e lifecycle; executar repetição e teste de iniciar/parar/reiniciar a aplicação no mesmo processo.

### A10 — P2 — Verificador independente aceita expectativas inválidas e overflow

**Local:** `cmd/verifywallet/main.go:47` e `:78`.

Sondas do parser retornaram, sem erro:

| Entrada | Resultado em centavos |
| --- | ---: |
| `92233720368547758.08` | `-9223372036854775808` (overflow) |
| `1.-1` | `99` |
| `-0.01` | `1` |

`parseCounts("BET=oops")` devolve mapa vazio, removendo silenciosamente a verificação. A execução real de `verifywallet -counts 'BET=oops'` sobre uma carteira íntegra terminou com **exit 0**.

**Impacto:** o oráculo de auditoria pode aceitar configuração inválida ou comparar contra valor incorreto. Este parser é do comando auxiliar; **não é o Money usado pelo backend**. Além disso, o comando faz múltiplas leituras fora de um snapshot transacional único; se usado com escritores ativos, a consistência temporal da auditoria precisa ser tratada. Essa última observação é de revisão estática, não uma divergência concorrente reproduzida.

**Fechamento:** reutilizar parsing monetário exato/validado, rejeitar expectativas inválidas e definir snapshot ou exigir explicitamente banco sem escritas durante a verificação; adicionar testes de fronteira do próprio verificador.

### A11 — P2 — Métricas financeiras não representam igualmente HTTP, SQS e retomada

**Local:** `internal/adapters/http/wagers.go:66`; `internal/adapters/sqs/consumer.go:109`, `:145`, `:162`; `internal/application/command/retry_reference.go`.

Revisão estática: operações por status, replays e duração são contabilizados no handler HTTP. O consumidor contabiliza reentregas/DLQ, mas não os mesmos resultados financeiros; a conclusão pelo worker de referências tampouco atualiza esses resultados. A métrica de DLQ incrementa antes da confirmação do envio; falha ao enviar pode ser contada como entrada, enquanto redrive automático do broker não passa por esse incremento.

**Impacto:** visão parcial pode esconder o comportamento justamente do canal assíncrono. Logs do consumidor omitem provider/wallet/correlation disponíveis na entrada, dificultando correlação entre etapas. Não foi feito um experimento numérico de deltas de métricas por canal nesta revisão; o achado decorre dos pontos de instrumentação no código.

**Fechamento:** definir semântica de tentativa versus efeito financeiro único e instrumentá-la nos fluxos compartilhados, sem contar replay como nova movimentação; distinguir tentativa de DLQ e confirmação; testar deltas para HTTP, SQS, replay, retry e retomada.

### A12 — P2 — Bloco de inicialização do README inclui rollback destrutivo

**Local:** `README.md:67` a `:75` na base auditada.

No bloco de execução local, `make migrate-up` é seguido por `make migrate-down N=1` e `make run`. Embora o comando tenha comentário “rollback destrutivo”, copiar e executar a sequência retira a última migration antes de iniciar a aplicação. Isso remove estruturas necessárias de inbox/outbox e deixa a execução incompatível com os fluxos financeiros.

**Evidência:** ordem dos comandos e migration de reversão; essa sequência destrutiva não foi executada no banco do usuário.

**Fechamento:** separar instalação e exemplo de reversão, deixando explícita a reaplicação necessária antes de iniciar. Validar o caminho literal de copiar/colar em banco descartável. Corrigir também o runbook de A02; a documentação operacional faz parte da usabilidade da entrega.

### A13 — P2 — Scanner fixado na CI não conclui com a toolchain atual

**Local:** `.github/workflows/ci.yml:50`; `go.mod:3`.

A execução local do mesmo `govulncheck@v1.1.4` fixado no workflow, usando Go 1.27.2, terminou com `panic: unexpected expr: *ast.KeyValueExpr` no builder SSA de `golang.org/x/tools@v0.29.0`. O wrapper `go run` saiu com 1; o scanner reportou `exit status 2`.

**Impacto:** não se obtém resultado da varredura, e essa combinação de ferramenta/toolchain falha no experimento. A existência do passo na CI não prova que a análise esteja sendo concluída. Não foi consultado o resultado da execução remota da CI nesta auditoria, nem foi identificada uma CVE por esse panic.

**Fechamento:** escolher e fixar uma versão compatível do scanner, executar até obter um resultado válido e confirmar o workflow remoto. Não mascarar falha do scanner com `continue-on-error`.

## 5. Ordem sugerida para a próxima etapa

1. **A01–A02:** fechar autorização e durabilidade da mensageria; verificar eventos antes/depois do restart.
2. **A03:** obter evidência determinística das interrupções nos pontos exigidos.
3. **A07–A09:** corrigir worker, validação e lifecycle das métricas, com regressões direcionadas.
4. **A04–A06/A10:** consertar os instrumentos antes de repetir e publicar resultados de carga.
5. **A11–A13:** completar observabilidade, instruções de uso e varredura de dependências.
6. Reexecutar os cenários oficiais e atualizar o parecer com evidências do novo commit. Manter um commit/push por incremento revisável.

Não é necessário refatorar toda a arquitetura. O núcleo tem uma base consistente; a prioridade é fechar os comportamentos e as provas que hoje impedem uma declaração fiel de entrega concluída.

## 6. Perguntas técnicas para revisão

1. **Por que o lock não evita A07?** Ele serializa a alteração da carteira, mas não atualiza o horário que o worker capturou antes de esperar pelo lock.
2. **Por que uma outbox publicada não salva A02 automaticamente?** O banco já registrou a confirmação do broker; se o broker perde a mensagem depois, o publisher não a considera pendente.
3. **Por que um teste com check vermelho pode sair com zero?** No k6, o check registra uma medida; é necessário um threshold ou mecanismo explícito de falha para reprovar a execução.

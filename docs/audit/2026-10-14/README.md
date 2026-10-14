# Evidências — 14/10/2026

Base inicial `6c04217`. `make check` passou. Regressão de credenciais/HMAC:
`go test -race ./internal/adapters/sqs -run 'TestSQSClient|TestProducer' -count=2`
passou, incluindo assinatura inválida e produtor desconhecido. Credenciais
fictícias somente no ambiente local, nenhum acesso a AWS.

Integração SQS com PostgreSQL :16432 e LocalStack :14566 reais:
`go test -race -tags=integration ./internal/adapters/sqs -count=1 -json`
exit 0, 42 testes/subtestes pass, zero fail/skip. Inclui mensagens não
autorizadas/contrato inválido sem efeito financeiro, redrive, shutdown,
inbox/commit e publisher (não equivale aos kills literais E/F).

## Recuperação e instrumentação

`FAULT_DISPOSABLE=1 bash scripts/fault.sh`, projeto `jungle-mission-1410-0515`,
PostgreSQL/Keycloak/LocalStack e três processos reais: **E/F/H passaram**.
Primeira execução atingiu E/F, mas H foi interrompido por edição do arquivo
shell em execução; a repetição foi feita sobre cópia estável do script.

E exige inbox commitada e failpoint bloqueado antes de SIGKILL; após restart,
mesmo broker messageId, `inboxReplay=true`, receiveCount > 1, saldo 75.00,
dois lançamentos e reconciliação verdadeira. F: evento
`01a13980-c05a-743d-9542-3cf4ea9fdb5a`, broker ID
`3c3373a1-ac7d-44fa-a554-0439287bed34`, tentativa 1 sem published_at e lease
vigente; SIGKILL; lease vence naturalmente; novo processo envia tentativa 2,
mesmo eventId e SHA-256
`8b2840d5ea4b9d0b50965b8b4874d3a1463dc383e18e5e412b5cc8361aedc3a3`.
ACK persistido às 08:22:12 UTC. Saldo 30.00, um lançamento, reconciliação
verdadeira. FIFO deduplicou o segundo envio aceito; não alegamos duas entregas.
H: referência pendente sobreviveu ao restart e foi resolvida com BET via SQS:
saldo 100.00, três lançamentos, reconciliação verdadeira.

A07: `go test -race -tags=integration ./internal/application/command -run
TestRetryReference -count=2`, PostgreSQL :16432, passou. Novo teste espera lock
real comprovado por `pg_blocking_pids`, confirma writer com updated_at futuro,
exercita relógio defasado e TTL vencendo durante a espera, sem duplicar saldo.

A09: gauges da outbox/pool agora têm registry próprio por handler; contadores
sem dependência de banco continuam por processo. Dois ciclos completos Fx de
`TestAuthenticatedHTTPAndSQSIntegration -count=2` passaram com IdP, PG e SQS.
Teste adicional mantém dois handlers vivos com stores diferentes.

A11: callback de referências só após commit bem-sucedido; bootstrap conectado;
retries de inbox/referências, duração SQS e falha de publicação na DLQ medidos.
Regressões com `-race -count=2` verificam replay sem novo efeito, rollback sem
conclusão e DLQ rejeitada sem contagem de entrega. Métricas são best-effort:
crash após commit pode perder incremento; ledger e SQL são a prova financeira.

## Autorização e durabilidade do broker

Probe separado: `localstack/localstack:4.14.0`, volume exclusivo montado em
`/var/lib/localstack`, PERSISTENCE=1 e ENFORCE_IAM=1. `scripts/broker-probe.py`
registrou SendMessage anônimo sob política Deny → HTTP 200, uma mensagem visível;
após restart, GetQueueUrl → HTTP 400 QueueDoesNotExist. Isso **reprova** IAM e
persistência nessa imagem. Não foi usado plano pago ou AWS real.

`FAULT_DISPOSABLE=1 python3 scripts/broker-recovery.py` no projeto exclusivo:
indisponibilidade temporária → readiness 503 → dois eventos pendentes →
recuperação → dois publicados. Restart posterior: 14 mensagens visíveis e 38
outboxes publicadas → filas perdidas → readiness 503 → restart das APIs →
readiness 200, zero mensagens e as mesmas 38 outboxes publicadas. Exit 2,
durabilidade reprovada. PostgreSQL pausado também produziu readiness 503.
Primeira tentativa do probe não tratava saída vazia de ListQueues; corrigido
o parser para `{}` e repetido. Nenhuma inferência de recuperação pós-ACK.

## Validadores e segurança

A08: identificadores de 513 bytes, multibyte e 12.000 bytes rejeitados por HTTP
400/SQS DLQ sem movimento; limite de 512 bytes exercitado no caso de uso.
Integração dos adaptadores: 81 testes/subtestes pass, zero fail/skip.

A10: parsers e snapshot passaram com `-race -count=2`. Teste real faz WIN
concorrente depois da primeira leitura: auditoria ainda vê abertura 100.00 e
um lançamento, enquanto outra conexão confirma saldo atual 125.00. Sem
snapshot esse cenário produziria uma divergência falsa.

`govulncheck@v1.9.0 ./...`: exit 0, No vulnerabilities found. Gitleaks detectou
a string fictícia `test-signing-key-a-at-least-32-bytes` em metrics_test.go,
commit 17499c3. Revisão confirmou fixture de httptest, sem serviço externo.
`.gitleaks.toml` permite somente esse valor literal (regex ancorada); nova
varredura do histórico: 44 commits, no leaks found. Não houve credencial real
revelada nem reescrita de histórico. CI usa a mesma configuração explicitamente.

CI do incremento `17499c3`: [run 38534939043](https://github.com/hendelsantos/jungle-backend-challenge-go/actions/runs/38534939043)
falhou no Gitleaks pela fixture acima; integração remota foi ignorada pela
 dependência desse job. A falha não foi mascarada: configuração de exceção
restrita e nova execução serão verificadas no próximo commit.

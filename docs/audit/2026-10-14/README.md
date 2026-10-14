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

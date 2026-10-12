# ADR 0001 — Uma compensação direta por transação

Data: 12/10/2026. Estado: política implementada no domínio; garantia persistente pendente.

## Contexto

O desafio permite reembolsar uma `BET` e reverter uma `BET`, `WIN` ou `REFUND`. É necessário definir como essas operações se combinam para evitar a devolução duplicada do mesmo débito.

## Decisão

Cada transação pode ter uma única compensação direta bem-sucedida, considerando `REFUND` e `ROLLBACK` juntos. O domínio recebe em `Reference.Compensated` a informação de que essa compensação já existe. Quando verdadeira, a nova compensação termina em `REJECTED`, com `ALREADY_REVERSED`.

Um `ROLLBACK` de `REFUND` é uma compensação do crédito do reembolso. Ele debita o valor integral e exige saldo suficiente. A referência da aposta original continua compensada; desfazer o reembolso não reabre a possibilidade de compensar diretamente essa aposta outra vez.

Exemplo, em BRL: saldo 100 → BET de 25 → saldo 75 → REFUND de 25 → saldo 100 → ROLLBACK do REFUND → saldo 75. Um novo REFUND ou ROLLBACK direto da BET original é rejeitado.

A transação de referência permanece terminal e imutável. A informação de compensação será obtida pelas transações que a referenciam, sem editar o resultado original.

## Garantia a implementar na persistência

O caso de uso deverá carregar referência e informação de compensação dentro da mesma transação SQL que protege a carteira. Um índice único parcial em `reference_transaction_id`, para `REFUND`/`ROLLBACK` com estado `PROCESSED`, deverá impedir duas compensações diretas bem-sucedidas entre processos.

O booleano recebido pelo domínio não pode vir do cliente HTTP/SQS nem ser calculado com uma leitura desprotegida. A política atual, isolada em memória, não garante unicidade distribuída.

## Evidências desta etapa

Os testes de domínio cobrem as direções de reversão, incompatibilidade de valor e escopo, saldo insuficiente de reversão e a cadeia BET → REFUND → ROLLBACK do REFUND. A cadeia continua recusando novas compensações diretas da aposta original. Testes com banco e concorrência permanecem pendentes.

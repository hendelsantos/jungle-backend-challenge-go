# Fluxo de desenvolvimento

- O README oficial em `docs/reference/README_DESAFIO.md` é a fonte normativa dos requisitos. O plano organiza a execução; em caso de divergência, corrigir o plano e a implementação para respeitar o enunciado. Distinguir obrigações, recomendações e opções explicitamente permitidas pelo desafio.
- Manter `docs/CONFORMIDADE_DESAFIO.md` atualizado: cada requisito precisa de implementação e evidência no nível exigido. Testes unitários não substituem integração real; goroutines não substituem três processos; persistir outbox ou pendência não conclui publicação ou recuperação. Não declarar a entrega pronta com requisitos obrigatórios pendentes.
- Seguir o `PLANO_SISTEMA_JUNGLE_GO.md`, trabalhando em um incremento pequeno por vez.
- Usar `docs/reference/README_DESAFIO.md` como referência do enunciado e preservar o clone oficial em `references/backend-challenge-go`.
- Antes de concluir cada incremento, executar as verificações pertinentes e atualizar o plano, o README e a arquitetura quando necessário.
- Após validar cada incremento, criar um commit descritivo e enviar para `origin/main`, no repositório `hendelsantos/jungle-backend-challenge-go`. O usuário autorizou esse fluxo contínuo; não é necessário pedir confirmação a cada push.
- Integrar alterações remotas preservando o histórico. Não usar force push nem descartar mudanças do usuário.
- Se o push falhar, preservar os commits locais e informar a causa; nunca declarar envio sem confirmação do remoto.
- Não versionar credenciais, arquivos `.env`, binários ou o clone de referência.

- Prazo interno de entrega: 14/10/2026. Após ProcessWager, priorizar endpoints autenticados/OIDC e os próximos incrementos previstos no plano.
- A cada incremento, explicar decisões, conceitos de Go e evidências dos testes; concluir com três perguntas técnicas curtas para apoiar o aprendizado do usuário.

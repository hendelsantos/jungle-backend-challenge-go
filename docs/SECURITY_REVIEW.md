# Revisão de segurança — Jungle Backend Go

> Auditoria de segurança contínua (última: 14/10/2026).
> Ferramentas: Gitleaks (histórico completo), govulncheck no CI, revisão manual
> do parecer de autenticação/autorização/contratos.

## 1. Superfície e ameaças (STRIDE-resumed)

| Ameaça | Vetor | Controle implementado | Evidência |
| --- | --- | --- | --- |
| Falsificação de mensagem SQS | produtor forjando `providerId`/payload | HMAC-SHA256 por provedor obrigatório vinculado ao corpo; envelho inválido/permanent → DLQ; `OPENING` sempre rejeitada | `sqs/auth_test.go`, integração com broker real |
| Acesso entre provedores | JWT de provedor A operando na carteira B | `provider_id` vem do token (nunca do body); rota/consulta/replay/conflito isolados; `403`/`404` sem vazamento | `api_integration_test.go` (Valida rastro por banco: saldo/ledger/outbox intactos) |
| Replay de requisições | reuso da mesma chave/corpo | identidade persistente `(provider, idempotency_key)` e `(provider, external_id)`; replay legítimo devolve resultado armazenado; conteúdo divergente → `409` | `process_wager_integration_test.go` |
| Token inválido/expirado/audience errada | JWT falsificado ou de outro realm | JWKS com rotação, RS256 apenas, `iss/aud/exp/nbf/typ/azp`, clock padrão do go-oidc; cliente de 3 s da vida real teste expiração | integração real com Keycloak |
| Vazamento de credenciais | segredos no repositório | Gitleaks na CI + histórico completo (`no leaks found` 25 commits); segredos de desenvolvimento marcados como exclusivos do compose local | `ci.yml`, `docs/QA_RELATORIO_FINAL.md` |
| Dependências vulneráveis | CVEs em módulos Go | govulncheck no workflow com versão fixa | CI (fast job) |
| Exposição de métricas/logs | `/metrics` e logs contendo dados sensíveis | `/metrics` no loopback com labels enumerados (kind/status/reason); logs JSON sem tokens/salarios/PAN — correlation/message/transaction/wallet/provider ids apenas | `internal/observability`, revisão de log calls |
| Uso indevido de endpoints de administração | manipulação de carto/ledger via API | sem rota de mutação financeira além dos contratos oficiais; ledger físico append-only (triggers); aplicação M faixa de runtime sem ALTER/DROP/TRUNCATE | triggers de migration + matriz |
| Fila de eventos consumida repetidamente | eventos duplicados fiscal | eventId estável + dedup FIFO + responsabilidade do consumidor externo documentada (at-least-once); o sistema nunca promete exactly-once | `ARCHITECTURE.md` §outbox |

## 2. Credenciais e segredos

- **Produção:** nenhuma credencial real no repositório. Segredos reais devem
  ser providenciados pelo operador (vault/env do ambiente) e nunca comitados.
- **Desenvolvimento:** PostgreSQL `jungle/jungle-local`, segredos do realm
  `jungle` (`*-client-secret`) e chaves HMAC (`local-provider-*-signing-key`)
  são **exclusivos do Compose de desenvolvimento**, marcados em `README.md`,
  `.env.example` e nos provisionings. Gitleaks acompanha o histórico.
- ** mais conflitos**: a chave de API usada na sessão de trabalho (TypeSafe AI)
  nunca entrou no repositório (0 no histórico; reside apenas em `~/.bashrc`
  da máquina local).

## 3. Fronteira de confiança da mensageria

Mensagens SQS **não são autenticação**: o consumidor valida a assinatura HMAC
e vincula a assinatura ao `providerId` do corpo; ainda com produtor falso, as
suas regras do domínio restringem o alcance (nunca `OPENING`; escopos
player/wallet/round/valor/moeda verificados). O enunciado requer produtor
autorizado via credenciais de fila; LocalStack Community não prova IAM —
limitação de dev documentada.

## 4. Procedimentos de resposta

Ver `RUNBOOK.md` (PostgreSQL/Keycloak/LocalStack indisponíveis, outbox
acumula, incidentes DLQ, divergências, pool e restrita réplica. Nenhuma ação
de correção financeira silenciosa: toda correção exige evidência, decisão
humana registrada e novo lançamento como trilha auditável.

## 5. Pendências e follow-up

- [ ] Rotação de segredos de dev ao encerrar o desafio (nenhum em produção).
- [ ] Avaliar `go-sec%`/`staticcheck` como job adicional da CI (P3).
- [ ] Assinatura detached das imagens de entrega se for publicar imagens (P3).

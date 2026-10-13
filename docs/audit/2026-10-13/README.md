# Evidências da auditoria de 13/10/2026

Base: `e11e32525562850715c306ca926e02c0a9b1e72a`. Ver [parecer completo](../../AUDITORIA_INDEPENDENTE_2026-10-13.md). Estes arquivos registram falhas esperadas do código auditado; não são correções nem um novo gate de aprovação.

| Arquivo | Uso |
| --- | --- |
| [http-probes.txt](http-probes.txt) | Respostas reais: 80+80, entrada longa, 50 duplicatas e reconciliação. |
| [broker-unsigned.txt](broker-unsigned.txt) | Chamadas sem Authorization contra broker isolado; mutações somente em fila descartável. |
| [broker-restart.txt](broker-restart.txt) | Transcrição resumida das contagens antes/depois dos restarts. |
| [k6-failed-check-exit-zero.txt](k6-failed-check-exit-zero.txt) | Command, exit 0 e check financeiro falho. |
| [classify_probe.js](classify_probe.js) / [classifier-output.txt](classifier-output.txt) | Sonda determinística, sem HTTP, para o helper k6 e isolamento do teardown. |
| [soak-existing-summary.json](soak-existing-summary.json) | Extrato do resultado preexistente; não representa novo soak. |
| [reference_clock_test.go.txt](reference_clock_test.go.txt) / [verifier_input_test.go.txt](verifier_input_test.go.txt) | Fontes das sondas Go carregadas por overlay. |
| [unit-probes.txt](unit-probes.txt) | Saída das sondas Go; PASS significa que reproduziram o defeito esperado. |
| [verifier-invalid-counts.txt](verifier-invalid-counts.txt) | CLI aceitou `-counts 'BET=oops'`, exit 0. |
| [metrics-repeat-panic.txt](metrics-repeat-panic.txt) | Stack do panic ao repetir o teste de integração. |
| [govulncheck-panic-excerpt.txt](govulncheck-panic-excerpt.txt) | Início da falha do scanner; stack restante omitido. |

## Reproduzir as sondas sem alterar a suíte

Na raiz do repositório:

```sh
k6 run docs/audit/2026-10-13/classify_probe.js
python3 - <<'PY'
import json, pathlib, subprocess, tempfile
root = pathlib.Path.cwd()
evidence = root / 'docs/audit/2026-10-13'
with tempfile.TemporaryDirectory(prefix='jungle-audit-overlay-') as tmp:
    overlay = pathlib.Path(tmp) / 'overlay.json'
    overlay.write_text(json.dumps({'Replace': {
        str(root / 'internal/application/command/audit_repro_test.go'):
            str(evidence / 'reference_clock_test.go.txt'),
        str(root / 'cmd/verifywallet/audit_repro_test.go'):
            str(evidence / 'verifier_input_test.go.txt'),
    }}))
    subprocess.run(['go', 'test', '-overlay', str(overlay),
                    './internal/application/command', './cmd/verifywallet',
                    '-run', '^TestAudit', '-v'], check=True)
PY
```

As sondas Go usam fakes existentes para isolar o horário e os parsers. A integração real foi executada separadamente:

```sh
TEST_DATABASE_URL='postgres://jungle:jungle-local@localhost:55432/jungle?sslmode=disable' \
TEST_SQS_ENDPOINT=http://localhost:14566 \
TEST_OIDC_ISSUER=http://localhost:58080/realms/jungle \
go test -race -tags=integration ./... -count=1 -json
```

Essas portas eram das dependências disponíveis na sessão. Ajustá-las ao ambiente descartável usado na reprodução. Para A09, substituir o comando Go por:

```sh
go test -race -tags=integration ./internal/adapters/http \
  -run '^TestAuthenticatedHTTPAndSQSIntegration$' -count=2
```

## Ambiente descartável das sondas HTTP/broker

As sondas de restart pertencem exclusivamente ao projeto abaixo, sem relação com os containers de entrega já existentes do usuário:

```sh
JUNGLE_DELIVERY_GATEWAY_PORT=19080 \
JUNGLE_DELIVERY_OIDC_PORT=19090 \
JUNGLE_DELIVERY_PG_PORT=19532 \
JUNGLE_DELIVERY_SQS_PORT=19570 \
docker compose -p jungle-quality-audit -f docker-compose.yml up --build -d
```

Para reproduzir A02, criar uma carteira com saldo positivo, aguardar os eventos aparecerem na fila e `published_at` no banco, e registrar ambas as contagens. Reiniciar **somente** o serviço `localstack` desse projeto, consultar filas/readiness e repetir a contagem no banco. Depois reiniciar as APIs e comparar novamente. Não executar esse ensaio no ambiente que contenha dados que devam ser preservados. O projeto descartável foi removido ao final da auditoria.

O ensaio k6 de A04 está transcrito no arquivo de saída. As credenciais de teste vêm do realm de desenvolvimento versionado; nenhum token de acesso foi preservado nas evidências.

#!/usr/bin/env bash
# Verificador de documentação: links relativos a arquivos existentes e
# ocorrências de expressões de pendência (para revisão manual, sem exclusões
# cegas). Sem dependências além de bash/grep/python3.
set -u
cd "$(dirname "$0")/.."
fail=0

echo "== 1. Links relativos a arquivos locais"
python3 - <<'PYEOF'
import os, re, sys
docs = ["README.md", "ARCHITECTURE.md", "PLANO_SISTEMA_JUNGLE_GO.md",
        "docs/CONFORMIDADE_DESAFIO.md", "docs/QA_RELATORIO_FINAL.md",
        "docs/PERFORMANCE_REPORT.md", "docs/ENGINEERING_EXCELLENCE_REPORT.md",
        "docs/RUNBOOK.md", "docs/SECURITY_REVIEW.md",
        "docs/ARCHITECTURE_DIAGRAMS.md", "tests/load/README.md",
        "docs/AUDITORIA_FINAL_ENTREGA.md", "docs/GUIA_ENTREVISTA_JUNGLE.md",
        "docs/audit/2026-10-14/README.md", "docs/VIABILIDADE_MINISTACK.md",
        "docs/AUDITORIA_MINISTACK_FINAL.md", "docs/HOMOLOGACAO_AWS.md",
        "docs/AUDITORIA_AWS_SQS_FINAL.md"]
bad = 0
for doc in docs:
    if not os.path.exists(doc):
        print(f"MISSING DOC: {doc}"); bad += 1; continue
    base = os.path.dirname(doc) or "."
    for m in re.finditer(r'\]\(([^)#]+?)\)', open(doc).read()):
        target = m.group(1)
        if target.startswith(("http://", "https://", "mailto:", "#")):
            continue
        path = os.path.normpath(os.path.join(base, target))
        if not os.path.exists(path):
            print(f"BROKEN: {doc} -> {target}"); bad += 1
print("links: OK" if bad == 0 else f"links: {bad} broken")
sys.exit(1 if bad else 0)
PYEOF
[ $? -eq 0 ] || fail=1

echo "== 2. Scripts e comandos referenciados existem"
for script in scripts/smoke.sh scripts/fault.sh cmd/migrate cmd/verifywallet cmd/provision-sqs; do
  [ -e "$script" ] && echo "  ok: $script" || { echo "  MISSING: $script"; fail=1; }
done

echo "== 3. Variáveis de ambiente no .env.example vs config.go"
for v in DATABASE_URL HTTP_ADDR LOG_LEVEL OIDC_ISSUER OIDC_AUDIENCE REFERENCE_TTL REFERENCE_WORKER_INTERVAL SQS_ENDPOINT_URL SQS_REGION SQS_PROVIDER_KEYS SQS_QUEUE_PREFIX SQS_PROVISIONER_PROFILE SQS_CONSUMER_PROFILE SQS_PUBLISHER_PROFILE PG_POOL_MAX_CONNS OUTBOX_WORKERS; do
  grep -q "$v" .env.example && true || { echo "  .env.example missing: $v"; fail=1; }
done
echo "  env vars: OK"

echo "== 4. Expressões de pendência (revisão manual)"
grep -rn -iE 'ainda (está pendente|falta)|não implementado|continua pendente|não entregue|falta (integrar|desenvolver)|em desenvolvimento' README.md ARCHITECTURE.md docs/*.md PLANO_SISTEMA_JUNGLE_GO.md tests/load/README.md 2>/dev/null | grep -viE 'PENDING_REFERENCE' || echo "  nenhuma ocorrência"

exit $fail

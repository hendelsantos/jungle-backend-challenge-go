#!/usr/bin/env bash
# Required gates with explicit real infrastructure; skips cannot pass silently.
set -euo pipefail
: "${TEST_DATABASE_URL:?real PostgreSQL is required}"
: "${TEST_OIDC_ISSUER:?real OIDC issuer is required}"
: "${TEST_SQS_ENDPOINT:?real SQS-compatible broker is required}"
RESULTS_DIR=${RESULTS_DIR:-/tmp/jungle-quality-$(date +%s)}
mkdir -p "$RESULTS_DIR"
run() { local name=$1; shift; "$@" > "$RESULTS_DIR/$name.log" 2>&1; echo "$name: exit 0"; }
run make-check make check
run vet-integration go vet -tags=integration ./...
run race2 go test -race ./... -count=2
for count in 1 2; do
  run "integration$count" go test -race -tags=integration ./... -count="$count" -json
  python3 - "$RESULTS_DIR/integration$count.log" <<'PY'
import collections,json,sys
c=collections.Counter(x['Action'] for l in open(sys.argv[1]) if (x:=json.loads(l)).get('Test'))
print(dict(c))
assert c['pass']>0 and c['fail']==0 and c['skip']==0,c
PY
done
run compose docker compose -f docker-compose.yml config -q
run docs bash scripts/doccheck.sh
printf 'Quality gate logs: %s\n' "$RESULTS_DIR"

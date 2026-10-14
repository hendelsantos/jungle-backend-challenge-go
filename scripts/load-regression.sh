#!/usr/bin/env bash
# Short, source-backed load regression. Not a 15-minute soak campaign.
set -euo pipefail
: "${DATABASE_URL:?point DATABASE_URL to the disposable delivery database}"
RESULTS_DIR=${RESULTS_DIR:-/tmp/jungle-load-regression-$(date +%s)}
mkdir -p "$RESULTS_DIR"
go build -o "$RESULTS_DIR/verifywallet" ./cmd/verifywallet
run() {
  local name=$1 scenario=$2; shift 2
  local start end
  start=$(date +%s%N)
  env "$@" k6 run --summary-trend-stats 'avg,min,med,max,p(50),p(95),p(99)' --summary-export "$RESULTS_DIR/$name.json" "tests/load/scenarios/$scenario.js" > "$RESULTS_DIR/$name.log" 2>&1
  end=$(date +%s%N)
  echo "$name $start $end" >> "$RESULTS_DIR/durations.txt"
  python3 - "$RESULTS_DIR" "$name" <<'PY'
import re,sys,subprocess,pathlib
root,name=sys.argv[1:];p=pathlib.Path(root)
wallets=list(dict.fromkeys(re.findall(r'audit-wallet scenario=\S+ id=([a-f0-9-]{36})',(p/(name+'.log')).read_text())))
assert wallets,'no audited wallets'
flags={
 'idempotency':['-final','50.00','-rows','2','-counts','OPENING=1,BET=1'],
 'idempotency-custom':['-final','99.00','-rows','2','-counts','OPENING=1,BET=1'],
 'contention':['-final','10.00','-rows','34','-counts','OPENING=1,BET=33','-rejections','BET=17'],
 'contention-custom':['-final','98.00','-rows','3','-counts','OPENING=1,BET=2'],
 'multiwallet':['-final','400.00','-rows','9','-counts','OPENING=1,BET=2,WIN=2,LOSS=2,REFUND=2,ROLLBACK=2'],
}.get(name,[])
with (p/(name+'-sql.json')).open('w') as out:
 subprocess.run([str(p/'verifywallet'),*flags,*wallets],stdout=out,check=True)
print(name+': k6 and independent SQL passed ('+str(len(wallets))+' wallets)')
PY
}
run smoke smoke
run idempotency idempotency
run idempotency-custom idempotency IDEMPOTENCY_REQUESTS=2 IDEMPOTENCY_AMOUNT=1.00
run contention contention
run contention-custom contention CONTENTION_OPENING=100.00 CONTENTION_AMOUNT=1.00 CONTENTION_BETS=2
run multiwallet multiwallet
run ramp ramp-up RAMP_LEVEL=25 RAMP_HOLD=30s WALLET_POOL_SIZE=25
run soak soak SOAK_DURATION=60s SOAK_VUS=25
printf 'Load regression artifacts: %s\n' "$RESULTS_DIR"

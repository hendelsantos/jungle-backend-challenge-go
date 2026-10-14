#!/usr/bin/env bash
# End-to-end smoke for the delivery Compose: real Keycloak tokens, opening,
# fifty concurrent identical bets through the gateway (one debit across three
# api replicas), a conflicting pair with only one winner, idempotent replay and
# final reconciliation. Needs curl, jq and python3.
set -euo pipefail

GATEWAY_URL=${GATEWAY_URL:-http://localhost:9080}
KEYCLOAK_URL=${KEYCLOAK_URL:-http://localhost:59080}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

fail() { echo "smoke FAILED: $*" >&2; exit 1; }
for tool in curl jq python3; do command -v "$tool" >/dev/null || fail "$tool is required"; done

for _ in $(seq 1 120); do
  if curl -sf "$GATEWAY_URL/health/ready" >/dev/null 2>&1; then break; fi
  sleep 1
done
curl -sf "$GATEWAY_URL/health/ready" >/dev/null || fail "gateway is not ready at $GATEWAY_URL"

token() {
  curl -sf "$KEYCLOAK_URL/realms/jungle/protocol/openid-connect/token" \
    -d grant_type=client_credentials \
    -d client_id=$1 -d client_secret=$2 | jq -r .access_token
}
INTERNAL_TOKEN=$(token wallet-internal-client local-wallet-internal-client-secret) || fail "internal token"
PROVIDER_TOKEN=$(token provider-a-client local-provider-a-client-secret) || fail "provider-a token"

playerID=$(python3 -c 'import uuid;print(uuid.uuid4())')
correlationID=$(python3 -c 'import uuid;print(uuid.uuid4())')
runId=$(date +%s)$RANDOM

openWallet() {
  curl -sf -X POST "$GATEWAY_URL/wallets" \
    -H "Authorization: Bearer $INTERNAL_TOKEN" \
    -H "Idempotency-Key: open-$runId" \
    -H "X-Correlation-ID: $correlationID" \
    -H "Content-Type: application/json" \
    -d "{\"playerId\":\"$playerID\",\"initialBalance\":{\"amount\":\"100.00\",\"currency\":\"BRL\"}}" \
    || fail "wallet opening"
}
wallet=$(openWallet)
walletID=$(echo "$wallet" | jq -r .id)
echo "wallet $walletID opened"

# Fifty concurrent identical BETs of 50.00, same identity, gateway round-robin:
# each replica must reach the same stored result — one debit, 49 replays.
betRequest() {
  curl -s -X POST "$GATEWAY_URL/wagering/transactions" \
    -H "Authorization: Bearer $PROVIDER_TOKEN" \
    -H "Idempotency-Key: bet-$runId" \
    -H "Content-Type: application/json" \
    -d '{"providerId":"provider-a","externalTransactionId":"bet-'$runId'","playerId":"'$playerID'","walletId":"'$walletID'","roundId":"r-1","gameId":"fortune-chimp","kind":"BET","money":{"amount":"50.00","currency":"BRL"}}'
}

rm -f $work/smoke-responses.*
for i in $(seq 1 50); do
  ( betRequest >"$work/smoke-responses.$i" ) &
done
wait
processed=0
replays=0
for f in $work/smoke-responses.*; do
  if jq -e '.status == "PROCESSED"' "$f" >/dev/null 2>&1; then
    if jq -e '.idempotentReplay != true' "$f" >/dev/null; then processed=$((processed+1));
    else replays=$((replays+1)); fi
  fi
done
rm -f $work/smoke-responses.*
[ "$replays" = "49" ] || fail "expected 49 replays, got $replays"
[ "$processed" = "1" ] || fail "expected exactly 1 processed bet, got $processed (replays: $replays)"

# Two different bets of 40.00 concurrently: the wallet has 50.00, so exactly
# one wins and the other is rejected with INSUFFICIENT_FUNDS.
conflict() {
  curl -s -o $work/smoke-conflict-$1.json -w '%{http_code}' -X POST "$GATEWAY_URL/wagering/transactions" \
    -H "Authorization: Bearer $PROVIDER_TOKEN" \
    -H "Idempotency-Key: conflict-$1-$runId" \
    -H "Content-Type: application/json" \
    -d '{"providerId":"provider-a","externalTransactionId":"conflict-'$1'-'$runId'","playerId":"'$playerID'","walletId":"'$walletID'","roundId":"r-2","gameId":"fortune-chimp","kind":"BET","money":{"amount":"'${conflictAmount:-40.00}'","currency":"BRL"}}'
}
(c1=$(conflict a); echo "$c1" >$work/smoke-c1.status) &
(c2=$(conflict b); echo "$c2" >$work/smoke-c2.status) &
wait
c1=$(cat $work/smoke-c1.status); c2=$(cat $work/smoke-c2.status)
pair() {
  if [ "$1" = "200" ] && [ "$2" = "422" ]; then return 0; fi
  if [ "$1" = "422" ] && [ "$2" = "200" ]; then return 0; fi
  return 1
}
pair "$c1" "$c2" || fail "conflicting bets: got $c1/$c2, want 200/422 in some order"
grep -q INSUFFICIENT_FUNDS $work/smoke-conflict-a.json $work/smoke-conflict-b.json || fail "missing INSUFFICIENT_FUNDS on the rejected bet"

final=$(curl -sf "$GATEWAY_URL/wallets/$walletID" -H "Authorization: Bearer $INTERNAL_TOKEN") || fail "final wallet read"
balance=$(echo "$final" | jq -r ".balance.amount")
[ "$balance" = "10.00" ] || fail "final balance $balance != 10.00"

recon=$(curl -sf -X POST "$GATEWAY_URL/wallets/$walletID/reconciliation" -H "Authorization: Bearer $INTERNAL_TOKEN") || fail "reconciliation"
consistent=$(echo "$recon" | jq -r .consistent)
[ "$consistent" = "true" ] || fail "reconciliation not consistent: $recon"

ledgerCount=$(curl -sf "$GATEWAY_URL/wallets/$walletID/ledger?limit=100" -H "Authorization: Bearer $INTERNAL_TOKEN" | jq '.entries | length')
[ "$ledgerCount" = "3" ] || fail "ledger has $ledgerCount entries, want 3 (opening credit + two debits)"

rm -f $work/smoke-conflict-*.json $work/smoke-c*.status
echo "smoke OK: 1 debit for 50 identical bets (processed=$processed replays=$replays), 1 of 2 conflicting bets, balance 10.00, ledger $ledgerCount rows, reconciliation consistent"

# Literal official scenario: two distinct 80.00 BETs against a fresh 100.00.
playerID=$(python3 -c 'import uuid;print(uuid.uuid4())')
walletID=$(openWallet | jq -r .id)
runId="official-$runId"
conflictAmount=80.00
(conflict a > "$work/official-a.status") &
(conflict b > "$work/official-b.status") &
wait
pair "$(cat "$work/official-a.status")" "$(cat "$work/official-b.status")" || fail "official 80+80 statuses"
final=$(curl -sf "$GATEWAY_URL/wallets/$walletID" -H "Authorization: Bearer $INTERNAL_TOKEN")
[ "$(jq -r .balance.amount <<< "$final")" = "20.00" ] || fail "official balance"
ledger=$(curl -sf "$GATEWAY_URL/wallets/$walletID/ledger?limit=100" -H "Authorization: Bearer $INTERNAL_TOKEN")
[ "$(jq '[.entries[] | select(.direction == "DEBIT")] | length' <<< "$ledger")" = 1 ] || fail "official single debit"
recon=$(curl -sf -X POST "$GATEWAY_URL/wallets/$walletID/reconciliation" -H "Authorization: Bearer $INTERNAL_TOKEN")
[ "$(jq -r .consistent <<< "$recon")" = true ] || fail "official reconciliation"
# Replays must preserve the same 200/422 outcome and current balance.
(conflict a > "$work/replay-a.status") &
(conflict b > "$work/replay-b.status") &
wait
cmp "$work/official-a.status" "$work/replay-a.status"
cmp "$work/official-b.status" "$work/replay-b.status"
for suffix in a b; do jq -e '.idempotentReplay == true' "$work/smoke-conflict-$suffix.json" >/dev/null || fail "official replay"; done
final=$(curl -sf "$GATEWAY_URL/wallets/$walletID" -H "Authorization: Bearer $INTERNAL_TOKEN")
[ "$(jq -r .balance.amount <<< "$final")" = "20.00" ] || fail "official replay changed balance"
echo "official scenario OK: wallet=$walletID 100.00 - one 80.00 debit = 20.00, one rejection, replays unchanged"

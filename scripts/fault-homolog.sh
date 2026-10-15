#!/usr/bin/env bash
# Homologation fault drills (variant of scripts/fault.sh for compose.homolog.yaml):
# deterministic SIGKILL windows E/F/H against the app processes ONLY. The broker
# is managed AWS SQS and is NEVER killed; this script asserts the project has no
# local broker container before touching anything. Run only in a disposable
# homologation project: JUNGLE_HOMOLOG_DISPOSABLE=1 required.
set -euo pipefail
GATEWAY_URL=${GATEWAY_URL:-http://localhost:9090}
KEYCLOAK_URL=${KEYCLOAK_URL:-http://localhost:59090}
fail() { echo "fault-homolog FAILED: $*" >&2; exit 1; }
[ "${JUNGLE_HOMOLOG_DISPOSABLE:-}" = 1 ] || fail "set JUNGLE_HOMOLOG_DISPOSABLE=1 for a disposable homologation project"
: "${JUNGLE_HOMOLOG_PROJECT:?set the homologation compose project}"
: "${JUNGLE_HOMOLOG_REGION:?set the homologation region}"
: "${JUNGLE_HOMOLOG_QUEUE_PREFIX:?set the homologation queue prefix}"
: "${JUNGLE_HOMOLOG_AWS_DIR:?set the external AWS credentials directory}"
COMPOSE="docker compose -f compose.homolog.yaml"
for tool in curl jq python3 docker go; do command -v "$tool" >/dev/null || fail "$tool is required"; done
$COMPOSE config --services | grep -q localstack && fail "local broker container found; homologation must use managed AWS only"
[ -f "$JUNGLE_HOMOLOG_AWS_DIR/credentials" ] || fail "AWS credentials file missing in JUNGLE_HOMOLOG_AWS_DIR"
PROVIDER_KEY=${SQS_PROVIDER_KEY:-$(jq -r '."provider-a" // empty' <<<"${JUNGLE_HOMOLOG_PROVIDER_KEYS:-}")}
[ ${#PROVIDER_KEY} -ge 32 ] || fail "provider-a HMAC key missing (SQS_PROVIDER_KEY or JUNGLE_HOMOLOG_PROVIDER_KEYS)"
normal_image=${JUNGLE_IMAGE:-jungle-backend-go:homolog}
normal_tags=${JUNGLE_BUILD_TAGS:-}
work=$(mktemp -d)
runId=$(date +%s)$RANDOM
sql() { $COMPOSE exec -T postgres psql -U jungle -d jungle -At -v ON_ERROR_STOP=1 -c "$1"; }
logs() { $COMPOSE logs --no-log-prefix api 2>/dev/null | jq -Rc 'fromjson?'; }
wait_log() {
  for _ in $(seq 1 120); do
    logs | jq -c "select($1)" > "$2"
    [ -s "$2" ] && { cat "$2"; return; }
    sleep 0.25
  done
  fail "critical window not reached: $1"
}
restore() {
  export JUNGLE_TEST_POST_COMMIT_PAUSE= JUNGLE_TEST_PUBLISH_ACK_PAUSE=
  export JUNGLE_IMAGE="$normal_image" JUNGLE_BUILD_TAGS="$normal_tags"
  $COMPOSE up -d --no-deps api >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap restore EXIT
token() {
  curl -sf "$KEYCLOAK_URL/realms/jungle/protocol/openid-connect/token" \
    -d grant_type=client_credentials -d client_id=$1 -d client_secret=$2 | jq -r .access_token
}
INTERNAL_TOKEN=$(token wallet-internal-client local-wallet-internal-client-secret) || fail "internal token"
PROVIDER_TOKEN=$(token provider-a-client local-provider-a-client-secret) || fail "provider-a token"

assert() { if [ "$2" != "$3" ]; then fail "$1: got '$3', want '$2'"; fi; echo "  ok: $1 = $2"; }

wait_replicas_healthy() {
  for _ in $(seq 1 90); do
    if [ "$(curl -sf -o /dev/null -w '%{http_code}' "$GATEWAY_URL/health/ready" 2>/dev/null)" = "200" ]; then
      echo "  replicas healthy again"; return 0
    fi
    $COMPOSE up -d --no-deps api >/dev/null 2>&1
    sleep 2
  done
  fail "replicas did not become healthy"
}

open_wallet() {
  local open
  open=$(curl -sf -X POST "$GATEWAY_URL/wallets" \
    -H "Authorization: Bearer $INTERNAL_TOKEN" -H "Idempotency-Key: fault-homolog-open-$RANDOM$RANDOM" \
    -H "Content-Type: application/json" \
    -d "{\"playerId\":\"$2\",\"initialBalance\":{\"amount\":\"$1\",\"currency\":\"BRL\"}}") \
    || fail "wallet opening"
  echo "$open" | jq -r .id
}
wallet_state() { curl -sf "$GATEWAY_URL/wallets/$1" -H "Authorization: Bearer $INTERNAL_TOKEN" | jq -r '.balance.amount'; }
ledger_count() { curl -sf "$GATEWAY_URL/wallets/$1/ledger?limit=100" -H "Authorization: Bearer $INTERNAL_TOKEN" | jq '.entries | length'; }
consistent() { curl -sf -X POST "$GATEWAY_URL/wallets/$1/reconciliation" -H "Authorization: Bearer $INTERNAL_TOKEN" | jq -r .consistent; }
external_status() { curl -sf "$GATEWAY_URL/providers/provider-a/wagering/transactions/$1" -H "Authorization: Bearer $PROVIDER_TOKEN" | jq -r .status; }

# send_wager publishes a signed envelope through the homologation producer role
# (assumed from the operator's base identity) on managed AWS SQS.
send_wager() {
  AWS_CONFIG_FILE="$JUNGLE_HOMOLOG_AWS_DIR/config" \
  AWS_SHARED_CREDENTIALS_FILE="$JUNGLE_HOMOLOG_AWS_DIR/credentials" \
  AWS_PROFILE="${JUNGLE_HOMOLOG_QUEUE_PREFIX%-}-jungle-wager-producer" \
  SQS_ENABLED=true SQS_REGION="$JUNGLE_HOMOLOG_REGION" SQS_QUEUE_PREFIX="$JUNGLE_HOMOLOG_QUEUE_PREFIX" \
  SQS_PROVIDER_KEY="$PROVIDER_KEY" \
    go run ./cmd/send-wager -provider provider-a -file "$1"
}

export JUNGLE_IMAGE="jungle-backend-go:${JUNGLE_HOMOLOG_PROJECT}-faulttest"
export JUNGLE_BUILD_TAGS=faulttest
$COMPOSE build api >/dev/null 2>&1
export JUNGLE_TEST_POST_COMMIT_PAUSE=1s JUNGLE_TEST_PUBLISH_ACK_PAUSE=
$COMPOSE up -d --no-deps api >/dev/null 2>&1
wait_replicas_healthy
assert "three independent processes" 3 "$($COMPOSE ps -q api | wc -l | tr -d ' ')"

echo '=== TEST E (homolog): durable inbox commit, blocked before DeleteMessage on AWS'
playerID=$(python3 -c 'import uuid;print(uuid.uuid4())')
wallet=$(open_wallet 100.00 "$playerID")
externalId="fault-homolog-bet-$runId"
jq -n --arg msg "fault-homolog-$runId" --arg ext "$externalId" --arg player "$playerID" --arg wallet "$wallet" \
  '{messageId:$msg,type:"WagerTransactionRequested",occurredAt:"2026-10-14T00:00:00Z",data:{providerId:"provider-a",externalTransactionId:$ext,idempotencyKey:$ext,playerId:$player,walletId:$wallet,roundId:"fault-e",gameId:"fortune-chimp",kind:"BET",money:{amount:"25.00",currency:"BRL"}}}' > "$work/wager.json"
send_wager "$work/wager.json" >/dev/null
wait_log ".failpoint == \"after-commit-before-delete\" and .messageId == \"fault-homolog-$runId\"" "$work/e-before.json"
assert "inbox committed" 1 "$(sql "SELECT count(*) FROM inbox_messages WHERE message_id='fault-homolog-$runId' AND completed_at IS NOT NULL")"
assert "committed balance before kill" 75.00 "$(wallet_state "$wallet")"
$COMPOSE kill -s KILL api >/dev/null
export JUNGLE_TEST_POST_COMMIT_PAUSE=
$COMPOSE up -d --no-deps api >/dev/null 2>&1
wait_replicas_healthy
wait_log ".msg == \"wager message consumed\" and .messageId == \"fault-homolog-$runId\" and .inboxReplay == true and (.receiveCount | tonumber) > 1" "$work/e-after.json"
assert "same broker message on redelivery" "$(jq -r '.sqsMessageId' "$work/e-before.json" | head -1)" "$(jq -r '.sqsMessageId' "$work/e-after.json" | head -1)"
assert "one inbox" 1 "$(sql "SELECT count(*) FROM inbox_messages WHERE message_id='fault-homolog-$runId'")"
assert "balance after redelivery" 75.00 "$(wallet_state "$wallet")"
assert "ledger after redelivery" 2 "$(ledger_count "$wallet")"
assert "reconciliation" true "$(consistent "$wallet")"

echo '=== TEST F (homolog): SendMessage accepted, blocked before outbox ACK'
export JUNGLE_TEST_PUBLISH_ACK_PAUSE=1s
$COMPOSE up -d --no-deps api >/dev/null 2>&1
wait_replicas_healthy
competingPlayer=$(python3 -c 'import uuid;print(uuid.uuid4())')
competingWallet=$(open_wallet 0.00 "$competingPlayer")
fbody=$(jq -n --arg p "$competingPlayer" --arg w "$competingWallet" --arg e "fault-homolog-f-$runId" '{providerId:"provider-a",externalTransactionId:$e,playerId:$p,walletId:$w,roundId:"fault-f",gameId:"fortune-chimp",kind:"WIN",money:{amount:"30.00",currency:"BRL"}}')
curl -sf -X POST "$GATEWAY_URL/wagering/transactions" -H "Authorization: Bearer $PROVIDER_TOKEN" -H "Idempotency-Key: fault-homolog-f-$runId" -H 'Content-Type: application/json' -d "$fbody" > "$work/f-result.json"
transaction=$(jq -r .transactionId "$work/f-result.json")
event_id=$(sql "SELECT event_id FROM outbox_events WHERE aggregate_id='$transaction' AND event_type='WagerTransactionProcessed'")
[ -n "$event_id" ] || fail "target event absent"
wait_log ".failpoint == \"after-send-before-ack\" and .eventId == \"$event_id\"" "$work/f-point.json"
wait_log ".msg == \"outbox send accepted\" and .eventId == \"$event_id\" and .attempt == 1" "$work/f-before.json"
assert "unacknowledged live lease, first attempt" 1 "$(sql "SELECT count(*) FROM outbox_events WHERE event_id='$event_id' AND published_at IS NULL AND leased_until > clock_timestamp() AND attempts=1")"
$COMPOSE kill -s KILL api >/dev/null
export JUNGLE_TEST_PUBLISH_ACK_PAUSE=
$COMPOSE up -d --no-deps api >/dev/null 2>&1
wait_replicas_healthy
for _ in $(seq 1 90); do
  [ "$(sql "SELECT count(*) FROM outbox_events WHERE event_id='$event_id' AND published_at IS NOT NULL AND attempts=2")" = 1 ] && break
  sleep 1
done
assert "new publisher confirmed second attempt" 1 "$(sql "SELECT count(*) FROM outbox_events WHERE event_id='$event_id' AND published_at IS NOT NULL AND attempts=2")"
wait_log ".msg == \"outbox send accepted\" and .eventId == \"$event_id\" and .attempt == 2" "$work/f-after.json"
assert "identical payload bytes across two accepted sends" "$(jq -r .payloadSHA256 "$work/f-before.json" | head -1)" "$(jq -r '.payloadSHA256' "$work/f-after.json" | head -1)"
assert "publisher wallet balance" 30.00 "$(wallet_state "$competingWallet")"
assert "one ledger movement" 1 "$(ledger_count "$competingWallet")"
assert "publisher reconciliation" true "$(consistent "$competingWallet")"

echo '=== TEST H (homolog): restart with data while a reference is still pending'
hPlayer=$(python3 -c 'import uuid;print(uuid.uuid4())')
hWallet=$(open_wallet 100.00 "$hPlayer")
pending=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$GATEWAY_URL/wagering/transactions" \
  -H "Authorization: Bearer $PROVIDER_TOKEN" -H "Idempotency-Key: fault-homolog-h-refund-$runId" \
  -H "Content-Type: application/json" \
  -d '{"providerId":"provider-a","externalTransactionId":"fault-homolog-h-refund-'$runId'","playerId":"'$hPlayer'","walletId":"'$hWallet'","roundId":"fault-h-1","gameId":"fortune-chimp","kind":"REFUND","referenceExternalTransactionId":"fault-homolog-h-future-bet-'$runId'","money":{"amount":"25.00","currency":"BRL"}}')
assert "refund pended on a missing reference" "202" "$pending"
assert "pending status stored" "PENDING_REFERENCE" "$(external_status "fault-homolog-h-refund-$runId")"

$COMPOSE restart api >/dev/null 2>&1
wait_replicas_healthy
sleep 5
deadline=$((SECONDS+90))
while true; do
  st=$(external_status "fault-homolog-h-refund-$runId" 2>/dev/null || echo none)
  if [ "$st" = "PENDING_REFERENCE" ]; then if [ "$(wallet_state "$hWallet")" = "100.00" ]; then break; fi; fi
  if [ $SECONDS -gt $deadline ]; then fail "pending reference changed across restart (last: $st)"; fi
  sleep 3
done
assert "pending survived restart untouched" "PENDING_REFERENCE" "$st"
cat > "$work/h-bet.json" <<EOF
{
  "messageId": "fault-homolog-h-bet-$runId",
  "type": "WagerTransactionRequested",
  "occurredAt": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "data": {
    "providerId": "provider-a",
    "externalTransactionId": "fault-homolog-h-future-bet-$runId",
    "idempotencyKey": "fault-homolog-h-future-bet-$runId",
    "playerId": "$hPlayer",
    "walletId": "$hWallet",
    "roundId": "fault-h-1",
    "gameId": "fortune-chimp",
    "kind": "BET",
    "money": {"amount": "25.00", "currency": "BRL"}
  }
}
EOF
send_wager "$work/h-bet.json" >/dev/null || fail "publishing referenced bet"
deadline=$((SECONDS+120))
while true; do
  st=$(external_status "fault-homolog-h-refund-$runId" 2>/dev/null || echo none)
  if [ "$st" = "PROCESSED" ]; then break; fi
  if [ $SECONDS -gt $deadline ]; then fail "pending reference was not concluded (last: $st)"; fi
  sleep 3
done
assert "refund concluded after bet arrival across restart" "100.00" "$(wallet_state "$hWallet")"
assert "refund now processed" "PROCESSED" "$st"
assert "ledger rows after cross-channel conclusion" "3" "$(ledger_count "$hWallet")"
assert "reconciliation after pending reference" "true" "$(consistent "$hWallet")"

echo 'faults OK (homolog): E/F/H on managed AWS, only app processes killed'

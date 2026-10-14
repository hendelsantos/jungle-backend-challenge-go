#!/usr/bin/env bash
# Deterministic SIGKILL drills. Run only in an explicitly disposable Compose.
set -euo pipefail
GATEWAY_URL=${GATEWAY_URL:-http://localhost:9080}
KEYCLOAK_URL=${KEYCLOAK_URL:-http://localhost:59080}
SQS_URL=${SQS_URL:-http://localhost:49570}
PROVIDER_KEY=${SQS_PROVIDER_KEY:-local-provider-a-signing-key-32-bytes-minimum}
fail() { echo "fault FAILED: $*" >&2; exit 1; }
[ "${FAULT_DISPOSABLE:-}" = 1 ] || fail "set FAULT_DISPOSABLE=1 for an isolated disposable project"
[ -n "${COMPOSE_PROJECT_NAME:-}" ] || fail "COMPOSE_PROJECT_NAME must identify the disposable project"
COMPOSE="docker compose -f docker-compose.yml"
for tool in curl jq python3 docker go; do command -v "$tool" >/dev/null || fail "$tool is required"; done
normal_image=${JUNGLE_IMAGE:-jungle-backend-go:delivery}
normal_tags=${JUNGLE_BUILD_TAGS:-}
work=$(mktemp -d)
runId=$(date +%s)$RANDOM
sql() { $COMPOSE exec -T postgres psql -U jungle -d jungle -At -v ON_ERROR_STOP=1 -c "$1"; }
logs() { $COMPOSE logs --no-log-prefix api 2>/dev/null | jq -Rc 'fromjson?'; }
wait_log() { # jq filter, destination
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
  rm -f /tmp/fault-h-"$runId".json /tmp/fault-h-bet-"$runId".json
}
trap restore EXIT
token() {
  curl -sf "$KEYCLOAK_URL/realms/jungle/protocol/openid-connect/token" \
    -d grant_type=client_credentials \
    -d client_id=$1 -d client_secret=$2 | jq -r .access_token
}
INTERNAL_TOKEN=$(token wallet-internal-client local-wallet-internal-client-secret) || fail "internal token"
PROVIDER_TOKEN=$(token provider-a-client local-provider-a-client-secret) || fail "provider-a token"

assert() { # assert LABEL EXPECTED GOT
  if [ "$2" != "$3" ]; then fail "$1: got '$3', want '$2'"; fi
  echo "  ok: $1 = $2"
}

wait_replicas_healthy() {
  for _ in $(seq 1 90); do
    if [ "$(curl -sf -o /dev/null -w '%{http_code}' "$GATEWAY_URL/health/ready" 2>/dev/null)" = "200" ]; then
      echo "  replicas healthy again"; return 0
    fi
    # A killed replica can flake out of `up -d`; re-drive any exited container.
    $COMPOSE up -d --no-deps api >/dev/null 2>&1
    sleep 2
  done
  fail "replicas did not become healthy"
}

open_wallet() { # open_wallet OPENING-AMOUNT PLAYER-ID -> walletID; the
# wager identity requires player and wallet to match (ErrWalletScope otherwise)
  local open
  open=$(curl -sf -X POST "$GATEWAY_URL/wallets" \
    -H "Authorization: Bearer $INTERNAL_TOKEN" -H "Idempotency-Key: fault-open-$RANDOM$RANDOM" \
    -H "Content-Type: application/json" \
    -d "{\"playerId\":\"$2\",\"initialBalance\":{\"amount\":\"$1\",\"currency\":\"BRL\"}}") \
    || fail "wallet opening"
  echo "$open" | jq -r .id
}

wallet_state() { # prints the current balance amount
  curl -sf "$GATEWAY_URL/wallets/$1" -H "Authorization: Bearer $INTERNAL_TOKEN" | jq -r '.balance.amount'
}
ledger_count() {
  curl -sf "$GATEWAY_URL/wallets/$1/ledger?limit=100" -H "Authorization: Bearer $INTERNAL_TOKEN" | jq '.entries | length'
}
consistent() {
  curl -sf -X POST "$GATEWAY_URL/wallets/$1/reconciliation" -H "Authorization: Bearer $INTERNAL_TOKEN" | jq -r .consistent
}
external_status() { # external_status EXTERNAL-ID
  curl -sf "$GATEWAY_URL/providers/provider-a/wagering/transactions/$1" \
    -H "Authorization: Bearer $PROVIDER_TOKEN" | jq -r .status
}


# A dedicated binary includes blocking failpoints; normal builds reject the flags.
export JUNGLE_IMAGE="jungle-backend-go:${COMPOSE_PROJECT_NAME}-faulttest"
export JUNGLE_BUILD_TAGS=faulttest
$COMPOSE build api >/dev/null 2>&1
export JUNGLE_TEST_POST_COMMIT_PAUSE=1s JUNGLE_TEST_PUBLISH_ACK_PAUSE=
$COMPOSE up -d --no-deps api >/dev/null 2>&1
wait_replicas_healthy
assert "three independent processes" 3 "$($COMPOSE ps -q api | wc -l | tr -d ' ')"

echo '=== TEST E: durable inbox commit, blocked before DeleteMessage'
playerID=$(python3 -c 'import uuid;print(uuid.uuid4())')
wallet=$(open_wallet 100.00 "$playerID")
externalId="fault-bet-$runId"
jq -n --arg msg "fault-$runId" --arg ext "$externalId" --arg player "$playerID" --arg wallet "$wallet" \
  '{messageId:$msg,type:"WagerTransactionRequested",occurredAt:"2026-10-14T00:00:00Z",data:{providerId:"provider-a",externalTransactionId:$ext,idempotencyKey:$ext,playerId:$player,walletId:$wallet,roundId:"fault-e",gameId:"fortune-chimp",kind:"BET",money:{amount:"25.00",currency:"BRL"}}}' > "$work/wager.json"
AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_SESSION_TOKEN= \
SQS_ENDPOINT_URL="$SQS_URL" SQS_PROVIDER_KEY="$PROVIDER_KEY" \
  go run ./cmd/send-wager -provider provider-a -file "$work/wager.json"
wait_log ".failpoint == \"after-commit-before-delete\" and .messageId == \"fault-$runId\"" "$work/e-before.json"
assert "inbox committed" 1 "$(sql "SELECT count(*) FROM inbox_messages WHERE message_id='fault-$runId' AND completed_at IS NOT NULL")"
assert "committed balance before kill" 75.00 "$(wallet_state "$wallet")"
$COMPOSE ps -q api
$COMPOSE kill -s KILL api >/dev/null
export JUNGLE_TEST_POST_COMMIT_PAUSE=
$COMPOSE up -d --no-deps api >/dev/null 2>&1
wait_replicas_healthy
wait_log ".msg == \"wager message consumed\" and .messageId == \"fault-$runId\" and .inboxReplay == true and (.receiveCount | tonumber) > 1" "$work/e-after.json"
assert "same broker message on redelivery" "$(jq -r '.sqsMessageId' "$work/e-before.json" | head -1)" "$(jq -r '.sqsMessageId' "$work/e-after.json" | head -1)"
assert "one inbox" 1 "$(sql "SELECT count(*) FROM inbox_messages WHERE message_id='fault-$runId'")"
assert "balance after redelivery" 75.00 "$(wallet_state "$wallet")"
assert "ledger after redelivery" 2 "$(ledger_count "$wallet")"
assert "reconciliation" true "$(consistent "$wallet")"

echo '=== TEST F: SendMessage accepted, blocked before outbox ACK'
export JUNGLE_TEST_PUBLISH_ACK_PAUSE=1s
$COMPOSE up -d --no-deps api >/dev/null 2>&1
wait_replicas_healthy
competingPlayer=$(python3 -c 'import uuid;print(uuid.uuid4())')
competingWallet=$(open_wallet 0.00 "$competingPlayer")
fbody=$(jq -n --arg p "$competingPlayer" --arg w "$competingWallet" --arg e "fault-f-$runId" '{providerId:"provider-a",externalTransactionId:$e,playerId:$p,walletId:$w,roundId:"fault-f",gameId:"fortune-chimp",kind:"WIN",money:{amount:"30.00",currency:"BRL"}}')
curl -sf -X POST "$GATEWAY_URL/wagering/transactions" -H "Authorization: Bearer $PROVIDER_TOKEN" -H "Idempotency-Key: fault-f-$runId" -H 'Content-Type: application/json' -d "$fbody" > "$work/f-result.json"
transaction=$(jq -r .transactionId "$work/f-result.json")
event_id=$(sql "SELECT event_id FROM outbox_events WHERE aggregate_id='$transaction' AND event_type='WagerTransactionProcessed'")
[ -n "$event_id" ] || fail "target event absent"
wait_log ".failpoint == \"after-send-before-ack\" and .eventId == \"$event_id\"" "$work/f-point.json"
wait_log ".msg == \"outbox send accepted\" and .eventId == \"$event_id\" and .attempt == 1" "$work/f-before.json"
assert "unacknowledged live lease, first attempt" 1 "$(sql "SELECT count(*) FROM outbox_events WHERE event_id='$event_id' AND published_at IS NULL AND leased_until > clock_timestamp() AND attempts=1")"
sql "SELECT event_id, attempts, leased_by, leased_until, published_at, payload_json FROM outbox_events WHERE event_id='$event_id'"
$COMPOSE ps -q api
$COMPOSE kill -s KILL api >/dev/null
export JUNGLE_TEST_PUBLISH_ACK_PAUSE=
$COMPOSE up -d --no-deps api >/dev/null 2>&1
wait_replicas_healthy
# Lease expires naturally in PostgreSQL; no test UPDATE and no clock manipulation.
for _ in $(seq 1 90); do
  [ "$(sql "SELECT count(*) FROM outbox_events WHERE event_id='$event_id' AND published_at IS NOT NULL AND attempts=2")" = 1 ] && break
  sleep 1
done
assert "new publisher confirmed second attempt" 1 "$(sql "SELECT count(*) FROM outbox_events WHERE event_id='$event_id' AND published_at IS NOT NULL AND attempts=2")"
wait_log ".msg == \"outbox send accepted\" and .eventId == \"$event_id\" and .attempt == 2" "$work/f-after.json"
assert "identical payload bytes across two accepted sends" "$(jq -r .payloadSHA256 "$work/f-before.json" | head -1)" "$(jq -r .payloadSHA256 "$work/f-after.json" | head -1)"
sql "SELECT event_id, attempts, published_at FROM outbox_events WHERE event_id='$event_id'"
assert "publisher wallet balance" 30.00 "$(wallet_state "$competingWallet")"
assert "one ledger movement" 1 "$(ledger_count "$competingWallet")"
assert "publisher reconciliation" true "$(consistent "$competingWallet")"
# FIFO may deduplicate the second accepted send: two SDK success responses,
# attempts=2 and identical hashes prove republication, not a second delivery.
echo "=== TEST H: restart with data while a reference is still pending"
hPlayer=$(python3 -c 'import uuid;print(uuid.uuid4())')
hWallet=$(open_wallet 100.00 "$hPlayer")
pending=$(curl -s -o /tmp/fault-h-$runId.json -w '%{http_code}' -X POST "$GATEWAY_URL/wagering/transactions" \
  -H "Authorization: Bearer $PROVIDER_TOKEN" -H "Idempotency-Key: fault-h-refund-$runId" \
  -H "Content-Type: application/json" \
  -d '{"providerId":"provider-a","externalTransactionId":"fault-h-refund-'$runId'","playerId":"'$hPlayer'","walletId":"'$hWallet'","roundId":"fault-h-1","gameId":"fortune-chimp","kind":"REFUND","referenceExternalTransactionId":"fault-h-future-bet-'$runId'","money":{"amount":"25.00","currency":"BRL"}}')
assert "refund pended on a missing reference" "202" "$pending"
assert "pending status stored" "PENDING_REFERENCE" "$(external_status "fault-h-refund-$runId")"

$COMPOSE restart api >/dev/null 2>&1
wait_replicas_healthy
sleep 5

# The pending reference must survive the restart untouched: still suspended,
# wallet untouched by a suspended operation.
deadline=$((SECONDS+90))
while true; do
  st=$(external_status "fault-h-refund-$runId" 2>/dev/null || echo none)
  if [ "$st" = "PENDING_REFERENCE" ]; then if [ "$(wallet_state "$hWallet")" = "100.00" ]; then break; fi; fi
  if [ $SECONDS -gt $deadline ]; then fail "pending reference changed across restart (last: $st)"; fi
  sleep 3
done
assert "pending survived restart untouched" "PENDING_REFERENCE" "$st"
# The referenced bet arrives by the OTHER channel: signed SQS envelope.
cat > /tmp/fault-h-bet-$runId.json <<EOF
{
  "messageId": "fault-h-bet-$runId",
  "type": "WagerTransactionRequested",
  "occurredAt": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "data": {
    "providerId": "provider-a",
    "externalTransactionId": "fault-h-future-bet-$runId",
    "idempotencyKey": "fault-h-future-bet-$runId",
    "playerId": "$hPlayer",
    "walletId": "$hWallet",
    "roundId": "fault-h-1",
    "gameId": "fortune-chimp",
    "kind": "BET",
    "money": {"amount": "25.00", "currency": "BRL"}
  }
}
EOF
AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_SESSION_TOKEN= \
SQS_ENDPOINT_URL="$SQS_URL" SQS_PROVIDER_KEY="$PROVIDER_KEY" \
  go run ./cmd/send-wager -provider provider-a -file /tmp/fault-h-bet-$runId.json >/dev/null \
  || fail "publishing referenced bet"

deadline=$((SECONDS+120))
while true; do
  st=$(external_status "fault-h-refund-$runId" 2>/dev/null || echo none)
  if [ "$st" = "PROCESSED" ]; then break; fi
  if [ $SECONDS -gt $deadline ]; then fail "pending reference was not concluded (last: $st)"; fi
  sleep 3
done
assert "refund concluded after bet arrival across restart" "100.00" "$(wallet_state "$hWallet")"
assert "refund now processed" "PROCESSED" "$st"
assert "ledger rows after cross-channel conclusion" "3" "$(ledger_count "$hWallet")"
assert "reconciliation after pending reference" "true" "$(consistent "$hWallet")"


echo 'faults OK: deterministic E/F and cross-channel reference recovery H'

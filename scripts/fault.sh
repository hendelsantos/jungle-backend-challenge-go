#!/usr/bin/env bash
# Literal failure drills against the delivery Compose (Fase 6, Testes E/F/H):
#
#  E - kill -9 the three api replicas while a signed SQS wager is being
#      consumed; after restart the message redelivers and accounts exactly one
#      movement for that identity (commit lost or kept, both recover).
#  F - kill -9 the replicas right after committing outbox events; after
#      restart the publisher drains the backlog to the same eventIds.
#  H - restart the replicas with data while a PENDING_REFERENCE exists; after
#      restart the worker keeps it scheduled and concludes it when the bet
#      arrives. Requires curl, jq, python3 and `go run ./cmd/send-wager`.
set -euo pipefail

GATEWAY_URL=${GATEWAY_URL:-http://localhost:9080}
KEYCLOAK_URL=${KEYCLOAK_URL:-http://localhost:59080}
SQS_URL=${SQS_URL:-http://localhost:49570}
COMPOSE="docker compose -f docker-compose.yml"
PROVIDER_KEY=${SQS_PROVIDER_KEY:-local-provider-a-signing-key-32-bytes-minimum}

fail() { echo "fault FAILED: $*" >&2; exit 1; }
for tool in curl jq python3 docker; do command -v "$tool" >/dev/null || fail "$tool is required"; done

COMPOSE_CONTEXT=$(docker compose -f docker-compose.yml images -q api 2>/dev/null | head -1)
[ -n "$COMPOSE_CONTEXT" ] || fail "delivery compose is not built; run docker compose -f docker-compose.yml up -d --build first"

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
    $COMPOSE up -d api >/dev/null 2>&1
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

not_visible() { # ApproximateNumberOfMessagesNotVisible of the intake queue
  $COMPOSE exec -T localstack sh -c \
    "awslocal sqs get-queue-attributes --queue-url '$queue_url' --attribute-names ApproximateNumberOfMessagesNotVisible --query 'Attributes[0].Value' --output text 2>/dev/null" | tr -d '[:space:]'
}

# The intake queue is provisioned by the api replicas at startup; after a
# LocalStack restart only a replica restart re-creates it. The queue URL comes
# from the broker itself: LocalStack changed its URL scheme over versions.
queue_url=""
for _ in $(seq 1 60); do
  queue_url=$($COMPOSE exec -T localstack sh -c \
    'awslocal sqs get-queue-url --queue-name wager-transactions.fifo 2>/dev/null' | awk -F'"' '/QueueUrl/{print $4}')
  if [ -n "$queue_url" ]; then echo "intake queue provisioned: $queue_url"; break; fi
  sleep 2
done
[ -n "$queue_url" ] || fail "intake queue was never provisioned"

playerID=$(python3 -c 'import uuid;print(uuid.uuid4())')
runId=$(date +%s)$RANDOM

echo "=== PREPARING: wallet with 100.00"
wallet=$(open_wallet 100.00 "$playerID")
echo "  wallet $wallet"
assert "initial balance" "100.00" "$(wallet_state "$wallet")"
assert "initial reconciliation" "true" "$(consistent "$wallet")"

echo ""
echo "=== TEST E: kill -9 all api replicas while a signed SQS wager is in flight"
externalId="fault-bet-$runId"
cat > /tmp/fault-wager-$runId.json <<EOF
{
  "messageId": "fault-$runId",
  "type": "WagerTransactionRequested",
  "occurredAt": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "data": {
    "providerId": "provider-a",
    "externalTransactionId": "$externalId",
    "idempotencyKey": "$externalId",
    "playerId": "$playerID",
    "walletId": "$wallet",
    "roundId": "fault-r-1",
    "gameId": "fortune-chimp",
    "kind": "BET",
    "money": {"amount": "25.00", "currency": "BRL"}
  }
}
EOF
SQS_ENDPOINT_URL="$SQS_URL" SQS_PROVIDER_KEY="$PROVIDER_KEY" \
  go run ./cmd/send-wager -provider provider-a -file /tmp/fault-wager-$runId.json >/dev/null \
  || fail "publishing signed wager"

# Wait until a replica is holding the message (not visible), then kill the
# three processes at once. Whatever the moment of death before (rollback
# restarts) or after the commit (re-delivery replays the inbox), the identity
# in PostgreSQL decides: exactly one debit.
# The exact window (commit done, DeleteMessage pending) is deterministic in
# the Go integration suite (failpoint). Here the drill syncs on the observable
# broker signal (not-visible) and FAILS when neither the window was observed
# nor the wager is already terminal: a kill without precondition proves nothing.
inflight=""
for _ in $(seq 1 120); do
  n=$(not_visible)
  case "$n" in ''|[!0-9]*) sleep 0.25; continue ;; esac
  if [ "$n" -ge 1 ]; then inflight="$n"; break; fi
  if [ "$(external_status "$externalId" 2>/dev/null || echo none)" = "PROCESSED" ]; then
    inflight="completed-before-kill"; break
  fi
  sleep 0.25
done
[ -n "$inflight" ] || fail "failed to observe the in-flight window within 30s"
echo "  precondition observed: $inflight"
$COMPOSE kill -s KILL api >/dev/null 2>&1 || fail "docker kill api"
echo "  three api processes killed while the message was possibly in flight"
$COMPOSE up -d api >/dev/null 2>&1
wait_replicas_healthy

deadline=$((SECONDS+120))
while true; do
  status=$(external_status "$externalId" 2>/dev/null || echo "not-found")
  if [ "$status" = "PROCESSED" ]; then break; fi
  if [ $SECONDS -gt $deadline ]; then fail "wager $externalId was not processed after restart (last: $status)"; fi
  sleep 3
done
assert "wager processed once" "PROCESSED" "$status"
assert "balance after redelivery" "75.00" "$(wallet_state "$wallet")"
assert "ledger rows after redelivery" "2" "$(ledger_count "$wallet")"
assert "reconciliation after redelivery" "true" "$(consistent "$wallet")"

echo ""
echo "=== TEST F: kill -9 after committing outbox events; publisher recovers"
competingPlayer=$(python3 -c 'import uuid;print(uuid.uuid4())')
competingWallet=$(open_wallet 40.00 "$competingPlayer")
curl -sf -X POST "$GATEWAY_URL/wagering/transactions" \
  -H "Authorization: Bearer $PROVIDER_TOKEN" -H "Idempotency-Key: fault-f-bet-$runId" \
  -H "Content-Type: application/json" \
  -d "{\"providerId\":\"provider-a\",\"externalTransactionId\":\"fault-f-bet-$runId\",\"playerId\":\"$competingPlayer\",\"walletId\":\"$competingWallet\",\"roundId\":\"fault-f-1\",\"gameId\":\"fortune-chimp\",\"kind\":\"BET\",\"money\":{\"amount\":\"10.00\",\"currency\":\"BRL\"}}" >/dev/null \
  || fail "committing an event for the publisher"
sleep 1
$COMPOSE kill -s KILL api >/dev/null 2>&1
echo "  three api processes killed with committed events possibly unacknowledged"
$COMPOSE up -d api >/dev/null 2>&1
wait_replicas_healthy

deadline=$((SECONDS+120))
while true; do
  pending=$(curl -s "$GATEWAY_URL/metrics" | grep '^jungle_outbox_pending ' | awk '{print $2}')
  if [ "$pending" = "0" ]; then break; fi
  if [ $SECONDS -gt $deadline ]; then fail "outbox did not drain (last: ${pending:-none})"; fi
  sleep 3
done
assert "publisher drained the abandoned backlog after restart" "0" "$pending"
assert "publisher wallet balance" "30.00" "$(wallet_state "$competingWallet")"

echo ""
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

rm -f /tmp/fault-wager-$runId.json /tmp/fault-h-$runId.json
echo ""
echo "faults OK: kill-9 with in-flight wager, kill with unacknowledged outbox and restart with pending reference all account exactly once."

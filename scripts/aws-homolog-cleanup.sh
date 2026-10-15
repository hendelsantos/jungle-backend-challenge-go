#!/usr/bin/env bash
# Removes ONLY the homologation resources created by scripts/aws-homolog-apply.sh:
# the three FIFO queues with the exact JUNGLE_HOMOLOG_QUEUE_PREFIX and the four
# roles with the exact derived names. It never touches anything else.
#
#   export JUNGLE_HOMOLOG_CONFIRM=CLEANUP-HOMOLOG   # explicit authorization
#   scripts/aws-homolog-cleanup.sh
set -euo pipefail
source "$(dirname "$0")/aws-homolog-env.sh"

[ "${JUNGLE_HOMOLOG_CONFIRM:-}" = "CLEANUP-HOMOLOG" ] || {
  echo 'This script DELETES homologation AWS resources. Set JUNGLE_HOMOLOG_CONFIRM=CLEANUP-HOMOLOG' >&2
  exit 1
}
command -v aws >/dev/null || { echo 'AWS CLI is required' >&2; exit 1; }

ACCOUNT="$JUNGLE_HOMOLOG_ACCOUNT"
REGION="$JUNGLE_HOMOLOG_REGION"
PREFIX="$JUNGLE_HOMOLOG_QUEUE_PREFIX"
BASE="${PREFIX%-}"
export AWS_REGION="$REGION"
if [ -n "${AWS_PROFILE:-}" ]; then
  echo "Using AWS_PROFILE=$AWS_PROFILE (identity not printed)"
fi

echo "This will delete queues '${PREFIX}wager-*.fifo' and roles '${BASE}-jungle-*' in account $ACCOUNT, region $REGION."
read -r -p "Type CLEANUP to continue: " answer
[ "$answer" = "CLEANUP" ] || { echo 'aborted' >&2; exit 1; }

# The queue resource policy denies any principal outside the four roles, so
# queue deletion runs through the provisioner/admin role of this homologation.
ADMIN_ROLE="arn:aws:iam::${ACCOUNT}:role/${BASE}-jungle-queue-admin"
admin_creds="$(aws sts assume-role --role-arn "$ADMIN_ROLE" --role-session-name "homolog-cleanup-$(date +%s)" \
  --query 'Credentials.[AccessKeyId,SecretAccessKey,SessionToken]' --output text)" || {
  echo 'Cannot assume the homologation admin role; run aws-homolog-apply.sh first or check trust.' >&2
  exit 1
}
read -r AK SK ST <<<"$admin_creds"
export AWS_ACCESS_KEY_ID="$AK" AWS_SECRET_ACCESS_KEY="$SK" AWS_SESSION_TOKEN="$ST"

for q in "${PREFIX}wager-transactions.fifo" "${PREFIX}wager-transactions-dlq.fifo" "${PREFIX}wager-events.fifo"; do
  if url="$(aws sqs get-queue-url --queue-name "$q" --query QueueUrl --output text 2>/dev/null)"; then
    aws sqs delete-queue --queue-url "$url"
    echo "deleted queue: $q"
  else
    echo "queue absent, skipped: $q"
  fi
done

unset AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN
for base_role in jungle-wager-producer jungle-wager-consumer jungle-event-publisher jungle-queue-admin; do
  name="${BASE}-${base_role}"
  if aws iam get-role --role-name "$name" >/dev/null 2>&1; then
    aws iam delete-role-policy --role-name "$name" --policy-name homolog 2>/dev/null || true
    aws iam delete-role --role-name "$name"
    echo "deleted role: $name"
  else
    echo "role absent, skipped: $name"
  fi
done

echo "Homologation cleanup finished. Nothing outside prefix '$PREFIX' / base '$BASE' was touched."

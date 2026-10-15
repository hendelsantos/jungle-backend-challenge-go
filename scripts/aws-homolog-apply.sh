#!/usr/bin/env bash
# Creates the homologation AWS resources: 4 least-privilege IAM roles with
# trust+inline policies, and 3 FIFO queues (DLQ -> intake with redrive ->
# events) with resource policies. REQUIRES explicit confirmation and an AWS
# CLI authenticated with the base operator identity.
#
#   export JUNGLE_HOMOLOG_CONFIRM=APPLY-HOMOLOG   # explicit authorization
#   scripts/aws-homolog-apply.sh
#
# Rerun behavior: the script refuses to touch an existing role or queue with a
# different attribute/policy; rerun after cleanup or accept the identical
# resources as already provisioned.
set -euo pipefail
source "$(dirname "$0")/aws-homolog-env.sh"

[ "${JUNGLE_HOMOLOG_CONFIRM:-}" = "APPLY-HOMOLOG" ] || {
  echo 'This script CREATES AWS resources. Set JUNGLE_HOMOLOG_CONFIRM=APPLY-HOMOLOG' >&2
  echo 'only after the operator reviews scripts/aws-homolog-render.sh output.' >&2
  exit 1
}
command -v aws >/dev/null || { echo 'AWS CLI is required: https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html' >&2; exit 1; }
command -v python3 >/dev/null || { echo 'python3 is required' >&2; exit 1; }

OUT="${JUNGLE_HOMOLOG_OUTPUT_DIR:-.homolog-rendered}"
[ -d "$OUT" ] || { echo "Render first: scripts/aws-homolog-render.sh" >&2; exit 1; }

ACCOUNT="$JUNGLE_HOMOLOG_ACCOUNT"
REGION="$JUNGLE_HOMOLOG_REGION"
PREFIX="$JUNGLE_HOMOLOG_QUEUE_PREFIX"
BASE="${PREFIX%-}"
export AWS_REGION="$REGION"
if [ -n "${AWS_PROFILE:-}" ]; then
  echo "Using AWS_PROFILE=$AWS_PROFILE (identity not printed)"
fi
aws sts get-caller-identity --query Account --output text >/dev/null || { echo 'AWS CLI is not authenticated or region is unreachable' >&2; exit 1; }

sqs_base() { aws sqs "$@"; }

create_role() {
  local name="$1"
  local policy="$OUT/${name}-policy.json"
  local trust="$OUT/${name}-trust.json"
  if aws iam get-role --role-name "$name" >/dev/null 2>&1; then
    echo "role exists, reconciling inline policy: $name"
  else
    aws iam create-role --role-name "$name" --assume-role-policy-document "file://$trust" \
      --description "Jungle homologation least-privilege role" >/dev/null
    echo "created role: $name"
  fi
  aws iam put-role-policy --role-name "$name" --policy-name homolog \
    --policy-document "file://$policy"
}

create_queue() {
  local name="$1" attributes_json="$2"
  if sqs_base get-queue-url --queue-name "$name" >/dev/null 2>&1; then
    echo "queue exists, setting attributes: $name"
    sqs_base set-queue-attributes --queue-url \
      "$(sqs_base get-queue-url --queue-name "$name" --query QueueUrl --output text)" \
      --attributes "$attributes_json" >/dev/null
  else
    sqs_base create-queue --queue-name "$name" --attributes "$attributes_json" >/dev/null
    echo "created queue: $name"
  fi
}

for base_role in jungle-wager-producer jungle-wager-consumer jungle-event-publisher jungle-queue-admin; do
  create_role "${BASE}-${base_role}"
done

DLQ="${PREFIX}wager-transactions-dlq.fifo"
WAGER="${PREFIX}wager-transactions.fifo"
EVENTS="${PREFIX}wager-events.fifo"

create_queue "$DLQ" '{"FifoQueue":"true","ContentBasedDeduplication":"false"}'
DLQ_ARN="$(sqs_base get-queue-attributes --queue-url \
  "$(sqs_base get-queue-url --queue-name "$DLQ" --query QueueUrl --output text)" \
  --attribute-names QueueArn --query Attributes.QueueArn --output text)"

WAGER_ATTRS="$(python3 -c "import json; print(json.dumps({'FifoQueue':'true','ContentBasedDeduplication':'false','VisibilityTimeout':'30','RedrivePolicy':json.dumps({'deadLetterTargetArn':'$DLQ_ARN','maxReceiveCount':'3'})}))")"
create_queue "$WAGER" "$WAGER_ATTRS"
create_queue "$EVENTS" '{"FifoQueue":"true","ContentBasedDeduplication":"false"}'

for q in "$DLQ" "$WAGER" "$EVENTS"; do
  policy_file="$OUT/${BASE}-${q}-queue-policy.json"
  url="$(sqs_base get-queue-url --queue-name "$q" --query QueueUrl --output text)"
  policy="$(python3 -c "import json; print(json.dumps({'Policy': open('$policy_file').read()}))")"
  sqs_base set-queue-attributes --queue-url "$url" --attributes "$policy" >/dev/null
  echo "queue resource policy applied: $q"
done

echo
echo "Homologation resources ready in $REGION (account $ACCOUNT)."
echo "AWS config profiles to use locally (adapt source_profile to your base identity):"
cat <<EOF

[profile ${BASE}-provisioner]
role_arn = arn:aws:iam::${ACCOUNT}:role/${BASE}-jungle-queue-admin
source_profile = <your-base-profile>
region = ${REGION}

[profile ${BASE}-consumer]
role_arn = arn:aws:iam::${ACCOUNT}:role/${BASE}-jungle-wager-consumer
source_profile = <your-base-profile>
region = ${REGION}

[profile ${BASE}-publisher]
role_arn = arn:aws:iam::${ACCOUNT}:role/${BASE}-jungle-event-publisher
source_profile = <your-base-profile>
region = ${REGION}
EOF
echo
echo "Next: docker compose -f compose.homolog.yaml up --build -d"
echo "Then: scripts/test-aws-sqs-iam.sh | test-aws-sqs-durability.sh | test-aws-sqs-app.sh"

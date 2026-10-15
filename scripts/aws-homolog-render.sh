#!/usr/bin/env bash
# Renders the homologation IAM policies and SQS queue resource policies into
# an output directory. Pure local rendering + JSON validation: it never calls
# AWS and never needs credentials. Review the output before applying.
set -euo pipefail
source "$(dirname "$0")/aws-homolog-env.sh"

OUT="${JUNGLE_HOMOLOG_OUTPUT_DIR:-.homolog-rendered}"
ASSUME_BY="${JUNGLE_HOMOLOG_ASSUME_BY:-arn:aws:iam::${JUNGLE_HOMOLOG_ACCOUNT}:root}"
mkdir -p "$OUT"
command -v python3 >/dev/null || { echo 'python3 is required' >&2; exit 1; }

JUNGLE_HOMOLOG_OUTPUT_DIR="$OUT" JUNGLE_HOMOLOG_ASSUME_BY="$ASSUME_BY" python3 - <<'PYEOF'
import json, os, re, sys

account = os.environ["JUNGLE_HOMOLOG_ACCOUNT"]
region = os.environ["JUNGLE_HOMOLOG_REGION"]
prefix = os.environ["JUNGLE_HOMOLOG_QUEUE_PREFIX"]
out = os.environ["JUNGLE_HOMOLOG_OUTPUT_DIR"]
assume_by = os.environ["JUNGLE_HOMOLOG_ASSUME_BY"]

with open("deployments/sqs/iam-policies.json", encoding="utf-8") as fh:
    template = json.load(fh)

def render_arns(document):
    text = json.dumps(document)
    text = text.replace("arn:aws:sqs:us-east-1:000000000000:", f"arn:aws:sqs:{region}:{account}:{prefix}")
    text = text.replace("arn:aws:iam::000000000000:", f"arn:aws:iam::{account}:{prefix.rstrip('-')}-")
    return json.loads(text)

role_names = {}
for base in template["roles"]:
    role_names[base] = f"{prefix.rstrip('-')}-{base}"

trust = {
    "Version": "2012-10-17",
    "Statement": [{
        "Sid": "HomologationOperatorAssumes",
        "Effect": "Allow",
        "Principal": {"AWS": assume_by},
        "Action": "sts:AssumeRole",
    }],
}

written = []
for base, role in template["roles"].items():
    policy = render_arns(role["policy"])
    name = role_names[base]
    policy_path = os.path.join(out, f"{name}-policy.json")
    trust_path = os.path.join(out, f"{name}-trust.json")
    with open(policy_path, "w", encoding="utf-8") as fh:
        json.dump(policy, fh, indent=2)
    with open(trust_path, "w", encoding="utf-8") as fh:
        json.dump(trust, fh, indent=2)
    written += [policy_path, trust_path]

queue_arn = lambda name: f"arn:aws:sqs:{region}:{account}:{prefix}{name}"
role_arns = [f"arn:aws:iam::{account}:role/{name}" for name in role_names.values()]
resource_template = template["queueResourcePolicy"]["policy"]
for name in ["wager-transactions.fifo", "wager-transactions-dlq.fifo", "wager-events.fifo"]:
    policy = json.loads(json.dumps(resource_template))
    for statement in policy["Statement"]:
        statement["Resource"] = queue_arn(name)
        statement["Condition"]["StringNotLike"]["aws:PrincipalArn"] = role_arns
    path = os.path.join(out, f"{prefix.rstrip('-')}-{name}-queue-policy.json")
    with open(path, "w", encoding="utf-8") as fh:
        json.dump(policy, fh, indent=2)
    written.append(path)

# Guard rails: every rendered document must be valid JSON (already true), must
# not contain the fictional account, and every queue ARN must be prefixed.
for path in written:
    with open(path, encoding="utf-8") as fh:
        text = fh.read()
    assert "000000000000" not in text, f"fictional account leaked into {path}"
    assert json.loads(text), f"invalid JSON in {path}"

print("Rendered homologation IAM/SQS documents:")
for path in written:
    print(f"  {path}")
print(f"\nRoles to create ({len(role_names)}): " + ", ".join(role_names.values()))
print(f"Trust principal: {assume_by}")
print(f"Queues to create (FIFO, prefix {prefix}): wager-transactions, wager-transactions-dlq, wager-events")
print(f"Region: {region} | Account: {account}")
PYEOF

echo
echo 'Review the rendered JSON, then request authorization before:'
echo '  scripts/aws-homolog-apply.sh   (creates the resources above)'
echo '  scripts/aws-homolog-cleanup.sh (removes ONLY the prefixed resources)'

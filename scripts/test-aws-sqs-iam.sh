#!/usr/bin/env bash
# A01 homologation gate: broker-side IAM authorization matrix on real AWS SQS.
# Requires the homologation environment (docs/HOMOLOGACAO_AWS.md) and an
# authenticated base identity; never runs in CI.
set -euo pipefail
source "$(dirname "$0")/aws-homolog-env.sh"
export AWS_HOMOLOG_DISPOSABLE=1
go test -race -tags=awshomolog ./tests/homolog -run '^TestAWSSQSIAM$' -count=1 -v -timeout=10m

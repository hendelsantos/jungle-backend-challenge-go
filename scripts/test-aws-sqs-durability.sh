#!/usr/bin/env bash
# A02 homologation gate: broker durability on real AWS SQS (crash-before-delete
# redelivery with identity preserved, visibility honored, FIFO dedup, DLQ).
set -euo pipefail
source "$(dirname "$0")/aws-homolog-env.sh"
export AWS_HOMOLOG_DISPOSABLE=1
go test -race -tags=awshomolog ./tests/homolog -run '^TestAWSSQSDurability$' -count=1 -v -timeout=10m

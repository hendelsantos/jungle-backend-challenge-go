#!/usr/bin/env bash
# A02 homologation gate, application level: single debit across broker
# redelivery (inbox replay) and outbox republication after an interrupted ACK.
# Needs the homologation PostgreSQL (compose.homolog.yaml) via TEST_DATABASE_URL.
set -euo pipefail
source "$(dirname "$0")/aws-homolog-env.sh"
export AWS_HOMOLOG_DISPOSABLE=1
: "${TEST_DATABASE_URL:?set the homologation PostgreSQL DSN, e.g. postgres://jungle:jungle-homolog@localhost:59542/jungle?sslmode=disable}"
go test -race -tags=awshomolog ./tests/homolog -run '^TestAWSSQSAppRecovery$' -count=1 -v -timeout=10m

#!/usr/bin/env bash
source "$(dirname "$0")/ministack-poc-env.sh"
go test -race -tags=ministackpoc ./tests/broker -run '^TestMiniStackIAM$' -count=1 -v -timeout=3m

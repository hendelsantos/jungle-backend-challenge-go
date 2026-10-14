#!/usr/bin/env bash
# Sourced by acceptance probes; never creates/removes infrastructure.
set -euo pipefail
: "${MINISTACK_POC_PROJECT:?set the dedicated jungle-ministack-poc-* project}"
: "${MINISTACK_POC_ENDPOINT:?set its explicit http://127.0.0.1:PORT endpoint}"
[[ "$MINISTACK_POC_PROJECT" =~ ^jungle-ministack-poc-[a-z0-9-]+$ ]] || { echo 'Not a dedicated POC project' >&2; exit 1; }
[ "${MINISTACK_POC_DISPOSABLE:-}" = 1 ] || { echo 'MINISTACK_POC_DISPOSABLE=1 required' >&2; exit 1; }
cd "$(dirname "${BASH_SOURCE[0]}")/.."
container=$(docker compose -f compose.ministack.yaml ps -q ministack)
[ -n "$container" ] || { echo 'Start a NEW isolated POC before running this probe' >&2; exit 1; }
docker inspect "$container" | jq -e '.[0].State.Health.Status == "healthy"' >/dev/null || { echo 'Wait for docker compose up --wait before testing' >&2; exit 1; }
port=$(docker compose -f compose.ministack.yaml port ministack 4566)
[ "$MINISTACK_POC_ENDPOINT" = "http://$port" ] || { echo 'Endpoint does not match the POC container' >&2; exit 1; }
docker inspect "$container" | jq -e '.[0].Config.Env | index("AUTH=true") != null and index("PERSIST_STATE=1") != null' >/dev/null
docker inspect "$container" | jq -e '.[0].Config.Image == "ministackorg/ministack:1.5.27@sha256:4cb4f8c5c1b8e58e9a37af1c80f243de5b5cecf2e7b0461086d0dc7781d6642c"' >/dev/null

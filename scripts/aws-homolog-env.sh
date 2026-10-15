#!/usr/bin/env bash
# Shared validation for homologation scripts. Sourced; never creates resources.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

: "${JUNGLE_HOMOLOG_ACCOUNT:?set the 12-digit AWS account id (no spaces)}"
: "${JUNGLE_HOMOLOG_REGION:?set the homologation region, e.g. sa-east-1}"
: "${JUNGLE_HOMOLOG_QUEUE_PREFIX:?set the queue prefix ending with '-', e.g. homolog-hendel-}"

[[ "$JUNGLE_HOMOLOG_ACCOUNT" =~ ^[0-9]{12}$ ]] || { echo 'Account id must be exactly 12 digits' >&2; exit 1; }
[[ "$JUNGLE_HOMOLOG_REGION" =~ ^[a-z]{2}-[a-z-]+-[0-9]$ ]] || { echo 'Region must look like sa-east-1' >&2; exit 1; }
[[ "$JUNGLE_HOMOLOG_QUEUE_PREFIX" =~ ^[a-z0-9][a-z0-9-]{2,20}-$ ]] || { echo 'Queue prefix must be lowercase letters/digits/hyphens ending with -' >&2; exit 1; }
case "$JUNGLE_HOMOLOG_QUEUE_PREFIX" in
  homolog-*) : ;;
  *) echo 'Queue prefix must start with homolog- to protect other resources' >&2; exit 1 ;;
esac

export JUNGLE_HOMOLOG_ACCOUNT JUNGLE_HOMOLOG_REGION JUNGLE_HOMOLOG_QUEUE_PREFIX

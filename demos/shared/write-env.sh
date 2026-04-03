#!/usr/bin/env bash
# Writes provider API keys from the current environment to stdout
# in .env format. Used by demo run.sh scripts to bootstrap .env
# when keys are already exported in the shell.
set -euo pipefail

[ -n "${OPENAI_API_KEY:-}" ]        && echo "OPENAI_API_KEY=${OPENAI_API_KEY}"
[ -n "${ANTHROPIC_API_KEY:-}" ]     && echo "ANTHROPIC_API_KEY=${ANTHROPIC_API_KEY}"
[ -n "${AZURE_OPENAI_KEY:-}" ]      && echo "AZURE_OPENAI_KEY=${AZURE_OPENAI_KEY}"
[ -n "${AZURE_OPENAI_ENDPOINT:-}" ] && echo "AZURE_OPENAI_ENDPOINT=${AZURE_OPENAI_ENDPOINT}"
[ -n "${OPENAI_ORG_ID:-}" ]         && echo "OPENAI_ORG_ID=${OPENAI_ORG_ID}"

# Ensure at least one line was written
true

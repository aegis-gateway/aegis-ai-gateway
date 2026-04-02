#!/usr/bin/env bash
set -euo pipefail

# AEGIS AI Gateway — Quickstart
# Thin wrapper that delegates to demos/00-quickstart/run.sh

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
DEMO_DIR="${SCRIPT_DIR}/demos/00-quickstart"

# Forward port overrides as env vars
export GATEWAY_PORT="${GATEWAY_HOST_PORT:-${GATEWAY_PORT:-8080}}"
export WEBUI_PORT="${WEBUI_HOST_PORT:-${WEBUI_PORT:-3000}}"
export METRICS_PORT="${METRICS_HOST_PORT:-${METRICS_PORT:-9090}}"

# Ensure .env exists at demo level — copy from root if available
if [ ! -f "${DEMO_DIR}/.env" ]; then
  if [ -f "${SCRIPT_DIR}/.env" ]; then
    cp "${SCRIPT_DIR}/.env" "${DEMO_DIR}/.env"
  fi
fi

exec "${DEMO_DIR}/run.sh"

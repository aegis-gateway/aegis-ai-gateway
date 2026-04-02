#!/usr/bin/env bash
# Wait for the AEGIS gateway health endpoint, then print status.
# Usage: ./wait-for-gateway.sh [base_url]
set -euo pipefail

BASE_URL="${1:-http://localhost:8080}"
RETRIES=60

echo "Waiting for gateway at ${BASE_URL}…"
until curl -sf "${BASE_URL}/aegis/v1/health" > /dev/null 2>&1 || [ $RETRIES -eq 0 ]; do
  RETRIES=$((RETRIES - 1))
  sleep 2
done

if [ $RETRIES -eq 0 ]; then
  echo "ERROR: gateway did not become healthy after 120s" >&2
  exit 1
fi

echo "Gateway is healthy."

#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"

DEMO_KEY="aegis-demo-quickstart"
GATEWAY_PORT="${GATEWAY_PORT:-8080}"
GATEWAY_URL="${GATEWAY_URL:-http://localhost:${GATEWAY_PORT}}"

# The gateway rejects filtered content with 451 (Unavailable For Legal
# Reasons) via httputil.WriteContentBlockedError, not 400.
BLOCKED_STATUS="451"

# ── Preflight: ensure provider keys ──────────────────────────────
#
# Compose passes provider keys to the gateway through env_file only, so
# anything exported in the shell has to be written into .env or the
# container starts with empty credentials. Exported keys therefore always
# win and are re-persisted on every run.
if [ -n "${OPENAI_API_KEY:-}" ] || [ -n "${ANTHROPIC_API_KEY:-}" ]; then
  ../shared/write-env.sh > .env
elif [ ! -f .env ] || ! grep -qE '^(OPENAI_API_KEY|ANTHROPIC_API_KEY)=.+' .env; then
  [ -f .env ] || cp ../shared/.env.example .env
  echo "Created .env — add at least one provider API key:"
  echo "  OPENAI_API_KEY=sk-proj-..."
  echo "  ANTHROPIC_API_KEY=sk-ant-..."
  echo ""
  echo "Or export them in your shell and re-run: ./run.sh"
  exit 1
fi

# ── Pick a model whose primary provider we actually have a key for ──
#
# The four blocked cases never reach a provider (the filter chain runs
# before routing), but the clean request does. aegis-fast's primary route
# is Anthropic, and ResolveRoute does not fall back to the OpenAI route on
# an auth error — the registry has no idea a key is missing — so with only
# OPENAI_API_KEY set, aegis-fast would return 500.
if grep -qE '^ANTHROPIC_API_KEY=.+' .env; then
  MODEL="aegis-fast"
else
  MODEL="gpt-4o"
fi

# ── Fake credentials, assembled at runtime ───────────────────────
#
# Built from fragments rather than written out literally so the repo never
# contains a token-shaped string. Secret scanners flag those as leaks, and
# an allowlist entry to silence them is exactly the thing that hides a real
# leak later. Each value still matches the corresponding regex in
# internal/filter/secrets/patterns.go.
AWS_FAKE="AKIAIOSFODNN7EXAMPLE"                            # AWS's own doc example
GH_FAKE="ghp_$(printf 'A%.0s' {1..36})"                    # gh[pousr]_[A-Za-z0-9_]{36,}
B64="eyJ"
JWT_FAKE="${B64}hbGciOiJIUzI1NiJ9.${B64}zdWIiOiJ0ZXN0In0.notarealsignature"
PEM_FAKE='-----BEGIN RSA PRIVATE KEY-----'

# The gateway and keygen both exit unless AEGIS_KEY_PEPPER is set (min 32
# chars), and Compose only reaches the gateway through env_file — so make sure
# .env carries one before starting the stack.
../shared/ensure-pepper.sh .env

# ── Start stack ───────────────────────────────────────────────────
echo "Building and starting AEGIS secrets-filter demo…"
docker compose up --build -d

../shared/wait-for-gateway.sh "${GATEWAY_URL}"

cat <<BANNER

============================================
  AEGIS Secrets Filter Demo
============================================

The secrets filter runs before every request reaches a provider.
Any message containing a recognisable credential pattern is rejected
with HTTP ${BLOCKED_STATUS} and logged to audit_events.

Clean requests are routed to: ${MODEL}

BANNER

PASS=0
FAIL=0

# Builds a chat-completions payload. %s keeps backslash escapes such as \n
# intact, so they reach the gateway as JSON escapes rather than real newlines.
body_for() {
  printf '{"model":"%s","messages":[{"role":"user","content":"%s"}]}' "${MODEL}" "$1"
}

check() {
  local label="$1"
  local expected_status="$2"
  local body="$3"

  actual_status=$(curl -s -o /dev/null -w "%{http_code}" \
    -X POST "${GATEWAY_URL}/v1/chat/completions" \
    -H "Authorization: Bearer ${DEMO_KEY}" \
    -H "Content-Type: application/json" \
    -d "${body}")

  if [ "${actual_status}" = "${expected_status}" ]; then
    printf "  %-40s → PASS (HTTP %s)\n" "${label}" "${actual_status}"
    PASS=$((PASS + 1))
  else
    printf "  %-40s → FAIL (expected %s, got %s)\n" "${label}" "${expected_status}" "${actual_status}"
    FAIL=$((FAIL + 1))
  fi
}

# ── Test cases ────────────────────────────────────────────────────
check "[1/5] AWS key blocked"      "${BLOCKED_STATUS}" "$(body_for "My AWS key is ${AWS_FAKE} — is this valid?")"
check "[2/5] GitHub token blocked" "${BLOCKED_STATUS}" "$(body_for "Token: ${GH_FAKE}")"
check "[3/5] Private key blocked"  "${BLOCKED_STATUS}" "$(body_for "Here is my key:\\n${PEM_FAKE}\\nMIIEowIBAAKCAQEA...")"
check "[4/5] JWT blocked"          "${BLOCKED_STATUS}" "$(body_for "JWT: ${JWT_FAKE}")"
check "[5/5] Clean request passes" "200"               "$(body_for 'Hello from AEGIS!')"

# ── Summary ───────────────────────────────────────────────────────
echo ""
echo "Results: ${PASS} passed, ${FAIL} failed"
echo ""

# ── Audit events ──────────────────────────────────────────────────
#
# audit_events columns are event_type / timestamp / error_message /
# metadata (JSONB). There is no action, reason, or created_at column.
echo "Recent audit_events (blocked requests):"
docker compose exec -T postgres psql -U aegis -d aegis -c \
  "SELECT timestamp, event_type, metadata->>'filter_type' AS filter, metadata->>'reason' AS reason
     FROM audit_events
    WHERE event_type = 'filter_block'
    ORDER BY timestamp DESC
    LIMIT 10;" \
  || echo "(could not read audit_events — see the psql error above)"

echo ""

# ── Tear down ─────────────────────────────────────────────────────
echo "Tearing down…"
docker compose down -v

echo ""
echo "============================================"
echo "  Done!"
echo "============================================"
echo ""
echo "Key takeaways:"
echo "  - Secrets are blocked before any data reaches an LLM provider"
echo "  - Every blocked request is logged to audit_events"
echo "  - Clean requests pass through unmodified"
echo "  - No configuration required — the filter is always on"
echo ""

if [ "${FAIL}" -gt 0 ]; then
  exit 1
fi

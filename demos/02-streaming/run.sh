#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"

DEMO_KEY="aegis-demo-quickstart"
GATEWAY_URL="${GATEWAY_URL:-http://localhost:${GATEWAY_PORT:-8080}}"
METRICS_URL="http://localhost:${METRICS_PORT:-9090}"

# ── Preflight: gateway must be running ───────────────────────────
if ! curl -sf "${GATEWAY_URL}/aegis/v1/health" > /dev/null 2>&1; then
  echo "ERROR: gateway not reachable at ${GATEWAY_URL}" >&2
  echo ""
  echo "Start demos/00-quickstart first:"
  echo "  cd demos/00-quickstart && ./run.sh"
  exit 1
fi
echo "Gateway is running at ${GATEWAY_URL}"
echo ""

# ── Helpers ──────────────────────────────────────────────────────
pause() {
  echo ""
  read -r -p "Press enter to continue…"
  echo ""
}

step() {
  echo "============================================"
  echo "  $1"
  echo "============================================"
  echo ""
}

# ── Step 1 — Streaming with aegis-fast ──────────────────────────
step "Step 1 — Streaming with aegis-fast (Claude Haiku / GPT-4o-mini)"

echo "Sending a streaming request — watch the SSE chunks arrive in real time."
echo ""
echo '$ curl --no-buffer '"${GATEWAY_URL}"'/v1/chat/completions \'
echo '    -H "Authorization: Bearer …" \'
echo '    -d '"'"'{"model":"aegis-fast","stream":true,"messages":[…]}'"'"
echo ""

curl --no-buffer -s -X POST "${GATEWAY_URL}/v1/chat/completions" \
  -H "Authorization: Bearer ${DEMO_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "aegis-fast",
    "stream": true,
    "messages": [{"role": "user", "content": "Count from 1 to 10, one number per line, with a brief pause between each."}]
  }'

echo ""
echo ""
echo "Each line above is an SSE chunk: data: {JSON}\\n\\n"
echo "The final chunk is always: data: [DONE]"

pause

# ── Step 2 — Streaming with aegis-reasoning ─────────────────────
step "Step 2 — Streaming with aegis-reasoning (Claude Opus)"

echo "Same request pattern, different model."
echo "Notice the model field in each chunk shows which provider is serving."
echo ""

curl --no-buffer -s -X POST "${GATEWAY_URL}/v1/chat/completions" \
  -H "Authorization: Bearer ${DEMO_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "aegis-reasoning",
    "stream": true,
    "messages": [{"role": "user", "content": "Count from 1 to 10, one number per line, with a brief pause between each."}]
  }'

echo ""
echo ""
echo "Both aegis-fast and aegis-reasoning stream in the same OpenAI-compatible"
echo "SSE format, even when the underlying provider is Anthropic."

pause

# ── Step 3 — Compare: non-streaming ─────────────────────────────
step "Step 3 — Compare: same request without streaming"

echo "Without \"stream\": true, the response arrives all at once:"
echo ""

curl -s -X POST "${GATEWAY_URL}/v1/chat/completions" \
  -H "Authorization: Bearer ${DEMO_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "aegis-fast",
    "messages": [{"role": "user", "content": "Count from 1 to 10, one number per line, with a brief pause between each."}]
  }' | jq .

echo ""
echo "Both return identical content — streaming is opt-in."

pause

# ── Step 4 — Check streaming metrics ────────────────────────────
step "Step 4 — Streaming metrics from Prometheus"

echo '$ curl '"${METRICS_URL}"'/metrics | grep aegis_streaming'
echo ""

METRICS_OUTPUT=$(curl -s "${METRICS_URL}/metrics" | grep -E 'aegis_streaming' || true)

if [ -z "${METRICS_OUTPUT}" ]; then
  echo "(No streaming metrics yet — they appear after the first streaming request"
  echo " is fully processed by the enhanced streaming handler.)"
else
  echo "${METRICS_OUTPUT}"
fi

echo ""
echo "Metric reference:"
echo "  aegis_streaming_chunk_total          — Total SSE chunks sent (per provider/model)"
echo "  aegis_streaming_time_to_first_token_ms — Time until the first token arrives (histogram)"
echo "  aegis_streaming_tokens_per_second    — Throughput during streaming (histogram)"
echo "  aegis_streaming_duration_ms          — Total wall-clock time of the stream (histogram)"
echo "  aegis_streaming_error_total          — Streaming errors by type (counter)"

echo ""
echo "============================================"
echo "  Done! All 4 steps complete."
echo "============================================"
echo ""
echo "Key takeaways:"
echo "  - Add \"stream\": true to any chat completion request"
echo "  - AEGIS normalizes all providers to OpenAI SSE format"
echo "  - Monitor TTFT to catch latency regressions early"
echo ""

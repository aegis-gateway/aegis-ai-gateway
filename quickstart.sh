#!/usr/bin/env bash
#
# AEGIS AI Gateway. The one documented way to run the quickstart.
#
#   ./quickstart.sh                start the gateway (no credentials required)
#   ./quickstart.sh verify         run the evidence sequence and show its work
#   ./quickstart.sh down           stop the stack and delete its volumes
#   ./quickstart.sh --build        build from this working tree, not the published image
#   ./quickstart.sh --with-webui   also start Open WebUI on :3000
#
# With no provider key set, the gateway answers completions from a mock
# provider. Every other stage of the pipeline still runs, so a refusal is a
# real refusal. Export OPENAI_API_KEY or ANTHROPIC_API_KEY to use a real one.
#
# The canonical command set lives in docs/QUICKSTART-COMMANDS.md.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
DEMO_DIR="${SCRIPT_DIR}/demos/00-quickstart"

DEMO_KEY="aegis-demo-quickstart"
DEMO_MODEL="aegis-fast"
PG_CONTAINER="aegis-demo-postgres"

# AWS's own documentation example key. It is not a live credential, and it
# matches the AKIA[0-9A-Z]{16} pattern in internal/filter/secrets/patterns.go,
# so the secrets filter blocks any request carrying it.
CANARY_KEY="AKIAIOSFODNN7EXAMPLE"

export GATEWAY_PORT="${GATEWAY_HOST_PORT:-${GATEWAY_PORT:-8080}}"
export WEBUI_PORT="${WEBUI_HOST_PORT:-${WEBUI_PORT:-3000}}"
export METRICS_PORT="${METRICS_HOST_PORT:-${METRICS_PORT:-9090}}"

BASE_URL="http://localhost:${GATEWAY_PORT}"

# ── Arguments ────────────────────────────────────────────────────

COMMAND="up"
BUILD_FROM_SOURCE=false
WITH_WEBUI=false

usage() {
  sed -n '3,15p' "$0" | sed 's/^# \{0,1\}//'
}

while [ $# -gt 0 ]; do
  case "$1" in
    up|verify|down)   COMMAND="$1" ;;
    --build)          BUILD_FROM_SOURCE=true ;;
    --with-webui)     WITH_WEBUI=true ;;
    -h|--help|help)   usage; exit 0 ;;
    *)
      echo "unknown argument: $1" >&2
      echo >&2
      usage >&2
      exit 2
      ;;
  esac
  shift
done

# ── Compose invocation ───────────────────────────────────────────

compose_files=(-f docker-compose.yaml)
if [ "$BUILD_FROM_SOURCE" = true ]; then
  compose_files+=(-f docker-compose.build.yaml)
fi

compose_profiles=()
if [ "$WITH_WEBUI" = true ]; then
  compose_profiles=(--profile webui)
fi

compose() {
  # ${arr[@]+"${arr[@]}"} rather than "${arr[@]}": under `set -u`, bash 3.2 (the
  # bash macOS ships) treats an empty array expansion as an unbound variable.
  (cd "$DEMO_DIR" && docker compose "${compose_files[@]}" \
    ${compose_profiles[@]+"${compose_profiles[@]}"} "$@")
}

# ── down ─────────────────────────────────────────────────────────

if [ "$COMMAND" = "down" ]; then
  # --profile webui so the WebUI container and its volume are removed too,
  # whether or not this invocation started it. Without it, a stack brought up
  # with --with-webui leaves that container running after a plain `down`.
  (cd "$DEMO_DIR" && docker compose -f docker-compose.yaml --profile webui down -v --remove-orphans)
  echo "Stopped. Volumes deleted."
  exit 0
fi

# ── Provider selection ───────────────────────────────────────────

# Keys may already be in the shell, or in a .env left by an earlier run.
if [ -f "${DEMO_DIR}/.env" ]; then
  # shellcheck disable=SC1091
  set -a && . "${DEMO_DIR}/.env" && set +a
elif [ -f "${SCRIPT_DIR}/.env" ]; then
  cp "${SCRIPT_DIR}/.env" "${DEMO_DIR}/.env"
  # shellcheck disable=SC1091
  set -a && . "${DEMO_DIR}/.env" && set +a
fi

PROVIDER_NOTICE=""
if [ -n "${OPENAI_API_KEY:-}" ] || [ -n "${ANTHROPIC_API_KEY:-}" ]; then
  export AEGIS_MOCK_PROVIDER=""
  PROVIDER_NOTICE="Using a real provider: a key was found in the environment."
else
  export AEGIS_MOCK_PROVIDER="true"
  PROVIDER_NOTICE="Running against a mock provider. No request will reach a real one. Set OPENAI_API_KEY or ANTHROPIC_API_KEY to use a real provider."
fi

# The gateway and keygen both refuse to start without a pepper of at least 32
# characters. Generated per machine rather than committed, and persisted in
# .env so restarts keep the same value.
if [ -z "${AEGIS_KEY_PEPPER:-}" ]; then
  "${SCRIPT_DIR}/demos/shared/ensure-pepper.sh" "${DEMO_DIR}/.env"
  # shellcheck disable=SC1091
  set -a && . "${DEMO_DIR}/.env" && set +a
fi
export AEGIS_KEY_PEPPER

# ── verify ───────────────────────────────────────────────────────
#
# The evidence sequence, printed step by step. Non-zero exit if the canary is
# found anywhere in the database.

hr() { printf '%s\n' "----------------------------------------------------------------"; }

# Pretty-print when jq is available, raw otherwise. The output of this command
# gets screen-recorded, so a wall of one-line JSON is worth avoiding, but jq is
# not worth requiring.
show_json() {
  local filter="${2:-.}"
  if command -v jq >/dev/null 2>&1; then
    jq "$filter" "$1" 2>/dev/null || cat "$1"
  else
    cat "$1"
  fi
}

step() {
  echo
  hr
  echo "  $1"
  hr
}

if [ "$COMMAND" = "verify" ]; then
  if ! curl -sf "${BASE_URL}/aegis/v1/health" >/dev/null 2>&1; then
    echo "No gateway at ${BASE_URL}. Start one first:" >&2
    echo "  ./quickstart.sh" >&2
    exit 1
  fi

  # Unique per run so step 3 finds this request's row and not an older one.
  request_id="req_verify_$(date +%s)_$$"

  echo
  echo "AEGIS verification sequence"
  echo "Gateway ${BASE_URL}, model ${DEMO_MODEL}, key ${DEMO_KEY}"
  mock=$(curl -s "${BASE_URL}/aegis/v1/health" | grep -o '"mock_provider":[a-z]*' || true)
  case "$mock" in
    *true)  echo "Provider: mock. No request leaves this machine." ;;
    *false) echo "Provider: real, as configured in configs/providers.yaml." ;;
  esac

  step "STEP 1  A benign request is permitted"
  benign_status=$(curl -s -o /tmp/aegis-verify-benign.json -w '%{http_code}' \
    "${BASE_URL}/v1/chat/completions" \
    -H "Authorization: Bearer ${DEMO_KEY}" \
    -H "Content-Type: application/json" \
    -d '{"model":"'"${DEMO_MODEL}"'","messages":[{"role":"user","content":"Hello from AEGIS!"}]}')
  echo "HTTP ${benign_status}"
  # filter_actions is dropped from the display only. Every filter ran; the
  # permitted path simply has nothing to report from any of them, and eight
  # empty objects on screen obscure the fields that matter here.
  show_json /tmp/aegis-verify-benign.json 'del(.filter_actions)' 
  echo
  if [ "$benign_status" != "200" ]; then
    echo "NOTE: the permitted request did not return 200. With a real provider key"
    echo "this usually means the provider rejected the credential. It does not"
    echo "affect steps 2 to 4, which never reach a provider."
    echo
  fi

  step "STEP 2  A request carrying a credential is refused"
  echo "Sending: \"My AWS key is ${CANARY_KEY}\""
  echo
  deny_status=$(curl -s -o /tmp/aegis-verify-deny.json -w '%{http_code}' \
    "${BASE_URL}/v1/chat/completions" \
    -H "Authorization: Bearer ${DEMO_KEY}" \
    -H "Content-Type: application/json" \
    -H "X-Request-ID: ${request_id}" \
    -d '{"model":"'"${DEMO_MODEL}"'","messages":[{"role":"user","content":"My AWS key is '"${CANARY_KEY}"'"}]}')
  echo "HTTP ${deny_status}"
  show_json /tmp/aegis-verify-deny.json
  echo

  if [ "$deny_status" != "451" ]; then
    echo
    echo "FAILED: expected HTTP 451 from the secrets filter, got ${deny_status}." >&2
    echo "The request did not traverse the filter and audit path, so nothing below proves anything." >&2
    exit 1
  fi

  step "STEP 3  The refusal was written to the audit trail"
  # The audit write is asynchronous, so poll rather than sleep a fixed interval.
  audit_row=""
  for _ in $(seq 1 25); do
    audit_row=$(docker exec "${PG_CONTAINER}" psql -U aegis -d aegis -x -P pager=off -c \
      "SELECT request_id, timestamp, event_type, status_code, filter_type,
              reason, organization_id, ip_address
         FROM audit_events WHERE request_id = '${request_id}';" 2>/dev/null \
      | grep -v '^(0 rows)' || true)
    # psql prints the header even for an empty result, so match on the row
    # marker rather than on the output being non-empty.
    case "$audit_row" in *"RECORD 1"*) ;; *) audit_row="" ;; esac
    [ -n "$audit_row" ] && break
    sleep 0.2
  done

  if [ -z "$audit_row" ]; then
    echo "FAILED: no audit row for ${request_id}." >&2
    echo "Without a confirmed audit write, the canary being absent below proves nothing." >&2
    exit 1
  fi
  echo "$audit_row"

  step "STEP 4  The credential is nowhere in the database"
  echo "\$ pg_dump ... | grep -c ${CANARY_KEY}"
  echo
  # grep -c exits 1 when the count is zero, and zero is the outcome this whole
  # sequence exists to produce. `|| true` keeps set -e from treating the good
  # result as a failure; the count itself is what is checked below.
  hits=$(docker exec "${PG_CONTAINER}" pg_dump -U aegis aegis 2>/dev/null | grep -c "${CANARY_KEY}" || true)
  echo "${hits}"

  step "SUMMARY"
  echo "  Written:     one audit row for ${request_id}, recording that a request"
  echo "               was refused, which filter refused it, and when."
  echo "  Not written: the request text, the credential it carried, and the"
  echo "               response. ${hits} occurrences of ${CANARY_KEY} in a full"
  echo "               dump of the database."
  echo

  if [ "$hits" != "0" ]; then
    echo "FAILED: the credential was found ${hits} time(s) in the database." >&2
    exit 1
  fi
  echo "PASS"
  exit 0
fi

# ── up ───────────────────────────────────────────────────────────

echo "${PROVIDER_NOTICE}"
echo

if [ "$BUILD_FROM_SOURCE" = true ]; then
  echo "Building the gateway from this working tree…"
  compose up -d --build
else
  echo "Starting AEGIS from the published image…"
  # A published image is the default so a first run needs no Go toolchain. If it
  # cannot be pulled, build instead rather than failing: an image that is
  # unreachable is not a reason a reader cannot see the gateway run.
  #
  # Compose output is captured rather than streamed, so a failed pull produces
  # one explanatory line instead of a registry error the reader has to decide
  # whether to worry about. It is printed in full only if the fallback also
  # fails, which is the case where it is worth reading.
  up_log=$(mktemp)
  if compose up -d >"$up_log" 2>&1; then
    cat "$up_log"
  else
    echo
    echo "Could not pull ${AEGIS_IMAGE:-ghcr.io/aegis-gateway/aegis-ai-gateway:v0.1.0}."
    echo "Building from this working tree instead. Pass --build to skip the pull next time."
    echo
    compose_files+=(-f docker-compose.build.yaml)
    if ! compose up -d --build; then
      echo
      echo "The build failed too. The pull failure was:" >&2
      sed 's/^/  /' "$up_log" >&2
      exit 1
    fi
  fi
fi

"${SCRIPT_DIR}/demos/shared/wait-for-gateway.sh" "${BASE_URL}"

cat <<EOF

================================================================
  AEGIS is running
================================================================

  ${PROVIDER_NOTICE}

  Gateway:  ${BASE_URL}
  Metrics:  http://localhost:${METRICS_PORT}/metrics
  Demo key: ${DEMO_KEY}

  See the whole thing, start to finish:

    ./quickstart.sh verify

  Or one step at a time:

    # A benign request is permitted
    curl ${BASE_URL}/v1/chat/completions \\
      -H 'Authorization: Bearer ${DEMO_KEY}' \\
      -H 'Content-Type: application/json' \\
      -d '{"model":"${DEMO_MODEL}","messages":[{"role":"user","content":"Hello!"}]}'

    # A request carrying a credential is refused with 451
    curl ${BASE_URL}/v1/chat/completions \\
      -H 'Authorization: Bearer ${DEMO_KEY}' \\
      -H 'Content-Type: application/json' \\
      -d '{"model":"${DEMO_MODEL}","messages":[{"role":"user","content":"My AWS key is ${CANARY_KEY}"}]}'

    # The credential is in no row of the database
    docker exec ${PG_CONTAINER} pg_dump -U aegis aegis | grep -c ${CANARY_KEY}

  Full command set: docs/QUICKSTART-COMMANDS.md
EOF

if [ "$WITH_WEBUI" = true ]; then
  cat <<EOF

  Chat UI:  http://localhost:${WEBUI_PORT}  (create an account on first visit)
EOF
else
  cat <<EOF

  Want a chat interface? ./quickstart.sh --with-webui adds Open WebUI on :${WEBUI_PORT}.
EOF
fi

cat <<EOF

  Stop: ./quickstart.sh down

EOF

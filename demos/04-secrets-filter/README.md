# 04 — Secrets Filter

Demonstrates the built-in secrets scanner that blocks requests containing credentials before they reach any LLM provider.

## What it shows

| Test case | Pattern blocked | Expected result |
|-----------|----------------|-----------------|
| AWS access key | `AKIA…` | HTTP 451 blocked |
| GitHub token | `ghp_…` | HTTP 451 blocked |
| RSA private key | `-----BEGIN RSA PRIVATE KEY-----` | HTTP 451 blocked |
| JWT | three base64url segments | HTTP 451 blocked |
| Clean message | `Hello from AEGIS!` | HTTP 200 passes |

Blocked requests return **451 Unavailable For Legal Reasons**, which is what
`httputil.WriteContentBlockedError` emits for every filter and policy block.

The sample credentials are assembled at runtime inside `run.sh` rather than
written out literally, so the repository never contains a token-shaped string
for a secret scanner to flag.

Every blocked request is logged to the `audit_events` table so you can audit credential leak attempts.

## Prerequisites

- Docker Compose v2
- No provider API key. Four of the five cases never reach a provider, and the fifth is answered by the mock provider when no key is set
- This demo starts its own stack under the same container names as the quickstart. Stop that one first: `./quickstart.sh down`

## How to run

```bash
cd demos/04-secrets-filter
cp ../shared/.env.example .env
# Fill in at least one of OPENAI_API_KEY or ANTHROPIC_API_KEY
./run.sh
```

`run.sh` starts the full stack, runs all five test cases, prints pass/fail for each, queries `audit_events`, and tears everything down.

## Expected output

```
  [1/5] AWS key blocked                    → PASS (HTTP 451)
  [2/5] GitHub token blocked               → PASS (HTTP 451)
  [3/5] Private key blocked                → PASS (HTTP 451)
  [4/5] JWT blocked                        → PASS (HTTP 451)
  [5/5] Clean request passes               → PASS (HTTP 200)

Results: 5 passed, 0 failed

Recent audit_events (blocked requests):
         timestamp          |  event_type  | filter  |                      reason
----------------------------+--------------+---------+---------------------------------------------------
 2026-08-20 14:31:22.108+00 | filter_block | secrets | Request blocked: detected 1 secret(s) of type: ...
(4 rows)
```

## How to see blocked requests in audit_events

While the stack is running:

```bash
docker compose exec -T postgres psql -U aegis -d aegis -c \
  "SELECT timestamp, event_type, filter_type AS filter, reason
     FROM audit_events
    WHERE event_type = 'filter_block'
    ORDER BY timestamp DESC
    LIMIT 10;"
```

`audit_events` stores `event_type`, `timestamp`, `error_message`, and a JSONB
`metadata` column — there is no `action`, `reason`, or `created_at` column. The
detector that fired is the `filter_type` column, and the full block message is the
`reason` column. Both were keys in a `metadata` JSONB column until migration 013
promoted them, along with the other ten, so that the audit table has no untyped
column left.

## How the secrets filter works

The filter runs inside the gateway process before any request is forwarded to a provider. It scans every message in the `messages` array using compiled regex patterns for:

- AWS access key IDs (`AKIA[0-9A-Z]{16}`)
- GitHub tokens, all prefixes (`gh[pousr]_[A-Za-z0-9_]{36,}`)
- GCP service account keys (`"private_key":\s*"-----BEGIN`)
- Stripe live secret keys (`sk_live_[A-Za-z0-9]{24,}`)
- PEM private key headers (`-----BEGIN (RSA |EC |DSA )?PRIVATE KEY-----`)
- Database connection strings (`postgres|mysql|mongodb|redis://…`)
- JSON Web Tokens (three base64url segments separated by dots starting with `eyJ`)

A single match in any message triggers an immediate 451 response. The filter chain
runs before routing, so no data is forwarded to any provider.

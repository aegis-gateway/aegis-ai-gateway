# 04 — Secrets Filter

Demonstrates the built-in secrets scanner that blocks requests containing credentials before they reach any LLM provider.

## What it shows

| Test case | Pattern blocked | Expected result |
|-----------|----------------|-----------------|
| AWS access key | `AKIAIOSFODNN7EXAMPLE` | HTTP 400 blocked |
| GitHub token | `ghp_AAAA…` | HTTP 400 blocked |
| RSA private key | `-----BEGIN RSA PRIVATE KEY-----` | HTTP 400 blocked |
| JWT | `eyJhbGciOiJIUzI1NiJ9.…` | HTTP 400 blocked |
| Clean message | `Hello from AEGIS!` | HTTP 200 passes |

Every blocked request is logged to the `audit_events` table so you can audit credential leak attempts.

## Prerequisites

- Docker Compose v2
- At least one provider API key (OpenAI or Anthropic)

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
[1/5] AWS key blocked          → PASS (HTTP 400)
[2/5] GitHub token blocked     → PASS (HTTP 400)
[3/5] Private key blocked      → PASS (HTTP 400)
[4/5] JWT blocked              → PASS (HTTP 400)
[5/5] Clean request passes     → PASS (HTTP 200)

Recent audit_events:
 action  |        reason
---------+----------------------
 block   | secrets: AWS key
 block   | secrets: GitHub token
 block   | secrets: private key
 block   | secrets: JWT
(4 rows)
```

## How to see blocked requests in audit_events

While the stack is running:

```bash
docker compose exec postgres psql -U aegis -d aegis \
  -c "SELECT action, reason FROM audit_events ORDER BY created_at DESC LIMIT 10;"
```

The `reason` column names the detector that fired (`secrets: AWS key`, `secrets: GitHub token`, etc.).

## How the secrets filter works

The filter runs inside the gateway process before any request is forwarded to a provider. It scans every message in the `messages` array using compiled regex patterns for:

- AWS access key IDs (`AKIA[0-9A-Z]{16}`)
- GitHub personal access tokens (`ghp_[A-Za-z0-9]{36}`)
- PEM private key headers (`-----BEGIN (RSA |EC |DSA )?PRIVATE KEY-----`)
- JSON Web Tokens (three base64url segments separated by dots starting with `eyJ`)

A single match in any message triggers an immediate 400 response. No data is forwarded to the provider.

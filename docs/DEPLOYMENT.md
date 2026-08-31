# Deployment Guide

AEGIS AI Gateway is distributed as a Docker image. This guide covers deployment options from local Docker Compose to production.

---

## Requirements

| Component | Minimum | Recommended |
|-----------|---------|-------------|
| PostgreSQL | 15 | 16 |
| Redis | 6 | 7 |
| RAM (gateway) | 256 MB | 512 MB |
| RAM (filter service) | 512 MB | 1 GB |
| CPU | 1 vCPU | 2+ vCPU |

---

## Option 1: Docker Compose (Self-Hosted)

The fastest path to a running instance.

### 1. Configure Environment

```bash
cp .env.example .env.production
```

Edit `.env.production` — required values:

```env
# At least one provider key is required
OPENAI_API_KEY=sk-proj-...
ANTHROPIC_API_KEY=sk-ant-...

# Generate strong secrets — never use defaults in production
POSTGRES_PASSWORD=<strong-random-password>
REDIS_PASSWORD=<strong-random-password>
WEBUI_SECRET_KEY=<strong-random-secret>
```

### 2. Start Services

```bash
docker compose -f deploy/docker-compose.yaml --env-file .env.production up -d
```

### 3. Run Migrations

```bash
docker compose -f deploy/docker-compose.yaml exec gateway ./migrate up
```

### 4. Generate Your First API Key

```bash
docker compose -f deploy/docker-compose.yaml exec gateway ./keygen \
  -org acme -team platform -name "first key" \
  -allowed-models aegis-fast,aegis-balanced
# Save the displayed key — it is shown only once
```

`-allowed-models` is required. It names the aliases from `configs/models.yaml` the key may
use, and is validated against that file. Pass `-allowed-models=any` for an unrestricted key:
an empty allowlist means **no restriction** rather than no access, so granting everything is
something you type rather than something that happens by omission.

### 5. Verify

```bash
curl http://localhost:8080/aegis/v1/health
# → {"status":"ok"}
```

---

## Option 2: Docker (Standalone)

If you manage PostgreSQL and Redis separately:

```bash
docker run -d \
  --name aegis-gateway \
  -p 8080:8080 \
  -p 9090:9090 \
  -e DB_HOST=<host> -e DB_PORT=5432 -e DB_USER=aegis -e DB_PASSWORD=<password> -e DB_NAME=aegis \
  -e REDIS_URL=redis://:<password>@<host>:6379 \
  -e OPENAI_API_KEY=sk-proj-... \
  -v $(pwd)/configs:/app/configs:ro \
  ghcr.io/aegis-gateway/aegis-ai-gateway:latest
```

---

## Configuration

AEGIS reads configuration from two sources, in order of precedence:

1. **Environment variables** (highest priority)
2. **YAML files** in `configs/` (gateway.yaml, models.yaml, providers.yaml)

### Key Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASSWORD`, `DB_NAME` | PostgreSQL connection settings. The gateway does **not** read `DATABASE_URL` — only the `keygen` and `migrate` CLIs do. Note that `DSN()` hardcodes `sslmode=disable`. | see `configs/gateway.yaml` |
| `REDIS_URL` | Redis connection string | — |
| `REDIS_PASSWORD` | Redis password (if not in URL) | — |
| `OPENAI_API_KEY` | OpenAI provider key | — |
| `ANTHROPIC_API_KEY` | Anthropic provider key | — |
| `AZURE_OPENAI_API_KEY` | Azure OpenAI key | — |
| `AZURE_OPENAI_ENDPOINT` | Azure OpenAI endpoint URL | — |
| `GATEWAY_PORT` | HTTP listen port | `8080` |
| `METRICS_PORT` | Prometheus metrics port | `9090` |
| `LOG_LEVEL` | `debug` / `info` / `warn` / `error` | `info` |
| `OPA_POLICY_BUNDLE_PATH` | Path to OPA policy bundle | `./policies` |

See `.env.example` for a complete list.

### YAML Configuration

- **`configs/gateway.yaml`** — server settings, timeouts, caching
- **`configs/models.yaml`** — model aliases, routing chains, classification levels
- **`configs/providers.yaml`** — provider endpoints, capabilities, pricing

YAML files support hot-reload — changes apply without restarting the gateway.

---

## Production Security Checklist

- [ ] All default passwords replaced with strong secrets (`POSTGRES_PASSWORD`, `REDIS_PASSWORD`, `WEBUI_SECRET_KEY`)
- [ ] Remove or rotate the demo API key (`aegis-demo-quickstart`)
- [ ] PostgreSQL and Redis not exposed to the public internet
- [ ] TLS termination at load balancer or reverse proxy (nginx, Caddy, Traefik)
- [ ] `LOG_LEVEL=info` (not `debug`) in production
- [ ] OPA policies reviewed and scoped to your classification requirements
- [ ] Regular database backups configured

---

## Reverse Proxy (TLS)

### Nginx example

```nginx
server {
    listen 443 ssl;
    server_name gateway.yourdomain.com;

    ssl_certificate     /etc/ssl/certs/gateway.crt;
    ssl_certificate_key /etc/ssl/private/gateway.key;

    location / {
        proxy_pass         http://localhost:8080;
        proxy_set_header   Host $host;
        proxy_set_header   X-Real-IP $remote_addr;
        proxy_set_header   X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header   X-Forwarded-Proto $scheme;

        # Required for SSE streaming
        proxy_buffering    off;
        proxy_cache        off;
        proxy_read_timeout 300s;
    }
}
```

---

## Monitoring

Prometheus metrics are available at `:9090/metrics`.

Key metrics:

| Metric | Description |
|--------|-------------|
| `aegis_requests_total` | Request count by model, provider, status |
| `aegis_request_duration_seconds` | Latency histogram |
| `aegis_tokens_total` | Token usage by direction (prompt/completion) |
| `aegis_estimated_cost_usd_total` | Cumulative cost by model/provider |
| `aegis_provider_errors_total` | Provider-level error counts |

---

## Upgrading

```bash
docker compose -f deploy/docker-compose.yaml pull
docker compose -f deploy/docker-compose.yaml up -d
docker compose -f deploy/docker-compose.yaml exec gateway ./migrate up
```

---

## Audit log retention and purge

AEGIS accumulates rows in `audit_events` indefinitely until an operator explicitly
purges old data. The `aegis-migrate purge` subcommand
provides a safe, auditable way to delete rows outside the retention window.

### Retention policy

Configure the minimum retention window in `configs/gateway.yaml`:

```yaml
audit:
  retention_days: 365  # six-month floor; adjust to meet your compliance requirements
```

### Purge subcommand

```
aegis-migrate purge [flags]

Flags:
  --before DATE      (required) delete rows with created_at < DATE (ISO 8601)
  --dry-run          print counts and ID ranges without deleting; writes a
                     dry_run=true row to audit_purges for traceability
  --table TABLE      audit_events | both (default: both). audit_logs was dropped
                     by migration 017; naming it is refused with an explanation
  --db-url URL       database URL (overrides DATABASE_URL env)
```

Every purge run — including dry runs — writes a row to `audit_purges`. The
`audit_purges` table is never itself purged; it provides a permanent record
that a purge occurred.

### Recommended seal → purge → seal sequence

For full attestation integrity, run `aegis-migrate seal` before and after a purge:

```bash
# 1. Seal all outstanding events up to the purge boundary.
aegis-migrate seal

# 2. Dry-run first to verify what will be deleted.
aegis-migrate purge --before 2024-01-01 --dry-run

# 3. Execute the purge.
aegis-migrate purge --before 2024-01-01

# 4. Seal again to create a checkpoint that references post-purge state.
aegis-migrate seal
```

Purging events that lie beyond the last checkpoint's `range_end` is allowed but
triggers a warning:

```
Warning: unsealed events in purge window (N rows beyond last checkpoint).
Run 'aegis-migrate seal' first for full attestation.
```

Heed this warning in compliance-sensitive deployments.

### Monitoring purge health

The `aegis_audit_oldest_event_age_days` Prometheus gauge is updated every 5 minutes
by the gateway. Alert when this value exceeds `retention_days` to detect a missed purge:

```promql
aegis_audit_oldest_event_age_days > 400  # alert if oldest event is > 400 days
```

Always check [CHANGELOG.md](../CHANGELOG.md) before upgrading between minor versions.

## Reconciling recorded spend

`usage_records.estimated_cost_usd` is what the gateway computed at the time of
the request. When the pricing config is corrected, or a cost defect is fixed,
the rows already written keep the old number — and every spend aggregate, budget
decision and invoice check is computed from those rows.

`reconcile-usage` reprices historical rows against the current pricing config
and reports the difference.

```
go run ./cmd/reconcile-usage [flags]

Flags:
  -database-url URL            Postgres connection (default: $DATABASE_URL)
  -pricing PATH                pricing.yaml to reprice against (default: configs/pricing.yaml)
  -since / -until RFC3339      restrict to a time window
  -detail-recorded-after TIME  when migration 014 was deployed (see below)
  -group-by day|org|team|model|provider
  -limit N                     stop after N rows
  -apply                       write corrected costs (requires -detail-recorded-after)
```

### What can and cannot be recovered

A row's cost depends on how its prompt tokens split between uncached input,
cache reads and cache writes, because those are priced differently — a read at
0.1x base input, a five-minute write at 1.25x, a one-hour write at 2x.

Migration 014 added `cached_tokens`, `cache_write_5m_tokens` and
`cache_write_1h_tokens`. **Rows written from that migration onwards carry the
inputs their cost depends on, so they reprice exactly.** Rows written before it
do not, and no amount of arithmetic recovers the split.

`-detail-recorded-after` is the deployment time of migration 014 on the database
being reconciled. Rows at or after it are repriced exactly; everything earlier is
reported as a **range**: the cheapest the request could have been (whole prompt a
cache read) and the dearest (whole prompt written to a one-hour entry). Without
the flag every row is treated as bounds-only, which is why `-apply` refuses to
run without it.

A recorded cost falling outside its range is wrong regardless of what the cache
split was. That is the strongest statement the surviving data supports about the
historical rows, and it is how the exposure from the pre-014 cost defects should
be sized.

Rows that recorded no tokens at all are reported separately rather than repriced.
Repricing zero tokens yields zero, which would agree with the recorded cost and
mark the row correct; in fact it is the fingerprint of the streamed-usage defect,
where the counts needed to reprice were never captured.

### Applying corrections

`-apply` rewrites `estimated_cost_usd` only on rows that reprice exactly, in a
single transaction. Bounded rows are never rewritten: writing a bound would turn
"this cannot be determined" into a number that later readers would treat as
measured. Re-running after an apply is a no-op.

```bash
# 1. Look first. Nothing is written without -apply.
go run ./cmd/reconcile-usage -detail-recorded-after 2026-08-29T12:00:00Z -group-by day

# 2. Apply once the delta looks like what you expect.
go run ./cmd/reconcile-usage -detail-recorded-after 2026-08-29T12:00:00Z -apply
```

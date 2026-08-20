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
docker compose -f deploy/docker-compose.yaml exec gateway ./keygen
# Save the displayed key — it is shown only once
```

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

Always check [CHANGELOG.md](../CHANGELOG.md) before upgrading between minor versions.

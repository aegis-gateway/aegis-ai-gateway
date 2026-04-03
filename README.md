# AEGIS AI Gateway

AI Enablement, Governance & Innovation System — a unified gateway that proxies requests to multiple AI providers (OpenAI, Anthropic, Azure, vLLM) with authentication, content filtering, classification gating, and observability.

## Quick Demo

Requires only **Docker Desktop** and one provider API key. Includes [Open WebUI](https://github.com/open-webui/open-webui) for a full chat interface.

```bash
export OPENAI_API_KEY=sk-proj-...   # or export ANTHROPIC_API_KEY=sk-ant-...
./quickstart.sh                     # builds, migrates, starts — prints when ready
```

Or use a `.env` file: `cp .env.example .env` and fill in the keys.

Then open **http://localhost:3000** — create an account and start chatting. Every request flows through the AEGIS gateway with cost tracking, secrets filtering, and audit logging.

Or use curl directly:

```bash
# Chat completion
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer aegis-demo-quickstart" \
  -H "Content-Type: application/json" \
  -d '{"model":"aegis-fast","messages":[{"role":"user","content":"Hello from AEGIS!"}]}' | jq

# Secrets filter — blocked before reaching the provider
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer aegis-demo-quickstart" \
  -H "Content-Type: application/json" \
  -d '{"model":"aegis-fast","messages":[{"role":"user","content":"My AWS key is AKIAIOSFODNN7EXAMPLE"}]}' | jq
```

**What to demo:**
- Pick any model in the UI (aegis-fast, aegis-gpt4, aegis-reasoning) — each routes to a different provider
- Try pasting an AWS key like `AKIAIOSFODNN7EXAMPLE` in a message — the gateway blocks it
- Check cost tracking: `docker exec aegis-postgres psql -U aegis -d aegis -c "SELECT model_served, SUM(estimated_cost_usd) FROM usage_records GROUP BY model_served;"`
- View Prometheus metrics: http://localhost:9090/metrics

Stop with `cd demos/00-quickstart && docker compose down -v`.

> **Port conflicts?** `GATEWAY_PORT=8088 WEBUI_PORT=3001 ./quickstart.sh`

See [demos/](demos/) for more examples (curl basics, streaming, cost tracking, secrets filter).

### Available Models

| Alias | Routes to | Classification |
|-------|-----------|----------------|
| `aegis-fast` | Claude Haiku → GPT-4o-mini | INTERNAL |
| `aegis-gpt4` | GPT-4o → Azure GPT-4o → Claude Sonnet | CONFIDENTIAL |
| `aegis-reasoning` | Claude Opus → o3 | CONFIDENTIAL |

## Development Setup

For local development without Docker for the gateway itself.

### Prerequisites

- [mise](https://mise.jdx.dev) — `brew install mise` or `curl https://mise.run | sh`
- Docker Desktop (for PostgreSQL and Redis)

### Setup

```bash
# Install tools (Go, golangci-lint) and dependencies
mise install
mise run setup

# Copy env template and add your provider API keys
cp .env.example .env
# Edit .env → set OPENAI_API_KEY and/or ANTHROPIC_API_KEY
```

### Run

```bash
# Start everything: Postgres, Redis, migrations, then the gateway
mise run dev
```

The gateway starts on `:8080` and Prometheus metrics on `:9090`.

### Generate an API Key

```bash
mise run keygen
# Save the displayed key — it's shown only once
```

### Test

```bash
# Health check (no auth required)
curl http://localhost:8080/aegis/v1/health

# Chat completion (replace <key> with your generated key)
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer <key>" \
  -H "Content-Type: application/json" \
  -d '{"model":"aegis-fast","messages":[{"role":"user","content":"Hello"}]}'
```

## Development

### Available Tasks

```bash
mise tasks ls          # list all tasks
```

| Task | Description |
|------|-------------|
| `mise run setup` | Install Go dependencies |
| `mise run services:up` | Start PostgreSQL + Redis in Docker |
| `mise run services:down` | Stop services (preserves data) |
| `mise run services:destroy` | Stop services and delete volumes |
| `mise run db:migrate` | Run database migrations up |
| `mise run db:reset` | Drop, recreate, and migrate database |
| `mise run build` | Compile binaries to `bin/` |
| `mise run test` | Unit tests with race detection |
| `mise run test:integration` | Integration tests (auto-starts services) |
| `mise run lint` | Run golangci-lint |
| `mise run fmt` | Format Go source files |
| `mise run dev` | Full stack: services + migrations + gateway |
| `mise run run` | Start gateway only (services must be running) |
| `mise run keygen` | Generate a dev API key |

### Environment

mise auto-sets database and Redis connection vars. Provider API keys go in `.env` (gitignored):

```bash
OPENAI_API_KEY=sk-...
ANTHROPIC_API_KEY=sk-ant-...
```

### Architecture

```
cmd/
  gateway/     Main API server
  keygen/      API key generation CLI
  migrate/     Database migration runner
internal/
  auth/        API key auth middleware + Redis caching
  config/      YAML config with hot-reload (fsnotify)
  filter/      Content filtering (secrets scanner)
  gateway/     Request handler + SSE streaming
  httputil/    OpenAI-compatible error responses
  router/      Provider registry + classification gating
    adapters/  OpenAI, Anthropic, Azure, vLLM adapters
  telemetry/   Prometheus metrics
  types/       Shared types (classification, request/response)
configs/       YAML configuration (gateway, models, providers)
deploy/        Docker Compose for local services
migrations/    PostgreSQL migrations
```

### API Endpoints

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/aegis/v1/health` | No | Health check |
| POST | `/v1/chat/completions` | Yes | Chat completions (OpenAI-compatible) |
| GET | `/v1/models` | Yes | List available models |

### Key Features

- **Multi-provider routing** with fallback chains and classification gating
- **OpenAI-compatible API** — drop-in replacement for OpenAI SDK
- **Streaming SSE** with transparent Anthropic-to-OpenAI format conversion
- **Secrets scanning** — blocks AWS keys, GitHub tokens, private keys, JWTs, and more
- **Prometheus metrics** — request counts, latency histograms, token usage, cost tracking
- **Config hot-reload** — update models/providers without restarting
- **Two-tier auth caching** — Redis + PostgreSQL

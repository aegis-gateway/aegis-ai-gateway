# AEGIS AI Gateway

AI Enablement, Governance & Innovation System — a unified gateway that proxies requests to multiple AI providers (OpenAI, Anthropic, Azure, vLLM) with authentication, content filtering, classification gating, and observability.

## Quick Start

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

# AEGIS AI Gateway — Demos

Self-contained, runnable demos that showcase gateway features. Each demo lives in its own directory with a `run.sh` and a `docker-compose.yaml`.

## Prerequisites

- Docker Desktop
- At least one provider API key (OpenAI or Anthropic)

## Demos

| # | Name | What it shows | Status |
|---|------|---------------|--------|
| [00](00-quickstart/) | **Quickstart** | Full stack with Open WebUI — multi-provider routing, secrets filter, cost tracking, metrics | Ready |
| [01](01-curl-basics/) | **curl Basics** | Step-by-step curl walkthrough of every endpoint | Ready |
| [02](02-streaming/) | **Streaming** | SSE streaming, Anthropic→OpenAI format conversion, TTFT metrics | Planned |
| [03](03-cost-tracking/) | **Cost Tracking** | Per-request cost, aggregated reports, Prometheus cost metrics | Planned |
| [04](04-secrets-filter/) | **Secrets Filter** | AWS keys, GitHub tokens, private keys, JWTs — all blocked | Planned |
| [15](15-custom-policies/) | **Custom Policies** | OPA Rego policies — competitor mentions, token budgets, topic restrictions, hot-reload | Ready |

## Quick start

If your provider keys are already exported, it just works:

```bash
export OPENAI_API_KEY=sk-proj-...   # or ANTHROPIC_API_KEY
cd demos/00-quickstart
./run.sh
```

Otherwise the script creates a `.env` file for you to fill in.

## Structure

```
demos/
  README.md              ← this file
  shared/
    .env.example         ← provider API key template (copied into each demo)
    wait-for-gateway.sh  ← health-check polling script used by all demos
  00-quickstart/
    docker-compose.yaml  ← gateway + Open WebUI + Postgres + Redis
    run.sh               ← one-command launcher
    README.md
  01-curl-basics/
    README.md            ← (planned)
  02-streaming/
    README.md            ← (planned)
  03-cost-tracking/
    README.md            ← (planned)
  04-secrets-filter/
    README.md            ← (planned)
```

## Writing a new demo

1. Create `demos/NN-slug/` with a `docker-compose.yaml`, `run.sh`, and `README.md`
2. Use `../shared/.env.example` as the env template — `run.sh` copies it on first run
3. Use `../shared/wait-for-gateway.sh` to poll for gateway readiness
4. Build the gateway from repo root: `build: { context: ../.. , dockerfile: Dockerfile }`
5. Use container names prefixed with `aegis-demo-` to avoid collisions with dev services
6. Add an entry to the table above

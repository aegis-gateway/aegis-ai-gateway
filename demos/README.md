# AEGIS AI Gateway — Demos

Self-contained, runnable demos that showcase gateway features. Each demo lives in its own directory with a `run.sh`. Demos that spin up additional services (databases, UI, policy servers) also include a `docker-compose.yaml`; lightweight curl/script demos do not.

## Prerequisites

- Docker Desktop
- At least one provider API key (OpenAI or Anthropic)

## Demos

| # | Name | What it shows | Status |
|---|------|---------------|--------|
| [00](00-quickstart/) | **Quickstart** | Full stack with Open WebUI — multi-provider routing, secrets filter, cost tracking, metrics | Ready |
| [01](01-curl-basics/) | **curl Basics** | Step-by-step curl walkthrough of every endpoint | Ready |
| [02](02-streaming/) | **Streaming** | SSE streaming, Anthropic→OpenAI format conversion, TTFT metrics | Ready |
| [03](03-cost-tracking/) | **Cost Tracking** | Per-request cost, aggregated reports, Prometheus cost metrics | Ready |
| [04](04-secrets-filter/) | **Secrets Filter** | AWS keys, GitHub tokens, private keys, JWTs — all blocked | Ready |
| [05](05-custom-policies/) | **Custom Policies** | OPA Rego policies — competitor mentions, token budgets, topic restrictions, hot-reload | Ready |

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
    run.sh
    README.md
  02-streaming/
    run.sh
    README.md
  03-cost-tracking/
    run.sh
    README.md
  04-secrets-filter/
    docker-compose.yaml
    run.sh
    README.md
  05-custom-policies/
    docker-compose.yaml
    run.sh
    policies/            ← Rego policy files (hot-reloaded)
    README.md
```

## Writing a new demo

1. Create `demos/NN-slug/` with a `run.sh` and `README.md`; add a `docker-compose.yaml` only if the demo requires additional services
2. Use `../shared/.env.example` as the env template — `run.sh` copies it on first run
3. Use `../shared/wait-for-gateway.sh` to poll for gateway readiness
4. Build the gateway from repo root: `build: { context: ../.. , dockerfile: Dockerfile }`
5. Use container names prefixed with `aegis-demo-` to avoid collisions with dev services
6. Add an entry to the table above

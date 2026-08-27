# AEGIS AI Gateway — Demos

Self-contained, runnable demos that showcase gateway features. Each demo lives in its own directory, and each except `00-quickstart` has a `run.sh`; the quickstart is driven by `./quickstart.sh` at the repo root instead. Demos that spin up additional services (databases, UI, policy servers) also include a `docker-compose.yaml`; lightweight curl and script demos do not.

## Prerequisites

- Docker Desktop
- No provider API key. Without one the gateway answers completions from a mock provider and every other stage of the pipeline still runs. Export `OPENAI_API_KEY` or `ANTHROPIC_API_KEY` to route to a real provider.

## Demos

| # | Name | What it shows | Status |
|---|------|---------------|--------|
| [00](00-quickstart/) | **Quickstart** | The gateway, Postgres and Redis. Started by `./quickstart.sh` at the repo root, which is the one entry point. Open WebUI optional | Ready |
| [01](01-curl-basics/) | **curl Basics** | Step-by-step curl walkthrough of every endpoint | Ready |
| [02](02-streaming/) | **Streaming** | SSE streaming, Anthropic→OpenAI format conversion, TTFT metrics | Ready |
| [03](03-cost-tracking/) | **Cost Tracking** | Per-request cost, aggregated reports, Prometheus cost metrics | Ready |
| [04](04-secrets-filter/) | **Secrets Filter** | AWS keys, GitHub tokens, private keys, JWTs — all blocked | Ready |
| [05](05-custom-policies/) | **Custom Policies** | OPA Rego policies — competitor mentions, token budgets, topic restrictions, hot-reload | Ready |

## Quick start

From the repo root, with no credentials:

```bash
./quickstart.sh
./quickstart.sh verify     # the whole evidence sequence, printed step by step
```

The canonical command set is in [docs/QUICKSTART-COMMANDS.md](../docs/QUICKSTART-COMMANDS.md).

## Structure

```
demos/
  README.md              ← this file
  shared/
    .env.example         ← provider API key template (copied into each demo)
    wait-for-gateway.sh  ← health-check polling script used by all demos
  00-quickstart/
    docker-compose.yaml       ← gateway + Postgres + Redis; Open WebUI behind a profile
    docker-compose.build.yaml ← overlay for ./quickstart.sh --build
    README.md
                              (no run.sh: ../../quickstart.sh is the one entry point)
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

1. Create `demos/NN-slug/` with a `run.sh` and `README.md`; add a `docker-compose.yaml` only if the demo requires additional services. `00-quickstart` is the exception: it has no `run.sh`, because `./quickstart.sh` at the repo root is the single documented entry point
2. Use `../shared/.env.example` as the env template if the demo needs provider keys. Prefer not needing them
3. Use `../shared/wait-for-gateway.sh` to poll for gateway readiness
4. Build the gateway from repo root: `build: { context: ../.. , dockerfile: Dockerfile }`
5. Use container names prefixed with `aegis-demo-` to avoid collisions with dev services
6. Add an entry to the table above, and add any command a reader is meant to paste to [docs/QUICKSTART-COMMANDS.md](../docs/QUICKSTART-COMMANDS.md)

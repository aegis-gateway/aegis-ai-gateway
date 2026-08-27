# Contributing to AEGIS AI Gateway

Thank you for your interest in contributing. This document covers how to get involved, and doubles as the development handbook: setup, tasks, environment, and the internal layout all live here rather than in the README.

## License

By contributing to AEGIS, you agree that your contributions will be licensed under the **Apache License 2.0**, the same license that covers this repository. There is no other inbound license; contributions are not subject to any proprietary or source-available license. All contributions are also subject to the AEGIS Individual Contributor License Agreement (CLA). See [docs/cla/](docs/cla/) for details.

## Just want to see it run?

You do not need any of this. `./quickstart.sh` from the repo root starts the gateway in Docker with no credentials and no toolchain. See the [README](README.md). Everything below is for changing the code.

## Getting started

### Prerequisites

- Go 1.25 (the version in `go.mod`; `mise install` fetches it)
- Docker and Docker Compose
- [mise](https://mise.jdx.dev/), the task runner

### Setup

```bash
git clone https://github.com/aegis-gateway/aegis-ai-gateway.git
cd aegis-ai-gateway

mise install                  # Go and golangci-lint
mise run setup                # go mod download + verify
mise run services:up          # PostgreSQL + Redis + the PII filter service
mise run db:migrate           # migrations up
mise run dev                  # services, migrations, then the gateway
```

The gateway starts on `:8080` and Prometheus metrics on `:9090`.

Generate a development key:

```bash
mise run keygen               # the key is printed once and not stored anywhere
```

Smoke test:

```bash
curl http://localhost:8080/aegis/v1/health

curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer <key>" \
  -H "Content-Type: application/json" \
  -d '{"model":"aegis-fast","messages":[{"role":"user","content":"Hello"}]}'
```

### Two stacks, two sets of container names

These do not collide, and mixing up which one a command targets is the most common source of a confusing error.

| Stack | Started by | Containers | Purpose |
|---|---|---|---|
| Contributor services | `mise run services:up` (`deploy/docker-compose.yaml`) | `aegis-postgres`, `aegis-redis`, `aegis-filter-nlp` | Postgres and Redis on published ports, for a gateway you run with `go run` |
| Quickstart demo | `./quickstart.sh` (`demos/00-quickstart/`) | `aegis-demo-postgres`, `aegis-demo-redis`, `aegis-demo-gateway`, `aegis-demo-webui` | The whole thing in Docker |

Every command in [docs/QUICKSTART-COMMANDS.md](docs/QUICKSTART-COMMANDS.md) targets the demo stack. `mise run db:reset` targets the contributor stack.

### Running against the mock provider locally

`AEGIS_MOCK_PROVIDER=true` makes the gateway answer completions from `internal/router/adapters/mock.go` instead of calling out, which is useful for working on the filter, policy, or audit path without a provider key:

```bash
AEGIS_MOCK_PROVIDER=true mise run dev
```

The value must be exactly `true`. Anything else, including `1` and `TRUE`, leaves the real adapters in place. The gateway logs a warning at startup when the mock is active and reports `"mock_provider": true` on `/aegis/v1/health`.

## Tasks

```bash
mise tasks ls
```

| Task | Description |
|------|-------------|
| `mise run setup` | Install Go dependencies |
| `mise run services:up` | Start PostgreSQL, Redis, and the PII filter service |
| `mise run services:down` | Stop services, preserving data |
| `mise run services:destroy` | Stop services and delete volumes |
| `mise run services:logs` | Tail service logs |
| `mise run db:migrate` | Run migrations up |
| `mise run db:migrate:down` | Run migrations down |
| `mise run db:reset` | Drop, recreate, and migrate |
| `mise run build` | Compile binaries to `bin/` |
| `mise run test` | Unit tests with race detection and coverage |
| `mise run lint` | golangci-lint |
| `mise run fmt` | Format Go source |
| `mise run dev` | Services, migrations, then the gateway |
| `mise run run` | Gateway only, services assumed up |
| `mise run keygen` | Generate a development API key |
| `mise run docs:models` | Regenerate the README model table from `configs/models.yaml` |
| `mise run docs:models:check` | Fail if that table is out of date (what CI runs) |

## Environment

mise sets the database and Redis connection variables in `.mise.toml`, including a local-development `AEGIS_KEY_PEPPER`. Provider keys go in `.env`, which is gitignored:

```bash
OPENAI_API_KEY=sk-...
ANTHROPIC_API_KEY=sk-ant-...
```

`AEGIS_KEY_PEPPER` must be at least 32 characters or the gateway and keygen both refuse to start. Leave it commented in `.env`: mise loads that file after its `[env]` block, so an empty assignment would override the development default and stop the gateway from starting.

## Testing

```bash
mise run test                                              # unit tests, no Postgres or Redis needed
go test ./internal/router/... -run TestResolveRoute -v      # one package, one test
go test ./internal/gateway/... -tags=integration -count=1    # integration; needs services up
```

Unit tests stub their dependencies, so `go test ./...` passes on a clean machine.

The zero-retention conformance tests are the exception worth knowing about. `TestNoPayload_SchemaIntrospection` reads migration files and needs nothing. `TestNoPayload_CanaryEndToEnd` needs a live gateway and database, is behind the `integration` tag, and **fails rather than skips** when `TEST_DATABASE_URL`, `TEST_SERVER_URL`, and `TEST_API_KEY` are absent. That is deliberate: it backs a public claim, and a green pipeline where it skipped is indistinguishable from one where it passed. Opt out by name with `AEGIS_SKIP_INTEGRATION=1` if you must.

```bash
TEST_DATABASE_URL=postgres://aegis:aegis-dev@localhost:5432/aegis?sslmode=disable \
TEST_SERVER_URL=http://localhost:8080 \
TEST_API_KEY=<key> \
  go test ./internal/audit/... -tags=integration -run NoPayload -v -p 1
```

`-p 1` matters: these packages share one audit database, and the emitter's fixtures truncate the table the canary is polling.

## Internal layout

```
cmd/
  gateway/      Main API server
  keygen/       API key generation CLI
  migrate/      Database migration runner
internal/
  auth/         API key validation, two-tier caching (Redis, then PostgreSQL)
  config/       YAML loader with ${VAR} expansion and fsnotify hot-reload
  filter/
    policy/     OPA/Rego policy evaluation
    secrets/    Secrets scanning (AWS keys, tokens, JWTs, private keys)
    pii/        PII detection over gRPC to filter-service/
    injection/  Prompt injection heuristics
  gateway/      Request handler, SSE streaming, telemetry logging
  audit/        Audit writer, reader, Merkle checkpoint sealer
  cost/         Per-request cost estimation from configs/pricing.yaml
  ratelimit/    Per-key sliding-window limits and daily spend budgets (Redis)
  retry/        Retry with exponential backoff, jitter, cancellation
  router/       Provider registry, classification gating, fallback chains
    adapters/   openai.go, anthropic.go, mock.go
  storage/      Shared DB access layer
  telemetry/    Prometheus metrics
  types/        Classification levels, canonical request and response
  validation/   Input validation
  httputil/     OpenAI-compatible error responses
  purge/        Retention enforcement
api/
  controlplane/v1/  The public wire protocol between a gateway and a control plane
configs/        gateway.yaml, models.yaml, providers.yaml, pricing.yaml, policies/
deploy/         Contributor services, plus deploy/demo/compose.yaml for the no-clone demo
demos/          Runnable examples
filter-service/ Python gRPC PII service (Presidio and spaCy)
migrations/     PostgreSQL migrations
```

`azure_openai` and `internal_vllm` are configured providers, not separate adapters. Both are served by the OpenAI adapter with a different base URL, so `adapter.Name()` returns `"openai"` for them. Pricing, metrics, and usage attribution key off the provider name from `providers.yaml`, not the adapter name; conflating the two looks up the wrong pricing row.

## How to contribute

### Reporting bugs

Open an issue using the [bug report template](.github/ISSUE_TEMPLATE/bug_report.md). Include version, steps to reproduce, and relevant logs. Strip any API keys or credentials before pasting.

### Suggesting features

Open an issue using the [feature request template](.github/ISSUE_TEMPLATE/feature_request.md). Discuss the approach before opening a large PR; it saves everyone time.

### Submitting pull requests

1. Fork the repo and create a branch from `main`. Branch names are `feature/...`, `fix/...`, or `chore/...`, and say what the change is rather than who wrote it.
2. Make your changes in clear, focused commits.
3. Ensure `mise run test` and `mise run lint` pass.
4. Open a PR against `main` using the PR template.
5. A maintainer will review and provide feedback.

### Accuracy checklist for security-sensitive packages

If your PR touches `internal/auth/`, `internal/filter/`, or `internal/audit/`, you **must** verify before merging:

- [ ] `.github/SECURITY.md` still accurately describes the key-hashing scheme, filter behaviour, and audit logging
- [ ] The capability claims in `README.md` still match what the code actually does
- [ ] `docs/evidence/known-limitations.md` still states the limits accurately, and has not been quietly softened

This check exists because documentation drift in these packages has occurred more than once and can mislead operators into misconfiguring production deployments.

### Documentation rules

These are not style preferences.

1. **Never claim a capability the code lacks, and never omit one it has.** This repository has shipped both errors.
2. **Every capability claim cites a package path, file, or test name.** Absolute links into this repository pin a full 40-character commit. Never link to `main`, and never to a tag: a tag can be moved, and this repository has moved one. `./scripts/check-citations.sh` enforces this and runs in CI.
3. **Never name a competitor.** Frame the tension generically.
4. **Never claim compliance.** Claim that AEGIS produces evidence relevant to a named article or control.
5. **Do not build urgency on a regulatory deadline.**
6. **The commercial tier is written in future or waitlist tense.** It does not exist.
7. **No em dashes.** Connect clauses directly.
8. **Do not invent metrics, users, logos, or testimonials.** There are none.
9. **When a claim fails verification, do not quietly soften the copy to make it true.** Report the failure, propose options, and stop. A weakened claim shipped without review is a worse outcome than a flagged one.

The model alias table in `README.md` is generated. Edit `configs/models.yaml` and run `mise run docs:models`; editing the table by hand fails CI.

`docs/QUICKSTART-COMMANDS.md` is the source of truth for the demo commands. Changing a container name, a port, or a path means updating that file, and the website at aegisgateway.ai is a separate repository that must be updated to match.

### Commit messages

Follow [Conventional Commits](https://www.conventionalcommits.org/):

```
feat: add support for Gemini provider
fix: correct token count for streaming responses
chore: update golangci-lint to v2.13.1
docs: add deployment guide for Fly.io
```

## Code guidelines

- Keep packages focused and well-named.
- Write tests for new behaviour.
- Components hold accessor funcs (`func() *config.Config`) rather than config values, because config hot-reloads. A component that captures a value holds stale config forever.
- Fail-open versus fail-closed is intentional and differs per component. Preserve the existing direction when editing, and say why in a comment when you change one.
- Avoid hardcoding credentials or environment-specific values.
- Prefer explicit error handling over panics.
- Structured logging with `log/slog`, always including `request_id` and, where available, `org_id` and `team_id`.
- Client-facing errors go through `internal/httputil` so responses stay OpenAI-shaped.
- Prometheus metric names are prefixed `aegis_` and registered in `internal/telemetry/metrics.go`.
- New files carry the Apache-2.0 header block used by every existing source file.
- `api/controlplane/v1` is a contract once tagged. Fields may be added; existing fields may not change name, type, or meaning. A breaking change needs a new versioned package, not an edit.

### Known dead code

`internal/gateway/handler_refactored.go` and the `streamSSE` function in `internal/gateway/streaming.go` are a parallel implementation that is not routed. The live path is `handler.go` and `streaming_enhanced.go`. Editing only the refactored files has no runtime effect.

`gen/filter/v1/filter.go` is hand-written, not protoc-generated. Changing `proto/filter/v1/filter.proto` means updating the Go stubs by hand and regenerating the Python side.

## Security issues

Do not open a public issue for security vulnerabilities. See [SECURITY.md](.github/SECURITY.md) for the responsible disclosure process.

## Questions

Open a [GitHub Discussion](https://github.com/aegis-gateway/aegis-ai-gateway/discussions) for general questions.

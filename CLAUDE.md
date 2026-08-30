# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository layout note

The git repo root is `aegis-ai-gateway/`, one level below the usual working directory
(`~/projects/aegis`). All paths below are relative to the repo root; `cd aegis-ai-gateway`
before running any command.

## Commands

`mise` is the task runner (`.mise.toml`); `Makefile` mirrors most tasks for CI.

```bash
mise run setup            # go mod download + verify
mise run services:up      # Postgres + Redis + PII filter service (deploy/docker-compose.yaml)
mise run db:migrate       # migrations up
mise run db:reset         # drop, recreate, migrate
mise run dev              # services:up + db:migrate + go run ./cmd/gateway
mise run run              # gateway only (services assumed up)
mise run build            # bin/gateway, bin/keygen, bin/migrate
mise run test             # go test ./... -v -race -cover
mise run lint             # golangci-lint run ./...
mise run keygen           # dev API key (org=dev-org, INTERNAL, 365d)
```

Unit tests need **no** Postgres/Redis — every package stubs its dependencies, so
`go test ./...` passes on a clean machine.

Single test / package:

```bash
go test ./internal/router/... -run TestResolveRoute -v
go test ./internal/gateway -run TestChatCompletions -race
```

Integration tests are guarded by the `integration` build tag and live in
`internal/gateway/integration_test.go` (they need Postgres + Redis running):

```bash
go test ./internal/gateway/... -tags=integration -count=1 -v
```

Note: `mise run test:integration` and `make test-integration` used to point at
`./test/integration/...`, a directory that does not exist. Both now select by build tag
across the tree instead.

Demos are Docker-only and self-contained. `./quickstart.sh` is the single entry point;
there is no `demos/00-quickstart/run.sh`. It brings up gateway + Postgres + Redis with the
hardcoded demo key `aegis-demo-quickstart`, requires no provider credential, and puts Open
WebUI behind `--with-webui`. `./quickstart.sh verify` runs the evidence sequence.
`docs/QUICKSTART-COMMANDS.md` is the source of truth for every published demo command.

## Architecture

An OpenAI-compatible proxy in front of multiple LLM providers. The single interesting code
path is `POST /v1/chat/completions`, handled by
[handler.go](internal/gateway/handler.go) — read that function first; it is the spine of
the system and calls into nearly every package.

Request pipeline, in order:

1. **chi middleware** (`cmd/gateway/main.go`) — RealIP, Recoverer, `X-Request-ID`
   generation, then per-route `auth.Middleware` and `ratelimit.Middleware`.
2. **auth** — `Authorization: Bearer <key>` → SHA-256 → `CachedKeyStore.Lookup`
   (Redis 5-min cache, then Postgres `api_keys`). Yields `AuthInfo` in the request context
   with org/team/user, `MaxClassification`, and `AllowedModels`.
3. **ratelimit** — Redis sliding-window (RPM) + daily spend budget, each behind a
   `RedisCircuitBreaker`. Deliberate asymmetry: **Redis not configured → fail open;
   Redis configured but unreachable → fail closed.**
4. **validation** — `internal/validation` size/shape limits on the parsed `AegisRequest`.
5. **filter chain** — `filter.Chain` runs `secrets` → `injection` → `pii` in order,
   stopping at the first `ActionBlock`. Each implements `filter.Filter`
   (`Name/Enabled/ScanRequest`); `Enabled()` reads live config so filters toggle on reload.
   `pii` is a gRPC call out to the Python Presidio service.
6. **routing** — `router.ResolveRoute` maps the model alias (`aegis-fast`, …) from
   `configs/models.yaml` to a primary provider, falling back down the `fallback` list.
   A route is skipped if its `classification_ceiling` does not `Allow` the key's
   classification, or if the provider's circuit breaker is open (`router.HealthTracker`).
7. **OPA policy** — runs *after* routing (it needs the provider type) via
   `internal/filter/policy`. Rego bundles are compiled from `configs/policies/` and hot-swapped;
   a failed compile leaves the last known-good query in place.
8. **provider call** — `adapters.ProviderAdapter` (`TransformRequest` / `SendRequest` /
   `TransformResponse` / `TransformStreamChunk`), wrapped in `retry.Executor`
   (exponential backoff + jitter) and watched by `retry.ContextMonitor` for client
   cancellation.
9. **response** — cost via `cost.Calculator` (pricing table in `configs/pricing.yaml`,
   priced per million tokens), Prometheus metrics, `slog` line, and an async
   `storage.UsageRecorder` insert into `usage_records`.

Streaming (`"stream": true`) branches at step 8 into
[streaming_enhanced.go](internal/gateway/streaming_enhanced.go) — SSE relay with per-chunk
and total timeouts, TTFT/tokens-per-second metrics, and its own cost/usage recording.

### Config and hot-reload

`internal/config.Loader` reads `configs/{gateway,models,providers}.yaml`, expanding
`${VAR}` and `${VAR:default}` before YAML parsing, and watches the directory (plus
subdirectories, so `.rego` edits count) with fsnotify. Reload callbacks registered via
`OnReload` rebuild the provider registry and recompile policies.

Because of this, components hold **accessor funcs** (`func() *config.Config`,
`func() config.PolicyFilterConfig`) rather than config values — new components must follow
that pattern or they will hold stale config forever.

### Data model

`migrations/` (golang-migrate, run by `cmd/migrate`): `api_keys` (hash + permissions +
limits), `usage_daily` (aggregates), `usage_records` (per-request detail),
`audit_events`. The `classification_tier` enum mirrors `types.Classification`
(PUBLIC < INTERNAL < CONFIDENTIAL < RESTRICTED).

### PII filter service

`filter-service/` is a separate Python gRPC service (Presidio + spaCy) on port 50051.
`gen/filter/v1/filter.go` is **hand-written, not protoc-generated** — if you change
`proto/filter/v1/filter.proto` you must update the Go stubs by hand and regenerate the
Python side.

## Gotchas

- **Dead code:** `internal/gateway/handler_refactored.go` (`ChatCompletionsRefactored`,
  `RequestProcessor`, `RouterProcessor`) is a parallel implementation that is *not routed*.
  `internal/gateway/streaming.go`'s `streamSSE` is likewise superseded by
  `streaming_enhanced.go`. Edits to the live path go in `handler.go` /
  `streaming_enhanced.go`; changing only the refactored files has no runtime effect.
- **`AEGIS_MOCK_PROVIDER=true` replaces every provider with the mock.** `BuildFromConfig`
  registers `adapters.MockAdapter` under each name in `providers.yaml` rather than adding a
  provider, so routing, ceilings, and pricing keep resolving against the real names. The
  value must be exactly `true`. A provider typed `mock` without the variable is left
  unregistered rather than falling through to the OpenAI adapter, so routing fails closed.
- **`adapter.Name()` is the adapter *type*, not the config key.** `OpenAIAdapter.Name()`
  always returns `"openai"` and `AnthropicAdapter.Name()` returns `"anthropic"`, even when
  registered as `azure_openai` or `internal_vllm` (both fall through to the OpenAI adapter
  in `router.BuildFromConfig`). Consequences: the `pricing.azure_openai` block in
  `configs/models.yaml` is never reached, and `input.request.provider_type` in Rego sees
  `"openai"`/`"anthropic"` — so the `provider_type == "external"` deny rule in
  `configs/policies/default.rego` never fires.
- Fail-open vs fail-closed is intentional and differs per component (see step 3, and
  `routeEligible`, which fails *open* on an unparseable request classification but *closed*
  on an unparseable ceiling). Preserve the existing direction when editing.
- The docs in `docs/` plus `NEXT_STEPS.md` and `ARTEMIS_REPORT.md` are point-in-time sprint
  reports, not maintained specs. Treat the code as authoritative.

## Conventions

- Conventional Commits (`feat:`, `fix:`, `chore:`, `docs:`); PRs target `main`.
- Branch names are `feature/...`, `fix/...`, or `chore/...`. Do not prefix a branch with
  the name of the tool that wrote it: the branch should say what the change is, not who
  typed it, and a reviewer scanning the branch list should learn something from the name.
- Structured logging with `log/slog` (JSON handler), always including `request_id` and,
  where available, `org_id`/`team_id`.
- Client-facing errors go through `internal/httputil` helpers
  (`WriteAuthError`, `WriteContentBlockedError`, …) so responses stay OpenAI-shaped.
- Prometheus metric names are prefixed `aegis_` and registered in
  `internal/telemetry/metrics.go`.
- Licensed under Apache-2.0 (see `LICENSE`, `NOTICE`, `LICENSING.md`); new files inherit
  it and carry the Apache header block used by every existing source file. This repository
  is the open core: no proprietary code lands here, and nothing here may be relicensed.
- `api/controlplane/v1` is the public wire protocol between a gateway and a control plane.
  It is Apache-2.0 and stays that way, and once it ships in a tagged release it is a
  contract: fields may be added, existing fields may not change name, type, or meaning.
  A breaking change needs a new versioned package, not an edit.

## Shared rules for all AEGIS repositories

These are not style preferences. Breaking one of them is worse than shipping late.
They apply to this repository, `aegisgateway.ai`, and `aegisgateway.dev` alike.

1. **Never claim a capability the code lacks, and never omit one it has.** This
   repository has already shipped both errors: a security policy claiming response
   scanning that did not exist, and a README that omitted the policy engine, PII
   filtering, injection detection, rate limiting, and audit logging.
2. **Every capability claim cites a package path, file, or test name**, linked to a
   pinned commit or tag. **Never link to `main`.**
3. **Never name a competitor** in any user-facing text. Frame the tension generically.
4. **Never claim compliance.** Claim that AEGIS produces evidence relevant to a named
   article or control.
5. **Do not build urgency on a regulatory deadline.**
6. **The commercial tier is written in future or waitlist tense.** It does not exist.
7. **No em dashes.** Connect clauses directly.
8. **Do not invent metrics, users, logos, or testimonials.** There are none.
9. **When a claim fails verification, do not quietly soften the copy to make it true.**
   Positioning is locked. Report the failure, propose options, and stop. A weakened
   claim shipped without review is a worse outcome than a flagged one.

### Verification artifacts

- [`VERIFICATION.md`](VERIFICATION.md): claim-by-claim verification against source,
  with per-claim verdicts and the launch blocker list.
- [`docs/reference/deny-reasons.md`](docs/reference/deny-reasons.md): every refusal
  string the gateway can emit.
- [`docs/evidence/known-limitations.md`](docs/evidence/known-limitations.md): what the
  audit trail does not establish.

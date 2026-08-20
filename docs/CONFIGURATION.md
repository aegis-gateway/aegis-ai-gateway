# AEGIS AI Gateway — Configuration Reference

AEGIS reads configuration from two sources, applied in this order of precedence (highest first):

1. **Environment variables** — but only where the YAML explicitly references them
2. **YAML files** in `configs/` — loaded at startup and hot-reloaded automatically

There is no generic environment override. `config.Loader` expands `${VAR_NAME:default}`
placeholders in the YAML text before parsing it, so an environment variable takes
effect **only if the YAML author wrote a placeholder for it**. A key with a literal
value — `database.name`, `telemetry.log_level`, `telemetry.metrics_port` — cannot be
changed from the environment; edit the YAML instead.

Hot-reload is driven by fsnotify on the config directory and its subdirectories, not
by a signal. See the warning in [POLICIES.md](POLICIES.md) before sending `SIGHUP`.

---

## `configs/gateway.yaml`

Controls the HTTP server, database, Redis, telemetry, content filters, and routing behaviour.

### `server`

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `server.host` | string | `"0.0.0.0"` | IP address the HTTP server binds to |
| `server.port` | int | `8080` | HTTP listen port (env: `GATEWAY_PORT`) |
| `server.read_timeout` | duration | `"30s"` | Maximum duration to read the full request |
| `server.write_timeout` | duration | `"120s"` | Maximum duration to write the full response (must exceed longest expected stream) |
| `server.idle_timeout` | duration | `"120s"` | Keep-alive timeout for idle connections |
| `server.graceful_shutdown` | duration | `"30s"` | Time allowed for in-flight requests to complete on shutdown |

### `database`

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `database.host` | string | `"localhost"` | PostgreSQL host (env: `DB_HOST`) |
| `database.port` | int | `5432` | PostgreSQL port (env: `DB_PORT`) |
| `database.name` | string | `"aegis"` | Database name |
| `database.user` | string | `"aegis"` | Database user (env: `DB_USER`) |
| `database.password` | string | `"aegis-dev"` | Database password (env: `DB_PASSWORD`). **Replace in production.** |
| `database.max_open_conns` | int | `25` | Maximum open connections in the pool |
| `database.max_idle_conns` | int | `10` | Maximum idle connections in the pool |
| `database.conn_max_lifetime` | duration | `"5m"` | Maximum lifetime of a connection before it is closed and replaced |

> **`DATABASE_URL` does not configure the gateway.** The gateway builds its DSN
> from the `database.*` keys above via `DatabaseConfig.DSN()` and never reads
> `DATABASE_URL`; only the `keygen` and `migrate` CLIs consult it. Configure the
> gateway's database through `DB_HOST` / `DB_PORT` / `DB_USER` / `DB_PASSWORD` /
> `DB_NAME`.
>
> **`DSN()` also hardcodes `sslmode=disable`**, so the gateway currently cannot
> negotiate TLS to PostgreSQL and an `sslmode=require` setting has no effect. Keep
> the database on a trusted network until this is configurable.

### `redis`

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `redis.addresses` | list of strings | `["localhost:6379"]` | Redis addresses (env overrides: `REDIS_HOST`, `REDIS_PORT`) |
| `redis.password` | string | `""` | Redis authentication password (env: `REDIS_PASSWORD`) |
| `redis.db` | int | `0` | Redis logical database index |
| `redis.pool_size` | int | `50` | Maximum number of connections in the Redis pool |

### `telemetry`

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `telemetry.log_level` | string | `"info"` | Log verbosity: `debug`, `info`, `warn`, `error` |
| `telemetry.log_format` | string | `"json"` | Log format: `json` (structured) or `text` |
| `telemetry.metrics_port` | int | `9090` | Port where Prometheus metrics are exposed at `/metrics` (env: `METRICS_PORT`) |
| `telemetry.otlp_endpoint` | string | `""` | OpenTelemetry collector endpoint for trace export (env: `OTLP_ENDPOINT`). Leave empty to disable tracing. |
| `telemetry.trace_sample_rate` | float | `0.1` | Fraction of requests sampled for distributed tracing (0.0–1.0) |

### `filter`

#### `filter.pii_service`

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `filter.pii_service.address` | string | `"aegis-filter-nlp:50051"` | gRPC address of the NLP filter service (env: `PII_SERVICE_ADDR`) |
| `filter.pii_service.timeout` | duration | `"5s"` | Per-call gRPC timeout |
| `filter.pii_service.max_retries` | int | `1` | Retry attempts on gRPC transport errors |

#### `filter.secrets`

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `filter.secrets.enabled` | bool | `true` | Enable secrets detection filter |

#### `filter.injection`

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `filter.injection.enabled` | bool | `true` | Enable prompt injection detection filter |
| `filter.injection.block_threshold` | float | `0.9` | Confidence score above which a request is blocked (0.0–1.0) |
| `filter.injection.flag_threshold` | float | `0.7` | Confidence score above which a request is flagged but not blocked |

#### `filter.policy`

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `filter.policy.enabled` | bool | `true` | Enable OPA policy evaluation |
| `filter.policy.bundle_path` | string | `"configs/policies"` | Directory containing `*.rego` policy files (env: `OPA_BUNDLE_PATH`) |
| `filter.policy.evaluation_timeout` | duration | `"100ms"` | Maximum time allowed for OPA to evaluate a single request |

### `routing`

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `routing.default_timeout` | duration | `"30s"` | Per-request timeout for non-streaming provider calls |
| `routing.stream_first_chunk_timeout` | duration | `"60s"` | **Not currently wired.** Parsed and defaulted, but never read — see the note below. |
| `routing.stream_chunk_timeout` | duration | `"10s"` | **Not currently wired.** Parsed and defaulted, but never read — see the note below. |
| `routing.max_retries` | int | `2` | Maximum retry attempts on transient provider errors |
| `routing.health_check_interval` | duration | `"10s"` | How often the gateway probes provider health |

> **Streaming timeouts are not configurable yet.** `NewHandler` always builds the
> streaming handler with `DefaultStreamingConfig()` — a 30s per-chunk timeout and a
> 5 minute total timeout — and neither `stream_first_chunk_timeout` nor
> `stream_chunk_timeout` is referenced anywhere outside the config definitions.
> Changing either value has no effect on a running gateway.

#### `routing.circuit_breaker`

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `routing.circuit_breaker.failure_threshold` | int | `5` | Number of consecutive failures before the circuit opens |
| `routing.circuit_breaker.error_rate_threshold` | float | `0.5` | Error rate (0.0–1.0) within the window that triggers the circuit |
| `routing.circuit_breaker.error_rate_window` | duration | `"30s"` | Sliding window for error rate calculation |
| `routing.circuit_breaker.recovery_probe_interval` | duration | `"15s"` | Time between recovery probe attempts when circuit is open |

---

## `configs/models.yaml`

Defines model aliases exposed to clients, the routing chain for each alias, and per-token pricing.

### Model alias structure

```yaml
models:
  <alias>:                          # e.g. "aegis-gpt4"
    display_name: "<Human name>"
    primary:
      provider: <provider-name>     # must match a key in providers.yaml
      model: <provider-model-id>    # model name sent to the provider
      classification_ceiling: <tier> # PUBLIC | INTERNAL | CONFIDENTIAL | RESTRICTED
    fallback:
      - provider: <provider-name>
        model: <provider-model-id>
        classification_ceiling: <tier>
        # Azure-specific:
        deployment: <azure-deployment-name>
```

`classification_ceiling` controls which data sensitivity tiers a route may serve. A route with `CONFIDENTIAL` ceiling accepts PUBLIC, INTERNAL, and CONFIDENTIAL requests but rejects RESTRICTED ones. Omitting the ceiling allows all tiers.

Routing tries the `primary` route first, then each `fallback` in order, skipping any route whose ceiling is below the request's classification or whose provider's circuit breaker is open.

### Pricing structure

```yaml
pricing:
  <provider-name>:
    <model-id>:
      input: <USD per 1000 tokens>   # prompt token cost
      output: <USD per 1000 tokens>  # completion token cost
```

Cost estimates shown in `usage_records.estimated_cost_usd` are calculated from these values. If a model/provider combination has no pricing entry, cost is recorded as `0` and a warning is logged.

---

## `configs/providers.yaml`

Defines connection parameters for each upstream LLM provider. Provider names here must match the `provider` keys referenced in `models.yaml`.

### Provider fields

| Field | Type | Description |
|-------|------|-------------|
| `type` | string | Adapter type. Only `openai` and `anthropic` have adapters; **every other value, including `azure_openai`, falls through to the OpenAI adapter.** |
| `base_url` | string | Base URL for API calls. Requests are posted to `base_url + "/chat/completions"` verbatim; no deployment path is appended. |
| `api_key` | string | API key sent in the `Authorization: Bearer` header (OpenAI adapter) or the `x-api-key` header (Anthropic adapter). |
| `max_concurrent` | int | Maximum number of concurrent HTTP connections to this provider. |
| `timeout` | duration | Per-request HTTP timeout. Should be at most `routing.default_timeout`. |
| `headers` | map | Extra HTTP headers merged into every request to this provider (e.g. `anthropic-version`). |
| `api_version` | string | Parsed into config but **read by no code**. Intended for Azure; has no effect today. |

> **Azure OpenAI is not supported yet.** `router.BuildFromConfig` has no
> `azure_openai` case, so such a provider is served by the OpenAI adapter: it posts
> to `base_url + "/chat/completions"` with bearer auth, and ignores both the
> `deployment` field in `models.yaml` and `api_version` here. Azure needs its
> `/openai/deployments/{deployment}/chat/completions?api-version=...` path shape and
> an `api-key` header, so routes pointing at it — including fallback routes — will
> not reach the intended deployment.

### Example

```yaml
providers:
  openai:
    type: openai
    base_url: "https://api.openai.com/v1"
    api_key: "${OPENAI_API_KEY:}"
    max_concurrent: 200
    timeout: "30s"
    headers:
      Organization: "${OPENAI_ORG_ID:}"

  anthropic:
    type: anthropic
    base_url: "https://api.anthropic.com/v1"
    api_key: "${ANTHROPIC_API_KEY:}"
    max_concurrent: 200
    timeout: "30s"
    headers:
      anthropic-version: "2023-06-01"

  azure_openai:
    type: azure_openai
    base_url: "https://${AZURE_OPENAI_ENDPOINT:}.openai.azure.com"
    api_key: "${AZURE_OPENAI_KEY:}"
    api_version: "2024-10-21"
    max_concurrent: 200
    timeout: "30s"
```

---

## Environment Variables

The following variables are read from the environment. Entries marked **No effect**
are recognised names that no code path consumes — they are listed so the gap is
explicit rather than discovered in production.

| Variable | Required | Description |
|----------|----------|-------------|
| `OPENAI_API_KEY` | If using OpenAI | OpenAI API key |
| `ANTHROPIC_API_KEY` | If using Anthropic | Anthropic API key |
| `AZURE_OPENAI_KEY` | If using Azure | Azure OpenAI API key |
| `AZURE_OPENAI_ENDPOINT` | If using Azure | Azure OpenAI resource name (used to build the base URL) |
| `OPENAI_ORG_ID` | No | OpenAI organization ID; sent as the `Organization` header |
| `DB_HOST` | No | PostgreSQL host (default: `localhost`) |
| `DB_PORT` | No | PostgreSQL port (default: `5432`) |
| `DB_USER` | No | PostgreSQL user (default: `aegis`) |
| `DB_PASSWORD` | No | PostgreSQL password (default: `aegis-dev` — **change in production**) |
| `DB_NAME` | No | **No effect.** `gateway.yaml` sets `database.name: "aegis"` literally, with no `${DB_NAME}` placeholder. Edit the YAML to change it. |
| `DATABASE_URL` | No | Read by the `keygen` and `migrate` CLIs only — **the gateway ignores it** and uses the `DB_*` variables |
| `REDIS_HOST` | No | Redis host (default: `localhost`) |
| `REDIS_PORT` | No | Redis port (default: `6379`) |
| `REDIS_PASSWORD` | No | Redis password (default: empty) |
| `GATEWAY_PORT` | No | HTTP listen port (default: `8080`) |
| `METRICS_PORT` | No | **No effect on the gateway.** `telemetry.metrics_port` is the literal `9090`. The demo Compose files use this name to map the host port only. |
| `LOG_LEVEL` | No | **No effect.** `telemetry.log_level` is the literal `"info"`, and `cmd/gateway/main.go` builds its slog handler with `slog.LevelInfo` regardless. |
| `OPA_BUNDLE_PATH` | No | Path to OPA `.rego` policy files (default: `configs/policies`). Also exposed as `OPA_POLICY_BUNDLE_PATH` in the Docker Compose template. |
| `OTLP_ENDPOINT` | No | OpenTelemetry collector endpoint for trace export. Leave unset to disable. |
| `PII_SERVICE_ADDR` | No | gRPC address of the NLP filter service (default: `aegis-filter-nlp:50051`) |
| `AEGIS_KEY_PEPPER` | **Yes** | Server-side pepper for HMAC-SHA256 API key hashing. Both the gateway and the `keygen` CLI refuse to start without it. Generate once with `openssl rand -hex 32` (min 32 chars). Do **not** rotate after deploy — changing it invalidates all v2 keys. |
| `WEBUI_SECRET_KEY` | Demo Compose only | Consumed by the bundled Open WebUI container, not by the gateway. Generate with `openssl rand -hex 32`. |
| `POSTGRES_PASSWORD` | Demo Compose only | Initializes the PostgreSQL container password. Not read by the gateway — set `DB_PASSWORD` to match. |

> **Security note**: Never commit real credentials to version control. Use the `.env.production.example` template and set values only in your deployment environment.

---

## API Key Hashing (`AEGIS_KEY_PEPPER`)

AEGIS stores API keys as hashes, never in plaintext. Two hash schemes are supported for zero-downtime migration:

| `hash_version` | Algorithm | Key type |
|---|---|---|
| 1 | SHA-256 | Legacy keys issued before HMAC support |
| 2 | HMAC-SHA256 with `AEGIS_KEY_PEPPER` | All new keys |

### How verification works

When a request arrives, the gateway:
1. Tries an HMAC-SHA256 (v2) lookup using the presented key + pepper
2. If not found, falls back to a SHA-256 (v1) lookup

Existing v1 keys continue to work transparently after the upgrade.

### Migration path

1. Set `AEGIS_KEY_PEPPER` to a strong random value (≥ 32 chars) before deploying.
2. All **new** keys issued by `keygen` will use HMAC-SHA256 (`hash_version=2`).
3. Existing v1 keys remain valid until they expire — no forced invalidation.
4. After all v1 keys have expired, the v1 fallback lookup can be removed.

### Pepper rotation warning

**Do not rotate `AEGIS_KEY_PEPPER` after deployment.** All v2 keys are indexed by their HMAC hash; changing the pepper means the gateway can no longer look them up. If rotation is necessary, re-issue all active v2 keys before changing the pepper.

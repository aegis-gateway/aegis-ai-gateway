# AEGIS AI Gateway — Configuration Reference

AEGIS reads configuration from two sources, applied in this order of precedence (highest first):

1. **Environment variables** — override any YAML value
2. **YAML files** in `configs/` — loaded at startup and hot-reloaded on `SIGHUP`

YAML values support `${VAR_NAME:default}` substitution: the value is taken from the environment variable `VAR_NAME`, falling back to `default` if the variable is unset.

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

> Alternatively, set `DATABASE_URL` as a full connection string (e.g. `postgres://aegis:password@host:5432/aegis?sslmode=require`). When set, it overrides the individual `database.*` keys.

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
| `routing.stream_first_chunk_timeout` | duration | `"60s"` | Timeout waiting for the first SSE chunk from the provider |
| `routing.stream_chunk_timeout` | duration | `"10s"` | Timeout between consecutive SSE chunks |
| `routing.max_retries` | int | `2` | Maximum retry attempts on transient provider errors |
| `routing.health_check_interval` | duration | `"10s"` | How often the gateway probes provider health |

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
| `type` | string | Adapter type: `openai`, `anthropic`, `azure_openai`. Unknown types fall back to `openai`. |
| `base_url` | string | Base URL for API calls. For Azure, the deployment path is appended automatically. |
| `api_key` | string | API key sent in the `Authorization` header (OpenAI) or `x-api-key` header (Anthropic). |
| `max_concurrent` | int | Maximum number of concurrent HTTP connections to this provider. |
| `timeout` | duration | Per-request HTTP timeout. Should be at most `routing.default_timeout`. |
| `headers` | map | Extra HTTP headers merged into every request to this provider (e.g. `anthropic-version`). |
| `api_version` | string | Azure-specific: API version query parameter appended to every URL. |

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

The following variables are read from the environment. Variables marked **Required** have no default and the gateway will not start without them (unless provided via YAML with an explicit default).

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
| `DB_NAME` | No | PostgreSQL database name (default: `aegis`) |
| `DATABASE_URL` | No | Full PostgreSQL DSN; overrides individual `DB_*` variables when set |
| `REDIS_HOST` | No | Redis host (default: `localhost`) |
| `REDIS_PORT` | No | Redis port (default: `6379`) |
| `REDIS_PASSWORD` | No | Redis password (default: empty) |
| `GATEWAY_PORT` | No | HTTP listen port (default: `8080`) |
| `METRICS_PORT` | No | Prometheus metrics port (default: `9090`) |
| `LOG_LEVEL` | No | Log verbosity: `debug`, `info`, `warn`, `error` (default: `info`) |
| `OPA_BUNDLE_PATH` | No | Path to OPA `.rego` policy files (default: `configs/policies`). Also exposed as `OPA_POLICY_BUNDLE_PATH` in the Docker Compose template. |
| `OTLP_ENDPOINT` | No | OpenTelemetry collector endpoint for trace export. Leave unset to disable. |
| `PII_SERVICE_ADDR` | No | gRPC address of the NLP filter service (default: `aegis-filter-nlp:50051`) |
| `WEBUI_SECRET_KEY` | If using bundled UI | Secret key for Open WebUI session signing. Generate with `openssl rand -hex 32`. |
| `POSTGRES_PASSWORD` | No | Used by Docker Compose to initialize the PostgreSQL container password |

> **Security note**: Never commit real credentials to version control. Use the `.env.production.example` template and set values only in your deployment environment.

# 15 — Custom OPA Policies

Three custom Rego policies — competitor mentions, token budgets, topic restrictions — enforced at the gateway with no code changes and hot-reloaded without restart.

## Prerequisites

- Docker Compose v2
- At least one provider API key (OpenAI or Anthropic)
- **00-quickstart does NOT need to be running** — this demo is self-contained

## Policy layers

| Layer | File | Customization method | Hot-reload |
|-------|------|---------------------|------------|
| Secrets scanner | `internal/filter/secrets/` | YAML config | Yes |
| OPA engine | `configs/policies/*.rego` | Drop .rego files | Yes (fsnotify) |
| NLP service | `filter-service/` | spaCy entity rules | Restart required |

## Writing your own policy

When multiple `.rego` files share a bundle directory, the `default allow`, `default reason`, `allow`, and `reason` complete rules **must live in exactly one file** (the base). Individual policy files only contribute `deny contains msg if { ... }` partial-set rules. Duplicating complete rules across files causes OPA to reject the bundle with `rego_type_error: conflicting rules`.

The base file (`base.rego`):

```rego
package aegis.policy

import rego.v1

default allow := true
default reason := ""

allow := false if {
    count(deny) > 0
}

reason := concat("; ", deny) if {
    count(deny) > 0
}
```

A policy file (e.g. `my-rule.rego`) only adds deny rules:

```rego
package aegis.policy

import rego.v1

deny contains msg if {
    # your condition here
    msg := "reason for denial"
}
```

If you only have a single `.rego` file, you can keep everything in one file (like `configs/policies/default.rego`).

### Available input fields

Every policy receives this input object:

| Path | Type | Description |
|------|------|-------------|
| `input.user.id` | string | User ID from API key |
| `input.user.org` | string | Organization ID |
| `input.user.team` | string | Team ID |
| `input.request.model` | string | Requested model name |
| `input.request.classification` | string | Data classification (PUBLIC, INTERNAL, CONFIDENTIAL, RESTRICTED) |
| `input.request.provider_type` | string | Resolved provider type (openai, anthropic, etc.) |
| `input.messages` | array | Array of `{role, content}` message objects |
| `input.time.hour` | int | Current UTC hour (0-23) |
| `input.time.day` | string | Current UTC weekday (Monday, Tuesday, etc.) |

### Allow / deny / reason pattern

- `data.aegis.policy.allow` must be `true` to permit the request
- `data.aegis.policy.reason` is returned to the caller on denial
- Any `.rego` file dropped into `OPA_BUNDLE_PATH` is picked up within seconds without restarting the gateway

## Why this matters

Most AI gateways give you a list of blocked patterns. AEGIS gives you a policy engine. When your legal team says no financial advice from external keys, you write one Rego rule. When compliance adds a new restricted topic next quarter, you add one more file. No Go code, no pull request, no deployment.

## How to run

```bash
cd demos/15-custom-policies
cp ../shared/.env.example .env
# add at least one provider key
docker compose up --build -d
./run.sh
docker compose down -v
```

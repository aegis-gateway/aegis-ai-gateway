# AEGIS AI Gateway — OPA Policy Guide

AEGIS uses [Open Policy Agent (OPA)](https://www.openpolicyagent.org/) and [Rego](https://www.openpolicyagent.org/docs/latest/policy-language/) to evaluate custom access-control rules for every request. This document explains how policies work, what data they receive, and how to write your own.

---

## How Policies Work

After the content filter chain runs and a provider has been selected, the gateway evaluates all loaded Rego modules against a structured **input document** that describes the current request. OPA evaluates the query:

```
[data.aegis.policy.allow, data.aegis.policy.reason]
```

If `allow` is `false`, the request is blocked with HTTP 451 and the `reason` string is returned to the caller. If `allow` is `true`, the request proceeds to the provider.

The evaluator fails **closed**: if no policies are loaded, OPA returns no result, or evaluation times out (default 100 ms), the request is **blocked**.

---

## Input Document Shape

The following Go struct is serialized and passed as the OPA input for every request (`internal/filter/policy/opa.go`):

```go
type PolicyInput struct {
    User     PolicyUser      `json:"user"`
    Request  PolicyReq       `json:"request"`
    Messages []PolicyMessage `json:"messages"`
    Time     PolicyTime      `json:"time"`
}

type PolicyUser struct {
    ID   string `json:"id"`
    Org  string `json:"org"`
    Team string `json:"team"`
}

type PolicyReq struct {
    Model          string `json:"model"`
    Classification string `json:"classification"`
    ProviderType   string `json:"provider_type"`
}

type PolicyMessage struct {
    Role    string `json:"role"`
    Content string `json:"content"`
}

type PolicyTime struct {
    Hour int    `json:"hour"`
    Day  string `json:"day"`
}
```

As a JSON example:

```json
{
  "user": {
    "id": "user-abc123",
    "org": "acme-corp",
    "team": "engineering"
  },
  "request": {
    "model": "aegis-gpt4",
    "classification": "INTERNAL",
    "provider_type": "openai"
  },
  "messages": [
    { "role": "system", "content": "You are a helpful assistant." },
    { "role": "user",   "content": "Summarize this document." }
  ],
  "time": {
    "hour": 14,
    "day": "Thursday"
  }
}
```

`classification` values match the tiers defined in the database: `PUBLIC`, `INTERNAL`, `CONFIDENTIAL`, `RESTRICTED`.

`provider_type` is the adapter name resolved during routing: `openai`, `anthropic`, or `azure_openai`.

`time.day` is the full English weekday name returned by Go's `time.Weekday().String()` (e.g., `"Monday"`, `"Saturday"`).

---

## Policy Result

OPA must evaluate the query `[data.aegis.policy.allow, data.aegis.policy.reason]` and return a two-element array:

| Position | Key | Type | Required | Meaning |
|----------|-----|------|----------|---------|
| 0 | `allow` | `boolean` | yes | `true` = request is permitted |
| 1 | `reason` | `string` | yes | Human-readable explanation; shown to caller when `allow = false` |

If the result set is empty or the array format is unexpected, the evaluator treats the request as **blocked**.

---

## Loading Policies

Rego files are loaded from the directory configured in `configs/gateway.yaml`:

```yaml
filter:
  policy:
    enabled: true
    bundle_path: "${OPA_BUNDLE_PATH:configs/policies}"
    evaluation_timeout: "100ms"
```

At startup the gateway calls `Evaluator.Load()`, which reads all `*.rego` files under `bundle_path`, compiles them into a single `PreparedEvalQuery`, and atomically swaps in the new query. **Hot-reload** is supported: send a `SIGHUP` to the gateway process (or call the reload endpoint if configured) to recompile policies without restarting.

If compilation fails (syntax error, conflict), the **previous compiled query is kept** so existing traffic continues to be evaluated. The failed reload is recorded in metrics (`RecordPolicyReload(false)`) and logged at `ERROR` level.

---

## Walkthrough: Demo Policies

The demo policies live in `demos/15-custom-policies/policies/`. There are four files; they all share the `package aegis.policy` namespace so OPA merges their rules.

### `base.rego` — the foundation

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

This file establishes the pattern used by all other policies: individual policy files contribute `deny` rules; `base.rego` aggregates them. If any `deny` rule fires, `allow` becomes `false` and `reason` is the concatenation of all denial messages.

You never need to set `allow` or `reason` directly in your own policy files — only add `deny contains msg if { ... }` rules.

### `token-budget.rego` — prompt length cap for external teams

```rego
package aegis.policy

import rego.v1

total_chars := sum([count(m.content) | some m in input.messages])

deny contains msg if {
    input.user.team == "external"
    total_chars > 500
    msg := "prompt too long for external team (limit: 500 chars)"
}
```

This policy computes the total character count across all messages and blocks users whose `team` is `"external"` if they send more than 500 characters. The `total_chars` helper expression uses a list comprehension over `input.messages`.

### `topic-restriction.rego` — restrict financial topics

```rego
package aegis.policy

import rego.v1

financial_terms := ["portfolio", "stock price", "buy shares", "trading strategy"]

deny contains msg if {
    input.user.team != "finance"
    some m in input.messages
    some term in financial_terms
    contains(lower(m.content), term)
    msg := "financial topic restricted to finance team"
}
```

This policy denies any request from outside the `"finance"` team that contains a financial keyword in any message. The `some` keyword iterates over collections; `lower()` makes matching case-insensitive.

---

## Writing Your Own Policy

All your policies must:

1. Declare `package aegis.policy`
2. Import `rego.v1`
3. Add `deny contains msg if { ... }` rules

Here is a minimal template:

```rego
package aegis.policy

import rego.v1

# Block requests to restricted providers from non-admin teams.
deny contains msg if {
    input.request.provider_type == "azure_openai"
    input.user.team != "platform"
    msg := "azure_openai provider is restricted to the platform team"
}
```

Drop the `.rego` file into your `bundle_path` directory and trigger a policy reload. No gateway restart is required.

### Tips

- **Use `base.rego`** (or include its content in your bundle) so that `allow` and `reason` are always defined even when no `deny` rules fire.
- **Fail closed by default**: the evaluator blocks if `allow` is undefined, so a missing base policy will block all traffic.
- **Test locally** with the OPA CLI: `opa eval -i input.json -d policies/ "data.aegis.policy.allow"`.
- **Keep rules simple**: each `deny` rule should express one human-readable concern. Aggregate complexity lives in the deny set, not in a single monolithic rule.
- **Classification checks**: use `input.request.classification` to restrict high-tier data to trusted providers (e.g., block RESTRICTED requests to external providers).

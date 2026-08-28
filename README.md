# AEGIS AI Gateway

AEGIS is an OpenAI-compatible gateway that decides whether an AI call is permitted and leaves an auditable record of why. Policy-as-code in Rego, classification gating, secrets and PII filtering, per-key limits, and a tamper-evident record of every denial, in Go, under Apache 2.0. That record contains no prompt text and no response text, and you can check that claim yourself in about two minutes.

**Status: v0.1.0, pre-launch.** No public production users. Every capability claim below names the package, file, or test that implements it, and [VERIFICATION.md](VERIFICATION.md) verifies each of them against source, including the ones that failed.

[aegisgateway.ai](https://aegisgateway.ai) · [Verification](VERIFICATION.md) · [Known limits](#known-limits) · [Docs](docs/) · [Commercial](mailto:hello@aegisgateway.ai)

## Quickstart

Docker, and nothing else. No API key, no account, no sign-up.

```bash
git clone https://github.com/aegis-gateway/aegis-ai-gateway.git
cd aegis-ai-gateway
./quickstart.sh
```

Or without cloning anything:

```bash
curl -O https://aegisgateway.ai/demo/compose.yaml
docker compose up
```

With no provider key in the environment, the gateway answers completions from a mock provider (`internal/router/adapters/mock.go`) and says so on startup and on `/aegis/v1/health`. Every other stage of the pipeline still runs, so a refusal is a real refusal. Export `OPENAI_API_KEY` or `ANTHROPIC_API_KEY` to route to a real provider instead.

The full command set, kept in sync with the site, is in [docs/QUICKSTART-COMMANDS.md](docs/QUICKSTART-COMMANDS.md).

## Verify it

The claim is that a request carrying a credential is refused, and that neither the credential nor the request text is anywhere in the database afterwards. One command shows the whole sequence:

```bash
./quickstart.sh verify
```

It sends a benign request, sends one containing the AWS documentation example key `AKIAIOSFODNN7EXAMPLE`, prints the 451 and the error body, prints the audit row written for that refusal, and then greps a full database dump for the credential:

```
STEP 2  A request carrying a credential is refused
HTTP 451
{ "error": { "message": "Request blocked: detected 1 secret(s) of type: AWS Access Key", ... } }

STEP 3  The refusal was written to the audit trail
request_id  | req_verify_1787829757_7933
event_type  | filter_block
status_code | 451
filter_type | secrets
reason      | Request blocked: detected 1 secret(s) of type: AWS Access Key

STEP 4  The credential is nowhere in the database
$ pg_dump ... | grep -c AKIAIOSFODNN7EXAMPLE
0
```

It exits non-zero if that count is anything other than zero.

Or run it by hand, which is the same thing without the script:

```bash
docker exec aegis-demo-postgres pg_dump -U aegis aegis | grep -c AKIAIOSFODNN7EXAMPLE
```

## What is written, and what is never written

Integrity is at checkpoint granularity, not per row. A sealer computes an RFC 6962 Merkle root over a contiguous range of event IDs and writes a checkpoint into `audit_checkpoints`, each binding the previous checkpoint's hash. A per-row `prev_hash` chain was rejected deliberately: it would serialise every audit write on the request path. See [docs/AUDIT-INTEGRITY.md](docs/AUDIT-INTEGRITY.md).

**Written.** Denials and failures go to `audit_events`, one row per refusal: request ID, timestamp, event type, HTTP status, which filter fired, the reason string, org and team, IP, and API key prefix. Per-request decisions go to `audit_logs`: latency, gateway overhead, model requested versus model served, provider, classification, token counts, cost, routing attempts, and failovers. Successful calls are additionally recorded in `usage_records` with tokens and cost (`internal/audit`, `internal/storage`).

**Never written.** No prompt text. No response text. No matched substring from a filter hit, so a secrets block records that an AWS key pattern was detected and not the key. Rows carry the reason a decision was made and not the content it was made about.

Two tests hold that line, and both are cited from the compliance mapping rather than only asserted here:

| Test | Where | What it does |
|---|---|---|
| `TestNoPayload_SchemaIntrospection` | [`no_payload_test.go:70`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/ea72971186eb5c316966b065bf710f2d85f578b1/internal/audit/no_payload_test.go#L70) | Scans every up migration for a column added to an audit table whose name suggests payload, and fails on a match. It does not hardcode a migration list, so a later `ALTER TABLE audit_logs ADD COLUMN payload TEXT` is caught too. |
| `TestNoPayload_CanaryEndToEnd` | [`no_payload_integration_test.go:66`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/ea72971186eb5c316966b065bf710f2d85f578b1/internal/audit/no_payload_integration_test.go#L66) | Sends a canary string against a live gateway, asserts exactly 451, asserts an audit row **was** written, and only then asserts the canary appears in no row of any audit table. |

The positive control in the second test is the point. Without asserting that a row was written, "the canary is absent" is satisfied by an empty table and proves nothing. The test fails rather than skips when its database is missing, because a conformance test that can silently not run is worse than no test.

Both run in CI on every push, in a dedicated Audit Conformance job that greps for the canary's PASS line by name, since `go test -run` exits 0 when its pattern matches nothing.

## What runs on every call

`POST /v1/chat/completions`, in order. Each stage can refuse, and each refusal is audited.

| # | Stage | Package | Notes |
|---|-------|---------|-------|
| 1 | Request ID, real IP, panic recovery | `cmd/gateway` | chi middleware |
| 2 | Authentication | `internal/auth` | Bearer key, HMAC-SHA256 with a server-side pepper (hash version 2), with a SHA-256 version 1 path still accepted. Redis cache, then PostgreSQL |
| 3 | Rate and budget limits | `internal/ratelimit` | Redis sliding window plus daily spend. Redis unconfigured fails open; Redis configured but unreachable fails closed |
| 4 | Input validation | `internal/validation` | Size and shape limits before anything downstream |
| 5 | Filter chain | `internal/filter/secrets`, `internal/filter/injection`, `internal/filter/pii` | Runs in that order and stops at the first block. Secrets covers AWS keys, GitHub tokens, private keys, JWTs |
| 6 | Routing and classification gating | `internal/router` | Alias to provider from `configs/models.yaml`, skipping any route whose classification ceiling does not admit the key's clearance, and any provider whose circuit breaker is open |
| 7 | Policy evaluation | `internal/filter/policy` | OPA/Rego, after routing because the rules can see the provider. Hot-reloaded; a failed compile keeps the last good query |
| 8 | Provider call | `internal/router/adapters` | Retry with exponential backoff and jitter (`internal/retry`), watched for client cancellation |
| 9 | Cost, metrics, audit write | `internal/cost`, `internal/telemetry`, `internal/audit`, `internal/storage` | Per-request cost from `configs/pricing.yaml`, Prometheus metrics, and the record described above |

Streaming branches at step 8 into `internal/gateway/streaming_enhanced.go`, an SSE relay with per-chunk and total timeouts, TTFT metrics, and its own cost and usage recording.

### Endpoints

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| GET | `/aegis/v1/health` | No | Health, including whether the mock provider is active |
| POST | `/v1/chat/completions` | Yes | Chat completions — an OpenAI-compatible **subset**, see below |
| GET | `/v1/models` | Yes | List available models |
| GET | `/aegis/v1/audit/events` | Yes | Read the denial and failure record. `?format=csv` to export |
| GET | `/aegis/v1/audit/logs` | Yes | Read the per-request decision record. `?format=csv` to export |

`model`, `messages`, `temperature`, `max_tokens`, `top_p`, `stop` and `stream`
are honoured, as is tool calling: `tools`, `tool_choice`, `parallel_tool_calls`,
`tool_calls` on an assistant message, and `tool_call_id` with the `tool` role.
`message.content` accepts a string or an array of `{"type":"text"}` parts.

Every other field an SDK sends, including `n`, `response_format`, `seed`,
`logprobs`, `logit_bias`, presence and frequency penalties, `store`, `user`,
`metadata` and `stream_options`, is **refused with HTTP 400 naming the field**. It used to be
silently discarded, which meant a request using those options did not fail; it
succeeded with different behaviour than the caller asked for. That is harder to
notice than an error, and it is how tool calling came to be stripped from every
agent request without anything reporting a problem. The full field-by-field
decision is in
[docs/reference/request-field-support.md](docs/reference/request-field-support.md).

Two limits are worth reading before pointing an agent at this: a non-text
content part (an image, audio, a file) is **refused**, because AEGIS cannot scan
it and will not forward what it cannot inspect; and tool calling works on
OpenAI-compatible routes only, so a tool-bearing request routed to Anthropic is
refused rather than served without its tools. Both are in
[known limitations](docs/evidence/known-limitations.md) §2.7 and §2.8.

### Model aliases

The live answer is `curl /v1/models` against a running gateway. The table below is generated from `configs/models.yaml` and therefore reflects the shipped demo configuration, not a recommendation.

<!-- BEGIN GENERATED MODEL TABLE -->
<!-- Generated by scripts/gen-model-table.sh from configs/models.yaml. -->
<!-- Do not edit by hand: run `mise run docs:models`. -->

| Alias | Routes to, in order | Classification ceiling |
|-------|---------------------|------------------------|
| `aegis-balanced` | anthropic `claude-sonnet-5` then openai `gpt-5.6-terra` | CONFIDENTIAL |
| `aegis-fast` | anthropic `claude-haiku-4-5-20251001` then openai `gpt-5.6-luna` | INTERNAL |
| `aegis-gpt4` | *(deprecated alias, routes as `aegis-balanced`)* | CONFIDENTIAL |
| `aegis-reasoning` | anthropic `claude-opus-5` then openai `gpt-5.6-sol` | CONFIDENTIAL |
<!-- END GENERATED MODEL TABLE -->

## Known limits

The audit trail establishes less than an evidence package usually wants it to. These are stated here rather than found during an audit, and in full in [docs/evidence/known-limitations.md](docs/evidence/known-limitations.md).

- **A checkpoint attests event integrity, not policy provenance.** It proves a set of audit events existed, in that order, unaltered since sealing. It does not prove which version of `default.rego` or which gateway configuration was in force when they were produced. Nothing computes a configuration digest; the control plane protocol reserves `ConfigHash` and `PolicyBundles` and actively rejects any v1 submission that populates them ([ADR 0004](docs/adr/0004-reserved-fields-must-not-be-populated.md)).
- **Requests are scanned; responses are not.** The filters run inbound. A secret in a model's output is not detected.
- **Non-text content parts are refused, not filtered.** An image, audio or file part in a content array returns 400. AEGIS cannot read it, and it will not forward to a provider what no filter has inspected.
- **Tool calling works on OpenAI-compatible routes only.** The Anthropic adapter does not carry tools, so a tool-bearing request routed to it is refused with 400 rather than served with its tools removed. Every shipped alias lists an Anthropic provider first.
- **The default policy bundle denies on alias, not on provider trust.** `input.request.provider_type` sees the adapter type rather than the configured provider name, so the `provider_type == "external"` deny rule in `configs/policies/default.rego` never fires.
- **Zero-retention is enforced behaviourally, and only partly structurally.** There is no database constraint that makes a payload column impossible. The guarantee rests on the schema, the two tests above, and the typed column bounds, not on something the database itself refuses.
- **Losing the Redis address is invisible on the health endpoint.** A Redis that is configured but unreachable fails closed, correctly. A Redis whose address was never configured fails open, and health reports that state the same way.
- **Column bounds establish that a value fits, not that it is meaningful.** Bounded widths stop an oversized value from losing the row; they do not validate what it says.

None of these bear on the zero-retention claim itself, and the linked document says so specifically rather than in passing.

## Licence, and where the boundary is

Apache 2.0 ([LICENSE](LICENSE), [NOTICE](NOTICE), [LICENSING.md](LICENSING.md)). No usage restrictions. This repository is the open core: no proprietary code lands here, and nothing here is relicensed.

The rule that governs the boundary is one line: **anything the public zero-retention claim depends on ships free.** That rule has already moved work out of the commercial plan and into this repository rather than the reverse. Hash-chained tamper-evident audit, the audit read API with JSON and CSV export, retention configuration and purge, the compliance framework mapping, and the conformance test asserting no payload is persisted are all here, all Apache 2.0.

A commercial control plane is planned, covering running many gateways rather than one: SSO and directory sync, multi-tenant policy management, cross-gateway aggregation, signed evidence bundles, long-horizon archive, and support with a service level agreement. **It is not built yet, and there is nothing to run.** To join the waitlist or discuss a design partnership, contact [hello@aegisgateway.ai](mailto:hello@aegisgateway.ai).

## More

| | |
|---|---|
| [VERIFICATION.md](VERIFICATION.md) | Every claim on the landing page and in this file, verified against source, with the failures kept in |
| [docs/COMPLIANCE-MAPPING.md](docs/COMPLIANCE-MAPPING.md) | What the audit trail is evidence for, mapped to named articles and controls |
| [docs/evidence/known-limitations.md](docs/evidence/known-limitations.md) | What the audit trail does not establish |
| [docs/AUDIT-INTEGRITY.md](docs/AUDIT-INTEGRITY.md) | The hash chain and how to verify it |
| [docs/QUICKSTART-COMMANDS.md](docs/QUICKSTART-COMMANDS.md) | The canonical command set for the demo |
| [docs/reference/deny-reasons.md](docs/reference/deny-reasons.md) | Every refusal string the gateway can emit |
| [docs/](docs/) | Architecture, configuration, deployment, policies, streaming, retry |
| [demos/](demos/) | Runnable examples: curl basics, streaming, cost tracking, secrets filter, custom policies |
| [CONTRIBUTING.md](CONTRIBUTING.md) | Development setup, mise tasks, environment, internal layout, PR process |
| [.github/SECURITY.md](.github/SECURITY.md) | Reporting a vulnerability. Please do not open a public issue |
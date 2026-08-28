# Changelog

All notable changes to AEGIS AI Gateway are documented here.

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versioning follows [Semantic Versioning](https://semver.org/).

---

## [Unreleased]

> Bookkeeping note, not part of this change: the entries below this heading that
> predate the `v0.1.0` tag describe work that shipped in it, and belong under a
> `## [0.1.0]` heading. Left alone here rather than restructured as a side effect
> of an unrelated change.

### Added
- **Tool calling works on Anthropic routes.** The Anthropic adapter translates the OpenAI tool surface in both directions: `tools[].function.parameters` becomes `input_schema`, `tool_choice: "required"` becomes `{"type":"any"}`, a named function becomes `{"type":"tool"}`, `parallel_tool_calls` becomes `disable_parallel_tool_use` *inside* `tool_choice`, a `role: "tool"` message becomes a `tool_result` block on a user turn, `strict` is carried through to the provider, which accepts it alongside `input_schema` and enforces the schema behind it, and `stop_reason: tool_use` becomes `finish_reason: tool_calls`. Every shipped alias lists an Anthropic provider first, so tool calling was previously unreachable in the configuration the project ships. (`internal/router/adapters/anthropic_tools.go`)

  The mapping was established by probing the live Messages API rather than from the schema as remembered. `scripts/dev/probe_anthropic_tools.py` is the probe and `docs/evidence/anthropic-tool-mapping.md` records what it returned, quoting the provider's own error strings. `internal/router/adapters/anthropic_live_test.go` runs the same assertions against the real API behind a `live` build tag.
- **Per-stream translation state.** `StreamTransformerFactory` gives each streamed response its own transformer. Anthropic numbers every content block in one sequence while OpenAI numbers tool calls in their own, so a response that says a sentence before calling a tool puts the call at content block index 1 while the client expects tool call ordinal 0; relaying the index unchanged hands the client a gap. The adapter is shared across concurrent requests, so that state cannot live on it. Adapters needing no state are unchanged. (`internal/router/adapters/anthropic_stream.go`)
- **Unmappable constructs are refused by name.** A tool call the conversation never answers, anything between a call and its result (a user turn or a further assistant turn), a result answering none of the preceding turn's calls, `strict` without `additionalProperties: false`, and arguments that are not valid JSON each return a 400 naming the construct rather than being approximated or forwarded for the provider to reject opaquely. (`UnmappableError`, `docs/evidence/known-limitations.md` §2.8)

  The handler maps `ErrUnmappable` to a 400 `unmappable_for_provider` carrying the construct. Without that the sentinel existed but nothing read it, so every refusal above reached the caller as a generic 500, which tells an agent the gateway broke and invites it to retry a request that can never succeed.

  `UnmappableError.Construct` is positional by invariant: a message index, a tool index, or a field name, never a value from the request. It is interpolated into the response body and into a log line, and a tool call id is scanned text. Unlike a validation error, this refusal is built after the filter chain has run, so a quoted value has at least been scanned; the reason ids and names stay out is that a position is equally actionable and the invariant is cheaper to hold than to reason about per field. `TestUnmappableConstructsAreNeverScannedText` drives every refusal with a sentinel in each scanned tool field and fails if one appears.
- **OpenAI tool calling.** `tools`, `tool_choice` and `parallel_tool_calls` on the request; `tool_calls` on an assistant message; `tool_call_id` with the `tool` role, which the validator previously rejected. `message.content` accepts a string or an array of `{"type":"text"}` parts. The OpenAI adapter carries all of it to the provider, and streaming tool call deltas are relayed byte for byte so a client can accumulate them by index. (`internal/types/request.go`, `internal/types/tools.go`, `internal/types/content.go`, `internal/router/adapters/openai.go`, `internal/gateway/tool_stream.go`)
- **The filter chain scans the widened surface.** Every text part of a structured content array, the `arguments` string of every tool call, the content of every tool result, and each tool definition's name, description and parameter schema are scanned for secrets, PII and prompt injection. `AegisRequest.TextSegments` is the single definition of what a filter must read, so a future widening cannot add a surface no filter looks at. Tool results in particular are where indirect prompt injection arrives: an agent that fetches a page and returns it to the model is carrying attacker-controlled text into the prompt. (`internal/filter/tool_surface_test.go` plants a canary credential in each surface and asserts both that the request is blocked and that the canary reaches neither the response nor the audit logger.)
- **`ProviderAdapter.SupportsTools()`.** A tool-bearing request routed to an adapter that cannot express tools is refused with 400 `tools_unsupported_by_provider` rather than forwarded with its tools removed. The Anthropic adapter reports false: it does not translate `tool_use` and `tool_result` content blocks. Every shipped alias lists Anthropic first, so this is the common case with a real Anthropic key. Metric `aegis_tool_requests_refused_total`. (`docs/evidence/known-limitations.md` §2.8)
- **Tool metadata in the Rego policy input.** `input.request.tools_offered`, `input.request.tools_called` and `input.request.tool_choice`, plus per-message `tool_calls` and structured `parts`. Names only: a tool name says which capability was exercised, its arguments say what was done with it and are payload. No tool-level enforcement rule ships in `configs/policies`; this is the seam that makes agent governance writable later. (`internal/filter/policy/opa.go`)
- **A reflection test that makes the build enforce filter coverage.** `TestScanSurface_EveryStringFieldIsClassified` walks every string-bearing field reachable from `AegisRequest` and fails on any that is not explicitly classified as scanned or excluded-with-a-reason; `TestScanSurface_ScannedFieldsReachTextSegments` plants a unique sentinel in each scanned field, decodes through the real wire boundary, and fails if a sentinel does not come back out of `TextSegments`. Adding a text channel now breaks the build unless it is either scanned or justified. This exists because three rounds of review each found a channel the previous hand enumeration had missed, and two independent reviewers each caught something the author did not. (`internal/types/scan_coverage_test.go`)
- `docs/adr/0009` and `docs/adr/0010` record two behaviours that shipped without a written decision: dropping an indexless streaming tool call delta, and applying the wire allowlist at every level of the request. The ADR index had also stopped at 0007 while 0008 existed; it is complete again.
- `docs/reference/request-field-support.md`: every field of the OpenAI chat completions schema, whether AEGIS accepts it, and for each refusal what accepting-and-ignoring it would have cost the caller.
- `internal/audit/tool_no_payload_test.go` extends the zero-retention conformance guard to the types tool calling touches, and pins the boundary on `ToolNames`/`CalledToolNames`, the two functions standing between a tool call and the record of it. `no_payload_test.go` and `no_payload_integration_test.go` are unmodified.
- `audit_events` detail columns. The `metadata` JSONB column is replaced by twelve typed, bounded columns: `api_key_prefix`, `limit_dimension`, `limit_value`, `spent_cents`, `limit_cents`, `filter_type`, `reason`, `provider`, `model`, `mode`, `operation`, `error_detail`. No column on the audit table is untyped or unbounded any more. (`migrations/013_promote_audit_metadata`)
- `hash_schema_version=2`, the leaf-hash field set matching those columns, specified in `docs/AUDIT-INTEGRITY.md` §5.1. Integrity coverage is equivalent to version 1, which hashed the JSONB object; the gain is typing and bounding, not more signed data.
- `docs/evidence/demo-run-checklist.md`: what to run, and which five log lines decide, for the one demo run that would settle the "all six demos are runnable" claim.
- `TestSchemaLimitsMatchMigration` parses the migrations and fails if the column widths and the Go constants disagree, because that drift is silent in the direction that matters: Go clips to the larger number and PostgreSQL rejects the row.

### Fixed
- **Every streamed request recorded zero spend.** A streamed completion persisted `prompt_tokens=0`, `completion_tokens=0` and `estimated_cost_usd=0.00` while the identical non-streamed request recorded real figures. The daily spend budget and the per-team cost aggregates are computed from those rows, so streamed traffic cost real money and moved no budget. Reproduced against a live gateway before and after.

  Two independent causes, one per adapter. **Anthropic** reports usage natively, on `message_start` and `message_delta`, and the stream translation relayed neither, so the gateway's usage extraction found nothing. **OpenAI** omits usage from a stream unless asked via `stream_options`, which AEGIS refuses on the way in; the gateway now sets it itself on every streamed request. That is the gateway asking for its own accounting data, not a client field being honoured: a caller must not be able to switch off the tracking its own spend limit is computed from. `stream_options` stays refused inbound.

  A third fault sat behind the first: with the token counts fixed, cost was still zero, because the translated chunks carried no `model` and the pricing lookup had nothing to key on. The served model now rides on the emitted chunks as it does from OpenAI.
- **`parallel_tool_calls: false` told Anthropic the opposite.** OpenAI's `parallel_tool_calls` and Anthropic's `disable_parallel_tool_use` are negations of each other, and the first implementation assigned the incoming pointer straight across, emitting `disable_parallel_tool_use: false`. Nothing about the types objected; a live call returning two tool calls is what caught it.
- **The Anthropic adapter never set `anthropic-version`.** The header is required by the API, and it was supplied only by the `headers` block in `configs/providers.yaml`, so one deleted line there would have broken every Anthropic request with the adapter itself content to send them. The adapter now defaults it and operator config still overrides.
- **The authenticated identity fields were settable in the request body, and are not any more.** `types.AegisRequest` was both the wire type and the internal type, so its `json` tags were live input: a client could send `classification`, `organization_id`, `team_id`, `user_id`, `api_key_id`, `request_id`, `project`, `prefer_provider`, `trace_context` or `skip_cache` in the body and the decoder would bind them.

  **This was not exploitable.** Every one of those fields is overwritten from the authenticated API key or from a header immediately after decoding, before validation, filtering, routing or policy evaluation sees the request. A body-supplied `classification: "RESTRICTED"` never reached the classification ceiling check. No deployment could have been used to escalate a clearance, reattribute spend, or forge a request id, and the audit trail is unaffected.

  It is recorded here anyway. The gap between "overwritten in practice" and "cannot be set" is the kind of distance that closes only by accident: any future refactor that read one of those fields before the overwrite, or that added a field to the struct without noticing the tags were public, would have turned a latent namespace collision into a live one. It is now closed structurally rather than behaviourally: decoding goes through a separate wire type, and all ten names are refused with a 400 explaining where the value actually comes from. `TestScanSurface_ExclusionsAreJustified` re-derives that refusal from the decoder on every run rather than trusting a comment.
- **Tool calling was silently stripped from every request.** `tools`, `tool_calls` and `tool_call_id` were absent from the structs in `internal/types/request.go`, so `json.Unmarshal` discarded them. The provider received a tool-less request and answered in prose; the client's agent loop stalled with no error anywhere. Every agent task that used tools was affected and nothing in the system reported a problem.
- **IPv6 clients failed authentication without leaving an audit row.** `audit_events.ip_address` was `VARCHAR(45)`, sized for the longest IPv6 literal, but it is written from Go's `RemoteAddr`, which is `host:port` with the host bracketed. An ordinary full-form IPv6 address plus a port is 47 characters and the widest is 53. PostgreSQL errors on `varchar` overflow rather than truncating, and the audit writer could only log that error, so the row was discarded. `LogAuthFailure` is on that path and is reachable unauthenticated. The column is now `VARCHAR(64)`, and every value is clipped in Go before the insert so that a long value is recorded truncated rather than costing the row. (`migrations/012_bound_audit_text_columns`, `internal/audit/limits.go`)
- `demos/05-custom-policies` named three competing projects in text a user reads: a Rego array the script prints to the terminal, and a demo prompt. The policy is now `restricted-terms.rego` over invented project codenames. Shared rule 3.
- `docs/reference/deny-reasons.md` gave the secrets pattern name as `aws_access_key`. The names are title-cased words such as `AWS Access Key`; all seven are now listed.
- `scripts/check-citations.sh` silently skipped any citation not pinned to a full 40-character commit, so a citation pinned to a short SHA or to a tag passed review looking checked. Both are now errors, and a citation's `file:line` label must agree with its own `#L` anchor.

### Changed
- **Validation errors no longer echo the values they are about.** `TextSegment.Ref` is interpolated into a validation error's field label, and that label goes to both the client response body and the structured log line. `Ref` carried the tool call id and the tool name, which was safe only while neither was scanned. Once both became scanned text it was a leak, and validation runs before the filter chain, so it fired before anything had looked for a secret: a request whose correlator held a credential and whose sibling field held a control character returned a 400 quoting the credential. `Ref` is now always positional, which also makes the labels precise: two tool calls in one message previously produced the identical label `messages[0].tool_calls[].id`, and now produce `[0]` and `[1]`. `TestSegmentRefsAreNeverScannedText` and `TestValidationErrorsDoNotEchoScannedValues` hold the line.
- **Tool call correlators are now scanned, and bounded.** `messages[].tool_call_id` and `messages[].tool_calls[].id` were excluded from the scan surface as "an opaque correlator the provider issued". That is true of the outbound leg only: an agent loop resends the whole conversation on every turn, so the gateway receives whatever the client put in those fields, and they are marshalled to the provider with the rest of the message. They were doubly easy to miss because they already appeared in `TextSegments` as the `Ref` label on other segments, and a `Ref` is metadata about where a finding was rather than text that gets scanned.

  **Nothing bounded their length.** A 100,005-character tool call id passed validation and was forwarded to the provider. Every other client-controlled string on the request had a limit; this one was missed because it reads like a value the gateway issued. `MaxToolCallIDLength` is now 128, roughly four times the ~30 characters OpenAI and Anthropic actually issue. **A request with an over-long correlator now returns 400 where it previously succeeded**, and one containing a credential returns 451. `TestProviderShapedToolCallIDsPass` asserts real provider-issued shapes are unaffected.
- **Stop sequences are now scanned for secrets, PII and prompt injection.** `stop` is client-supplied text forwarded verbatim to the provider, as `stop` on OpenAI routes and `stop_sequences` on Anthropic ones, and the validator bounds only how many and how long: four at 256 characters is a kilobyte of arbitrary content. It had been excluded from the scan surface on the grounds that a stop sequence is never part of the prompt, which is true and answers the wrong question. The model not reading a value has no bearing on whether the value leaves the gateway. **A request whose stop sequence contains a credential now returns 451 where it previously succeeded.** Ordinary delimiters are unaffected, which `TestOrdinaryStopSequencesStillPass` asserts against the real filter chain. This was the first thing the new reflection coverage test found that hand enumeration had not.
- **`scripts/check-citations.sh` walks every Markdown file** instead of four root files plus `docs/`. It had been reporting success over a scope that excluded `.github/`, `CONTRIBUTING.md`, `LICENSING.md` and all seven demo READMEs. None carried a citation at the time, so "found nothing" and "did not look" were indistinguishable, which is the dangerous shape for any check whose success condition is absence. A demo README is user-facing documentation and is exactly where a link to `main` would be added without thought.
- **BREAKING: the gateway refuses request fields it does not support, instead of discarding them.** Decoding is now an explicit allowlist (`internal/types/chat_request.go`). A body carrying `n`, `response_format`, `seed`, `logprobs`, `logit_bias`, presence or frequency penalties, `max_completion_tokens`, `store`, `metadata`, `user`, `stream_options`, the deprecated `functions`/`function_call`, or any unrecognised key returns **HTTP 400 with `"code": "unsupported_field"` naming the field**. So do AEGIS's own `classification`, `organization_id`, `project` and `skip_cache`, which were part of the request namespace only because the wire type and the internal type were one type; they were parsed and then overwritten, so setting them was already a silent no-op.

  **This rejects requests that previously returned 200.** That is the point. Those requests were already not doing what the caller asked: a client sending `seed` got no determinism, a client sending `n: 3` got one completion, a client sending `tools` got an answer in prose. Silent acceptance of an unsupported field is the failure that produced the tool-calling bug, and an allowlist is what prevents the next one. The remedy is to remove the field, and the error message names it. Full table with per-field reasoning in `docs/reference/request-field-support.md`. Metric `aegis_unsupported_field_total`.
- **BREAKING: non-text content parts are refused.** Widening `message.content` to accept an array made image, audio and file parts expressible for the first time. They return 400 `unsupported_content_part` rather than being forwarded. AEGIS cannot scan an image, so an image part would be an egress path to a provider that the secrets, PII and injection filters do not cover; admitting one as a side effect of a compatibility fix would have put a hole in the claim the product is built on. Multimodal support needs its own decision. (`docs/evidence/known-limitations.md` §2.7)
- **`prefer_provider` and `skip_cache` are removed, not documented.** `X-Aegis-Prefer-Provider` was read into the request and never consulted by `router.ResolveRoute`; `skip_cache` was parsed and never read by anything. Both are gone from the type and from the handler, and both wire names are still refused by the decoder with a message saying they were removed, so an existing caller gets an explanation rather than a generic rejection. Documenting an inert field describes a capability the code lacks.
- `types.Message.Content` is `types.Content` rather than `string`, and `types.Message` carries `ToolCalls` and `ToolCallID`. Callers that read content as a string use `Content.Flatten()`; filters use `TextSegments` instead, because joining parts could manufacture a match that exists in neither.
- Per-message length and control-character validation now measures every text-bearing element of a message, so a message whose size sits in tool call arguments is bounded the same way as one whose size sits in its content.
- **Audit read API response shape.** `GET /aegis/v1/audit/events` returns the twelve typed fields instead of a `metadata` object, in both JSON and CSV. A response still presenting a `metadata` object would describe storage that no longer exists.
- `audit_events.error_message` is `VARCHAR(128)` and `user_agent` is `VARCHAR(256)`, from `TEXT`. These bounds mean the columns cannot hold a document, a conversation or a transcript, and the limit is visible in the schema rather than asserted in prose. They do not make storing a prompt impossible: no bound both excludes every prompt and fits a real browser user agent. See `docs/evidence/known-limitations.md` §2.6.
- `audit_logs.filter_results` dropped. No code path ever wrote it.
- Every citation in this repository is re-pinned to `ea72971`, the commit `v0.1.0` names. Citations name the commit rather than the tag: a tag is a moving pointer, and `v0.1.0` has already been deleted and recreated on a different commit once.

### Migration notes
- **Migration 013 refuses to run in a database holding `hash_schema_version=1` checkpoints.** A version-1 leaf hash cannot be recomputed once `metadata` is gone, so dropping it under an existing chain would leave every sealed checkpoint permanently unverifiable. The check runs before any DDL, so a refusal leaves the schema untouched; the migrator still marks the version dirty, which is cleared with `UPDATE schema_migrations SET version=12, dirty=false`. To proceed, verify and archive the existing chain first.
- Migration 012's down does not narrow `ip_address` back to 45 characters. Reinstating a width that discards audit rows is a regression rather than a rollback.

### Added
- Audit read API: `GET /aegis/v1/audit/events` and `GET /aegis/v1/audit/logs`, both authenticated, with `?format=csv` for export and id-based paging. Every query is scoped to the calling key's organization inside the reader rather than by a filter the handler applies, so a query that omits the scope cannot be constructed. A key carrying no organization is refused rather than served an unscoped query. (`internal/audit/reader.go`, `internal/gateway/audit_handler.go`)
- `docs/COMPLIANCE-MAPPING.md`: maps artifacts the gateway produces to references an assessor is likely to raise (EU AI Act, ISO/IEC 27001 Annex A, SOC 2, GDPR). Each row states which question the artifact helps answer, never that it discharges an obligation.
- `VERIFICATION.md`: claim-by-claim verification of the landing page and README against source, with per-claim verdicts and permalinks pinned to a commit.
- `docs/reference/deny-reasons.md`: every deny, refusal and policy-violation string the gateway can emit, with trigger, status code, originating stage and operator action.
- `docs/evidence/known-limitations.md`: what the audit trail does not establish, including the checkpoint provenance gap.
- `TestShippedDefaultPolicy_CanActuallyDeny` loads the real policy bundle and asserts it denies. Every other test in that file builds its own inline fixture, which is how the defect below survived.
- `TestNoPayload_AuditReadAPIStructs` extends the no-payload reflection guard to the types the read API serialises.

### Fixed
- The shipped default policy bundle could not deny anything. Its only rule required `input.request.provider_type == "external"`, and that field is set from `adapter.Name()`, which returns `"openai"` or `"anthropic"` and never `"external"`: `azure_openai` and `internal_vllm` both route through the OpenAI adapter and both report `"openai"`. The rule compiled, read correctly, and was unreachable. It now gates on the model alias against an operator-controlled allowlist, empty as shipped.
- The zero-retention canary skipped silently when `TEST_DATABASE_URL`, `TEST_SERVER_URL` or `TEST_API_KEY` were absent, so a pipeline where it skipped was indistinguishable from one where it passed. It now fails and names the missing variables; the only way to not run it is `AEGIS_SKIP_INTEGRATION=1`. The CI step additionally greps for the pass line by name, because `go test -run` exits 0 when its pattern matches nothing.
- `README.md` named two competing projects, and described the commercial control plane in present tense. Both are corrected.

### Changed
- Relicensed from Business Source License 1.1 to Apache License 2.0. The gateway is now fully open source with no usage restrictions. Commercial value moves to the separately-developed control plane.
- Clarified the open-core boundary: hash-chained tamper-evident audit (Merkle checkpoints, T24), audit read API with JSON/CSV export (T23, implemented in this release; the line above previously asserted it before the code existed), retention configuration and purge (T20), compliance mapping document (T19), and the no-payload conformance test (T22) are all confirmed in the Apache core. The "zero-retention governance" claim now rests on verifiable open-source code, not marketing. Multi-tenant console, SSO, policy pack library with lifecycle management, signed auditor-ready evidence bundles, long-horizon WORM archive, and SLA-backed support remain commercial-tier features planned for a future release.

---

## [0.1.0] — 2026-08-20

Initial public release of AEGIS AI Gateway.

### Added

#### Core Gateway
- OpenAI-compatible REST API (drop-in replacement for `/v1/chat/completions`, `/v1/models`)
- Multi-provider routing: OpenAI, Anthropic, Azure OpenAI, vLLM
- Streaming support (SSE) with provider-transparent passthrough
- Request/response transformation and normalization

#### Authentication & Authorization
- API key management with hashed storage (SHA-256)
- Classification-based access control (public / internal / confidential / restricted)
- OPA (Open Policy Agent) policy engine integration
- Provider-type and model-type routing policies

#### Security & Compliance
- Secrets scanning (AWS keys, GitHub tokens, JWTs, private keys, and more)
- Prompt injection detection (heuristic-based)
- PII filtering via gRPC-connected NLP filter service
- Audit logging for all requests and policy decisions
- Input validation and sanitization

#### Reliability
- Circuit breakers per provider
- Retry logic with exponential backoff
- Provider health checks
- Rate limiting with configurable budget tracking
- Request-level cost tracking and budget enforcement

#### Observability
- Prometheus metrics (latency, token counts, cost, error rates)
- Structured JSON logging
- Request tracing with correlation IDs
- Cost tracking per API key and provider

#### Operations
- Database migrations (PostgreSQL via golang-migrate)
- Docker multi-stage build (Alpine-based, non-root)
- Docker Compose quickstart (includes PostgreSQL 16, Redis 7, filter service, Open WebUI)
- Makefile + mise task runner for common dev workflows
- Hot-reloadable YAML configuration

#### Developer Experience
- Quickstart demo with pre-seeded API key
- curl-based demo scripts (basic, streaming, cost tracking)
- `.env.example` with all supported configuration options
- CI pipeline: lint, unit tests, integration tests, build (GitHub Actions)

---

## License

AEGIS AI Gateway is open source under the [Apache License 2.0](LICENSE).

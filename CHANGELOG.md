# Changelog

All notable changes to AEGIS AI Gateway are documented here.

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versioning follows [Semantic Versioning](https://semver.org/).

---

## [Unreleased]

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

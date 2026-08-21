# Changelog

All notable changes to AEGIS AI Gateway are documented here.

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Versioning follows [Semantic Versioning](https://semver.org/).

---

## [Unreleased]

### Changed
- Relicensed from Business Source License 1.1 to Apache License 2.0. The gateway is now fully open source with no usage restrictions. Commercial value moves to the separately-developed control plane.
- Clarified the open-core boundary: hash-chained tamper-evident audit (Merkle checkpoints, T24), audit read API with JSON/CSV export (T23), retention configuration and purge (T20), compliance mapping document (T19), and the no-payload conformance test (T22) are all confirmed in the Apache core. The "zero-retention governance" claim now rests on verifiable open-source code, not marketing. Multi-tenant console, SSO, policy pack library with lifecycle management, signed auditor-ready evidence bundles, long-horizon WORM archive, and SLA-backed support remain commercial-tier features planned for a future release.

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

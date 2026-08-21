# Licensing

## What's free, permanently

The AEGIS AI Gateway is Apache 2.0 — free for any use, including commercial production.

The Apache core includes:

- **Gateway core** — multi-provider routing (OpenAI, Anthropic, Azure OpenAI, vLLM), streaming, request normalization
- **Auth and access control** — API key management, classification-based policies, OPA integration
- **Security filters** — secrets scanning, PII filtering, prompt injection detection
- **Audit logging** — structured logs for every request and policy decision
- **No-payload conformance test** — verifiable proof that request bodies are not stored in the audit trail (T22)
- **Observability** — Prometheus metrics, structured logging, request tracing

Committed to the open core, but **not yet shipped** — no release contains them
today, and nothing above should be read as available:

- **Hash-chained tamper-evident audit** — Merkle checkpoints, so a deployment can prove its own audit trail has not been altered (T24) — in progress
- **Retention configuration and purge** — set how long audit records are kept and remove them on schedule (T20) — in progress
- **Audit read API** — JSON and CSV export for pulling records for review (T23) — planned
- **Compliance mapping** — documentation mapping gateway controls to common regulatory frameworks (T19) — planned

No usage cap. No time limit. No SaaS carve-out. If you run it, modify it, or build a product on top of it, the Apache 2.0 terms apply — see [LICENSE](./LICENSE).

## What's commercial

The commercial tier is planned for teams that need organizational-scale operations. It will include:

- **Multi-tenant management console** and cross-gateway aggregation — planned
- **SSO** with SAML, OIDC, and SCIM — planned
- **Policy pack library** with approval workflows, versioning, staged rollout, and policy approval audit trail — planned
- **Signed auditor-ready evidence bundles** with framework mapping and chain verification report — planned
- **Long-horizon WORM archive** with external anchoring (RFC 3161 / notary integration) — planned
- **Support with SLA and indemnity** — planned

None of this is in the repo today. Interested? Email [komlan@atlanticfrontier.com](mailto:komlan@atlanticfrontier.com)

## Why this split

The Apache core is enough for one deployment to prove its own governance posture — you can verify the audit trail, export records, and demonstrate that no request payloads are retained. The commercial tier adds the tooling organizations need when they are running multiple gateways, aggregating reports across teams, or handing evidence to an external auditor.

## License

The gateway is [Apache 2.0](./LICENSE).

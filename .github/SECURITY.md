# Security Policy

## Supported Versions

| Version | Supported |
|---------|-----------|
| 0.1.x   | ✅ Yes    |

## Reporting a Vulnerability

**Do not open a public GitHub issue for security vulnerabilities.**

Email us at **komlan@atlanticfrontier.com** with:

- A description of the vulnerability
- Steps to reproduce
- Potential impact assessment
- Any suggested mitigations (optional)

We will acknowledge receipt within 2 business days and aim to provide a fix or mitigation within 14 days depending on severity.

We ask that you follow responsible disclosure and give us reasonable time to address the issue before any public disclosure.

## Scope

In scope:
- Authentication and authorization bypass
- Secrets leakage (API keys, credentials in responses or logs)
- Prompt injection that bypasses classification controls
- Remote code execution
- Privilege escalation

Out of scope:
- Issues in third-party dependencies (report to the upstream project)
- Denial of service via excessive legitimate load
- Issues requiring physical access to the host

## Security Design

AEGIS includes several built-in security layers applied to **inbound requests**:

- **Secrets scanning** (`internal/filter/secrets`) — inbound request content is scanned for credentials (AWS keys, GitHub tokens, private keys, JWTs, and more) before the request reaches a provider
- **Prompt injection detection** (`internal/filter/injection`) — heuristic detection of jailbreak and injection attempts in request content
- **PII filtering** (`internal/filter/pii`) — request content is checked against a gRPC-backed NLP filter service
- **Classification gating** (`internal/router`) — requests are gated based on the API key's assigned classification level before routing
- **OPA policy evaluation** (`internal/filter/policy`) — every request is evaluated against Rego policies; allow/deny decisions are logged
- **Audit logging** (`internal/audit`) — all requests and policy decisions are written to `audit_logs` and `audit_events`
- **API key hashing** (`internal/auth`) — keys are never stored in plaintext; the scheme is versioned:
  - **`hash_version=2` (default for new keys):** HMAC-SHA256 with a server-side pepper (`AEGIS_KEY_PEPPER`, minimum 32 characters). The gateway refuses to start if the pepper is unset or shorter than 32 characters.
  - **`hash_version=1` (legacy):** plain SHA-256, retained for backward compatibility with keys created before the pepper scheme. Legacy keys are still verified correctly.

**Note:** Response-side egress scanning (checking provider responses for credential leakage) is not yet implemented. A tracking issue covers the design. Until it lands, treat AEGIS as an inbound governance layer.

For production deployments, always review the [deployment guide](docs/DEPLOYMENT.md) for secure defaults.

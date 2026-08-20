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

AEGIS includes several built-in security layers:

- **Secrets scanning** — outbound responses are scanned for leaked credentials
- **Prompt injection detection** — heuristic detection of jailbreak/injection attempts
- **Classification gating** — requests are classified and routed based on sensitivity level
- **Audit logging** — all requests and policy decisions are logged
- **API key hashing** — keys are stored as SHA-256 hashes, never in plaintext

For production deployments, always review the [configuration guide](docs/DEPLOYMENT.md) for secure defaults.

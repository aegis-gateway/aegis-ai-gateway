# Licensing

## The gateway is Apache 2.0 — free for any use, including commercial

The AEGIS AI Gateway in this repository is released under the **Apache License 2.0**.

You can use it, modify it, and deploy it for any purpose — including commercial production use — without paying Atlantic Frontier anything. There is no usage cap, no time limit, no SaaS carve-out, no "non-commercial only" clause. The full license text is in [LICENSE](./LICENSE).

If you want to run this at your company: go ahead. Deploying it — modified or not, in production, behind a paid service — is not redistribution, so the conditions below do not apply. No lawyer required.

### If you redistribute it

Apache 2.0 attaches conditions to *distributing* the gateway or a derivative work, not to running it. If you ship it to someone else, [LICENSE](./LICENSE) section 4 asks you to:

- include a copy of the Apache 2.0 license;
- mark any files you changed as changed;
- keep the existing copyright, patent, trademark, and attribution notices; and
- carry the attribution text from [NOTICE](./NOTICE) along with it.

These are the ordinary Apache 2.0 obligations — the same ones you already meet when redistributing any Apache-licensed dependency.

## What the commercial control plane adds

The **AEGIS Control Plane** is a separate, closed-source product not included in this repo. It is built for teams that need managed multi-tenant operations and compliance tooling on top of the open-source gateway. It adds:

- **Multi-tenant management console** — manage multiple teams, API keys, and policy sets from a single interface
- **SSO / enterprise identity integration** — SAML, OIDC, and directory sync
- **Pre-built compliance policy packs** — ready-made policy sets for common regulatory frameworks
- **Long-horizon tamper-evident audit** — an immutable audit trail extending well beyond what the gateway stores locally
- **Evidence export** — structured audit exports for compliance reviews and regulatory reporting
- **SLA-backed support** — guaranteed response times and escalation paths

None of this is in this repo. The open-source gateway is fully functional without any of it.

## Contributor License Agreement

Contributions to this repository require a signed CLA; see [`docs/cla/`](./docs/cla/).

## Contact

For commercial licensing or control plane access:

- Email: [komlan@atlanticfrontier.com](mailto:komlan@atlanticfrontier.com)
- Web: [aegisgateway.ai](https://aegisgateway.ai)

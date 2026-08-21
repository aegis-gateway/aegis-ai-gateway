# Contributing to AEGIS AI Gateway

Thank you for your interest in contributing. This document covers how to get involved.

## License

By contributing to AEGIS, you agree that your contributions will be licensed under the **Apache License 2.0** — the same license that covers this repository. There is no other inbound license; contributions are not subject to any proprietary or source-available license. All contributions are also subject to the AEGIS Individual Contributor License Agreement (CLA). See [docs/cla/](docs/cla/) for details.

## Getting Started

### Prerequisites

- Go 1.22+
- Docker and Docker Compose
- [mise](https://mise.jdx.dev/) (recommended task runner)
- PostgreSQL 16+ and Redis 7+ (or use the provided Docker Compose services)

### Setup

```bash
git clone https://github.com/aegis-gateway/aegis-ai-gateway.git
cd aegis-ai-gateway
cp .env.example .env          # fill in your provider API keys
mise run setup                # install dependencies
mise run services:up          # start PostgreSQL + Redis
mise run db:migrate           # run migrations
mise run dev                  # start the gateway
```

### Running Tests

```bash
mise run test      # unit + integration tests
mise run lint      # golangci-lint
```

## How to Contribute

### Reporting Bugs

Open an issue using the [bug report template](.github/ISSUE_TEMPLATE/bug_report.md).
Please include version, steps to reproduce, and relevant logs. Strip any API keys or credentials before pasting.

### Suggesting Features

Open an issue using the [feature request template](.github/ISSUE_TEMPLATE/feature_request.md).
Discuss the approach before opening a large PR — it saves everyone time.

### Submitting Pull Requests

1. Fork the repo and create a branch from `main`.
2. Make your changes with clear, focused commits.
3. Ensure `mise run test` and `mise run lint` pass.
4. Open a PR against `main` using the PR template.
5. A maintainer will review and provide feedback.

### Accuracy checklist for security-sensitive packages

If your PR touches `internal/auth/`, `internal/filter/`, or `internal/audit/`, you **must** verify before merging:

- [ ] `.github/SECURITY.md` still accurately describes the key-hashing scheme, filter behaviour, and audit logging
- [ ] The feature list in `README.md` still matches what the code actually does

This check exists because documentation drift in these packages has occurred more than once and can mislead operators into misconfiguring production deployments.

### Commit Messages

Follow [Conventional Commits](https://www.conventionalcommits.org/):

```
feat: add support for Gemini provider
fix: correct token count for streaming responses
chore: update golangci-lint to v1.58
docs: add deployment guide for Fly.io
```

## Code Guidelines

- Keep packages focused and well-named.
- Write tests for new behavior.
- Avoid hardcoding credentials or environment-specific values.
- Prefer explicit error handling over panics.
- Run the linter before pushing — CI will catch it anyway.

## Security Issues

Do not open a public issue for security vulnerabilities.
See [SECURITY.md](.github/SECURITY.md) for the responsible disclosure process.

## Questions

Open a [GitHub Discussion](https://github.com/aegis-gateway/aegis-ai-gateway/discussions) for general questions.

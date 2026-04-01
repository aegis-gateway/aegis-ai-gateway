# 04 — Secrets Filter

Demonstrates the built-in secrets scanner.

## What it shows

- AWS access keys blocked (AKIA...)
- GitHub tokens blocked (ghp_...)
- Private keys blocked (-----BEGIN RSA PRIVATE KEY-----)
- JWTs blocked (eyJ...)
- Audit events logged to `audit_events` table
- Clean requests pass through unmodified

## Status

Planned.

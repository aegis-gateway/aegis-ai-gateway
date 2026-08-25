# Compliance mapping

**AEGIS does not make you compliant, and no gateway can.** This document maps what the
gateway *produces* to obligations and controls that reference it. Deciding whether that
evidence satisfies your obligations is your compliance team's work, and this document
exists so they can start rather than reverse-engineer it from source.

Verified against commit
[`c74fa7a`](https://github.com/aegis-gateway/aegis-ai-gateway/tree/ea72971186eb5c316966b065bf710f2d85f578b1),
which is the commit that introduces the audit read API. An earlier draft pinned these rows
to `0344929`, where `internal/audit/reader.go` does not exist, so the citation for the
export claim resolved to a missing file. Caught in review.
Every row cites a package, file, or test. A row with no citation is not in this table.

## How to read this

Each row says: here is an artifact the gateway emits, here is where it comes from, and
here is the obligation or control that an assessor is likely to point at when they ask
for it. The third column is **the question the artifact helps answer**, not a claim that
the artifact discharges the obligation.

Nothing in this document should be quoted as "AEGIS is compliant with X". If you need a
sentence to paste into an internal document, use this one: *AEGIS produces a per-request
decision record, retained without request or response content, which we use as evidence
for X.*

## What the gateway produces

| Artifact | Where it comes from | What it helps answer |
|---|---|---|
| Per-request decision record: identity, model, provider, classification, outcome, timing, tokens, cost | [`internal/audit/logger.go`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/ea72971186eb5c316966b065bf710f2d85f578b1/internal/audit/logger.go), tables `audit_logs` and `audit_events` | Can you show what an automated system did, for a given request, after the fact |
| Denial record for every refusal, with the reason string and the stage that produced it | `LogFilterBlock`, `LogRateLimitViolation`, `LogRedisFailure` in [`internal/audit/logger.go`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/ea72971186eb5c316966b065bf710f2d85f578b1/internal/audit/logger.go) | Can you show that a control fired, and why |
| Records readable and exportable as JSON or CSV, scoped to one organization | [`internal/audit/reader.go`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/ea72971186eb5c316966b065bf710f2d85f578b1/internal/audit/reader.go), `GET /aegis/v1/audit/events`, `GET /aegis/v1/audit/logs` | Can you hand an assessor the record without giving them database access |
| Merkle checkpoints over event ranges, chained to their predecessor (RFC 6962) | [`internal/audit/checkpoint`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/ea72971186eb5c316966b065bf710f2d85f578b1/internal/audit/checkpoint) | Can you show a record has not been altered or deleted since it was sealed |
| Inclusion proofs for a single event within a sealed range | [`checkpoint/verifier.go`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/ea72971186eb5c316966b065bf710f2d85f578b1/internal/audit/checkpoint/verifier.go) | Can you prove one specific decision was in the sealed set |
| Policy evaluated as code, versioned in the repository | [`internal/filter/policy`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/ea72971186eb5c316966b065bf710f2d85f578b1/internal/filter/policy), [`configs/policies/`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/ea72971186eb5c316966b065bf710f2d85f578b1/configs/policies) | Can you show the rule that was in force, and review changes to it |
| Classification gating per route | [`internal/router/provider.go`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/ea72971186eb5c316966b065bf710f2d85f578b1/internal/router/provider.go) | Can you show data of a given class could not reach an unapproved model |
| Credential, PII, and prompt-injection screening on input | [`internal/filter`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/ea72971186eb5c316966b065bf710f2d85f578b1/internal/filter) | Can you show what screening ran before a call left your perimeter |
| Retention configuration and purge, with a purge record | [`internal/purge`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/ea72971186eb5c316966b065bf710f2d85f578b1/internal/purge), table `audit_purges` | Can you show records were deleted on schedule, and which |
| A conformance test asserting no request or response content is retained | `TestNoPayload_CanaryEndToEnd`, `TestNoPayload_SchemaIntrospection` in [`internal/audit`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/ea72971186eb5c316966b065bf710f2d85f578b1/internal/audit) | Can you show the evidence store is not itself a copy of the data |

## Frameworks an assessor is likely to reference

These are pointers, not mappings performed on your behalf. Article and control numbers
are given so your compliance team can find the text; the wording of the obligation is
theirs to read, and its applicability to you is theirs to determine.

### EU AI Act

| Reference | Subject | Which artifacts above are relevant |
|---|---|---|
| Article 12 | Record-keeping and automatic logging over the lifetime of a system | Decision record, denial record, checkpoints, export |
| Article 14 | Human oversight | Denial record and deny-reason catalogue, which give an operator a reason they can act on |
| Article 26 | Obligations of deployers, including keeping logs | Decision record, retention configuration and purge |
| Article 50 | Transparency obligations | Not addressed by the gateway. Recorded here so its absence is explicit |

The gateway is infrastructure a deployer runs. Whether any of these articles bind you,
and in what role, is a question about your system and your organisation, not about
AEGIS.

### ISO/IEC 27001:2022 Annex A

| Control | Subject | Which artifacts above are relevant |
|---|---|---|
| A.8.15 | Logging | Decision record, denial record |
| A.8.16 | Monitoring activities | Prometheus metrics ([`internal/telemetry`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/ea72971186eb5c316966b065bf710f2d85f578b1/internal/telemetry)), denial record |
| A.5.33 | Protection of records | Merkle checkpoints, inclusion proofs |
| A.8.10 | Information deletion | Retention configuration and purge |
| A.8.12 | Data leakage prevention | Credential and PII screening on input. **Input only**, see limits below |

### SOC 2 trust services criteria

| Criterion | Subject | Which artifacts above are relevant |
|---|---|---|
| CC6.1 | Logical access controls | API key authentication, per-key model allowlists ([`internal/auth`](https://github.com/aegis-gateway/aegis-ai-gateway/blob/ea72971186eb5c316966b065bf710f2d85f578b1/internal/auth)) |
| CC7.2 | Monitoring for anomalies | Denial record, rate-limit and budget violations |
| CC7.3 | Evaluation of security events | Denial record with reason and stage |
| P4.2 | Retention and disposal of personal information | Purge, plus the absence of payload retention |

### GDPR

Article 5(1)(c), data minimisation, is the obligation the product's design is organised
around: the decision record is retained and the content is not. Article 30, records of
processing, is a documentation obligation about your processing, not something a gateway
produces for you, though the decision record evidences what processing occurred.

**A caution worth stating.** The audit trail retains `organization_id`, `team_id`,
`user_id`, `api_key_id` and `ip_address`. Those are likely personal data in your
jurisdiction even though no prompt content is stored. The absence of payload does not put
the audit trail outside the scope of data protection law, and treating it as though it
does would be a mistake this document is trying to prevent.

## Limits that bear directly on this mapping

Read these with the table. Each is documented in full in
[`docs/evidence/known-limitations.md`](evidence/known-limitations.md).

1. **Requests are screened; responses are not.** Every filter implements `ScanRequest`
   and runs on input. There is no output filtering. Any control you map to A.8.12 or
   similar covers the inbound direction only.
2. **A checkpoint attests event integrity, not policy provenance.** It proves a record
   is unaltered since sealing. It does **not** bind that record to the policy bundle or
   configuration in force when the decision was made, because the gateway computes no
   configuration digest and Rego bundles are neither versioned nor digested. If your
   assessor asks "under which version of the rule was this decided", the evidence does
   not answer it today.
3. **Zero retention is enforced behaviourally, not structurally.** No code path writes
   payload to the audit tables, a static test rejects payload-named columns, and an
   end-to-end canary asserts at runtime that a planted string reaches no audit row. The
   schema itself does not make payload storage impossible.
4. **The gateway does not attest to what the provider does** with a request after it
   leaves. The record ends at the boundary.

## Keeping this document honest

If a row here stops being true, that is a defect of the same severity as a broken
build. The citations are pinned to a commit, so an assessor can check any row against the
source as it was when this was written. Re-pin them when a release is tagged.

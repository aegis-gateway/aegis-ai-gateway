# Architecture decision records

One file per decision, numbered, never renumbered. A decision that is
superseded keeps its file and gains a status line pointing at the record that
replaced it, because the reasoning behind a reversed decision is usually the
reason the reversal was right.

**Read this directory at the start of a session, before asking anything.** It
exists so that a decision already taken costs a re-read rather than a
re-argument. Where a record captures a disagreement, both positions are stated
and the one that stands is marked.

The AEGIS Control Plane keeps its own `docs/adr/` for decisions internal to
that service. Decisions about the wire protocol live here, in the repository
that publishes it.

## Format

```
# NNNN. Short title

Status:   Accepted | Superseded by NNNN | Proposed
Date:     YYYY-MM-DD
Decision: one sentence, in the imperative

## Context
What was true that made this a decision rather than an obvious step.

## Decision
What was chosen.

## Consequences
What this costs, and what it forecloses.
```

## Records

| Record | Title | Status |
|---|---|---|
| [0001](0001-protocol-lives-in-the-gateway-repo.md) | The wire protocol lives in the gateway repository | Accepted |
| [0002](0002-checkpoint-payload-mirrors-the-sealer.md) | The checkpoint payload mirrors what the sealer produces | Accepted |
| [0003](0003-no-schema-validator-in-the-gateway.md) | No JSON Schema validator in the gateway | Accepted |
| [0004](0004-reserved-fields-must-not-be-populated.md) | Undecided protocol fields are reserved, not optional | Accepted |
| [0005](0005-covered-time-range-is-required.md) | The covered time range is required in v1 | Accepted |
| [0006](0006-predecessor-identity-is-not-hash-bound.md) | Predecessor identity is not bound into the checkpoint hash | Proposed |
| [0007](0007-hash-construction-belongs-to-the-protocol.md) | The hash construction belongs to the protocol package | Accepted |
| [0008](0008-seal-status-is-gateway-declared.md) | Sealing status is three states, judged against a gateway-declared window | Accepted |
| [0009](0009-indexless-tool-call-deltas-are-dropped.md) | An indexless streaming tool call delta is dropped, not merged | Accepted |
| [0010](0010-nested-request-fields-are-allowlisted.md) | Unknown fields are refused at every level of the request | Accepted |

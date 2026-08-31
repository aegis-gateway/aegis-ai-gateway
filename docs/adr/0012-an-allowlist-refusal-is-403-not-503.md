# 0012. A per-key model refusal is 403, not 503

Status:   Accepted
Date:     2026-08-30
Decision: Return 403 `permission_error` / `model_not_allowed` when a key's allowlist excludes the requested model. Do not disguise the refusal as unavailability.

## Context

Phase 1 of the week-one work order added enforcement of `api_keys.allowed_models`.
A refused request returns 403. The question raised at review was whether it
should instead return 503, so that a caller cannot tell a model it may not use
from a model that does not exist.

The argument for 503 is enumeration: a distinct 403 confirms the model exists,
which a caller probing aliases could use to map the catalogue.

## Decision

403, unchanged.

**The enumeration argument does not apply here, though not for the reason it
first appears.** An earlier draft of this record said `GET /v1/models` publishes
the catalogue, so 503 would hide nothing. That is wrong: for a key with a
non-empty allowlist the endpoint lists only the permitted aliases, so the
excluded ones are *not* published to that caller.

The property that actually holds is stronger. The allowlist check runs **before
route resolution**, so a configured-but-excluded alias and an alias that does not
exist produce the same response. Measured against a key allowlisted for
`aegis-fast` alone:

| requested | status | code |
|---|---|---|
| `aegis-fast` | 200 | |
| `aegis-balanced` (configured, excluded) | 403 | `model_not_allowed` |
| `totally-made-up-model` (does not exist) | 403 | `model_not_allowed` |

A restricted caller cannot tell the two apart, so there is nothing for 503 to
conceal. `GET /v1/models` returns `["aegis-fast"]` for that key, which is
consistent: it publishes what the key may use, not what exists.

For an **unrestricted** key the two are distinguishable, because an empty
allowlist permits everything and an unknown alias reaches `ResolveRoute` and
returns 503 `service_unavailable`. That is not a leak worth closing: a key that
may use every configured model has nothing withheld from it to enumerate.

**503 is a false statement about the system.** The service is available; the
request was understood and refused on authorisation grounds. An audit trail whose
HTTP status says "temporarily unavailable" for a permission decision is wrong in
the record, and `audit_events.status_code` carries that status into the sealed
chain, where it cannot be corrected afterwards.

**503 is actively harmful to callers.** 5xx is retryable by convention and most
SDKs retry it automatically, often with backoff and several attempts. Every
misconfigured allowlist would become a retry storm against the gateway, and the
operator would see load rather than a clear refusal. 403 is terminal: a caller
that receives it stops.

**It also degrades the operator's own signal.** A refusal recorded with status
403 is countable as a policy outcome. Mixed into 503s it is indistinguishable
from provider unavailability, which is the one thing an operator most wants to
separate it from. The event type carries the same distinction and is the subject
of its own change.

## Consequences

A restricted caller cannot distinguish "not permitted for you" from "not a
model", which is the position this decision leaves in place rather than one it
had to argue for. The pre-routing order of the check is what provides it, so
moving the allowlist check after `ResolveRoute` would introduce an enumeration
channel that does not exist today. Anyone reordering that path should treat this
as a constraint.

The refusal is already indistinguishable from absence for the caller it matters
for. What a 403 does still reveal is that *some* restriction applies, as against
a deployment that wanted refusals to look like ordinary unavailability. Changing
that is not the status code alone: the error code `model_not_allowed` and the
message name the reason, and the audit record would need a way to say what really
happened while the caller is told something else. That is a different decision
and should be recorded separately.

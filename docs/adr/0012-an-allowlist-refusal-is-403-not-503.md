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

**The enumeration argument does not apply here.** `GET /v1/models` is already
filtered by the same allowlist, so what a key can see and what it may use are the
same set by construction. A caller learns the catalogue from the endpoint built
to tell it. Returning 503 would hide nothing that is not already published to
that caller, and would hide it only from the caller who asked politely.

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

A caller holding a key with a restricted allowlist can distinguish "not permitted
for you" from "not a model", for models that appear in its own `/v1/models`
listing. That is accepted, because the listing already tells it.

If a deployment ever needs the refusal to be indistinguishable from absence, the
change is not the status code alone: `/v1/models` filtering, the error code
`model_not_allowed`, and the message would all have to be reconsidered together,
and the audit record would need a way to say what really happened while the
caller is told something else. That is a different decision from this one and
should be recorded separately.

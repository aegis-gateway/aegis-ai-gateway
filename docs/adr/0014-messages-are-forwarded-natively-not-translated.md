# 0014. `/v1/messages` is governed through a projection and forwarded natively, not translated

Status:   Accepted
Date:     2026-10-20
Decision: Serve the Anthropic Messages API by governing a projection of the request and forwarding the client's own body, with only the model replaced, to Anthropic-type routes. Do not translate it into the OpenAI-shaped internal request.

## Context

Claude Code calls `POST /v1/messages` and cannot be pointed at `/v1/chat/completions`
(docs/evidence/agent-compatibility.md, Subject B). A protocol shim in front of the
gateway would itself be an ungoverned surface.

The gateway's internal request is OpenAI-shaped. Claude Code's requests carry constructs
it has no faithful slot for: thinking blocks whose `signature` the provider verifies
byte for byte on the next turn, `redacted_thinking`, `cache_control` markers on system,
message and tool blocks, `output_config.effort`, `context_management` and adaptive
thinking. Translating in and out again would drop or approximate each of them, and a
dropped thinking signature fails the next turn of the conversation at the provider.

## Decision

- Decode the Messages API behind an allowlist at every level, as
  `DecodeChatCompletion` does for chat completions.
- Build a projection (`MessagesRequest.Projection`) that places every client string
  in the internal request, so the existing filter chain, validator, image gate, policy
  and audit govern it. `messages_scan_coverage_test.go` walks the raw body and fails if
  any string reaches neither the projection's scanned segments nor a fixed vocabulary
  the decoder enforces.
- Forward the client's body with only `model` replaced, to routes whose adapter
  implements `adapters.AnthropicNative`. Other routes are not eligible, on the primary
  and every fallback.
- Relay the response verbatim and read usage from it.

## Consequences

- Claude Code works with full fidelity on Anthropic routes.
- `/v1/messages` cannot reach OpenAI-compatible or local models. An alias whose routes
  include none of Anthropic type is refused with 400 `model_not_anthropic`.
  Translation could be added later as a separate, explicitly lossy path; it is not
  implied here.
- The decoder is strict, so a future Claude Code release that adds a field or block
  type will be refused by name until the allowlist is extended. That is the same trade
  the chat decoder makes ([ADR 0010](0010-nested-request-fields-are-allowlisted.md)).
- The projection, not the forwarded body, is what the filters read. The body-walk test
  exists because that gap is where a field could leave unscanned.

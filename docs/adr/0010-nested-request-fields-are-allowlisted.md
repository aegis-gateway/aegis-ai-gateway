# 0010. Unknown fields are refused at every level of the request, not only the top two

Status:   Accepted
Date:     2026-08-28
Decision: Apply the wire allowlist to nested objects as well: tool definitions, their function objects, tool calls and their function objects. Refuse an unrecognised key at any depth with a 400 naming its path.

## Context

Decoding a chat completions request became an allowlist because the gateway had
been discarding fields it did not understand, which is how tool calling came to
be stripped from every agent request with nothing reporting a problem.

The first allowlist covered two levels: the top-level object and each message
object. Everything nested below that, `tools[]`, `tools[].function`,
`tool_calls[]` and their `function`, still fell through to a plain
`json.Unmarshal` that ignores unknown keys. A review bot demonstrated the hole
with `{"name": "f", "strcit": true}`: a typo for `strict` inside a tool
definition, accepted and discarded exactly as `tools` itself had been.

Fixing it made the decoder reject more traffic than before. The author added a
test asserting that valid tool shapes still decode, which shows awareness that
over-rejection is a real cost, but no decision was recorded about how strict the
decoder should be. This record supplies it.

## Decision

**Strict at every level.** An unrecognised key anywhere in the request body is a
400 naming its path, for the same reason it is at the top level: a field the
gateway cannot honour is a field the caller believes is doing something. Depth
does not change that. A `strcit` typo inside a tool definition costs the caller
their strict-mode guarantee just as surely as a dropped `tools` array cost them
their tools.

**The cost is accepted deliberately.** Strictness means a provider adding a
field to the tool schema breaks AEGIS until the allowlist learns it, and that a
client sending a field for a provider AEGIS does not route to gets a 400 rather
than having it ignored. Both are real, and both are preferable to the
alternative, which is the gateway forwarding a request it has silently altered.

The direction to fail matters more here than the convenience. This product's
claim is that it decides whether a call is permitted and records why. A decoder
that quietly edits the request before that decision undermines every claim
downstream of it.

**Where strictness must not apply: provider responses.** The allowlist governs
what a client may send. Response decoding stays permissive, because a provider
adding a field to its own response is not a governance event and failing the
request over it would be an outage the operator cannot fix.

## Consequences

Six allowlists exist where two did, and each is a place a future field must be
added. That maintenance burden is the honest cost, and it is bounded: the OpenAI
tool schema is small and changes slowly.

The failure mode to watch is a legitimate provider feature landing before the
allowlist learns it, producing 400s on traffic that would have worked. That is
visible: `aegis_unsupported_field_total` is labelled by field name, so an
unfamiliar label appearing in volume is the signal, and the fix is one map
entry.

This decision does not extend to a permissive escape hatch. A configuration flag
letting an operator turn strict decoding off would recreate the original defect
on demand, and would do it in exactly the deployments least likely to notice.

# 0009. An indexless streaming tool call delta is dropped, not merged

Status:   Accepted
Date:     2026-08-28
Decision: Discard a streaming tool call delta that carries no `index`, log a warning naming the call, and keep the rest of the stream. Do not attach it to index 0 and do not fail the request.

## Context

A provider streams a tool call in pieces. The first delta carries the index, the
id, the type and the function name; later deltas carry the index and the next
fragment of the arguments string. The index is the join key, and it is what lets
two tools called in one turn interleave in the stream and still be
reconstructed.

The gateway accumulates in parallel with the client, so that its own record can
say which tools a streamed response called. `Index` is a `*int` rather than an
`int` for a specific reason: a delta genuinely carrying index 0 and a delta
carrying no index at all are different facts. The first accumulator coerced nil
to 0, which folded an indexless fragment into the first tool call and corrupted
its arguments.

That was found by a review bot as a Low-severity finding, fixed in the same
change as three other findings, and merged without anyone deciding what the
behaviour ought to be. This record supplies the decision after the fact, which
is the wrong order but better than leaving it inferred from code.

Three options were available.

**Merge into index 0.** The original behaviour. It silently corrupts a real tool
call, and the corruption surfaces at the client as arguments that will not
parse, with nothing pointing back at the gateway.

**Fail the stream.** Consistent with the gateway's fail-closed posture
elsewhere: a rate limiter with an unreachable Redis refuses rather than guesses.

**Drop the fragment and continue.** What shipped.

## Decision

Drop it, warn, and continue.

The fail-closed posture applies to **decisions the gateway makes about a
request**: whether to permit it, what it costs, what clearance it needs. There,
guessing is a governance failure and refusing is correct. This is not that. The
accumulator is a passive observer: it does not gate the request, and the client
receives every chunk byte for byte regardless of what the accumulator does with
its copy. Nothing the client sees changes.

So the question is only what the gateway's own log line should say when a
provider sends something malformed. Failing the stream would take a healthy
response away from a client because a bookkeeping side-channel could not parse
one fragment. That trades a real outage for a metadata inaccuracy.

**What a client should expect.** Nothing different. The relay is unaffected and
the client's own accumulation, which is the one that produces the tool call it
acts on, sees every delta the provider sent.

**What an operator should expect.** A `tool call delta has no index` warning
means `tools_returned` on that request's completion log line may undercount. It
does not mean the client got a broken response. Every provider that streams
parallel tool calls sends the index, so this warning firing at all suggests a
provider change worth looking at.

## Consequences

The gateway's tool-name metadata is best-effort on the streaming path, and this
record is the statement that it is best-effort by choice. Anything that must be
exact cannot be built on it: an audit record of which tools were called, if one
is ever added, needs a stronger source than an accumulator that is allowed to
drop fragments.

It also means a malformed stream is visible only in a log line. If tool-call
metadata later becomes load-bearing for policy or billing, this decision should
be revisited rather than inherited, and a metric would need to exist before it
could be.

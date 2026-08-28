# Request field support

Every field of the OpenAI chat completions request schema, and what AEGIS does with
it.

This page exists because of a defect. `tools`, `tool_calls` and `tool_call_id` were
absent from the canonical request type, so `json.Unmarshal` discarded them. The
provider received a tool-less request and answered in prose, the client's agent loop
stalled, and nothing anywhere reported a problem. Every agent task that used tools
was affected.

The narrow fix was to add the missing fields. The wider fix, and the reason this page
exists, is that the gateway accepted input it did not understand. For a product whose
job is to decide whether a call is permitted, silently discarding part of the request
is the wrong direction to fail in: the caller gets a 200 and a different answer than
the one they asked for, which is harder to notice than an error.

**Decoding is now an allowlist.** A field that is not in the "accepted" table below is
refused with HTTP 400 naming the field. There is no third category of accepted and
quietly ignored, and adding a field to the schema without deciding what to do with it
is no longer possible without editing this list.

Implemented in
[`internal/types/chat_request.go`](../../internal/types/chat_request.go); the
per-field refusal text lives in `refusalReasons` in that file, and
[`internal/types/chat_request_test.go`](../../internal/types/chat_request_test.go)
asserts every row below.

---

## Accepted

| Field | Behaviour |
|-------|-----------|
| `model` | Resolved as an alias against `configs/models.yaml` |
| `messages` | Scanned by the filter chain, then forwarded |
| `messages[].role` | `system`, `user`, `assistant`, `tool`, `function` |
| `messages[].content` | A string, or an array of `{"type":"text"}` parts. See the content parts note below |
| `messages[].name` | Forwarded unchanged |
| `messages[].tool_calls` | Forwarded. Every `function.arguments` string is scanned by the filter chain |
| `messages[].tool_call_id` | Forwarded. Required on a `tool` message |
| `temperature`, `top_p`, `max_tokens` | Validated against `internal/validation` limits, then forwarded |
| `stop` | String or array of strings, up to four |
| `stream` | Branches into the SSE relay |
| `tools` | Forwarded. Names, descriptions and parameter schemas are scanned by the filter chain |
| `tool_choice` | `"none"`, `"auto"`, `"required"`, or `{"type":"function","function":{"name":…}}`. Any other value is refused |
| `parallel_tool_calls` | Forwarded to the provider |

### Accepted but not acted upon

Two fields are forwarded to an OpenAI-compatible provider and have no effect on any
other route, because no other adapter can express them:

| Field | Where it has no effect | Why |
|-------|------------------------|-----|
| `tool_choice` | Anthropic routes | The Anthropic adapter does not carry tools at all |
| `parallel_tool_calls` | Anthropic routes | As above |

In practice neither is reachable on an Anthropic route: a request carrying any tool
field routed to a provider whose adapter reports `SupportsTools() == false` is refused
before dispatch with `tools_unsupported_by_provider`, rather than forwarded with its
tools removed. The rows are here because the general statement, "a field accepted on
one route may be inert on another", is the kind of thing that should be written down
once rather than rediscovered.

### Content parts

Only `{"type": "text", "text": "…"}` is accepted inside a content array. Any other
part type is refused with `unsupported_content_part`.

This is deliberate and it is a security boundary, not a scoping decision. AEGIS
cannot read an image, an audio clip or a file attachment, so a part of that kind would
be an egress path to a provider that the secrets, PII and injection filters do not
cover. Widening `content` to admit structured parts must not admit unscannable ones as
a side effect. See
[known limitations §2.7](../evidence/known-limitations.md).

---

## Refused

Each of these returns HTTP 400 with `"code": "unsupported_field"` and a message naming
the field. The "if it were accepted and dropped" column is the reason each one is a
refusal rather than a tolerated no-op.

### Sampling and output shape

| Field | If it were accepted and dropped |
|-------|--------------------------------|
| `n` | Caller asks for three completions and receives one, with no error |
| `response_format` | Caller asks for JSON mode and receives prose. The parse fails in their code, not here |
| `seed` | Caller believes a run is reproducible when nothing forwarded the hint |
| `logprobs`, `top_logprobs` | Requested data never arrives |
| `logit_bias` | Sampling bias silently absent |
| `presence_penalty`, `frequency_penalty` | Sampling penalties silently absent |
| `max_completion_tokens` | The modern spelling of `max_tokens`. Dropping it leaves a current SDK's only length control with no effect. Use `max_tokens` |
| `service_tier` | Provider tier selection silently absent |
| `reasoning_effort`, `verbosity` | Silently absent |

### Modality

| Field | If it were accepted and dropped |
|-------|--------------------------------|
| `modalities`, `audio`, `prediction` | AEGIS handles text only |
| `web_search_options` | Provider-side web search is a tool the gateway cannot see being called. A capability exercised outside the audit trail is worse than one refused |

### Governance-relevant

These are refused for a stronger reason than the rest.

| Field | Why refusal is the only honest answer |
|-------|--------------------------------------|
| `store` | Asks the **provider** to retain the exchange. AEGIS's central claim is about retention. It will neither pass a retention instruction it cannot audit nor drop one and leave the caller believing it was honoured |
| `metadata` | A client-supplied object that reaches the provider uninspected. Attribution comes from the API key and the `X-Aegis-Project` header |
| `user` | An end-user identity claim the gateway would ignore while asserting its own identity from the API key. Two identity systems, one of them invisible to every audit row and every policy |
| `safety_identifier`, `prompt_cache_key` | Same class as `user` |
| `stream_options` | Not forwarded. Usage in a stream is read from whatever the provider sends unprompted. See the cost note in [known limitations §2.8](../evidence/known-limitations.md) |

### Deprecated

| Field | Use instead |
|-------|-------------|
| `functions` | `tools` |
| `function_call` | `tool_choice` |
| `messages[].function_call` | `messages[].tool_calls` |
| `messages[].refusal` | Nothing. It is a provider output field and is not accepted inbound |

### AEGIS's own fields

The canonical request type used to be the wire type, so its `json` tags were live
input. A client could set any of these in the request body; they were parsed and then
overwritten from the auth context or from a header, which made them a silent no-op
rather than a privilege escalation. They are now refused by name.

| Field | Where the value actually comes from |
|-------|-------------------------------------|
| `request_id` | The gateway. Supply the `X-Request-ID` header to choose it |
| `organization_id`, `team_id`, `user_id`, `api_key_id` | The authenticated API key |
| `classification` | The authenticated API key. A body-supplied classification would be a clearance a caller granted itself |
| `project` | The `X-Aegis-Project` header |
| `trace_context` | The `X-Aegis-Trace-Context` header |
| `prefer_provider` | Nothing. `X-Aegis-Prefer-Provider` is read into the request and then never consulted by `router.ResolveRoute`. The header is inert |
| `skip_cache` | Nothing. AEGIS does not cache completions, so there is nothing to skip |

Anything else, including a typo such as `tolls` for `tools`, is refused with the
generic message. That is the point: the original defect would have been caught at the
first request if a misspelled or unrecognised field had been an error.

---

## Behaviour change

Refusing unknown fields **rejects requests that previously returned 200**. A client
that has been sending `seed`, `user`, `n` or `response_format` and not noticing that
they had no effect will now get a 400 naming the field.

That is the intended outcome. Those requests were already not doing what the caller
asked; the only change is that they now say so. The remedy is to remove the field, and
the error message names it.

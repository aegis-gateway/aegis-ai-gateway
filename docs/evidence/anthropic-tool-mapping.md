# Anthropic tool surface: observed behaviour and mapping

Every row here was produced by `scripts/dev/probe_anthropic_tools.py` against the
live Messages API, not from a remembered schema. Re-run it with a key in `.env`
to reproduce.

Probed against `claude-haiku-4-5` on 2026-08-28.

---

## 1. Tool definitions

| | OpenAI | Anthropic |
|---|---|---|
| Container | `tools[].function` | `tools[]` (flat) |
| Schema field | `parameters` | `input_schema` |
| Description | optional | optional (confirmed: a tool with no description returns `stop_reason=tool_use`) |

Sending OpenAI's spelling is refused, so this cannot be passed through:

```
tools.0.custom.input_schema: Field required
```

**`strict`.** Anthropic accepts the field but validates the schema behind it:

```
tools.0.custom: For 'object' type, 'additionalProperties' must be explicitly set to false
```

So `strict: true` maps, but only when the caller's schema already sets
`additionalProperties: false`. A caller who sets `strict` without it gets a 400
from the provider rather than from AEGIS.

## 2. tool_choice

Anthropic accepts exactly four object forms and rejects everything else:

| OpenAI | Anthropic | Probe result |
|---|---|---|
| `"auto"` | `{"type":"auto"}` | 200, `stop_reason=tool_use` |
| `"required"` | `{"type":"any"}` | 200, `stop_reason=tool_use` |
| `"none"` | `{"type":"none"}` | 200, `stop_reason=end_turn` |
| `{"type":"function","function":{"name":X}}` | `{"type":"tool","name":X}` | 200, `stop_reason=tool_use` |

Refused, with the exact errors:

```
{"type":"required"}   tool_choice: Input tag 'required' found using 'type' does not match
                      any of the expected tags: 'auto', 'any', 'tool', 'none'
"auto"                tool_choice: Input should be an object
{"type":"function"}   tool_choice: Input tag 'function' ...
```

The mapping is total in the OpenAI-to-Anthropic direction: every value AEGIS
accepts has an Anthropic equivalent.

## 3. parallel_tool_calls

OpenAI puts it at the top level. Anthropic puts it **inside** `tool_choice`:

```json
"tool_choice": {"type": "auto", "disable_parallel_tool_use": true}
```

Confirmed: with it, one `tool_use` block; without it, two. So a request carrying
`parallel_tool_calls: false` and **no** `tool_choice` still has to synthesise a
`tool_choice` object to express it.

## 4. Tool results

| | OpenAI | Anthropic |
|---|---|---|
| Carrier | a message with `role: "tool"` | a `tool_result` block inside a **user** message |
| Correlator | `tool_call_id` on the message | `tool_use_id` on the block |
| Content | string | string **or** an array of blocks (both confirmed 200) |
| Error signalling | none | `is_error: true` (confirmed 200) |

Three constraints, each confirmed by a refusal:

```
result on an assistant turn   messages.1: `tool_result` blocks can only be in `user` messages
result with no tool_use_id    messages.2.content.0.tool_result.tool_use_id: Field required
result with an unknown id     unexpected `tool_use_id` found in `tool_result` blocks
```

**Adjacency is enforced.** A `tool_use` must be followed *immediately* by its
`tool_result`:

```
messages.2: `tool_use` ids were found without `tool_result` blocks immediately after:
toolu_x. Each `tool_use` block must have a corresponding `tool_result`
```

OpenAI does not require this. See the unmappable list below.

## 5. stop_reason

| Anthropic | OpenAI | Probe |
|---|---|---|
| `tool_use` | `tool_calls` | a tool call returns `tool_use` |
| `end_turn` | `stop` | `tool_choice: none` returns `end_turn` |
| `max_tokens` | `length` | `max_tokens: 1` returns `max_tokens` |
| `stop_sequence` | `stop` | already mapped |

## 6. Streaming, and the index trap

Event order observed:

```
message_start  content_block_start  ping  content_block_delta
content_block_stop  message_delta  message_stop
```

A tool call arrives as a `content_block_start` carrying `index`, `type:
"tool_use"`, `id`, `name` and an empty `input`, then `content_block_delta`
events whose `delta.type` is `input_json_delta` carrying `partial_json`
fragments. Fragments reassemble per index into valid JSON. The first fragment is
frequently the empty string.

**The index spaces are not the same, and this is the trap.** Anthropic's `index`
counts *every* content block. OpenAI's `tool_calls[].index` counts only tool
calls. With two tool calls and no text they agree by coincidence:

```
content_block_start index=0 type=tool_use name=get_weather
content_block_start index=1 type=tool_use name=get_time
```

Add one sentence of text and they diverge:

```
content_block_start index=0 type=text
content_block_start index=1 type=tool_use name=get_weather
input_json_delta    index=1 '{"city": "Paris"}'
```

Relaying Anthropic's index unchanged would hand an OpenAI client a call at
`tool_calls[1]` with nothing at `[0]`. A client accumulating by index sees a gap
and reconstructs one call where the model made one, at the wrong ordinal, or
drops it. The translation must therefore keep its own Anthropic-block-index to
OpenAI-tool-ordinal map for the life of the stream.

This is the same defect class as the indexless-delta bug fixed in #51.

## 7. System messages

`role: "system"` inside `messages` is refused:

```
messages.0: use the top-level 'system' parameter for the initial system prompt
```

The existing adapter already hoists system messages, so this is unchanged by
tool support. Noted because the error mentions a "directive-only form", which
suggests mid-conversation system messages exist in some shape this probe did not
explore.

---

## Constructs that do not map

Each is refused rather than approximated, because an approximation is the same
failure as a silent drop and harder to detect.

1. **A tool call not immediately followed by its result.** Anthropic enforces
   adjacency; OpenAI does not. A conversation that interleaves anything between
   a `tool_calls` assistant turn and its `tool` result message is expressible in
   OpenAI and rejected by Anthropic.

2. **A tool result with no matching call in the preceding turn.** OpenAI
   tolerates an orphan `tool_call_id`; Anthropic refuses it.

3. **`strict: true` without `additionalProperties: false`.** Accepted by AEGIS
   today, refused by Anthropic. AEGIS cannot rewrite the caller's schema without
   changing what they asked for.

4. **`is_error` has no OpenAI equivalent.** In the Anthropic-to-OpenAI direction
   there is nowhere to put it: OpenAI signals tool failure in the result text.
   Nothing is lost translating OpenAI to Anthropic, since AEGIS never sets it.

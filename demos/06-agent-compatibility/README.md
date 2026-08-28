# 06 — Agent Compatibility

Tests an OpenAI-SDK-based coding agent routed through the AEGIS gateway, covering
six scenarios: secrets blocking, policy denial, streaming, tool use, long context, and
denial behaviour.

The tool-use act is the substantial one. It runs a complete tool call loop, repeats it
over SSE, hides credentials and a prompt injection in the surfaces that tool calling
added, and checks what the gateway refuses to carry.

## What it shows

**Working today (Subject A: OpenClaw, or any OpenAI-SDK client):**
- Any client using `openai-completions` against `/v1/chat/completions` connects with zero code change, only configuration
- Secrets embedded in a prompt are blocked before reaching the provider; the audit row records the block with no payload
- A Rego policy that restricts a request class returns a clear, human-readable 451 with the denial reason; the agent does not retry
- Streaming passes through intact
- **Tool calling works on the shipped aliases**, Anthropic included. `tools`, `tool_choice`, `tool_calls`, `tool_call_id` and the `tool` role are carried end to end, translated into the Anthropic Messages API shape where the route requires it. Act 4 runs a complete loop: offer a tool, receive a call, return a result, receive an answer, and then does it again over SSE so the index-keyed deltas have to be reassembled
- **The filter chain reads the widened request.** Act 4c hides a credential in a tool call's arguments, in a tool result, and in a structured content part, then hides a prompt injection in a tool result. All four are blocked before the provider sees them

**Refused by design, and shown as refusals rather than hidden:**
- **A non-text content part.** An image part returns 400 `unsupported_content_part`. AEGIS cannot scan an image and will not forward to a provider what no filter has read
- **An unsupported request field.** `seed`, `n` and everything else outside the allowlist return 400 naming the field. This used to be a silent discard, which is how tool calling was lost in the first place

**Still broken:**
- **Claude Code does not connect at all.** It calls `POST /v1/messages` (Anthropic Messages API). AEGIS exposes no such route. First request returns 404

## Prerequisites

- Docker Compose v2
- `ANTHROPIC_API_KEY` (every shipped alias routes to Anthropic first)
- `python3` with `openai` package (`pip install openai`)

## Architecture

```
agent.py (openai SDK) → AEGIS Gateway (:8080) → OpenAI / Anthropic
                               ↓
                         PostgreSQL (audit)
                         Redis (auth cache, rate limits)
```

The agent script uses the `openai` Python package pointed at AEGIS. No client code
changes are needed — only the `base_url` and `api_key` parameters.

## Run

```bash
cd demos/06-agent-compatibility
./run.sh
```

The script starts the stack, then walks through six acts without pauses.

## Cleanup

```bash
docker compose down -v
```

## Key finding

Tool use was the gap, and it is closed for OpenAI-compatible routes. `tools`,
`tool_calls` and `tool_call_id` were absent from the structs the gateway decoded
into, so `json.Unmarshal` discarded them: an agent appeared to work for simple
questions and silently degraded on any task requiring tool use.

Two things came out of closing it that are worth as much as the fix.

The first is that the gateway no longer accepts input it does not understand. An
unrecognised request field is now a 400 naming the field, not a discarded key.
Silent acceptance is what let this defect exist, and for a product whose job is
deciding whether a call is permitted it is the wrong direction to fail in.

The second is that widening what a message can carry widened what it can smuggle.
A credential in a tool call's arguments, or in a tool result, or in one part of a
content array, is invisible to a filter that reads content as a string. Act 4c
exists because a compatibility fix that left those unscanned would have been a
hole in the one claim this product is built on. Tool results deserve the most
attention: a tool result is content fetched from outside the model and handed back
into the prompt, which makes it where indirect prompt injection actually arrives.

See `docs/evidence/agent-compatibility.md` for the full analysis and
`docs/reference/request-field-support.md` for the field-by-field decision.

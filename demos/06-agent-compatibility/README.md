# 06 — Agent Compatibility

Tests an OpenAI-SDK-based coding agent routed through the AEGIS gateway, covering
six scenarios: secrets blocking, policy denial, streaming, tool use, long context, and
denial behaviour.

## What it shows

**What works today (Subject A — OpenClaw / any OpenAI-SDK client):**
- Any client using `openai-completions` against `/v1/chat/completions` connects with zero code change — only configuration
- Secrets embedded in a prompt are blocked by the gateway before reaching the provider; the audit row records the block with no payload
- A Rego policy that restricts a request class returns a clear, human-readable 451 with the denial reason; the agent does not retry
- Streaming passes through intact

**What is broken today:**
- **Tool use is silently stripped.** `tools`, `tool_calls`, and `tool_call_id` fields are not in AEGIS's canonical `AegisRequest` / `Message` types. `json.Unmarshal` discards them without error. The provider receives a request with no tools declared and responds with plain text. The agent loop stalls silently.
- **Claude Code does not connect at all.** Claude Code calls `POST /v1/messages` (Anthropic Messages API). AEGIS exposes no such route. First request returns 404.

## Prerequisites

- Docker Compose v2
- At least one provider API key (`OPENAI_API_KEY` or `ANTHROPIC_API_KEY`)
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

Tool use is the gap. An agent that relies on function calls for its core workflow will
appear to work for simple questions and silently degrade on any task requiring tool use.
Closing this gap requires expanding `types.Message` to carry `json.RawMessage` content
and adding `Tools`/`ToolChoice` to `AegisRequest`. See `docs/evidence/agent-compatibility.md`
for the full analysis.

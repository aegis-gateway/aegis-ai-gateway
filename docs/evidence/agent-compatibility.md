# Agent Compatibility Evidence

**Date:** 2026-08-27  
**Branch:** research/agent-compatibility  
**Repo:** aegis-gateway/aegis-ai-gateway

---

## Summary

Two coding agent surfaces were tested against the AEGIS AI Gateway:

| Subject | Connects | Protocol match | Tool use | Verdict |
|---------|----------|---------------|----------|---------|
| OpenClaw (custom provider via `openai-completions` API) | ✅ Yes | ✅ OpenAI `/v1/chat/completions` | ❌ Silently stripped | Connects; tool use broken |
| Claude Code (`ANTHROPIC_BASE_URL` override) | ❌ No | ❌ `/v1/messages` not exposed | N/A | Fails at first request |

**Most important finding:** Tool use—the feature coding agents depend on most—is silently stripped by AEGIS for both subjects that reach the gateway. The `tools` parameter and `tool_calls` message fields are simply not in any struct AEGIS deserializes, so they disappear before the request reaches the provider. The provider responds as if tools were never declared. The agent loop breaks without an error.

---

## Subject A: OpenClaw

**Version tested:** OpenClaw npm package at `/home/openclaw/.npm-global/lib/node_modules/openclaw`  
**Connection method:** Custom provider via `models.providers` config, `api: "openai-completions"`, `baseUrl: "http://localhost:8080/v1"`, `apiKey: "aegis-demo-quickstart"`

### How to configure OpenClaw to route through AEGIS

```json5
{
  models: {
    providers: {
      "aegis": {
        baseUrl: "http://localhost:8080/v1",
        apiKey: "aegis-demo-quickstart",
        api: "openai-completions",
        models: [
          {
            id: "aegis-fast",
            name: "AEGIS Fast",
            reasoning: false,
            input: ["text"],
            cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
            contextWindow: 200000,
            contextTokens: 180000,
            maxTokens: 8096,
          },
          {
            id: "aegis-balanced",
            name: "AEGIS Balanced",
            reasoning: false,
            input: ["text"],
            cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
            contextWindow: 200000,
            contextTokens: 180000,
            maxTokens: 8096,
          },
          {
            id: "aegis-reasoning",
            name: "AEGIS Reasoning",
            reasoning: true,
            input: ["text"],
            cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
            contextWindow: 200000,
            contextTokens: 180000,
            maxTokens: 16384,
          },
        ],
      },
    },
  },
}
```

OpenClaw's `openai-completions` API adapter calls `POST {baseUrl}/chat/completions`, which maps exactly to AEGIS's `POST /v1/chat/completions`. Authentication is passed as `Authorization: Bearer {apiKey}`.

### Test case results

#### Test 1: Secrets — PASSES ✅

AEGIS runs the secrets filter before the request reaches any provider. A request containing an AWS access key (`AKIAIOSFODNN7EXAMPLE`) is rejected before it leaves the gateway:

```
HTTP 451 Unavailable For Legal Reasons
{
  "error": {
    "message": "Request blocked: AWS Access Key detected",
    "type": "content_filter_error",
    "code": "content_blocked",
    "aegis_request_id": "..."
  }
}
```

The audit row records the block event (`event_type: filter_block`) without recording the key value. OpenClaw receives the 451, surfaces the error to the user as a provider refusal, and stops—it does not retry.

#### Test 2: Classification policy denial — PASSES ✅

A Rego policy that denies requests mentioning a restricted topic returns:

```
HTTP 451 Unavailable For Legal Reasons
{
  "error": {
    "message": "financial topic restricted to finance team",
    "type": "content_filter_error",
    "code": "content_blocked",
    "aegis_request_id": "..."
  }
}
```

The error includes the human-readable `reason` from the Rego `reason` rule, which is the concatenation of all fired `deny` messages. OpenClaw surfaces this as a clear refusal with the reason text visible. The agent does not retry a 451.

#### Test 3: Streaming — PASSES ✅

AEGIS has full SSE streaming support (`StreamingHandler.HandleStream()`). It sets `Content-Type: text/event-stream`, forwards chunks through `bufio.Scanner`, and calls `Flush()` after each chunk. The OpenAI streaming format (`data: {"choices":[{"delta":...}]}`) passes through unchanged for OpenAI-backed models and is normalized from Anthropic's native format (`content_block_delta`) by `AnthropicAdapter.TransformStreamChunk()`. OpenClaw's `openai-completions` API consumer handles standard SSE, so streaming works end to end.

#### Test 4: Tool use — FAILS SILENTLY ❌

This is the critical gap. AEGIS's canonical request type (`internal/types/request.go`) does not include tool definitions or tool call responses:

```go
// internal/types/request.go
type AegisRequest struct {
    Model    string    `json:"model"`
    Messages []Message `json:"messages"`
    Stream   bool      `json:"stream"`
    // ... temperature, max_tokens, etc.
    // NO: Tools, ToolChoice, Functions
}

type Message struct {
    Role    string `json:"role"`
    Content string `json:"content"` // string only, not []ContentBlock
    Name    string `json:"name,omitempty"`
    // NO: ToolCalls, ToolCallID, FunctionCall
}
```

And the OpenAI adapter's outgoing body matches:

```go
// internal/router/adapters/openai.go
type openAIRequestBody struct {
    Model    string          `json:"model"`
    Messages []types.Message `json:"messages"`
    Stream   bool            `json:"stream,omitempty"`
    // NO: Tools, ToolChoice
}
```

When an agent sends a tool-augmented request:

```json
POST /v1/chat/completions
{
  "model": "aegis-fast",
  "tools": [{"type": "function", "function": {"name": "read_file", "parameters": {...}}}],
  "messages": [
    {"role": "user", "content": "Read src/main.go and summarize it"},
    {"role": "assistant", "tool_calls": [{"id": "call_abc", "type": "function", "function": {"name": "read_file", "arguments": "{\"path\":\"src/main.go\"}"}}]},
    {"role": "tool", "tool_call_id": "call_abc", "content": "package main\n..."}
  ]
}
```

`json.Unmarshal` into `AegisRequest` silently discards:
- `tools` (no matching field)
- `messages[1].tool_calls` (no matching field in `types.Message`)
- `messages[2].tool_call_id` (no matching field)

The `Content string` field cannot unmarshal a `content` array of content blocks; `json.Unmarshal` returns an error (`cannot unmarshal array into Go struct field`), causing AEGIS to return `HTTP 400 Invalid JSON`. So multi-part `content` arrays fail loudly, but tool call metadata on messages with string content fails silently.

The provider receives a stripped request with no tools declared. It responds with plain text. The agent loop stalls: the agent expected a tool call response but got a text response, so it either loops trying again or gives a confusing answer.

**This is not a minor edge case.** Every coding agent uses tool calls as its primary mechanism for reading files, running commands, and searching code. An agent routed through AEGIS today will appear to work for simple questions and silently degrade on any task requiring tool use.

#### Test 5: Long context and many turns — PASSES (with caveats) ✅⚠️

The validator (`internal/validation/validator.go`) allows up to 1,000 messages and 1M characters of total content by default—sufficient for most agent sessions. Large payloads pass through without truncation. The timeout default is not visible in config but the streaming handler has configurable per-chunk and total-stream timeouts.

One caveat: `types.Message.Content` is typed as `string`. Anthropic's native format uses content arrays for multi-part messages (text + tool results). AEGIS normalizes these during outbound adaptation but does not store them in the canonical type. As context grows and multi-part messages appear, the content array unmarshal error (HTTP 400) becomes more likely.

#### Test 6: Agent behaviour on denial — PASSES ✅

AEGIS returns HTTP 451 with `{"error": {"type": "content_filter_error", "code": "content_blocked", "message": "..."}}`. The `message` field contains the human-readable reason from the Rego policy or filter. OpenClaw surfaces this as a clear error. It does not retry 451 responses. The user sees the denial reason. No infinite retry loop observed.

---

## Subject B: Claude Code

**Version tested:** Claude Code CLI (current install, model `claude-sonnet-4-6`)  
**Connection method:** `ANTHROPIC_BASE_URL=http://localhost:8080`

### Point of failure

Claude Code, when `ANTHROPIC_BASE_URL` is set, calls the **Anthropic Messages API** at:

```
POST {ANTHROPIC_BASE_URL}/v1/messages
```

with an Anthropic-format request body:

```json
{
  "model": "claude-sonnet-4-6",
  "max_tokens": 8096,
  "messages": [...],
  "tools": [...],
  "system": "..."
}
```

and streams the response as Anthropic SSE events (`content_block_start`, `content_block_delta`, `content_block_stop`, `message_stop`).

AEGIS exposes no `/v1/messages` route. The full route table in `cmd/gateway/main.go`:

```go
r.Get("/aegis/v1/health", makeHealthHandler(...))
r.Group(func(r chi.Router) {
    r.Use(auth.Middleware(...))
    r.Use(ratelimit.Middleware(...))
    r.Post("/v1/chat/completions", handler.ChatCompletions)
    r.Get("/v1/models", handler.ListModels)
})
```

The first request from Claude Code would produce:

```
POST http://localhost:8080/v1/messages HTTP/1.1
anthropic-version: 2023-06-01
x-api-key: <key>
Content-Type: application/json

→ HTTP 404 Not Found (chi default "404 page not found")
```

This is a **protocol mismatch**, not a configuration problem. Setting `ANTHROPIC_BASE_URL` to the AEGIS gateway simply does not work because AEGIS speaks OpenAI Chat Completions in and Anthropic Messages API out (to the provider), not Anthropic Messages API in.

Claude Code cannot be made to call `/v1/chat/completions` instead of `/v1/messages` through configuration alone—the endpoint is baked into the Anthropic SDK that Claude Code uses internally.

### What would have to change in AEGIS

To accept Claude Code (and any Anthropic SDK client), AEGIS would need a new ingress surface handling the Anthropic Messages API format. This is not a small configuration change—it requires:

| Work item | Package | Scope |
|-----------|---------|-------|
| New route `POST /v1/messages` | `cmd/gateway/main.go` | 5 lines to register |
| Anthropic ingress parser | new `internal/gateway/anthropic_ingress.go` | Parse Anthropic request format into `AegisRequest` |
| `AegisRequest` tool fields | `internal/types/request.go` | Add `Tools`, `ToolChoice`, retype `Message.Content` to `json.RawMessage` |
| Anthropic SSE ingress streaming | `internal/gateway/streaming.go` | Translate Anthropic SSE event names to canonical form |
| Filter chain adaptation | `internal/filter/` | Secrets/PII filters must handle tool content blocks |
| Policy input adaptation | `internal/filter/policy/opa.go` | Expose tool names in `input.request` for Rego policies |
| Response format conversion | new outbound adapter or `handler.go` | Translate `AegisResponse` back to Anthropic Messages format |
| Anthropic streaming egress | `internal/gateway/streaming.go` | Translate outbound SSE back to Anthropic event format |

This is a contained change—it does not require a new provider integration and it reuses the existing filter chain, policy evaluator, audit logger, and cost tracker. But it is a new ingress surface with its own content shape (multi-part content blocks, tool definitions, tool call results, image attachments) that would require AEGIS's canonical `AegisRequest`/`Message` types to be significantly expanded.

**Is it worth doing?** Yes, if the goal is to route any standard AI coding agent through AEGIS for governance. Claude Code is the dominant coding agent surface. Without `/v1/messages` support, any team using Claude Code cannot put AEGIS in the path without a protocol translation layer in front of it (such as LiteLLM, which converts Anthropic → OpenAI format). That proxy would itself become an ungoverned surface, defeating the purpose.

The narrower near-term fix—closing the tool use gap for Subject A—is smaller: add `Tools []Tool`, `ToolChoice interface{}` to `AegisRequest` and `openAIRequestBody`, and retype `Message.Content` to `json.RawMessage` so both string and array content pass through. The filter chain would need to handle array content for secrets/PII scanning. That change makes AEGIS work for OpenClaw and any other OpenAI-SDK-based agent today.

---

## What the AEGIS gateway exposes today

```
GET  /aegis/v1/health         (unauthenticated)
POST /v1/chat/completions     (OpenAI Chat Completions format in, routed to OpenAI or Anthropic)
GET  /v1/models               (OpenAI model list format)
```

There is no `/v1/messages`, no `/v1/responses`, no `/v1/embeddings`, and no audit query HTTP endpoint exposed (audit data is in PostgreSQL, accessible only via `docker exec ... psql`).

---

## Recommended next steps

1. **Fix tool use for OpenAI-compatible agents (Subject A):** Expand `types.Message.Content` from `string` to `json.RawMessage`, add `Tools` and `ToolChoice` to `AegisRequest` and `openAIRequestBody`. Update secrets/PII scanners to handle array content. This closes test case 4 for OpenClaw and any OpenAI-SDK-based agent.

2. **Add `/v1/messages` Anthropic ingress (Subject B):** New route + ingress parser + streaming translation. This enables Claude Code and any Anthropic-SDK-based agent. The expanded tool fields from step 1 are a prerequisite.

3. **Expose audit query endpoints over HTTP:** The task spec mentions `GET /aegis/v1/audit/events` and `GET /aegis/v1/audit/logs`, but these routes do not exist in the current codebase. Audit data is only accessible via direct database queries. Adding read-only authenticated audit query endpoints would make the demo scriptable without `docker exec`.

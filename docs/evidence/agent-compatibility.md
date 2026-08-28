# Agent Compatibility Evidence

**Original assessment:** 2026-08-27, branch `research/agent-compatibility`
**Tool-calling gap closed:** 2026-08-27, branch `feature/openai-tool-calling-support`
**Repo:** aegis-gateway/aegis-ai-gateway

---

## Summary

Two coding agent surfaces were tested against the AEGIS AI Gateway.

| Subject | Connects | Protocol match | Tool use | Verdict |
|---------|----------|---------------|----------|---------|
| OpenClaw (custom provider via `openai-completions` API) | Yes | OpenAI `/v1/chat/completions` | Supported | Works, on OpenAI-compatible routes |
| Claude Code (`ANTHROPIC_BASE_URL` override) | No | `/v1/messages` not exposed | N/A | Still fails at first request |

**What changed.** Tool use is no longer silently stripped. `tools`, `tool_choice`,
`parallel_tool_calls`, `tool_calls` and `tool_call_id` are carried end to end,
`message.content` accepts a string or an array of text parts, and the `tool` role is
accepted by the validator. Test 4 below now passes.

**What did not change.** Claude Code still cannot connect: it calls the Anthropic
Messages API at `POST /v1/messages`, and AEGIS exposes no such route. That is a
separate piece of work, described under Subject B.

**Tool calling now works on every shipped alias.** The first version of this fix
covered OpenAI-compatible routes only, which meant a tool request to `aegis-fast` with
a real `ANTHROPIC_API_KEY` was refused, since every alias lists Anthropic first. The
Anthropic adapter now translates the tool surface in both directions, streaming
included. Verified against a live gateway on 2026-08-28: a two-turn agent loop on
`aegis-fast`, routed to `claude-haiku-4-5`, issued a `read_file` call and completed.

**One limit the fix deliberately did not paper over.** Non-text content parts are
refused. Widening `content` made image parts expressible for the first time. They are
rejected at decode rather than admitted, because AEGIS cannot scan an image and will
not forward to a provider what no filter has read.

**And a portability limit worth knowing.** Five tool-calling constructs are legal
OpenAI and cannot be expressed against Anthropic, chiefly a tool call that is not
immediately followed by its result. They are refused by name rather than approximated.
See [known limitations](known-limitations.md) §2.8.

**And one thing that got stricter for everyone.** The gateway used to discard any
request field it did not recognise. It now refuses them with a 400 naming the field.
See [request field support](../reference/request-field-support.md). This rejects
requests that previously returned 200; those requests were already not doing what the
caller asked.

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

#### Test 4: Tool use — PASSES (closed 2026-08-27) ✅

**What was broken.** AEGIS's canonical request type carried no tool fields:

```go
// internal/types/request.go, before
type AegisRequest struct {
    Model    string    `json:"model"`
    Messages []Message `json:"messages"`
    // NO: Tools, ToolChoice
}

type Message struct {
    Role    string `json:"role"`
    Content string `json:"content"` // string only
    // NO: ToolCalls, ToolCallID
}
```

`json.Unmarshal` discarded `tools`, `messages[].tool_calls` and
`messages[].tool_call_id` without error. The provider received a request with no tools
declared, answered in prose, and the agent loop stalled. Nothing in the system reported
a problem.

**What it is now.** `Tools`, `ToolChoice` and `ParallelToolCalls` are on the request
type; `ToolCalls` and `ToolCallID` are on `Message`; `Content` is a
[`types.Content`](../../internal/types/content.go) carrying either a string or an array
of text parts. The `tool` role is accepted by the validator, which previously rejected
it. The OpenAI adapter's outgoing body carries all of it.

The request that used to be stripped now arrives intact:

```json
POST /v1/chat/completions
{
  "model": "aegis-fast",
  "tools": [{"type": "function", "function": {"name": "read_file", "parameters": {...}}}],
  "tool_choice": "auto",
  "messages": [
    {"role": "user", "content": "Read src/main.go and summarize it"},
    {"role": "assistant", "content": null, "tool_calls": [{"id": "call_abc", "type": "function",
      "function": {"name": "read_file", "arguments": "{\"path\":\"src/main.go\"}"}}]},
    {"role": "tool", "tool_call_id": "call_abc", "content": "package main\n..."}
  ]
}
```

Evidence: `TestChatCompletions_ToolsReachTheProvider` asserts on the bytes the adapter
was asked to send, not on the status code. A 200 would not settle it, since the broken
gateway also returned 200.

Streaming tool calls are relayed byte for byte, so a client accumulates them by index as
it would from the provider directly. The gateway accumulates in parallel for its own
record: `internal/gateway/tool_stream.go`, tested against split arguments and against
interleaved parallel calls.

**Filtering.** This is the part that had to be right. Widening the message shape added
three places a credential can hide that no filter previously read. All three, plus the
tool definitions themselves, are now scanned by the existing chain via
`AegisRequest.TextSegments`:

| Surface | Scanned for |
|---------|-------------|
| Each text part of a structured content array | secrets, PII, injection |
| The `arguments` string of every tool call | secrets, PII, injection |
| The content of every tool result message | secrets, PII, injection |
| Tool name, description and parameter schema | secrets, PII, injection |

Tool results get particular attention because that is where indirect prompt injection
arrives: an agent that fetches a web page and returns it to the model is carrying
attacker-controlled text into the prompt. `TestChatCompletions_InjectionInToolResultIsBlocked`
covers it end to end, and `internal/filter/tool_surface_test.go` plants a canary
credential in each surface and asserts both that the request is blocked and that the
canary reaches neither the response body nor the audit logger.

**Verified against a live Anthropic-backed gateway**, 2026-08-28, not only in tests:

| Check | Result |
|---|---|
| Two-turn agent loop on `aegis-fast` (routes to Anthropic) | tool call issued, result returned, final answer produced |
| Streaming with prose before the call | tool call arrives at ordinal 0, arguments reassemble to valid JSON |
| Credential in a tool call's arguments, a tool result, a tool definition, a tool call id, a stop sequence | all 451 |
| Injection payload in a tool result | 451, injection filter |
| Blocks written to `audit_events` (positive control) | 5 secrets, 1 injection |
| Canaries anywhere in `pg_dump` | 0 |
| Canaries in the gateway process log | 0 |

The streaming row is the one that matters. Anthropic numbers every content block in
one sequence and OpenAI numbers tool calls in their own, so prose before a call is what
separates the two index spaces. The call still arrived at ordinal 0.

**One refusal this test does not cover as a pass.** A non-text content part is refused
with `unsupported_content_part`. See [known limitations §2.7](known-limitations.md).

#### Test 5: Long context and many turns — PASSES (with caveats) ✅⚠️

The validator (`internal/validation/validator.go`) allows up to 1,000 messages and 1M characters of total content by default—sufficient for most agent sessions. Large payloads pass through without truncation. The timeout default is not visible in config but the streaming handler has configurable per-chunk and total-stream timeouts.

The caveat recorded here originally, that `types.Message.Content` was a `string` and a
multi-part content array therefore failed with HTTP 400, no longer applies to text
parts: `Content` now carries either shape. It still applies to non-text parts, which are
refused deliberately rather than incidentally. Note also that the per-message length
limit is now measured across every text-bearing element of the message, so a message
whose size sits in tool call arguments is bounded the same way as one whose size sits in
its content.

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

Numbered as of 2026-08-27, after the tool-calling work landed.

1. **~~Fix tool use for OpenAI-compatible agents~~. Done.** Landed on
   `feature/openai-tool-calling-support`. See Test 4 above.

2. **~~Translate tools in the Anthropic adapter~~. Done.** Landed on
   `feature/anthropic-tool-translation`. `SupportsTools` returns true and means it.

3. **Add `/v1/messages` Anthropic ingress (Subject B).** New route, ingress parser, and
   streaming translation. The expanded tool fields from step 1 were a prerequisite and
   now exist, so this is smaller than it was. Item 2 is a prerequisite for the egress
   half of it.

4. **Forward `stream_options`.** Currently refused, which means a streaming client
   cannot ask the provider for usage, which means a streamed request can record zero
   tokens and therefore zero cost. See [known limitations §2.9](known-limitations.md).
   The smallest item on this list and the one with the most direct effect on the spend
   controls.

5. **Record tool names in the audit trail.** `tools_offered` and `tools_called` reach
   Rego and the log line but not `audit_events`. For an agent workload, "a call was
   made" and "a shell tool was offered and called" are different facts and only the
   first is in the evidence. Needs a migration and a `hash_schema_version` decision. See
   [known limitations §2.10](known-limitations.md).

6. **~~Expose audit query endpoints over HTTP~~. Done.** `GET /aegis/v1/audit/events`
   and `GET /aegis/v1/audit/logs` exist and are authenticated.

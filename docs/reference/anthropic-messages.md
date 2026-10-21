# Anthropic Messages API (`/v1/messages`)

AEGIS serves `POST /v1/messages` and `POST /v1/messages/count_tokens`, the Anthropic
Messages API that Claude Code and the Anthropic SDKs speak. The handler is
`internal/gateway/messages_handler.go` and the decoder is
`internal/types/messages_request.go`.

## How a request is handled

1. **Authentication.** The key is read from `x-api-key`, where the Anthropic SDKs put
   `ANTHROPIC_API_KEY`, or from `Authorization: Bearer`, where they put
   `ANTHROPIC_AUTH_TOKEN`. If both are sent they must carry the same key, or the request
   is refused with 401 (`auth.AnthropicMiddleware`). Rate limits and budgets apply as on
   every route.
2. **Decode.** The body is checked against an allowlist at every level. A field, block
   type or tool type outside it is a 400 naming it (see the field table below).
3. **Govern a projection.** `MessagesRequest.Projection` maps every client string into
   the request shape the rest of the gateway reads, so the request passes the same
   gates, in the same order, as a chat completion (`internal/gateway/gates.go`):
   body cap, validation, filter chain, key allowlist, image gate, routing, policy and
   pricing. A tool result is projected as a tool result, so the injection filter treats
   it as untrusted.
4. **Forward natively.** The client's own body is sent with only `model` replaced by the
   routed provider model. Thinking signatures, `cache_control` markers, effort and
   context management reach the provider as the client wrote them.
5. **Relay.** The provider's response is relayed as it came: the JSON body, or the event
   stream line by line with its `event:` lines, and any error status with its body and
   `Retry-After`, so the SDK's own retry on 429 and 529 works. Usage, including cache
   reads and 5-minute and 1-hour cache writes, is read on the way past for cost, the
   usage record and the sealed audit event.

`TestMessages_ClaudeCodeTurnForwardedNativelyAndStreamRelayedVerbatim` asserts steps 4
and 5 against a stand-in provider; `messages_scan_coverage_test.go` asserts step 3.

## Routing

Only an alias routed to an Anthropic-type provider can serve this endpoint, because the
body is forwarded rather than translated. Routes whose adapter cannot take it are not
eligible, on the primary and on every fallback (`router.ResolveRouteWhere`). An alias
with no Anthropic provider at all is a 400 `model_not_anthropic`. Whether a provider is
Anthropic-type is read from its `type` in `configs/providers.yaml`
(`router.ProviderIsAnthropicNative`), so an Anthropic route that is only unregistered,
for example with no API key, is a retryable 503 rather than either refusal. See
[ADR 0014](../adr/0014-messages-are-forwarded-natively-not-translated.md).

## Field support

| Accepted | Notes |
|---|---|
| `model`, `max_tokens`, `stream`, `temperature`, `top_p`, `top_k`, `stop_sequences` | `stop_sequences` is scanned |
| `system` | A string, or an array of `text` blocks |
| `messages[].role` | `user` or `assistant` |
| Content blocks | `text`, `image` (base64 source only, gated like an `image_url` part), `tool_use`, `tool_result` (string, or `text` and `image` blocks), `thinking`, `redacted_thinking` |
| `cache_control` | `{"type":"ephemeral"}` with an optional `ttl` of `5m` or `1h` |
| `tools[]` | Client tools: `name`, `description`, `input_schema`, `cache_control`, `type: "custom"` |
| `tool_choice` | `auto`, `any`, `none`, or `tool` with `name` |
| `metadata.user_id` | Scanned |
| `thinking`, `context_management`, `output_config` | Forwarded; every string inside is scanned |

Every client string is scanned, including thinking text, thinking signatures and
redacted thinking data. The last two are opaque values the provider verifies, but a
client can put anything in them and it leaves the gateway before the provider rejects
it. The one exception is image data, which is gated rather than scanned
([known limitations §2.7](../evidence/known-limitations.md)). On this endpoint an image is admitted only if
one route of the alias both lists `image` in `unscanned_parts` and is an Anthropic-type
provider; a cleared OpenAI-type route does not count, and the request is refused with the
sealed 451 `unscanned_parts_not_cleared`
([deny reasons §3.4](deny-reasons.md#34-unscanned-content-parts-451)).

| Refused | Why |
|---|---|
| `document`, `search_result` and provider-side tool blocks | AEGIS cannot scan them, or they run outside the audit trail |
| Server tools (`web_search_*`, `code_execution_*`, …) | A provider-side tool fetches or runs things no filter or audit record sees |
| `mcp_servers`, `container` | The provider would act on systems the gateway never sees |
| `service_tier`, `inference_geo` | Not forwarded, and not dropped either |
| An image with a `url` or `file` source | The provider would fetch content the gateway never sees |
| Non-empty `citations` | They refer to document or search sources AEGIS does not forward |

## Headers and query

- `anthropic-version` must be `2023-06-01` when present.
- `anthropic-beta` flags are decided per flag by `anthropic_messages` in
  `configs/gateway.yaml` (`internal/gateway/beta_policy.go`):
  1. A value that is not a well-formed flag (lowercase name, then a `YYYY-MM-DD` or
     `YYYYMMDD` date) is refused, whatever the configuration.
  2. A flag matching `denied_betas` is refused. The defaults cover the families that move
     data or execution to the provider: `files-api-*`, `mcp-client-*`,
     `code-execution-*`, `web-search-*`, `web-fetch-*`, `skills-*`.
  3. A flag in `allowed_betas` is forwarded. The defaults are the twelve flags Claude Code
     2.1.150 was observed sending across print and interactive mode, the session-title
     request, `/init`, and installs with different server-side feature flags.
  4. Any other flag follows `unlisted_betas`. `forward`, the default, forwards it, logs it
     by name and counts it in `aegis_anthropic_beta_unlisted_total{flag}`, with the label
     capped at 32 names. `refuse` refuses it.

  Refusals are 400 `unsupported_beta` and are sealed as `feature_denied`; the flag is
  never sealed. Forwarding is the default because Claude Code's flag set varies by
  release, mode, command and install, and an exact list broke interactive Claude Code on
  every turn. It is a safe default because a beta mostly switches on request constructs
  (block types, tool types, fields), and the decoder refuses every construct it does not
  allowlist whatever flags are set. Which of the forwarded flags change pricing is not
  something AEGIS can verify; `context-1m` does, and is priced through the long-context
  rates.
- `?beta=true`, which the SDKs append, is accepted. Any other query parameter is a 400.

## Streaming

Completion is established only by the provider's `message_stop`. A stream that ends
without it is sealed as `stream_truncated`, through the same outcome logic as a chat
stream (`attestStreamOutcome`). `anthropic_messages.stream_total_timeout` (default 30m)
and `stream_idle_timeout` (default 60s) bound a stream, and the connection's write
deadline is extended to match for these streams, because a coding agent's turn outlasts
`server.write_timeout`.

## `count_tokens`

It sends the whole prompt to the provider, so it passes every gate a message does and is
attested. It is free at the provider and is not priced or written to `usage_records`.

## Running Claude Code through AEGIS

Name the alias after the Claude model id it serves, and route it to that same model:

```yaml
# configs/models.yaml
models:
  claude-haiku-4-5-20251001:
    primary:
      provider: anthropic
      model: claude-haiku-4-5-20251001
      classification_ceiling: INTERNAL
```

Then point Claude Code at the gateway and set the model variables to that id:

```bash
export ANTHROPIC_BASE_URL=http://<gateway>:8080
export ANTHROPIC_API_KEY=<AEGIS key>        # sent as x-api-key
export ANTHROPIC_MODEL=claude-haiku-4-5-20251001
export ANTHROPIC_DEFAULT_OPUS_MODEL=claude-haiku-4-5-20251001
export ANTHROPIC_DEFAULT_SONNET_MODEL=claude-haiku-4-5-20251001
export ANTHROPIC_DEFAULT_HAIKU_MODEL=claude-haiku-4-5-20251001
```

Why the alias carries the model id: Claude Code decides which request features to send,
such as the thinking mode and effort, from the model name it is configured with. AEGIS
forwards the body unchanged apart from the model, so those features reach the provider
as Claude Code chose them. With an arbitrarily named alias Claude Code cannot tell which
model is behind it, and the provider may refuse a feature that model does not support
with a 400, which AEGIS relays. With `ANTHROPIC_MODEL=aegis-claude-haiku` routed to
Haiku 4.5, every turn drew the relayed 400 `adaptive thinking is not supported on this
model`; with an alias named `claude-haiku-4-5-20251001` the same session worked. That
was observed on 2026-10-20 with Claude Code 2.1.150 and Haiku 4.5 only; other models and
releases have not been checked.

AEGIS does not map model ids itself: an id with no alias of that name in
`configs/models.yaml` is refused like any unknown alias. The key's model allowlist
applies to whatever alias Claude Code sends.

Verified on 2026-10-20 with Claude Code 2.1.150 against a gateway on the mock provider:
a turn completed and was sealed as `request_complete`, a prompt carrying an AWS key was
refused with 451 and shown by Claude Code as `API Error: 451 Request blocked: detected
1 secret(s) of type: AWS Access Key`, and an alias outside the key's allowlist was
refused with 403.

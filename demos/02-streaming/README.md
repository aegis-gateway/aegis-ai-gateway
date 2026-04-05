# 02 — Streaming

Demonstrates real-time SSE streaming through the AEGIS gateway, including format normalization and streaming metrics.

## Prerequisites

The **00-quickstart** demo must be running:

```bash
cd demos/00-quickstart && ./run.sh
```

## How to run

```bash
./run.sh
```

The script walks through four interactive steps — press Enter to advance between them.

## What is SSE streaming?

Server-Sent Events (SSE) let the gateway push tokens to the client as they are generated, rather than waiting for the full response. This means the user sees the first words in milliseconds instead of waiting seconds for the complete answer — a significant UX improvement for long responses.

## Anthropic → OpenAI format conversion

Anthropic's native streaming format (`content_block_delta`, `message_stop`, etc.) differs from OpenAI's SSE format (`choices[].delta`, `data: [DONE]`). AEGIS normalizes Anthropic streams on the fly so your client only ever sees one format — the OpenAI-compatible SSE protocol — regardless of which provider is actually serving the request. You never need provider-specific parsing logic.

## Streaming metrics

The gateway exposes five Prometheus metrics for streaming:

| Metric | Type | What it measures |
|--------|------|------------------|
| `aegis_streaming_chunk_total` | Counter | Total SSE chunks sent (by provider/model) |
| `aegis_streaming_time_to_first_token_ms` | Histogram | Time from request to first token |
| `aegis_streaming_tokens_per_second` | Histogram | Token throughput during streaming |
| `aegis_streaming_duration_ms` | Histogram | Total wall-clock duration of the stream |
| `aegis_streaming_error_total` | Counter | Streaming errors by provider and type |

**Time-to-first-token (TTFT)** is the most important streaming metric. It measures how long a user waits before seeing _any_ output. A TTFT regression — even if total generation time stays the same — makes the application feel sluggish. Monitor TTFT percentiles (p50, p95) per provider to catch latency issues before users notice.

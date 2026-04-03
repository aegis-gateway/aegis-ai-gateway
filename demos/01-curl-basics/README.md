# 01 — curl Basics

Interactive walkthrough of every gateway endpoint using curl — health checks, model listing, chat completions, error handling, and database verification.

## Prerequisites

`demos/00-quickstart` must be running:

```bash
cd demos/00-quickstart && ./run.sh
```

## Run

```bash
cd demos/01-curl-basics
./run.sh
```

The script pauses between steps so you can read each response.

## Steps

1. **Health check** — `GET /aegis/v1/health` returns database, Redis, and provider status (no auth required)
2. **List models** — `GET /v1/models` returns available model aliases in OpenAI-compatible format
3. **Chat completion** — `POST /v1/chat/completions` sends a request through the gateway, response includes `usage` (token counts) and `estimated_cost_usd`
4. **Error: missing auth** — request without `Authorization` header returns HTTP 401 with `authentication_error`
5. **Error: unknown model** — request for a non-existent model returns HTTP 503 with `service_unavailable`
6. **Error: missing messages** — request without `messages` field returns HTTP 400 with `invalid_request`
7. **Database records** — queries `usage_records` in PostgreSQL to show that every successful request is logged with model, tokens, and cost

## Key takeaway

The gateway is fully OpenAI-compatible. Any existing OpenAI client library works by changing `base_url`:

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://localhost:8080/v1",
    api_key="aegis-demo-quickstart",
)

response = client.chat.completions.create(
    model="aegis-fast",
    messages=[{"role": "user", "content": "Hello!"}],
)
```

## Next

See [demos/02-streaming](../02-streaming/) for SSE streaming through the gateway.

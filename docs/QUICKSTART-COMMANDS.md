# Quickstart commands: the canonical set

This file is the source of truth for every command published about running the AEGIS demo. If a command appears in `README.md`, in a `demos/*/README.md`, in `deploy/demo/compose.yaml`, or on the website, it must match what is written here.

> **aegisgateway.ai is a separate repository and must be updated whenever this file changes.** The website is not built from this repo and nothing checks the two against each other automatically. A change to a container name, a port, a path, or the demo key here is a change the site needs too. The tail of this file lists the specific commands the site publishes.

Every command below was run verbatim against a clean stack. Container names are the `aegis-demo-` set, which is what the quickstart starts. The contributor services in `deploy/docker-compose.yaml` use the unprefixed names `aegis-postgres` and `aegis-redis`; those are not part of this file and are documented in [CONTRIBUTING.md](../CONTRIBUTING.md).

## Facts every command depends on

| | |
|---|---|
| Entry point | `./quickstart.sh` at the repo root. There is no second script. |
| Gateway | `http://localhost:8080` |
| Metrics | `http://localhost:9090/metrics` |
| Open WebUI | `http://localhost:3000`, only with `--with-webui` |
| Demo API key | `aegis-demo-quickstart` |
| Model alias | `aegis-fast` |
| Postgres container | `aegis-demo-postgres` |
| Redis container | `aegis-demo-redis` |
| Gateway container | `aegis-demo-gateway` |
| WebUI container | `aegis-demo-webui` |
| Database | user `aegis`, database `aegis`, password `aegis-dev` |
| Canary credential | `AKIAIOSFODNN7EXAMPLE`, AWS's own documentation example key |
| Published image | `ghcr.io/aegis-gateway/aegis-ai-gateway:v0.1.0` |
| Provider key | Not required. Without one the gateway uses the mock provider. |

## Starting and stopping

```bash
./quickstart.sh                # start; pulls the published image
./quickstart.sh --build        # start; build from the working tree instead
./quickstart.sh --with-webui   # also start Open WebUI on :3000
./quickstart.sh verify         # run the evidence sequence
./quickstart.sh down           # stop and delete the volumes
./quickstart.sh --help
```

Ports can be overridden:

```bash
GATEWAY_PORT=8088 METRICS_PORT=9091 WEBUI_PORT=3001 ./quickstart.sh
```

## Without cloning

```bash
curl -O https://aegisgateway.ai/demo/compose.yaml
docker compose up
```

That file is [`deploy/demo/compose.yaml`](../deploy/demo/compose.yaml) in this repository. It references published images only and builds nothing. Stop it with `docker compose down -v` from the directory you downloaded it into.

## The evidence sequence

`./quickstart.sh verify` runs all four of these and prints each step. They are listed individually so they can be run by hand, pasted into a terminal one at a time, or published on the site.

### 1. A benign request is permitted

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer aegis-demo-quickstart" \
  -H "Content-Type: application/json" \
  -d '{"model":"aegis-fast","messages":[{"role":"user","content":"Hello from AEGIS!"}]}'
```

Returns HTTP 200 with a completion, token counts, and `estimated_cost_usd`.

### 2. A request carrying a credential is refused

```bash
curl -i http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer aegis-demo-quickstart" \
  -H "Content-Type: application/json" \
  -d '{"model":"aegis-fast","messages":[{"role":"user","content":"My AWS key is AKIAIOSFODNN7EXAMPLE"}]}'
```

Returns HTTP 451:

```json
{"error":{"message":"Request blocked: detected 1 secret(s) of type: AWS Access Key","type":"content_filter_error","code":"content_blocked","aegis_request_id":"req_..."}}
```

### 3. The refusal was recorded

```bash
docker exec aegis-demo-postgres psql -U aegis -d aegis -c \
  "SELECT request_id, timestamp, event_type, status_code, filter_type, reason
     FROM audit_events ORDER BY timestamp DESC LIMIT 5;"
```

Or through the read API, which scopes every query to the calling key's organisation:

```bash
curl http://localhost:8080/aegis/v1/audit/events \
  -H "Authorization: Bearer aegis-demo-quickstart"

curl "http://localhost:8080/aegis/v1/audit/events?format=csv" \
  -H "Authorization: Bearer aegis-demo-quickstart"
```

### 4. The credential is in no row of the database

```bash
docker exec aegis-demo-postgres pg_dump -U aegis aegis | grep -c AKIAIOSFODNN7EXAMPLE
```

Prints `0`.

`grep -c` exits 1 when the count is zero. Zero is the good result here, so do not chain this command with `&&` or run it under `set -e` without accounting for that.

## Other commands published about the demo

Cost tracking:

```bash
docker exec aegis-demo-postgres psql -U aegis -d aegis -c \
  "SELECT model_served, COUNT(*), SUM(estimated_cost_usd)
     FROM usage_records GROUP BY model_served;"
```

Which provider is answering:

```bash
curl -s http://localhost:8080/aegis/v1/health
```

`"mock_provider": true` means completions are answered locally by `internal/router/adapters/mock.go` and no request reaches a provider. Each entry under `providers.details` carries an `adapter` field naming the adapter type serving it.

Available models:

```bash
curl http://localhost:8080/v1/models \
  -H "Authorization: Bearer aegis-demo-quickstart"
```

Prometheus metrics:

```bash
curl -s http://localhost:9090/metrics | grep aegis_
```

Streaming:

```bash
curl -N http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer aegis-demo-quickstart" \
  -H "Content-Type: application/json" \
  -d '{"model":"aegis-fast","stream":true,"messages":[{"role":"user","content":"Count to five"}]}'
```

Gateway logs:

```bash
docker logs aegis-demo-gateway
```

Using a real provider instead of the mock:

```bash
export OPENAI_API_KEY=sk-proj-...      # or ANTHROPIC_API_KEY=sk-ant-...
./quickstart.sh down
./quickstart.sh
```

## What the website publishes

The list below is what aegisgateway.ai currently needs to carry, and what must be re-checked against this file on every change. Anything the site shows that is not on this list is either out of date or was never verified here.

| Where | Command or value |
|---|---|
| Hero / install | `git clone https://github.com/aegis-gateway/aegis-ai-gateway.git` then `cd aegis-ai-gateway` then `./quickstart.sh` |
| No-clone path | `curl -O https://aegisgateway.ai/demo/compose.yaml` then `docker compose up` |
| Demo file hosting | `/demo/compose.yaml` on the site must serve the current [`deploy/demo/compose.yaml`](../deploy/demo/compose.yaml) |
| One-command proof | `./quickstart.sh verify` |
| Benign request | Step 1 above, verbatim |
| Blocked request | Step 2 above, verbatim, with the 451 body |
| Audit query | Step 3 above, verbatim, container `aegis-demo-postgres` |
| The zero | Step 4 above, verbatim, container `aegis-demo-postgres` |
| Demo key | `aegis-demo-quickstart` |
| Ports | 8080 gateway, 9090 metrics, 3000 WebUI |
| Credential requirement | None. Say that the demo needs no API key. |
| Chat UI | Second step only, `./quickstart.sh --with-webui`. Not part of the first-run path. |

### Known site drift, as of this file being written

| Site says | Correct |
|---|---|
| `aegis-demo-postgres` | Correct already. The old README said `aegis-postgres`, which is the contributor stack. |
| A provider API key is required | No longer true. The demo runs with none. |
| `cd demos/00-quickstart && ./run.sh` | That script is gone. `./quickstart.sh` is the only entry point. |
| Open WebUI in the first-run path | Now behind `--with-webui`. |
| `cd demos/00-quickstart && docker compose down -v` | `./quickstart.sh down` |

# 00. Quickstart

The gateway, PostgreSQL, and Redis. Started by `./quickstart.sh` at the repo root, which is the one documented entry point. There is no `run.sh` here: this directory holds the compose files that `quickstart.sh` drives.

## Run

No credentials needed.

```bash
./quickstart.sh
```

With no provider key in the environment the gateway answers completions from the mock provider in [`internal/router/adapters/mock.go`](../../internal/router/adapters/mock.go). Every other stage of the pipeline still runs, so a refusal is a real refusal, written to the real audit trail. Export `OPENAI_API_KEY` or `ANTHROPIC_API_KEY` to use a real provider instead.

## What it shows

Run the whole evidence sequence in one command:

```bash
./quickstart.sh verify
```

It sends a benign request, sends one carrying `AKIAIOSFODNN7EXAMPLE`, shows the 451 and the error body, prints the audit row written for that refusal, and greps a full `pg_dump` for the credential. It exits non-zero if that count is anything other than zero.

Every command it runs is listed in [docs/QUICKSTART-COMMANDS.md](../../docs/QUICKSTART-COMMANDS.md).

## Files

| File | Purpose |
|---|---|
| `docker-compose.yaml` | The stack. Pulls the published gateway image. |
| `docker-compose.build.yaml` | Overlay applied by `--build` to compile from the working tree instead. |

## Chat interface

Open WebUI is not in the default path, because account creation in a chat UI should not stand between a reader and the refusal demo. Add it when you want one:

```bash
./quickstart.sh --with-webui     # http://localhost:3000
```

## Architecture

```
curl or Open WebUI (:3000) -> AEGIS Gateway (:8080) -> OpenAI / Anthropic / mock
                                      |
                              PostgreSQL (audit, usage)
                              Redis (auth cache, rate limits)
```

## Cleanup

```bash
./quickstart.sh down
```

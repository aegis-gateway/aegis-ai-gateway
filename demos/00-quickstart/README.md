# 00 — Quickstart

Full-stack demo: AEGIS gateway + Open WebUI chat interface.

## What it shows

- Multi-provider routing (aegis-fast → Claude Haiku, aegis-gpt4 → GPT-4o)
- Secrets filtering (AWS keys blocked before reaching the provider)
- Cost tracking per model in PostgreSQL
- Prometheus metrics
- OpenAI-compatible API with zero client changes

## Run

If your provider keys are already exported in the shell, it just works:

```bash
export OPENAI_API_KEY=sk-proj-...   # or ANTHROPIC_API_KEY
cd demos/00-quickstart
./run.sh
```

Otherwise the script creates a `.env` file for you to fill in.

Open **http://localhost:3000**, create an account, and start chatting.

## Architecture

```
Browser → Open WebUI (:3000) → AEGIS Gateway (:8080) → OpenAI / Anthropic
                                      ↓
                              PostgreSQL (audit, usage)
                              Redis (auth cache, rate limits)
```

## Cleanup

```bash
docker compose down -v
```

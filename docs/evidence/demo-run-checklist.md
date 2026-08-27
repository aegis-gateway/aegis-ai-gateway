# Demo run checklist

The one run that closes `VERIFICATION.md` §4.5, the last blocker on the landing page's
sentence "The gateway runs and all six demos are runnable."

## History, and what changed

All six demos were exercised on 2026-08-25 (§6.2). Four exited 0. Two did not, and neither
failure was a defect in the gateway: `04-secrets-filter` needed a provider credential for
its fifth assertion, and `00-quickstart` needed to pull `ghcr.io/open-webui/open-webui:main`,
which the sandbox's egress policy denied. Both were inputs, so that run could not settle
the claim in either direction.

**Both inputs have since been removed rather than satisfied.**

- No demo requires a provider credential. With none set, the gateway answers completions
  from the mock provider in `internal/router/adapters/mock.go`, which replaces the upstream
  HTTP call and nothing else. Every filter, the policy engine, classification gating,
  limits, and the audit write still run, so every assertion any demo makes about a refusal
  is a real refusal.
- Open WebUI is no longer in the quickstart path. It sits behind a compose profile and is
  started only by `./quickstart.sh --with-webui`, so `ghcr.io/open-webui/open-webui:main`
  is not on the critical path for any demo.

`04-secrets-filter` also no longer routes its clean request to `gpt-4o` when only
`OPENAI_API_KEY` is set. That model is not an alias in `configs/models.yaml`, so the
request resolved to 503 rather than to an OpenAI route. It uses `aegis-fast` in every case.

## What the machine needs

1. **Docker.** Able to reach Docker Hub for `postgres:16-alpine` and `redis:7-alpine`.
2. Nothing else. No provider key, no Go toolchain, no account anywhere.

Add `ANTHROPIC_API_KEY` or `OPENAI_API_KEY` only if the point of the run is to confirm
behaviour against a real provider. Nothing in the checklist below requires it.

## Run

```bash
# 01 through 03 share one gateway. Start it once and leave it up.
./quickstart.sh

for d in 01-curl-basics 02-streaming 03-cost-tracking; do
  yes '' | ./demos/$d/run.sh 2>&1 | tee /tmp/aegis-demo-$d.log
done

./quickstart.sh verify 2>&1 | tee /tmp/aegis-demo-verify.log
./quickstart.sh down

# 04 and 05 each start their own stack, under the same container names, so the
# quickstart must be down first.
( cd demos/04-secrets-filter  && ./run.sh 2>&1 | tee /tmp/aegis-demo-04.log )
( cd demos/04-secrets-filter  && docker compose down -v )
( cd demos/05-custom-policies && ./run.sh 2>&1 | tee /tmp/aegis-demo-05.log )
( cd demos/05-custom-policies && docker compose down -v )
```

`01` through `03` page through steps with a prompt, which is why they are piped from
`yes ''`. `04` and `05` do not prompt.

## What to send back

The logs, or just these lines, which are the ones that decide the claim:

| Demo | The line that settles it |
|---|---|
| `./quickstart.sh` | reaches its `AEGIS is running` banner |
| `./quickstart.sh verify` | ends `PASS`, with `0` printed under step 4 |
| `01-curl-basics` | step 3 returns a completion, and step 7 shows a non-empty `usage_records` row |
| `02-streaming` | step 1 emits SSE chunks rather than an error envelope |
| `03-cost-tracking` | step 1 shows three `estimated_cost_usd` values rather than `n/a` |
| `04-secrets-filter` | `Results: 5 passed, 0 failed` |
| `05-custom-policies` | both blocked acts report `content_blocked`, and the clean request returns a completion |

## What a mock-provider run does and does not settle

It settles every assertion about refusal, audit, routing, classification, cost
calculation, and the zero-retention claim, because none of those involve the provider
call. `estimated_cost_usd` is computed by `internal/cost` against the real rows in
`configs/pricing.yaml` from token counts the mock reports, so the cost path is exercised
rather than stubbed.

It does not settle that a real provider's response parses correctly, that Anthropic's
streaming format converts to OpenAI's, or that provider authentication works. Those need a
key. `internal/router/adapters/adapters_test.go` covers the transformation logic against
recorded provider payloads; a live run is still the only thing that covers a live
provider.

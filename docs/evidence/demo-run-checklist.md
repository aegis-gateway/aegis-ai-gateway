# Demo run checklist

The one run that closes `VERIFICATION.md` §4.5, the last blocker on the landing page's
sentence "The gateway runs and all six demos are runnable."

All six demos were exercised on 2026-08-25 (§6.2). Four exit 0. Two did not, and neither
failure was a defect: `04-secrets-filter` needs a provider credential for its fifth
assertion, and `00-quickstart` needs to pull `ghcr.io/open-webui/open-webui:main`, which
the sandbox's egress policy denies. Both are inputs, so that run could not settle the
claim in either direction. This one can.

## What the machine needs

1. **A provider API key with credit.** Either works, and which one you set decides which
   model `04-secrets-filter` routes its clean request to:
   - `ANTHROPIC_API_KEY` sends it to `aegis-fast`.
   - `OPENAI_API_KEY` only sends it to `gpt-4o`, because `ResolveRoute` does not fall back
     to an OpenAI route on an auth error.
2. **Docker, able to reach `ghcr.io` and Docker Hub.** Only `00-quickstart` and
   `05-custom-policies` need `ghcr.io`, and only for the Open WebUI container.
3. Nothing else. The demos build the gateway image themselves from the repository
   `Dockerfile`.

## Run

```bash
export ANTHROPIC_API_KEY=sk-ant-...     # or OPENAI_API_KEY
cd demos

for d in 01-curl-basics 02-streaming 03-cost-tracking; do
  ( cd 00-quickstart && ./run.sh )      # only needed once, leaves the stack up
  yes '' | ./$d/run.sh 2>&1 | tee /tmp/aegis-demo-$d.log
done

( cd 04-secrets-filter  && ./run.sh 2>&1 | tee /tmp/aegis-demo-04.log )
( cd 05-custom-policies && ./run.sh 2>&1 | tee /tmp/aegis-demo-05.log )
```

`01` through `03` page through steps with a prompt, which is why they are piped from
`yes ''`. `04` and `05` do not prompt.

## What to send back

The six logs, or just these five lines, which are the ones that decide the claim:

| Demo | The line that settles it |
|---|---|
| `00-quickstart` | that it reaches its `AEGIS Quickstart Demo` banner rather than failing on an image pull |
| `01-curl-basics` | step 3 returns a completion, and step 7 shows a non-empty `usage_records` row |
| `02-streaming` | step 1 emits SSE chunks rather than an error envelope |
| `03-cost-tracking` | step 1 shows three different `estimated_cost_usd` values rather than `n/a` |
| `04-secrets-filter` | `Results: 5 passed, 0 failed` |

`05-custom-policies` already passed on every assertion it makes; only its clean request
needed a credential.

## Two things that will look like failures and are not

- `04-secrets-filter` ends in `docker compose down -v`, so a passing run destroys the
  `audit_events` rows it just printed. Capture the output, not the database.
- The comment at `demos/04-secrets-filter/run.sh:33-42` predicts HTTP 500 for a clean
  request with no usable key. The observed status on 2026-08-25 was 503. That comment is
  wrong, and it is unchanged pending this run confirming what a *working* key produces.

## If `04-secrets-filter` still reports 4 of 5

Then the fifth case fails for a reason other than a missing credential, and that is a real
finding rather than a missing input. Send the log and the gateway's stdout; do not adjust
the page's sentence to match. Rule 9.

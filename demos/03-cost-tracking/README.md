# 03 — Cost Tracking

Track per-request cost across models and providers, all in one place.

## Prerequisites

- **00-quickstart** running (`cd demos/00-quickstart && ./run.sh`)
- `jq` installed

## How to run

```bash
cd demos/03-cost-tracking
./run.sh
```

The script walks through four interactive steps with pause prompts between them.

## What it does

| Step | What |
|------|------|
| 1 | Sends the same prompt ("Say hi in one word") to **aegis-fast**, **aegis-gpt4**, and **aegis-reasoning** — prints `estimated_cost_usd` for each |
| 2 | Queries `usage_records` in PostgreSQL for a per-model cost breakdown |
| 3 | Shows the `aegis_cost_usd_total` Prometheus counter |
| 4 | Prints session totals (all requests since the gateway started) |

## Sample output

**Step 1 — Three model tiers, same prompt:**

```
Request A: aegis-fast (cheapest — Claude Haiku / GPT-4o-mini)
  estimated_cost_usd: 0.000028

Request B: aegis-gpt4 (mid-tier — GPT-4o)
  estimated_cost_usd: 0.000175

Request C: aegis-reasoning (most expensive — Claude Opus / o3)
  estimated_cost_usd: 0.000600

Same prompt, three different cost tiers.
```

**Step 2 — Per-model breakdown:**

```
    model_served     | requests | total_tokens | total_cost_usd
---------------------+----------+--------------+----------------
 claude-opus-4-5     |        1 |           14 |   0.00060000
 gpt-4o              |        1 |           16 |   0.00017500
 claude-haiku-4-5    |        1 |           12 |   0.00002800
(3 rows)
```

**Step 4 — Session total:**

```
 total_requests | session_cost_usd
----------------+------------------
              3 |       0.00080300
(1 row)
```

## So what?

Without a gateway, this data is split across OpenAI's dashboard, Anthropic's console, and whatever logging you remembered to add. AEGIS records it in one place, per request, in a schema you own.

Every row in `usage_records` includes the model requested, the model actually served (after routing/fallback), token counts, and the estimated cost — so you always know what you spent and why.

## How to extend

The `usage_records` table includes `organization_id` and `team_id` columns on every row. Use these for per-team cost attribution:

```sql
SELECT team_id,
       SUM(estimated_cost_usd) AS cost
FROM usage_records
GROUP BY team_id
ORDER BY cost DESC;
```

Teams and orgs are set by the API key's metadata — see the classification demo for how different keys map to different teams and access levels.

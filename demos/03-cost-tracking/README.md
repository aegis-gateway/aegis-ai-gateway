# 03 — Cost Tracking

Track per-request cost across models and providers, all in one place.

## Prerequisites

- The gateway running (`./quickstart.sh` from the repo root; no provider key needed)
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

Captured from an actual run against the mock provider. Token counts and therefore costs
depend on the response, so your numbers will differ; the point is that the three tiers
differ from each other.

**Step 1, three model tiers, same prompt:**

```
Request A: aegis-fast (cheapest: claude-haiku-4-5, falling back to gpt-5.6-luna)
  estimated_cost_usd: 0.000193

Request B: aegis-gpt4 (mid-tier: alias for aegis-balanced, claude-sonnet-5)
  estimated_cost_usd: 0.000386

Request C: aegis-reasoning (most expensive: claude-opus-5, falling back to gpt-5.6-sol)
  estimated_cost_usd: 0.0009649999999999999

Same prompt, three different cost tiers.
```

**Step 2, per-model breakdown:**

```
       model_served        | requests | total_tokens | total_cost_usd
---------------------------+----------+--------------+----------------
 claude-opus-5             |        2 |          104 |     0.00096500
 claude-haiku-4-5-20251001 |        4 |          211 |     0.00059600
 claude-sonnet-5           |        1 |           45 |     0.00038600
(3 rows)
```

Note the `model_served` column: `aegis-gpt4` is a deprecated alias, and the row records
`claude-sonnet-5`, the model actually served, not the alias that was asked for.

**Step 4, session total:**

```
 total_requests | session_cost_usd
----------------+------------------
              7 |       0.00194700
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

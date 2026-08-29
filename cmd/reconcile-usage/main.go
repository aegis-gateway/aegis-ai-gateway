// Copyright 2026 Atlantic Frontier Corporations LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Command reconcile-usage reprices historical usage_records against the current
// pricing configuration and reports what changed.
//
// It exists because four separate defects moved estimated_cost_usd in both
// directions — cached input billed at the full rate, then at zero, cache writes
// billed as ordinary input at a tenfold-wrong rate, and streamed requests
// recording no spend at all — and the rows those defects wrote are still the
// basis of every spend aggregate and budget decision.
//
// What it can and cannot do is the whole design:
//
//   - A row written from migration 014 onwards carries its own cache breakdown,
//     so its cost is a pure function of columns that are still there. Those rows
//     reprice EXACTLY, and -apply will write the corrected value.
//
//   - A row written before that carries prompt_tokens as one opaque number. The
//     cached and written subsets are gone, and they are priced between 0.1x and
//     2x base input, so the true cost is a RANGE rather than a number. Those
//     rows are reported as bounds and are never rewritten. A tool that guessed a
//     point value here would be inventing financial data.
//
// The bounds are still worth having: when a recorded cost falls outside them it
// is provably wrong regardless of what the cache split was, which is the closest
// thing to proof of the historical exposure that the data supports.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/cost"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "reconcile-usage: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		dbURL       = flag.String("database-url", os.Getenv("DATABASE_URL"), "Postgres connection string (defaults to $DATABASE_URL)")
		pricingPath = flag.String("pricing", "configs/pricing.yaml", "path to pricing.yaml")
		sinceStr    = flag.String("since", "", "only consider rows created at or after this RFC3339 time")
		untilStr    = flag.String("until", "", "only consider rows created before this RFC3339 time")
		detailAfter = flag.String("detail-recorded-after", "", "RFC3339 time at which migration 014 was deployed; rows at or after it are repriced exactly, everything earlier is reported as bounds only")
		groupBy     = flag.String("group-by", "day", "day, org, team, model or provider")
		apply       = flag.Bool("apply", false, "write corrected costs for exactly repriceable rows (requires -detail-recorded-after)")
		limit       = flag.Int("limit", 0, "stop after this many rows (0 means all)")
	)
	flag.Parse()

	if *dbURL == "" {
		return fmt.Errorf("no database URL: pass -database-url or set DATABASE_URL")
	}

	var cutover time.Time
	if *detailAfter != "" {
		t, err := time.Parse(time.RFC3339, *detailAfter)
		if err != nil {
			return fmt.Errorf("parsing -detail-recorded-after: %w", err)
		}
		cutover = t
	}
	if *apply && cutover.IsZero() {
		// Without the cutover every row is bounds-only, and applying a bound as
		// if it were a measurement is the one thing this tool must not do.
		return fmt.Errorf("-apply needs -detail-recorded-after: without it no row is known to carry the cache detail its cost depends on")
	}

	window, err := parseWindow(*sinceStr, *untilStr)
	if err != nil {
		return err
	}

	pricing := &config.PricingConfig{}
	if err := config.LoadFile(*pricingPath, pricing); err != nil {
		return fmt.Errorf("loading %s: %w", *pricingPath, err)
	}
	calc := cost.NewCalculator(func() *config.PricingConfig { return pricing })

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, *dbURL)
	if err != nil {
		return fmt.Errorf("connecting: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("reaching the database: %w", err)
	}

	rows, err := loadRows(ctx, pool, window, *limit)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Println("no usage rows in the selected window")
		return nil
	}

	report := reconcile(rows, calc, cutover)
	printReport(report, *groupBy, cutover)

	if !*apply {
		if report.exactChanged > 0 {
			fmt.Printf("\nnothing was written. Re-run with -apply to correct the %d exactly repriceable row(s).\n", report.exactChanged)
		}
		return nil
	}
	return applyCorrections(ctx, pool, report)
}

type window struct{ since, until time.Time }

func parseWindow(sinceStr, untilStr string) (window, error) {
	var w window
	if sinceStr != "" {
		t, err := time.Parse(time.RFC3339, sinceStr)
		if err != nil {
			return w, fmt.Errorf("parsing -since: %w", err)
		}
		w.since = t
	}
	if untilStr != "" {
		t, err := time.Parse(time.RFC3339, untilStr)
		if err != nil {
			return w, fmt.Errorf("parsing -until: %w", err)
		}
		w.until = t
	}
	if !w.since.IsZero() && !w.until.IsZero() && !w.until.After(w.since) {
		return w, fmt.Errorf("-until (%s) is not after -since (%s)", untilStr, sinceStr)
	}
	return w, nil
}

// usageRow is one row of usage_records, carrying only what pricing depends on.
type usageRow struct {
	id         int64
	createdAt  time.Time
	org        string
	team       string
	provider   string
	model      string
	prompt     int
	completion int
	cached     int
	write5m    int
	write1h    int
	recorded   float64
	stream     bool
}

func loadRows(ctx context.Context, pool *pgxpool.Pool, w window, limit int) ([]usageRow, error) {
	// A zero time bound is expressed as NULL rather than a sentinel date, so an
	// unset flag means "no bound" and never silently excludes real rows.
	query := `
		SELECT id, created_at, organization_id, team_id, provider, model_served,
		       prompt_tokens, completion_tokens,
		       cached_tokens, cache_write_5m_tokens, cache_write_1h_tokens,
		       estimated_cost_usd, stream
		  FROM usage_records
		 WHERE ($1::timestamptz IS NULL OR created_at >= $1)
		   AND ($2::timestamptz IS NULL OR created_at <  $2)
		 ORDER BY created_at`
	args := []any{nullTime(w.since), nullTime(w.until)}
	if limit > 0 {
		query += fmt.Sprintf("\n LIMIT %d", limit)
	}

	rs, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying usage_records: %w", err)
	}
	defer rs.Close()

	var out []usageRow
	for rs.Next() {
		var r usageRow
		if err := rs.Scan(&r.id, &r.createdAt, &r.org, &r.team, &r.provider, &r.model,
			&r.prompt, &r.completion, &r.cached, &r.write5m, &r.write1h,
			&r.recorded, &r.stream); err != nil {
			return nil, fmt.Errorf("scanning a usage row: %w", err)
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// verdict classifies what could be established about one row.
type verdict int

const (
	// verdictExact means the row carries its own cache breakdown, so the
	// recomputed cost is the cost.
	verdictExact verdict = iota
	// verdictBounded means the cache split is unrecorded, so only a range is
	// derivable. The row is never rewritten.
	verdictBounded
	// verdictUnpriceable means pricing.yaml has no entry for the row's
	// provider and model, so nothing can be said about it.
	verdictUnpriceable
	// verdictNoTokens means the row recorded no tokens at all. Repricing zero
	// tokens yields zero, which is not evidence the request was free — it is
	// the fingerprint of the streamed-usage defect.
	verdictNoTokens
)

type finding struct {
	row        usageRow
	verdict    verdict
	recomputed float64 // verdictExact only
	low, high  float64 // verdictBounded only
	outside    bool    // verdictBounded: recorded falls outside [low, high]
}

type reportData struct {
	findings     []finding
	exactChanged int
	groups       map[string]*groupTotals
}

type groupTotals struct {
	key                   string
	rows                  int
	exact, bounded        int
	unpriceable, noTokens int
	recordedSum           float64
	exactSum              float64
	lowSum, highSum       float64
	outsideRows           int
	exactDelta            float64
}

func reconcile(rows []usageRow, calc *cost.Calculator, cutover time.Time) reportData {
	rep := reportData{groups: map[string]*groupTotals{}}

	for _, r := range rows {
		f := finding{row: r}

		switch {
		case r.prompt == 0 && r.completion == 0:
			f.verdict = verdictNoTokens

		case !calc.HasPricing(r.provider, r.model):
			f.verdict = verdictUnpriceable

		case !cutover.IsZero() && !r.createdAt.Before(cutover):
			f.verdict = verdictExact
			c, ok := calc.Calculate(cost.RequestDetails{
				Provider:           r.provider,
				Model:              r.model,
				PromptTokens:       r.prompt,
				CachedTokens:       r.cached,
				CompletionTokens:   r.completion,
				CacheWrite5mTokens: r.write5m,
				CacheWrite1hTokens: r.write1h,
			})
			if !ok {
				f.verdict = verdictUnpriceable
				break
			}
			f.recomputed = c
			if !sameCost(c, r.recorded) {
				rep.exactChanged++
			}

		default:
			f.verdict = verdictBounded
			// Cheapest the prompt could have been: every token a cache read.
			// Dearest: every token written to a one-hour entry, at 2x input.
			// The true cost is somewhere between, and no column left in the row
			// narrows it further.
			low, lowOK := calc.Calculate(cost.RequestDetails{
				Provider: r.provider, Model: r.model,
				PromptTokens: r.prompt, CachedTokens: r.prompt,
				CompletionTokens: r.completion,
			})
			high, highOK := calc.Calculate(cost.RequestDetails{
				Provider: r.provider, Model: r.model,
				PromptTokens: r.prompt, CacheWrite1hTokens: r.prompt,
				CompletionTokens: r.completion,
			})
			if !lowOK || !highOK {
				f.verdict = verdictUnpriceable
				break
			}
			f.low, f.high = low, high
			f.outside = r.recorded < low-costEpsilon || r.recorded > high+costEpsilon
		}

		rep.findings = append(rep.findings, f)
	}
	return rep
}

// costEpsilon is the tolerance for comparing two float dollar amounts. Costs are
// sums of products of rates and token counts, so two arithmetically equal values
// can differ in the last bits; a tenth of a cent per million is far below any
// amount that matters and far above that noise.
const costEpsilon = 1e-9

func sameCost(a, b float64) bool {
	d := a - b
	return d < costEpsilon && d > -costEpsilon
}

func groupKey(r usageRow, by string) string {
	switch by {
	case "org":
		return r.org
	case "team":
		return r.team
	case "model":
		return r.model
	case "provider":
		return r.provider
	default:
		return r.createdAt.UTC().Format("2006-01-02")
	}
}

func printReport(rep reportData, groupBy string, cutover time.Time) {
	for _, f := range rep.findings {
		key := groupKey(f.row, groupBy)
		g, ok := rep.groups[key]
		if !ok {
			g = &groupTotals{key: key}
			rep.groups[key] = g
		}
		g.rows++
		g.recordedSum += f.row.recorded

		switch f.verdict {
		case verdictExact:
			g.exact++
			g.exactSum += f.recomputed
			g.exactDelta += f.recomputed - f.row.recorded
		case verdictBounded:
			g.bounded++
			g.lowSum += f.low
			g.highSum += f.high
			if f.outside {
				g.outsideRows++
			}
		case verdictUnpriceable:
			g.unpriceable++
		case verdictNoTokens:
			g.noTokens++
		}
	}

	keys := make([]string, 0, len(rep.groups))
	for k := range rep.groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	if cutover.IsZero() {
		fmt.Println("No -detail-recorded-after given, so every row is treated as bounds-only.")
		fmt.Println("Pass the time migration 014 was deployed to reprice later rows exactly.")
		fmt.Println()
	}

	fmt.Printf("%-24s %7s %9s %12s %12s %12s %12s %8s\n",
		groupBy, "rows", "recorded", "exact", "delta", "bound low", "bound high", "outside")
	fmt.Println(dashes(103))

	var tot groupTotals
	for _, k := range keys {
		g := rep.groups[k]
		fmt.Printf("%-24s %7d %9.4f %12s %12s %12s %12s %8s\n",
			truncate(k, 24), g.rows, g.recordedSum,
			amountOrDash(g.exactSum, g.exact > 0),
			amountOrDash(g.exactDelta, g.exact > 0),
			amountOrDash(g.lowSum, g.bounded > 0),
			amountOrDash(g.highSum, g.bounded > 0),
			countOrDash(g.outsideRows, g.bounded > 0))

		tot.rows += g.rows
		tot.recordedSum += g.recordedSum
		tot.exact += g.exact
		tot.exactSum += g.exactSum
		tot.exactDelta += g.exactDelta
		tot.bounded += g.bounded
		tot.lowSum += g.lowSum
		tot.highSum += g.highSum
		tot.outsideRows += g.outsideRows
		tot.unpriceable += g.unpriceable
		tot.noTokens += g.noTokens
	}

	fmt.Println(dashes(103))
	fmt.Printf("%-24s %7d %9.4f %12s %12s %12s %12s %8s\n",
		"TOTAL", tot.rows, tot.recordedSum,
		amountOrDash(tot.exactSum, tot.exact > 0),
		amountOrDash(tot.exactDelta, tot.exact > 0),
		amountOrDash(tot.lowSum, tot.bounded > 0),
		amountOrDash(tot.highSum, tot.bounded > 0),
		countOrDash(tot.outsideRows, tot.bounded > 0))

	fmt.Println()
	fmt.Printf("Exactly repriceable: %d row(s) — %d already correct, %d mispriced.\n",
		tot.exact, tot.exact-rep.exactChanged, rep.exactChanged)
	fmt.Printf("Bounds only: %d row(s) predate the cache breakdown, of which %d recorded a "+
		"cost outside the achievable range and are wrong whatever the cache split was.\n",
		tot.bounded, tot.outsideRows)

	if tot.noTokens > 0 {
		fmt.Printf("%d row(s) recorded no tokens at all. Repricing those yields zero, which is "+
			"not evidence the requests were free: it is the signature of the streamed-usage "+
			"defect, and the counts to reprice from were never captured.\n", tot.noTokens)
	}
	if tot.unpriceable > 0 {
		fmt.Printf("%d row(s) name a provider and model with no entry in the pricing config, "+
			"so nothing can be established about them.\n", tot.unpriceable)
	}
}

func amountOrDash(v float64, show bool) string {
	if !show {
		return "-"
	}
	return fmt.Sprintf("%.4f", v)
}

func countOrDash(n int, show bool) string {
	if !show {
		return "-"
	}
	return fmt.Sprintf("%d", n)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func dashes(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = '-'
	}
	return string(b)
}

// applyCorrections writes recomputed costs for the exactly repriceable rows.
//
// Only verdictExact rows are touched. Bounded rows are left exactly as they are:
// their true cost is unknown, and writing a bound would turn "we cannot tell"
// into a number that later readers would treat as measured.
func applyCorrections(ctx context.Context, pool *pgxpool.Pool, rep reportData) error {
	var toWrite []finding
	for _, f := range rep.findings {
		if f.verdict == verdictExact && !sameCost(f.recomputed, f.row.recorded) {
			toWrite = append(toWrite, f)
		}
	}
	if len(toWrite) == 0 {
		fmt.Println("\nnothing to write: every exactly repriceable row already carries its correct cost.")
		return nil
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	batch := &pgx.Batch{}
	for _, f := range toWrite {
		batch.Queue("UPDATE usage_records SET estimated_cost_usd = $1 WHERE id = $2",
			f.recomputed, f.row.id)
	}
	br := tx.SendBatch(ctx, batch)
	for range toWrite {
		if _, err := br.Exec(); err != nil {
			_ = br.Close()
			return fmt.Errorf("updating a usage row: %w", err)
		}
	}
	if err := br.Close(); err != nil {
		return fmt.Errorf("closing the update batch: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing: %w", err)
	}

	fmt.Printf("\nrewrote estimated_cost_usd on %d row(s).\n", len(toWrite))
	return nil
}

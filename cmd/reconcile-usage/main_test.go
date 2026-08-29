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

package main

import (
	"testing"
	"time"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/cost"
)

// testCalculator prices one model, with the published Anthropic multipliers: a
// cache read at 0.1x input, a five-minute write at 1.25x, a one-hour write at 2x.
func testCalculator() *cost.Calculator {
	pricing := &config.PricingConfig{
		Providers: map[string]config.ProviderPricing{
			"anthropic": {Models: map[string]config.PriceEntry{
				"claude-test": {
					Input:        10.00,
					CachedInput:  1.00,
					CacheWrite5m: 12.50,
					CacheWrite1h: 20.00,
					Output:       50.00,
				},
			}},
		},
	}
	return cost.NewCalculator(func() *config.PricingConfig { return pricing })
}

var cutover = time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

func baseRow() usageRow {
	return usageRow{
		id:        1,
		createdAt: cutover.Add(time.Hour), // after the cutover unless overridden
		org:       "org",
		provider:  "anthropic",
		model:     "claude-test",
	}
}

func TestReconcile_ExactRowIsRepricedFromItsOwnColumns(t *testing.T) {
	r := baseRow()
	r.prompt = 1_000_000
	r.completion = 0
	r.cached = 1_000_000 // the whole prompt was a cache read
	r.recorded = 10.00   // what the pre-#59 calculator charged: full input rate

	rep := reconcile([]usageRow{r}, testCalculator(), cutover)
	f := rep.findings[0]

	if f.verdict != verdictExact {
		t.Fatalf("verdict = %v, want verdictExact", f.verdict)
	}
	// A fully cached million-token prompt is priced at cached_input, not input.
	if !sameCost(f.recomputed, 1.00) {
		t.Errorf("recomputed = %v, want 1.00 (cached_input on the whole prompt)", f.recomputed)
	}
	if rep.exactChanged != 1 {
		t.Errorf("exactChanged = %d, want 1: the recorded 10.00 is tenfold the true cost", rep.exactChanged)
	}
}

func TestReconcile_ExactRowAlreadyCorrectIsNotCounted(t *testing.T) {
	r := baseRow()
	r.prompt = 1_000_000
	r.cached = 1_000_000
	r.recorded = 1.00 // already right

	rep := reconcile([]usageRow{r}, testCalculator(), cutover)
	if rep.findings[0].verdict != verdictExact {
		t.Fatalf("verdict = %v, want verdictExact", rep.findings[0].verdict)
	}
	if rep.exactChanged != 0 {
		t.Errorf("exactChanged = %d, want 0: a correct row must not be rewritten", rep.exactChanged)
	}
}

func TestReconcile_PreCutoverRowIsBoundedNotRepriced(t *testing.T) {
	r := baseRow()
	r.createdAt = cutover.Add(-time.Hour)
	r.prompt = 1_000_000
	r.recorded = 10.00

	rep := reconcile([]usageRow{r}, testCalculator(), cutover)
	f := rep.findings[0]

	if f.verdict != verdictBounded {
		t.Fatalf("verdict = %v, want verdictBounded", f.verdict)
	}
	// Cheapest: the whole prompt a cache read at 1.00/M. Dearest: the whole
	// prompt written to a one-hour entry at 20.00/M.
	if !sameCost(f.low, 1.00) {
		t.Errorf("low = %v, want 1.00", f.low)
	}
	if !sameCost(f.high, 20.00) {
		t.Errorf("high = %v, want 20.00", f.high)
	}
	if f.outside {
		t.Error("10.00 lies inside [1.00, 20.00] and must not be flagged as outside")
	}
	if f.recomputed != 0 {
		t.Error("a bounded row must not carry a recomputed point value")
	}
	if rep.exactChanged != 0 {
		t.Error("a bounded row must never be counted as needing a rewrite")
	}
}

func TestReconcile_RecordedCostBelowTheCheapestPossibleIsFlagged(t *testing.T) {
	// The #59 bypass: an absent cached_input rate priced the cached subset at
	// zero, so a fully cached request recorded no input cost at all. That is
	// below the cheapest achievable price, which is what makes it provable
	// without knowing the cache split.
	r := baseRow()
	r.createdAt = cutover.Add(-time.Hour)
	r.prompt = 1_000_000
	r.recorded = 0.00

	rep := reconcile([]usageRow{r}, testCalculator(), cutover)
	f := rep.findings[0]

	if f.verdict != verdictBounded {
		t.Fatalf("verdict = %v, want verdictBounded", f.verdict)
	}
	if !f.outside {
		t.Errorf("recorded 0.00 is below the 1.00 floor and must be flagged")
	}
}

func TestReconcile_RecordedCostAboveTheDearestPossibleIsFlagged(t *testing.T) {
	r := baseRow()
	r.createdAt = cutover.Add(-time.Hour)
	r.prompt = 1_000_000
	r.recorded = 25.00 // above the 20.00 ceiling

	rep := reconcile([]usageRow{r}, testCalculator(), cutover)
	if !rep.findings[0].outside {
		t.Error("recorded 25.00 exceeds the 20.00 ceiling and must be flagged")
	}
}

func TestReconcile_WithoutACutoverEveryRowIsBounded(t *testing.T) {
	// The zero cutover is the safe default: no row is known to carry the cache
	// detail its cost depends on, so none may be rewritten.
	r := baseRow()
	r.prompt = 1_000_000
	r.cached = 1_000_000
	r.recorded = 10.00

	rep := reconcile([]usageRow{r}, testCalculator(), time.Time{})
	if rep.findings[0].verdict != verdictBounded {
		t.Errorf("verdict = %v, want verdictBounded with no cutover", rep.findings[0].verdict)
	}
	if rep.exactChanged != 0 {
		t.Errorf("exactChanged = %d, want 0 with no cutover", rep.exactChanged)
	}
}

func TestReconcile_ZeroTokenRowIsNotCalledFree(t *testing.T) {
	// Streamed requests recorded prompt_tokens=0, completion_tokens=0 and a zero
	// cost before 0a2bec3. Repricing zero tokens yields zero, which would agree
	// with the recorded value and report the row as correct.
	r := baseRow()
	r.stream = true
	r.recorded = 0

	rep := reconcile([]usageRow{r}, testCalculator(), cutover)
	if rep.findings[0].verdict != verdictNoTokens {
		t.Errorf("verdict = %v, want verdictNoTokens: a zero-token row is unrecorded, not free",
			rep.findings[0].verdict)
	}
	if rep.exactChanged != 0 {
		t.Error("a zero-token row must not be counted as repriceable")
	}
}

func TestReconcile_UnpricedModelIsReportedNotGuessed(t *testing.T) {
	r := baseRow()
	r.model = "a-model-with-no-price"
	r.prompt = 1000
	r.recorded = 0.5

	rep := reconcile([]usageRow{r}, testCalculator(), cutover)
	if rep.findings[0].verdict != verdictUnpriceable {
		t.Errorf("verdict = %v, want verdictUnpriceable", rep.findings[0].verdict)
	}
}

func TestParseWindow_RejectsAnInvertedRange(t *testing.T) {
	if _, err := parseWindow("2026-08-29T00:00:00Z", "2026-08-28T00:00:00Z"); err == nil {
		t.Error("an -until before -since should be rejected rather than silently matching nothing")
	}
}

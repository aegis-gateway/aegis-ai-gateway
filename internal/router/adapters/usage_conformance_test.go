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

package adapters

import (
	"testing"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
)

// Two independent implementations turn an Anthropic usage block into canonical
// token counts: anthropicUsageToCanonical for a whole response, and the
// streaming transformer accumulating across events. They are billed identically
// downstream, so they must agree — and twice they did not.
//
//	#59  absorbUsage rebuilt the prompt total from whichever fields the current
//	     event carried, so a later message_delta without the cache fields turned
//	     1005 into 5. The calculator then computed uncached = 5 - 1000, clamped
//	     it to zero, and billed none of the uncached input.
//	#60  The transformer kept cache writes as one aggregate and emitted no tier
//	     fields, so every streamed cache-warming request was billed at 1x input
//	     instead of 1.25x or 2x.
//
// Both were found in review rather than by a test, both times because the
// non-streaming path was fixed and the streaming path was not. This asserts the
// property directly: for the same provider usage, both paths must produce the
// same canonical Usage. A future divergence fails here rather than in someone's
// bill.
func TestUsageConformance_StreamingMatchesNonStreaming(t *testing.T) {
	cases := map[string]struct {
		// events is the usage sequence a stream observes. The non-streaming
		// path sees the equivalent single block, given by final.
		events []anthropicUsage
		final  anthropicUsage
	}{
		"plain request, no cache": {
			events: []anthropicUsage{
				{InputTokens: 100},
				{OutputTokens: 20},
			},
			final: anthropicUsage{InputTokens: 100, OutputTokens: 20},
		},
		"cache read": {
			events: []anthropicUsage{
				{InputTokens: 5, CacheReadInputTokens: 1000},
				{OutputTokens: 20},
			},
			final: anthropicUsage{InputTokens: 5, CacheReadInputTokens: 1000, OutputTokens: 20},
		},
		"cache read, later event repeats input_tokens without cache fields": {
			// The #59 shape. Anthropic's message_delta may carry input_tokens
			// again while omitting the cache fields.
			events: []anthropicUsage{
				{InputTokens: 5, CacheReadInputTokens: 1000},
				{InputTokens: 5, OutputTokens: 20},
			},
			final: anthropicUsage{InputTokens: 5, CacheReadInputTokens: 1000, OutputTokens: 20},
		},
		"cache write, both tiers": {
			// The #60 shape.
			events: []anthropicUsage{
				{
					InputTokens:              10,
					CacheCreationInputTokens: 1500,
					CacheCreation:            anthropicCacheCreation{Ephemeral5m: 1000, Ephemeral1h: 500},
				},
				{OutputTokens: 7},
			},
			final: anthropicUsage{
				InputTokens:              10,
				CacheCreationInputTokens: 1500,
				CacheCreation:            anthropicCacheCreation{Ephemeral5m: 1000, Ephemeral1h: 500},
				OutputTokens:             7,
			},
		},
		"cache write reported only as an aggregate": {
			events: []anthropicUsage{
				{InputTokens: 10, CacheCreationInputTokens: 800},
				{OutputTokens: 7},
			},
			final: anthropicUsage{InputTokens: 10, CacheCreationInputTokens: 800, OutputTokens: 7},
		},
		"read and write together": {
			events: []anthropicUsage{
				{
					InputTokens:              10,
					CacheReadInputTokens:     400,
					CacheCreationInputTokens: 600,
					CacheCreation:            anthropicCacheCreation{Ephemeral5m: 600},
				},
				{OutputTokens: 9},
			},
			final: anthropicUsage{
				InputTokens:              10,
				CacheReadInputTokens:     400,
				CacheCreationInputTokens: 600,
				CacheCreation:            anthropicCacheCreation{Ephemeral5m: 600},
				OutputTokens:             9,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			want := anthropicUsageToCanonical(tc.final)

			tr := &anthropicStreamTransformer{}
			for _, e := range tc.events {
				tr.absorbUsage(e)
			}
			write5m, write1h := tr.cacheWriteTiers()
			got := types.Usage{
				PromptTokens:     tr.promptTokens(),
				CompletionTokens: tr.outputTokens,
				TotalTokens:      tr.promptTokens() + tr.outputTokens,
			}
			if tr.cachedTokens > 0 || write5m > 0 || write1h > 0 {
				got.PromptTokensDetails = &types.PromptTokensDetails{
					CachedTokens:       tr.cachedTokens,
					CacheWrite5mTokens: write5m,
					CacheWrite1hTokens: write1h,
				}
			}

			if got.PromptTokens != want.PromptTokens {
				t.Errorf("prompt tokens: streaming %d, non-streaming %d — the two paths bill differently",
					got.PromptTokens, want.PromptTokens)
			}
			if got.CompletionTokens != want.CompletionTokens {
				t.Errorf("completion tokens: streaming %d, non-streaming %d", got.CompletionTokens, want.CompletionTokens)
			}
			if got.TotalTokens != want.TotalTokens {
				t.Errorf("total tokens: streaming %d, non-streaming %d", got.TotalTokens, want.TotalTokens)
			}

			gd, wd := got.PromptTokensDetails, want.PromptTokensDetails
			switch {
			case gd == nil && wd == nil:
			case gd == nil || wd == nil:
				t.Fatalf("prompt_tokens_details: streaming %+v, non-streaming %+v — one path "+
					"reports a cache breakdown and the other does not", gd, wd)
			default:
				if gd.CachedTokens != wd.CachedTokens {
					t.Errorf("cached tokens: streaming %d, non-streaming %d", gd.CachedTokens, wd.CachedTokens)
				}
				if gd.CacheWrite5mTokens != wd.CacheWrite5mTokens {
					t.Errorf("5m cache write: streaming %d, non-streaming %d — a tier billed at "+
						"the wrong rate", gd.CacheWrite5mTokens, wd.CacheWrite5mTokens)
				}
				if gd.CacheWrite1hTokens != wd.CacheWrite1hTokens {
					t.Errorf("1h cache write: streaming %d, non-streaming %d — a tier billed at "+
						"the wrong rate", gd.CacheWrite1hTokens, wd.CacheWrite1hTokens)
				}
			}
		})
	}
}

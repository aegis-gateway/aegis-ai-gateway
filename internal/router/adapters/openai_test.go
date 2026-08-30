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
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// An absent usage object and one carrying zeros are different facts, and
// audit_events seals the counts at hash_schema_version=3. Decoding both into a
// value type made them indistinguishable.
func TestTransformResponse_DistinguishesAbsentUsageFromReportedZeros(t *testing.T) {
	a := &OpenAIAdapter{}
	ctx := context.Background()

	for _, tc := range []struct {
		name         string
		body         string
		wantReported bool
	}{
		{
			"no usage object",
			`{"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"x"},"finish_reason":"stop"}]}`,
			false,
		},
		{
			"usage object of zeros",
			`{"model":"m","usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0},"choices":[{"index":0,"message":{"role":"assistant","content":"x"},"finish_reason":"stop"}]}`,
			true,
		},
		{
			"zero completion beside a nonzero prompt",
			`{"model":"m","usage":{"prompt_tokens":7,"completion_tokens":0,"total_tokens":7},"choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"stop"}]}`,
			true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(tc.body)),
				Header:     make(http.Header),
			}
			out, err := a.TransformResponse(ctx, resp)
			if err != nil {
				t.Fatalf("transforming: %v", err)
			}
			if out.UsageReported != tc.wantReported {
				t.Errorf("UsageReported = %v, want %v; an absent usage object and a "+
					"reported zero must not seal the same thing",
					out.UsageReported, tc.wantReported)
			}
		})
	}
}

// An Anthropic message_delta reporting an all-zero usage object is a
// measurement. Gating the relayed usage on a positive count dropped it, which
// left metrics.UsageReported false downstream and sealed three NULLs meaning
// "the provider reported nothing".
func TestAnthropicStream_AllZeroUsageIsStillReported(t *testing.T) {
	tr := &anthropicStreamTransformer{toolOrdinal: map[int]int{}}

	start := `{"type":"message_start","message":{"model":"claude-test","usage":{"input_tokens":0}}}`
	if _, err := tr.Transform([]byte(start)); err != nil {
		t.Fatalf("message_start: %v", err)
	}
	delta := `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":0}}`
	out, err := tr.Transform([]byte(delta))
	if err != nil {
		t.Fatalf("message_delta: %v", err)
	}
	if !strings.Contains(string(out), `"usage"`) {
		t.Errorf("an all-zero usage object was dropped from the relayed chunk, so the "+
			"sealed row records no measurement where the provider reported zeros; got %s",
			string(out))
	}
}

// The converse: a stream that never reports usage must not gain one.
func TestAnthropicStream_NoUsageObjectStaysAbsent(t *testing.T) {
	tr := &anthropicStreamTransformer{toolOrdinal: map[int]int{}}

	if _, err := tr.Transform([]byte(`{"type":"message_start","message":{"model":"claude-test"}}`)); err != nil {
		t.Fatalf("message_start: %v", err)
	}
	out, err := tr.Transform([]byte(`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`))
	if err != nil {
		t.Fatalf("message_delta: %v", err)
	}
	if strings.Contains(string(out), `"usage"`) {
		t.Errorf("a stream that reported no usage gained a usage object: %s", string(out))
	}
}

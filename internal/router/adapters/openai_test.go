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

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

	"github.com/aegis-gateway/aegis-ai-gateway/internal/redact"
)

// The non-streaming path never logs a provider body directly. The adapter wraps
// it into an error and internal/gateway/handler.go logs that error, so the
// bound has to be applied where the error is built or it is not applied at all.
//
// Every adapter is covered rather than just the one named in the review: they
// share the shape, and fixing one while leaving the others is how the streaming
// and non-streaming cost defects both reached main.
func TestTransformResponse_ErrorCarriesOnlyABoundedExcerpt(t *testing.T) {
	const tailMarker = "CALLER_CONTENT_ECHOED_AT_THE_END"
	body := `{"error":{"message":"` + strings.Repeat("x", 4000) + tailMarker + `"}}`

	adapters := map[string]ProviderAdapter{
		"openai":    &OpenAIAdapter{},
		"anthropic": &AnthropicAdapter{},
	}

	for name, adapter := range adapters {
		t.Run(name, func(t *testing.T) {
			resp := &http.Response{
				StatusCode: http.StatusBadRequest,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
			}

			_, err := adapter.TransformResponse(context.Background(), resp)
			if err == nil {
				t.Fatal("a non-200 provider response must produce an error")
			}
			msg := err.Error()

			if strings.Contains(msg, tailMarker) {
				t.Errorf("the end of a %d-byte body reached the error, which the handler logs", len(body))
			}
			if strings.Contains(msg, strings.Repeat("x", redact.ExcerptLimit+1)) {
				t.Errorf("more than ExcerptLimit=%d characters of the body reached the error",
					redact.ExcerptLimit)
			}
			if !strings.Contains(msg, "truncated") {
				t.Errorf("the error does not mark the body as truncated: %s", msg)
			}
			// The status has to survive: it is the part an operator acts on.
			if !strings.Contains(msg, "400") {
				t.Errorf("the error lost the provider status code: %s", msg)
			}
		})
	}
}

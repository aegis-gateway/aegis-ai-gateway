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
	"testing"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
)

func TestNativeStreamUsage_CacheAndCumulativeOutput(t *testing.T) {
	var u NativeStreamUsage
	u.Observe([]byte(`{"type":"message_start","message":{"model":"claude-x","usage":{"input_tokens":10,"cache_read_input_tokens":200,"cache_creation_input_tokens":30,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":30},"output_tokens":1}}}`))
	u.Observe([]byte(`{"type":"ping"}`))
	u.Observe([]byte(`{"type":"message_delta","usage":{"output_tokens":5}}`))
	u.Observe([]byte(`{"type":"message_delta","usage":{"output_tokens":9}}`))
	got, err := u.Result()
	if err != nil || !got.Reported || got.Model != "claude-x" {
		t.Fatalf("%+v %v", got, err)
	}
	if got.Usage.PromptTokens != 240 || got.Usage.CompletionTokens != 9 ||
		got.Usage.CachedPromptTokens() != 200 || got.Usage.CacheWrite1hTokens() != 30 {
		t.Errorf("usage %+v", got.Usage)
	}
}

func TestNativeStreamUsage_NoUsageIsNotReported(t *testing.T) {
	var u NativeStreamUsage
	u.Observe([]byte(`{"type":"ping"}`))
	if got, _ := u.Result(); got.Reported {
		t.Error("a stream with no usage reported a measurement")
	}
}

func TestParseNativeResponse_NegativeRefused(t *testing.T) {
	if _, err := ParseNativeResponse([]byte(`{"model":"m","usage":{"input_tokens":-5,"output_tokens":1}}`)); err == nil {
		t.Error("negative usage accepted")
	}
}

// A providers.yaml header cannot replace the credential or the betas.
func TestAnthropicNativeRequest_ConfiguredHeadersCannotOverride(t *testing.T) {
	a := NewAnthropicAdapter(config.ProviderConfig{BaseURL: "https://api.invalid/v1", APIKey: "real",
		Headers: map[string]string{"x-api-key": "spoof", "anthropic-beta": "spoof-beta"}}, nil)
	req, err := a.NativeRequest(context.Background(), NativeMessages, []byte(`{}`), []string{"b1", "b2"})
	if err != nil {
		t.Fatal(err)
	}
	if req.URL.String() != "https://api.invalid/v1/messages" || req.Header.Get("x-api-key") != "real" ||
		req.Header.Get("anthropic-beta") != "b1,b2" {
		t.Errorf("url %s key %q beta %q", req.URL, req.Header.Get("x-api-key"), req.Header.Get("anthropic-beta"))
	}
}

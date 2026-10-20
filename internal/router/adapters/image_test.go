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
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
)

const testPNGB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="

const imagePartsJSON = `[{"type":"text","text":"what is this"},` +
	`{"type":"image_url","image_url":{"url":"data:image/png;base64,` + testPNGB64 + `","detail":"low"}}]`

func imageRequest(t *testing.T, role string) *types.AegisRequest {
	t.Helper()
	msgs := `{"role":"user","content":` + imagePartsJSON + `}`
	if role != types.RoleUser {
		msgs = `{"role":"` + role + `","content":` + imagePartsJSON + `},{"role":"user","content":"hi"}`
	}
	req, err := types.DecodeChatCompletion([]byte(`{"model":"m","messages":[` + msgs + `]}`))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return req
}

// The OpenAI-compatible path (OpenAI, vLLM, llama.cpp) carries the part
// through byte-for-byte: what the client sent is what the provider receives.
func TestOpenAI_ImagePartPassesThroughUnchanged(t *testing.T) {
	a := NewOpenAIAdapter(config.ProviderConfig{BaseURL: "http://openai.invalid/v1"}, nil)
	httpReq, err := a.TransformRequest(context.Background(), imageRequest(t, types.RoleUser))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(httpReq.Body)
	if !strings.Contains(string(raw), `"content":`+imagePartsJSON) {
		t.Errorf("image part did not pass through unchanged:\n%s", raw)
	}
}

func TestAnthropic_ImagePartBecomesBase64ImageBlock(t *testing.T) {
	body := transform(t, imageRequest(t, types.RoleUser))
	msgs := body["messages"].([]any)
	blocks := msgs[0].(map[string]any)["content"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want text then image: %v", len(blocks), blocks)
	}
	if b := blocks[0].(map[string]any); b["type"] != "text" || b["text"] != "what is this" {
		t.Errorf("block 0 = %v", b)
	}
	img := blocks[1].(map[string]any)
	src, _ := img["source"].(map[string]any)
	if img["type"] != "image" || src["type"] != "base64" || src["media_type"] != "image/png" || src["data"] != testPNGB64 {
		t.Errorf("image block = %v", img)
	}
	// Anthropic has no detail field; it must not leak through as an unknown key.
	if out, _ := json.Marshal(img); strings.Contains(string(out), "detail") {
		t.Errorf("detail leaked into the Anthropic block: %s", out)
	}
}

// Text-only user turns keep the string form, so this change does not alter
// any request that carries no image.
func TestAnthropic_TextOnlyUserTurnUnchanged(t *testing.T) {
	body := transform(t, &types.AegisRequest{Model: "m", Messages: []types.Message{userMsg("hi")}})
	if c := body["messages"].([]any)[0].(map[string]any)["content"]; c != "hi" {
		t.Errorf("content = %v, want the string form", c)
	}
}

// An image anywhere Anthropic only takes a string is refused, never flattened
// away.
func TestAnthropic_ImageOffUserTurnRefused(t *testing.T) {
	a := NewAnthropicAdapter(config.ProviderConfig{BaseURL: "http://anthropic.invalid/v1"}, nil)
	for _, role := range []string{types.RoleSystem, types.RoleAssistant} {
		_, err := a.TransformRequest(context.Background(), imageRequest(t, role))
		var unmappable *UnmappableError
		if !errors.As(err, &unmappable) {
			t.Errorf("role %s: got %v, want UnmappableError", role, err)
		}
	}
}

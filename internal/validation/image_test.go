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

package validation

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
)

func imageReq(t *testing.T, messages string) *types.AegisRequest {
	t.Helper()
	req, err := types.DecodeChatCompletion([]byte(`{"model":"aegis-fast","messages":[` + messages + `]}`))
	if err != nil {
		t.Fatalf("fixture does not decode: %v", err)
	}
	return req
}

func pngPart(size int) string {
	raw := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, size)...)
	return `{"type":"image_url","image_url":{"url":"data:image/png;base64,` + base64.StdEncoding.EncodeToString(raw) + `"}}`
}

func TestValidate_ImageOnUserMessageAccepted(t *testing.T) {
	v := NewValidator(DefaultLimits(), nil)
	req := imageReq(t, `{"role":"user","content":[{"type":"text","text":"what is this"},`+pngPart(16)+`]}`)
	if err := v.Validate(req); err != nil {
		t.Fatalf("refused: %v", err)
	}
}

// An image anywhere but a user turn is refused, because the adapters can only
// express those positions as text and would drop it.
func TestValidate_ImageOffUserMessageRefused(t *testing.T) {
	v := NewValidator(DefaultLimits(), nil)
	for _, role := range []string{"system", "assistant"} {
		req := imageReq(t, `{"role":"`+role+`","content":[`+pngPart(16)+`]},{"role":"user","content":"hi"}`)
		err := v.Validate(req)
		if err == nil || !strings.Contains(err.Error(), "only on user messages") {
			t.Errorf("role %s: got %v", role, err)
		}
	}
}

func TestValidate_ImageLimits(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxImageBytes = 100
	limits.MaxImageParts = 2
	v := NewValidator(limits, nil)

	big := imageReq(t, `{"role":"user","content":[`+pngPart(200)+`]}`)
	if err := v.Validate(big); err == nil || !strings.Contains(err.Error(), "image too large") {
		t.Errorf("oversized image: got %v", err)
	}

	many := imageReq(t, `{"role":"user","content":[`+pngPart(1)+`,`+pngPart(1)+`]},`+
		`{"role":"assistant","content":"ok"},{"role":"user","content":[`+pngPart(1)+`]}`)
	if err := v.Validate(many); err == nil || !strings.Contains(err.Error(), "too many image parts") {
		t.Errorf("image count summed across messages: got %v", err)
	}
}

// Base64 is not text. It must not count toward the character limits, or a
// single photo would trip MaxMessageLength.
func TestValidate_ImageBytesNotCountedAsText(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxMessageLength = 50
	v := NewValidator(limits, nil)
	req := imageReq(t, `{"role":"user","content":[{"type":"text","text":"short"},`+pngPart(4096)+`]}`)
	if err := v.Validate(req); err != nil {
		t.Errorf("refused: %v", err)
	}
}

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

package policy

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/filter"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
)

const policyPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="

func imagePolicyReq(t *testing.T, classification types.Classification) *types.AegisRequest {
	t.Helper()
	req, err := types.DecodeChatCompletion([]byte(`{"model":"aegis-local","messages":[{"role":"user","content":[` +
		`{"type":"text","text":"describe"},` +
		`{"type":"image_url","image_url":{"url":"data:image/png;base64,` + policyPNG + `"}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Classification = classification
	return req
}

// A rule can be written against the unscanned-part metadata. The fixture is
// the kind of rule an operator would write: images only for keys below
// RESTRICTED, and nothing over 1 MB.
func TestPolicyInput_ExposesUnscannedParts(t *testing.T) {
	const module = `package aegis.policy

import rego.v1

default allow := false

deny contains "images_not_permitted_for_restricted_keys" if {
	"image" in input.request.unscanned_parts.kinds
	input.request.classification == "RESTRICTED"
}

deny contains "unscanned_parts_too_large" if {
	input.request.unscanned_parts.bytes > 1048576
}

allow if count(deny) == 0

reason := concat("; ", deny)
`
	e := NewEvaluator(func() config.PolicyFilterConfig {
		return config.PolicyFilterConfig{Enabled: true, EvaluationTimeout: time.Second}
	})
	if err := e.LoadFromModules(map[string]string{"images.rego": module}); err != nil {
		t.Fatalf("compiling the fixture policy: %v", err)
	}

	if r := e.ScanRequest(context.Background(), imagePolicyReq(t, types.ClassInternal)); r.Action == filter.ActionBlock {
		t.Errorf("INTERNAL image request blocked: %s", r.Message)
	}
	r := e.ScanRequest(context.Background(), imagePolicyReq(t, types.ClassRestricted))
	if r.Action != filter.ActionBlock || !strings.Contains(r.Message, "images_not_permitted_for_restricted_keys") {
		t.Errorf("RESTRICTED image request: %s %q; the unscanned-part metadata is not reaching the policy input",
			r.Action, r.Message)
	}

	// A text request evaluates cleanly against the same rules: kinds is an
	// empty array, not undefined.
	text := &types.AegisRequest{Classification: types.ClassRestricted,
		Messages: []types.Message{{Role: types.RoleUser, Content: types.TextContent("hi")}}}
	if r := e.ScanRequest(context.Background(), text); r.Action == filter.ActionBlock {
		t.Errorf("text request blocked: %s", r.Message)
	}
}

// The policy input carries the image's metadata and never its bytes. A deny
// message is sealed into the audit trail, so anything the input holds can be
// copied there by a careless rule.
func TestPolicyInput_CarriesNoImageData(t *testing.T) {
	input := buildPolicyInput(imagePolicyReq(t, types.ClassInternal), time.Now())
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), policyPNG[:24]) || strings.Contains(string(encoded), "data:image") {
		t.Errorf("the policy input carries image data: %s", encoded)
	}
	got := input.Request.UnscannedParts
	if got.Count != 1 || got.Bytes == 0 || len(got.MediaTypes) != 1 || got.MediaTypes[0] != "image/png" {
		t.Errorf("unscanned_parts = %+v", got)
	}
}

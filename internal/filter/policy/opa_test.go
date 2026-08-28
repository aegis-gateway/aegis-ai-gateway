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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/filter"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
)

type fakeMetrics struct {
	reloadSuccess int
	reloadError   int
}

func (f *fakeMetrics) RecordPolicyReload(success bool) {
	if success {
		f.reloadSuccess++
	} else {
		f.reloadError++
	}
}

func testCfg() func() config.PolicyFilterConfig {
	return func() config.PolicyFilterConfig {
		return config.PolicyFilterConfig{
			Enabled:           true,
			EvaluationTimeout: 100 * time.Millisecond,
		}
	}
}

const defaultPolicy = `
package aegis.policy

import rego.v1

default allow := true
default reason := ""

deny contains msg if {
	input.request.classification == "RESTRICTED"
	input.request.provider_type == "external"
	msg := "RESTRICTED data cannot be sent to external providers"
}

allow := false if {
	count(deny) > 0
}

reason := concat("; ", deny) if {
	count(deny) > 0
}
`

func loadTestEvaluator(t *testing.T, policy string) *Evaluator {
	t.Helper()
	e := NewEvaluator(testCfg())
	if err := e.LoadFromModules(map[string]string{"test.rego": policy}); err != nil {
		t.Fatalf("failed to load policy: %v", err)
	}
	return e
}

func TestEvaluator_AllowByDefault(t *testing.T) {
	e := loadTestEvaluator(t, defaultPolicy)

	allowed, reason, err := e.Evaluate(context.Background(), PolicyInput{
		User:    PolicyUser{ID: "user-1", Org: "org-1", Team: "team-1"},
		Request: PolicyReq{Model: "gpt-4o", Classification: "INTERNAL", ProviderType: "external"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Errorf("expected allowed, got denied: %s", reason)
	}
}

func TestEvaluator_BlockRestrictedExternal(t *testing.T) {
	e := loadTestEvaluator(t, defaultPolicy)

	allowed, reason, err := e.Evaluate(context.Background(), PolicyInput{
		User:    PolicyUser{ID: "user-1", Org: "org-1", Team: "team-1"},
		Request: PolicyReq{Model: "gpt-4o", Classification: "RESTRICTED", ProviderType: "external"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allowed {
		t.Error("expected denied for RESTRICTED+external")
	}
	if reason == "" {
		t.Error("expected non-empty reason")
	}
}

func TestEvaluator_AllowRestrictedInternal(t *testing.T) {
	e := loadTestEvaluator(t, defaultPolicy)

	allowed, _, err := e.Evaluate(context.Background(), PolicyInput{
		User:    PolicyUser{ID: "user-1", Org: "org-1", Team: "team-1"},
		Request: PolicyReq{Model: "llama-70b", Classification: "RESTRICTED", ProviderType: "internal"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Error("expected allowed for RESTRICTED+internal")
	}
}

func TestEvaluator_NoPoliciesLoaded_FailClosed(t *testing.T) {
	e := NewEvaluator(testCfg())
	// Don't load any policies

	allowed, _, _ := e.Evaluate(context.Background(), PolicyInput{})
	if allowed {
		t.Error("expected denied when no policies loaded (fail closed)")
	}
}

func TestEvaluator_ScanRequest_Block(t *testing.T) {
	e := loadTestEvaluator(t, defaultPolicy)

	req := &types.AegisRequest{
		Model:          "gpt-4o",
		Classification: "RESTRICTED",
		UserID:         "user-1",
		OrganizationID: "org-1",
		TeamID:         "team-1",
	}

	// We need to set provider_type in the input, but ScanRequest doesn't have
	// provider info yet. This test verifies the filter interface works.
	// With no provider_type set, the default policy allows it.
	result := e.ScanRequest(context.Background(), req)
	if result.Action != filter.ActionPass {
		t.Errorf("expected pass (no provider_type in request), got %s", result.Action)
	}
}

func TestEvaluator_ScanRequest_Pass(t *testing.T) {
	e := loadTestEvaluator(t, defaultPolicy)

	req := &types.AegisRequest{
		Model:          "gpt-4o",
		Classification: "INTERNAL",
		UserID:         "user-1",
		OrganizationID: "org-1",
		TeamID:         "team-1",
	}

	result := e.ScanRequest(context.Background(), req)
	if result.Action != filter.ActionPass {
		t.Errorf("expected pass, got %s: %s", result.Action, result.Message)
	}
	if result.FilterName != "policy" {
		t.Errorf("expected filter name 'policy', got %s", result.FilterName)
	}
}

func TestLoad_SyntaxError_KeepsOldPolicy(t *testing.T) {
	// Start with a valid policy that allows everything.
	validPolicy := `
package aegis.policy

import rego.v1

default allow := true
default reason := ""
`
	fm := &fakeMetrics{}
	e := NewEvaluator(testCfg())
	e.SetMetrics(fm)

	// Load the valid policy first.
	if err := e.LoadFromModules(map[string]string{"valid.rego": validPolicy}); err != nil {
		t.Fatalf("failed to load valid policy: %v", err)
	}

	// Confirm it works.
	allowed, _, err := e.Evaluate(context.Background(), PolicyInput{
		Request: PolicyReq{Model: "gpt-4o", Classification: "PUBLIC"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Fatal("expected allowed with valid policy")
	}

	// Write a broken .rego file to a temp dir.
	dir := t.TempDir()
	brokenRego := `package aegis.policy @@@ THIS IS INVALID SYNTAX`
	if err := os.WriteFile(filepath.Join(dir, "broken.rego"), []byte(brokenRego), 0644); err != nil {
		t.Fatalf("failed to write broken rego: %v", err)
	}

	// Point Load() at the broken dir — should fail.
	brokenCfg := func() config.PolicyFilterConfig {
		return config.PolicyFilterConfig{
			Enabled:           true,
			BundlePath:        dir,
			EvaluationTimeout: 100 * time.Millisecond,
		}
	}
	eBroken := &Evaluator{
		prepared: e.prepared, // start with the known-good query
		cfg:      brokenCfg,
		metrics:  fm,
	}

	err = eBroken.Load()
	if err == nil {
		t.Fatal("expected error from broken rego, got nil")
	}

	// The old query must still be intact.
	allowed, _, err = eBroken.Evaluate(context.Background(), PolicyInput{
		Request: PolicyReq{Model: "gpt-4o", Classification: "PUBLIC"},
	})
	if err != nil {
		t.Fatalf("evaluation should still work after bad reload: %v", err)
	}
	if !allowed {
		t.Error("expected allowed — old policy should still be active after failed reload")
	}

	// ScanRequest should also still work.
	result := eBroken.ScanRequest(context.Background(), &types.AegisRequest{
		Model:          "gpt-4o",
		Classification: "PUBLIC",
		UserID:         "u1",
		OrganizationID: "org1",
		TeamID:         "team1",
	})
	if result.Action != filter.ActionPass {
		t.Errorf("expected pass from ScanRequest, got %s: %s", result.Action, result.Message)
	}

	// Metrics: at least one error recorded.
	if fm.reloadError == 0 {
		t.Error("expected reload error metric to be incremented")
	}
}

func TestLoad_EmptyDir_ClearsPolicies(t *testing.T) {
	// Start with a valid policy loaded.
	e := loadTestEvaluator(t, defaultPolicy)

	allowed, _, err := e.Evaluate(context.Background(), PolicyInput{
		Request: PolicyReq{Model: "gpt-4o", Classification: "PUBLIC"},
	})
	if err != nil || !allowed {
		t.Fatal("expected allowed before clearing")
	}

	// Point Load() at an empty directory — should clear the prepared query.
	emptyDir := t.TempDir()
	e.cfg = func() config.PolicyFilterConfig {
		return config.PolicyFilterConfig{
			Enabled:           true,
			BundlePath:        emptyDir,
			EvaluationTimeout: 100 * time.Millisecond,
		}
	}
	if err := e.Load(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// With no policies loaded, evaluator should fail-closed.
	allowed, reason, _ := e.Evaluate(context.Background(), PolicyInput{
		Request: PolicyReq{Model: "gpt-4o", Classification: "PUBLIC"},
	})
	if allowed {
		t.Error("expected denied after all policies removed (fail-closed)")
	}
	if reason != "no policies loaded" {
		t.Errorf("expected 'no policies loaded', got: %s", reason)
	}
}

func TestMissingDefaults_DeniesCleanRequest(t *testing.T) {
	// When no module provides `default allow := true`, a clean request
	// (no deny fires) leaves `allow` undefined. The evaluator treats
	// undefined as "no policy result" and denies — fail-closed.
	// This is the bug that base.rego prevents.
	moduleNoDef := `
package aegis.policy

import rego.v1

deny contains msg if {
	input.request.model == "blocked"
	msg := "blocked"
}

allow := false if { count(deny) > 0 }
reason := concat("; ", deny) if { count(deny) > 0 }
`
	e := NewEvaluator(testCfg())
	if err := e.LoadFromModules(map[string]string{"nodef.rego": moduleNoDef}); err != nil {
		t.Fatalf("unexpected compile error: %v", err)
	}

	// A clean request — no deny fires, but allow is undefined (no default).
	allowed, reason, err := e.Evaluate(context.Background(), PolicyInput{
		Request: PolicyReq{Model: "gpt-4o", Classification: "INTERNAL"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allowed {
		t.Error("expected denied when defaults are missing and no deny fires")
	}
	if reason != "no policy result" {
		t.Errorf("expected 'no policy result', got: %s", reason)
	}
}

func TestBaseWithDenyModules_AllowsCleanRequest(t *testing.T) {
	// With a base module providing defaults, clean requests pass
	// even when other modules only have conditional deny rules.
	base := `
package aegis.policy

import rego.v1

default allow := true
default reason := ""

allow := false if { count(deny) > 0 }
reason := concat("; ", deny) if { count(deny) > 0 }
`
	denyModule := `
package aegis.policy

import rego.v1

deny contains msg if {
	input.request.model == "blocked"
	msg := "blocked model"
}
`
	e := NewEvaluator(testCfg())
	if err := e.LoadFromModules(map[string]string{
		"base.rego": base,
		"deny.rego": denyModule,
	}); err != nil {
		t.Fatalf("unexpected compile error: %v", err)
	}

	// Clean request — should be allowed.
	allowed, _, err := e.Evaluate(context.Background(), PolicyInput{
		Request: PolicyReq{Model: "gpt-4o", Classification: "INTERNAL"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Error("expected allowed when base provides defaults and no deny fires")
	}

	// Blocked request — should be denied.
	allowed, reason, err := e.Evaluate(context.Background(), PolicyInput{
		Request: PolicyReq{Model: "blocked", Classification: "INTERNAL"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allowed {
		t.Error("expected denied for blocked model")
	}
	if !strings.Contains(reason, "blocked model") {
		t.Errorf("expected reason to contain 'blocked model', got: %s", reason)
	}
}

func TestDemoPolicies_CompileTogether(t *testing.T) {
	// Verify that all demo .rego files compile together without
	// conflicting complete-rule errors.
	demoDir := filepath.Join("..", "..", "..", "demos", "05-custom-policies", "policies")
	modules, err := LoadRegoFiles(demoDir)
	if err != nil {
		t.Fatalf("failed to load demo policies: %v", err)
	}
	if len(modules) == 0 {
		t.Fatal("no demo policy files found")
	}

	e := NewEvaluator(testCfg())
	if err := e.LoadFromModules(modules); err != nil {
		t.Fatalf("demo policies failed to compile together: %v", err)
	}

	// A clean request should be allowed.
	allowed, _, err := e.Evaluate(context.Background(), PolicyInput{
		User:     PolicyUser{ID: "u1", Org: "org1", Team: "engineering"},
		Request:  PolicyReq{Model: "gpt-4o", Classification: "INTERNAL"},
		Messages: []PolicyMessage{{Role: "user", Content: "Hello world"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Error("expected clean request to be allowed")
	}

	// A restricted term should be denied.
	allowed, reason, err := e.Evaluate(context.Background(), PolicyInput{
		User:     PolicyUser{ID: "u1", Org: "org1", Team: "engineering"},
		Request:  PolicyReq{Model: "gpt-4o", Classification: "INTERNAL"},
		Messages: []PolicyMessage{{Role: "user", Content: "What is the current status of Project Ironwood?"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allowed {
		t.Error("expected restricted term to be denied")
	}
	if reason == "" {
		t.Error("expected non-empty reason for restricted-term denial")
	}
}

func TestEvaluator_Disabled(t *testing.T) {
	e := NewEvaluator(func() config.PolicyFilterConfig {
		return config.PolicyFilterConfig{Enabled: false}
	})
	if e.Enabled() {
		t.Error("expected evaluator to be disabled")
	}
}

func TestEvaluator_EnabledThenDisabled(t *testing.T) {
	// Simulates: policy enabled at startup with policies loaded,
	// then config reloads with policy disabled.
	// The old prepared query stays cached but Enabled() returns false,
	// so the handler will never call ScanRequest.
	enabled := true
	e := NewEvaluator(func() config.PolicyFilterConfig {
		return config.PolicyFilterConfig{
			Enabled:           enabled,
			BundlePath:        "",
			EvaluationTimeout: 100 * time.Millisecond,
		}
	})
	if err := e.LoadFromModules(map[string]string{"test.rego": defaultPolicy}); err != nil {
		t.Fatalf("failed to load: %v", err)
	}

	// While enabled, evaluation works.
	if !e.Enabled() {
		t.Fatal("expected enabled")
	}
	allowed, _, err := e.Evaluate(context.Background(), PolicyInput{
		Request: PolicyReq{Model: "gpt-4o", Classification: "PUBLIC"},
	})
	if err != nil || !allowed {
		t.Fatal("expected allowed while enabled")
	}

	// Config reloads — policy now disabled.
	enabled = false
	if e.Enabled() {
		t.Error("expected disabled after config change")
	}

	// Old prepared query is still there, but the handler won't call us
	// because Enabled() is false. Verify that Evaluate still works if
	// called directly (defensive — not a handler code path).
	allowed, _, err = e.Evaluate(context.Background(), PolicyInput{
		Request: PolicyReq{Model: "gpt-4o", Classification: "PUBLIC"},
	})
	if err != nil || !allowed {
		t.Error("cached query should still work if called directly")
	}
}

func TestEvaluator_DisabledThenEnabled(t *testing.T) {
	// Simulates: policy disabled at startup (Load never called),
	// then config reloads with policy enabled and Load() runs.
	enabled := false
	dir := t.TempDir()
	// Write a valid policy file to the temp dir.
	regoContent := []byte(defaultPolicy)
	if err := os.WriteFile(filepath.Join(dir, "test.rego"), regoContent, 0644); err != nil {
		t.Fatalf("failed to write rego: %v", err)
	}

	e := NewEvaluator(func() config.PolicyFilterConfig {
		return config.PolicyFilterConfig{
			Enabled:           enabled,
			BundlePath:        dir,
			EvaluationTimeout: 100 * time.Millisecond,
		}
	})

	// At startup: disabled, no Load() called, prepared is nil.
	if e.Enabled() {
		t.Fatal("expected disabled at startup")
	}
	// Evaluate without loading — fail-closed.
	allowed, reason, _ := e.Evaluate(context.Background(), PolicyInput{
		Request: PolicyReq{Model: "gpt-4o", Classification: "PUBLIC"},
	})
	if allowed {
		t.Error("expected denied with no policies loaded")
	}
	if reason != "no policies loaded" {
		t.Errorf("expected 'no policies loaded', got: %s", reason)
	}

	// Config reloads — policy now enabled. The reload callback calls Load().
	enabled = true
	if !e.Enabled() {
		t.Fatal("expected enabled after config change")
	}
	if err := e.Load(); err != nil {
		t.Fatalf("Load() after enabling failed: %v", err)
	}

	// Now evaluation works.
	allowed, _, err := e.Evaluate(context.Background(), PolicyInput{
		Request: PolicyReq{Model: "gpt-4o", Classification: "INTERNAL", ProviderType: "openai"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Error("expected allowed after loading policies")
	}
}

func TestEvaluator_DisabledStaysDisabled(t *testing.T) {
	// Policy disabled at startup, stays disabled on reload.
	// Load() is never called, prepared stays nil, Enabled() stays false.
	e := NewEvaluator(func() config.PolicyFilterConfig {
		return config.PolicyFilterConfig{Enabled: false}
	})

	if e.Enabled() {
		t.Error("expected disabled")
	}

	// No Load() called — prepared is nil, fail-closed if called directly.
	allowed, _, _ := e.Evaluate(context.Background(), PolicyInput{})
	if allowed {
		t.Error("expected denied with nil prepared (fail-closed)")
	}

	// Simulate reload — still disabled, OnReload skips Load().
	// Nothing changes.
	if e.Enabled() {
		t.Error("still expected disabled")
	}
}

func TestEvaluator_CustomDenyAllPolicy(t *testing.T) {
	denyAll := `
package aegis.policy

import rego.v1

allow := false
reason := "all requests denied"
`
	e := loadTestEvaluator(t, denyAll)

	allowed, reason, err := e.Evaluate(context.Background(), PolicyInput{
		Request: PolicyReq{Model: "gpt-4o", Classification: "PUBLIC"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allowed {
		t.Error("expected denied by deny-all policy")
	}
	if reason != "all requests denied" {
		t.Errorf("expected 'all requests denied', got %s", reason)
	}
}

// TestShippedDefaultPolicy_CanActuallyDeny loads configs/policies/default.rego,
// the bundle operators actually get, and asserts it denies something.
//
// The previous default gated on input.request.provider_type == "external".
// provider_type is set from adapter.Name(), which returns "openai" or
// "anthropic" and never "external", so the only deny rule in the shipped bundle
// could not fire. The bundle compiled, the tests passed, the landing page
// advertised policy enforcement, and no policy denial was reachable.
//
// Every other test in this file builds its own inline fixture, which is exactly
// how that survived. This one reads the real file.
//
// What it does NOT prove, stated plainly because the gap is the same shape as
// the bug: it calls Evaluate directly, so it shows the bundle can deny, not that
// a deny is reachable over HTTP. Policy runs after routing, and on the shipped
// models config every RESTRICTED request fails ResolveRoute first and returns
// 503, so this rule does not fire on the request path until an operator adds a
// route whose classification_ceiling admits RESTRICTED.
//
// Closing that gap needs a request-path test with such a route configured, which
// needs a running stack. Until then, a green result here means the bundle is
// sound, not that the deny is live.
func TestShippedDefaultPolicy_CanActuallyDeny(t *testing.T) {
	modules, err := LoadRegoFiles(filepath.Join("..", "..", "..", "configs", "policies"))
	if err != nil {
		t.Fatalf("loading the shipped policy dir: %v", err)
	}
	if len(modules) == 0 {
		t.Fatal("no .rego files in configs/policies; the shipped bundle is empty")
	}

	e := NewEvaluator(testCfg())
	if err := e.LoadFromModules(modules); err != nil {
		t.Fatalf("the shipped bundle does not compile: %v", err)
	}

	ctx := context.Background()

	// RESTRICTED on an uncleared alias must be denied, with a reason that names
	// the alias rather than an empty string.
	allowed, reason, err := e.Evaluate(ctx, PolicyInput{
		User:    PolicyUser{ID: "u1", Org: "org1", Team: "eng"},
		Request: PolicyReq{Model: "aegis-balanced", Classification: "RESTRICTED"},
	})
	if err != nil {
		t.Fatalf("evaluating a RESTRICTED request: %v", err)
	}
	if allowed {
		t.Error("the shipped default bundle allowed RESTRICTED data through an uncleared " +
			"alias; it can deny nothing, which is the regression this test exists to catch")
	}
	if !strings.Contains(reason, "aegis-balanced") {
		t.Errorf("deny reason %q does not name the alias; an operator cannot act on it", reason)
	}

	// A routine request must still pass, or the rule is denying everything.
	allowed, _, err = e.Evaluate(ctx, PolicyInput{
		User:    PolicyUser{ID: "u1", Org: "org1", Team: "eng"},
		Request: PolicyReq{Model: "aegis-balanced", Classification: "INTERNAL"},
	})
	if err != nil {
		t.Fatalf("evaluating an INTERNAL request: %v", err)
	}
	if !allowed {
		t.Error("the shipped default bundle denied an ordinary INTERNAL request")
	}
}

// TestPolicyInput_ExposesToolMetadata asserts a Rego rule can be written
// against the tools a request offers and the tools its history has called.
//
// This is Part 7 of the tool-calling work: the seam, not the policy. No
// tool-level rule ships in configs/policies. The test proves the metadata is
// reachable from Rego, because "we exposed it" is otherwise a claim about a
// struct tag that nothing checks.
//
// The rule below is a fixture. It is deliberately the kind of rule an operator
// would actually write, so that if the input shape changes the failure looks
// like a broken policy rather than a broken assertion.
func TestPolicyInput_ExposesToolMetadata(t *testing.T) {
	const module = `package aegis.policy

import rego.v1

default allow := false

# An operator gating on which capability is put in front of the model.
deny contains msg if {
	some tool in input.request.tools_offered
	tool == "run_shell"
	msg := "shell tool not permitted for this key"
}

# An operator gating on which capability the conversation has already used.
deny contains msg if {
	some tool in input.request.tools_called
	tool == "transfer_funds"
	msg := "transfer_funds already called in this conversation"
}

allow if count(deny) == 0

reason := concat("; ", deny)
`

	cases := []struct {
		name       string
		req        types.AegisRequest
		wantAllow  bool
		wantReason string
	}{
		{
			name: "a harmless tool is allowed",
			req: types.AegisRequest{
				Tools: []types.Tool{{Type: types.ToolTypeFunction, Function: types.FunctionDef{Name: "read_file"}}},
			},
			wantAllow: true,
		},
		{
			name: "a denied tool offered is visible to the rule",
			req: types.AegisRequest{
				Tools: []types.Tool{
					{Type: types.ToolTypeFunction, Function: types.FunctionDef{Name: "read_file"}},
					{Type: types.ToolTypeFunction, Function: types.FunctionDef{Name: "run_shell"}},
				},
			},
			wantAllow:  false,
			wantReason: "shell tool not permitted for this key",
		},
		{
			name: "a tool already called is visible to the rule",
			req: types.AegisRequest{
				Messages: []types.Message{{Role: types.RoleAssistant, ToolCalls: []types.ToolCall{{
					ID:       "c1",
					Function: types.FunctionCallSpec{Name: "transfer_funds", Arguments: `{"amount":1000000}`},
				}}}},
			},
			wantAllow:  false,
			wantReason: "transfer_funds already called in this conversation",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := NewEvaluator(func() config.PolicyFilterConfig {
				return config.PolicyFilterConfig{Enabled: true, EvaluationTimeout: time.Second}
			})
			if err := e.LoadFromModules(map[string]string{"tools.rego": module}); err != nil {
				t.Fatalf("compiling the fixture policy: %v", err)
			}

			result := e.ScanRequest(context.Background(), &tc.req)

			if tc.wantAllow {
				if result.Action == filter.ActionBlock {
					t.Fatalf("expected pass, got block: %s", result.Message)
				}
				return
			}
			if result.Action != filter.ActionBlock {
				t.Fatalf("expected block, got %s — the tool metadata is not reaching the "+
					"policy input, so no policy can be written against it", result.Action)
			}
			if !strings.Contains(result.Message, tc.wantReason) {
				t.Errorf("deny reason = %q, want it to contain %q", result.Message, tc.wantReason)
			}
		})
	}
}

// TestPolicyInput_CarriesNoToolArguments guards the no-payload boundary on the
// metadata Part 7 exposes.
//
// The policy input is serialised and handed to OPA. If a tool call's arguments
// rode along, request payload would enter the policy evaluation path, and from
// there any policy could copy it into a deny reason that is written to the
// audit trail.
func TestPolicyInput_CarriesNoToolArguments(t *testing.T) {
	const argumentPayload = "PAYLOAD_IN_TOOL_ARGUMENTS_c47e19"

	req := &types.AegisRequest{
		Tools: []types.Tool{{Type: types.ToolTypeFunction, Function: types.FunctionDef{
			Name:       "read_file",
			Parameters: []byte(`{"secret":"` + argumentPayload + `"}`),
		}}},
		Messages: []types.Message{{Role: types.RoleAssistant, ToolCalls: []types.ToolCall{{
			ID:       "c1",
			Function: types.FunctionCallSpec{Name: "read_file", Arguments: `{"p":"` + argumentPayload + `"}`},
		}}}},
	}

	// Build the input exactly as ScanRequest does, then serialise it and search
	// the whole document rather than named fields, so a field added later is
	// covered without this test being updated.
	input := PolicyInput{
		Request: PolicyReq{
			ToolsOffered: req.ToolNames(),
			ToolsCalled:  req.CalledToolNames(),
			ToolChoice:   req.ToolChoice.String(),
		},
	}
	for _, m := range req.Messages {
		pm := PolicyMessage{Role: m.Role, Content: m.Content.Flatten()}
		for _, tc := range m.ToolCalls {
			pm.ToolCalls = append(pm.ToolCalls, tc.Function.Name)
		}
		input.Messages = append(input.Messages, pm)
	}

	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshalling policy input: %v", err)
	}
	if strings.Contains(string(encoded), argumentPayload) {
		t.Errorf("the policy input carries tool argument payload: %s", encoded)
	}
	if !strings.Contains(string(encoded), "read_file") {
		t.Errorf("the policy input carries no tool name, so the metadata is not actually exposed: %s", encoded)
	}
}

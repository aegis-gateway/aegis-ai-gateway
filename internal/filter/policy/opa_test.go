package policy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/af-corp/aegis-gateway/internal/config"
	"github.com/af-corp/aegis-gateway/internal/filter"
	"github.com/af-corp/aegis-gateway/internal/types"
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
	demoDir := filepath.Join("..", "..", "..", "demos", "15-custom-policies", "policies")
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

	// A competitor mention should be denied.
	allowed, reason, err := e.Evaluate(context.Background(), PolicyInput{
		User:     PolicyUser{ID: "u1", Org: "org1", Team: "engineering"},
		Request:  PolicyReq{Model: "gpt-4o", Classification: "INTERNAL"},
		Messages: []PolicyMessage{{Role: "user", Content: "How does Portkey compare?"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allowed {
		t.Error("expected competitor mention to be denied")
	}
	if reason == "" {
		t.Error("expected non-empty reason for competitor denial")
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

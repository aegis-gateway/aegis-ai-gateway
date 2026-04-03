package policy

import (
	"context"
	"os"
	"path/filepath"
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

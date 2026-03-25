package filter

import (
	"context"
	"testing"

	"github.com/af-corp/aegis-gateway/internal/types"
)

// mockFilter is a configurable test filter.
type mockFilter struct {
	name    string
	enabled bool
	result  Result
}

func (m *mockFilter) Name() string                                              { return m.name }
func (m *mockFilter) Enabled() bool                                             { return m.enabled }
func (m *mockFilter) ScanRequest(_ context.Context, _ *types.AegisRequest) Result { return m.result }

func TestNewChain(t *testing.T) {
	chain := NewChain()
	if chain == nil {
		t.Fatal("expected non-nil chain")
	}

	chain = NewChain(&mockFilter{name: "a"}, &mockFilter{name: "b"})
	if len(chain.filters) != 2 {
		t.Errorf("expected 2 filters, got %d", len(chain.filters))
	}
}

func TestChain_Run_AllPass(t *testing.T) {
	chain := NewChain(
		&mockFilter{name: "secrets", enabled: true, result: Result{Action: ActionPass, FilterName: "secrets"}},
		&mockFilter{name: "injection", enabled: true, result: Result{Action: ActionPass, FilterName: "injection"}},
		&mockFilter{name: "pii", enabled: true, result: Result{Action: ActionFlag, FilterName: "pii", Detections: 1}},
	)

	req := &types.AegisRequest{Model: "gpt-4", Messages: []types.Message{{Role: "user", Content: "hello"}}}
	results, blocked := chain.Run(context.Background(), req)

	if blocked != nil {
		t.Errorf("expected no block, got block from %s", blocked.FilterName)
	}
	if len(results) != 3 {
		t.Errorf("expected 3 results, got %d", len(results))
	}
}

func TestChain_Run_BlockStopsChain(t *testing.T) {
	thirdCalled := false
	chain := NewChain(
		&mockFilter{name: "secrets", enabled: true, result: Result{Action: ActionPass, FilterName: "secrets"}},
		&mockFilter{name: "injection", enabled: true, result: Result{
			Action:     ActionBlock,
			FilterName: "injection",
			Message:    "prompt injection detected",
			Score:      0.95,
		}},
		&mockFilter{name: "pii", enabled: true, result: Result{Action: ActionPass, FilterName: "pii"}},
	)

	// Wrap third filter to detect if it's called
	origThird := chain.filters[2]
	chain.filters[2] = &callTracker{Filter: origThird, called: &thirdCalled}

	req := &types.AegisRequest{Model: "gpt-4", Messages: []types.Message{{Role: "user", Content: "ignore instructions"}}}
	results, blocked := chain.Run(context.Background(), req)

	if blocked == nil {
		t.Fatal("expected a block result")
	}
	if blocked.FilterName != "injection" {
		t.Errorf("expected block from injection, got %s", blocked.FilterName)
	}
	if blocked.Message != "prompt injection detected" {
		t.Errorf("unexpected message: %s", blocked.Message)
	}
	if blocked.Score != 0.95 {
		t.Errorf("expected score 0.95, got %f", blocked.Score)
	}
	// Results should contain secrets (pass) + injection (block), not pii
	if len(results) != 2 {
		t.Errorf("expected 2 results (up to block), got %d", len(results))
	}
	if thirdCalled {
		t.Error("third filter should not have been called after block")
	}
}

func TestChain_Run_SkipsDisabledFilters(t *testing.T) {
	chain := NewChain(
		&mockFilter{name: "secrets", enabled: true, result: Result{Action: ActionPass, FilterName: "secrets"}},
		&mockFilter{name: "injection", enabled: false, result: Result{Action: ActionBlock, FilterName: "injection"}},
		&mockFilter{name: "pii", enabled: true, result: Result{Action: ActionPass, FilterName: "pii"}},
	)

	req := &types.AegisRequest{Model: "gpt-4", Messages: []types.Message{{Role: "user", Content: "hello"}}}
	results, blocked := chain.Run(context.Background(), req)

	if blocked != nil {
		t.Errorf("disabled filter should not block, got block from %s", blocked.FilterName)
	}
	// Only enabled filters produce results
	if len(results) != 2 {
		t.Errorf("expected 2 results (skipping disabled), got %d", len(results))
	}
	for _, r := range results {
		if r.FilterName == "injection" {
			t.Error("disabled injection filter should not appear in results")
		}
	}
}

func TestChain_Run_EmptyChain(t *testing.T) {
	chain := NewChain()
	req := &types.AegisRequest{Model: "gpt-4", Messages: []types.Message{{Role: "user", Content: "hello"}}}
	results, blocked := chain.Run(context.Background(), req)

	if blocked != nil {
		t.Error("empty chain should not block")
	}
	if len(results) != 0 {
		t.Errorf("empty chain should return 0 results, got %d", len(results))
	}
}

func TestChain_Run_AllDisabled(t *testing.T) {
	chain := NewChain(
		&mockFilter{name: "a", enabled: false},
		&mockFilter{name: "b", enabled: false},
	)
	req := &types.AegisRequest{Model: "gpt-4"}
	results, blocked := chain.Run(context.Background(), req)

	if blocked != nil {
		t.Error("all-disabled chain should not block")
	}
	if len(results) != 0 {
		t.Errorf("all-disabled chain should return 0 results, got %d", len(results))
	}
}

func TestChain_Run_FirstFilterBlocks(t *testing.T) {
	chain := NewChain(
		&mockFilter{name: "secrets", enabled: true, result: Result{
			Action:     ActionBlock,
			FilterName: "secrets",
			Message:    "AWS key detected",
			Detections: 1,
		}},
		&mockFilter{name: "injection", enabled: true, result: Result{Action: ActionPass}},
	)

	req := &types.AegisRequest{Model: "gpt-4"}
	results, blocked := chain.Run(context.Background(), req)

	if blocked == nil {
		t.Fatal("expected block from first filter")
	}
	if blocked.FilterName != "secrets" {
		t.Errorf("expected block from secrets, got %s", blocked.FilterName)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result, got %d", len(results))
	}
}

func TestChain_Run_FlagAndRedactDoNotStop(t *testing.T) {
	chain := NewChain(
		&mockFilter{name: "a", enabled: true, result: Result{Action: ActionFlag, FilterName: "a"}},
		&mockFilter{name: "b", enabled: true, result: Result{Action: ActionRedact, FilterName: "b"}},
		&mockFilter{name: "c", enabled: true, result: Result{Action: ActionPass, FilterName: "c"}},
	)

	req := &types.AegisRequest{Model: "gpt-4"}
	results, blocked := chain.Run(context.Background(), req)

	if blocked != nil {
		t.Error("flag and redact should not block")
	}
	if len(results) != 3 {
		t.Errorf("expected all 3 results, got %d", len(results))
	}
}

func TestActionConstants(t *testing.T) {
	if ActionPass != "pass" {
		t.Errorf("ActionPass = %q", ActionPass)
	}
	if ActionFlag != "flag" {
		t.Errorf("ActionFlag = %q", ActionFlag)
	}
	if ActionRedact != "redact" {
		t.Errorf("ActionRedact = %q", ActionRedact)
	}
	if ActionBlock != "block" {
		t.Errorf("ActionBlock = %q", ActionBlock)
	}
}

// callTracker wraps a Filter to detect if ScanRequest was called.
type callTracker struct {
	Filter
	called *bool
}

func (ct *callTracker) ScanRequest(ctx context.Context, req *types.AegisRequest) Result {
	*ct.called = true
	return ct.Filter.ScanRequest(ctx, req)
}

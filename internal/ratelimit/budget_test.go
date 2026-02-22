package ratelimit

import (
	"context"
	"testing"
)

func TestBudgetTracker_NilRedis_FailOpen(t *testing.T) {
	b := NewBudgetTracker(nil)
	result, err := b.CheckDailySpend(context.Background(), "team-1", 10000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Allowed {
		t.Error("expected allowed when Redis is nil")
	}
	if result.LimitCents != 10000 {
		t.Errorf("expected limit=10000, got %d", result.LimitCents)
	}
}

func TestBudgetTracker_NilRedis_RecordSpend(t *testing.T) {
	b := NewBudgetTracker(nil)
	// RecordSpend should be a no-op with nil Redis
	err := b.RecordSpend(context.Background(), "team-1", 500)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBudgetTracker_NilRedis_ZeroCost(t *testing.T) {
	b := NewBudgetTracker(nil)
	err := b.RecordSpend(context.Background(), "team-1", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

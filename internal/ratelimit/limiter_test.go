package ratelimit

import (
	"context"
	"testing"
	"time"
)

func TestLimiter_NilRedis_FailOpen(t *testing.T) {
	l := NewLimiter(nil)
	result, err := l.Check(context.Background(), "test:key", 60, time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Allowed {
		t.Error("expected allowed when Redis is nil")
	}
	if result.Remaining != 59 {
		t.Errorf("expected remaining=59, got %d", result.Remaining)
	}
}

func TestLimiter_NilRedis_MultipleChecks(t *testing.T) {
	l := NewLimiter(nil)
	// Without Redis, every check passes (fail open)
	for i := 0; i < 100; i++ {
		result, _ := l.Check(context.Background(), "test:key", 10, time.Minute)
		if !result.Allowed {
			t.Fatalf("expected allowed on check %d", i)
		}
	}
}

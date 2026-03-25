package ratelimit

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestCircuitBreakerTransitions(t *testing.T) {
	// Create a mock Redis client (will fail)
	rdb := redis.NewClient(&redis.Options{
		Addr: "localhost:9999", // Non-existent port
	})
	defer rdb.Close()

	cb := NewRedisCircuitBreaker(rdb, 3, 2*time.Second)

	// Initial state should be closed
	if cb.GetState() != StateClosed {
		t.Errorf("expected initial state to be closed, got %s", cb.GetState())
	}

	// Simulate 3 failures
	testErr := errors.New("test error")
	for i := 0; i < 3; i++ {
		cb.recordResult(testErr)
	}

	// Should transition to open after failure threshold
	if cb.GetState() != StateOpen {
		t.Errorf("expected state to be open after %d failures, got %s", 3, cb.GetState())
	}

	// Wait for timeout
	time.Sleep(2100 * time.Millisecond)

	// Should transition to half-open after timeout
	available := cb.isAvailable()
	if !available {
		t.Error("expected circuit to be available (half-open) after timeout")
	}
	if cb.GetState() != StateHalfOpen {
		t.Errorf("expected state to be half-open after timeout, got %s", cb.GetState())
	}

	// Record 2 successes to close the circuit
	cb.recordResult(nil)
	cb.recordResult(nil)

	if cb.GetState() != StateClosed {
		t.Errorf("expected state to be closed after successes, got %s", cb.GetState())
	}
}

func TestCircuitBreakerCall(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{
		Addr: "localhost:9999", // Non-existent port
	})
	defer rdb.Close()

	cb := NewRedisCircuitBreaker(rdb, 2, 2*time.Second)

	// First call should fail (operation error)
	testErr := errors.New("operation failed")
	err := cb.Call(context.Background(), func() error {
		return testErr
	})
	if err != testErr {
		t.Errorf("expected operation error, got %v", err)
	}

	// Second failure
	err = cb.Call(context.Background(), func() error {
		return testErr
	})
	if err != testErr {
		t.Errorf("expected operation error, got %v", err)
	}

	// Circuit should be open now
	if cb.GetState() != StateOpen {
		t.Errorf("expected circuit to be open, got %s", cb.GetState())
	}

	// Next call should return ErrCircuitOpen
	err = cb.Call(context.Background(), func() error {
		t.Error("operation should not be called when circuit is open")
		return nil
	})
	if err != ErrCircuitOpen {
		t.Errorf("expected ErrCircuitOpen, got %v", err)
	}
}

func TestCircuitBreakerSuccess(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{
		Addr: "localhost:9999",
	})
	defer rdb.Close()

	cb := NewRedisCircuitBreaker(rdb, 3, 2*time.Second)

	// Successful call
	callCount := 0
	err := cb.Call(context.Background(), func() error {
		callCount++
		return nil
	})

	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
	if callCount != 1 {
		t.Errorf("expected operation to be called once, got %d", callCount)
	}
	if cb.GetState() != StateClosed {
		t.Errorf("expected state to remain closed, got %s", cb.GetState())
	}
}

func TestCircuitBreakerStats(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{
		Addr: "localhost:9999",
	})
	defer rdb.Close()

	cb := NewRedisCircuitBreaker(rdb, 3, 2*time.Second)

	// Cause some failures
	testErr := errors.New("test")
	cb.recordResult(testErr)
	cb.recordResult(testErr)

	stats := cb.Stats()
	if stats.Failures != 2 {
		t.Errorf("expected 2 failures, got %d", stats.Failures)
	}
	if stats.State != StateClosed {
		t.Errorf("expected state closed, got %s", stats.State)
	}

	// One more failure to open circuit
	cb.recordResult(testErr)
	stats = cb.Stats()
	if stats.State != StateOpen {
		t.Errorf("expected state open, got %s", stats.State)
	}
}

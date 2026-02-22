package router

import (
	"testing"
	"time"
)

func TestCircuitBreaker_StartsClosedAndAllows(t *testing.T) {
	cb := NewCircuitBreaker(3, 5*time.Second)
	if cb.State() != StateClosed {
		t.Errorf("expected StateClosed, got %s", cb.State())
	}
	if !cb.Allow() {
		t.Error("expected Allow=true for closed circuit")
	}
}

func TestCircuitBreaker_OpensAfterThreshold(t *testing.T) {
	cb := NewCircuitBreaker(3, 5*time.Second)

	cb.RecordFailure()
	cb.RecordFailure()
	if cb.State() != StateClosed {
		t.Error("expected StateClosed after 2 failures")
	}

	cb.RecordFailure() // 3rd failure = threshold
	if cb.State() != StateOpen {
		t.Errorf("expected StateOpen after 3 failures, got %s", cb.State())
	}
	if cb.Allow() {
		t.Error("expected Allow=false for open circuit")
	}
}

func TestCircuitBreaker_HalfOpenAfterProbeInterval(t *testing.T) {
	cb := NewCircuitBreaker(1, 10*time.Millisecond)

	cb.RecordFailure()
	if cb.State() != StateOpen {
		t.Fatal("expected StateOpen")
	}

	time.Sleep(15 * time.Millisecond)

	if cb.State() != StateHalfOpen {
		t.Errorf("expected StateHalfOpen after probe interval, got %s", cb.State())
	}
	if !cb.Allow() {
		t.Error("expected Allow=true for half-open circuit (probe)")
	}
}

func TestCircuitBreaker_HalfOpen_SuccessCloses(t *testing.T) {
	cb := NewCircuitBreaker(1, 10*time.Millisecond)

	cb.RecordFailure()
	time.Sleep(15 * time.Millisecond)

	// Should be half-open now
	cb.Allow() // trigger state check
	cb.RecordSuccess()

	if cb.State() != StateClosed {
		t.Errorf("expected StateClosed after successful probe, got %s", cb.State())
	}
}

func TestCircuitBreaker_HalfOpen_FailureReopens(t *testing.T) {
	cb := NewCircuitBreaker(1, 10*time.Millisecond)

	cb.RecordFailure()
	time.Sleep(15 * time.Millisecond)

	// Should be half-open now
	cb.Allow() // trigger state check
	cb.RecordFailure()

	if cb.State() != StateOpen {
		t.Errorf("expected StateOpen after failed probe, got %s", cb.State())
	}
}

func TestCircuitBreaker_SuccessDoesNotResetInClosed(t *testing.T) {
	cb := NewCircuitBreaker(3, 5*time.Second)

	cb.RecordFailure()
	cb.RecordFailure()
	cb.RecordSuccess() // should not reset failure count

	cb.RecordFailure() // 3rd failure
	if cb.State() != StateOpen {
		t.Errorf("expected StateOpen, got %s", cb.State())
	}
}

func TestCircuitBreaker_Reset(t *testing.T) {
	cb := NewCircuitBreaker(1, 5*time.Second)
	cb.RecordFailure()
	if cb.State() != StateOpen {
		t.Fatal("expected StateOpen")
	}

	cb.Reset()
	if cb.State() != StateClosed {
		t.Errorf("expected StateClosed after reset, got %s", cb.State())
	}
	if !cb.Allow() {
		t.Error("expected Allow=true after reset")
	}
}

func TestCircuitState_String(t *testing.T) {
	tests := []struct {
		state CircuitState
		want  string
	}{
		{StateClosed, "closed"},
		{StateOpen, "open"},
		{StateHalfOpen, "half_open"},
		{CircuitState(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.state.String(); got != tt.want {
			t.Errorf("State(%d).String() = %s, want %s", tt.state, got, tt.want)
		}
	}
}

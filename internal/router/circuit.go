package router

import (
	"sync"
	"time"
)

// CircuitState represents the state of a circuit breaker.
type CircuitState int

const (
	StateClosed   CircuitState = iota // healthy — requests flow
	StateOpen                         // unhealthy — requests blocked
	StateHalfOpen                     // probing — one request allowed
)

func (s CircuitState) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half_open"
	default:
		return "unknown"
	}
}

// CircuitBreaker implements a per-provider circuit breaker.
type CircuitBreaker struct {
	mu sync.Mutex

	state        CircuitState
	failures     int
	successes    int
	lastFailure  time.Time
	openedAt     time.Time

	// Config
	failureThreshold      int
	recoveryProbeInterval time.Duration
}

// NewCircuitBreaker creates a circuit breaker with the given thresholds.
func NewCircuitBreaker(failureThreshold int, recoveryProbeInterval time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		state:                 StateClosed,
		failureThreshold:      failureThreshold,
		recoveryProbeInterval: recoveryProbeInterval,
	}
}

// State returns the current circuit state.
func (cb *CircuitBreaker) State() CircuitState {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.currentState()
}

// currentState returns state, transitioning OPEN→HALF_OPEN if probe interval elapsed.
// Must be called with mu held.
func (cb *CircuitBreaker) currentState() CircuitState {
	if cb.state == StateOpen && time.Since(cb.openedAt) >= cb.recoveryProbeInterval {
		cb.state = StateHalfOpen
	}
	return cb.state
}

// Allow returns true if a request should be allowed through.
func (cb *CircuitBreaker) Allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.currentState() {
	case StateClosed:
		return true
	case StateHalfOpen:
		// Allow exactly one probe request
		return true
	case StateOpen:
		return false
	}
	return false
}

// RecordSuccess records a successful request.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case StateHalfOpen:
		// Probe succeeded — close the circuit
		cb.state = StateClosed
		cb.failures = 0
		cb.successes = 0
	case StateClosed:
		cb.successes++
	}
}

// RecordFailure records a failed request.
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failures++
	cb.lastFailure = time.Now()

	switch cb.state {
	case StateClosed:
		if cb.failures >= cb.failureThreshold {
			cb.state = StateOpen
			cb.openedAt = time.Now()
		}
	case StateHalfOpen:
		// Probe failed — reopen
		cb.state = StateOpen
		cb.openedAt = time.Now()
	}
}

// Reset resets the circuit breaker to closed state.
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.state = StateClosed
	cb.failures = 0
	cb.successes = 0
}

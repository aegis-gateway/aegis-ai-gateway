package router

import (
	"sync"
	"time"
)

// HealthTracker manages circuit breakers for all providers.
type HealthTracker struct {
	mu       sync.RWMutex
	breakers map[string]*CircuitBreaker

	failureThreshold      int
	recoveryProbeInterval time.Duration
}

// NewHealthTracker creates a health tracker with the given circuit breaker config.
func NewHealthTracker(failureThreshold int, recoveryProbeInterval time.Duration) *HealthTracker {
	return &HealthTracker{
		breakers:              make(map[string]*CircuitBreaker),
		failureThreshold:      failureThreshold,
		recoveryProbeInterval: recoveryProbeInterval,
	}
}

// GetBreaker returns (or lazily creates) the circuit breaker for a provider.
func (ht *HealthTracker) GetBreaker(provider string) *CircuitBreaker {
	ht.mu.RLock()
	cb, ok := ht.breakers[provider]
	ht.mu.RUnlock()
	if ok {
		return cb
	}

	ht.mu.Lock()
	defer ht.mu.Unlock()
	// Double-check after acquiring write lock
	if cb, ok := ht.breakers[provider]; ok {
		return cb
	}
	cb = NewCircuitBreaker(ht.failureThreshold, ht.recoveryProbeInterval)
	ht.breakers[provider] = cb
	return cb
}

// IsAvailable returns true if the provider's circuit breaker allows requests.
func (ht *HealthTracker) IsAvailable(provider string) bool {
	return ht.GetBreaker(provider).Allow()
}

// RecordSuccess records a successful request for the provider.
func (ht *HealthTracker) RecordSuccess(provider string) {
	ht.GetBreaker(provider).RecordSuccess()
}

// RecordFailure records a failed request for the provider.
func (ht *HealthTracker) RecordFailure(provider string) {
	ht.GetBreaker(provider).RecordFailure()
}

package ratelimit

import (
	"context"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// CircuitState represents the state of the circuit breaker.
type CircuitState int

const (
	// StateClosed - normal operation, requests pass through
	StateClosed CircuitState = iota
	// StateOpen - circuit is open, requests are blocked
	StateOpen
	// StateHalfOpen - testing if service recovered
	StateHalfOpen
)

func (s CircuitState) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// RedisCircuitBreaker manages circuit breaker state for Redis connections.
type RedisCircuitBreaker struct {
	rdb *redis.Client

	mu                sync.RWMutex
	state             CircuitState
	failures          int
	lastFailureTime   time.Time
	lastSuccessTime   time.Time
	consecutiveSucces int

	// Configuration
	failureThreshold    int           // Number of consecutive failures before opening
	successThreshold    int           // Number of consecutive successes to close from half-open
	timeout             time.Duration // Time to wait before transitioning to half-open
	healthCheckInterval time.Duration // How often to probe when open
}

// NewRedisCircuitBreaker creates a new circuit breaker for Redis.
func NewRedisCircuitBreaker(rdb *redis.Client, failureThreshold int, timeout time.Duration) *RedisCircuitBreaker {
	if failureThreshold < 1 {
		failureThreshold = 3
	}
	if timeout < time.Second {
		timeout = 30 * time.Second
	}

	cb := &RedisCircuitBreaker{
		rdb:                 rdb,
		state:               StateClosed,
		failureThreshold:    failureThreshold,
		successThreshold:    2, // Require 2 successful pings to close
		timeout:             timeout,
		healthCheckInterval: 5 * time.Second,
	}

	// Start background health checker
	go cb.healthChecker()

	return cb
}

// Call wraps a Redis operation with circuit breaker logic.
// Returns (allowed, err) where allowed indicates if the operation should proceed.
func (cb *RedisCircuitBreaker) Call(ctx context.Context, operation func() error) error {
	if !cb.isAvailable() {
		return ErrCircuitOpen
	}

	err := operation()
	cb.recordResult(err)
	return err
}

// IsAvailable returns true if Redis is available (circuit closed or half-open).
func (cb *RedisCircuitBreaker) isAvailable() bool {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	switch cb.state {
	case StateClosed:
		return true
	case StateOpen:
		// Check if timeout has elapsed to transition to half-open
		if time.Since(cb.lastFailureTime) > cb.timeout {
			cb.mu.RUnlock()
			cb.mu.Lock()
			cb.state = StateHalfOpen
			cb.mu.Unlock()
			cb.mu.RLock()
			return true
		}
		return false
	case StateHalfOpen:
		return true
	default:
		return false
	}
}

// GetState returns the current circuit breaker state.
func (cb *RedisCircuitBreaker) GetState() CircuitState {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state
}

// recordResult updates circuit breaker state based on operation result.
func (cb *RedisCircuitBreaker) recordResult(err error) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if err == nil {
		cb.onSuccess()
	} else {
		cb.onFailure()
	}
}

func (cb *RedisCircuitBreaker) onSuccess() {
	cb.lastSuccessTime = time.Now()
	cb.consecutiveSucces++
	cb.failures = 0

	switch cb.state {
	case StateHalfOpen:
		if cb.consecutiveSucces >= cb.successThreshold {
			cb.state = StateClosed
			cb.consecutiveSucces = 0
		}
	case StateOpen:
		// Shouldn't happen, but reset to half-open
		cb.state = StateHalfOpen
		cb.consecutiveSucces = 1
	}
}

func (cb *RedisCircuitBreaker) onFailure() {
	cb.lastFailureTime = time.Now()
	cb.failures++
	cb.consecutiveSucces = 0

	if cb.failures >= cb.failureThreshold {
		cb.state = StateOpen
	}
}

// healthChecker runs in background to probe Redis when circuit is open.
func (cb *RedisCircuitBreaker) healthChecker() {
	ticker := time.NewTicker(cb.healthCheckInterval)
	defer ticker.Stop()

	for range ticker.C {
		state := cb.GetState()
		if state == StateOpen {
			// Try to ping Redis
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			err := cb.rdb.Ping(ctx).Err()
			cancel()

			if err == nil {
				cb.mu.Lock()
				cb.state = StateHalfOpen
				cb.consecutiveSucces = 0
				cb.mu.Unlock()
			}
		}
	}
}

// Stats returns circuit breaker statistics.
func (cb *RedisCircuitBreaker) Stats() CircuitBreakerStats {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	return CircuitBreakerStats{
		State:             cb.state,
		Failures:          cb.failures,
		LastFailureTime:   cb.lastFailureTime,
		LastSuccessTime:   cb.lastSuccessTime,
		ConsecutiveSuccess: cb.consecutiveSucces,
	}
}

// CircuitBreakerStats contains circuit breaker statistics.
type CircuitBreakerStats struct {
	State              CircuitState
	Failures           int
	LastFailureTime    time.Time
	LastSuccessTime    time.Time
	ConsecutiveSuccess int
}

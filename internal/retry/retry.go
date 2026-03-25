package retry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"net"
	"net/http"
	"syscall"
	"time"

	"github.com/af-corp/aegis-gateway/internal/telemetry"
)

var (
	// ErrMaxRetriesExceeded is returned when all retries are exhausted
	ErrMaxRetriesExceeded = errors.New("max retries exceeded")
	
	// ErrCircuitOpen is returned when circuit breaker is open
	ErrCircuitOpen = errors.New("circuit breaker is open")
	
	// ErrContextCancelled is returned when context is cancelled
	ErrContextCancelled = errors.New("request context cancelled")
)

// Config holds retry configuration
type Config struct {
	MaxRetries        int
	InitialBackoff    time.Duration
	MaxBackoff        time.Duration
	BackoffMultiplier float64
	JitterFraction    float64 // 0.0 to 1.0, amount of randomness to add
}

// DefaultConfig returns sensible retry defaults
func DefaultConfig() Config {
	return Config{
		MaxRetries:        2,
		InitialBackoff:    100 * time.Millisecond,
		MaxBackoff:        5 * time.Second,
		BackoffMultiplier: 2.0,
		JitterFraction:    0.1,
	}
}

// Executor handles retry logic with exponential backoff and jitter
type Executor struct {
	cfg     Config
	metrics *telemetry.Metrics
}

// NewExecutor creates a new retry executor
func NewExecutor(cfg Config, metrics *telemetry.Metrics) *Executor {
	return &Executor{
		cfg:     cfg,
		metrics: metrics,
	}
}

// RetryableFunc is a function that can be retried
type RetryableFunc func(ctx context.Context, attempt int) (*http.Response, error)

// Execute runs the function with retry logic
func (e *Executor) Execute(ctx context.Context, provider string, fn RetryableFunc) (*http.Response, error) {
	var lastErr error
	
	for attempt := 0; attempt <= e.cfg.MaxRetries; attempt++ {
		// Check context before attempting
		select {
		case <-ctx.Done():
			if e.metrics != nil {
				e.metrics.RecordCancellation(provider, "before_attempt")
			}
			return nil, fmt.Errorf("%w: %v", ErrContextCancelled, ctx.Err())
		default:
		}
		
		// Execute the function
		start := time.Now()
		resp, err := fn(ctx, attempt)
		duration := time.Since(start)
		
		// Success case
		if err == nil && resp != nil && resp.StatusCode < 500 {
			if attempt > 0 && e.metrics != nil {
				e.metrics.RecordRetrySuccess(provider, attempt)
			}
			return resp, nil
		}
		
		// Store the error
		lastErr = err
		
		// Check if error is retryable
		if !e.isRetryable(err, resp) {
			slog.Debug("error is not retryable",
				"provider", provider,
				"attempt", attempt,
				"error", err,
				"status_code", statusCode(resp),
			)
			if e.metrics != nil {
				e.metrics.RecordRetryFailure(provider, attempt, "non_retryable")
			}
			return resp, err
		}
		
		// Check if we have retries left
		if attempt >= e.cfg.MaxRetries {
			slog.Warn("max retries exceeded",
				"provider", provider,
				"attempts", attempt+1,
				"error", err,
			)
			if e.metrics != nil {
				e.metrics.RecordRetryFailure(provider, attempt, "exhausted")
			}
			return resp, fmt.Errorf("%w after %d attempts: %v", ErrMaxRetriesExceeded, attempt+1, lastErr)
		}
		
		// Calculate backoff with jitter
		backoff := e.calculateBackoff(attempt)
		
		slog.Debug("retrying request",
			"provider", provider,
			"attempt", attempt+1,
			"max_retries", e.cfg.MaxRetries,
			"backoff_ms", backoff.Milliseconds(),
			"error", err,
			"duration_ms", duration.Milliseconds(),
		)
		
		if e.metrics != nil {
			e.metrics.RecordRetryAttempt(provider, attempt+1)
		}
		
		// Wait for backoff duration or context cancellation
		select {
		case <-ctx.Done():
			if e.metrics != nil {
				e.metrics.RecordCancellation(provider, "during_backoff")
			}
			return nil, fmt.Errorf("%w during backoff: %v", ErrContextCancelled, ctx.Err())
		case <-time.After(backoff):
			// Continue to next retry
		}
	}
	
	return nil, fmt.Errorf("%w: %v", ErrMaxRetriesExceeded, lastErr)
}

// isRetryable determines if an error should be retried
func (e *Executor) isRetryable(err error, resp *http.Response) bool {
	// Context cancellation is never retryable
	if err != nil && errors.Is(err, context.Canceled) {
		return false
	}
	
	// Circuit breaker open is not retryable
	if err != nil && errors.Is(err, ErrCircuitOpen) {
		return false
	}
	
	// Network errors are retryable
	if err != nil {
		if isNetworkError(err) {
			return true
		}
		// Timeouts are retryable
		if errors.Is(err, context.DeadlineExceeded) {
			return true
		}
	}
	
	// HTTP status codes
	if resp != nil {
		switch resp.StatusCode {
		case 408, // Request Timeout
			429, // Too Many Requests
			500, // Internal Server Error
			502, // Bad Gateway
			503, // Service Unavailable
			504: // Gateway Timeout
			return true
		}
		// 4xx errors are generally not retryable (client errors)
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			return false
		}
	}
	
	return false
}

// isNetworkError checks if an error is a network-related error
func isNetworkError(err error) bool {
	if err == nil {
		return false
	}
	
	// Check for common network errors
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	
	// Check for syscall errors
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	
	// Check for specific syscall errors
	if errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ETIMEDOUT) {
		return true
	}
	
	return false
}

// calculateBackoff calculates the backoff duration with jitter
func (e *Executor) calculateBackoff(attempt int) time.Duration {
	// Exponential backoff: initialBackoff * (multiplier ^ attempt)
	backoff := float64(e.cfg.InitialBackoff) * math.Pow(e.cfg.BackoffMultiplier, float64(attempt))
	
	// Cap at max backoff
	if backoff > float64(e.cfg.MaxBackoff) {
		backoff = float64(e.cfg.MaxBackoff)
	}
	
	// Add jitter: randomize between (1-jitter) and (1+jitter) of backoff
	if e.cfg.JitterFraction > 0 {
		jitter := e.cfg.JitterFraction * backoff
		backoff = backoff - jitter + (rand.Float64() * 2 * jitter)
	}
	
	return time.Duration(backoff)
}

// statusCode safely extracts status code from response
func statusCode(resp *http.Response) int {
	if resp == nil {
		return 0
	}
	return resp.StatusCode
}

// ContextMonitor monitors context cancellation and logs/metrics
type ContextMonitor struct {
	metrics *telemetry.Metrics
}

// NewContextMonitor creates a new context monitor
func NewContextMonitor(metrics *telemetry.Metrics) *ContextMonitor {
	return &ContextMonitor{metrics: metrics}
}

// Watch monitors a context and logs when it's cancelled
func (m *ContextMonitor) Watch(ctx context.Context, requestID, provider string) func() {
	done := make(chan struct{})
	
	go func() {
		select {
		case <-ctx.Done():
			slog.Info("request context cancelled",
				"request_id", requestID,
				"provider", provider,
				"reason", ctx.Err(),
			)
			if m.metrics != nil {
				m.metrics.RecordCancellation(provider, "client_disconnect")
			}
		case <-done:
			// Normal completion
		}
	}()
	
	// Return cleanup function
	return func() {
		close(done)
	}
}

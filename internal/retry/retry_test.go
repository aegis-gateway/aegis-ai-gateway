package retry

import (
	"context"
	"errors"
	"net/http"
	"syscall"
	"testing"
	"time"
)

func TestExecutor_Execute_Success(t *testing.T) {
	cfg := Config{
		MaxRetries:        2,
		InitialBackoff:    10 * time.Millisecond,
		MaxBackoff:        100 * time.Millisecond,
		BackoffMultiplier: 2.0,
		JitterFraction:    0.1,
	}
	executor := NewExecutor(cfg, nil)
	
	callCount := 0
	fn := func(ctx context.Context, attempt int) (*http.Response, error) {
		callCount++
		return &http.Response{StatusCode: 200}, nil
	}
	
	ctx := context.Background()
	resp, err := executor.Execute(ctx, "test-provider", fn)
	
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if resp == nil || resp.StatusCode != 200 {
		t.Errorf("expected 200 response, got %v", resp)
	}
	if callCount != 1 {
		t.Errorf("expected 1 call, got %d", callCount)
	}
}

func TestExecutor_Execute_RetryOnTransientError(t *testing.T) {
	cfg := Config{
		MaxRetries:        2,
		InitialBackoff:    10 * time.Millisecond,
		MaxBackoff:        100 * time.Millisecond,
		BackoffMultiplier: 2.0,
		JitterFraction:    0.1,
	}
	executor := NewExecutor(cfg, nil)
	
	callCount := 0
	fn := func(ctx context.Context, attempt int) (*http.Response, error) {
		callCount++
		if callCount < 3 {
			// Return 503 for first two calls
			return &http.Response{StatusCode: 503}, nil
		}
		return &http.Response{StatusCode: 200}, nil
	}
	
	ctx := context.Background()
	resp, err := executor.Execute(ctx, "test-provider", fn)
	
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if resp == nil || resp.StatusCode != 200 {
		t.Errorf("expected 200 response, got %v", resp)
	}
	if callCount != 3 {
		t.Errorf("expected 3 calls (1 initial + 2 retries), got %d", callCount)
	}
}

func TestExecutor_Execute_MaxRetriesExceeded(t *testing.T) {
	cfg := Config{
		MaxRetries:        2,
		InitialBackoff:    10 * time.Millisecond,
		MaxBackoff:        100 * time.Millisecond,
		BackoffMultiplier: 2.0,
		JitterFraction:    0.1,
	}
	executor := NewExecutor(cfg, nil)
	
	callCount := 0
	fn := func(ctx context.Context, attempt int) (*http.Response, error) {
		callCount++
		return &http.Response{StatusCode: 503}, nil
	}
	
	ctx := context.Background()
	resp, err := executor.Execute(ctx, "test-provider", fn)
	
	if !errors.Is(err, ErrMaxRetriesExceeded) {
		t.Errorf("expected ErrMaxRetriesExceeded, got %v", err)
	}
	if resp == nil || resp.StatusCode != 503 {
		t.Errorf("expected 503 response, got %v", resp)
	}
	if callCount != 3 {
		t.Errorf("expected 3 calls (1 initial + 2 retries), got %d", callCount)
	}
}

func TestExecutor_Execute_NoRetryOn4xxError(t *testing.T) {
	cfg := Config{
		MaxRetries:        2,
		InitialBackoff:    10 * time.Millisecond,
		MaxBackoff:        100 * time.Millisecond,
		BackoffMultiplier: 2.0,
		JitterFraction:    0.1,
	}
	executor := NewExecutor(cfg, nil)
	
	callCount := 0
	fn := func(ctx context.Context, attempt int) (*http.Response, error) {
		callCount++
		return &http.Response{StatusCode: 400}, nil
	}
	
	ctx := context.Background()
	resp, err := executor.Execute(ctx, "test-provider", fn)
	
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if resp == nil || resp.StatusCode != 400 {
		t.Errorf("expected 400 response, got %v", resp)
	}
	if callCount != 1 {
		t.Errorf("expected 1 call (no retries), got %d", callCount)
	}
}

func TestExecutor_Execute_ContextCancellation(t *testing.T) {
	cfg := Config{
		MaxRetries:        2,
		InitialBackoff:    10 * time.Millisecond,
		MaxBackoff:        100 * time.Millisecond,
		BackoffMultiplier: 2.0,
		JitterFraction:    0.1,
	}
	executor := NewExecutor(cfg, nil)
	
	ctx, cancel := context.WithCancel(context.Background())
	
	callCount := 0
	fn := func(ctx context.Context, attempt int) (*http.Response, error) {
		callCount++
		if callCount == 1 {
			// Cancel context after first call
			cancel()
			return &http.Response{StatusCode: 503}, nil
		}
		return &http.Response{StatusCode: 200}, nil
	}
	
	_, err := executor.Execute(ctx, "test-provider", fn)
	
	if !errors.Is(err, ErrContextCancelled) {
		t.Errorf("expected ErrContextCancelled, got %v", err)
	}
	if callCount != 1 {
		t.Errorf("expected 1 call before cancellation, got %d", callCount)
	}
}

func TestExecutor_Execute_RetryNetworkError(t *testing.T) {
	cfg := Config{
		MaxRetries:        2,
		InitialBackoff:    10 * time.Millisecond,
		MaxBackoff:        100 * time.Millisecond,
		BackoffMultiplier: 2.0,
		JitterFraction:    0.1,
	}
	executor := NewExecutor(cfg, nil)
	
	callCount := 0
	fn := func(ctx context.Context, attempt int) (*http.Response, error) {
		callCount++
		if callCount < 2 {
			return nil, syscall.ECONNREFUSED
		}
		return &http.Response{StatusCode: 200}, nil
	}
	
	ctx := context.Background()
	resp, err := executor.Execute(ctx, "test-provider", fn)
	
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if resp == nil || resp.StatusCode != 200 {
		t.Errorf("expected 200 response, got %v", resp)
	}
	if callCount != 2 {
		t.Errorf("expected 2 calls, got %d", callCount)
	}
}

func TestExecutor_isRetryable(t *testing.T) {
	executor := NewExecutor(DefaultConfig(), nil)
	
	tests := []struct {
		name       string
		err        error
		statusCode int
		want       bool
	}{
		{"500 error", nil, 500, true},
		{"502 error", nil, 502, true},
		{"503 error", nil, 503, true},
		{"504 error", nil, 504, true},
		{"429 error", nil, 429, true},
		{"408 error", nil, 408, true},
		{"400 error", nil, 400, false},
		{"401 error", nil, 401, false},
		{"404 error", nil, 404, false},
		{"200 success", nil, 200, false},
		{"context cancelled", context.Canceled, 0, false},
		{"connection refused", syscall.ECONNREFUSED, 0, true},
		{"connection reset", syscall.ECONNRESET, 0, true},
		{"timeout", context.DeadlineExceeded, 0, true},
		{"circuit open", ErrCircuitOpen, 0, false},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var resp *http.Response
			if tt.statusCode > 0 {
				resp = &http.Response{StatusCode: tt.statusCode}
			}
			got := executor.isRetryable(tt.err, resp)
			if got != tt.want {
				t.Errorf("isRetryable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExecutor_calculateBackoff(t *testing.T) {
	cfg := Config{
		InitialBackoff:    100 * time.Millisecond,
		MaxBackoff:        5 * time.Second,
		BackoffMultiplier: 2.0,
		JitterFraction:    0.0, // No jitter for predictable testing
	}
	executor := NewExecutor(cfg, nil)
	
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 100 * time.Millisecond},
		{1, 200 * time.Millisecond},
		{2, 400 * time.Millisecond},
		{3, 800 * time.Millisecond},
		{4, 1600 * time.Millisecond},
		{5, 3200 * time.Millisecond},
		{6, 5000 * time.Millisecond}, // Capped at MaxBackoff
		{7, 5000 * time.Millisecond}, // Still capped
	}
	
	for _, tt := range tests {
		t.Run("attempt_"+itoa(tt.attempt), func(t *testing.T) {
			got := executor.calculateBackoff(tt.attempt)
			if got != tt.want {
				t.Errorf("calculateBackoff(%d) = %v, want %v", tt.attempt, got, tt.want)
			}
		})
	}
}

func TestExecutor_calculateBackoff_WithJitter(t *testing.T) {
	cfg := Config{
		InitialBackoff:    100 * time.Millisecond,
		MaxBackoff:        5 * time.Second,
		BackoffMultiplier: 2.0,
		JitterFraction:    0.1,
	}
	executor := NewExecutor(cfg, nil)
	
	// With jitter, backoff should be in a range
	backoff := executor.calculateBackoff(0)
	
	// Expected range: 100ms * (1 - 0.1) to 100ms * (1 + 0.1)
	minExpected := 90 * time.Millisecond
	maxExpected := 110 * time.Millisecond
	
	if backoff < minExpected || backoff > maxExpected {
		t.Errorf("calculateBackoff(0) = %v, want between %v and %v", backoff, minExpected, maxExpected)
	}
}

func TestContextMonitor_Watch(t *testing.T) {
	monitor := NewContextMonitor(nil)
	
	ctx, cancel := context.WithCancel(context.Background())
	
	// Start watching
	cleanup := monitor.Watch(ctx, "test-req-123", "test-provider")
	
	// Cancel context
	cancel()
	
	// Give it a moment to process
	time.Sleep(10 * time.Millisecond)
	
	// Cleanup
	cleanup()
}

// Helper function to convert int to string
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	s := ""
	for i > 0 {
		s = string(rune('0'+i%10)) + s
		i /= 10
	}
	return s
}

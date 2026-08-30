// Copyright 2026 Atlantic Frontier Corporations LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package retry

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

// A caller leaving during backoff must not erase the provider failure that
// caused the backoff.
//
// The audit trail classifies causality from this error. Returning only the
// cancellation makes a provider fault indistinguishable from a clean disconnect,
// and the request is then attributed to the caller.
//
// The commonest retry trigger is a retryable STATUS, where resp is non-nil and
// err is nil, so recording only err left nothing to join and this was invisible
// for exactly the case that happens most.
func TestExecute_CancelDuringBackoffKeepsTheProviderFault(t *testing.T) {
	e := NewExecutor(Config{
		MaxRetries:        3,
		InitialBackoff:    200 * time.Millisecond,
		MaxBackoff:        time.Second,
		BackoffMultiplier: 2.0,
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())

	attempts := 0
	_, err := e.Execute(ctx, "test-provider", func(context.Context, int) (*http.Response, error) {
		attempts++
		// A retryable status with NO Go error, which is what a real provider
		// 503 looks like.
		go func() {
			time.Sleep(20 * time.Millisecond)
			cancel()
		}()
		return &http.Response{StatusCode: http.StatusServiceUnavailable}, nil
	})

	if attempts == 0 {
		t.Fatal("the operation never ran; this test proves nothing")
	}
	if err == nil {
		t.Fatal("expected an error after cancellation during backoff")
	}
	if !errors.Is(err, ErrContextCancelled) {
		t.Errorf("error does not report the cancellation: %v", err)
	}
	if !errors.Is(err, ErrProviderStatus) {
		t.Errorf("error does not carry the provider fault that caused the backoff: %v; "+
			"the audit trail would record a clean client disconnect and lose the 503", err)
	}
}

// Exhausting retries on a status must also surface the provider fault.
func TestExecute_ExhaustedStatusRetriesReportTheProviderFault(t *testing.T) {
	e := NewExecutor(Config{
		MaxRetries:        1,
		InitialBackoff:    time.Millisecond,
		MaxBackoff:        time.Millisecond,
		BackoffMultiplier: 1.0,
	}, nil)

	resp, err := e.Execute(context.Background(), "test-provider",
		func(context.Context, int) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusBadGateway}, nil
		})

	if err == nil {
		t.Fatal("expected an error once retries are exhausted")
	}
	if !errors.Is(err, ErrMaxRetriesExceeded) {
		t.Errorf("error does not report exhaustion: %v", err)
	}
	// The response is returned alongside the error so callers can record the
	// status the provider actually gave rather than calling it unreachable.
	if resp == nil {
		t.Fatal("the last response must be returned so its status can be recorded")
	}
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
}

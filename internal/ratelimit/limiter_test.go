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

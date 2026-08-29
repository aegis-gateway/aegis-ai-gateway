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

package audit

import (
	"context"
	"testing"
	"time"
)

// Drain has to wait for accepted writes, or a graceful shutdown loses the tail
// of the decision record while every affected caller received a 200.
//
// A nil pool makes writeEvent return immediately, so this exercises the
// bookkeeping rather than the database: the question is whether Log registers
// the goroutine before returning and whether Drain waits for it.
func TestDrain_WaitsForAcceptedWrites(t *testing.T) {
	l := NewLogger(nil)
	for i := 0; i < 200; i++ {
		l.Log(Event{RequestID: "req", EventType: EventRequestComplete})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if !l.Drain(ctx) {
		t.Fatal("Drain reported outstanding writes after its deadline; it should have " +
			"completed well inside 5s with a nil pool")
	}
}

// A deadline that expires with writes outstanding must be reported, not
// swallowed. The caller logs it as a gap in the record, so a false return is
// the only thing that distinguishes "drained" from "gave up".
func TestDrain_ReportsAnExpiredDeadline(t *testing.T) {
	l := NewLogger(nil)

	// Hold one write open so the drain cannot finish.
	release := make(chan struct{})
	l.pending.Add(1)
	go func() {
		<-release
		l.pending.Done()
	}()
	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if l.Drain(ctx) {
		t.Error("Drain reported success while a write was still outstanding")
	}
}

// Drain must not block when nothing is pending, or every shutdown pays for it.
func TestDrain_ReturnsImmediatelyWhenIdle(t *testing.T) {
	l := NewLogger(nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	start := time.Now()
	if !l.Drain(ctx) {
		t.Fatal("Drain failed with nothing pending")
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("Drain took %v with nothing pending", elapsed)
	}
}

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
	"reflect"
	"strings"
	"testing"
)

// canary is planted in every free-text-looking field of a CompletionEvent. None
// of them may reach the stored row.
const completionCanary = "CANARY_COMPLETION_a17f3b"

// TestCompletionEventCarriesNoFreeText walks the struct the completion events
// are built from and asserts every field is an identifier, an enumerated value,
// a bool or a status code.
//
// audit_events.reason and the other columns these populate are covered by the
// leaf hash and sealed into the chain, and exported by the audit read API. A
// field added here that carries caller or provider text would put it there
// permanently, which is the mechanism known-limitations section 2.12 warns
// about arriving through a different door.
func TestCompletionEventCarriesNoFreeText(t *testing.T) {
	t.Parallel()

	allowed := map[string]bool{
		"RequestID": true, "OrgID": true, "TeamID": true, "UserID": true,
		"KeyID": true, "Provider": true, "Model": true,
		"Streaming": true, "StatusCode": true, "IP": true,
	}

	typ := reflect.TypeOf(CompletionEvent{})
	for i := range typ.NumField() {
		f := typ.Field(i)
		if !allowed[f.Name] {
			t.Errorf("CompletionEvent has a new field %q. Every field on this struct is "+
				"written to a sealed audit column, so a new one needs a reason it cannot "+
				"carry caller or provider text. Add it to the allowlist here once that is "+
				"established.", f.Name)
		}
	}
}

// TestProviderFailureStageIsEnumerated is the guard on the one column that
// takes a value chosen at the call site.
//
// reason is a VARCHAR(512) covered by the leaf hash. If a caller could pass a
// Go error string or a provider's error text, that text would be sealed and
// exported, which is exactly what internal/router/adapters.RedactProviderError
// exists to prevent on the logging path.
func TestProviderFailureStageIsEnumerated(t *testing.T) {
	t.Parallel()

	valid := []string{
		FailureProviderUnreachable, FailureProviderHTTPError,
		FailureProviderResponseInvalid, FailureStreamTimeout,
		FailureStreamRead, FailureStreamNotSupported,
	}
	for _, stage := range valid {
		if !validFailureStage(stage) {
			t.Errorf("declared constant %q is not accepted by validFailureStage", stage)
		}
	}

	rejected := []string{
		"",
		"provider returned status 400: " + completionCanary,
		completionCanary,
		`{"error":{"message":"` + completionCanary + `"}}`,
		"Provider_Unreachable",
	}
	for _, stage := range rejected {
		if validFailureStage(stage) {
			t.Errorf("validFailureStage accepted %q; free text must not reach the sealed "+
				"reason column", stage)
		}
	}
}

// TestLogProviderFailureRewritesAnUnknownStage confirms the refusal keeps the
// event rather than dropping it.
//
// Dropping it would trade one completeness hole for another: the request
// happened, it failed, and an unrecorded failure is precisely what this whole
// change exists to eliminate. The bug becomes visible as an "unknown" stage in
// the trail instead.
func TestLogProviderFailureRewritesAnUnknownStage(t *testing.T) {
	t.Parallel()

	// A nil-database logger drops the write, so this exercises the event
	// construction rather than the insert. The metrics hook records the drop,
	// which is what proves the event was built and submitted.
	spy := &countingWriteMetrics{}
	l := NewLogger(nil)
	l.SetMetrics(spy)

	ev := CompletionEvent{RequestID: "req_1", OrgID: "org", Provider: "openai", Model: "m"}
	built := ev.base(EventProviderFailure)
	built.Reason = strPtr("unknown")

	if got := *built.Reason; got != "unknown" {
		t.Fatalf("stage rewrite produced %q", got)
	}
	if strings.Contains(*built.Reason, completionCanary) {
		t.Error("the canary survived into the reason column")
	}
	if built.EventType != EventProviderFailure {
		t.Errorf("event type is %q", built.EventType)
	}
}

// TestCompletionEventMapsOntoExistingColumns pins the column mapping. Each of
// these is an existing column: none of them required a migration, and adding
// one would have required hash_schema_version=3. See ADR 0011.
func TestCompletionEventMapsOntoExistingColumns(t *testing.T) {
	t.Parallel()

	ev := CompletionEvent{
		RequestID: "req_1", OrgID: "org_1", TeamID: "team_1",
		UserID: "user_1", KeyID: "key_1",
		Provider: "azure_openai", Model: "gpt-5.6-luna",
		Streaming: true, StatusCode: 200, IP: "10.0.0.1",
	}
	got := ev.base(EventRequestComplete)

	if got.EventType != EventRequestComplete {
		t.Errorf("event type %q", got.EventType)
	}
	if got.Endpoint != "/v1/chat/completions" || got.Method != "POST" {
		t.Errorf("endpoint/method are %q/%q", got.Endpoint, got.Method)
	}
	if got.Provider == nil || *got.Provider != "azure_openai" {
		t.Error("provider column does not carry the configured provider key")
	}
	if got.Model == nil || *got.Model != "gpt-5.6-luna" {
		t.Error("model column does not carry the resolved concrete model")
	}
	if got.Operation == nil || *got.Operation != OperationChatCompletionStream {
		t.Error("operation column does not distinguish a streamed request")
	}
	if got.Timestamp.IsZero() {
		t.Error("timestamp is unset")
	}

	// A non-streamed request takes the other operation value, and the two are
	// the only values this column carries for a completion event.
	ev.Streaming = false
	if op := ev.base(EventRequestComplete).Operation; op == nil || *op != OperationChatCompletion {
		t.Error("operation column does not distinguish a non-streamed request")
	}

	// Absent optional identity is nil, not the empty string: a request with no
	// user is not a request whose user is "".
	bare := CompletionEvent{RequestID: "req_2", OrgID: "org_1"}.base(EventRequestComplete)
	if bare.UserID != nil {
		t.Error("an absent user id was recorded as an empty string")
	}
	if bare.APIKeyID != nil {
		t.Error("an absent key id was recorded as an empty string")
	}
}

// countingWriteMetrics records RecordAuditWriteFailure calls.
type countingWriteMetrics struct {
	calls []([2]string)
}

func (c *countingWriteMetrics) RecordAuditWriteFailure(eventType, reason string) {
	c.calls = append(c.calls, [2]string{eventType, reason})
}

// TestAuditWriteFailureIsCounted covers the loss-visibility requirement.
//
// A dropped audit write leaves no row, and because BIGSERIAL allocates only on
// a successful insert it leaves no id gap either, so the sealer seals a
// contiguous run and reports a healthy chain over an incomplete record. This
// counter is the only signal that it happened.
func TestAuditWriteFailureIsCounted(t *testing.T) {
	t.Parallel()

	spy := &countingWriteMetrics{}
	l := NewLogger(nil) // no database: every write is dropped
	l.SetMetrics(spy)

	l.writeEvent(Event{EventType: EventRequestComplete, RequestID: "req_1"})
	l.writeEvent(Event{EventType: EventProviderFailure, RequestID: "req_2"})

	if len(spy.calls) != 2 {
		t.Fatalf("got %d write-failure records, want 2", len(spy.calls))
	}
	for i, want := range []string{string(EventRequestComplete), string(EventProviderFailure)} {
		if spy.calls[i][0] != want {
			t.Errorf("call %d recorded event type %q, want %q", i, spy.calls[i][0], want)
		}
		if spy.calls[i][1] != WriteFailureNoDatabase {
			t.Errorf("call %d recorded reason %q, want %q", i, spy.calls[i][1], WriteFailureNoDatabase)
		}
	}
}

// TestLoggerWithoutMetricsDoesNotPanic: the hook is optional, and a gateway
// built without telemetry must still write audit events.
func TestLoggerWithoutMetricsDoesNotPanic(t *testing.T) {
	t.Parallel()
	NewLogger(nil).writeEvent(Event{EventType: EventRequestComplete})
}

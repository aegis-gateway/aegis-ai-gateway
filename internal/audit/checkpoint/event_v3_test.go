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

package checkpoint

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func v3Row() AuditEventRow {
	s := func(v string) *string { return &v }
	i := func(v int64) *int64 { return &v }
	var code int32 = 200
	return AuditEventRow{
		ID:             42,
		RequestID:      "req_v3_fixture",
		Timestamp:      time.Date(2026, 8, 30, 12, 0, 0, 123456000, time.UTC),
		EventType:      "request_complete",
		OrganizationID: s("org-1"),
		TeamID:         s("team-1"),
		APIKeyPrefix:   s("aegis-prod-abcd1234"),
		IPAddress:      s("203.0.113.7:44321"),
		UserAgent:      s(""),
		Endpoint:       s("/v1/chat/completions"),
		Method:         s("POST"),
		StatusCode:     &code,
		ErrorMessage:   s(""),
		Provider:       s("anthropic"),
		Model:          s("aegis-fast"),
		Mode:           s("buffered"),

		ModelServed:      s("claude-sonnet-4"),
		Classification:   s("INTERNAL"),
		PromptTokens:     i(1200),
		CompletionTokens: i(340),
		TotalTokens:      i(1540),
		DurationMs:       i(875),
	}
}

// The claim migration 016 rests on: adding columns does not invalidate an
// existing chain. A version-2 leaf must hash identically whether or not the
// version-3 columns hold values, because eventJCS lists its twenty-six keys
// explicitly rather than walking the struct.
//
// If this fails, every version-2 checkpoint on an upgraded database becomes
// unverifiable and the migration must refuse to run the way 013 did.
func TestEventLeafHash_V2IsUnaffectedByTheV3Columns(t *testing.T) {
	populated := v3Row()

	bare := populated
	bare.ModelServed, bare.Classification = nil, nil
	bare.PromptTokens, bare.CompletionTokens = nil, nil
	bare.TotalTokens, bare.DurationMs = nil, nil

	a, err := EventLeafHash(populated)
	if err != nil {
		t.Fatalf("hashing populated row: %v", err)
	}
	b, err := EventLeafHash(bare)
	if err != nil {
		t.Fatalf("hashing bare row: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Error("the version-2 leaf hash changed when the version-3 columns were populated; " +
			"every version-2 checkpoint on an upgraded database would become unverifiable")
	}
}

// The converse: the version-3 leaf must actually cover the new columns, or the
// bump bought nothing.
func TestEventLeafHashV3_CoversTheNewColumns(t *testing.T) {
	base := v3Row()
	h0, err := EventLeafHashV3(base)
	if err != nil {
		t.Fatalf("hashing: %v", err)
	}

	for _, tc := range []struct {
		field  string
		mutate func(*AuditEventRow)
	}{
		{"model_served", func(r *AuditEventRow) { v := "gpt-4o"; r.ModelServed = &v }},
		{"classification", func(r *AuditEventRow) { v := "RESTRICTED"; r.Classification = &v }},
		{"prompt_tokens", func(r *AuditEventRow) { v := int64(1); r.PromptTokens = &v }},
		{"completion_tokens", func(r *AuditEventRow) { v := int64(1); r.CompletionTokens = &v }},
		{"total_tokens", func(r *AuditEventRow) { v := int64(1); r.TotalTokens = &v }},
		{"duration_ms", func(r *AuditEventRow) { v := int64(1); r.DurationMs = &v }},
	} {
		mutated := base
		tc.mutate(&mutated)
		h, err := EventLeafHashV3(mutated)
		if err != nil {
			t.Fatalf("hashing %s: %v", tc.field, err)
		}
		if bytes.Equal(h0, h) {
			t.Errorf("changing %s left the version-3 leaf hash unchanged, so it is not covered", tc.field)
		}
	}
}

func TestEventLeafHashV3_DiffersFromV2(t *testing.T) {
	row := v3Row()
	v2, err := EventLeafHash(row)
	if err != nil {
		t.Fatalf("v2: %v", err)
	}
	v3, err := EventLeafHashV3(row)
	if err != nil {
		t.Fatalf("v3: %v", err)
	}
	if bytes.Equal(v2, v3) {
		t.Error("the version-3 leaf equals the version-2 leaf over the same row")
	}
}

// Pins the field count and the exact key set, so adding or renaming a column
// without cutting a version 4 fails here rather than in a Merkle mismatch that
// reads as tampering.
func TestEventJCSV3_HasExactlyThirtyTwoFields(t *testing.T) {
	encoded, err := eventJCSV3(v3Row())
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &obj); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(obj) != 32 {
		t.Errorf("version-3 leaf covers %d fields, want 32", len(obj))
	}
	for _, k := range []string{
		"model_served", "classification", "prompt_tokens",
		"completion_tokens", "total_tokens", "duration_ms",
	} {
		if _, ok := obj[k]; !ok {
			t.Errorf("version-3 leaf is missing %q", k)
		}
	}
	// Tool names were deferred rather than ridden along; see ADR 0011.
	for _, k := range []string{"tools_offered", "tools_called"} {
		if _, ok := obj[k]; ok {
			t.Errorf("version-3 leaf carries %q, which was deliberately deferred", k)
		}
	}
}

// EventColumnsV3 and scanEventRowsV3 must agree, and V3 must extend V2 rather
// than restate it.
func TestEventColumnsV3_ExtendsV2(t *testing.T) {
	if !bytes.HasPrefix([]byte(EventColumnsV3), []byte(EventColumns)) {
		t.Error("EventColumnsV3 does not begin with EventColumns, so the shared columns can drift")
	}
	for _, c := range []string{
		"model_served", "classification", "prompt_tokens",
		"completion_tokens", "total_tokens", "duration_ms",
	} {
		if !bytes.Contains([]byte(EventColumnsV3), []byte(c)) {
			t.Errorf("EventColumnsV3 is missing %q", c)
		}
	}
}

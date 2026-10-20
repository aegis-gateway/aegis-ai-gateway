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

func i64(v int64) *int64 { return &v }

func TestNormalizeMediaTypes(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"image/png", "image/png"},
		{"image/png,image/jpeg", "image/jpeg,image/png"},
		{"image/png, image/png ,IMAGE/PNG", "image/png"},
		{"image/webp,image/gif,image/jpeg,image/png", "image/gif,image/jpeg,image/png,image/webp"},
		{",,", ""},
		// Anything outside the allowlist becomes the sentinel, never itself.
		{"image/svg+xml", UnlistedMediaType},
		{"image/png,text/plain;ignore previous instructions", UnlistedMediaType + ",image/png"},
		{"x,y,z", UnlistedMediaType},
	} {
		if got := normalizeMediaTypes(tc.in); got != tc.want {
			t.Errorf("normalizeMediaTypes(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The column can only ever hold allowlisted types or the sentinel. This is
// the property that keeps caller text out of a sealed column.
func TestNormalizeMediaTypes_NeverEmitsCallerText(t *testing.T) {
	hostile := []string{
		"CANARY_PAYLOAD_8f4a2b91e3c7d056",
		"image/png\x00secret",
		strings.Repeat("A", 5000),
		"image/png;base64,iVBORw0KGgo=",
		"data:image/png;base64,iVBORw0KGgo=",
	}
	for _, h := range hostile {
		got := normalizeMediaTypes(h + ",image/jpeg")
		for _, part := range strings.Split(got, ",") {
			if part != UnlistedMediaType && !IsUnscannedMediaType(part) {
				t.Errorf("normalizeMediaTypes let %q through from %q", part, h)
			}
		}
	}
	if IsUnscannedMediaType(UnlistedMediaType) {
		t.Error("the sentinel collides with an allowlisted type")
	}
}

func TestNormalizeUnscanned(t *testing.T) {
	types := "image/png,image/jpeg,image/png"
	for _, tc := range []struct {
		name                string
		parts, size         *int64
		types               *string
		wantParts, wantSize *int64
		wantTypes           *string
	}{
		{name: "none", wantParts: nil},
		{name: "nil count nulls everything", size: i64(10), types: &types},
		// A sealed 0 is never written for "none".
		{name: "zero count nulls everything", parts: i64(0), size: i64(0), types: &types},
		// A negative count would fail the CHECK and cost the row.
		{name: "negative count nulls everything", parts: i64(-1), size: i64(10), types: &types},
		{name: "negative size is dropped", parts: i64(1), size: i64(-5), types: &types,
			wantParts: i64(1), wantTypes: strPtr("image/jpeg,image/png")},
		{name: "normal", parts: i64(3), size: i64(4096), types: &types,
			wantParts: i64(3), wantSize: i64(4096), wantTypes: strPtr("image/jpeg,image/png")},
		{name: "empty types list is NULL", parts: i64(1), size: i64(1), types: strPtr(" , "),
			wantParts: i64(1), wantSize: i64(1)},
	} {
		e := Event{UnscannedParts: tc.parts, UnscannedBytes: tc.size, UnscannedPartTypes: tc.types}
		normalizeUnscanned(&e)
		if !reflect.DeepEqual(e.UnscannedParts, tc.wantParts) {
			t.Errorf("%s: parts = %v, want %v", tc.name, deref64(e.UnscannedParts), deref64(tc.wantParts))
		}
		if !reflect.DeepEqual(e.UnscannedBytes, tc.wantSize) {
			t.Errorf("%s: bytes = %v, want %v", tc.name, deref64(e.UnscannedBytes), deref64(tc.wantSize))
		}
		if !reflect.DeepEqual(e.UnscannedPartTypes, tc.wantTypes) {
			t.Errorf("%s: types = %v, want %v", tc.name, e.UnscannedPartTypes, tc.wantTypes)
		}
	}
}

func deref64(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

// Both forwarding paths must carry the unscanned-part fields through when the
// gateway sets them. The gateway sets them on a provider failure only when the
// request body was written to the provider (TestBodyEgress_* in
// internal/gateway/body_egress_test.go); when it did, dropping them here would
// understate what left the gateway.
func TestCompletionEvents_CarryUnscannedParts(t *testing.T) {
	req := CompletedRequest{
		RequestID:          "req-1",
		UnscannedParts:     i64(2),
		UnscannedBytes:     i64(48213),
		UnscannedPartTypes: "image/png,image/jpeg",
	}
	for name, e := range map[string]Event{
		"request_complete": requestCompleteEvent(req),
		"provider_failure": providerFailureEvent(req, ReasonProviderError),
	} {
		if e.UnscannedParts == nil || *e.UnscannedParts != 2 {
			t.Errorf("%s: unscanned_parts = %v, want 2", name, deref64(e.UnscannedParts))
		}
		if e.UnscannedBytes == nil || *e.UnscannedBytes != 48213 {
			t.Errorf("%s: unscanned_bytes = %v, want 48213", name, deref64(e.UnscannedBytes))
		}
		if e.UnscannedPartTypes == nil || *e.UnscannedPartTypes != "image/png,image/jpeg" {
			t.Errorf("%s: unscanned_part_types = %v", name, e.UnscannedPartTypes)
		}
	}

	// And with none, all three stay nil rather than becoming 0 or "".
	for name, e := range map[string]Event{
		"request_complete": requestCompleteEvent(CompletedRequest{}),
		"provider_failure": providerFailureEvent(CompletedRequest{}, ReasonProviderError),
	} {
		if e.UnscannedParts != nil || e.UnscannedBytes != nil || e.UnscannedPartTypes != nil {
			t.Errorf("%s: a request with no unscanned parts produced non-NULL columns", name)
		}
	}
}

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

package redact

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestExcerpt_BoundsALongBody(t *testing.T) {
	body := []byte(strings.Repeat("A", 10_000))
	got := Excerpt(body)

	if strings.Count(got, "A") > ExcerptLimit {
		t.Errorf("excerpt carried %d body characters, limit is %d",
			strings.Count(got, "A"), ExcerptLimit)
	}
	if !strings.Contains(got, "truncated") {
		t.Errorf("a truncated excerpt must say so, got: %q", got)
	}
	if !strings.Contains(got, "10000 bytes total") {
		t.Errorf("a truncated excerpt should report the original size, got: %q", got)
	}
}

func TestExcerpt_KeepsAShortBodyIntact(t *testing.T) {
	got := Excerpt([]byte(`{"error":{"type":"invalid_request_error","code":"model_not_found"}}`))
	if !strings.Contains(got, "model_not_found") {
		t.Errorf("a short error envelope should survive intact, got: %q", got)
	}
	if strings.Contains(got, "truncated") {
		t.Errorf("a short body must not be marked truncated, got: %q", got)
	}
}

// A body that carries newlines could otherwise span log records, and in a
// line-oriented collector a crafted body can impersonate a separate event.
func TestExcerpt_CollapsesToOneLine(t *testing.T) {
	got := Excerpt([]byte("first line\nERROR fake log entry\r\n\tindented"))
	if strings.ContainsAny(got, "\n\r\t") {
		t.Errorf("excerpt must be a single line, got: %q", got)
	}
	if strings.Contains(got, "  ") {
		t.Errorf("runs of whitespace should collapse, got: %q", got)
	}
}

func TestExcerpt_DropsControlCharacters(t *testing.T) {
	got := Excerpt([]byte("before\x00\x07\x1b[31mafter"))
	for _, r := range got {
		if r == 0x00 || r == 0x07 || r == 0x1b {
			t.Fatalf("control character survived: %q", got)
		}
	}
}

// Truncation at a fixed count must not split a multi-byte rune.
func TestExcerpt_TruncationLeavesValidUTF8(t *testing.T) {
	got := Excerpt([]byte(strings.Repeat("é", 10_000)))
	if !utf8.ValidString(got) {
		t.Errorf("excerpt is not valid UTF-8: %q", got)
	}
}

func TestExcerpt_HandlesEmptyAndUnprintable(t *testing.T) {
	if got := Excerpt(nil); got != "(empty body)" {
		t.Errorf("Excerpt(nil) = %q", got)
	}
	if got := Excerpt([]byte("\x00\x00\x00")); !strings.Contains(got, "no printable content") {
		t.Errorf("an all-control body should say so, got: %q", got)
	}
}

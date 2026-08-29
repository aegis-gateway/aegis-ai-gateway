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

package adapters

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// fakeAWSKey is AWS's own documentation example key, which matches the
// AKIA[0-9A-Z]{16} pattern in internal/filter/secrets/patterns.go.
const fakeAWSKey = "AKIAIOSFODNN7EXAMPLE"

// TestRedactProviderError_TruncatesLongBodies is the test the bound exists for.
//
// A provider error body is unbounded text the gateway does not control. Before
// this, the whole thing went into a log record.
func TestRedactProviderError_TruncatesLongBodies(t *testing.T) {
	t.Parallel()

	const bodyLen = 20000
	body := []byte(`{"error":{"message":"` + strings.Repeat("x", bodyLen) + `"}}`)

	got := RedactProviderError(body)

	if len(got) >= len(body) {
		t.Fatalf("excerpt is %d bytes for a %d byte body; it was not truncated",
			len(got), len(body))
	}
	// The excerpt is the retained prefix plus the truncation notice. The
	// prefix itself must not exceed the bound.
	prefix, _, found := strings.Cut(got, " [truncated,")
	if !found {
		t.Fatalf("a truncated excerpt does not say so: %q", got)
	}
	if len(prefix) > MaxProviderErrorExcerpt {
		t.Errorf("retained prefix is %d bytes, want at most %d",
			len(prefix), MaxProviderErrorExcerpt)
	}
	// A reader has to know how much they are not seeing.
	if !strings.Contains(got, "bytes total") {
		t.Errorf("the truncation notice does not state the full length: %q", got)
	}
	if !strings.Contains(got, "20000") && !strings.Contains(got, "2002") {
		t.Errorf("the truncation notice does not carry the body length: %q", got)
	}
	if !strings.Contains(got, `{"error"`) {
		t.Errorf("the retained prefix lost the error envelope shape: %q", got)
	}
}

// TestRedactProviderError_ShortBodyIsKept confirms the bound does not mangle
// the ordinary case. A provider error envelope is small, and an operator
// diagnosing a rejection needs it.
func TestRedactProviderError_ShortBodyIsKept(t *testing.T) {
	t.Parallel()

	body := []byte(`{"error":{"type":"invalid_request_error","code":"model_not_found"}}`)
	got := RedactProviderError(body)

	if got != string(body) {
		t.Errorf("a body within the bound was altered:\n got %q\nwant %q", got, string(body))
	}
}

// TestRedactProviderError_RedactsSecrets covers the reason a provider error
// body is dangerous rather than merely large: a provider that rejects a request
// commonly quotes it back, and what it quotes back can be the credential.
func TestRedactProviderError_RedactsSecrets(t *testing.T) {
	t.Parallel()

	body := []byte(`{"error":{"message":"invalid value ` + fakeAWSKey + ` supplied"}}`)
	got := RedactProviderError(body)

	if strings.Contains(got, fakeAWSKey) {
		t.Errorf("the secret survived redaction: %q", got)
	}
	if !strings.Contains(got, "[redacted:") {
		t.Errorf("the excerpt does not record that something was redacted: %q", got)
	}
	// The surrounding envelope is still readable, or the redaction has cost
	// the diagnostic value the excerpt exists for.
	if !strings.Contains(got, "invalid value") {
		t.Errorf("redaction removed more than the secret: %q", got)
	}
}

// TestRedactProviderError_SecretStraddlingTheBoundIsRedacted pins the ordering
// of redaction and truncation. Truncating first would clip a secret in half and
// emit the surviving prefix.
func TestRedactProviderError_SecretStraddlingTheBoundIsRedacted(t *testing.T) {
	t.Parallel()

	// Place the key so that it begins inside the retained prefix and ends
	// past it.
	lead := strings.Repeat("y", MaxProviderErrorExcerpt-10)
	body := []byte(lead + fakeAWSKey + strings.Repeat("z", 100))

	got := RedactProviderError(body)

	if strings.Contains(got, fakeAWSKey[:10]) {
		t.Errorf("a secret straddling the truncation boundary was emitted in part: %q", got)
	}
}

// TestRedactProviderError_CollapsesControlCharacters covers log forging. The
// body reaches a JSON slog handler, and an embedded newline is how one record
// is made to look like two.
func TestRedactProviderError_CollapsesControlCharacters(t *testing.T) {
	t.Parallel()

	body := []byte("line one\nline two\r\tand a \x00 null")
	got := RedactProviderError(body)

	for _, bad := range []string{"\n", "\r", "\t", "\x00"} {
		if strings.Contains(got, bad) {
			t.Errorf("control character %q survived: %q", bad, got)
		}
	}
	if !strings.Contains(got, "line one line two") {
		t.Errorf("collapsing control characters lost the text: %q", got)
	}
}

// TestRedactProviderError_TruncatesOnARuneBoundary asserts the excerpt is
// always valid UTF-8. A log handler that re-encodes invalid UTF-8 produces
// replacement characters, and a value that changes shape on its way to the log
// is a value an operator cannot match against anything.
func TestRedactProviderError_TruncatesOnARuneBoundary(t *testing.T) {
	t.Parallel()

	// Multi-byte runes throughout, so the naive cut lands mid-rune for at
	// least some offsets.
	for pad := 0; pad < 8; pad++ {
		body := []byte(strings.Repeat("a", pad) + strings.Repeat("é", 400))
		got := RedactProviderError(body)
		if !utf8.ValidString(got) {
			t.Errorf("pad %d: excerpt is not valid UTF-8: %q", pad, got)
		}
	}
}

// TestRedactProviderError_EmptyBody covers the case where a provider returns a
// status and nothing else, so the log line says that rather than nothing.
func TestRedactProviderError_EmptyBody(t *testing.T) {
	t.Parallel()

	if got := RedactProviderError(nil); got != "<empty body>" {
		t.Errorf("nil body rendered as %q", got)
	}
	if got := RedactProviderError([]byte{}); got != "<empty body>" {
		t.Errorf("empty body rendered as %q", got)
	}
}

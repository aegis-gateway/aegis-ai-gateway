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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestSchemaLimitsMatchMigration fails if the constants in limits.go and the
// column widths in the migrations disagree.
//
// Two numbers describing one limit will drift, and the failure is silent in the
// direction that matters: Go clips to the larger number, PostgreSQL rejects the
// row, and the audit event is lost with only a log line. This reads the
// migrations rather than trusting a comment.
func TestSchemaLimitsMatchMigration(t *testing.T) {
	sql := readMigrations(t)

	// ALTER TABLE audit_events ALTER COLUMN <name> TYPE VARCHAR(<n>)
	alter := regexp.MustCompile(`(?is)ALTER\s+TABLE\s+audit_events\s+ALTER\s+COLUMN\s+(\w+)\s+TYPE\s+VARCHAR\((\d+)\)`)
	found := map[string]int{}
	for _, m := range alter.FindAllStringSubmatch(sql, -1) {
		n, err := strconv.Atoi(m[2])
		if err != nil {
			t.Fatalf("unparseable width for %s: %v", m[1], err)
		}
		// Later migrations win, which matches the order they are applied.
		found[strings.ToLower(m[1])] = n
	}

	want := map[string]int{
		"ip_address":    MaxIPAddress,
		"error_message": MaxErrorMessage,
		"user_agent":    MaxUserAgent,
	}
	for col, limit := range want {
		got, ok := found[col]
		if !ok {
			t.Errorf("no migration sets a VARCHAR width for audit_events.%s; limits.go declares %d", col, limit)
			continue
		}
		if got != limit {
			t.Errorf("audit_events.%s: migration says VARCHAR(%d), limits.go says %d", col, got, limit)
		}
	}

	// The metadata CHECK must agree with MaxMetadataBytes for the same reason.
	check := regexp.MustCompile(`(?is)pg_column_size\(metadata\)\s*<=\s*(\d+)`)
	m := check.FindStringSubmatch(sql)
	if m == nil {
		t.Fatalf("no pg_column_size(metadata) CHECK found in migrations; limits.go declares MaxMetadataBytes=%d", MaxMetadataBytes)
	}
	got, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("unparseable metadata bound: %v", err)
	}
	if got != MaxMetadataBytes {
		t.Errorf("metadata CHECK says %d bytes, limits.go says %d", got, MaxMetadataBytes)
	}
}

// TestClipNeverExceedsLimit is the property the database depends on. A value
// longer than the column, in bytes or in runes, must come back within the limit.
func TestClipNeverExceedsLimit(t *testing.T) {
	limits := []int{1, 2, 3, 4, MaxIPAddress, MaxErrorMessage, MaxUserAgent}
	inputs := []string{
		"",
		"short",
		strings.Repeat("a", 1000),
		strings.Repeat("é", 1000), // 2 bytes per rune
		strings.Repeat("😀", 1000), // 4 bytes per rune, outside the BMP
		strings.Repeat("日本語", 400),
	}
	for _, limit := range limits {
		for _, in := range inputs {
			out := clip(in, limit)
			if n := utf8.RuneCountInString(out); n > limit {
				t.Errorf("clip(%d runes, %d) returned %d runes", utf8.RuneCountInString(in), limit, n)
			}
			if !utf8.ValidString(out) {
				t.Errorf("clip(%q..., %d) produced invalid UTF-8", in[:min(len(in), 12)], limit)
			}
		}
	}
}

// TestClipMetadataStaysUnderByteLimit covers the JSONB CHECK. Whatever goes in,
// the serialized result must fit, and it must never come back nil: an audit row
// noting that metadata was dropped beats no audit row.
func TestClipMetadataStaysUnderByteLimit(t *testing.T) {
	cases := []map[string]interface{}{
		nil,
		{},
		{"filter_type": "secrets", "reason": "Request blocked: detected 1 secret(s) of type: AWS Access Key"},
		{"error": strings.Repeat("x", 100_000)},
		{"reason": strings.Repeat("😀", 50_000), "operation": "rate_limit_check"},
		{"limit": 60, "dimension": "rpm"},
	}
	for i, in := range cases {
		out := clipMetadata(in)
		encoded, err := json.Marshal(out)
		if err != nil {
			t.Fatalf("case %d: marshal: %v", i, err)
		}
		if len(encoded) > MaxMetadataBytes {
			t.Errorf("case %d: serialized metadata is %d bytes, limit is %d", i, len(encoded), MaxMetadataBytes)
		}
	}
}

// TestClipMarksTruncation guards the reader-facing half: a truncated value has
// to be distinguishable from a value that was simply short.
func TestClipMarksTruncation(t *testing.T) {
	out := clip(strings.Repeat("a", 500), MaxErrorMessage)
	if !strings.HasSuffix(out, "...") {
		t.Errorf("a truncated value should end in an ellipsis, got %q", out[max(0, len(out)-8):])
	}
	short := "Redis unavailable - failed closed"
	if clip(short, MaxErrorMessage) != short {
		t.Error("a value within the limit must be returned unchanged")
	}
}

// TestRemoteAddrFitsIPAddressColumn pins the bug migration 012 repairs. Go's
// RemoteAddr is host:port, and for IPv6 the host is bracketed, so the widest
// value the gateway can pass is longer than the 45 characters the column
// originally allowed.
func TestRemoteAddrFitsIPAddressColumn(t *testing.T) {
	widest := "[0000:0000:0000:0000:0000:ffff:255.255.255.255]:65535"
	if len(widest) <= 45 {
		t.Fatalf("test fixture is wrong: %d characters", len(widest))
	}
	if got := utf8.RuneCountInString(clip(widest, MaxIPAddress)); got > MaxIPAddress {
		t.Errorf("clipped RemoteAddr is %d characters, column allows %d", got, MaxIPAddress)
	}
	if clip(widest, MaxIPAddress) != widest {
		t.Errorf("the widest real RemoteAddr should survive unclipped at %d, got %q", MaxIPAddress, clip(widest, MaxIPAddress))
	}
}

func readMigrations(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("..", "..", "migrations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	var b strings.Builder
	var ups int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".up.sql") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		b.WriteString(string(data))
		b.WriteString("\n")
		ups++
	}
	if ups == 0 {
		t.Fatal("no .up.sql migrations found; this test would pass vacuously")
	}
	fmt.Fprintf(os.Stderr, "read %d up migrations\n", ups)
	return b.String()
}

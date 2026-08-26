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
	// ADD COLUMN <name> VARCHAR(<n>), from migration 013.
	addRe := regexp.MustCompile(`(?is)ADD\s+COLUMN\s+(\w+)\s+VARCHAR\((\d+)\)`)
	added := map[string]int{}
	for _, m := range addRe.FindAllStringSubmatch(sql, -1) {
		n, err := strconv.Atoi(m[2])
		if err != nil {
			t.Fatalf("unparseable width for %s: %v", m[1], err)
		}
		added[strings.ToLower(m[1])] = n
	}

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

	// The columns migration 013 promoted must match too, for the same reason.
	for col, limit := range map[string]int{
		"api_key_prefix":  MaxAPIKeyPrefix,
		"limit_dimension": MaxLimitDimension,
		"filter_type":     MaxFilterType,
		"reason":          MaxReason,
		"provider":        MaxProvider,
		"model":           MaxModel,
		"mode":            MaxMode,
		"operation":       MaxOperation,
		"error_detail":    MaxErrorDetail,
	} {
		got, ok := added[col]
		if !ok {
			t.Errorf("no migration adds audit_events.%s; limits.go declares %d", col, limit)
			continue
		}
		if got != limit {
			t.Errorf("audit_events.%s: migration says VARCHAR(%d), limits.go says %d", col, got, limit)
		}
	}

	// metadata must be gone: it is the column this whole change removes, and a
	// migration that quietly left it would defeat the point.
	if regexp.MustCompile(`(?i)DROP\s+COLUMN\s+metadata`).FindString(sql) == "" {
		t.Error("no migration drops audit_events.metadata")
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

// TestClipPtrPreservesNil covers the distinction the detail columns depend on:
// nil means the event carries no such detail, "" would mean it carries an empty
// one, and clipping must not turn the first into the second.
func TestClipPtrPreservesNil(t *testing.T) {
	if clipPtr(nil, MaxReason) != nil {
		t.Error("clipPtr(nil) must stay nil")
	}
	empty := ""
	if got := clipPtr(&empty, MaxReason); got == nil || *got != "" {
		t.Error("clipPtr of an empty string must stay an empty string, not nil")
	}
	long := strings.Repeat("x", 5000)
	got := clipPtr(&long, MaxReason)
	if got == nil {
		t.Fatal("clipPtr of a long string must not return nil")
	}
	if utf8.RuneCountInString(*got) > MaxReason {
		t.Errorf("clipPtr returned %d runes, limit is %d", utf8.RuneCountInString(*got), MaxReason)
	}
}

// TestPolicyDenyReasonFits pins the width that decided MaxReason. Rego joins the
// deny set with concat("; ", deny), so the string grows with the number of rules
// that fire.
func TestPolicyDenyReasonFits(t *testing.T) {
	one := `Request denied by policy: RESTRICTED data cannot be routed through alias "aegis-fast": it is not cleared for RESTRICTED`
	two := one + "; restricted term detected: project ironwood; financial topic restricted to finance team"
	for _, s := range []string{one, two} {
		if utf8.RuneCountInString(s) > MaxReason {
			t.Errorf("a real deny reason is %d characters, MaxReason is %d", utf8.RuneCountInString(s), MaxReason)
		}
	}
	// Why 512 and not the 128 the original plan proposed. A single shipped rule
	// already fills most of 128, and two rules exceed it outright, so 128 would
	// have started rejecting audit writes the first time two policies denied the
	// same request.
	if n := utf8.RuneCountInString(one); n > 128 {
		t.Errorf("fixture drifted: expected the single-rule reason to fit in 128 with little room, got %d", n)
	} else if 128-n > 32 {
		t.Errorf("fixture drifted: the single-rule reason was 119 characters, leaving 9 of headroom in 128; now it leaves %d", 128-n)
	}
	if utf8.RuneCountInString(two) <= 128 {
		t.Error("fixture is wrong: the two-rule reason is the case that rules out varchar(128)")
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

// TestClipReturnsValidUTF8 covers the substitution PostgreSQL forces on us.
//
// An invalid byte sequence is rejected outright ("invalid byte sequence for
// encoding UTF8"), so a value carrying one costs the audit row. Before the
// detail columns existed these values went through json.Marshal on their way
// into the JSONB, which substituted U+FFFD; promoting them to columns removed
// that step, so clip has to do it.
func TestClipReturnsValidUTF8(t *testing.T) {
	cases := []string{
		"sk-\xc3",                       // truncated two-byte sequence
		"\xff\xfe\xfd",                  // never valid
		strings.Repeat("\xe4\xbd", 500), // truncated three-byte sequences, over the limit
		"valid éé text",
	}
	for _, in := range cases {
		for _, limit := range []int{1, 8, MaxUserAgent} {
			out := clip(in, limit)
			if !utf8.ValidString(out) {
				t.Errorf("clip(%q, %d) returned invalid UTF-8: %q", in, limit, out)
			}
			if utf8.RuneCountInString(out) > limit {
				t.Errorf("clip(%q, %d) returned %d runes", in, limit, utf8.RuneCountInString(out))
			}
		}
	}
}

// TestTruncateAPIKeyIsRuneSafe pins the path that made this matter. The "key" on
// an auth failure is whatever the caller sent, so slicing it at a byte offset
// can split a character and produce a value the database refuses, costing the
// audit row that records the failure.
func TestTruncateAPIKeyIsRuneSafe(t *testing.T) {
	for _, in := range []string{
		"sk-ééééékey",
		"\U0001F600\U0001F600\U0001F600\U0001F600\U0001F600\U0001F600",
		"sk-ascii-key-1234567890",
		"short",
	} {
		got := truncateAPIKey(in)
		if !utf8.ValidString(got) {
			t.Errorf("truncateAPIKey(%q) produced invalid UTF-8: %q", in, got)
		}
	}
}

// TestMigrationGuardsArePresent fails if either migration loses the check that
// makes it safe.
//
// This is a structural test, not a behavioural one, and it is worth being clear
// about the difference. It cannot tell you the guards work; the three branches
// of each were verified by hand against PostgreSQL 16 and are recorded in the
// migration comments. What it catches is the realistic regression: someone
// tidying a migration, seeing a DO block that "does nothing" on their machine
// because their database is empty, and deleting it.
//
// Both guards protect the same thing from opposite directions. 012 refuses to
// truncate a hashed column on a row that is already sealed; 013 refuses to drop
// a hashed column while any checkpoint that hashes it exists. Losing either one
// turns an upgrade into silent, permanent damage to the audit chain, which is
// exactly the damage the chain exists to make visible.
func TestMigrationGuardsArePresent(t *testing.T) {
	for _, want := range []struct {
		file   string
		needle string
		why    string
	}{
		{
			file:   "012_bound_audit_text_columns.up.sql",
			needle: "refusing to truncate",
			why:    "012 must refuse to truncate user_agent or error_message on a sealed row: both are in the leaf hash",
		},
		{
			file:   "012_bound_audit_text_columns.up.sql",
			needle: "audit_checkpoints",
			why:    "012's guard has to consult audit_checkpoints to know which rows are attested",
		},
		{
			file:   "013_promote_audit_metadata.up.sql",
			needle: "refusing to drop audit_events.metadata",
			why:    "013 must refuse while version-1 checkpoints exist: a version-1 leaf cannot be recomputed without metadata",
		},
		{
			file:   "013_promote_audit_metadata.down.sql",
			needle: "refusing to restore audit_events.metadata",
			why:    "013's down must refuse while version-2 checkpoints exist, for the mirror-image reason",
		},
	} {
		data, err := os.ReadFile(filepath.Join("..", "..", "migrations", want.file))
		if err != nil {
			t.Errorf("read %s: %v", want.file, err)
			continue
		}
		if !strings.Contains(string(data), want.needle) {
			t.Errorf("%s no longer contains %q.\n%s", want.file, want.needle, want.why)
		}
		if !strings.Contains(string(data), "RAISE EXCEPTION") {
			t.Errorf("%s has no RAISE EXCEPTION: a guard that does not abort is not a guard", want.file)
		}
	}
}

// TestMigration012WidthsAgreeThroughout reconciles every copy of the bounded
// widths inside migration 012.
//
// Each bounded column states its width in three places in that file: the
// ALTER's VARCHAR(n), the left(col, n) in the same statement's USING clause, and
// the char_length(col) > n threshold in the section-0 guard. Add the Go constant
// and that is four copies of one number, and the existing tests reconcile only
// the first and the last.
//
// The dangerous drift is the guard. If a width is tightened but the threshold is
// left higher, a sealed row between the two numbers passes the guard, USING
// left() rewrites it, and because user_agent and error_message are both in the
// leaf hash, verify-chain reports the result as tampering. That is the exact
// damage section 0 exists to prevent, reintroduced by an inconsistent edit.
//
// The left() argument drifting is less dangerous, because a mismatch there makes
// PostgreSQL fail the ALTER loudly, but it is the same class and costs nothing to
// pin here.
func TestMigration012WidthsAgreeThroughout(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "migrations", "012_bound_audit_text_columns.up.sql"))
	if err != nil {
		t.Fatalf("read migration 012: %v", err)
	}
	sql := string(data)

	for _, col := range []struct {
		name string
		want int
	}{
		{"user_agent", MaxUserAgent},
		{"error_message", MaxErrorMessage},
	} {
		alterRe := regexp.MustCompile(`(?is)ALTER\s+COLUMN\s+` + col.name + `\s+TYPE\s+VARCHAR\((\d+)\)`)
		usingRe := regexp.MustCompile(`(?is)left\(\s*` + col.name + `\s*,\s*(\d+)\s*\)`)
		guardRe := regexp.MustCompile(`(?is)char_length\(\s*\w+\.` + col.name + `\s*\)\s*>\s*(\d+)`)

		for _, site := range []struct {
			what string
			re   *regexp.Regexp
		}{
			{"ALTER ... TYPE VARCHAR(n)", alterRe},
			{"USING left(col, n)", usingRe},
			{"section-0 guard char_length(col) > n", guardRe},
		} {
			m := site.re.FindStringSubmatch(sql)
			if m == nil {
				t.Errorf("%s: no %s found in migration 012; limits.go declares %d", col.name, site.what, col.want)
				continue
			}
			got, err := strconv.Atoi(m[1])
			if err != nil {
				t.Errorf("%s: unparseable width in %s: %v", col.name, site.what, err)
				continue
			}
			if got != col.want {
				t.Errorf("%s: %s says %d, limits.go says %d. Every copy of this width must agree, and the guard copy is the one whose drift is silent.",
					col.name, site.what, got, col.want)
			}
		}
	}
}

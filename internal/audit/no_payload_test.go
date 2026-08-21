// Package audit_test contains conformance tests that enforce AEGIS's
// zero-retention guarantee: no request payload (prompt text, completion text,
// or any user-supplied content) may reach the audit trail.
//
// Run unit tests (Tests 1 and 2):
//
//	go test ./internal/audit/ -run NoPayload -v
//
// Run integration tests (Test 3):
//
//	TEST_DATABASE_URL=postgres://... TEST_SERVER_URL=http://... TEST_API_KEY=... \
//	  go test ./internal/audit/ -run NoPayload -v -tags integration
//
// Cited in README and docs/ARCHITECTURE.md.
package audit_test

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/filter"
)

// payloadColumnPattern matches column or field names that suggest stored
// message content (exact-name match; anchored so "error_message" is safe).
var payloadColumnPattern = regexp.MustCompile(
	`(?i)^(prompt|response|content|body|text|message|input|output|completion|payload|sample|excerpt|raw)$`,
)

// sqlColumnDeclPattern extracts the leading identifier from a DDL column line,
// e.g. "    column_name  TYPE …" → "column_name".
var sqlColumnDeclPattern = regexp.MustCompile(`^\s+(\w+)\s+\w`)

// TestNoPayload_SchemaIntrospection parses the *.up.sql files for audit_logs
// (migration 002) and audit_events (migration 005) and fails if any column
// name matches a set of payload-indicative words.
//
// Allowed columns include metadata names such as "error_message", "user_agent",
// "filter_results", "event_type", etc. — none of which match the anchored
// pattern below.
//
// This test does NOT connect to a database; it reads files from disk.
func TestNoPayload_SchemaIntrospection(t *testing.T) {
	t.Parallel()

	migrations := map[string]string{
		"002_create_audit_logs.up.sql":   "../../migrations/002_create_audit_logs.up.sql",
		"005_create_audit_events.up.sql": "../../migrations/005_create_audit_events.up.sql",
	}

	for filename, path := range migrations {
		t.Run(filename, func(t *testing.T) {
			t.Parallel()

			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("cannot read migration %s: %v", path, err)
			}

			for _, line := range strings.Split(string(data), "\n") {
				m := sqlColumnDeclPattern.FindStringSubmatch(line)
				if m == nil {
					continue
				}
				colName := strings.ToLower(m[1])
				if payloadColumnPattern.MatchString(colName) {
					t.Errorf("%s: column %q looks like it may hold payload content (line: %q)",
						filename, m[1], strings.TrimSpace(line))
				}
			}
		})
	}
}

// payloadFieldNames is the set of filter.Result field names that would indicate
// the struct carries matched user content rather than only metadata.
var payloadFieldNames = map[string]bool{
	"MatchedText": true,
	"Content":     true,
	"Payload":     true,
	"RawInput":    true,
	"RawOutput":   true,
	"Sample":      true,
	"Excerpt":     true,
	"Body":        true,
	"Text":        true,
	"Prompt":      true,
	"Response":    true,
	"Input":       true,
	"Output":      true,
	"Completion":  true,
	"MatchedString": true,
}

// TestNoPayload_FilterResultStruct uses reflection to confirm that
// filter.Result (and any struct types embedded in it) carries only metadata
// and no fields whose name or JSON tag suggests they hold payload content.
//
// The "Message" field is intentionally allowed: it carries a short human-
// readable reason for the filter decision (e.g. "AWS key pattern detected"),
// not a copy of user-supplied text.
//
// This test does NOT connect to a database.
func TestNoPayload_FilterResultStruct(t *testing.T) {
	t.Parallel()

	visited := make(map[reflect.Type]bool)
	assertNoPayloadFields(t, reflect.TypeOf(filter.Result{}), visited)

	t.Logf("filter.Result struct inspection passed — no payload-holding field names found")
}

func assertNoPayloadFields(t *testing.T, typ reflect.Type, visited map[reflect.Type]bool) {
	t.Helper()

	if typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct || visited[typ] {
		return
	}
	visited[typ] = true

	for i := range typ.NumField() {
		field := typ.Field(i)

		if payloadFieldNames[field.Name] {
			t.Errorf("type %s: field %q may hold payload content; "+
				"filter.Result must carry only metadata (action, filter name, reason, detections, score)",
				typ.Name(), field.Name)
		}

		// Check json struct tags for suspicious names.
		tag := field.Tag.Get("json")
		tagName := strings.Split(tag, ",")[0]
		if tagName != "" && tagName != "-" && payloadColumnPattern.MatchString(strings.ToLower(tagName)) {
			t.Errorf("type %s: field %q has json tag %q that may expose payload content",
				typ.Name(), field.Name, tagName)
		}

		// Recurse into embedded / nested struct types.
		ft := field.Type
		if ft.Kind() == reflect.Ptr {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct {
			assertNoPayloadFields(t, ft, visited)
		}
	}
}

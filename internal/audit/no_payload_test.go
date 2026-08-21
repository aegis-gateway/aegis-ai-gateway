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
	"path/filepath"
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

// auditTablePattern matches a CREATE TABLE or ALTER TABLE statement targeting
// one of the audit tables, capturing the statement kind and the table name.
var auditTablePattern = regexp.MustCompile(
	`(?i)\b(CREATE\s+TABLE|ALTER\s+TABLE)\s+(?:IF\s+NOT\s+EXISTS\s+)?(audit_\w+)`)

// alterAddColumnPattern extracts the column name from an ALTER TABLE ... ADD
// clause. PostgreSQL treats COLUMN as optional — "ALTER TABLE t ADD payload
// TEXT" is valid DDL — so requiring the keyword would let exactly the column
// this test guards against slip through.
var alterAddColumnPattern = regexp.MustCompile(
	`(?i)\bADD\s+(?:COLUMN\s+)?(?:IF\s+NOT\s+EXISTS\s+)?(\w+)`)

// addNonColumnKeywords are the ADD forms that introduce table constraints
// rather than columns. Without COLUMN required, the pattern above also matches
// these, and their first word would otherwise be treated as a column name.
var addNonColumnKeywords = map[string]bool{
	"constraint": true, "primary": true, "foreign": true,
	"unique": true, "check": true, "exclude": true,
}

// TestNoPayload_SchemaIntrospection scans *every* up migration for columns added
// to an audit table and fails if any name matches a payload-indicative word.
//
// It deliberately does not hardcode migration 002 and 005: a later migration
// such as "ALTER TABLE audit_logs ADD COLUMN payload TEXT" would slip past a
// fixed list, which is exactly the regression this test exists to prevent.
//
// Allowed columns include metadata names such as "error_message", "user_agent",
// "filter_results", "event_type" — none of which match the anchored pattern.
//
// This test does NOT connect to a database; it reads files from disk.
func TestNoPayload_SchemaIntrospection(t *testing.T) {
	t.Parallel()

	paths, err := filepath.Glob("../../migrations/*.up.sql")
	if err != nil {
		t.Fatalf("globbing migrations: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no up migrations found — the schema guarantee is unverified")
	}

	var auditMigrations int
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("cannot read migration %s: %v", path, err)
		}
		filename := filepath.Base(path)

		// Walk statement by statement so a migration touching several tables
		// only has its audit-table statements inspected.
		for _, stmt := range strings.Split(string(data), ";") {
			m := auditTablePattern.FindStringSubmatch(stmt)
			if m == nil {
				continue
			}
			auditMigrations++
			kind, table := strings.ToUpper(strings.Fields(m[1])[0]), m[2]

			var cols []string
			if kind == "CREATE" {
				for _, line := range strings.Split(stmt, "\n") {
					if c := sqlColumnDeclPattern.FindStringSubmatch(line); c != nil {
						cols = append(cols, c[1])
					}
				}
			} else {
				for _, c := range alterAddColumnPattern.FindAllStringSubmatch(stmt, -1) {
					if addNonColumnKeywords[strings.ToLower(c[1])] {
						continue
					}
					cols = append(cols, c[1])
				}
			}

			for _, col := range cols {
				if payloadColumnPattern.MatchString(strings.ToLower(col)) {
					t.Errorf("%s: %s %s adds column %q, which looks like it may hold "+
						"payload content — the audit trail must store metadata only",
						filename, kind, table, col)
				}
			}
		}
	}

	if auditMigrations == 0 {
		t.Fatal("scanned migrations but found no audit-table statements — " +
			"the test is not actually inspecting the audit schema")
	}
	t.Logf("inspected %d audit-table statement(s) across %d migration file(s)", auditMigrations, len(paths))
}

// payloadFieldNames is the set of filter.Result field names that would indicate
// the struct carries matched user content rather than only metadata.
var payloadFieldNames = map[string]bool{
	"MatchedText":   true,
	"Content":       true,
	"Payload":       true,
	"RawInput":      true,
	"RawOutput":     true,
	"Sample":        true,
	"Excerpt":       true,
	"Body":          true,
	"Text":          true,
	"Prompt":        true,
	"Response":      true,
	"Input":         true,
	"Output":        true,
	"Completion":    true,
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

	if typ.Kind() == reflect.Pointer {
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
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct {
			assertNoPayloadFields(t, ft, visited)
		}
	}
}

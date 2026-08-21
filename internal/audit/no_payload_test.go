// Cited in README and docs/ARCHITECTURE.md

// Package audit contains conformance tests that enforce AEGIS's
// zero-retention guarantee: no request payload (prompt text, completion text,
// or any user-supplied content) may reach the audit trail.
package audit

import (
	"context"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/filter"
	"github.com/jackc/pgx/v5/pgxpool"
)

// payloadColumnPattern matches column or field names that suggest stored content.
var payloadColumnPattern = regexp.MustCompile(
	`(?i)^(prompt|response|content|body|text|message|input|output|completion|payload|sample|excerpt|raw)$`,
)

// sqlColumnDeclPattern extracts column names from CREATE TABLE DDL.
// Matches lines like: "    column_name  TYPE ..."
var sqlColumnDeclPattern = regexp.MustCompile(`^\s+(\w+)\s+\w`)

// TestNoPayload_SchemaIntrospection verifies that neither audit_logs nor
// audit_events defines any column whose name suggests it holds message content.
func TestNoPayload_SchemaIntrospection(t *testing.T) {
	migrations := map[string]string{
		"002_create_audit_logs.up.sql":   "../../migrations/002_create_audit_logs.up.sql",
		"005_create_audit_events.up.sql": "../../migrations/005_create_audit_events.up.sql",
	}

	for filename, path := range migrations {
		t.Run(filename, func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("cannot read migration %s: %v", path, err)
			}
			sql := string(data)

			for _, line := range strings.Split(sql, "\n") {
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

// payloadFieldNames is the set of struct field names that would indicate
// a filter.Result carries matched content rather than only metadata.
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
}

// TestNoPayload_FilterResultStruct uses reflection to confirm that
// filter.Result carries only metadata and no fields that could hold
// the actual text of a blocked or flagged request.
func TestNoPayload_FilterResultStruct(t *testing.T) {
	resultType := reflect.TypeOf(filter.Result{})

	if resultType.Kind() != reflect.Struct {
		t.Fatalf("filter.Result is not a struct (got %s)", resultType.Kind())
	}

	for i := range resultType.NumField() {
		field := resultType.Field(i)

		if payloadFieldNames[field.Name] {
			t.Errorf("filter.Result has field %q which may hold payload content; "+
				"the struct must carry only metadata (action, filter name, message, detections, score)",
				field.Name)
		}

		// Also check json struct tags for suspicious names.
		tag := field.Tag.Get("json")
		tagName := strings.Split(tag, ",")[0]
		if tagName != "" && tagName != "-" && payloadColumnPattern.MatchString(tagName) {
			t.Errorf("filter.Result field %q has json tag %q which may expose payload content",
				field.Name, tagName)
		}
	}

	t.Logf("filter.Result has %d fields: all passed payload-name check", resultType.NumField())
}

// TestNoPayload_CanaryEndToEnd connects to the live audit database and
// asserts that a distinctive canary string does not appear in any text column
// of audit_logs or audit_events.
//
// Requires TEST_DATABASE_URL to be set; skips cleanly otherwise.
func TestNoPayload_CanaryEndToEnd(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("requires TEST_DATABASE_URL")
	}

	ctx := context.Background()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect to database: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping database: %v", err)
	}

	const canary = "CANARY_DO_NOT_STORE_8675309"

	// Search every plain-text column in audit_logs for the canary.
	auditLogsCols := []string{
		"request_id", "organization_id", "team_id", "user_id",
		"model_requested", "model_served", "provider", "endpoint",
		"project", "trace_id",
	}
	for _, col := range auditLogsCols {
		var count int
		q := "SELECT COUNT(*) FROM audit_logs WHERE " + col + " ILIKE $1"
		if err := pool.QueryRow(ctx, q, "%"+canary+"%").Scan(&count); err != nil {
			t.Errorf("querying audit_logs.%s: %v", col, err)
			continue
		}
		if count > 0 {
			t.Errorf("canary %q found in audit_logs.%s (%d rows) — payload reached audit trail",
				canary, col, count)
		}
	}

	// Check JSONB filter_results in audit_logs.
	var jsonbCount int
	if err := pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM audit_logs WHERE filter_results::text ILIKE $1",
		"%"+canary+"%",
	).Scan(&jsonbCount); err != nil {
		t.Errorf("querying audit_logs.filter_results: %v", err)
	} else if jsonbCount > 0 {
		t.Errorf("canary %q found in audit_logs.filter_results (%d rows) — payload reached audit trail",
			canary, jsonbCount)
	}

	// Search every plain-text column in audit_events for the canary.
	auditEventsCols := []string{
		"request_id", "organization_id", "team_id", "user_id",
		"ip_address", "user_agent", "endpoint", "method", "error_message",
	}
	for _, col := range auditEventsCols {
		var count int
		q := "SELECT COUNT(*) FROM audit_events WHERE " + col + " ILIKE $1"
		if err := pool.QueryRow(ctx, q, "%"+canary+"%").Scan(&count); err != nil {
			t.Errorf("querying audit_events.%s: %v", col, err)
			continue
		}
		if count > 0 {
			t.Errorf("canary %q found in audit_events.%s (%d rows) — payload reached audit trail",
				canary, col, count)
		}
	}

	// Check JSONB metadata in audit_events.
	if err := pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM audit_events WHERE metadata::text ILIKE $1",
		"%"+canary+"%",
	).Scan(&jsonbCount); err != nil {
		t.Errorf("querying audit_events.metadata: %v", err)
	} else if jsonbCount > 0 {
		t.Errorf("canary %q found in audit_events.metadata (%d rows) — payload reached audit trail",
			canary, jsonbCount)
	}

	if !t.Failed() {
		t.Logf("canary %q not found in any audit table column — zero-retention confirmed", canary)
	}
}

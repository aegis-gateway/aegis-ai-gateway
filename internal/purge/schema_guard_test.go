package purge

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// auditTimeColumn is the column the purge queries filter on. It must exist in
// the shipped audit table migrations.
const auditTimeColumn = "timestamp"

// TestPurgeTimeColumnExistsInMigrations guards against the failure this package
// already shipped once: the purge queries filtered on `created_at` while both
// audit tables define `timestamp`, so every purge, dry-run and age-gauge query
// failed against a migrated database. The integration tests did not catch it
// because their fixture hand-rolled tables carrying `created_at`.
//
// This test reads the real migrations, needs no database, and therefore runs in
// the normal test job rather than only when TEST_DATABASE_URL is set.
func TestPurgeTimeColumnExistsInMigrations(t *testing.T) {
	t.Parallel()

	paths, err := filepath.Glob("../../migrations/*.up.sql")
	if err != nil || len(paths) == 0 {
		t.Fatalf("cannot locate migrations: %v", err)
	}

	// Collect every column declared on the audit tables the purger touches.
	colDecl := regexp.MustCompile(`^\s+"?(\w+)"?\s+\w`)
	tableStmt := regexp.MustCompile(`(?i)\bCREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(audit_logs|audit_events)\b`)

	cols := map[string]map[string]bool{}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, stmt := range strings.Split(string(data), ";") {
			m := tableStmt.FindStringSubmatch(stmt)
			if m == nil {
				continue
			}
			table := strings.ToLower(m[1])
			if cols[table] == nil {
				cols[table] = map[string]bool{}
			}
			for _, line := range strings.Split(stmt, "\n") {
				if c := colDecl.FindStringSubmatch(line); c != nil {
					cols[table][strings.ToLower(c[1])] = true
				}
			}
		}
	}

	for _, table := range []string{"audit_logs", "audit_events"} {
		if len(cols[table]) == 0 {
			t.Fatalf("no CREATE TABLE found for %s across the migrations", table)
		}
		if !cols[table][auditTimeColumn] {
			t.Errorf("%s has no %q column; the purge queries filter on it and would fail at runtime",
				table, auditTimeColumn)
		}
		if cols[table]["created_at"] {
			t.Errorf("%s declares created_at — if the schema moved, the purge queries "+
				"and this guard must move with it", table)
		}
	}
}

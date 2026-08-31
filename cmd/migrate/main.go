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

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/audit/checkpoint"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/config"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/purge"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	// Subcommand dispatch. First arg selects the command; fall back to legacy
	// flag-based invocation when the first arg looks like a flag or is absent.
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "seal":
			runSeal(os.Args[2:])
			return
		case "verify-chain":
			runVerifyChain(os.Args[2:])
			return
		case "purge":
			runPurge(os.Args[2:])
			return
		case "submit":
			runSubmit(os.Args[2:])
			return
		case "audit-keys":
			runAuditKeys(os.Args[2:])
			return
		case "up", "down":
			runMigrate(os.Args[1], os.Args[2:])
			return
		}
	}
	// Legacy: original flag-based invocation (-direction up/down).
	runMigrateLegacy()
}

// runMigrate handles 'aegis-migrate up [flags]' and 'aegis-migrate down [flags]'.
func runMigrate(direction string, args []string) {
	fs := flag.NewFlagSet("migrate-"+direction, flag.ExitOnError)
	steps := fs.Int("steps", 0, "number of steps (0 = all)")
	dbURL := fs.String("db-url", "", "database URL (overrides env)")
	migrationsPath := fs.String("path", "migrations", "path to migrations directory")
	if err := fs.Parse(args); err != nil {
		log.Fatalf("parse flags: %v", err)
	}

	dsn := resolveDSN(*dbURL)
	m, err := migrate.New("file://"+*migrationsPath, dsn)
	if err != nil {
		log.Fatalf("failed to create migrator: %v", err)
	}
	defer func() { _, _ = m.Close() }()

	var migrateErr error
	switch direction {
	case "up":
		if *steps > 0 {
			migrateErr = m.Steps(*steps)
		} else {
			migrateErr = m.Up()
		}
	case "down":
		if *steps > 0 {
			migrateErr = m.Steps(-*steps)
		} else {
			migrateErr = m.Down()
		}
	}

	if migrateErr != nil && migrateErr != migrate.ErrNoChange {
		log.Fatalf("migration failed: %v", migrateErr)
	}

	v, dirty, _ := m.Version()
	fmt.Printf("migration %s complete (version: %d, dirty: %v)\n", direction, v, dirty)
}

// runMigrateLegacy preserves the original flag-based invocation style so that
// existing scripts using '-direction up' continue to work unchanged.
func runMigrateLegacy() {
	direction := flag.String("direction", "up", "migration direction: up or down")
	steps := flag.Int("steps", 0, "number of steps (0 = all)")
	dbURL := flag.String("db-url", "", "database URL (overrides env)")
	migrationsPath := flag.String("path", "migrations", "path to migrations directory")
	flag.Parse()

	dsn := resolveDSN(*dbURL)
	m, err := migrate.New("file://"+*migrationsPath, dsn)
	if err != nil {
		log.Fatalf("failed to create migrator: %v", err)
	}
	defer func() { _, _ = m.Close() }()

	switch *direction {
	case "up":
		if *steps > 0 {
			err = m.Steps(*steps)
		} else {
			err = m.Up()
		}
	case "down":
		if *steps > 0 {
			err = m.Steps(-*steps)
		} else {
			err = m.Down()
		}
	default:
		log.Fatalf("invalid direction: %s (use 'up' or 'down')", *direction)
	}

	if err != nil && err != migrate.ErrNoChange {
		log.Fatalf("migration failed: %v", err)
	}

	v, dirty, _ := m.Version()
	fmt.Printf("migration %s complete (version: %d, dirty: %v)\n", *direction, v, dirty)
}

// runSeal handles 'aegis-migrate seal [flags]'.
// It acquires a pg_advisory_lock (single-writer), seals events into Merkle
// checkpoints, and exits when caught up.
func runSeal(args []string) {
	fs := flag.NewFlagSet("seal", flag.ExitOnError)
	sinceEvent := fs.Int64("since-event", 0, "start from event ID N (0 = all history)")
	batchSize := fs.Int("batch-size", 10000, "events per checkpoint")
	lagSeconds := fs.Int("lag-seconds", checkpoint.DefaultLagSeconds, "safety window: only seal events older than this many seconds")
	dbURL := fs.String("db-url", "", "database URL (overrides env)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: aegis-migrate seal [flags]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Seals audit_events into Merkle checkpoints (RFC 6962).")
		fmt.Fprintln(os.Stderr, "Acquires pg_advisory_lock — run only one instance at a time.")
		fmt.Fprintln(os.Stderr, "")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		log.Fatalf("seal: parse flags: %v", err)
	}

	dsn := resolveDSN(*dbURL)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalf("seal: connect to database: %v", err)
	}
	defer pool.Close()

	opts := checkpoint.SealOptions{
		SinceEvent: *sinceEvent,
		BatchSize:  *batchSize,
		LagSeconds: checkpoint.SealLag(*lagSeconds),
	}

	if err := checkpoint.RunSeal(ctx, pool, opts); err != nil {
		log.Fatalf("seal: %v", err)
	}
}

// runVerifyChain handles 'aegis-migrate verify-chain [flags]'.
// It walks the checkpoint chain and verifies every checkpoint_hash. With --full
// it re-hashes retained event rows against stored Merkle roots.
func runVerifyChain(args []string) {
	fs := flag.NewFlagSet("verify-chain", flag.ExitOnError)
	full := fs.Bool("full", false, "re-hash retained event rows against Merkle roots (slow)")
	fromCP := fs.Int64("from-checkpoint", 0, "start verification from checkpoint N (0 = all)")
	toCP := fs.Int64("to-checkpoint", 0, "end verification at checkpoint M (0 = all)")
	eventID := fs.Int64("event", 0, "produce inclusion proof for event ID E")
	outputFmt := fs.String("output-format", "text", "output format: json or text")
	fs.StringVar(outputFmt, "output", "text", "alias for --output-format")
	dbURL := fs.String("db-url", "", "database URL (overrides env)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: aegis-migrate verify-chain [flags]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Verifies the audit checkpoint chain.")
		fmt.Fprintln(os.Stderr, "")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		log.Fatalf("verify-chain: parse flags: %v", err)
	}

	asJSON := *outputFmt == "json"

	dsn := resolveDSN(*dbURL)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalf("verify-chain: connect to database: %v", err)
	}
	defer pool.Close()

	opts := checkpoint.VerifyOptions{
		Full:           *full,
		FromCheckpoint: *fromCP,
		ToCheckpoint:   *toCP,
		EventID:        *eventID,
		OutputJSON:     asJSON,
	}

	result, proof, err := checkpoint.RunVerify(ctx, pool, opts)
	if err != nil {
		log.Fatalf("verify-chain: %v", err)
	}

	if proof != nil {
		printProof(proof, asJSON)
		return
	}

	printResult(result, asJSON)

	if len(result.Anomalies) > 0 {
		os.Exit(2)
	}
}

func printResult(r *checkpoint.VerifyResult, asJSON bool) {
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		type output struct {
			CheckpointsVerified int       `json:"checkpoints_verified"`
			EventsCovered       int64     `json:"events_covered"`
			SealedAtStart       time.Time `json:"sealed_at_start,omitempty"`
			SealedAtEnd         time.Time `json:"sealed_at_end,omitempty"`
			UnsealedEvents      int64     `json:"unsealed_events"`
			Anomalies           []string  `json:"anomalies"`
		}
		o := output{
			CheckpointsVerified: r.CheckpointsVerified,
			EventsCovered:       r.EventsCovered,
			UnsealedEvents:      r.UnsealedCount,
			Anomalies:           r.Anomalies,
		}
		if r.CheckpointsVerified > 0 {
			o.SealedAtStart = r.SealedAtRange[0]
			o.SealedAtEnd = r.SealedAtRange[1]
		}
		enc.Encode(o) //nolint:errcheck
		return
	}

	fmt.Printf("checkpoints verified: %d\n", r.CheckpointsVerified)
	fmt.Printf("events covered:       %d\n", r.EventsCovered)
	if r.CheckpointsVerified > 0 {
		fmt.Printf("sealed_at range:      %s – %s\n",
			r.SealedAtRange[0].Format(time.RFC3339),
			r.SealedAtRange[1].Format(time.RFC3339))
	}
	fmt.Printf("unsealed events:      %d\n", r.UnsealedCount)
	if len(r.Anomalies) == 0 {
		fmt.Println("result:               OK")
	} else {
		fmt.Printf("result:               FAIL (%d anomaly/anomalies)\n", len(r.Anomalies))
		for _, a := range r.Anomalies {
			fmt.Printf("  ANOMALY: %s\n", a)
		}
	}
}

func printProof(p *checkpoint.InclusionProofResult, asJSON bool) {
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(p) //nolint:errcheck
		return
	}

	fmt.Printf("inclusion proof for event %d:\n", p.EventID)
	fmt.Printf("  checkpoint_id:         %d\n", p.CheckpointID)
	fmt.Printf("  checkpoint_hash:       %s\n", p.CheckpointHash)
	fmt.Printf("  merkle_root:           %s\n", p.MerkleRoot)
	fmt.Printf("  hash_schema_version:   %d\n", p.HashSchemaVersion)
	fmt.Printf("  canonicalization_spec: %s\n", p.CanonicalizationSpec)
	fmt.Printf("  leaf_index:            %d\n", p.LeafIndex)
	fmt.Printf("  sibling_hashes (%d):\n", len(p.SiblingHashes))
	for i, h := range p.SiblingHashes {
		fmt.Printf("    [%d] %s\n", i, h)
	}
}

// runPurge handles 'aegis-migrate purge [flags]'.
// It deletes audit_events rows older than --before and writes
// an audit_purges record for every run (including dry runs).
func runPurge(args []string) {
	fs := flag.NewFlagSet("purge", flag.ExitOnError)
	beforeStr := fs.String("before", "", "delete rows with timestamp < DATE (ISO 8601, required)")
	dryRun := fs.Bool("dry-run", false, "print counts and ID ranges without deleting; writes dry_run=true row to audit_purges")
	tableStr := fs.String("table", "both", "table(s) to purge: audit_events | both (audit_logs was dropped by migration 017)")
	dbURL := fs.String("db-url", "", "database URL (overrides DATABASE_URL env)")
	configDir := fs.String("config", "configs", "configuration directory (for audit.retention_days)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: aegis-migrate purge [flags]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Deletes audit rows outside the retention window. Always run with --dry-run first.")
		fmt.Fprintln(os.Stderr, "Every run writes a row to audit_purges — including dry runs.")
		fmt.Fprintln(os.Stderr, "")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		log.Fatalf("purge: parse flags: %v", err)
	}

	if *beforeStr == "" {
		log.Fatal("purge: --before is required (ISO 8601 date or datetime, e.g. 2025-01-01)")
	}

	before, err := parsePurgeDate(*beforeStr)
	if err != nil {
		log.Fatalf("purge: invalid --before %q: %v", *beforeStr, err)
	}

	// Enforce the configured retention floor. audit.retention_days is
	// documented as a minimum that purge honours; without this check the flag
	// could delete rows well inside the window the policy promises to keep,
	// and the operator would have no indication anything was wrong.
	cfg, cfgErr := loadPurgeConfig(*configDir)
	if cfgErr != nil {
		// Fall back to the built-in default rather than dropping the floor. A
		// wrong --config path or an unexpected cwd must not be the difference
		// between honouring the retention policy and deleting inside it.
		cfg = config.DefaultConfig()
		log.Printf("purge: warning: could not load %s/gateway.yaml (%v); "+
			"enforcing the built-in default retention floor of %d days",
			*configDir, cfgErr, cfg.Audit.RetentionDays)
	}
	if cfg.Audit.RetentionDays > 0 {
		floor := time.Now().UTC().AddDate(0, 0, -cfg.Audit.RetentionDays)
		if before.After(floor) {
			log.Fatalf("purge: --before %s is inside the configured retention window "+
				"(audit.retention_days = %d, earliest permitted --before is %s). "+
				"Lower retention_days if this is intended.",
				before.Format(time.RFC3339), cfg.Audit.RetentionDays, floor.Format(time.RFC3339))
		}
	}

	var tbl purge.Table
	switch *tableStr {
	case "audit_logs":
		// Accepted so the refusal in purge.Run explains itself, rather than
		// failing here as an unknown flag value.
		tbl = purge.TableAuditLogs
	case "audit_events":
		tbl = purge.TableAuditEvents
	case "both", "":
		tbl = purge.TableBoth
	default:
		log.Fatalf("purge: invalid --table %q: use audit_events or both", *tableStr)
	}

	dsn := resolveDSN(*dbURL)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalf("purge: connect to database: %v", err)
	}
	defer pool.Close()

	opts := purge.Options{
		Before:  before,
		DryRun:  *dryRun,
		Table:   tbl,
		BatchSz: 1000,
	}

	if *dryRun {
		fmt.Printf("dry-run: querying rows with timestamp < %s in table(s): %s\n", before.Format(time.RFC3339), tbl)
	} else {
		fmt.Printf("purge: deleting rows with timestamp < %s from table(s): %s\n", before.Format(time.RFC3339), tbl)
	}

	result, err := purge.Run(ctx, pool, opts)
	if err != nil {
		log.Fatalf("purge: %v", err)
	}

	label := "deleted"
	if result.DryRun {
		label = "would delete"
	}
	fmt.Printf("purge complete:\n")
	fmt.Printf("  rows %s:          %d\n", label, result.RowsDeleted)
	fmt.Printf("  event_id range:      [%d, %d]\n", result.IDMin, result.IDMax)
	fmt.Printf("  affected checkpoints: %v\n", result.CheckpointIDs)
	fmt.Printf("  dry_run:             %v\n", result.DryRun)
	fmt.Printf("  audit_purges row written\n")
}

// parsePurgeDate accepts ISO 8601 date (2006-01-02) or datetime (RFC3339) strings.
func parsePurgeDate(s string) (time.Time, error) {
	for _, f := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised format; use YYYY-MM-DD or RFC3339")
}

func resolveDSN(override string) string {
	if override != "" {
		return override
	}
	if v := os.Getenv("DATABASE_URL"); v != "" {
		return v
	}
	host := envOrDefault("DB_HOST", "localhost")
	port := envOrDefault("DB_PORT", "5432")
	user := envOrDefault("DB_USER", "aegis")
	pass := envOrDefault("DB_PASSWORD", "aegis-dev")
	name := envOrDefault("DB_NAME", "aegis")
	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", user, pass, host, port, name)
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// loadPurgeConfig reads gateway.yaml for the audit retention policy. It is
// deliberately narrow: purge only needs audit.retention_days, and a missing or
// unreadable config must not silently disable the floor.
func loadPurgeConfig(dir string) (*config.Config, error) {
	cfg := config.DefaultConfig()
	if err := config.LoadFile(dir+"/gateway.yaml", cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// runAuditKeys reports API keys whose model allowlist is empty.
//
// An empty allowlist does not mean "no access": modelAllowed returns true for a
// zero-length list, so such a key may use every configured model, including any
// added later. keygen wrote that value unconditionally until 2026-08-30, so
// every key issued before then is unrestricted.
//
// No migration can fix them, which is why this is a report rather than a repair:
// an empty allowlist left by the old keygen is byte-identical to one an operator
// chose deliberately, and the database cannot tell them apart. Only someone who
// knows what a key is for can say which it is.
//
// Read-only by construction. It issues SELECTs and nothing else, so it is safe
// to run against a production database.
func runAuditKeys(args []string) {
	fs := flag.NewFlagSet("audit-keys", flag.ExitOnError)
	dbURL := fs.String("db-url", "", "database URL (overrides env)")
	org := fs.String("org", "", "restrict the report to one organization")
	includeInactive := fs.Bool("include-inactive", false,
		"also list revoked and expired keys, which cannot authenticate")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: aegis-migrate audit-keys [flags]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Lists API keys that may use EVERY configured model.")
		fmt.Fprintln(os.Stderr, "An empty allowed_models is no restriction, not no access.")
		fmt.Fprintln(os.Stderr, "Read-only: issues SELECTs and nothing else.")
		fmt.Fprintln(os.Stderr, "")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		log.Fatalf("audit-keys: parse flags: %v", err)
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, resolveDSN(*dbURL))
	if err != nil {
		log.Fatalf("audit-keys: connect: %v", err)
	}
	defer pool.Close()

	report, err := AuditKeys(ctx, pool, *org, *includeInactive)
	if err != nil {
		log.Fatalf("audit-keys: %v", err)
	}
	total, unrestricted, restricted := report.Active, report.Unrestricted, report.Restricted

	fmt.Println("=== API keys that may use every configured model ===")
	fmt.Println()
	for i, k := range report.Keys {
		if i == 0 {
			fmt.Printf("  %-22s %-24s %-16s %-14s %-10s %-12s %s\n",
				"KEY PREFIX", "NAME", "ORG", "TEAM", "STATUS", "CREATED", "EXPIRES")
		}
		fmt.Printf("  %-22s %-24s %-16s %-14s %-10s %-12s %s\n",
			k.Prefix, truncate(k.Name, 24), truncate(k.Org, 16), truncate(k.Team, 14), k.Status,
			k.Created.UTC().Format("2006-01-02"), k.Expires.UTC().Format("2006-01-02"))
	}
	if len(report.Keys) == 0 {
		fmt.Println("  none")
	}
	fmt.Println()
	fmt.Printf("  active keys:        %d\n", total)
	fmt.Printf("  unrestricted:       %d\n", unrestricted)
	fmt.Printf("  restricted:         %d\n", restricted)
	fmt.Println()

	if unrestricted == 0 {
		fmt.Println("  Every active key names the models it may use.")
		return
	}
	fmt.Println("  An empty allowed_models permits EVERY configured model, including any")
	fmt.Println("  added later. Keys issued before 2026-08-30 have it because keygen wrote")
	fmt.Println("  it unconditionally; it is indistinguishable from a deliberate grant-all,")
	fmt.Println("  so each one needs a human decision.")
	fmt.Println()
	fmt.Println("  To restrict one:")
	fmt.Println("    UPDATE api_keys SET allowed_models = '[\"aegis-fast\"]'::jsonb")
	fmt.Println("     WHERE key_prefix = '<prefix>';")
	fmt.Println()
	fmt.Println("  The value must be a JSON array of strings; migration 015 rejects anything")
	fmt.Println("  else, and a rejected UPDATE leaves the key exactly as it was.")

	// Non-zero so a scheduled run is visible in CI or cron without parsing text.
	os.Exit(2)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "\u2026"
}

// UnrestrictedKey is one API key whose allowlist is empty.
type UnrestrictedKey struct {
	Prefix, Name, Org, Team, User, Status string
	Created, Expires                      time.Time
}

// KeyAuditReport is what audit-keys found.
//
// Active is every active key in scope, not just the listed ones, because
// "12 unrestricted" means little without knowing whether the estate is 13 keys
// or 1,300.
type KeyAuditReport struct {
	Active       int64
	Unrestricted int64
	Restricted   int64
	Keys         []UnrestrictedKey
}

// AuditKeys reports the keys whose allowed_models is empty, which permits every
// configured model.
//
// Separate from the command so the query can be tested without a process exit.
// Read-only: SELECTs and nothing else, so it is safe against a production
// database.
func AuditKeys(ctx context.Context, pool *pgxpool.Pool, org string, includeInactive bool) (*KeyAuditReport, error) {
	rep := &KeyAuditReport{}

	// The counts are always over ACTIVE keys, whatever the listing includes: a
	// revoked key cannot authenticate, so counting it as unrestricted would
	// overstate the exposure the report exists to measure.
	if err := pool.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE allowed_models = '[]'::jsonb),
		       count(*) FILTER (WHERE allowed_models <> '[]'::jsonb)
		  FROM api_keys
		 WHERE status = 'active' AND expires_at > NOW()
		   AND ($1 = '' OR organization_id = $1)
	`, org).Scan(&rep.Active, &rep.Unrestricted, &rep.Restricted); err != nil {
		return nil, fmt.Errorf("counting: %w", err)
	}

	statusFilter := "status = 'active' AND expires_at > NOW()"
	if includeInactive {
		statusFilter = "TRUE"
	}
	rows, err := pool.Query(ctx, `
		SELECT key_prefix, name, organization_id, team_id,
		       coalesce(user_id, '-'), status, created_at, expires_at
		  FROM api_keys
		 WHERE allowed_models = '[]'::jsonb
		   AND (`+statusFilter+`)
		   AND ($1 = '' OR organization_id = $1)
		 ORDER BY created_at
	`, org)
	if err != nil {
		return nil, fmt.Errorf("listing: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var k UnrestrictedKey
		if err := rows.Scan(&k.Prefix, &k.Name, &k.Org, &k.Team, &k.User,
			&k.Status, &k.Created, &k.Expires); err != nil {
			return nil, fmt.Errorf("scanning: %w", err)
		}
		rep.Keys = append(rep.Keys, k)
	}
	return rep, rows.Err()
}

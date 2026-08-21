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
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/audit"
)

func main() {
	if len(os.Args) < 2 {
		runMigrate(os.Args[1:])
		return
	}
	switch os.Args[1] {
	case "seal":
		runSeal(os.Args[2:])
	case "verify-chain":
		runVerify(os.Args[2:])
	case "help", "-h", "--help":
		usage()
	default:
		// Preserve legacy behavior: bare flags run the migrator.
		runMigrate(os.Args[1:])
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `aegis-migrate — database migrations and audit sealing

Usage:
  aegis-migrate [flags]                run schema migrations
  aegis-migrate seal [flags]           seal audit_events into audit_checkpoints
  aegis-migrate verify-chain [flags]   verify the audit checkpoint chain

See docs/AUDIT-INTEGRITY.md for the checkpoint design.`)
}

// runMigrate is the original migrate behavior.
func runMigrate(args []string) {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	direction := fs.String("direction", "up", "migration direction: up or down")
	steps := fs.Int("steps", 0, "number of steps (0 = all)")
	dbURL := fs.String("db-url", "", "database URL (overrides env)")
	migrationsPath := fs.String("path", "migrations", "path to migrations directory")
	_ = fs.Parse(args)

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

func runSeal(args []string) {
	fs := flag.NewFlagSet("seal", flag.ExitOnError)
	sinceEvent := fs.Int64("since-event", 0, "start from event ID N")
	batchSize := fs.Int("batch-size", 10000, "events per checkpoint")
	lagSeconds := fs.Int("lag-seconds", 300, "only seal events older than this many seconds")
	dbURL := fs.String("db-url", "", "database URL (overrides env)")
	_ = fs.Parse(args)

	dsn := resolveDSN(*dbURL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalf("seal: connect: %v", err)
	}
	defer pool.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	sealer := audit.NewSealer(pool, logger, audit.SealerOptions{
		SinceEvent: *sinceEvent,
		BatchSize:  *batchSize,
		LagWindow:  time.Duration(*lagSeconds) * time.Second,
	})
	res, err := sealer.Run(ctx)
	if err != nil {
		if errors.Is(err, audit.ErrLockUnavailable) {
			fmt.Fprintln(os.Stderr, "seal: another sealer is running; exiting")
			os.Exit(2)
		}
		log.Fatalf("seal: %v", err)
	}
	fmt.Printf("seal: %d checkpoint(s), %d events sealed", res.CheckpointsCreated, res.EventsSealed)
	if res.CheckpointsCreated > 0 {
		fmt.Printf(" (events %d..%d)", res.FirstEventID, res.LastEventID)
	}
	fmt.Println()
}

func runVerify(args []string) {
	fs := flag.NewFlagSet("verify-chain", flag.ExitOnError)
	full := fs.Bool("full", false, "also re-hash retained events per range")
	from := fs.Int64("from-checkpoint", 0, "start at checkpoint id")
	to := fs.Int64("to-checkpoint", 0, "stop at checkpoint id")
	eventID := fs.Int64("event", 0, "event id to emit inclusion proof for")
	output := fs.String("output", "text", "output format: text|json")
	dbURL := fs.String("db-url", "", "database URL (overrides env)")
	_ = fs.Parse(args)

	dsn := resolveDSN(*dbURL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalf("verify-chain: connect: %v", err)
	}
	defer pool.Close()

	res, err := audit.Verify(ctx, pool, audit.VerifyOptions{
		Full:           *full,
		FromCheckpoint: *from,
		ToCheckpoint:   *to,
		EventID:        *eventID,
	})
	if err != nil {
		log.Fatalf("verify-chain: %v", err)
	}

	switch *output {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(res); err != nil {
			log.Fatalf("verify-chain: encode: %v", err)
		}
	default:
		printVerifyText(res)
	}
	if !res.ChainOK || (res.FullOK != nil && !*res.FullOK) {
		os.Exit(1)
	}
}

func printVerifyText(r *audit.VerifyResult) {
	fmt.Printf("checkpoints examined: %d\n", r.CheckpointsExamined)
	fmt.Printf("chain_ok: %v\n", r.ChainOK)
	if r.Detail != "" {
		fmt.Printf("detail: %s\n", r.Detail)
	}
	if r.FailedAt != nil {
		fmt.Printf("failed_at_checkpoint: %d\n", *r.FailedAt)
	}
	if r.FullOK != nil {
		fmt.Printf("full_ok: %v\n", *r.FullOK)
	}
	for _, s := range r.Ranges {
		fmt.Printf("  checkpoint %d: %s", s.CheckpointID, s.Status)
		if s.Detail != "" {
			fmt.Printf(" — %s", s.Detail)
		}
		fmt.Println()
	}
	if r.Proof != nil {
		p := r.Proof
		fmt.Printf("inclusion proof for event %d:\n", p.EventID)
		fmt.Printf("  checkpoint_id:         %d\n", p.CheckpointID)
		fmt.Printf("  checkpoint_hash:       %s\n", p.CheckpointHash)
		fmt.Printf("  merkle_root:           %s\n", p.MerkleRoot)
		fmt.Printf("  leaf_hash:             %s\n", p.LeafHash)
		fmt.Printf("  hash_schema_version:   %d\n", p.HashSchemaVersion)
		fmt.Printf("  canonicalization_spec: %s\n", p.CanonicalizationSpec)
		for i, s := range p.Siblings {
			fmt.Printf("  step %d [%s]: %s\n", i, p.Directions[i], s)
		}
	}
}

func resolveDSN(override string) string {
	if override != "" {
		return override
	}
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		return dsn
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

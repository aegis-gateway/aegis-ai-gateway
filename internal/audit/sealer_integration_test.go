// Copyright 2026 Atlantic Frontier Corporations LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package audit

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func setupIntegrationDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	// Ensure a clean slate for this test's ranges. We only clear rows the
	// checkpoint sealer will see; we do NOT drop tables shared with other
	// suites.
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `TRUNCATE audit_checkpoints RESTART IDENTITY`); err != nil {
		t.Fatalf("truncate audit_checkpoints: %v (is the migration applied?)", err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE audit_events RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate audit_events: %v", err)
	}
	return pool
}

func insertEvent(t *testing.T, pool *pgxpool.Pool, requestID string, ts time.Time) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(context.Background(), `
		INSERT INTO audit_events (
			request_id, timestamp, event_type, organization_id, team_id,
			endpoint, method, status_code, error_message, metadata
		) VALUES ($1, $2, 'auth_success', 'org-1', 'team-1',
			'/v1/chat/completions', 'POST', 200, '', '{"note":"ok"}')
		RETURNING id
	`, requestID, ts).Scan(&id)
	if err != nil {
		t.Fatalf("insert audit_event: %v", err)
	}
	return id
}

func TestCheckpointIntegration_EmptyTableSealIsNoOp(t *testing.T) {
	pool := setupIntegrationDB(t)
	sealer := NewSealer(pool, slog.Default(), SealerOptions{
		BatchSize: 100,
		LagWindow: 0, // no lag window for tests
	})
	res, err := sealer.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.CheckpointsCreated != 0 || res.EventsSealed != 0 {
		t.Fatalf("expected no-op on empty table, got %+v", res)
	}
}

func TestCheckpointIntegration_SealThenVerifyFull(t *testing.T) {
	pool := setupIntegrationDB(t)
	past := time.Now().Add(-time.Hour)
	for i := 0; i < 5; i++ {
		insertEvent(t, pool, "req-"+time.Now().Format("150405.000000")+"-"+string(rune('a'+i)), past.Add(time.Duration(i)*time.Second))
	}

	sealer := NewSealer(pool, slog.Default(), SealerOptions{
		BatchSize: 2, LagWindow: 0,
	})
	res, err := sealer.Run(context.Background())
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if res.CheckpointsCreated == 0 || res.EventsSealed != 5 {
		t.Fatalf("expected 5 events sealed across >=1 checkpoints, got %+v", res)
	}

	v, err := Verify(context.Background(), pool, VerifyOptions{Full: true})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !v.ChainOK {
		t.Fatalf("chain not OK: %+v", v)
	}
	if v.FullOK == nil || !*v.FullOK {
		t.Fatalf("full verification failed: %+v", v)
	}
}

func TestCheckpointIntegration_TamperDetectedByFullVerify(t *testing.T) {
	pool := setupIntegrationDB(t)
	past := time.Now().Add(-time.Hour)
	for i := 0; i < 4; i++ {
		insertEvent(t, pool, "req-t-"+string(rune('a'+i)), past.Add(time.Duration(i)*time.Second))
	}

	sealer := NewSealer(pool, slog.Default(), SealerOptions{BatchSize: 100, LagWindow: 0})
	if _, err := sealer.Run(context.Background()); err != nil {
		t.Fatalf("seal: %v", err)
	}

	// Tamper: mutate a retained event's metadata.
	if _, err := pool.Exec(context.Background(),
		`UPDATE audit_events SET error_message = 'TAMPERED' WHERE id = 2`); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	v, err := Verify(context.Background(), pool, VerifyOptions{Full: true})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !v.ChainOK {
		t.Fatalf("chain fast-path should still pass (only events tampered, not checkpoints): %+v", v)
	}
	if v.FullOK == nil || *v.FullOK {
		t.Fatalf("full verification should have detected the tamper: %+v", v)
	}
}

func TestCheckpointIntegration_ConcurrentSealerExitsClean(t *testing.T) {
	pool := setupIntegrationDB(t)
	past := time.Now().Add(-time.Hour)
	for i := 0; i < 10; i++ {
		insertEvent(t, pool, "req-c-"+string(rune('a'+i)), past.Add(time.Duration(i)*time.Second))
	}

	// Hold the advisory lock manually on one connection, then run the
	// sealer — it must immediately return ErrLockUnavailable.
	conn, err := pool.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()
	var got bool
	if err := conn.QueryRow(context.Background(),
		"SELECT pg_try_advisory_lock($1)", advisoryLockKey).Scan(&got); err != nil {
		t.Fatalf("lock: %v", err)
	}
	if !got {
		t.Fatal("could not take advisory lock in test")
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", advisoryLockKey)
	}()

	sealer := NewSealer(pool, slog.Default(), SealerOptions{BatchSize: 100, LagWindow: 0})
	_, err = sealer.Run(context.Background())
	if !errors.Is(err, ErrLockUnavailable) {
		t.Fatalf("expected ErrLockUnavailable, got %v", err)
	}

	// Verify the chain was not forked: no checkpoints exist.
	var count int
	if err := pool.QueryRow(context.Background(),
		"SELECT COUNT(*) FROM audit_checkpoints").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 checkpoints (sealer never ran), got %d", count)
	}
}

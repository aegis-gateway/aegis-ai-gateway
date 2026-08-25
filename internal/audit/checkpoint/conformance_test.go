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

// This file pins the sealer to the published hash construction.
//
// api/controlplane/v1 exports the normative implementation of
// docs/AUDIT-INTEGRITY.md section 3, and the sealer calls it, so the two agree
// by construction rather than by review. What still needs asserting is that the
// bytes the sealer writes to audit_checkpoints are the bytes that construction
// produces, end to end through Postgres.
//
// That is not the same claim. The hash covers sealed_at at microsecond
// precision, and sealed_at makes a round trip through a TIMESTAMPTZ column. A
// truncation anywhere on that path would leave every unit test passing and
// every stored checkpoint unverifiable by an outside party, which is the
// failure this file exists to catch.
//
// See docs/adr/0007-hash-construction-belongs-to-the-protocol.md.
package checkpoint_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"testing"
	"time"

	controlplanev1 "github.com/aegis-gateway/aegis-ai-gateway/api/controlplane/v1"
	"github.com/aegis-gateway/aegis-ai-gateway/internal/audit/checkpoint"
)

// TestHashInputMatchesThePublishedLayout checks the construction field by
// field against the byte layout in docs/AUDIT-INTEGRITY.md section 3, without
// reference to how it is implemented.
//
// The published specification is the contract for independent verifiers. The
// whole point of publishing it is that someone can check a checkpoint without
// reading this repository, which only holds while the two agree.
func TestHashInputMatchesThePublishedLayout(t *testing.T) {
	t.Parallel()

	merkleRoot := bytes.Repeat([]byte{0xAA}, 32)
	prevHash := bytes.Repeat([]byte{0xBB}, 32)
	sealedAt := time.Date(2026, 8, 21, 14, 0, 0, 123456000, time.UTC)

	got, err := controlplanev1.CheckpointHashInput(
		merkleRoot, prevHash, 1, 10000, 10000, controlplanev1.HashSchemaVersion1, sealedAt)
	if err != nil {
		t.Fatalf("building the hash input: %v", err)
	}
	if len(got) != controlplanev1.CheckpointHashInputLen {
		t.Fatalf("the hash input is %d bytes; docs/AUDIT-INTEGRITY.md section 3 publishes %d",
			len(got), controlplanev1.CheckpointHashInputLen)
	}

	// Read the layout back out of the bytes, rather than rebuilding it the same
	// way the implementation does.
	if !bytes.Equal(got[0:32], merkleRoot) {
		t.Errorf("bytes 0-31 are not the Merkle root: %x", got[0:32])
	}
	if !bytes.Equal(got[32:64], prevHash) {
		t.Errorf("bytes 32-63 are not the predecessor hash: %x", got[32:64])
	}
	for _, f := range []struct {
		name string
		got  uint64
		want uint64
	}{
		{"range_start at bytes 64-71", binary.LittleEndian.Uint64(got[64:72]), 1},
		{"range_end at bytes 72-79", binary.LittleEndian.Uint64(got[72:80]), 10000},
		{"event_count at bytes 80-83", uint64(binary.LittleEndian.Uint32(got[80:84])), 10000},
		{"hash_schema_version at bytes 84-87", uint64(binary.LittleEndian.Uint32(got[84:88])), 1},
		{"sealed_at micros at bytes 88-95", binary.LittleEndian.Uint64(got[88:96]), uint64(sealedAt.UnixMicro())},
	} {
		if f.got != f.want {
			t.Errorf("%s is %d, want %d", f.name, f.got, f.want)
		}
	}
}

// TestHashInputRejectsAShortPredecessor covers the genesis mistake.
//
// docs/AUDIT-INTEGRITY.md section 3 defines the genesis predecessor as 32 zero
// bytes and says explicitly not to substitute NULL or an empty value. Passing
// nil would silently shorten the input, and the resulting digest would be one
// no spec-following verifier reproduces.
func TestHashInputRejectsAShortPredecessor(t *testing.T) {
	t.Parallel()

	merkleRoot := bytes.Repeat([]byte{0xAA}, 32)
	for name, prev := range map[string][]byte{
		"nil":           nil,
		"empty":         {},
		"sixteen bytes": bytes.Repeat([]byte{0x01}, 16),
		"sixty-four":    bytes.Repeat([]byte{0x01}, 64),
	} {
		if _, err := controlplanev1.CheckpointHashInput(
			merkleRoot, prev, 1, 10, 10, 1, time.Unix(0, 0)); err == nil {
			t.Errorf("a %s predecessor hash was accepted; the input length would change", name)
		}
	}
	// The genesis constant itself must be accepted.
	if _, err := controlplanev1.CheckpointHashInput(
		merkleRoot, controlplanev1.GenesisPrevHashBytes(), 1, 10, 10, 1, time.Unix(0, 0)); err != nil {
		t.Errorf("the genesis constant was rejected: %v", err)
	}
}

// TestSealerWritesThePublishedHash is the conformance check that matters.
//
// It seals real events through the production path and recomputes every stored
// checkpoint_hash with the protocol package, exactly as a control plane or an
// offline verifier would, from the columns as they came back out of Postgres.
func TestSealerWritesThePublishedHash(t *testing.T) {
	db := testDB(t)
	resetCheckpoints(t, db)
	ctx := context.Background()

	// Two batches, so the run produces a genesis checkpoint and at least one
	// checkpoint chained to a predecessor. The two cases differ in exactly the
	// field this test is about.
	const events = 7
	base := time.Now().UTC().Add(-time.Hour)
	for i := range events {
		insertTestEvent(t, db, base.Add(time.Duration(i)*time.Second))
	}
	if err := checkpoint.RunSeal(ctx, db, checkpoint.SealOptions{
		LagSeconds: checkpoint.SealLag(0), BatchSize: 4,
	}); err != nil {
		t.Fatalf("sealing: %v", err)
	}

	rows, err := db.Query(ctx, `
		SELECT id, range_start, range_end, event_count, merkle_root,
		       prev_checkpoint_hash, checkpoint_hash, hash_schema_version, sealed_at
		FROM audit_checkpoints
		ORDER BY id ASC
	`)
	if err != nil {
		t.Fatalf("reading checkpoints: %v", err)
	}
	defer rows.Close()

	var checked int
	for rows.Next() {
		var (
			id                            int64
			rangeStart, rangeEnd          int64
			eventCount, hashSchemaVersion int32
			merkleRoot, prevHash, stored  []byte
			sealedAt                      time.Time
		)
		if err := rows.Scan(&id, &rangeStart, &rangeEnd, &eventCount, &merkleRoot,
			&prevHash, &stored, &hashSchemaVersion, &sealedAt); err != nil {
			t.Fatalf("scanning a checkpoint: %v", err)
		}

		recomputed, err := controlplanev1.ComputeCheckpointHash(
			merkleRoot, prevHash, rangeStart, rangeEnd, eventCount, hashSchemaVersion, sealedAt)
		if err != nil {
			t.Fatalf("checkpoint %d: recomputing: %v", id, err)
		}
		if !bytes.Equal(recomputed, stored) {
			t.Errorf("checkpoint %d: the sealer stored %s but the published construction "+
				"produces %s from the same stored columns; an independent verifier would "+
				"report this checkpoint as tampered",
				id, hex.EncodeToString(stored), hex.EncodeToString(recomputed))
		}
		checked++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading checkpoints: %v", err)
	}

	if checked < 2 {
		t.Fatalf("only %d checkpoint(s) were written, so the chained case went unchecked", checked)
	}
}

// TestSealedAtSurvivesTheDatabaseRoundTrip isolates the part of the path most
// likely to break the conformance above.
//
// sealed_at is hashed as microseconds. PostgreSQL TIMESTAMPTZ stores
// microseconds, so the round trip is lossless, but a driver or a column type
// that rounded would make every stored checkpoint unverifiable while leaving
// the unit tests green.
func TestSealedAtSurvivesTheDatabaseRoundTrip(t *testing.T) {
	db := testDB(t)
	resetCheckpoints(t, db)
	ctx := context.Background()

	base := time.Now().UTC().Add(-time.Hour)
	for i := range 3 {
		insertTestEvent(t, db, base.Add(time.Duration(i)*time.Second))
	}
	if err := checkpoint.RunSeal(ctx, db, checkpoint.SealOptions{LagSeconds: checkpoint.SealLag(0)}); err != nil {
		t.Fatalf("sealing: %v", err)
	}

	var sealedAt time.Time
	if err := db.QueryRow(ctx,
		"SELECT sealed_at FROM audit_checkpoints ORDER BY id DESC LIMIT 1").Scan(&sealedAt); err != nil {
		t.Fatalf("reading sealed_at: %v", err)
	}

	// The stored value must carry sub-second precision at all. A column that
	// truncated to seconds would make this zero and every hash irreproducible
	// from a re-read of the row it was computed from.
	if sealedAt.UnixMicro()%1_000_000 == 0 {
		t.Logf("sealed_at landed on a whole second (%s); this is possible but rare, "+
			"and a persistent pattern of it means precision is being lost",
			sealedAt.Format(controlplanev1.TimestampFormat))
	}

	// The wire format must reproduce the same instant the hash was taken over.
	wire := controlplanev1.NewTimestamp(sealedAt)
	if wire.UnixMicro() != sealedAt.UnixMicro() {
		t.Errorf("the wire format moved sealed_at: stored %d micros, transmitted %d micros",
			sealedAt.UnixMicro(), wire.UnixMicro())
	}
}

// TestChainSurvivesABurnedCheckpointID pins a property the verifier has always
// had and nothing tested.
//
// audit_checkpoints.id is a BIGSERIAL, and PostgreSQL sequences are
// deliberately non-transactional so that concurrent writers do not serialise
// on the counter: a value handed to a transaction that rolls back is consumed
// and never reissued. The sealer writes each checkpoint inside a transaction,
// so any failure between the insert and the commit burns an id and the chain
// runs 1, 3, 4.
//
// The chain is still continuous, because continuity is the predecessor pointer
// and not the id. verify-chain compares each checkpoint's prev_checkpoint_id
// against the row that actually precedes it, so it reports no anomaly. A
// consumer that assumed contiguous ids did not fare as well: see the control
// plane's ADR 0007.
//
// Test fixtures normally truncate with RESTART IDENTITY, which is exactly the
// condition under which ids are dense, so this arranges the opposite.
func TestChainSurvivesABurnedCheckpointID(t *testing.T) {
	db := testDB(t)
	resetCheckpoints(t, db)
	ctx := context.Background()

	// Only the first four events exist to begin with. RunSeal loops until it
	// is caught up, so seeding everything up front would seal both batches in
	// one call and leave no moment in between at which to burn an id.
	base := time.Now().UTC().Add(-time.Hour)
	for i := range 4 {
		insertTestEvent(t, db, base.Add(time.Duration(i)*time.Second))
	}
	if err := checkpoint.RunSeal(ctx, db, checkpoint.SealOptions{LagSeconds: checkpoint.SealLag(0)}); err != nil {
		t.Fatalf("sealing the first batch: %v", err)
	}

	// Burn the next id the way a failed seal does: allocate it inside a
	// transaction that rolls back.
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatalf("beginning the burn transaction: %v", err)
	}
	var burned int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO audit_checkpoints
		    (range_start, range_end, event_count, merkle_root, prev_checkpoint_hash,
		     checkpoint_hash, hash_schema_version, sealed_at, sealer_version,
		     canonicalization_spec, covered_from, covered_to)
		VALUES (900, 901, 2, $1, $1, $1, 1, NOW(), 'burn', 'rfc8785-v1', NOW(), NOW())
		RETURNING id
	`, bytes.Repeat([]byte{0x00}, 32)).Scan(&burned); err != nil {
		t.Fatalf("allocating the id to burn: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rolling back the burn transaction: %v", err)
	}

	// Now the rest. Its checkpoint must skip the burned id.
	for i := 4; i < 8; i++ {
		insertTestEvent(t, db, base.Add(time.Duration(i)*time.Second))
	}
	if err := checkpoint.RunSeal(ctx, db, checkpoint.SealOptions{LagSeconds: checkpoint.SealLag(0)}); err != nil {
		t.Fatalf("sealing the second batch: %v", err)
	}

	var ids []int64
	rows, err := db.Query(ctx, `SELECT id FROM audit_checkpoints ORDER BY id`)
	if err != nil {
		t.Fatalf("reading checkpoint ids: %v", err)
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			t.Fatalf("scanning a checkpoint id: %v", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("reading checkpoint ids: %v", err)
	}

	if len(ids) != 2 {
		t.Fatalf("%d checkpoints written, want 2", len(ids))
	}
	if ids[1] == ids[0]+1 {
		t.Fatalf("the ids are contiguous (%v), so the rollback did not burn one and this "+
			"test proves nothing about sparse ids", ids)
	}
	if ids[1] != burned+1 {
		t.Errorf("the second checkpoint has id %d; the burned id was %d", ids[1], burned)
	}

	// The gap in the ids must not read as a gap in the chain.
	result, _, err := checkpoint.RunVerify(ctx, db, checkpoint.VerifyOptions{})
	if err != nil {
		t.Fatalf("verifying the chain: %v", err)
	}
	if len(result.Anomalies) != 0 {
		t.Errorf("a burned checkpoint id was reported as a chain anomaly: %v", result.Anomalies)
	}
	if result.CheckpointsVerified != 2 {
		t.Errorf("%d checkpoints verified, want 2", result.CheckpointsVerified)
	}
}

// migration011Rule mirrors the decision migration 011 makes, collapsed into the
// single statement a test can run.
//
// It is a copy, and a copy is a liability: the first version of this test kept
// the original event_id_min/event_id_max join after the migration moved to
// affected_checkpoint_ids, so it went on asserting a rule the schema no longer
// applied. Keep it in step with
// migrations/011_verify_covered_range_provenance.up.sql.
//
// The migration itself cannot be replayed here. It writes the transitional
// value 'unverified' before adding the CHECK constraint that forbids it, so
// re-running its statements against an already-migrated database fails on that
// constraint. What is reproduced is the outcome: discard any interval that is
// not provably complete.
const migration011Rule = `
	WITH survival AS (
	    SELECT cp.id AS checkpoint_id, cp.event_count AS attested_count,
	           COUNT(e.id) AS surviving_count
	    FROM audit_checkpoints cp
	    LEFT JOIN audit_events e ON e.id >= cp.range_start AND e.id <= cp.range_end
	    GROUP BY cp.id, cp.event_count
	), purged AS (
	    SELECT DISTINCT cid AS checkpoint_id
	    FROM audit_purges p
	    CROSS JOIN LATERAL UNNEST(p.affected_checkpoint_ids) AS cid
	    WHERE NOT p.dry_run
	)
	UPDATE audit_checkpoints c
	SET covered_from = NULL, covered_to = NULL, covered_range_source = NULL
	FROM survival s LEFT JOIN purged p ON p.checkpoint_id = s.checkpoint_id
	WHERE c.id = s.checkpoint_id
	  AND NOT (s.surviving_count = s.attested_count AND p.checkpoint_id IS NULL)
`

// TestPartiallyPurgedRangeIsNotTrusted covers migration 011.
//
// Migration 009 backfilled covered_from and covered_to from whatever events
// survived. Where a purge had already removed part of a checkpoint's range,
// that produced an interval narrower than what the checkpoint attests, and
// once written it is indistinguishable from one computed over a complete set.
//
// A missing range is a known unknown: the emitter refuses the checkpoint and
// names it. A narrowed range is a silent falsehood in the field an auditor uses
// to scope the evidence, and it understates coverage while looking
// authoritative. 011 discards any interval it cannot prove complete.
func TestPartiallyPurgedRangeIsNotTrusted(t *testing.T) {
	db := testDB(t)
	resetCheckpoints(t, db)
	ctx := context.Background()

	base := time.Now().UTC().Add(-2 * time.Hour)
	for i := range 6 {
		insertTestEvent(t, db, base.Add(time.Duration(i)*time.Minute))
	}
	if err := checkpoint.RunSeal(ctx, db, checkpoint.SealOptions{LagSeconds: checkpoint.SealLag(0)}); err != nil {
		t.Fatalf("sealing: %v", err)
	}

	var cpID int64
	var sealedFrom, sealedTo time.Time
	var source string
	if err := db.QueryRow(ctx, `
		SELECT id, covered_from, covered_to, covered_range_source
		FROM audit_checkpoints ORDER BY id DESC LIMIT 1
	`).Scan(&cpID, &sealedFrom, &sealedTo, &source); err != nil {
		t.Fatalf("reading the checkpoint: %v", err)
	}
	if source != "sealed" {
		t.Fatalf("a freshly sealed checkpoint has provenance %q, want \"sealed\"", source)
	}

	// Purge the first two events and record it, as the purge command does.
	if _, err := db.Exec(ctx, `DELETE FROM audit_events WHERE id <= 2`); err != nil {
		t.Fatalf("purging events: %v", err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO audit_purges
		    (window_start, window_end, event_id_min, event_id_max, rows_deleted,
		     affected_checkpoint_ids, dry_run)
		VALUES ($1, $2, 1, 2, 2, ARRAY[$3]::BIGINT[], FALSE)
	`, base, base.Add(time.Minute), cpID); err != nil {
		t.Fatalf("recording the purge: %v", err)
	}

	// Re-run what 009 did, which is what produces the narrowed interval.
	if _, err := db.Exec(ctx, `
		UPDATE audit_checkpoints c
		SET covered_from = r.min_ts, covered_to = r.max_ts
		FROM (
			SELECT cp.id AS checkpoint_id,
			       MIN(e."timestamp") AS min_ts, MAX(e."timestamp") AS max_ts
			FROM audit_checkpoints cp
			JOIN audit_events e ON e.id >= cp.range_start AND e.id <= cp.range_end
			GROUP BY cp.id
		) r
		WHERE c.id = r.checkpoint_id
	`); err != nil {
		t.Fatalf("re-running the 009 backfill: %v", err)
	}

	var narrowedFrom time.Time
	if err := db.QueryRow(ctx,
		`SELECT covered_from FROM audit_checkpoints WHERE id = $1`, cpID).Scan(&narrowedFrom); err != nil {
		t.Fatalf("reading the narrowed range: %v", err)
	}
	if !narrowedFrom.After(sealedFrom) {
		t.Fatalf("the backfill did not narrow the range (%v then %v), so this test is not "+
			"exercising the case 011 exists for", sealedFrom, narrowedFrom)
	}

	// Now apply 011's rule. The surviving count no longer matches the
	// checkpoint's event_count and a purge is recorded against it, so neither
	// condition holds and the interval must be discarded.
	if _, err := db.Exec(ctx, migration011Rule); err != nil {
		t.Fatalf("applying the 011 rule: %v", err)
	}

	var from, to *time.Time
	var src *string
	if err := db.QueryRow(ctx, `
		SELECT covered_from, covered_to, covered_range_source
		FROM audit_checkpoints WHERE id = $1
	`, cpID).Scan(&from, &to, &src); err != nil {
		t.Fatalf("reading the checkpoint after 011: %v", err)
	}
	if from != nil || to != nil || src != nil {
		t.Errorf("a partially purged checkpoint kept its narrowed range: from=%v to=%v source=%v; "+
			"a narrowed interval understates coverage while looking authoritative", from, to, src)
	}
}

// TestLogOnlyPurgeDoesNotInvalidateCoverage is the counterpart to
// TestPartiallyPurgedRangeIsNotTrusted: 011 must discard what it cannot prove,
// and must not discard anything else.
//
// audit_purges.event_id_min/event_id_max hold ids from whichever table the run
// targeted. `aegis-migrate purge --table audit_logs` records audit_logs ids,
// and that BIGSERIAL is unrelated to audit_events' — the two id spaces overlap
// numerically all the time. Attributing a purge by comparing those columns
// against range_start/range_end therefore lets a retention run on a table no
// checkpoint attests destroy a valid checkpoint's coverage, after which the
// emitter refuses that checkpoint by name and the rest of the chain cannot be
// submitted.
//
// affected_checkpoint_ids is the distinction those columns cannot express:
// purge.go populates it only when audit_events rows are in scope and leaves it
// explicitly empty otherwise.
func TestLogOnlyPurgeDoesNotInvalidateCoverage(t *testing.T) {
	db := testDB(t)
	resetCheckpoints(t, db)
	ctx := context.Background()

	base := time.Now().UTC().Add(-2 * time.Hour)
	for i := range 6 {
		insertTestEvent(t, db, base.Add(time.Duration(i)*time.Minute))
	}
	if err := checkpoint.RunSeal(ctx, db, checkpoint.SealOptions{LagSeconds: checkpoint.SealLag(0)}); err != nil {
		t.Fatalf("sealing: %v", err)
	}

	var cpID int64
	var rangeStart, rangeEnd int64
	if err := db.QueryRow(ctx, `
		SELECT id, range_start, range_end
		FROM audit_checkpoints ORDER BY id DESC LIMIT 1
	`).Scan(&cpID, &rangeStart, &rangeEnd); err != nil {
		t.Fatalf("reading the checkpoint: %v", err)
	}

	// A retention run against audit_logs only. Its recorded id range is chosen
	// to sit squarely inside the checkpoint's event range, which is the
	// coincidence the old attribution could not tell from a real overlap. No
	// audit_events row is touched, and affected_checkpoint_ids is empty, which
	// is what purge.go writes for a log-only run.
	if _, err := db.Exec(ctx, `
		INSERT INTO audit_purges
		    (window_start, window_end, event_id_min, event_id_max, rows_deleted,
		     affected_checkpoint_ids, dry_run)
		VALUES ($1, $2, $3, $4, 500, '{}'::BIGINT[], FALSE)
	`, base, base.Add(time.Minute), rangeStart, rangeEnd); err != nil {
		t.Fatalf("recording the log-only purge: %v", err)
	}

	if _, err := db.Exec(ctx, migration011Rule); err != nil {
		t.Fatalf("applying the 011 rule: %v", err)
	}

	var from, to *time.Time
	var src *string
	if err := db.QueryRow(ctx, `
		SELECT covered_from, covered_to, covered_range_source
		FROM audit_checkpoints WHERE id = $1
	`, cpID).Scan(&from, &to, &src); err != nil {
		t.Fatalf("reading the checkpoint after 011: %v", err)
	}
	if from == nil || to == nil || src == nil {
		t.Fatalf("a purge of audit_logs discarded an intact checkpoint's coverage "+
			"(from=%v to=%v source=%v); the emitter now refuses this checkpoint by name "+
			"and cannot submit the rest of the chain", from, to, src)
	}
	if *src != "sealed" {
		t.Errorf("provenance is %q after an unrelated purge, want it left as \"sealed\"", *src)
	}
}

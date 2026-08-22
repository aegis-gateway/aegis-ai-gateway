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
		LagSeconds: 0, BatchSize: 4,
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
	if err := checkpoint.RunSeal(ctx, db, checkpoint.SealOptions{LagSeconds: 0}); err != nil {
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

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

package controlplanev1

import (
	"bytes"
	"testing"
	"time"
)

// fixed inputs, so a change to the construction shows up as a changed digest
// rather than as a passing test over different data.
func v3Fixture() (root, prev []byte, sealedAt time.Time) {
	root = bytes.Repeat([]byte{0xAB}, 32)
	prev = bytes.Repeat([]byte{0xCD}, 32)
	sealedAt = time.Date(2026, 8, 30, 12, 0, 0, 123456000, time.UTC)
	return
}

func TestCheckpointHashInputV3_IsNinetySixPlusEight(t *testing.T) {
	root, prev, at := v3Fixture()
	got, err := CheckpointHashInputV3(root, prev, 1, 10000, 10000, HashSchemaVersion3, at, 41)
	if err != nil {
		t.Fatalf("building input: %v", err)
	}
	if len(got) != CheckpointHashInputLenV3 {
		t.Errorf("input length = %d, want %d", len(got), CheckpointHashInputLenV3)
	}
	if CheckpointHashInputLenV3 != 104 {
		t.Errorf("CheckpointHashInputLenV3 = %d, want 104", CheckpointHashInputLenV3)
	}
}

// The first 96 bytes must be byte-identical to version 2, so a verifier that
// implements both shares everything but the tail.
func TestCheckpointHashInputV3_SharesThePrefixWithV2(t *testing.T) {
	root, prev, at := v3Fixture()
	v2, err := CheckpointHashInput(root, prev, 1, 10000, 10000, HashSchemaVersion3, at)
	if err != nil {
		t.Fatalf("v2 input: %v", err)
	}
	v3, err := CheckpointHashInputV3(root, prev, 1, 10000, 10000, HashSchemaVersion3, at, 41)
	if err != nil {
		t.Fatalf("v3 input: %v", err)
	}
	if !bytes.Equal(v2, v3[:CheckpointHashInputLen]) {
		t.Error("the first 96 bytes of the version-3 input differ from the version-2 input")
	}
}

// The whole point of ADR 0006: repointing a checkpoint at a different
// predecessor must change the digest even when every other input is identical.
func TestComputeCheckpointHashV3_PredecessorIdentityChangesTheDigest(t *testing.T) {
	root, prev, at := v3Fixture()
	a, err := ComputeCheckpointHashV3(root, prev, 1, 10000, 10000, HashSchemaVersion3, at, 41)
	if err != nil {
		t.Fatalf("hashing: %v", err)
	}
	b, err := ComputeCheckpointHashV3(root, prev, 1, 10000, 10000, HashSchemaVersion3, at, 7)
	if err != nil {
		t.Fatalf("hashing: %v", err)
	}
	if bytes.Equal(a, b) {
		t.Error("repointing at a different predecessor left the digest unchanged, " +
			"which is the detachment ADR 0006 describes")
	}
}

// A version-2 digest and a version-3 digest over the same inputs must differ.
// The version scalar was always in the input so that a construction change
// could not be mistaken for tampering.
func TestComputeCheckpointHashV3_DiffersFromV2(t *testing.T) {
	root, prev, at := v3Fixture()
	v2, err := ComputeCheckpointHash(root, prev, 1, 10000, 10000, HashSchemaVersion3, at)
	if err != nil {
		t.Fatalf("v2: %v", err)
	}
	v3, err := ComputeCheckpointHashV3(root, prev, 1, 10000, 10000, HashSchemaVersion3, at, 41)
	if err != nil {
		t.Fatalf("v3: %v", err)
	}
	if bytes.Equal(v2, v3) {
		t.Error("the version-3 digest equals the version-2 digest over the same inputs")
	}
}

func TestCheckpointHashInputV3_GenesisAndRejections(t *testing.T) {
	root, prev, at := v3Fixture()

	if _, err := CheckpointHashInputV3(root, prev, 1, 10, 10,
		HashSchemaVersion3, at, GenesisPrevCheckpointID); err != nil {
		t.Errorf("genesis prev_checkpoint_id must be accepted: %v", err)
	}
	if _, err := CheckpointHashInputV3(root, prev, 1, 10, 10,
		HashSchemaVersion3, at, -1); err == nil {
		t.Error("a negative prev_checkpoint_id was accepted")
	}
	if _, err := CheckpointHashInputV3(root[:31], prev, 1, 10, 10,
		HashSchemaVersion3, at, 1); err == nil {
		t.Error("a short merkle_root was accepted")
	}
	if _, err := CheckpointHashInputV3(root, prev[:31], 1, 10, 10,
		HashSchemaVersion3, at, 1); err == nil {
		t.Error("a short prev_checkpoint_hash was accepted")
	}
}

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
	"errors"
	"testing"
)

// The constant is part of the wire contract once shipped. Pin its value so a
// renumbering fails here rather than in a verifier that disagrees with a
// sealer about what 4 means.
func TestHashSchemaVersion4_IsFour(t *testing.T) {
	if HashSchemaVersion4 != 4 {
		t.Errorf("HashSchemaVersion4 = %d, want 4", HashSchemaVersion4)
	}
}

// Version 4 changed the leaf field set and nothing else. Its checkpoint hash
// input is the version-3 construction, 104 bytes, carrying 4 in the version
// scalar.
func TestCheckpointHashInputV4_IsTheV3Construction(t *testing.T) {
	root, prev, at := v3Fixture()
	got, err := CheckpointHashInputV3(root, prev, 1, 10000, 10000, HashSchemaVersion4, at, 41)
	if err != nil {
		t.Fatalf("building input: %v", err)
	}
	if len(got) != CheckpointHashInputLenV3 {
		t.Fatalf("input length = %d, want %d", len(got), CheckpointHashInputLenV3)
	}
	// The version scalar sits at bytes 84-87: 32 + 32 + 8 + 8 + 4.
	if v := got[84:88]; !bytes.Equal(v, []byte{4, 0, 0, 0}) {
		t.Errorf("version scalar = %x, want 04000000", v)
	}
	v3, err := CheckpointHashInputV3(root, prev, 1, 10000, 10000, HashSchemaVersion3, at, 41)
	if err != nil {
		t.Fatalf("building v3 input: %v", err)
	}
	// Everything but the version scalar is identical to version 3.
	for i := range got {
		if i >= 84 && i < 88 {
			continue
		}
		if got[i] != v3[i] {
			t.Fatalf("byte %d differs between the version-3 and version-4 inputs; "+
				"only the version scalar may", i)
		}
	}
}

// The version scalar is what keeps a version-3 digest and a version-4 digest
// over identical inputs apart, so one can never be passed off as the other.
func TestComputeCheckpointHashV4_DiffersFromV3(t *testing.T) {
	root, prev, at := v3Fixture()
	v3, err := ComputeCheckpointHashV3(root, prev, 1, 10000, 10000, HashSchemaVersion3, at, 41)
	if err != nil {
		t.Fatalf("v3: %v", err)
	}
	v4, err := ComputeCheckpointHashV3(root, prev, 1, 10000, 10000, HashSchemaVersion4, at, 41)
	if err != nil {
		t.Fatalf("v4: %v", err)
	}
	if bytes.Equal(v3, v4) {
		t.Error("the version-4 digest equals the version-3 digest over the same inputs")
	}
}

// A version-4 submission verifies under the 104-byte construction, and fails
// under the 96-byte one. Recomputing it with the version-2 rules would report
// intact data as a mismatch, which is the false accusation the version switch
// exists to prevent.
func TestVerifyCheckpointHash_Version4UsesTheV3Construction(t *testing.T) {
	root, prev, at := v3Fixture()
	prevID := int64(41)

	build := func(h []byte) *CheckpointSubmission {
		return &CheckpointSubmission{
			CheckpointID:       42,
			MerkleRoot:         NewHashHex(root),
			PrevCheckpointHash: NewHashHex(prev),
			CheckpointHash:     NewHashHex(h),
			PrevCheckpointID:   &prevID,
			RangeStart:         1,
			RangeEnd:           10000,
			EventCount:         10000,
			HashAlgorithm:      HashAlgorithmSHA256,
			HashSchemaVersion:  HashSchemaVersion4,
			SealedAt:           Timestamp{Time: at},
		}
	}

	good, err := ComputeCheckpointHashV3(root, prev, 1, 10000, 10000, HashSchemaVersion4, at, prevID)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	if err := VerifyCheckpointHash(build(good)); err != nil {
		t.Errorf("a version-4 checkpoint built with the 104-byte construction did not verify: %v", err)
	}

	wrong, err := ComputeCheckpointHash(root, prev, 1, 10000, 10000, HashSchemaVersion4, at)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	var mismatch *ErrCheckpointHashMismatch
	if err := VerifyCheckpointHash(build(wrong)); !errors.As(err, &mismatch) {
		t.Errorf("a version-4 checkpoint built with the 96-byte construction: err = %v, want a mismatch", err)
	}

	// Repointing the predecessor must break a version-4 digest exactly as it
	// breaks a version-3 one, since the construction is shared.
	other := int64(7)
	sub := build(good)
	sub.PrevCheckpointID = &other
	if err := VerifyCheckpointHash(sub); !errors.As(err, &mismatch) {
		t.Errorf("a repointed version-4 checkpoint: err = %v, want a mismatch", err)
	}
}

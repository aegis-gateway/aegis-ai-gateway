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
	"strings"
	"testing"
	"time"
)

// An unknown version must be refused, not recomputed with whatever construction
// this build happens to implement. Falling back would return success for a
// digest that was never checked, which is worse than reporting inability.
func TestVerifyCheckpointHash_RefusesAnUnknownVersion(t *testing.T) {
	root := bytes.Repeat([]byte{0xAB}, 32)
	prev := bytes.Repeat([]byte{0xCD}, 32)
	at := time.Date(2026, 8, 30, 12, 0, 0, 123456000, time.UTC)

	// A digest built with the 96-byte construction but ADVERTISING version 4.
	// Before the fix this verified, because the default branch used that same
	// construction and the two therefore agreed.
	h, err := ComputeCheckpointHash(root, prev, 1, 10, 10, 4, at)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	sub := &CheckpointSubmission{
		CheckpointID:       7,
		MerkleRoot:         NewHashHex(root),
		PrevCheckpointHash: NewHashHex(prev),
		CheckpointHash:     NewHashHex(h),
		RangeStart:         1,
		RangeEnd:           10,
		EventCount:         10,
		HashAlgorithm:      HashAlgorithmSHA256,
		HashSchemaVersion:  4,
		SealedAt:           Timestamp{Time: at},
	}
	err = VerifyCheckpointHash(sub)
	if err == nil {
		t.Fatal("a version-4 checkpoint verified against the version-2 construction; " +
			"this build reported a digest as verified that it cannot compute")
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Errorf("error = %q, want it to say the version is unsupported", err.Error())
	}
}

// The supported versions must still verify, or the refusal above is overzealous.
func TestVerifyCheckpointHash_AcceptsEverySupportedVersion(t *testing.T) {
	root := bytes.Repeat([]byte{0xAB}, 32)
	prev := bytes.Repeat([]byte{0xCD}, 32)
	at := time.Date(2026, 8, 30, 12, 0, 0, 123456000, time.UTC)
	prevID := int64(41)

	for _, v := range []int32{HashSchemaVersion1, HashSchemaVersion2, HashSchemaVersion3} {
		var h []byte
		var err error
		if v == HashSchemaVersion3 {
			h, err = ComputeCheckpointHashV3(root, prev, 1, 10, 10, v, at, prevID)
		} else {
			h, err = ComputeCheckpointHash(root, prev, 1, 10, 10, v, at)
		}
		if err != nil {
			t.Fatalf("v%d building: %v", v, err)
		}
		sub := &CheckpointSubmission{
			CheckpointID:       7,
			MerkleRoot:         NewHashHex(root),
			PrevCheckpointHash: NewHashHex(prev),
			CheckpointHash:     NewHashHex(h),
			PrevCheckpointID:   &prevID,
			RangeStart:         1,
			RangeEnd:           10,
			EventCount:         10,
			HashAlgorithm:      HashAlgorithmSHA256,
			HashSchemaVersion:  v,
			SealedAt:           Timestamp{Time: at},
		}
		if err := VerifyCheckpointHash(sub); err != nil {
			t.Errorf("version %d did not verify: %v", v, err)
		}
	}
}

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
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"time"
)

// This file is the normative implementation of the checkpoint hash construction
// specified in docs/AUDIT-INTEGRITY.md section 3.
//
// It lives in the public protocol package, rather than beside the sealer,
// because more than one party needs it and they must not each write their own.
// The gateway's sealer calls it. A control plane aggregating checkpoints calls
// it to confirm that what it stored can be re-derived. An independent verifier
// checking a years-old evidence bundle calls it, or reimplements it from the
// published specification and gets the same bytes.
//
// Two implementations of one specification is the drift the specification
// exists to prevent. There is one here.

// CheckpointHashInputLen is the byte length of the hash input at
// [HashSchemaVersion1]: two 32-byte digests followed by 32 bytes of scalars.
//
// The length is part of the contract. An earlier version of this construction
// prefixed the predecessor hash with its length, producing a 100-byte input
// that no verifier following the published specification would reproduce.
const CheckpointHashInputLen = 96

// HashSchemaVersion1 is the serialization and hash-input specification
// described in docs/AUDIT-INTEGRITY.md sections 3 through 5.
//
// Superseded by [HashSchemaVersion2]. The constant stays because removing a name
// from this package is a breaking change to a shipped wire contract, and because
// a verifier reading an archived version-1 checkpoint still needs to name what
// it is reading.
const HashSchemaVersion1 int32 = 1

// HashSchemaVersion2 is the field set that replaced audit_events.metadata with
// twelve typed columns, defined in docs/AUDIT-INTEGRITY.md section 5.1 and cut by
// migration 013.
//
// The checkpoint hash construction is unchanged between versions 1 and 2: the
// same 96 bytes in the same order, with this number in the version scalar. Only
// the set of columns the leaf hash covers differs.
const HashSchemaVersion2 int32 = 2

// HashSchemaVersion3 binds the predecessor's identity into the checkpoint hash
// and extends the leaf field set, defined in docs/AUDIT-INTEGRITY.md sections 3
// and 5.1 and cut by migration 016.
//
// Two changes, one version, because a version number is expensive: every bump
// costs a verifier that can compute another field set, so the queued work rides
// together rather than separately. ADR 0006 (issue #38) asked for
// prev_checkpoint_id in the hash; ADR 0011 asked for tool names in the leaf and
// said explicitly they should ride the next bump rather than cause one; the
// allow event gained the outcome fields that made a permitted request's
// attestation say what actually ran.
//
// Unlike versions 1 and 2, this one CHANGES THE CHECKPOINT HASH CONSTRUCTION.
// The input is 104 bytes rather than 96, with int64_le(prev_checkpoint_id)
// appended. A version-2 checkpoint and a version-3 checkpoint over identical
// inputs therefore hash differently, which is the point: the version scalar was
// always in the input precisely so that a construction change could not be
// mistaken for tampering.
const HashSchemaVersion3 int32 = 3

// CheckpointHashInputLenV3 is the length of the version-3 checkpoint hash
// input: the 96 bytes of [CheckpointHashInputLen] plus 8 for the predecessor's
// identity.
const CheckpointHashInputLenV3 = CheckpointHashInputLen + 8

// GenesisPrevCheckpointID is the prev_checkpoint_id a genesis checkpoint binds,
// standing for "no predecessor".
//
// Zero rather than NULL for the same reason [GenesisPrevHashBytes] is 32 zero
// bytes rather than an empty slice: the hash input is a fixed width, and an
// absent value has to have a defined encoding or the first checkpoint in a
// chain has no reproducible digest.
const GenesisPrevCheckpointID int64 = 0

// CheckpointHashInputV3 returns the exact bytes hashed to produce a version-3
// checkpoint hash, per docs/AUDIT-INTEGRITY.md section 3:
//
//	merkle_root                     (32 bytes)
//	|| prev_checkpoint_hash         (32 bytes)
//	|| uint64_le(range_start)        (8)
//	|| uint64_le(range_end)          (8)
//	|| uint32_le(event_count)        (4)
//	|| uint32_le(hash_schema_version)(4)
//	|| int64_le(sealed_at_unix_micros)(8)
//	|| int64_le(prev_checkpoint_id)  (8)
//
// The first seven fields are byte-identical to [CheckpointHashInput], so a
// reader implementing both versions shares everything but the tail.
//
// Binding the identity narrows the gap ADR 0006 records, and it is worth being
// exact about how far. With only the predecessor's HASH in the input,
// prev_checkpoint_id could be altered while every digest in the chain, INCLUDING
// one held by an anchoring party, continued to verify: it was the one structural
// field an external commitment did not reach. Inside the digest, it is covered
// by whatever that party holds.
//
// This does NOT make a rewritten chain detectable offline. The construction is
// unkeyed, so an attacker with write access to the checkpoint table recomputes
// any digest and every successor, at version 3 exactly as at version 2.
// Detection still requires digests obtained independently of the database.
//
// It is a separate function rather than a version switch inside
// [CheckpointHashInput] because that function's output is a shipped contract:
// an external verifier reproduces those 96 bytes, and adding a branch to it
// risks changing them.
func CheckpointHashInputV3(
	merkleRoot, prevCheckpointHash []byte,
	rangeStart, rangeEnd int64,
	eventCount, hashSchemaVersion int32,
	sealedAt time.Time,
	prevCheckpointID int64,
) ([]byte, error) {
	base, err := CheckpointHashInput(
		merkleRoot, prevCheckpointHash, rangeStart, rangeEnd,
		eventCount, hashSchemaVersion, sealedAt)
	if err != nil {
		return nil, err
	}
	if prevCheckpointID < 0 {
		return nil, fmt.Errorf("prev_checkpoint_id must not be negative, got %d; "+
			"the genesis checkpoint passes %d", prevCheckpointID, GenesisPrevCheckpointID)
	}

	buf := make([]byte, 0, CheckpointHashInputLenV3)
	buf = append(buf, base...)
	var tail [8]byte
	binary.LittleEndian.PutUint64(tail[:], uint64(prevCheckpointID))
	buf = append(buf, tail[:]...)

	return buf, nil
}

// ComputeCheckpointHashV3 returns SHA-256 over [CheckpointHashInputV3].
func ComputeCheckpointHashV3(
	merkleRoot, prevCheckpointHash []byte,
	rangeStart, rangeEnd int64,
	eventCount, hashSchemaVersion int32,
	sealedAt time.Time,
	prevCheckpointID int64,
) ([]byte, error) {
	input, err := CheckpointHashInputV3(
		merkleRoot, prevCheckpointHash, rangeStart, rangeEnd,
		eventCount, hashSchemaVersion, sealedAt, prevCheckpointID)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(input)
	return sum[:], nil
}

// CheckpointHashInput returns the exact bytes hashed to produce a checkpoint
// hash, per docs/AUDIT-INTEGRITY.md section 3:
//
//	merkle_root                     (32 bytes)
//	|| prev_checkpoint_hash         (32 bytes)
//	|| uint64_le(range_start)        (8)
//	|| uint64_le(range_end)          (8)
//	|| uint32_le(event_count)        (4)
//	|| uint32_le(hash_schema_version)(4)
//	|| int64_le(sealed_at_unix_micros)(8)
//
// Both digests must be exactly 32 bytes. The first checkpoint in a chain passes
// [GenesisPrevHashBytes], never nil: the input is a fixed 96 bytes, and a
// shorter predecessor changes the length and so the result.
//
// It is exported separately from [ComputeCheckpointHash] because a verifier
// diagnosing a mismatch wants the bytes, not another digest.
func CheckpointHashInput(
	merkleRoot, prevCheckpointHash []byte,
	rangeStart, rangeEnd int64,
	eventCount, hashSchemaVersion int32,
	sealedAt time.Time,
) ([]byte, error) {
	if len(merkleRoot) != digestLenSHA256 {
		return nil, fmt.Errorf("merkle_root must be %d bytes, got %d", digestLenSHA256, len(merkleRoot))
	}
	if len(prevCheckpointHash) != digestLenSHA256 {
		return nil, fmt.Errorf("prev_checkpoint_hash must be %d bytes, got %d; "+
			"the genesis checkpoint passes 32 zero bytes rather than an empty value",
			digestLenSHA256, len(prevCheckpointHash))
	}

	buf := make([]byte, 0, CheckpointHashInputLen)
	buf = append(buf, merkleRoot...)
	buf = append(buf, prevCheckpointHash...)

	var scalars [32]byte
	binary.LittleEndian.PutUint64(scalars[0:8], uint64(rangeStart))
	binary.LittleEndian.PutUint64(scalars[8:16], uint64(rangeEnd))
	binary.LittleEndian.PutUint32(scalars[16:20], uint32(eventCount))
	binary.LittleEndian.PutUint32(scalars[20:24], uint32(hashSchemaVersion))
	// UnixMicro, matching the TIMESTAMPTZ precision PostgreSQL stores. Rounding
	// to seconds or extending to nanoseconds produces a different digest from
	// the same instant.
	binary.LittleEndian.PutUint64(scalars[24:32], uint64(sealedAt.UTC().UnixMicro()))
	buf = append(buf, scalars[:]...)

	return buf, nil
}

// ComputeCheckpointHash returns SHA-256 over [CheckpointHashInput].
func ComputeCheckpointHash(
	merkleRoot, prevCheckpointHash []byte,
	rangeStart, rangeEnd int64,
	eventCount, hashSchemaVersion int32,
	sealedAt time.Time,
) ([]byte, error) {
	input, err := CheckpointHashInput(
		merkleRoot, prevCheckpointHash, rangeStart, rangeEnd,
		eventCount, hashSchemaVersion, sealedAt)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(input)
	return sum[:], nil
}

// GenesisPrevHashBytes returns the 32 zero bytes a genesis checkpoint binds as
// its predecessor.
//
// It returns a fresh slice each call so a caller cannot alter the constant for
// everyone else.
func GenesisPrevHashBytes() []byte { return make([]byte, digestLenSHA256) }

// ErrCheckpointHashMismatch reports that a checkpoint does not hash to the
// value it states.
type ErrCheckpointHashMismatch struct {
	CheckpointID int64
	Stated       HashHex
	Recomputed   HashHex
}

// Error implements the error interface.
func (e *ErrCheckpointHashMismatch) Error() string {
	return fmt.Sprintf(
		"checkpoint %d does not hash to its stated value: recomputed %s, stated %s",
		e.CheckpointID, e.Recomputed, e.Stated)
}

// VerifyCheckpointHash recomputes a submission's checkpoint hash from its own
// attested fields and reports whether it matches what the submission states.
//
// This is the property the protocol exists to preserve: a checkpoint that
// arrived intact can be re-derived from what was stored, with no access to the
// gateway that sealed it.
//
// It says nothing about the chain. Whether this checkpoint follows the one
// before it needs state a single submission does not carry.
func VerifyCheckpointHash(sub *CheckpointSubmission) error {
	if sub.HashAlgorithm != HashAlgorithmSHA256 {
		return fmt.Errorf("cannot verify checkpoint %d: hash_algorithm %q is not supported",
			sub.CheckpointID, sub.HashAlgorithm)
	}
	merkleRoot, err := sub.MerkleRoot.Bytes()
	if err != nil {
		return fmt.Errorf("checkpoint %d: merkle_root: %w", sub.CheckpointID, err)
	}
	prevHash, err := sub.PrevCheckpointHash.Bytes()
	if err != nil {
		return fmt.Errorf("checkpoint %d: prev_checkpoint_hash: %w", sub.CheckpointID, err)
	}

	// The construction differs by version and the submission states which one
	// sealed it. Recomputing a version-3 checkpoint with the version-2 input
	// yields a mismatch from intact data, which this function reports as a
	// checkpoint that does not hash to its stated value: a false accusation
	// against a gateway whose chain is sound, and one that would stop every
	// version-3 checkpoint at submission.
	var recomputed []byte
	switch sub.HashSchemaVersion {
	case HashSchemaVersion1, HashSchemaVersion2:
		recomputed, err = ComputeCheckpointHash(
			merkleRoot, prevHash, sub.RangeStart, sub.RangeEnd,
			sub.EventCount, sub.HashSchemaVersion, sub.SealedAt.Time)
	case HashSchemaVersion3:
		prevID := GenesisPrevCheckpointID
		if sub.PrevCheckpointID != nil {
			prevID = *sub.PrevCheckpointID
		}
		recomputed, err = ComputeCheckpointHashV3(
			merkleRoot, prevHash, sub.RangeStart, sub.RangeEnd,
			sub.EventCount, sub.HashSchemaVersion, sub.SealedAt.Time, prevID)
	default:
		// Refuse rather than guess. The version now selects the construction,
		// so falling back to the 96-byte input for an unknown version would
		// recompute a version-4 checkpoint with version-2 rules; if the two
		// happened to agree this function would return success, reporting a
		// digest as verified that it never checked. An unverifiable checkpoint
		// and a verified one must not be the same answer.
		return fmt.Errorf(
			"cannot verify checkpoint %d: hash_schema_version %d is not supported by this build, "+
				"which verifies versions %d, %d and %d",
			sub.CheckpointID, sub.HashSchemaVersion,
			HashSchemaVersion1, HashSchemaVersion2, HashSchemaVersion3)
	}
	if err != nil {
		return fmt.Errorf("checkpoint %d: %w", sub.CheckpointID, err)
	}

	stated, err := sub.CheckpointHash.Bytes()
	if err != nil {
		return fmt.Errorf("checkpoint %d: checkpoint_hash: %w", sub.CheckpointID, err)
	}
	if !bytes.Equal(recomputed, stated) {
		return &ErrCheckpointHashMismatch{
			CheckpointID: sub.CheckpointID,
			Stated:       sub.CheckpointHash,
			Recomputed:   NewHashHex(recomputed),
		}
	}
	return nil
}

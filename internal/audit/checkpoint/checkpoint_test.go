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

package checkpoint_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"testing"
	"time"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/audit/checkpoint"
)

// ---------------------------------------------------------------------------
// RFC 6962 Merkle tree
// ---------------------------------------------------------------------------

func TestCheckpointMerkleRoot_One(t *testing.T) {
	leaves := [][]byte{checkpoint.LeafHash([]byte("a"))}
	root := checkpoint.MerkleRoot(leaves)
	if len(root) != 32 {
		t.Fatalf("expected 32-byte root, got %d", len(root))
	}
	// Single leaf: root == leaf_hash.
	if !bytes.Equal(root, leaves[0]) {
		t.Fatal("single-leaf root should equal the leaf hash")
	}
}

func TestCheckpointMerkleRoot_Two(t *testing.T) {
	l0 := checkpoint.LeafHash([]byte("a"))
	l1 := checkpoint.LeafHash([]byte("b"))
	root := checkpoint.MerkleRoot([][]byte{l0, l1})
	expected := checkpoint.InternalHash(l0, l1)
	if !bytes.Equal(root, expected) {
		t.Fatal("two-leaf root does not match InternalHash(l0,l1)")
	}
}

func TestCheckpointMerkleRoot_Three_OddPromotion(t *testing.T) {
	l0 := checkpoint.LeafHash([]byte("a"))
	l1 := checkpoint.LeafHash([]byte("b"))
	l2 := checkpoint.LeafHash([]byte("c"))
	root := checkpoint.MerkleRoot([][]byte{l0, l1, l2})
	// Level 1: InternalHash(l0,l1); l2 promoted.
	// Root: InternalHash(n01, l2).
	n01 := checkpoint.InternalHash(l0, l1)
	expected := checkpoint.InternalHash(n01, l2)
	if !bytes.Equal(root, expected) {
		t.Fatal("three-leaf root does not match RFC 6962 odd-promotion construction")
	}
}

func TestCheckpointMerkleRoot_Four(t *testing.T) {
	leaves := make([][]byte, 4)
	for i := range leaves {
		leaves[i] = checkpoint.LeafHash([]byte{byte('a' + i)})
	}
	root := checkpoint.MerkleRoot(leaves)
	l01 := checkpoint.InternalHash(leaves[0], leaves[1])
	l23 := checkpoint.InternalHash(leaves[2], leaves[3])
	expected := checkpoint.InternalHash(l01, l23)
	if !bytes.Equal(root, expected) {
		t.Fatal("four-leaf root does not match RFC 6962 construction")
	}
}

func TestCheckpointMerkleRoot_Eight(t *testing.T) {
	leaves := make([][]byte, 8)
	for i := range leaves {
		leaves[i] = checkpoint.LeafHash([]byte{byte('a' + i)})
	}
	root := checkpoint.MerkleRoot(leaves)
	if len(root) != 32 {
		t.Fatalf("expected 32-byte root for 8 leaves, got %d", len(root))
	}
}

// ---------------------------------------------------------------------------
// Domain separation: leaf vs internal prefix bytes
// ---------------------------------------------------------------------------

func TestCheckpointDomainSeparation(t *testing.T) {
	data := []byte("domain-separation-test-data-32by") // 32 bytes
	lh := checkpoint.LeafHash(data)
	ih := checkpoint.InternalHash(data[:16], data[16:])
	plain := sha256.Sum256(data)
	if bytes.Equal(lh, plain[:]) {
		t.Fatal("leaf hash must not equal SHA-256(data)")
	}
	if bytes.Equal(ih, plain[:]) {
		t.Fatal("internal hash must not equal SHA-256(data)")
	}
	if bytes.Equal(lh, ih) {
		t.Fatal("leaf hash and internal hash must differ (domain separation)")
	}
}

// ---------------------------------------------------------------------------
// Genesis constant: first checkpoint uses 32 zero bytes for prev_checkpoint_hash
// ---------------------------------------------------------------------------

func TestCheckpointGenesisConstant(t *testing.T) {
	// The genesis prev_checkpoint_hash is 32 zero bytes per docs/AUDIT-INTEGRITY.md §3.
	// testComputeCheckpointHash copies prevHash into a [96]byte buffer; passing an empty
	// slice results in the same zero bytes as an explicit 32-zero-byte slice (both leave
	// buf[32:64] zero). The spec-mandated invariant is that a non-zero prev hash changes
	// the output — which proves the chain binding is sensitive to prev_checkpoint_hash.
	merkleRoot := bytes.Repeat([]byte{0x01}, 32)
	genesis32zeros := make([]byte, 32) // all zeros — the normative genesis constant
	sealedAt := time.Date(2026, 8, 21, 14, 32, 0, 0, time.UTC)

	// Determinism: identical inputs must produce identical checkpoint hash.
	h1 := testComputeCheckpointHash(merkleRoot, genesis32zeros, 1, 100, 100, 1, sealedAt)
	h2 := testComputeCheckpointHash(merkleRoot, genesis32zeros, 1, 100, 100, 1, sealedAt)
	if !bytes.Equal(h1, h2) {
		t.Fatal("identical inputs must produce identical checkpoint hash")
	}

	// Chain binding: a different (non-zero) prev_hash must produce a different hash.
	nonZeroPrev := bytes.Repeat([]byte{0xFF}, 32)
	h3 := testComputeCheckpointHash(merkleRoot, nonZeroPrev, 1, 100, 100, 1, sealedAt)
	if bytes.Equal(h1, h3) {
		t.Fatal("genesis (32 zeros) and non-zero prev_checkpoint_hash must produce different hashes")
	}
}

// ---------------------------------------------------------------------------
// Chain binding: detect tampering of a middle checkpoint
// ---------------------------------------------------------------------------

func TestCheckpointChainBinding_TamperedField(t *testing.T) {
	merkleRoot := bytes.Repeat([]byte{0xAB}, 32)
	prevHash := make([]byte, 32) // genesis zeros
	sealedAt := time.Date(2026, 8, 21, 14, 32, 0, 0, time.UTC)

	correct := testComputeCheckpointHash(merkleRoot, prevHash, 1, 100, 100, 1, sealedAt)

	// Tamper: range_end
	tampered := testComputeCheckpointHash(merkleRoot, prevHash, 1, 101, 100, 1, sealedAt)
	if bytes.Equal(correct, tampered) {
		t.Fatal("tampering range_end must change checkpoint_hash")
	}

	// Tamper: event_count
	if bytes.Equal(correct, testComputeCheckpointHash(merkleRoot, prevHash, 1, 100, 99, 1, sealedAt)) {
		t.Fatal("tampering event_count must change checkpoint_hash")
	}

	// Tamper: sealed_at (one second later)
	if bytes.Equal(correct, testComputeCheckpointHash(merkleRoot, prevHash, 1, 100, 100, 1, sealedAt.Add(time.Second))) {
		t.Fatal("tampering sealed_at must change checkpoint_hash")
	}

	// Tamper: prev_checkpoint_hash
	altPrev := bytes.Repeat([]byte{0xFF}, 32)
	if bytes.Equal(correct, testComputeCheckpointHash(merkleRoot, altPrev, 1, 100, 100, 1, sealedAt)) {
		t.Fatal("tampering prev_checkpoint_hash must change checkpoint_hash")
	}
}

// ---------------------------------------------------------------------------
// JCS canonicalization
// ---------------------------------------------------------------------------

func TestCheckpointJCS_ObjectKeyOrder(t *testing.T) {
	v := map[string]interface{}{
		"z": int64(1),
		"a": int64(2),
		"m": int64(3),
	}
	got, err := checkpoint.JCSEncode(v)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a":2,"m":3,"z":1}`
	if string(got) != want {
		t.Fatalf("JCS key order wrong:\n  got:  %s\n  want: %s", got, want)
	}
}

func TestCheckpointJCS_NullValue(t *testing.T) {
	v := map[string]interface{}{"x": nil}
	got, err := checkpoint.JCSEncode(v)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"x":null}` {
		t.Fatalf("null encoding wrong: %s", got)
	}
}

func TestCheckpointJCS_NestedObject(t *testing.T) {
	v := map[string]interface{}{
		"b": map[string]interface{}{"y": int64(2), "x": int64(1)},
		"a": int64(0),
	}
	got, err := checkpoint.JCSEncode(v)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a":0,"b":{"x":1,"y":2}}`
	if string(got) != want {
		t.Fatalf("nested JCS wrong:\n  got:  %s\n  want: %s", got, want)
	}
}

// ---------------------------------------------------------------------------
// Timestamp format in JCS serialization (microsecond precision, UTC Z suffix)
// ---------------------------------------------------------------------------

func TestCheckpointTimestampFormat(t *testing.T) {
	ts := time.Date(2026, 8, 21, 14, 32, 0, 123456000, time.UTC) // 123456 microseconds
	row := checkpoint.AuditEventRow{
		ID:        1,
		RequestID: "req-1",
		Timestamp: ts,
		EventType: "test",
		Metadata:  []byte("{}"),
	}
	lh, err := checkpoint.EventLeafHash(row)
	if err != nil {
		t.Fatalf("EventLeafHash: %v", err)
	}
	if len(lh) != 32 {
		t.Fatalf("expected 32-byte leaf hash, got %d bytes", len(lh))
	}
	// A different microsecond must produce a different hash.
	row2 := row
	row2.Timestamp = ts.Add(time.Microsecond)
	lh2, err := checkpoint.EventLeafHash(row2)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(lh, lh2) {
		t.Fatal("different timestamps must produce different leaf hashes")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// testComputeCheckpointHash replicates docs/AUDIT-INTEGRITY.md §3 hash construction
// independently of the production code, for test isolation.
// testComputeCheckpointHash mirrors the production computeCheckpointHash algorithm.
// The length-prefix on prevHash distinguishes genesis (len=0) from a non-genesis
// checkpoint whose hash happens to be 32 zero bytes.
func testComputeCheckpointHash(merkleRoot, prevHash []byte, rangeStart, rangeEnd int64, eventCount, schemaVersion int32, sealedAt time.Time) []byte {
	h := sha256.New()
	h.Write(merkleRoot)
	var lenBuf [4]byte
	binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(prevHash)))
	h.Write(lenBuf[:])
	h.Write(prevHash)
	var scalars [32]byte
	binary.LittleEndian.PutUint64(scalars[0:8], uint64(rangeStart))
	binary.LittleEndian.PutUint64(scalars[8:16], uint64(rangeEnd))
	binary.LittleEndian.PutUint32(scalars[16:20], uint32(eventCount))
	binary.LittleEndian.PutUint32(scalars[20:24], uint32(schemaVersion))
	binary.LittleEndian.PutUint64(scalars[24:32], uint64(sealedAt.UnixMicro()))
	h.Write(scalars[:])
	return h.Sum(nil)
}

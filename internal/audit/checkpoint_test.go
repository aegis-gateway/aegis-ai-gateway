// Copyright 2026 Atlantic Frontier Corporations LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package audit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"
)

func hashN(b byte) []byte {
	sum := sha256.Sum256([]byte{b})
	return sum[:]
}

func TestCheckpoint_MerkleRoot_SingleLeaf(t *testing.T) {
	leaf := hashN(1)
	root, err := MerkleRoot([][]byte{leaf})
	if err != nil {
		t.Fatalf("MerkleRoot: %v", err)
	}
	if !bytes.Equal(root, leaf) {
		t.Fatalf("single-leaf root should equal the leaf; got %x", root)
	}
}

func TestCheckpoint_MerkleRoot_TwoLeaves(t *testing.T) {
	l0, l1 := hashN(1), hashN(2)
	root, err := MerkleRoot([][]byte{l0, l1})
	if err != nil {
		t.Fatal(err)
	}
	want := internalHash(l0, l1)
	if !bytes.Equal(root, want) {
		t.Fatalf("root mismatch:\n got %x\nwant %x", root, want)
	}
}

func TestCheckpoint_MerkleRoot_ThreeLeavesPromoted(t *testing.T) {
	// RFC 6962: odd node is promoted, not duplicated.
	l0, l1, l2 := hashN(1), hashN(2), hashN(3)
	root, err := MerkleRoot([][]byte{l0, l1, l2})
	if err != nil {
		t.Fatal(err)
	}
	pair := internalHash(l0, l1)
	want := internalHash(pair, l2)
	if !bytes.Equal(root, want) {
		t.Fatalf("root mismatch:\n got %x\nwant %x", root, want)
	}

	// Sanity: a 3-leaf root must differ from a "duplicate the odd" root
	// so we can catch a regression to CVE-2012-2459 behavior.
	dup := internalHash(pair, internalHash(l2, l2))
	if bytes.Equal(root, dup) {
		t.Fatal("root matches CVE-2012-2459-style duplication; expected promotion")
	}
}

func TestCheckpoint_MerkleRoot_FourLeaves(t *testing.T) {
	l0, l1, l2, l3 := hashN(1), hashN(2), hashN(3), hashN(4)
	root, err := MerkleRoot([][]byte{l0, l1, l2, l3})
	if err != nil {
		t.Fatal(err)
	}
	want := internalHash(internalHash(l0, l1), internalHash(l2, l3))
	if !bytes.Equal(root, want) {
		t.Fatalf("root mismatch:\n got %x\nwant %x", root, want)
	}
}

func TestCheckpoint_MerkleRoot_EightLeaves(t *testing.T) {
	leaves := make([][]byte, 8)
	for i := range leaves {
		leaves[i] = hashN(byte(i + 1))
	}
	root, err := MerkleRoot(leaves)
	if err != nil {
		t.Fatal(err)
	}
	// Full-tree manual root.
	l01 := internalHash(leaves[0], leaves[1])
	l23 := internalHash(leaves[2], leaves[3])
	l45 := internalHash(leaves[4], leaves[5])
	l67 := internalHash(leaves[6], leaves[7])
	left := internalHash(l01, l23)
	right := internalHash(l45, l67)
	want := internalHash(left, right)
	if !bytes.Equal(root, want) {
		t.Fatalf("root mismatch:\n got %x\nwant %x", root, want)
	}
}

func TestCheckpoint_DomainSeparation(t *testing.T) {
	// Leaf hash must not equal SHA-256(data) alone.
	e := &CanonicalEvent{
		ID:        1,
		RequestID: "req-1",
		Timestamp: time.Unix(1_700_000_000, 123_456_000).UTC(),
		EventType: "auth_success",
		Metadata:  []byte(`{}`),
	}
	leaf, err := e.LeafHash()
	if err != nil {
		t.Fatal(err)
	}
	body, _ := e.CanonicalBytes()
	naive := sha256.Sum256(body)
	if bytes.Equal(leaf, naive[:]) {
		t.Fatal("leaf hash must include 0x00 domain prefix, not plain SHA-256")
	}

	// Internal node must not equal SHA-256(left||right) alone.
	l, r := hashN(1), hashN(2)
	internal := internalHash(l, r)
	naiveNode := sha256.Sum256(append(l, r...))
	if bytes.Equal(internal, naiveNode[:]) {
		t.Fatal("internal node must include 0x01 domain prefix")
	}
}

func TestCheckpoint_InclusionProof_ThreeLeaves(t *testing.T) {
	// Odd-count tree exercises the promotion branch.
	leaves := [][]byte{hashN(1), hashN(2), hashN(3)}
	root, err := MerkleRoot(leaves)
	if err != nil {
		t.Fatal(err)
	}
	for i := range leaves {
		proof, err := InclusionProof(leaves, i)
		if err != nil {
			t.Fatalf("InclusionProof(%d): %v", i, err)
		}
		if !VerifyProof(leaves[i], root, proof) {
			t.Fatalf("proof for leaf %d did not verify", i)
		}
	}
}

func TestCheckpoint_InclusionProof_FourLeaves(t *testing.T) {
	leaves := [][]byte{hashN(1), hashN(2), hashN(3), hashN(4)}
	root, _ := MerkleRoot(leaves)
	for i := range leaves {
		proof, err := InclusionProof(leaves, i)
		if err != nil {
			t.Fatal(err)
		}
		if !VerifyProof(leaves[i], root, proof) {
			t.Fatalf("proof for leaf %d did not verify", i)
		}
		// Wrong leaf must not verify.
		if VerifyProof(hashN(0xFF), root, proof) {
			t.Fatalf("proof accepted for wrong leaf at %d", i)
		}
	}
}

func TestCheckpoint_GenesisConstant(t *testing.T) {
	if len(GenesisPrevHash) != 32 {
		t.Fatalf("GenesisPrevHash length: got %d want 32", len(GenesisPrevHash))
	}
	for _, b := range GenesisPrevHash {
		if b != 0 {
			t.Fatal("GenesisPrevHash must be 32 zero bytes")
		}
	}

	// Recompute a checkpoint hash: genesis uses GenesisPrevHash.
	root := bytes.Repeat([]byte{0xAB}, 32)
	sealedAt := time.Unix(1_700_000_000, 0).UTC()
	genesis := CheckpointInput{
		MerkleRoot:        root,
		PrevHash:          GenesisPrevHash,
		RangeStart:        1,
		RangeEnd:          10,
		EventCount:        10,
		HashSchemaVersion: 1,
		SealedAt:          sealedAt,
	}
	h1, err := genesis.Hash()
	if err != nil {
		t.Fatal(err)
	}

	// Substituting a non-zero prev hash must change the output.
	alt := genesis
	alt.PrevHash = bytes.Repeat([]byte{0xCD}, 32)
	h2, err := alt.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(h1, h2) {
		t.Fatal("checkpoint hash unaffected by prev_hash — genesis constant not authenticating")
	}
}

func TestCheckpoint_ChainBindingDetection(t *testing.T) {
	// Build a 3-checkpoint chain, then tamper the middle one's stored
	// merkle_root. Fast-path verify must detect it because the
	// checkpoint_hash recomputation fails.
	sealed := time.Unix(1_700_000_000, 0).UTC()
	root1 := bytes.Repeat([]byte{0x01}, 32)
	root2 := bytes.Repeat([]byte{0x02}, 32)
	root3 := bytes.Repeat([]byte{0x03}, 32)

	cp1 := CheckpointInput{MerkleRoot: root1, PrevHash: GenesisPrevHash,
		RangeStart: 1, RangeEnd: 10, EventCount: 10, HashSchemaVersion: 1, SealedAt: sealed}
	h1, _ := cp1.Hash()
	cp2 := CheckpointInput{MerkleRoot: root2, PrevHash: h1,
		RangeStart: 11, RangeEnd: 20, EventCount: 10, HashSchemaVersion: 1, SealedAt: sealed.Add(time.Minute)}
	h2, _ := cp2.Hash()
	cp3 := CheckpointInput{MerkleRoot: root3, PrevHash: h2,
		RangeStart: 21, RangeEnd: 30, EventCount: 10, HashSchemaVersion: 1, SealedAt: sealed.Add(2 * time.Minute)}
	h3, _ := cp3.Hash()

	// Tamper cp2's merkle_root but keep stored hash h2 (attacker
	// substitutes events but forgets to re-hash).
	tampered := cp2
	tampered.MerkleRoot = bytes.Repeat([]byte{0xFF}, 32)
	recomputed, _ := tampered.Hash()
	if bytes.Equal(recomputed, h2) {
		t.Fatal("tampered checkpoint recomputed to the same hash — chain binding broken")
	}
	// cp3 still claims prev = h2; recomputing cp3 with prev = recomputed
	// (i.e. re-linking) also changes cp3's own hash.
	relinked := cp3
	relinked.PrevHash = recomputed
	relinkedHash, _ := relinked.Hash()
	if bytes.Equal(relinkedHash, h3) {
		t.Fatal("relinking cp3 to tampered cp2 did not change its hash")
	}
}

func TestCheckpoint_CanonicalTimestampFormat(t *testing.T) {
	// Nanosecond precision in input must be truncated to microseconds.
	e := &CanonicalEvent{
		ID:        1,
		RequestID: "r",
		Timestamp: time.Date(2026, 8, 21, 14, 32, 0, 123_456_789, time.UTC),
		EventType: "auth_success",
		Metadata:  []byte(`{}`),
	}
	body, err := e.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	if !contains(got, `"2026-08-21T14:32:00.123456Z"`) {
		t.Fatalf("timestamp not in RFC3339 μs Z form: %s", got)
	}
	if contains(got, `.123456789`) {
		t.Fatalf("nanoseconds leaked into canonical form: %s", got)
	}
}

func TestCheckpoint_JCS_SortedKeys(t *testing.T) {
	m := map[string]any{
		"b": 1,
		"a": 2,
		"A": 3,
	}
	out, err := JCS(m)
	if err != nil {
		t.Fatal(err)
	}
	// Uppercase 'A' has a lower UTF-16 code unit than 'a' and 'b'.
	want := `{"A":3,"a":2,"b":1}`
	if string(out) != want {
		t.Fatalf("JCS output:\n got %s\nwant %s", string(out), want)
	}
}

func TestCheckpoint_HashHex(t *testing.T) {
	// Regression: ensure the hex form is 64 chars for a well-formed input.
	cp := CheckpointInput{
		MerkleRoot:        bytes.Repeat([]byte{1}, 32),
		PrevHash:          GenesisPrevHash,
		RangeStart:        1, RangeEnd: 1, EventCount: 1,
		HashSchemaVersion: 1,
		SealedAt:          time.Unix(0, 0),
	}
	h, err := cp.HexHash()
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 64 {
		t.Fatalf("hex hash length: got %d want 64 (%s)", len(h), h)
	}
	if _, err := hex.DecodeString(h); err != nil {
		t.Fatalf("hex decode: %v", err)
	}
}

func contains(s, sub string) bool { return bytes.Contains([]byte(s), []byte(sub)) }

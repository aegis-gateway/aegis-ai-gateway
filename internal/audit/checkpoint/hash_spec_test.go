package checkpoint

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"testing"
	"time"
)

// TestCheckpointHashInputIsNinetySixBytes pins the hash input to the length
// published in docs/AUDIT-INTEGRITY.md §3. The value of RFC 6962 here is that
// an independent verifier can check a checkpoint without reading this package,
// which only holds while the implementation and the spec agree.
func TestCheckpointHashInputIsNinetySixBytes(t *testing.T) {
	merkleRoot := bytes.Repeat([]byte{0xAA}, 32)
	prevHash := bytes.Repeat([]byte{0xBB}, 32)
	sealedAt := time.Unix(1700000000, 0).UTC()

	got := computeCheckpointHash(merkleRoot, prevHash, 1, 100, 100, 1, sealedAt)

	// Reproduce the documented concatenation independently.
	var want []byte
	want = append(want, merkleRoot...)
	want = append(want, prevHash...)
	scalars := make([]byte, 32)
	binary.LittleEndian.PutUint64(scalars[0:8], 1)
	binary.LittleEndian.PutUint64(scalars[8:16], 100)
	binary.LittleEndian.PutUint32(scalars[16:20], 100)
	binary.LittleEndian.PutUint32(scalars[20:24], 1)
	binary.LittleEndian.PutUint64(scalars[24:32], uint64(sealedAt.UnixMicro()))
	want = append(want, scalars...)

	if len(want) != 96 {
		t.Fatalf("spec input is %d bytes, expected 96", len(want))
	}
	sum := sha256.Sum256(want)
	if !bytes.Equal(got, sum[:]) {
		t.Errorf("checkpoint hash does not match the documented 96-byte input;\n got  %x\n want %x", got, sum[:])
	}
}

// TestSealLockKeyMatchesDocumentedConstant pins the advisory lock key to the
// value published in docs/AUDIT-INTEGRITY.md. The doc states it as normative so
// an operator can take the same lock from psql; if the derivation here changes
// without the doc, a maintenance script would take a different lock and would
// not exclude the sealer at all.
func TestSealLockKeyMatchesDocumentedConstant(t *testing.T) {
	const documented = 4367013267506373021
	if aegisSealLockKey != documented {
		t.Errorf("advisory lock key is %d but docs/AUDIT-INTEGRITY.md publishes %d; "+
			"an operator following the doc would take a different lock and not exclude the sealer",
			aegisSealLockKey, documented)
	}
}

// TestInclusionProofRejectsAlteredLeaves is a unit-level check of the property
// the verifier now enforces: if any leaf in a checkpoint's range changes, the
// recomputed Merkle root no longer matches the sealed one.
//
// buildInclusionProof previously emitted a proof without making this
// comparison, so `verify-chain --event E` could print a proof that does not
// verify and still exit 0 — the chain hashes alone say nothing about leaves.
func TestInclusionProofRejectsAlteredLeaves(t *testing.T) {
	leaf := func(b byte) []byte {
		h := sha256.Sum256([]byte{b})
		return h[:]
	}
	original := [][]byte{leaf(1), leaf(2), leaf(3), leaf(4)}
	sealedRoot := MerkleRoot(original)

	// Unchanged leaves must reproduce the sealed root.
	if !bytes.Equal(MerkleRoot(original), sealedRoot) {
		t.Fatal("MerkleRoot is not deterministic over identical leaves")
	}

	// Altering any single leaf must break it — which is what the verifier now
	// refuses to paper over.
	for i := range original {
		altered := make([][]byte, len(original))
		copy(altered, original)
		altered[i] = leaf(0xFF)
		if bytes.Equal(MerkleRoot(altered), sealedRoot) {
			t.Errorf("altering leaf %d still reproduced the sealed root; "+
				"an inclusion proof over these leaves would verify against a tampered range", i)
		}
	}
}

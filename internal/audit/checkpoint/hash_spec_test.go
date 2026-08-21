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

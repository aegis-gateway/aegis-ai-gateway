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

// Package checkpoint implements RFC 6962 Merkle checkpoints for tamper-evident audit.
// See docs/AUDIT-INTEGRITY.md for the full design specification.
package checkpoint

import "crypto/sha256"

// leafPrefix and internalPrefix provide RFC 6962 domain separation.
// Without domain separation, a 64-byte blob can be ambiguously interpreted
// as either a leaf hash or an internal node hash (CVE-2012-2459 variant).
const (
	leafPrefix     = byte(0x00)
	internalPrefix = byte(0x01)
)

// LeafHash computes SHA-256(0x00 || data) per RFC 6962 §2.1.
func LeafHash(data []byte) []byte {
	h := sha256.New()
	h.Write([]byte{leafPrefix})
	h.Write(data)
	return h.Sum(nil)
}

// InternalHash computes SHA-256(0x01 || left || right) per RFC 6962 §2.1.
// left and right must each be 32 bytes.
func InternalHash(left, right []byte) []byte {
	h := sha256.New()
	h.Write([]byte{internalPrefix})
	h.Write(left)
	h.Write(right)
	return h.Sum(nil)
}

// MerkleRoot computes the RFC 6962 Merkle tree root over the given leaf hashes.
// Odd node count: unpaired nodes are promoted unchanged (not duplicated).
// This avoids the identical-root-for-distinct-leaf-sets flaw.
// Returns nil for an empty slice.
func MerkleRoot(leaves [][]byte) []byte {
	n := len(leaves)
	if n == 0 {
		return nil
	}
	nodes := make([][]byte, n)
	copy(nodes, leaves)
	for len(nodes) > 1 {
		next := make([][]byte, 0, (len(nodes)+1)/2)
		for i := 0; i < len(nodes); i += 2 {
			if i+1 < len(nodes) {
				next = append(next, InternalHash(nodes[i], nodes[i+1]))
			} else {
				// Odd node: promote unchanged per RFC 6962.
				next = append(next, nodes[i])
			}
		}
		nodes = next
	}
	return nodes[0]
}

// InclusionProof returns the RFC 6962 Merkle inclusion proof (sibling hashes)
// for the leaf at index leafIdx in a tree of the given size. The proof is the
// ordered list of sibling hashes from leaf to root. An auditor verifies by
// walking the proof list: at each step hash the current node with the sibling
// (using InternalHash), alternating left/right based on the index parity.
//
// Returns nil if leafIdx >= numLeaves.
func InclusionProof(leaves [][]byte, leafIdx int) [][]byte {
	if leafIdx < 0 || leafIdx >= len(leaves) {
		return nil
	}
	nodes := make([][]byte, len(leaves))
	copy(nodes, leaves)

	var proof [][]byte
	idx := leafIdx
	for len(nodes) > 1 {
		next := make([][]byte, 0, (len(nodes)+1)/2)
		for i := 0; i < len(nodes); i += 2 {
			if i+1 < len(nodes) {
				// Record sibling for the target index.
				if i == idx || i+1 == idx {
					if i == idx {
						proof = append(proof, nodes[i+1])
					} else {
						proof = append(proof, nodes[i])
					}
				}
				next = append(next, InternalHash(nodes[i], nodes[i+1]))
			} else {
				// Odd node: promoted, no sibling to record.
				next = append(next, nodes[i])
			}
		}
		idx /= 2
		nodes = next
	}
	return proof
}

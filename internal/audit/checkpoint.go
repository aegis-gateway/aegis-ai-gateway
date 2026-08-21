// Copyright 2026 Atlantic Frontier Corporations LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package audit

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

// Canonical serialization and Merkle construction for tamper-evident audit
// checkpoints. Full normative spec lives in docs/AUDIT-INTEGRITY.md.
//
// We deliberately implement RFC 8785 (JCS) inline instead of pulling a new
// dependency: the surface we need is small (null, bool, string, number,
// array, sorted-key object) and staying dep-free makes it easier for
// external auditors to reason about byte-level output.

const (
	// SealerVersion is stamped on inserted checkpoints for debugging only.
	// It is NOT included in the checkpoint hash — attackers can rewrite it.
	SealerVersion = "aegis-sealer/1"

	// CanonicalizationSpec is the label stored on each checkpoint row and
	// emitted by verify-chain so external auditors can pick the right
	// canonicalizer.
	CanonicalizationSpec = "rfc8785-v1"

	// HashSchemaVersion is the schema version covered by the JCS map below.
	HashSchemaVersion = 1

	domainLeaf     byte = 0x00
	domainInternal byte = 0x01
)

// GenesisPrevHash is the 32 zero bytes used as prev_checkpoint_hash when
// hashing the very first (genesis) checkpoint.
var GenesisPrevHash = make([]byte, 32)

// Event mirrors the audit_events columns covered by hash_schema_version=1.
// Nullable string columns use *string; nullable int uses *int.
type CanonicalEvent struct {
	ID             int64
	RequestID      string
	Timestamp      time.Time
	EventType      string
	OrganizationID *string
	TeamID         *string
	UserID         *string
	APIKeyID       *string
	IPAddress      *string
	UserAgent      *string
	Endpoint       *string
	Method         *string
	StatusCode     *int
	ErrorMessage   *string
	Metadata       []byte // raw JSONB bytes
}

// CanonicalBytes returns the RFC 8785 canonical JSON representation of the
// event at hash_schema_version=1.
func (e *CanonicalEvent) CanonicalBytes() ([]byte, error) {
	var metaVal any
	if len(e.Metadata) == 0 {
		metaVal = map[string]any{}
	} else {
		dec := json.NewDecoder(strings.NewReader(string(e.Metadata)))
		dec.UseNumber()
		if err := dec.Decode(&metaVal); err != nil {
			return nil, fmt.Errorf("audit: parse metadata for event %d: %w", e.ID, err)
		}
	}

	obj := map[string]any{
		"id":              e.ID,
		"request_id":      e.RequestID,
		"timestamp":       formatTimestamp(e.Timestamp),
		"event_type":      e.EventType,
		"organization_id": nullableString(e.OrganizationID),
		"team_id":         nullableString(e.TeamID),
		"user_id":         nullableString(e.UserID),
		"api_key_id":      nullableString(e.APIKeyID),
		"ip_address":      nullableString(e.IPAddress),
		"user_agent":      nullableString(e.UserAgent),
		"endpoint":        nullableString(e.Endpoint),
		"method":          nullableString(e.Method),
		"status_code":     nullableInt(e.StatusCode),
		"error_message":   nullableString(e.ErrorMessage),
		"metadata":        metaVal,
	}
	return JCS(obj)
}

// LeafHash returns the domain-separated SHA-256 leaf hash for an event.
func (e *CanonicalEvent) LeafHash() ([]byte, error) {
	body, err := e.CanonicalBytes()
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	h.Write([]byte{domainLeaf})
	h.Write(body)
	return h.Sum(nil), nil
}

func nullableString(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullableInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

// formatTimestamp emits RFC 3339 with microsecond precision and a UTC Z
// suffix. TIMESTAMPTZ carries microseconds; we truncate any nanoseconds so
// that a value round-tripped through the database always hashes the same.
func formatTimestamp(t time.Time) string {
	utc := t.UTC()
	utc = utc.Truncate(time.Microsecond)
	return utc.Format("2006-01-02T15:04:05.000000Z")
}

// MerkleRoot computes an RFC 6962-style Merkle root over `leaves`. Odd
// nodes at any level are promoted (not duplicated) per RFC 6962 to avoid
// CVE-2012-2459.
func MerkleRoot(leaves [][]byte) ([]byte, error) {
	if len(leaves) == 0 {
		return nil, errors.New("audit: empty range has no Merkle root")
	}
	level := make([][]byte, len(leaves))
	for i, l := range leaves {
		if len(l) != 32 {
			return nil, fmt.Errorf("audit: leaf %d has length %d, want 32", i, len(l))
		}
		level[i] = l
	}
	for len(level) > 1 {
		next := make([][]byte, 0, (len(level)+1)/2)
		for i := 0; i+1 < len(level); i += 2 {
			next = append(next, internalHash(level[i], level[i+1]))
		}
		if len(level)%2 == 1 {
			next = append(next, level[len(level)-1]) // promote odd
		}
		level = next
	}
	return level[0], nil
}

// InclusionProof returns the sibling hashes required to prove that
// `leaves[index]` is included in the Merkle root, ordered leaf-to-root.
// Each entry is a struct describing the sibling hash and whether it is on
// the left of the current node.
type ProofStep struct {
	Sibling []byte
	IsLeft  bool // true if sibling sits to the left of the current node
}

func InclusionProof(leaves [][]byte, index int) ([]ProofStep, error) {
	if index < 0 || index >= len(leaves) {
		return nil, fmt.Errorf("audit: index %d out of range [0,%d)", index, len(leaves))
	}
	if len(leaves) == 1 {
		return nil, nil
	}
	proof := []ProofStep{}
	level := make([][]byte, len(leaves))
	copy(level, leaves)
	i := index
	for len(level) > 1 {
		next := make([][]byte, 0, (len(level)+1)/2)
		for j := 0; j+1 < len(level); j += 2 {
			next = append(next, internalHash(level[j], level[j+1]))
		}
		odd := len(level)%2 == 1
		if odd {
			next = append(next, level[len(level)-1])
		}

		if odd && i == len(level)-1 {
			// promoted node: no sibling at this level, index in next level
			// is len(next)-1.
			i = len(next) - 1
		} else {
			if i%2 == 0 {
				proof = append(proof, ProofStep{Sibling: level[i+1], IsLeft: false})
			} else {
				proof = append(proof, ProofStep{Sibling: level[i-1], IsLeft: true})
			}
			i /= 2
		}
		level = next
	}
	return proof, nil
}

// VerifyProof checks that a leaf produces the given root by applying the
// proof steps.
func VerifyProof(leaf, root []byte, proof []ProofStep) bool {
	cur := leaf
	for _, step := range proof {
		if step.IsLeft {
			cur = internalHash(step.Sibling, cur)
		} else {
			cur = internalHash(cur, step.Sibling)
		}
	}
	if len(cur) != len(root) {
		return false
	}
	for i := range cur {
		if cur[i] != root[i] {
			return false
		}
	}
	return true
}

func internalHash(left, right []byte) []byte {
	h := sha256.New()
	h.Write([]byte{domainInternal})
	h.Write(left)
	h.Write(right)
	return h.Sum(nil)
}

// CheckpointInput carries the exact fields that participate in the
// checkpoint_hash computation. `PrevHash` must be 32 bytes; use
// GenesisPrevHash for the first checkpoint.
type CheckpointInput struct {
	MerkleRoot        []byte
	PrevHash          []byte
	RangeStart        uint64
	RangeEnd          uint64
	EventCount        uint32
	HashSchemaVersion uint32
	SealedAt          time.Time
}

// Hash returns the SHA-256 checkpoint hash per docs/AUDIT-INTEGRITY.md §4.
func (c CheckpointInput) Hash() ([]byte, error) {
	if len(c.MerkleRoot) != 32 {
		return nil, fmt.Errorf("audit: merkle_root has length %d, want 32", len(c.MerkleRoot))
	}
	if len(c.PrevHash) != 32 {
		return nil, fmt.Errorf("audit: prev_hash has length %d, want 32", len(c.PrevHash))
	}
	h := sha256.New()
	h.Write(c.MerkleRoot)
	h.Write(c.PrevHash)
	var buf8 [8]byte
	var buf4 [4]byte
	binary.LittleEndian.PutUint64(buf8[:], c.RangeStart)
	h.Write(buf8[:])
	binary.LittleEndian.PutUint64(buf8[:], c.RangeEnd)
	h.Write(buf8[:])
	binary.LittleEndian.PutUint32(buf4[:], c.EventCount)
	h.Write(buf4[:])
	binary.LittleEndian.PutUint32(buf4[:], c.HashSchemaVersion)
	h.Write(buf4[:])
	micros := c.SealedAt.UTC().UnixMicro()
	binary.LittleEndian.PutUint64(buf8[:], uint64(micros))
	h.Write(buf8[:])
	return h.Sum(nil), nil
}

// HexHash is a convenience wrapper.
func (c CheckpointInput) HexHash() (string, error) {
	sum, err := c.Hash()
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(sum), nil
}

// -----------------------------------------------------------------------
// RFC 8785 (JCS) — minimal, dependency-free implementation.
// Handles: null, bool, string, json.Number, float64, int/int64, []any,
// map[string]any. Object keys are sorted by UTF-16 code units (JCS §3.2.3).
// -----------------------------------------------------------------------

// JCS canonicalizes a JSON-compatible Go value per RFC 8785.
func JCS(v any) ([]byte, error) {
	var b strings.Builder
	if err := writeJCS(&b, v); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

func writeJCS(b *strings.Builder, v any) error {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		writeJCSString(b, x)
	case json.Number:
		s, err := jcsNumberString(string(x))
		if err != nil {
			return err
		}
		b.WriteString(s)
	case float64:
		s, err := jcsFloatString(x)
		if err != nil {
			return err
		}
		b.WriteString(s)
	case float32:
		return writeJCS(b, float64(x))
	case int:
		b.WriteString(strconv.FormatInt(int64(x), 10))
	case int32:
		b.WriteString(strconv.FormatInt(int64(x), 10))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case uint:
		b.WriteString(strconv.FormatUint(uint64(x), 10))
	case uint32:
		b.WriteString(strconv.FormatUint(uint64(x), 10))
	case uint64:
		b.WriteString(strconv.FormatUint(x, 10))
	case []any:
		b.WriteByte('[')
		for i, item := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := writeJCS(b, item); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sortUTF16(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			writeJCSString(b, k)
			b.WriteByte(':')
			if err := writeJCS(b, x[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		return fmt.Errorf("audit: JCS: unsupported type %T", v)
	}
	return nil
}

// sortUTF16 sorts strings by their UTF-16 code-unit sequence per JCS §3.2.3.
func sortUTF16(keys []string) {
	sort.Slice(keys, func(i, j int) bool {
		a := utf16.Encode([]rune(keys[i]))
		bs := utf16.Encode([]rune(keys[j]))
		for k := 0; k < len(a) && k < len(bs); k++ {
			if a[k] != bs[k] {
				return a[k] < bs[k]
			}
		}
		return len(a) < len(bs)
	})
}

// writeJCSString writes a JSON string per RFC 8785 §3.2.2 — minimal
// escaping, no unnecessary \uXXXX sequences.
func writeJCSString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}

// jcsNumberString formats a JSON number token per JCS (ECMAScript
// ToString(Number)). We reuse jcsFloatString when possible; if the input
// is an integer that fits in int64 we preserve its integer form to avoid
// spurious ".0" suffixes.
func jcsNumberString(s string) (string, error) {
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return strconv.FormatInt(i, 10), nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return "", fmt.Errorf("audit: JCS: not a JSON number: %q", s)
	}
	return jcsFloatString(f)
}

// jcsFloatString formats a float per ECMAScript §7.1.12.1 as required by
// RFC 8785 §3.2.2.3. Go's strconv 'g' with -1 precision produces the
// shortest round-trip representation, which matches ECMAScript for finite
// numbers.
func jcsFloatString(f float64) (string, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "", fmt.Errorf("audit: JCS: NaN/Inf not permitted")
	}
	if f == 0 {
		return "0", nil
	}
	// If integral and within safe integer range, emit as integer.
	if f == math.Trunc(f) && math.Abs(f) < 1e21 {
		return strconv.FormatFloat(f, 'f', -1, 64), nil
	}
	return strconv.FormatFloat(f, 'g', -1, 64), nil
}

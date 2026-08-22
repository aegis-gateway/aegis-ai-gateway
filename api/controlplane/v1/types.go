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
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// TimestampFormat is the canonical wire format for every timestamp in this
// package: RFC 3339, UTC, exactly six fractional digits.
//
// The precision is not cosmetic. audit_checkpoints.sealed_at is hashed as
// microseconds since the Unix epoch (docs/AUDIT-INTEGRITY.md section 3), so a
// wire format that truncated to seconds or extended to nanoseconds would
// produce a timestamp from which the original checkpoint_hash cannot be
// recomputed. PostgreSQL TIMESTAMPTZ stores microseconds, which is exactly
// what this format preserves.
const TimestampFormat = "2006-01-02T15:04:05.000000Z"

// Timestamp is a time.Time that marshals to and from [TimestampFormat].
type Timestamp struct {
	time.Time
}

// NewTimestamp returns t truncated to microseconds in UTC, which is the only
// precision this protocol transmits. Truncating at construction means a value
// compared after a round trip equals the value that was sent.
func NewTimestamp(t time.Time) Timestamp {
	return Timestamp{t.UTC().Truncate(time.Microsecond)}
}

// MarshalJSON writes the timestamp in [TimestampFormat].
func (t Timestamp) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.UTC().Format(TimestampFormat))
}

// UnmarshalJSON reads a timestamp in [TimestampFormat]. It rejects any other
// spelling of the same instant, including offsets other than Z and fractional
// digit counts other than six, so that what a verifier reads back is byte
// identical to what the sealer wrote.
func (t *Timestamp) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("timestamp: %w", err)
	}
	parsed, err := time.Parse(TimestampFormat, s)
	if err != nil {
		return fmt.Errorf("timestamp %q is not %s: %w", s, TimestampFormat, err)
	}
	t.Time = parsed.UTC()
	return nil
}

// HashHex is a hash digest carried as lowercase hexadecimal.
//
// Hex rather than base64 because every value of this type also appears in
// aegis-migrate verify-chain output and in docs/AUDIT-INTEGRITY.md examples,
// and an auditor comparing the two should not have to transcode.
type HashHex string

// digestLenSHA256 is the byte length of a SHA-256 digest.
const digestLenSHA256 = 32

// NewHashHex encodes raw digest bytes as a [HashHex].
func NewHashHex(raw []byte) HashHex {
	return HashHex(hex.EncodeToString(raw))
}

// Bytes decodes the digest. It returns an error rather than a zero value on
// malformed input so a caller cannot accidentally compare against empty bytes.
func (h HashHex) Bytes() ([]byte, error) {
	return hex.DecodeString(string(h))
}

// Validate reports whether h is lowercase hex of exactly wantLen bytes.
func (h HashHex) Validate(field string, wantLen int) error {
	if string(h) != strings.ToLower(string(h)) {
		return fmt.Errorf("%s must be lowercase hexadecimal", field)
	}
	raw, err := hex.DecodeString(string(h))
	if err != nil {
		return fmt.Errorf("%s is not hexadecimal: %w", field, err)
	}
	if len(raw) != wantLen {
		return fmt.Errorf("%s must be %d bytes (%d hex characters), got %d bytes",
			field, wantLen, wantLen*2, len(raw))
	}
	return nil
}

// GenesisPrevHash is the value a gateway sends as PrevCheckpointHash for the
// first checkpoint in its chain: 32 zero bytes.
//
// docs/AUDIT-INTEGRITY.md section 3 defines this as a normative constant. It is
// not null and not an empty string, because the checkpoint hash input is a
// fixed 96 bytes and a shorter predecessor would change the length.
const GenesisPrevHash HashHex = "0000000000000000000000000000000000000000000000000000000000000000"

// HashAlgorithm names the digest function used for the Merkle tree and the
// checkpoint chain hash.
type HashAlgorithm string

// HashAlgorithmSHA256 is the only algorithm the sealer currently emits.
//
// The field exists even with one legal value because verification may happen
// years after sealing, potentially by a tool that has never seen this release.
// A stored attestation that does not say which digest produced it is one
// migration away from being unverifiable.
const HashAlgorithmSHA256 HashAlgorithm = "sha-256"

// CanonicalizationSpec identifies how an audit event row was serialized before
// its leaf hash was taken.
type CanonicalizationSpec string

// CanonicalizationRFC8785V1 is JSON Canonicalization Scheme (RFC 8785) applied
// to the schema version 1 field set, per docs/AUDIT-INTEGRITY.md section 5.
// It matches the value the sealer writes to
// audit_checkpoints.canonicalization_spec.
const CanonicalizationRFC8785V1 CanonicalizationSpec = "rfc8785-v1"

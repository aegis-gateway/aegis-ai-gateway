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

package checkpoint

import (
	"fmt"
	"time"
)

// timestampFormat is the canonical RFC 3339 format for audit event timestamps.
// Microsecond precision, UTC Z suffix, per docs/AUDIT-INTEGRITY.md §5.
const timestampFormat = "2006-01-02T15:04:05.000000Z"

// AuditEventRow holds the fields of audit_events covered by the leaf hash at
// hash_schema_version=2, the set migration 013 established.
//
// Version 2 replaced the single `metadata` JSONB field of version 1 with the
// twelve typed columns below. There is no version-1 variant of this struct
// because there cannot be a database that needs one: migration 013 refuses to
// run while any version-1 checkpoint exists, so a schema that has the version-2
// columns provably has no version-1 chain left to verify.
type AuditEventRow struct {
	ID             int64
	RequestID      string
	Timestamp      time.Time
	EventType      string
	OrganizationID *string
	TeamID         *string
	UserID         *string
	APIKeyID       *string // UUID as string
	IPAddress      *string
	UserAgent      *string
	Endpoint       *string
	Method         *string
	StatusCode     *int32
	ErrorMessage   *string

	// Promoted from metadata by migration 013.
	APIKeyPrefix   *string
	LimitDimension *string
	LimitValue     *int64
	SpentCents     *int64
	LimitCents     *int64
	FilterType     *string
	Reason         *string
	Provider       *string
	Model          *string
	Mode           *string
	Operation      *string
	ErrorDetail    *string
}

// EventColumns is the audit_events column list the leaf hash covers at
// hash_schema_version=2, in the order scanEventRows expects.
//
// It is one string rather than three copies because the sealer and both verifier
// queries must read exactly the same columns in exactly the same order. Three
// hand-maintained lists drift, and this particular drift is invisible: the
// scan still succeeds, the leaf hash silently covers a different field set, and
// the Merkle mismatch that follows is reported as tampering.
const EventColumns = `id, request_id, timestamp, event_type,
	organization_id, team_id, user_id, api_key_id,
	ip_address, user_agent, endpoint, method,
	status_code, error_message,
	api_key_prefix, limit_dimension, limit_value,
	spent_cents, limit_cents, filter_type, reason,
	provider, model, mode, operation, error_detail`

// EventLeafHash computes the RFC 6962 leaf hash for a single audit_events row.
//
//	leaf_hash = SHA-256(0x00 || JCS(event))
//
// JCS is applied to the event serialized as a JSON object with keys in Unicode
// code point order and timestamps in RFC 3339 microsecond UTC format.
// This is hash_schema_version=2 per docs/AUDIT-INTEGRITY.md §5.1.
func EventLeafHash(row AuditEventRow) ([]byte, error) {
	jcsBytes, err := eventJCS(row)
	if err != nil {
		return nil, fmt.Errorf("leaf hash for event %d: %w", row.ID, err)
	}
	return LeafHash(jcsBytes), nil
}

// eventJCS returns the RFC 8785 canonical JSON for one audit_events row at
// hash_schema_version=2. JCSEncode orders keys by Unicode code point, so the
// order they appear in this literal has no effect on the output.
//
// The twenty-six fields are the fourteen carried over from version 1 (all of
// version 1 except `metadata`) plus the twelve columns migration 013 promoted
// out of it. Adding, removing or renaming any of them changes every leaf hash
// and therefore requires a version 3, not an edit here.
func eventJCS(row AuditEventRow) ([]byte, error) {
	obj := map[string]interface{}{
		"api_key_id":      nullableString(row.APIKeyID),
		"api_key_prefix":  nullableString(row.APIKeyPrefix),
		"endpoint":        nullableString(row.Endpoint),
		"error_detail":    nullableString(row.ErrorDetail),
		"error_message":   nullableString(row.ErrorMessage),
		"event_type":      row.EventType,
		"filter_type":     nullableString(row.FilterType),
		"id":              row.ID,
		"ip_address":      nullableString(row.IPAddress),
		"limit_cents":     nullableInt64(row.LimitCents),
		"limit_dimension": nullableString(row.LimitDimension),
		"limit_value":     nullableInt64(row.LimitValue),
		"method":          nullableString(row.Method),
		"mode":            nullableString(row.Mode),
		"model":           nullableString(row.Model),
		"operation":       nullableString(row.Operation),
		"organization_id": nullableString(row.OrganizationID),
		"provider":        nullableString(row.Provider),
		"reason":          nullableString(row.Reason),
		"request_id":      row.RequestID,
		"spent_cents":     nullableInt64(row.SpentCents),
		"status_code":     nullableInt32(row.StatusCode),
		"team_id":         nullableString(row.TeamID),
		"timestamp":       row.Timestamp.UTC().Format(timestampFormat),
		"user_agent":      nullableString(row.UserAgent),
		"user_id":         nullableString(row.UserID),
	}
	return JCSEncode(obj)
}

func nullableString(s *string) interface{} {
	if s == nil {
		return nil
	}
	return *s
}

func nullableInt32(i *int32) interface{} {
	if i == nil {
		return nil
	}
	return int64(*i)
}

// nullableInt64 mirrors nullableInt32 for the three counter columns migration
// 013 promoted. It returns int64 rather than the pointer so JCSEncode sees a
// number, and nil rather than 0 for an absent value: an event that carries no
// limit is not an event with a limit of zero.
func nullableInt64(i *int64) interface{} {
	if i == nil {
		return nil
	}
	return *i
}

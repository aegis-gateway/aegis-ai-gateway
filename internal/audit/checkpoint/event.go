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
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// timestampFormat is the canonical RFC 3339 format for audit event timestamps.
// Microsecond precision, UTC Z suffix, per docs/AUDIT-INTEGRITY.md §5.
const timestampFormat = "2006-01-02T15:04:05.000000Z"

// AuditEventRow holds the schema-v1 fields of audit_events (migration 005).
// This is the canonical set of fields included in the leaf hash at hash_schema_version=1.
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
	Metadata       []byte // raw JSONB bytes
}

// EventLeafHash computes the RFC 6962 leaf hash for a single audit_events row.
//
//	leaf_hash = SHA-256(0x00 || JCS(event))
//
// JCS is applied to the event serialized as a JSON object with keys in Unicode
// code point order and timestamps in RFC 3339 microsecond UTC format.
// This is hash_schema_version=1 per docs/AUDIT-INTEGRITY.md §5.
func EventLeafHash(row AuditEventRow) ([]byte, error) {
	jcsBytes, err := eventJCS(row)
	if err != nil {
		return nil, fmt.Errorf("leaf hash for event %d: %w", row.ID, err)
	}
	return LeafHash(jcsBytes), nil
}

// eventJCS returns the RFC 8785 canonical JSON for one audit_events row (schema v1).
// Fields are ordered by Unicode code point (lexicographic for ASCII names):
// api_key_id, endpoint, error_message, event_type, id, ip_address, metadata,
// method, organization_id, request_id, status_code, team_id, timestamp, user_agent, user_id
func eventJCS(row AuditEventRow) ([]byte, error) {
	metaVal, err := decodeJSONBMetadata(row.Metadata)
	if err != nil {
		return nil, fmt.Errorf("metadata unmarshal event %d: %w", row.ID, err)
	}

	obj := map[string]interface{}{
		"api_key_id":      nullableString(row.APIKeyID),
		"endpoint":        nullableString(row.Endpoint),
		"error_message":   nullableString(row.ErrorMessage),
		"event_type":      row.EventType,
		"id":              row.ID,
		"ip_address":      nullableString(row.IPAddress),
		"metadata":        metaVal,
		"method":          nullableString(row.Method),
		"organization_id": nullableString(row.OrganizationID),
		"request_id":      row.RequestID,
		"status_code":     nullableInt32(row.StatusCode),
		"team_id":         nullableString(row.TeamID),
		"timestamp":       row.Timestamp.UTC().Format(timestampFormat),
		"user_agent":      nullableString(row.UserAgent),
		"user_id":         nullableString(row.UserID),
	}
	return JCSEncode(obj)
}

// decodeJSONBMetadata unmarshals the raw JSONB bytes from PostgreSQL into an
// interface{} using json.Number to preserve integer precision across platforms.
// An empty or null metadata column produces an empty JSON object.
func decodeJSONBMetadata(raw []byte) (interface{}, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return map[string]interface{}{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v interface{}
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
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

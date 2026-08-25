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

package audit

import (
	"encoding/json"
	"unicode/utf8"
)

// Column limits for audit_events, as declared by migration 012.
//
// These are not advisory. PostgreSQL raises an error on varchar overflow rather
// than truncating, and writeEvent can only log that error, so an over-long value
// does not arrive truncated: it costs the entire audit row. Two of these columns
// are written on the unauthenticated auth-failure path, which would hand any
// caller a way to suppress their own audit record by sending a long header.
//
// So every value is clipped here, before the insert, and TestSchemaLimitsMatch
// fails if these numbers and the migration ever disagree.
const (
	// MaxIPAddress covers Go's http.Request.RemoteAddr, which is host:port. The
	// longest IPv6 form bracketed and with a port is 53 characters.
	MaxIPAddress = 64

	// MaxErrorMessage covers gateway-generated refusal text. The longest today
	// is 33 characters.
	MaxErrorMessage = 128

	// MaxUserAgent covers the caller-supplied User-Agent header. Real browser
	// user agents run past 200 characters.
	MaxUserAgent = 256

	// MaxMetadataBytes bounds the serialized JSONB, matching the
	// audit_events_metadata_bounded CHECK constraint.
	MaxMetadataBytes = 4096

	// MaxMetadataValue bounds any single string value inside metadata, so the
	// serialized whole stays under MaxMetadataBytes without the marshal step
	// having to guess which key was the large one.
	MaxMetadataValue = 512
)

// clip truncates s to at most max runes, never splitting a UTF-8 sequence.
//
// It counts runes rather than bytes because varchar(n) counts characters. A
// clipped value is marked with a trailing ellipsis so a reader can tell a
// truncated record from a short one, and the marker is included in the budget
// rather than added past it.
func clip(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	const ellipsis = "..."
	keep := max - len(ellipsis)
	if keep < 1 {
		// No room for both. Return runes only, still respecting the limit.
		keep = max
		return string([]rune(s)[:keep])
	}
	return string([]rune(s)[:keep]) + ellipsis
}

// clipMetadata bounds every string value in a metadata map, then bounds the
// serialized whole. Non-string values are left alone: the six Log* methods only
// ever put strings and integers in here, and an integer cannot run long.
//
// If the map still serializes past MaxMetadataBytes, the metadata is replaced
// with a marker rather than dropping the event. An audit row with a note saying
// its metadata was too large is worth more than no audit row.
func clipMetadata(m map[string]interface{}) map[string]interface{} {
	if len(m) == 0 {
		return m
	}
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		if s, ok := v.(string); ok {
			out[k] = clip(s, MaxMetadataValue)
			continue
		}
		out[k] = v
	}
	if encoded, err := json.Marshal(out); err == nil && len(encoded) <= MaxMetadataBytes {
		return out
	}
	return map[string]interface{}{"metadata_omitted": "exceeded size limit"}
}

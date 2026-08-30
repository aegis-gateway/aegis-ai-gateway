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
	"strings"
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

	// The detail columns migration 013 promoted out of the metadata JSONB.
	//
	// MaxReason is the widest at 512. It carries the joined policy deny message,
	// which Rego builds with concat("; ", deny), so it grows with the number of
	// rules that fire: the shipped RESTRICTED rule alone produces 119 characters
	// and two rules together produce 201.
	// MaxRequestID matches audit_events.request_id, which is VARCHAR(50).
	//
	// The request ID is caller-supplied via X-Request-ID, so it is the one
	// bounded column whose value an outsider chooses directly. It was missing
	// from this list, and an over-long header therefore cost the entire audit
	// row: the request succeeded, returned 200, and left nothing in the trail.
	// Now that every permitted request writes an event, that was reachable on
	// the happy path by any caller.
	MaxRequestID = 50
	// MaxEndpoint and MaxMethod match audit_events.endpoint VARCHAR(200) and
	// method VARCHAR(10).
	//
	// Both are derived from the request. Today every route is an exact match,
	// so the path can only be one of a handful of literals and neither can
	// overflow. They are clipped anyway because that stops being true the
	// moment someone adds a wildcard or path-parameter route, and the failure
	// mode is the one that already cost a request its whole audit row: an
	// over-long value is rejected by PostgreSQL rather than truncated.
	MaxEndpoint       = 200
	MaxMethod         = 10
	MaxAPIKeyPrefix   = 32
	MaxLimitDimension = 32
	MaxFilterType     = 32
	MaxReason         = 512
	MaxProvider       = 64
	MaxModel          = 128
	MaxMode           = 32
	MaxOperation      = 64
	MaxErrorDetail    = 512
)

// clip truncates s to at most max runes, never splitting a UTF-8 sequence, and
// returns valid UTF-8 whatever it was given.
//
// It counts runes rather than bytes because varchar(n) counts characters. A
// clipped value is marked with a trailing ellipsis so a reader can tell a
// truncated record from a short one, and the marker is included in the budget
// rather than added past it.
//
// The validity pass is not belt and braces. PostgreSQL rejects an invalid byte
// sequence outright ("invalid byte sequence for encoding UTF8"), and these
// columns are fed from sources that can carry arbitrary bytes: the User-Agent
// header, an API key prefix cut from a caller-supplied token, an error string.
// Before migration 013 those values went through json.Marshal on their way into
// the JSONB, which substituted U+FFFD; promoting them to columns removed that
// step, so the substitution has to happen here or the insert fails and the audit
// row is lost. Reproduced on PostgreSQL 16 with a single trailing 0xc3.
func clip(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "\uFFFD")
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

// clipPtr applies clip through a pointer, leaving nil as nil.
//
// nil and the empty string are different facts in these columns: nil means the
// event does not carry that detail, and "" would mean it carries an empty one.
// Clipping must not turn the first into the second.
func clipPtr(s *string, max int) *string {
	if s == nil {
		return nil
	}
	out := clip(*s, max)
	return &out
}

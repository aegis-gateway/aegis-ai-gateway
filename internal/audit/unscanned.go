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
	"sort"
	"strings"
)

// unscannedMediaTypes is the set of media types audit_events.unscanned_part_types
// may hold, and the only text that column ever receives besides
// [UnlistedMediaType]. Unexported so nothing outside this package can widen it
// at runtime; [IsUnscannedMediaType] reads it.
//
// It is the gateway's image allowlist: the types a route that opts in to
// unscanned content parts admits. The column is sealed and exported, so it must
// carry nothing a caller chose. A media type looks like metadata, but a
// caller-declared one is free text with a slash in it, and sealing it verbatim
// would be a channel for arbitrary text into an immutable record.
var unscannedMediaTypes = map[string]bool{
	"image/gif":  true,
	"image/jpeg": true,
	"image/png":  true,
	"image/webp": true,
}

// IsUnscannedMediaType reports whether t, exactly as given, is one of the media
// types the unscanned_part_types column may record.
func IsUnscannedMediaType(t string) bool { return unscannedMediaTypes[t] }

// UnlistedMediaType is recorded in place of any media type outside
// [IsUnscannedMediaType].
//
// It should never be written: the gateway admits only allowlisted types, so a
// type outside the list here means a producer passed something it should not
// have. Recording the sentinel rather than dropping the entry keeps the record
// honest about how many distinct types were counted without letting the
// unexpected value itself through. The parentheses keep it from ever colliding
// with a real media type, as [UnconfiguredModel] does for a model name.
const UnlistedMediaType = "(unlisted)"

// normalizeUnscanned enforces the unscanned-part columns' contract on an event
// before it is written, whatever produced it.
//
//   - NULL means none. If the part count is nil or not positive, all three
//     columns are written NULL. A sealed 0 for "no unscanned parts" would be a
//     second encoding of the same fact, and a negative count would fail the
//     CHECK in migration 018 and lose the whole audit row.
//   - A negative byte total is dropped to NULL for the same reason: losing the
//     row is worse than losing one measurement.
//   - Media types are trimmed, lower-cased, mapped onto the allowlist,
//     de-duplicated, sorted and comma-joined, then clipped to the column. An
//     empty result is NULL.
func normalizeUnscanned(e *Event) {
	if e.UnscannedParts == nil || *e.UnscannedParts <= 0 {
		e.UnscannedParts, e.UnscannedBytes, e.UnscannedPartTypes = nil, nil, nil
		return
	}
	if e.UnscannedBytes != nil && *e.UnscannedBytes < 0 {
		e.UnscannedBytes = nil
	}
	if e.UnscannedPartTypes != nil {
		e.UnscannedPartTypes = strPtr(clip(normalizeMediaTypes(*e.UnscannedPartTypes), MaxUnscannedPartTypes))
	}
}

// normalizeMediaTypes returns the canonical form of a comma-separated list of
// media types: each mapped onto [IsUnscannedMediaType] or [UnlistedMediaType],
// de-duplicated, sorted, and joined with "," and no spaces.
//
// Sorted and de-duplicated so that two requests carrying the same kinds of
// part produce the same sealed value regardless of order, and so the value
// says which types were present rather than leaking their sequence.
func normalizeMediaTypes(list string) string {
	seen := map[string]bool{}
	for _, raw := range strings.Split(list, ",") {
		t := strings.ToLower(strings.TrimSpace(raw))
		if t == "" {
			continue
		}
		if !unscannedMediaTypes[t] {
			t = UnlistedMediaType
		}
		seen[t] = true
	}
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

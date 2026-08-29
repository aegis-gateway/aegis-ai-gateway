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
	"testing"
)

// The request ID is the one bounded audit column whose value a caller supplies
// directly, via X-Request-ID. PostgreSQL rejects an over-long value rather than
// truncating it, so an unbounded id cost the whole audit row: the request
// returned 200 and left no attested record. It was missing from the clip list.
//
// The gateway middleware bounds it too, so this is the backstop. Both exist
// because a dropped row is invisible: there is no error the caller sees and
// nothing downstream can reconstruct it.
func TestClip_BoundsTheRequestIDToTheColumn(t *testing.T) {
	if got := len(clip(strings.Repeat("X", 200), MaxRequestID)); got != MaxRequestID {
		t.Errorf("clipped length = %d, want %d", got, MaxRequestID)
	}
	if got := clip("req_short", MaxRequestID); got != "req_short" {
		t.Errorf("a short id must pass through unchanged, got %q", got)
	}
}

// MaxRequestID has to match the column or the clip is decorative.
func TestMaxRequestID_MatchesTheColumnWidth(t *testing.T) {
	// migrations/005 declares request_id VARCHAR(50) on audit_events.
	const columnWidth = 50
	if MaxRequestID != columnWidth {
		t.Errorf("MaxRequestID = %d but audit_events.request_id is VARCHAR(%d); "+
			"an insert with a longer id fails and the row is lost", MaxRequestID, columnWidth)
	}
}

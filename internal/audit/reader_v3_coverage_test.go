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
	"reflect"
	"testing"
	"time"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/audit/checkpoint"
)

// Every field the version-3 leaf hash commits to must be readable through the
// audit API, or an auditor cannot reconstruct the leaf they are asked to trust.
//
// This failed twice while the columns were being added: first for the six
// outcome columns, then for user_agent, which had been outside the read path
// since before this schema version and only became material once the API was
// claimed to support reconstruction. Comparing the two field sets directly is
// the check that would have caught both.
func TestEventRow_CoversEveryFieldTheV3LeafHashes(t *testing.T) {
	// The leaf's field set, taken from the encoder itself rather than restated.
	encoded, err := checkpoint.EventLeafJCSV3ForTest(checkpoint.AuditEventRow{
		ID:        1,
		Timestamp: time.Unix(0, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("encoding a leaf: %v", err)
	}
	var leaf map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &leaf); err != nil {
		t.Fatalf("decoding the leaf: %v", err)
	}

	exposed := map[string]bool{}
	rt := reflect.TypeOf(EventRow{})
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("json")
		for j := 0; j < len(tag); j++ {
			if tag[j] == ',' {
				tag = tag[:j]
				break
			}
		}
		if tag != "" && tag != "-" {
			exposed[tag] = true
		}
	}

	var missing []string
	for field := range leaf {
		if !exposed[field] {
			missing = append(missing, field)
		}
	}
	if len(missing) > 0 {
		t.Errorf("the version-3 leaf commits to %d fields and EventRow omits %v; "+
			"an auditor reading GET /aegis/v1/audit/events cannot reconstruct the leaf",
			len(leaf), missing)
	}
}

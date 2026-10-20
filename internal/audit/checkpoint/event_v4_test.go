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
	"testing"

	controlplanev1 "github.com/aegis-gateway/aegis-ai-gateway/api/controlplane/v1"
)

// v4Columns are the three columns migration 018 added, in EventColumnsV4 order.
var v4Columns = []string{"unscanned_parts", "unscanned_bytes", "unscanned_part_types"}

func v4Row() AuditEventRow {
	row := v3Row()
	parts, size := int64(2), int64(48213)
	types := "image/jpeg,image/png"
	row.UnscannedParts = &parts
	row.UnscannedBytes = &size
	row.UnscannedPartTypes = &types
	return row
}

func stripV4(row AuditEventRow) AuditEventRow {
	row.UnscannedParts, row.UnscannedBytes, row.UnscannedPartTypes = nil, nil, nil
	return row
}

// The claim migration 018 rests on, for both earlier versions still verifiable:
// adding columns does not invalidate an existing chain. A version-2 and a
// version-3 leaf must hash identically whether or not the version-4 columns
// hold values, because eventJCS and eventJCSV3 list their keys explicitly
// rather than walking the struct.
//
// If this fails, every version-2 or version-3 checkpoint on an upgraded
// database becomes unverifiable and the migration must refuse to run the way
// 013 did.
func TestEventLeafHash_V2AndV3AreUnaffectedByTheV4Columns(t *testing.T) {
	populated := v4Row()
	bare := stripV4(populated)

	for _, tc := range []struct {
		name string
		leaf func(AuditEventRow) ([]byte, error)
	}{
		{"version 2", EventLeafHash},
		{"version 3", EventLeafHashV3},
	} {
		a, err := tc.leaf(populated)
		if err != nil {
			t.Fatalf("%s: hashing populated row: %v", tc.name, err)
		}
		b, err := tc.leaf(bare)
		if err != nil {
			t.Fatalf("%s: hashing bare row: %v", tc.name, err)
		}
		if !bytes.Equal(a, b) {
			t.Errorf("the %s leaf hash changed when the version-4 columns were populated; "+
				"every %s checkpoint on an upgraded database would become unverifiable",
				tc.name, tc.name)
		}
	}
}

// The canonical bytes, not only the digest: a version-3 encoding of a row with
// the version-4 fields set must not mention them at all.
func TestEventJCSV3_OmitsTheV4Keys(t *testing.T) {
	encoded, err := eventJCSV3(v4Row())
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	for _, k := range v4Columns {
		if bytes.Contains(encoded, []byte(`"`+k+`"`)) {
			t.Errorf("the version-3 encoding carries %q", k)
		}
	}
}

// The converse: the version-4 leaf must actually cover the new columns, or the
// bump bought nothing.
func TestEventLeafHashV4_CoversTheNewColumns(t *testing.T) {
	base := v4Row()
	h0, err := EventLeafHashV4(base)
	if err != nil {
		t.Fatalf("hashing: %v", err)
	}

	for _, tc := range []struct {
		field  string
		mutate func(*AuditEventRow)
	}{
		{"unscanned_parts", func(r *AuditEventRow) { v := int64(3); r.UnscannedParts = &v }},
		{"unscanned_bytes", func(r *AuditEventRow) { v := int64(1); r.UnscannedBytes = &v }},
		{"unscanned_part_types", func(r *AuditEventRow) { v := "image/png"; r.UnscannedPartTypes = &v }},
		// NULL and a value must hash differently, or "no unscanned parts" and
		// "some" would be indistinguishable in the sealed record.
		{"unscanned_parts to null", func(r *AuditEventRow) { r.UnscannedParts = nil }},
		{"unscanned_part_types to null", func(r *AuditEventRow) { r.UnscannedPartTypes = nil }},
	} {
		mutated := base
		tc.mutate(&mutated)
		h, err := EventLeafHashV4(mutated)
		if err != nil {
			t.Fatalf("hashing %s: %v", tc.field, err)
		}
		if bytes.Equal(h0, h) {
			t.Errorf("changing %s left the version-4 leaf hash unchanged, so it is not covered", tc.field)
		}
	}
}

// The version-4 leaf still covers everything version 3 did.
func TestEventLeafHashV4_StillCoversTheV3Columns(t *testing.T) {
	base := v4Row()
	h0, err := EventLeafHashV4(base)
	if err != nil {
		t.Fatalf("hashing: %v", err)
	}
	mutated := base
	v := "gpt-4o"
	mutated.ModelServed = &v
	h, err := EventLeafHashV4(mutated)
	if err != nil {
		t.Fatalf("hashing: %v", err)
	}
	if bytes.Equal(h0, h) {
		t.Error("changing model_served left the version-4 leaf hash unchanged")
	}
}

// A version-4 leaf and a version-3 leaf over the same row must differ, even
// when the new columns are all NULL: the key set differs, so the encoding does.
func TestEventLeafHashV4_DiffersFromV3(t *testing.T) {
	for name, row := range map[string]AuditEventRow{
		"populated": v4Row(),
		"all null":  stripV4(v4Row()),
	} {
		v3, err := EventLeafHashV3(row)
		if err != nil {
			t.Fatalf("%s v3: %v", name, err)
		}
		v4, err := EventLeafHashV4(row)
		if err != nil {
			t.Fatalf("%s v4: %v", name, err)
		}
		if bytes.Equal(v3, v4) {
			t.Errorf("%s: the version-4 leaf equals the version-3 leaf over the same row", name)
		}
	}
}

// Pins the field count and the exact key set, so adding or renaming a column
// without cutting a version 5 fails here rather than in a Merkle mismatch that
// reads as tampering.
func TestEventJCSV4_HasExactlyThirtyFiveFields(t *testing.T) {
	encoded, err := eventJCSV4(v4Row())
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &obj); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(obj) != 35 {
		t.Errorf("version-4 leaf covers %d fields, want 35", len(obj))
	}

	// Every version-3 key, unchanged.
	v3, err := eventJCSV3(v3Row())
	if err != nil {
		t.Fatalf("encoding v3: %v", err)
	}
	var v3obj map[string]json.RawMessage
	if err := json.Unmarshal(v3, &v3obj); err != nil {
		t.Fatalf("decoding v3: %v", err)
	}
	for k := range v3obj {
		if _, ok := obj[k]; !ok {
			t.Errorf("version-4 leaf is missing version-3 field %q", k)
		}
	}
	for _, k := range v4Columns {
		if _, ok := obj[k]; !ok {
			t.Errorf("version-4 leaf is missing %q", k)
		}
	}

	// No content and no content hash: ADR 0013. A digest of an unscanned part
	// is a fingerprint that lets anyone holding a candidate image confirm it
	// was sent, so the leaf must not grow one by accident.
	for k := range obj {
		for _, banned := range []string{"hash", "digest", "sha", "content", "data", "url"} {
			if bytes.Contains([]byte(k), []byte(banned)) {
				t.Errorf("version-4 leaf carries %q, which looks like content or a content fingerprint", k)
			}
		}
	}
}

// The exact encoding of the three new keys. Absent is null, never 0 or "".
func TestEventJCSV4_EncodesTheNewFields(t *testing.T) {
	encoded, err := eventJCSV4(v4Row())
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	for _, want := range []string{
		`"unscanned_bytes":48213`,
		`"unscanned_part_types":"image/jpeg,image/png"`,
		`"unscanned_parts":2`,
	} {
		if !bytes.Contains(encoded, []byte(want)) {
			t.Errorf("encoding lacks %s:\n%s", want, encoded)
		}
	}

	bare, err := eventJCSV4(stripV4(v4Row()))
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	for _, want := range []string{
		`"unscanned_bytes":null`,
		`"unscanned_part_types":null`,
		`"unscanned_parts":null`,
	} {
		if !bytes.Contains(bare, []byte(want)) {
			t.Errorf("encoding of a row with no unscanned parts lacks %s:\n%s", want, bare)
		}
	}
}

// EventColumnsV4 must extend V3 rather than restate it, and end with the three
// columns in the order scanEventRowsV4 binds them.
func TestEventColumnsV4_ExtendsV3(t *testing.T) {
	if !bytes.HasPrefix([]byte(EventColumnsV4), []byte(EventColumnsV3)) {
		t.Error("EventColumnsV4 does not begin with EventColumnsV3, so the shared columns can drift")
	}
	tail := EventColumnsV4[len(EventColumnsV3):]
	last := -1
	for _, c := range v4Columns {
		i := bytes.Index([]byte(tail), []byte(c))
		if i < 0 {
			t.Errorf("EventColumnsV4 is missing %q", c)
			continue
		}
		if i < last {
			t.Errorf("%q is out of order in EventColumnsV4; scanEventRowsV4 binds positionally", c)
		}
		last = i
	}
}

// One table selects the columns, scanner and encoder for each version. Pin
// that each version gets its own, and that the sealer's version is one of them.
func TestLeafFieldSetFor_SelectsPerVersion(t *testing.T) {
	row := v4Row()
	for _, tc := range []struct {
		version int32
		columns string
		leaf    func(AuditEventRow) ([]byte, error)
	}{
		{controlplanev1.HashSchemaVersion2, EventColumns, EventLeafHash},
		{controlplanev1.HashSchemaVersion3, EventColumnsV3, EventLeafHashV3},
		{controlplanev1.HashSchemaVersion4, EventColumnsV4, EventLeafHashV4},
	} {
		fs, ok := leafFieldSetFor(tc.version)
		if !ok {
			t.Errorf("version %d: no field set", tc.version)
			continue
		}
		if fs.columns != tc.columns {
			t.Errorf("version %d: selects the wrong column list", tc.version)
		}
		got, err := fs.leaf(row)
		if err != nil {
			t.Fatalf("version %d: %v", tc.version, err)
		}
		want, err := tc.leaf(row)
		if err != nil {
			t.Fatalf("version %d: %v", tc.version, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("version %d: selects the wrong leaf encoder", tc.version)
		}
	}
	for _, v := range []int32{0, controlplanev1.HashSchemaVersion1, 5} {
		if _, ok := leafFieldSetFor(v); ok {
			t.Errorf("version %d: returned a field set this build cannot have", v)
		}
	}
	if sealVersion != controlplanev1.HashSchemaVersion4 {
		t.Errorf("sealVersion = %d, want %d", sealVersion, controlplanev1.HashSchemaVersion4)
	}
	if _, ok := leafFieldSetFor(sealVersion); !ok {
		t.Error("the sealer's own version has no field set")
	}
}

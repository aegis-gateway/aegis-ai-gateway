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

// This file extends the zero-retention conformance coverage to unscanned
// content parts, which hash_schema_version 4 records.
//
// An image part forwarded unscanned is payload by the same reasoning as a
// prompt: it is content the caller sent. The sealed record may say that such
// parts left the gateway, how many and how big, and which allowlisted media
// types they were. It may not carry the content, and it may not carry a hash
// of the content either: a digest is a fingerprint that lets anyone holding a
// candidate image confirm it was sent, which discloses content by another
// route. ADR 0013.
//
// no_payload_test.go and tool_no_payload_test.go are not modified; this is
// additive, and reuses their migration-walking patterns.
package audit_test

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/audit"
)

// fingerprintColumnPattern matches names that suggest a digest of content.
// Unanchored, unlike payloadColumnPattern, because "image_sha256" or
// "content_hash" is exactly as bad as "hash".
var fingerprintColumnPattern = regexp.MustCompile(
	`(?i)(hash|digest|sha\d*|md5|fingerprint|checksum|phash|perceptual)`)

// unscannedColumnsAllowed is every audit_events column that describes unscanned
// content parts, with the reason each one is metadata rather than payload.
// A new unscanned_* column fails TestNoPayload_UnscannedColumnsAreAllowlisted
// until it is added here with its own justification.
var unscannedColumnsAllowed = map[string]string{
	"unscanned_parts": "a count the gateway took; says how many parts went out " +
		"unscanned, nothing about what they contained",
	"unscanned_bytes": "a summed decoded size the gateway measured; many images " +
		"share a size, so a match does not confirm a candidate was sent (a " +
		"mismatch can rule one out; known-limitations 2.17 states this)",
	"unscanned_part_types": "media types from the gateway allowlist only " +
		"(image/png, image/jpeg, image/gif, image/webp, or the (unlisted) " +
		"sentinel); never caller text, enforced by normalizeUnscanned",
}

// auditEventsColumns returns every column any up migration adds to
// audit_events, by CREATE TABLE or ALTER TABLE ... ADD, mapped to the file
// that adds it.
func auditEventsColumns(t *testing.T) map[string]string {
	t.Helper()
	paths, err := filepath.Glob("../../migrations/*.up.sql")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no up migrations found (err=%v)", err)
	}
	cols := map[string]string{}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("cannot read migration %s: %v", path, err)
		}
		for _, stmt := range strings.Split(string(data), ";") {
			m := auditTablePattern.FindStringSubmatch(stmt)
			if m == nil || !strings.EqualFold(m[2], "audit_events") {
				continue
			}
			if strings.HasPrefix(strings.ToUpper(m[1]), "CREATE") {
				for _, line := range strings.Split(stmt, "\n") {
					if c := sqlColumnDeclPattern.FindStringSubmatch(line); c != nil {
						cols[strings.ToLower(c[1])] = filepath.Base(path)
					}
				}
				continue
			}
			for _, c := range alterAddColumnPattern.FindAllStringSubmatch(stmt, -1) {
				if addNonColumnKeywords[strings.ToLower(c[1])] {
					continue
				}
				cols[strings.ToLower(c[1])] = filepath.Base(path)
			}
		}
	}
	return cols
}

// No column on audit_events may hold a digest of anything. The sealed record's
// own integrity hashes live on audit_checkpoints; audit_events holds none, and
// a content hash arriving there is the specific regression ADR 0013 rules out.
func TestNoPayload_AuditEventsHoldNoContentFingerprint(t *testing.T) {
	t.Parallel()
	cols := auditEventsColumns(t)
	// A positive control: audit_events has had more than thirty columns since
	// migration 016. Far fewer means the walk is not reading the schema, and
	// the check below would pass over nothing.
	if len(cols) < 30 {
		t.Fatalf("found only %d audit_events columns; the migration walk is not seeing the schema", len(cols))
	}
	for col, file := range cols {
		if fingerprintColumnPattern.MatchString(col) {
			t.Errorf("%s adds audit_events.%s, which looks like a content fingerprint. "+
				"A digest of an unscanned part lets anyone holding a candidate confirm it "+
				"was sent; record counts, sizes and allowlisted types only (ADR 0013)", file, col)
		}
	}
}

// The unscanned_* columns are exactly the allowlisted ones, and each is there.
// The positive half matters: if migration 018's columns stopped being found,
// the checks here would pass over a schema they were not reading.
func TestNoPayload_UnscannedColumnsAreAllowlisted(t *testing.T) {
	t.Parallel()
	cols := auditEventsColumns(t)
	var found []string
	for col := range cols {
		if !strings.HasPrefix(col, "unscanned") {
			continue
		}
		found = append(found, col)
		if _, ok := unscannedColumnsAllowed[col]; !ok {
			t.Errorf("%s adds audit_events.%s, which is not in unscannedColumnsAllowed. "+
				"Add it there with the reason it is metadata rather than payload, or remove it",
				cols[col], col)
		}
	}
	for col := range unscannedColumnsAllowed {
		if _, ok := cols[col]; !ok {
			t.Errorf("audit_events.%s is allowlisted but no migration adds it", col)
		}
	}
	sort.Strings(found)
	t.Logf("unscanned-part columns on audit_events: %v", found)
}

// unscannedPayloadFieldNames are field names that would mean a type carries an
// unscanned part's content, a reference to it, or a fingerprint of it. Exact
// names, so UnscannedBytes, a count of bytes, is not mistaken for "bytes".
var unscannedPayloadFieldNames = regexp.MustCompile(
	`(?i)^(image|images|imagedata|imageurl|imagebytes|data|url|base64|blob|bytes|` +
		`contenthash|imagehash|digest|sha256|fingerprint|checksum|perceptualhash)$`)

// The write type, the read type and the completion record must not gain a
// field that would carry an image, a reference to one, or its digest.
func TestNoPayload_AuditTypesCarryNoUnscannedContent(t *testing.T) {
	t.Parallel()
	for _, target := range []struct {
		name string
		typ  reflect.Type
	}{
		{"audit.Event", reflect.TypeOf(audit.Event{})},
		{"audit.EventRow", reflect.TypeOf(audit.EventRow{})},
		{"audit.CompletedRequest", reflect.TypeOf(audit.CompletedRequest{})},
	} {
		for i := range target.typ.NumField() {
			f := target.typ.Field(i)
			tag := strings.ReplaceAll(strings.Split(f.Tag.Get("json"), ",")[0], "_", "")
			if unscannedPayloadFieldNames.MatchString(f.Name) ||
				(tag != "" && unscannedPayloadFieldNames.MatchString(tag)) {
				t.Errorf("%s.%s may carry unscanned content or a reference to it; "+
					"the audit trail records counts, sizes and allowlisted types only",
					target.name, f.Name)
			}
			if fingerprintColumnPattern.MatchString(f.Name) {
				t.Errorf("%s.%s looks like a content fingerprint (ADR 0013)", target.name, f.Name)
			}
		}
	}
}

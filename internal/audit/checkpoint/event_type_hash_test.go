package checkpoint

import (
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"
)

// TestEventTypeValueDoesNotChangeTheFieldSet pins the property that let
// request_complete and provider_failure be introduced without a hash schema
// bump.
//
// A new event_type is a new VALUE in an existing field, not a new field, so the
// JCS field set is unchanged and hash_schema_version stays at 2. Adding a
// COLUMN is the opposite case: it changes the field set, changes every leaf
// hash, and requires version 3, which is why the allow event carries only
// existing columns. See ADR 0011 and docs/evidence/known-limitations.md 2.14.
//
// Both halves are asserted, because each alone would be misleading. The field
// count must not change, or the chain breaks. And the two hashes must differ,
// or event_type would not be attested at all and the new events could be
// relabelled after sealing without detection.
func TestEventTypeValueDoesNotChangeTheFieldSet(t *testing.T) {
	base := AuditEventRow{
		ID:        1,
		RequestID: "req-1",
		Timestamp: time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC),
		EventType: "filter_block",
	}
	newType := base
	newType.EventType = "request_complete"

	a, err := eventJCS(base)
	if err != nil {
		t.Fatal(err)
	}
	b, err := eventJCS(newType)
	if err != nil {
		t.Fatal(err)
	}

	var ma, mb map[string]json.RawMessage
	if err := json.Unmarshal(a, &ma); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &mb); err != nil {
		t.Fatal(err)
	}
	if len(ma) != len(mb) || len(ma) != 26 {
		t.Fatalf("field count changed: %d vs %d (want 26 both)", len(ma), len(mb))
	}
	t.Logf("field set unchanged at %d fields", len(ma))

	ha, _ := EventLeafHash(base)
	hb, _ := EventLeafHash(newType)
	if hex.EncodeToString(ha) == hex.EncodeToString(hb) {
		t.Error("event_type is not covered by the leaf hash")
	}
	t.Logf("filter_block      leaf: %s", hex.EncodeToString(ha)[:16])
	t.Logf("request_complete  leaf: %s", hex.EncodeToString(hb)[:16])
}

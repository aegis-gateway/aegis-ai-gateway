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

package auth

import (
	"encoding/json"
	"testing"
)

// These call parseAllowedModels, the function lookupDB uses, rather than a copy
// of its logic. A test that reimplements the parser proves the copy behaves,
// which is the failure mode that made an earlier test in this PR worthless.
func TestAllowedModels_MalformedValuesFailClosed(t *testing.T) {
	// Every one of these leaves the slice empty under a discarded error, and an
	// empty allowlist permits every model.
	for _, raw := range []string{
		`null`,         // decodes cleanly to nil: the dangerous case
		`{"a":1}`,      // an object
		`"aegis-fast"`, // a bare string
		`[`,            // truncated
		`{}`,           // empty object
	} {
		t.Run(raw, func(t *testing.T) {
			got, err := parseAllowedModels([]byte(raw))
			if err == nil {
				t.Errorf("decoded %s to %v with no error; an unreadable allowlist must "+
					"fail closed, because an empty one grants every model", raw, got)
			}
		})
	}
}

func TestAllowedModels_WellFormedValuesDecode(t *testing.T) {
	for raw, want := range map[string]int{
		`[]`:                          0,
		`["aegis-fast"]`:              1,
		`["aegis-fast","aegis-slow"]`: 2,
		`  ["aegis-fast"]  `:          1, // whitespace from JSONB formatting
	} {
		got, err := parseAllowedModels([]byte(raw))
		if err != nil {
			t.Errorf("%s: unexpected error %v", raw, err)
			continue
		}
		if len(got) != want {
			t.Errorf("%s: got %d entries, want %d", raw, len(got), want)
		}
	}
}

// An absent value is not a malformed one: a key that never set an allowlist is
// unrestricted by design, and rejecting that would revoke every model from
// every such key.
func TestAllowedModels_AbsentValueIsUnrestricted(t *testing.T) {
	got, err := parseAllowedModels(nil)
	if err != nil {
		t.Fatalf("an absent allowed_models must not be an error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want an empty allowlist", got)
	}
}

// The cache path must fail closed too.
//
// parseAllowedModels fixed the database read, and the Redis path decoded cached
// metadata straight into a KeyMetadata and returned it. An entry written before
// that fix holds "allowed_models":null for a key whose stored value was
// malformed, which decodes cleanly to an empty slice and grants every model.
// Validating inside UnmarshalJSON gives both paths the same guarantee, rather
// than leaving the second one to be remembered.
func TestKeyMetadata_CachedAllowlistFailsClosed(t *testing.T) {
	for _, raw := range []string{
		`{"id":"k","allowed_models":null}`,
		`{"id":"k","allowed_models":{"a":1}}`,
		`{"id":"k","allowed_models":"aegis-fast"}`,
	} {
		t.Run(raw, func(t *testing.T) {
			var km KeyMetadata
			if err := json.Unmarshal([]byte(raw), &km); err == nil {
				t.Errorf("decoded to AllowedModels=%v with no error; an unreadable "+
					"cached allowlist grants every model", km.AllowedModels)
			}
		})
	}
}

func TestKeyMetadata_CachedAllowlistRoundTrips(t *testing.T) {
	original := &KeyMetadata{ID: "k", AllowedModels: []string{"aegis-fast", "aegis-slow"}}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var back KeyMetadata
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("a well-formed cache entry must decode: %v", err)
	}
	if len(back.AllowedModels) != 2 {
		t.Errorf("got %v, want the two configured aliases", back.AllowedModels)
	}
}

// A key with no allowlist is unrestricted by design, and its cached form omits
// the field. Rejecting that would revoke every model from every such key, which
// is a worse outage than the defect being fixed.
func TestKeyMetadata_CachedAbsentAllowlistIsUnrestricted(t *testing.T) {
	var km KeyMetadata
	if err := json.Unmarshal([]byte(`{"id":"k"}`), &km); err != nil {
		t.Fatalf("an absent allowlist must decode: %v", err)
	}
	if len(km.AllowedModels) != 0 {
		t.Errorf("got %v, want an empty allowlist", km.AllowedModels)
	}
}

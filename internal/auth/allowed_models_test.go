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

// An ABSENT value is rejected, which reverses what an earlier version of this
// test asserted.
//
// The reasoning then was that absent is the schema default for a key with no
// allowlist. It is not: the default is '[]', an explicit empty array, and a nil
// byte slice means a SQL NULL that an import or a manual UPDATE left behind.
// Reading that as "no restriction" grants every model on a key whose
// restrictions could not be determined.
//
// It also made the two lookup paths disagree, because a nil slice marshals to
// "allowed_models":null in the cache, which the decoder refuses: the same key
// succeeded on a cache miss and failed on a cache hit.
func TestAllowedModels_AbsentValueIsRejected(t *testing.T) {
	if _, err := parseAllowedModels(nil); err == nil {
		t.Error("an absent allowed_models decoded without error; the no-allowlist " +
			"value is [], so nil means a SQL NULL and must fail closed")
	}
	// The genuine no-allowlist value still works.
	got, err := parseAllowedModels([]byte(`[]`))
	if err != nil {
		t.Fatalf("[] is the documented unrestricted value and must decode: %v", err)
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

// A cached entry with no allowed_models field is rejected, which also reverses
// an earlier assertion here.
//
// KeyMetadata always marshals the field, so its absence means the entry did not
// come from this type, and an unrestricted key is not a safe default for a value
// of unknown origin. A key that genuinely has no allowlist caches as [] and
// still decodes.
func TestKeyMetadata_CachedAbsentAllowlistIsRejected(t *testing.T) {
	var km KeyMetadata
	if err := json.Unmarshal([]byte(`{"id":"k"}`), &km); err == nil {
		t.Errorf("decoded to AllowedModels=%v with no error; this type always writes "+
			"the field, so its absence is not a value we produced", km.AllowedModels)
	}

	var unrestricted KeyMetadata
	if err := json.Unmarshal([]byte(`{"id":"k","allowed_models":[]}`), &unrestricted); err != nil {
		t.Fatalf("a genuinely unrestricted key must still decode: %v", err)
	}
	if len(unrestricted.AllowedModels) != 0 {
		t.Errorf("got %v, want an empty allowlist", unrestricted.AllowedModels)
	}
}

// KeyPrefix must never return the whole input.
//
// It is written to audit_events.api_key_prefix, which is sealed and served by
// the audit read API, so returning the credential puts it somewhere it can
// never be removed from. A generated key is always long enough that this did
// not arise, but an imported or manually provisioned one need not be.
func TestKeyPrefix_NeverReturnsTheWholeKey(t *testing.T) {
	for _, key := range []string{
		"aegis-dev-abcdefgh12345678abcdefgh12345678", // generated
		"short-key",             // shorter than 16, returned verbatim before
		"sixteenbyteskey0",      // exactly 16 with no second dash
		"imported_token_nodash", // no dashes at all
		"tiny",
		"",
	} {
		got := KeyPrefix(key)
		if key != "" && got == key {
			t.Errorf("KeyPrefix(%q) returned the key itself; this value is sealed "+
				"into audit_events.api_key_prefix", key)
		}
		if len(got) > len(key) {
			t.Errorf("KeyPrefix(%q) = %q, longer than its input", key, got)
		}
	}
}

// The generated format must still yield the documented display prefix, or the
// hardening above would break the value operators match against.
func TestKeyPrefix_KeepsTheDocumentedFormat(t *testing.T) {
	const key = "aegis-dev-abcdefgh12345678abcdefgh12345678"
	if got := KeyPrefix(key); got != "aegis-dev-abcdefgh" {
		t.Errorf("KeyPrefix(%q) = %q, want aegis-dev-abcdefgh", key, got)
	}
}

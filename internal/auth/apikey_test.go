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
	"strings"
	"testing"
)

func TestGenerateKey(t *testing.T) {
	key, err := GenerateKey("prod")
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}

	if !strings.HasPrefix(key, "aegis-prod-") {
		t.Errorf("key should start with 'aegis-prod-', got: %s", key)
	}

	// aegis-prod- is 11 chars, plus 32 random = 43 total
	if len(key) != 43 {
		t.Errorf("expected key length 43, got %d: %s", len(key), key)
	}

	// Ensure randomness: two keys should differ
	key2, _ := GenerateKey("prod")
	if key == key2 {
		t.Error("two generated keys should not be identical")
	}
}

func TestGenerateKey_DifferentEnv(t *testing.T) {
	key, err := GenerateKey("dev")
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}
	if !strings.HasPrefix(key, "aegis-dev-") {
		t.Errorf("key should start with 'aegis-dev-', got: %s", key)
	}
}

func TestHashKey(t *testing.T) {
	key := "aegis-prod-abcdefghijklmnopqrstuvwxyz012345"
	hash := HashKey(key)

	// SHA-256 produces 64-char hex string
	if len(hash) != 64 {
		t.Errorf("expected hash length 64, got %d", len(hash))
	}

	// Same input should produce same hash
	hash2 := HashKey(key)
	if hash != hash2 {
		t.Error("same key should produce same hash")
	}

	// Different input should produce different hash
	hash3 := HashKey("aegis-prod-different")
	if hash == hash3 {
		t.Error("different keys should produce different hashes")
	}
}

func TestKeyPrefix(t *testing.T) {
	tests := []struct {
		key      string
		expected string
	}{
		{"aegis-prod-abcdefghijklmnopqrstuvwxyz012345", "aegis-prod-abcdefgh"},
		{"aegis-dev-12345678901234567890123456789012", "aegis-dev-12345678"},

		// A short key returns a fragment, NOT the key. This case asserted
		// {"short", "short"} until 2026-08-30, which encoded the defect: the
		// result is written to audit_events.api_key_prefix, which is sealed and
		// served by the audit read API, so returning the input whole put a
		// credential somewhere it can never be removed from. A generated key is
		// always long enough that this never arose in practice, but an imported
		// or manually provisioned one need not be.
		{"short", "s"},
	}

	for _, tt := range tests {
		got := KeyPrefix(tt.key)
		if got != tt.expected {
			t.Errorf("KeyPrefix(%q) = %q, want %q", tt.key, got, tt.expected)
		}
	}
}

func TestHashKeyV2(t *testing.T) {
	key := "aegis-prod-abcdefghijklmnopqrstuvwxyz012345"
	pepper := "a-test-pepper-value-that-is-at-least-32-chars"

	hash := HashKeyV2(key, pepper)

	// HMAC-SHA256 produces a 64-char hex string
	if len(hash) != 64 {
		t.Errorf("expected hash length 64, got %d", len(hash))
	}

	// Deterministic
	if HashKeyV2(key, pepper) != hash {
		t.Error("same inputs should produce same hash")
	}

	// Different key → different hash
	if HashKeyV2("other-key", pepper) == hash {
		t.Error("different keys should produce different hashes")
	}

	// Different pepper → different hash
	if HashKeyV2(key, "different-pepper-value-that-is-32-chars!!") == hash {
		t.Error("different peppers should produce different hashes")
	}

	// V1 and V2 must not collide
	if HashKey(key) == hash {
		t.Error("HashKey and HashKeyV2 must produce different values for the same key")
	}
}

func TestVerifyKey_V1(t *testing.T) {
	key := "aegis-prod-abcdefghijklmnopqrstuvwxyz012345"
	pepper := "a-test-pepper-value-that-is-at-least-32-chars"
	hash := HashKey(key)

	if !VerifyKey(key, hash, 1, pepper) {
		t.Error("VerifyKey v1 should return true for matching key")
	}
	if VerifyKey("wrong-key", hash, 1, pepper) {
		t.Error("VerifyKey v1 should return false for wrong key")
	}
}

func TestVerifyKey_V2(t *testing.T) {
	key := "aegis-prod-abcdefghijklmnopqrstuvwxyz012345"
	pepper := "a-test-pepper-value-that-is-at-least-32-chars"
	hash := HashKeyV2(key, pepper)

	if !VerifyKey(key, hash, 2, pepper) {
		t.Error("VerifyKey v2 should return true for matching key+pepper")
	}
	if VerifyKey("wrong-key", hash, 2, pepper) {
		t.Error("VerifyKey v2 should return false for wrong key")
	}
	if VerifyKey(key, hash, 2, "wrong-pepper-value-that-is-at-least-32ch") {
		t.Error("VerifyKey v2 should return false for wrong pepper")
	}
}

func TestVerifyKey_UnknownVersion(t *testing.T) {
	if VerifyKey("any-key", "any-hash", 99, "any-pepper") {
		t.Error("VerifyKey with unknown version should return false")
	}
}

func TestVerifyKey_V1DoesNotAcceptV2Hash(t *testing.T) {
	key := "aegis-prod-abcdefghijklmnopqrstuvwxyz012345"
	pepper := "a-test-pepper-value-that-is-at-least-32-chars"
	v2Hash := HashKeyV2(key, pepper)

	// Version 1 verification against a v2 hash must fail
	if VerifyKey(key, v2Hash, 1, pepper) {
		t.Error("VerifyKey v1 must not verify a v2 hash")
	}
}

func TestParseDuration(t *testing.T) {
	tests := []struct {
		input   string
		wantErr bool
		hours   float64
	}{
		{"365d", false, 365 * 24},
		{"30d", false, 30 * 24},
		{"24h", false, 24},
		{"1h", false, 1},
		{"", true, 0},
	}

	for _, tt := range tests {
		dur, err := ParseDuration(tt.input)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseDuration(%q) should have errored", tt.input)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseDuration(%q) unexpected error: %v", tt.input, err)
			continue
		}
		if dur.Hours() != tt.hours {
			t.Errorf("ParseDuration(%q) = %v hours, want %v", tt.input, dur.Hours(), tt.hours)
		}
	}
}

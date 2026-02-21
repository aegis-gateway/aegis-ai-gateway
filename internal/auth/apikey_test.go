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
		{"short", "short"},
	}

	for _, tt := range tests {
		got := KeyPrefix(tt.key)
		if got != tt.expected {
			t.Errorf("KeyPrefix(%q) = %q, want %q", tt.key, got, tt.expected)
		}
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

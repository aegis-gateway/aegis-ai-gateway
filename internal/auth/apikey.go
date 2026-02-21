package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/big"
	"time"

	"github.com/af-corp/aegis-gateway/internal/types"
)

const alphanumeric = "abcdefghijklmnopqrstuvwxyz0123456789"

// GenerateKey creates a new API key with the format: aegis-{env}-{32 random alphanumeric chars}
func GenerateKey(env string) (string, error) {
	random, err := randomString(32)
	if err != nil {
		return "", fmt.Errorf("generate random: %w", err)
	}
	return fmt.Sprintf("aegis-%s-%s", env, random), nil
}

// HashKey returns the SHA-256 hex digest of an API key.
func HashKey(key string) string {
	h := sha256.Sum256([]byte(key))
	return fmt.Sprintf("%x", h)
}

// KeyPrefix extracts a display-safe prefix from a key: aegis-{env}-{first 8 chars}
func KeyPrefix(key string) string {
	// Key format: aegis-{env}-{32chars}
	// We want: aegis-{env}-{first 8 of random}
	if len(key) < 16 {
		return key
	}
	// Find the position after the second dash
	dashes := 0
	for i, c := range key {
		if c == '-' {
			dashes++
			if dashes == 2 {
				end := i + 9 // dash + 8 chars
				if end > len(key) {
					end = len(key)
				}
				return key[:end]
			}
		}
	}
	return key[:16]
}

func randomString(n int) (string, error) {
	b := make([]byte, n)
	max := big.NewInt(int64(len(alphanumeric)))
	for i := range b {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b[i] = alphanumeric[idx.Int64()]
	}
	return string(b), nil
}

// KeyMetadata holds the cached metadata for an API key.
type KeyMetadata struct {
	ID                   string              `json:"id"`
	OrganizationID       string              `json:"organization_id"`
	TeamID               string              `json:"team_id"`
	UserID               string              `json:"user_id,omitempty"`
	Name                 string              `json:"name"`
	MaxClassification    types.Classification `json:"max_classification"`
	AllowedModels        []string            `json:"allowed_models"`
	RPMLimit             *int                `json:"rpm_limit,omitempty"`
	TPMLimit             *int                `json:"tpm_limit,omitempty"`
	DailySpendLimitCents *int                `json:"daily_spend_limit_cents,omitempty"`
	ExpiresAt            time.Time           `json:"expires_at"`
}

func (km *KeyMetadata) MarshalJSON() ([]byte, error) {
	type Alias KeyMetadata
	return json.Marshal((*Alias)(km))
}

func (km *KeyMetadata) UnmarshalJSON(data []byte) error {
	type Alias KeyMetadata
	return json.Unmarshal(data, (*Alias)(km))
}

// ParseDuration parses a duration string like "365d", "30d", "24h".
func ParseDuration(s string) (time.Duration, error) {
	if len(s) == 0 {
		return 0, fmt.Errorf("empty duration")
	}
	last := s[len(s)-1]
	if last == 'd' {
		var days int
		_, err := fmt.Sscanf(s, "%dd", &days)
		if err != nil {
			return 0, fmt.Errorf("parse days: %w", err)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

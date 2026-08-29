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
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"time"

	"github.com/aegis-gateway/aegis-ai-gateway/internal/types"
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

// HashKey returns the SHA-256 hex digest of an API key (hash_version=1).
func HashKey(key string) string {
	h := sha256.Sum256([]byte(key))
	return fmt.Sprintf("%x", h)
}

// HashKeyV2 returns the HMAC-SHA256 hex digest of an API key using the given pepper (hash_version=2).
func HashKeyV2(key, pepper string) string {
	mac := hmac.New(sha256.New, []byte(pepper))
	mac.Write([]byte(key))
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyKey checks whether rawKey matches storedHash under the given hash_version.
// version=1 uses SHA-256; version=2 uses HMAC-SHA256 with pepper.
func VerifyKey(rawKey, storedHash string, version int, pepper string) bool {
	switch version {
	case 1:
		return hmac.Equal([]byte(HashKey(rawKey)), []byte(storedHash))
	case 2:
		return hmac.Equal([]byte(HashKeyV2(rawKey, pepper)), []byte(storedHash))
	default:
		return false
	}
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
	ID                   string               `json:"id"`
	OrganizationID       string               `json:"organization_id"`
	TeamID               string               `json:"team_id"`
	UserID               string               `json:"user_id,omitempty"`
	Name                 string               `json:"name"`
	MaxClassification    types.Classification `json:"max_classification"`
	AllowedModels        []string             `json:"allowed_models"`
	RPMLimit             *int                 `json:"rpm_limit,omitempty"`
	TPMLimit             *int                 `json:"tpm_limit,omitempty"`
	DailySpendLimitCents *int                 `json:"daily_spend_limit_cents,omitempty"`
	ExpiresAt            time.Time            `json:"expires_at"`
}

func (km *KeyMetadata) MarshalJSON() ([]byte, error) {
	type Alias KeyMetadata
	return json.Marshal((*Alias)(km))
}

// UnmarshalJSON decodes cached key metadata and validates the allowlist.
//
// The allowlist is an access control whose EMPTY value grants every model, so a
// representation that cannot be read must not decode to one. Validating here
// rather than at the call site means the Redis cache path and any future
// decoder get the same guarantee as the database path by construction; the two
// diverging is exactly how the cache kept serving unrestricted keys after the
// database read was fixed.
func (km *KeyMetadata) UnmarshalJSON(data []byte) error {
	type Alias KeyMetadata
	if err := json.Unmarshal(data, (*Alias)(km)); err != nil {
		return err
	}

	// The decoded slice cannot distinguish an absent allowlist from a null or
	// malformed one, so the raw field is re-read and validated by the same
	// function the database path uses. An absent field is rejected too: this
	// type always marshals allowed_models, so its absence means the entry did
	// not come from here, and an unrestricted key is not a safe default for a
	// value of unknown origin.
	var raw struct {
		AllowedModels json.RawMessage `json:"allowed_models"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	allowed, err := parseAllowedModels(raw.AllowedModels)
	if err != nil {
		return err
	}
	km.AllowedModels = allowed
	return nil
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

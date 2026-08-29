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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

const redisCacheTTL = 5 * time.Minute

// redisKeyPrefix is versioned so a change to how cached metadata is validated
// cannot be defeated by entries written under the old rules.
//
// v2 made entries cached before the allowlist decode failed closed unreachable:
// they hold "allowed_models":null for keys whose stored value was malformed,
// which decodes into an empty slice and grants every model.
//
// v3 adds key_prefix to the cached shape. A v2 entry lacks it, so it would
// decode with an empty prefix and the audit record would lose the key
// attribution for the length of the TTL.
const redisKeyPrefix = "aegis:key:v3:"

// KeyStore looks up API key metadata by hash.
type KeyStore interface {
	Lookup(ctx context.Context, keyHash string) (*KeyMetadata, error)
}

// CachedKeyStore implements KeyStore with PostgreSQL + Redis cache.
type CachedKeyStore struct {
	db    *pgxpool.Pool
	redis *redis.Client
}

func NewCachedKeyStore(db *pgxpool.Pool, rdb *redis.Client) *CachedKeyStore {
	return &CachedKeyStore{db: db, redis: rdb}
}

func (s *CachedKeyStore) Lookup(ctx context.Context, keyHash string) (*KeyMetadata, error) {
	// Check Redis cache first
	if s.redis != nil {
		cached, err := s.redis.Get(ctx, redisKeyPrefix+keyHash).Bytes()
		if err == nil {
			var meta KeyMetadata
			if err := json.Unmarshal(cached, &meta); err == nil {
				return &meta, nil
			}
		}
	}

	// Query PostgreSQL
	meta, err := s.lookupDB(ctx, keyHash)
	if err != nil {
		return nil, err
	}
	if meta == nil {
		return nil, nil
	}

	// Cache in Redis
	if s.redis != nil {
		data, err := json.Marshal(meta)
		if err == nil {
			s.redis.Set(ctx, redisKeyPrefix+keyHash, data, redisCacheTTL)
		}
	}

	return meta, nil
}

// parseAllowedModels decodes api_keys.allowed_models, failing closed.
//
// An empty result means "every model is permitted", which is the documented
// default for a key that never set an allowlist. That makes a discarded decode
// error dangerous: a JSONB object, a bare string, or an array holding a
// non-string all leave the slice empty, so an unreadable value silently became
// an unrestricted key. It is a fail-open on an access control, and it became
// consequential when the allowlist started gating what a key may USE rather
// than only what it may see.
//
// EVERY anomalous representation is rejected, including an absent value. The
// database's normal no-allowlist value is the default '[]', not NULL, so a nil
// byte slice here means a SQL NULL that an import or a manual UPDATE left
// behind, and reading it as "no restriction" grants every model on a key whose
// restrictions could not be determined.
//
// An earlier version of this function accepted nil as unrestricted, reasoning
// that it was the schema default. It is not: the default is '[]'. That mistake
// also made the two lookup paths disagree, because a nil slice marshals to
// "allowed_models":null in the Redis cache, which the decoder refuses, so the
// same key succeeded on a cache miss and failed on a cache hit.
//
// Migration 015 backfills and constrains the column so the anomaly cannot
// exist. This stays as the guard for a database that has not been migrated.
func parseAllowedModels(raw []byte) ([]string, error) {
	if len(raw) == 0 {
		return nil, errors.New("allowed_models is absent; the no-allowlist value is [], not NULL")
	}

	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, errors.New("allowed_models is not a JSON array")
	}

	var out []string
	if err := json.Unmarshal(trimmed, &out); err != nil {
		return nil, fmt.Errorf("allowed_models is unreadable: %w", err)
	}
	return out, nil
}

func (s *CachedKeyStore) lookupDB(ctx context.Context, keyHash string) (*KeyMetadata, error) {
	var meta KeyMetadata
	var allowedModelsJSON []byte
	var userID *string

	err := s.db.QueryRow(ctx, `
		SELECT id, key_prefix, organization_id, team_id, user_id, name, max_classification,
		       allowed_models, rpm_limit, tpm_limit, daily_spend_limit_cents, expires_at
		FROM api_keys
		WHERE key_hash = $1
		  AND status = 'active'
		  AND expires_at > NOW()
	`, keyHash).Scan(
		&meta.ID,
		&meta.KeyPrefix,
		&meta.OrganizationID,
		&meta.TeamID,
		&userID,
		&meta.Name,
		&meta.MaxClassification,
		&allowedModelsJSON,
		&meta.RPMLimit,
		&meta.TPMLimit,
		&meta.DailySpendLimitCents,
		&meta.ExpiresAt,
	)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return nil, nil
		}
		return nil, fmt.Errorf("query api_keys: %w", err)
	}

	if userID != nil {
		meta.UserID = *userID
	}

	allowed, err := parseAllowedModels(allowedModelsJSON)
	if err != nil {
		return nil, fmt.Errorf("api key %s: %w", meta.ID, err)
	}
	meta.AllowedModels = allowed

	// Update last_used_at asynchronously (fire-and-forget)
	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = s.db.Exec(bgCtx, `UPDATE api_keys SET last_used_at = NOW() WHERE id = $1`, meta.ID)
	}()

	return &meta, nil
}

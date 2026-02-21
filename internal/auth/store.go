package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

const redisCacheTTL = 5 * time.Minute
const redisKeyPrefix = "aegis:key:"

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

func (s *CachedKeyStore) lookupDB(ctx context.Context, keyHash string) (*KeyMetadata, error) {
	var meta KeyMetadata
	var allowedModelsJSON []byte
	var userID *string

	err := s.db.QueryRow(ctx, `
		SELECT id, organization_id, team_id, user_id, name, max_classification,
		       allowed_models, rpm_limit, tpm_limit, daily_spend_limit_cents, expires_at
		FROM api_keys
		WHERE key_hash = $1
		  AND status = 'active'
		  AND expires_at > NOW()
	`, keyHash).Scan(
		&meta.ID,
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

	if len(allowedModelsJSON) > 0 {
		json.Unmarshal(allowedModelsJSON, &meta.AllowedModels)
	}

	// Update last_used_at asynchronously (fire-and-forget)
	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		s.db.Exec(bgCtx, `UPDATE api_keys SET last_used_at = NOW() WHERE id = $1`, meta.ID)
	}()

	return &meta, nil
}

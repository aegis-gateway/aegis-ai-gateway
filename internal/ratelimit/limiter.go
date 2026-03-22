package ratelimit

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// LimitResult is the outcome of a rate limit check.
type LimitResult struct {
	Allowed   bool
	Remaining int64
	ResetAt   time.Time
	RetryAfter time.Duration
}

// Limiter performs sliding-window rate limiting backed by Redis sorted sets.
type Limiter struct {
	rdb            *redis.Client
	circuitBreaker *RedisCircuitBreaker
}

// NewLimiter creates a new rate limiter with circuit breaker protection.
// If rdb is nil, rate limiting is disabled (fail open only when Redis is not configured).
func NewLimiter(rdb *redis.Client) *Limiter {
	var cb *RedisCircuitBreaker
	if rdb != nil {
		// Circuit breaker: 3 failures, 30s timeout before half-open retry
		cb = NewRedisCircuitBreaker(rdb, 3, 30*time.Second)
	}
	return &Limiter{
		rdb:            rdb,
		circuitBreaker: cb,
	}
}

// slidingWindowScript atomically: removes expired entries, adds current, counts.
// KEYS[1] = sorted set key
// ARGV[1] = window start (unix micro)
// ARGV[2] = now (unix micro) — used as both score and member uniqueness
// ARGV[3] = limit
// ARGV[4] = TTL seconds for the key
// Returns: [current_count, 1=allowed/0=denied]
var slidingWindowScript = redis.NewScript(`
local key = KEYS[1]
local window_start = tonumber(ARGV[1])
local now = tonumber(ARGV[2])
local limit = tonumber(ARGV[3])
local ttl = tonumber(ARGV[4])

redis.call('ZREMRANGEBYSCORE', key, '-inf', window_start)
local count = redis.call('ZCARD', key)

if count < limit then
    redis.call('ZADD', key, now, now .. ':' .. math.random(1000000))
    redis.call('EXPIRE', key, ttl)
    return {count + 1, 1}
end

redis.call('EXPIRE', key, ttl)
return {count, 0}
`)

// Check performs a sliding-window rate limit check.
// key: the rate limit bucket identifier
// limit: maximum allowed requests in the window
// window: the sliding window duration
//
// Security: FAILS CLOSED when Redis is unavailable (circuit breaker open).
func (l *Limiter) Check(ctx context.Context, key string, limit int64, window time.Duration) (LimitResult, error) {
	// If Redis is not configured at all, allow (not a security risk, just no rate limiting)
	if l.rdb == nil {
		return LimitResult{Allowed: true, Remaining: limit - 1, ResetAt: time.Now().Add(window)}, nil
	}

	now := time.Now()
	windowStart := now.Add(-window).UnixMicro()
	nowMicro := now.UnixMicro()
	ttlSecs := int64(window.Seconds()) + 1

	redisKey := fmt.Sprintf("aegis:rl:%s", key)

	// Use circuit breaker to wrap Redis call
	var result []int64
	var scriptErr error

	err := l.circuitBreaker.Call(ctx, func() error {
		res, err := slidingWindowScript.Run(ctx, l.rdb, []string{redisKey},
			windowStart, nowMicro, limit, ttlSecs,
		).Int64Slice()
		result = res
		scriptErr = err
		return err
	})

	// If circuit breaker is open, FAIL CLOSED (deny the request)
	if err == ErrCircuitOpen {
		return LimitResult{
			Allowed:    false,
			Remaining:  0,
			ResetAt:    now.Add(window),
			RetryAfter: 30 * time.Second, // Circuit breaker timeout
		}, ErrRedisUnavailable
	}

	// If Redis operation failed, FAIL CLOSED (deny the request)
	if scriptErr != nil {
		return LimitResult{
			Allowed:    false,
			Remaining:  0,
			ResetAt:    now.Add(window),
			RetryAfter: 10 * time.Second,
		}, ErrRedisUnavailable
	}

	count := result[0]
	allowed := result[1] == 1
	remaining := limit - count
	if remaining < 0 {
		remaining = 0
	}

	resetAt := now.Add(window)
	var retryAfter time.Duration
	if !allowed {
		retryAfter = window / 2 // conservative estimate
	}

	return LimitResult{
		Allowed:    allowed,
		Remaining:  remaining,
		ResetAt:    resetAt,
		RetryAfter: retryAfter,
	}, nil
}

// GetCircuitBreakerState returns the current circuit breaker state as a string.
func (l *Limiter) GetCircuitBreakerState() string {
	if l.circuitBreaker == nil {
		return "disabled"
	}
	return l.circuitBreaker.GetState().String()
}

// GetCircuitBreakerStats returns circuit breaker statistics.
func (l *Limiter) GetCircuitBreakerStats() *CircuitBreakerStats {
	if l.circuitBreaker == nil {
		return nil
	}
	stats := l.circuitBreaker.Stats()
	return &stats
}


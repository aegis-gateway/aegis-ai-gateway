package ratelimit

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// BudgetResult is the outcome of a budget check.
type BudgetResult struct {
	Allowed    bool
	SpentCents int64
	LimitCents int64
}

// BudgetTracker tracks daily spend per team via Redis.
type BudgetTracker struct {
	rdb            *redis.Client
	circuitBreaker *RedisCircuitBreaker
}

// NewBudgetTracker creates a budget tracker with circuit breaker protection.
// If rdb is nil, budget tracking is disabled (fail open only when Redis is not configured).
func NewBudgetTracker(rdb *redis.Client) *BudgetTracker {
	var cb *RedisCircuitBreaker
	if rdb != nil {
		// Reuse same circuit breaker parameters as Limiter
		cb = NewRedisCircuitBreaker(rdb, 3, 30*time.Second)
	}
	return &BudgetTracker{
		rdb:            rdb,
		circuitBreaker: cb,
	}
}

func dailyBudgetKey(teamID string) string {
	day := time.Now().UTC().Format("2006-01-02")
	return fmt.Sprintf("aegis:budget:daily:%s:%s", teamID, day)
}

// CheckDailySpend checks if the team is under their daily spend limit.
//
// Security: FAILS CLOSED when Redis is unavailable (circuit breaker open).
func (b *BudgetTracker) CheckDailySpend(ctx context.Context, teamID string, limitCents int64) (BudgetResult, error) {
	// If Redis is not configured at all, allow (not a security risk, just no budget tracking)
	if b.rdb == nil {
		return BudgetResult{Allowed: true, LimitCents: limitCents}, nil
	}

	key := dailyBudgetKey(teamID)
	var spent int64
	var getErr error

	// Use circuit breaker to wrap Redis call
	err := b.circuitBreaker.Call(ctx, func() error {
		result, err := b.rdb.Get(ctx, key).Int64()
		if err != nil && err != redis.Nil {
			return err
		}
		spent = result
		getErr = err
		return nil
	})

	// If circuit breaker is open, FAIL CLOSED (deny the request)
	if err == ErrCircuitOpen {
		return BudgetResult{
			Allowed:    false,
			SpentCents: 0,
			LimitCents: limitCents,
		}, ErrRedisUnavailable
	}

	// If Redis operation failed (non-Nil error), FAIL CLOSED
	if getErr != nil && getErr != redis.Nil {
		return BudgetResult{
			Allowed:    false,
			SpentCents: 0,
			LimitCents: limitCents,
		}, ErrRedisUnavailable
	}

	return BudgetResult{
		Allowed:    spent < limitCents,
		SpentCents: spent,
		LimitCents: limitCents,
	}, nil
}

// RecordSpend adds cost to the team's daily spend counter.
func (b *BudgetTracker) RecordSpend(ctx context.Context, teamID string, costCents int64) error {
	if b.rdb == nil || costCents <= 0 {
		return nil
	}

	key := dailyBudgetKey(teamID)
	pipe := b.rdb.Pipeline()
	pipe.IncrBy(ctx, key, costCents)
	// Expire at end of day UTC + 1 hour buffer
	now := time.Now().UTC()
	endOfDay := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
	ttl := endOfDay.Sub(now) + time.Hour
	pipe.Expire(ctx, key, ttl)
	_, err := pipe.Exec(ctx)
	return err
}

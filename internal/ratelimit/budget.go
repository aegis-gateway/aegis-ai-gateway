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
	rdb *redis.Client
}

// NewBudgetTracker creates a budget tracker. If rdb is nil, all checks pass.
func NewBudgetTracker(rdb *redis.Client) *BudgetTracker {
	return &BudgetTracker{rdb: rdb}
}

func dailyBudgetKey(teamID string) string {
	day := time.Now().UTC().Format("2006-01-02")
	return fmt.Sprintf("aegis:budget:daily:%s:%s", teamID, day)
}

// CheckDailySpend checks if the team is under their daily spend limit.
func (b *BudgetTracker) CheckDailySpend(ctx context.Context, teamID string, limitCents int64) (BudgetResult, error) {
	if b.rdb == nil {
		return BudgetResult{Allowed: true, LimitCents: limitCents}, nil
	}

	key := dailyBudgetKey(teamID)
	spent, err := b.rdb.Get(ctx, key).Int64()
	if err != nil && err != redis.Nil {
		// Fail open on Redis errors
		return BudgetResult{Allowed: true, LimitCents: limitCents}, nil
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

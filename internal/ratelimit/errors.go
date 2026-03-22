package ratelimit

import "errors"

// ErrCircuitOpen is returned when the Redis circuit breaker is open.
var ErrCircuitOpen = errors.New("redis circuit breaker is open")

// ErrRedisUnavailable is returned when Redis is unavailable and fail-closed is enforced.
var ErrRedisUnavailable = errors.New("redis unavailable")

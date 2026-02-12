package redis

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/redis/go-redis/v9"
)

//go:embed chatpulse.lua
var chatpulseLibrary string

// NewClient creates a Redis client and loads the chatpulse Lua function library.
// The client is wrapped with a circuit breaker hook for graceful degradation
// when Redis becomes unavailable.
func NewClient(ctx context.Context, redisURL string) (*redis.Client, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse redis URL: %w", err)
	}

	rdb := redis.NewClient(opts)

	// Add circuit breaker hook FIRST (innermost protection layer)
	rdb.AddHook(NewCircuitBreakerHook())

	// Add metrics hook to collect operation metrics (wraps circuit breaker)
	rdb.AddHook(&MetricsHook{})

	if err := rdb.FunctionLoadReplace(ctx, chatpulseLibrary).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("load chatpulse library: %w", err)
	}

	return rdb, nil
}

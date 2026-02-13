package redis

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"

	"github.com/redis/go-redis/v9"
)

//go:embed chatpulse.lua
var chatpulseLibrary string

//go:embed vote_rate_limit.lua
var voteRateLimitLibrary string

// LibraryVersion is the current version of the chatpulse Lua library.
// Must match LIBRARY_VERSION in chatpulse.lua.
// Increment on breaking changes to function signatures or behavior.
const LibraryVersion = "3"

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

	// Check existing library version before loading
	versionKey := "chatpulse:function:version"
	existingVersion, err := rdb.Get(ctx, versionKey).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		_ = rdb.Close()
		return nil, fmt.Errorf("failed to check function version: %w", err)
	}

	// Warn on version mismatch (another instance may have different version)
	if existingVersion != "" && existingVersion != LibraryVersion {
		slog.Warn("Lua function version mismatch detected",
			"current_version", existingVersion,
			"new_version", LibraryVersion,
			"action", "replacing")
	}

	// Load chatpulse library (sentiment functions)
	if err := rdb.FunctionLoadReplace(ctx, chatpulseLibrary).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("load chatpulse library: %w", err)
	}

	// Load vote_rate_limit library (rate limiting functions)
	if err := rdb.FunctionLoadReplace(ctx, voteRateLimitLibrary).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("load vote_rate_limit library: %w", err)
	}

	// Store version in Redis for version coordination across instances
	if err := rdb.Set(ctx, versionKey, LibraryVersion, 0).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("failed to set function version: %w", err)
	}

	slog.Info("Lua functions loaded",
		"version", LibraryVersion,
		"library", "chatpulse")

	return rdb, nil
}

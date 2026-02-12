package redis

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jonboulle/clockwork"
	goredis "github.com/redis/go-redis/v9"
)

// VoteRateLimiter implements token bucket rate limiting for votes.
type VoteRateLimiter struct {
	rdb      *goredis.Client
	clock    clockwork.Clock
	capacity int
	rate     int // tokens per minute
}

// NewVoteRateLimiter creates a new vote rate limiter.
// capacity: maximum burst size (tokens)
// rate: sustained rate (tokens per minute)
func NewVoteRateLimiter(rdb *goredis.Client, clock clockwork.Clock, capacity, rate int) *VoteRateLimiter {
	return &VoteRateLimiter{
		rdb:      rdb,
		clock:    clock,
		capacity: capacity,
		rate:     rate,
	}
}

// CheckVoteRateLimit checks if a vote is allowed for the session.
// Returns true if allowed (token consumed), false if rate limited.
func (v *VoteRateLimiter) CheckVoteRateLimit(ctx context.Context, sessionUUID uuid.UUID) (bool, error) {
	key := fmt.Sprintf("rate_limit:votes:%s", sessionUUID)

	result := v.rdb.FCall(ctx, "check_vote_rate_limit",
		[]string{key},
		v.clock.Now().UnixMilli(),
		v.capacity,
		v.rate,
	)

	if err := result.Err(); err != nil {
		return false, fmt.Errorf("rate limit check failed: %w", err)
	}

	allowed, err := result.Int()
	if err != nil {
		return false, fmt.Errorf("failed to parse rate limit result: %w", err)
	}

	return allowed == 1, nil
}

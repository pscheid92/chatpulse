package domain

import (
	"context"
)

// VoteRateLimiter enforces per-broadcaster vote rate limits using a token bucket algorithm.
// Allows burst traffic (up to capacity) while limiting sustained rate.
type VoteRateLimiter interface {
	// CheckVoteRateLimit checks if a vote is allowed for the broadcaster.
	// Returns true if allowed (token consumed), false if rate limited.
	CheckVoteRateLimit(ctx context.Context, broadcasterID string) (bool, error)
}

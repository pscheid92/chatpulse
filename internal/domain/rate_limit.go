package domain

import (
	"context"

	"github.com/google/uuid"
)

// VoteRateLimiter enforces per-session vote rate limits using a token bucket algorithm.
// Allows burst traffic (up to capacity) while limiting sustained rate.
type VoteRateLimiter interface {
	// CheckVoteRateLimit checks if a vote is allowed for the session.
	// Returns true if allowed (token consumed), false if rate limited.
	CheckVoteRateLimit(ctx context.Context, sessionUUID uuid.UUID) (bool, error)
}

package domain

import (
	"context"

	"github.com/google/uuid"
)

type SentimentStore interface {
	ApplyVote(ctx context.Context, sessionUUID uuid.UUID, delta, decayRate float64, nowMs int64) (float64, error)
	GetSentiment(ctx context.Context, sessionUUID uuid.UUID, decayRate float64, nowMs int64) (float64, error)
	ResetSentiment(ctx context.Context, sessionUUID uuid.UUID) error
}

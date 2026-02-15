package domain

import (
	"context"
)

type SentimentStore interface {
	ApplyVote(ctx context.Context, broadcasterID string, delta, decayRate float64, nowMs int64) (float64, error)
	GetRawSentiment(ctx context.Context, broadcasterID string) (value float64, lastUpdateMs int64, err error)
	ResetSentiment(ctx context.Context, broadcasterID string) error
}

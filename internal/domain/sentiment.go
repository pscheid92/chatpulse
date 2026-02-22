package domain

import (
	"context"
)

// WindowSnapshot holds the computed ratios from the sliding-window vote store.
type WindowSnapshot struct {
	ForRatio     float64 // 0.0–1.0
	AgainstRatio float64 // 0.0–1.0
	TotalVotes   int
}

type SentimentStore interface {
	RecordVote(ctx context.Context, broadcasterID string, target VoteTarget, windowSeconds int) (*WindowSnapshot, error)
	GetSnapshot(ctx context.Context, broadcasterID string, windowSeconds int) (*WindowSnapshot, error)
	ResetSentiment(ctx context.Context, broadcasterID string) error
}

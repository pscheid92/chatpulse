package domain

import (
	"context"

	"github.com/google/uuid"
)

type Engine interface {
	GetCurrentValue(ctx context.Context, sessionUUID uuid.UUID) (float64, error)
	ProcessVote(ctx context.Context, broadcasterUserID, chatterUserID, messageText string) (float64, bool)
	ResetSentiment(ctx context.Context, sessionUUID uuid.UUID) error
	InvalidateConfigCache(sessionUUID uuid.UUID)
}

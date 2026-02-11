package domain

import (
	"context"

	"github.com/google/uuid"
)

type Debouncer interface {
	CheckDebounce(ctx context.Context, sessionUUID uuid.UUID, twitchUserID string) (bool, error)
}

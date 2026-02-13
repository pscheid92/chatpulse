package domain

import (
	"context"
)

type Debouncer interface {
	CheckDebounce(ctx context.Context, broadcasterID string, twitchUserID string) (bool, error)
}

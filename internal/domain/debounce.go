package domain

import (
	"context"
)

// ParticipantDebouncer prevents the same user from voting too frequently.
// IsDebounced returns true if the user is currently debounced (cannot vote).
type ParticipantDebouncer interface {
	IsDebounced(ctx context.Context, broadcasterID string, twitchUserID string) (bool, error)
}

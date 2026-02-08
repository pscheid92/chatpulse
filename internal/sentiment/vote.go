package sentiment

import (
	"context"
	"log"

	"github.com/google/uuid"
	"github.com/pscheid92/chatpulse/internal/models"
)

// VoteStore is the subset of SessionStateStore needed for vote processing.
type VoteStore interface {
	GetSessionByBroadcaster(ctx context.Context, broadcasterUserID string) (uuid.UUID, bool, error)
	GetSessionConfig(ctx context.Context, sessionUUID uuid.UUID) (*models.ConfigSnapshot, error)
	CheckDebounce(ctx context.Context, sessionUUID uuid.UUID, twitchUserID string) (bool, error)
	ApplyVote(ctx context.Context, sessionUUID uuid.UUID, delta float64) (float64, error)
}

// ProcessVote runs the complete vote pipeline: broadcaster lookup → trigger match →
// debounce check → atomic vote application. Returns the new value and whether a
// vote was actually applied.
func ProcessVote(ctx context.Context, store VoteStore, broadcasterUserID, chatterUserID, messageText string) (float64, bool) {
	sessionUUID, found, err := store.GetSessionByBroadcaster(ctx, broadcasterUserID)
	if err != nil || !found {
		return 0, false
	}

	config, err := store.GetSessionConfig(ctx, sessionUUID)
	if err != nil || config == nil {
		return 0, false
	}

	delta := MatchTrigger(messageText, config)
	if delta == 0 {
		return 0, false
	}

	allowed, err := store.CheckDebounce(ctx, sessionUUID, chatterUserID)
	if err != nil || !allowed {
		return 0, false
	}

	newValue, err := store.ApplyVote(ctx, sessionUUID, delta)
	if err != nil {
		log.Printf("ApplyVote error: %v", err)
		return 0, false
	}

	return newValue, true
}

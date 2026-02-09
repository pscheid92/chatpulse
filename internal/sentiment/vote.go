package sentiment

import (
	"context"
	"log"
	"time"

	"github.com/pscheid92/chatpulse/internal/domain"
)

// ProcessVote runs the complete vote pipeline: broadcaster lookup → trigger match →
// debounce check → atomic vote application. Returns the new value and whether a
// vote was actually applied.
func ProcessVote(ctx context.Context, store domain.VoteStore, broadcasterUserID, chatterUserID, messageText string) (float64, bool) {
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

	nowMs := time.Now().UnixMilli()
	newValue, err := store.ApplyVote(ctx, sessionUUID, delta, config.DecaySpeed, nowMs)
	if err != nil {
		log.Printf("ApplyVote error: %v", err)
		return 0, false
	}

	return newValue, true
}

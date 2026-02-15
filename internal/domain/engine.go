package domain

import "context"

// BroadcastData contains everything needed for a WebSocket broadcast message.
// Value is the raw (undecayed) sentiment value as stored in Redis.
// DecaySpeed and UnixTimestamp allow the client to compute decay locally.
type BroadcastData struct {
	Value         float64
	DecaySpeed    float64
	UnixTimestamp int64
}

// VoteResult describes why a vote was or wasn't applied.
type VoteResult int

const (
	VoteApplied   VoteResult = iota // Vote was successfully applied
	VoteNoSession                   // No active session for broadcaster
	VoteNoMatch                     // Message didn't match any trigger
	VoteDebounced                   // Per-user debounce rejected the vote
	VoteError                       // Infrastructure error (Redis, etc.)
)

func (r VoteResult) String() string {
	switch r {
	case VoteApplied:
		return "applied"
	case VoteNoSession:
		return "no_session"
	case VoteNoMatch:
		return "no_match"
	case VoteDebounced:
		return "debounced"
	case VoteError:
		return "error"
	default:
		return "unknown"
	}
}

type Engine interface {
	GetBroadcastData(ctx context.Context, broadcasterID string) (*BroadcastData, error)
	ProcessVote(ctx context.Context, broadcasterUserID, chatterUserID, messageText string) (float64, VoteResult, error)
	ResetSentiment(ctx context.Context, broadcasterID string) error
}

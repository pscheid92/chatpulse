package domain

import "context"

// DisplayMode controls how the overlay renders sentiment.
type DisplayMode string

const (
	DisplayModeCombined DisplayMode = "combined"
	DisplayModeSplit    DisplayMode = "split"
)

// ParseDisplayMode converts a string to a DisplayMode, defaulting to combined.
func ParseDisplayMode(s string) DisplayMode {
	switch s {
	case "split":
		return DisplayModeSplit
	default:
		return DisplayModeCombined
	}
}

// VoteTarget indicates which counter a vote applies to.
type VoteTarget int

const (
	VoteTargetNone     VoteTarget = iota
	VoteTargetPositive            // "for" trigger matched
	VoteTargetNegative            // "against" trigger matched
)

// VoteResult describes why a vote was or wasn't applied.
type VoteResult int

const (
	VoteApplied   VoteResult = iota // Vote was successfully applied
	VoteNoMatch                     // Message didn't match any trigger
	VoteDebounced                   // Per-user debounce rejected the vote
)

func (r VoteResult) String() string {
	switch r {
	case VoteApplied:
		return "applied"
	case VoteNoMatch:
		return "no_match"
	case VoteDebounced:
		return "debounced"
	default:
		return "unknown"
	}
}

type Overlay interface {
	ProcessMessage(ctx context.Context, broadcasterUserID, chatterUserID, messageText string) (*WindowSnapshot, VoteResult, VoteTarget, error)
	Reset(ctx context.Context, broadcasterID string) error
}

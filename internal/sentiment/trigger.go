package sentiment

import (
	"strings"

	"github.com/pscheid92/chatpulse/internal/domain"
)

const voteDelta = 10.0

// MatchTrigger checks if a message matches any configured trigger.
// Returns +voteDelta for the "for" trigger, -voteDelta for the "against" trigger, or 0 for no match.
// Matching is case-insensitive substring matching. The "for" trigger takes priority.
func MatchTrigger(messageText string, config *domain.ConfigSnapshot) float64 {
	if config == nil {
		return 0
	}

	lowerText := strings.ToLower(messageText)

	if config.ForTrigger != "" && strings.Contains(lowerText, strings.ToLower(config.ForTrigger)) {
		return voteDelta
	}
	if config.AgainstTrigger != "" && strings.Contains(lowerText, strings.ToLower(config.AgainstTrigger)) {
		return -voteDelta
	}
	return 0
}

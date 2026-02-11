package sentiment

import (
	"context"
	"log"
	"strings"

	"github.com/google/uuid"
	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/domain"
)

const voteDelta = 10.0

type Engine struct {
	sessions  domain.SessionRepository
	sentiment domain.SentimentStore
	debounce  domain.Debouncer
	clock     clockwork.Clock
}

func NewEngine(sessions domain.SessionRepository, sentiment domain.SentimentStore, debounce domain.Debouncer, clock clockwork.Clock) *Engine {
	return &Engine{sessions: sessions, sentiment: sentiment, debounce: debounce, clock: clock}
}

func (e *Engine) GetCurrentValue(ctx context.Context, sessionUUID uuid.UUID) (float64, error) {
	config, err := e.sessions.GetSessionConfig(ctx, sessionUUID)
	if err != nil {
		return 0, err
	}
	if config == nil {
		return 0, nil
	}

	nowMs := e.clock.Now().UnixMilli()
	return e.sentiment.GetSentiment(ctx, sessionUUID, config.DecaySpeed, nowMs)
}

func (e *Engine) ProcessVote(ctx context.Context, broadcasterUserID, chatterUserID, messageText string) (float64, bool) {
	sessionUUID, found, err := e.sessions.GetSessionByBroadcaster(ctx, broadcasterUserID)
	if err != nil || !found {
		return 0, false
	}

	config, err := e.sessions.GetSessionConfig(ctx, sessionUUID)
	if err != nil || config == nil {
		return 0, false
	}

	delta := matchTrigger(messageText, config)
	if delta == 0 {
		return 0, false
	}

	allowed, err := e.debounce.CheckDebounce(ctx, sessionUUID, chatterUserID)
	if err != nil || !allowed {
		return 0, false
	}

	nowMs := e.clock.Now().UnixMilli()
	newValue, err := e.sentiment.ApplyVote(ctx, sessionUUID, delta, config.DecaySpeed, nowMs)
	if err != nil {
		log.Printf("ApplyVote error: %v", err)
		return 0, false
	}

	return newValue, true
}

func (e *Engine) ResetSentiment(ctx context.Context, sessionUUID uuid.UUID) error {
	return e.sentiment.ResetSentiment(ctx, sessionUUID)
}

func matchTrigger(messageText string, config *domain.ConfigSnapshot) float64 {
	if config == nil {
		return 0
	}

	trimmed := strings.TrimSpace(messageText)

	if config.ForTrigger != "" && strings.EqualFold(trimmed, config.ForTrigger) {
		return voteDelta
	}
	if config.AgainstTrigger != "" && strings.EqualFold(trimmed, config.AgainstTrigger) {
		return -voteDelta
	}
	return 0
}

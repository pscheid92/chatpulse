package sentiment

import (
	"context"

	"github.com/google/uuid"
	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/domain"
)

// Engine provides sentiment value reads with time-decay applied.
// No actor, no goroutines — all state lives in Redis.
type Engine struct {
	store domain.SessionStateStore
	clock clockwork.Clock
}

func NewEngine(store domain.SessionStateStore, clock clockwork.Clock) *Engine {
	return &Engine{store: store, clock: clock}
}

// GetCurrentValue reads the current sentiment value with time-decay applied.
// Pure read — no writes to Redis.
func (e *Engine) GetCurrentValue(ctx context.Context, sessionUUID uuid.UUID) (float64, error) {
	config, err := e.store.GetSessionConfig(ctx, sessionUUID)
	if err != nil {
		return 0, err
	}
	if config == nil {
		return 0, nil
	}

	nowMs := e.clock.Now().UnixMilli()
	return e.store.GetDecayedValue(ctx, sessionUUID, config.DecaySpeed, nowMs)
}

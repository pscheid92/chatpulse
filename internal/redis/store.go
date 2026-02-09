package redis

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/domain"
)

// SentimentStore provides Redis-backed session state.
// Vote operations use Lua scripts for atomicity with time-decayed accumulator math.
type SentimentStore struct {
	sessions *SessionStore
	scripts  *ScriptRunner
	clock    clockwork.Clock
}

func NewSentimentStore(sessions *SessionStore, scripts *ScriptRunner, clock clockwork.Clock) *SentimentStore {
	return &SentimentStore{sessions: sessions, scripts: scripts, clock: clock}
}

func (s *SentimentStore) ActivateSession(ctx context.Context, sessionUUID uuid.UUID, broadcasterUserID string, config domain.ConfigSnapshot) error {
	return s.sessions.ActivateSession(ctx, sessionUUID, broadcasterUserID, config)
}

func (s *SentimentStore) ResumeSession(ctx context.Context, sessionUUID uuid.UUID) error {
	return s.sessions.ClearDisconnected(ctx, sessionUUID)
}

func (s *SentimentStore) SessionExists(ctx context.Context, sessionUUID uuid.UUID) (bool, error) {
	_, cfg, err := s.sessions.GetSession(ctx, sessionUUID)
	if err != nil {
		return false, err
	}
	return cfg != nil, nil
}

func (s *SentimentStore) DeleteSession(ctx context.Context, sessionUUID uuid.UUID) error {
	return s.sessions.DeleteSession(ctx, sessionUUID)
}

func (s *SentimentStore) GetSessionByBroadcaster(ctx context.Context, broadcasterUserID string) (uuid.UUID, bool, error) {
	id, err := s.sessions.GetSessionByBroadcaster(ctx, broadcasterUserID)
	if err != nil {
		return uuid.Nil, false, err
	}
	if id == uuid.Nil {
		return uuid.Nil, false, nil
	}
	return id, true, nil
}

func (s *SentimentStore) GetSessionConfig(ctx context.Context, sessionUUID uuid.UUID) (*domain.ConfigSnapshot, error) {
	_, cfg, err := s.sessions.GetSession(ctx, sessionUUID)
	return cfg, err
}

func (s *SentimentStore) GetSessionValue(ctx context.Context, sessionUUID uuid.UUID) (float64, bool, error) {
	return s.sessions.GetValue(ctx, sessionUUID)
}

func (s *SentimentStore) CheckDebounce(ctx context.Context, sessionUUID uuid.UUID, twitchUserID string) (bool, error) {
	return s.sessions.CheckAndSetDebounce(ctx, sessionUUID, twitchUserID)
}

func (s *SentimentStore) ApplyVote(ctx context.Context, sessionUUID uuid.UUID, delta, decayRate float64, nowMs int64) (float64, error) {
	return s.scripts.ApplyVote(ctx, sessionUUID, delta, decayRate, nowMs)
}

func (s *SentimentStore) GetDecayedValue(ctx context.Context, sessionUUID uuid.UUID, decayRate float64, nowMs int64) (float64, error) {
	return s.scripts.GetDecayedValue(ctx, sessionUUID, decayRate, nowMs)
}

func (s *SentimentStore) ResetValue(ctx context.Context, sessionUUID uuid.UUID) error {
	return s.sessions.UpdateValue(ctx, sessionUUID, 0)
}

func (s *SentimentStore) MarkDisconnected(ctx context.Context, sessionUUID uuid.UUID) error {
	return s.sessions.MarkDisconnected(ctx, sessionUUID, s.clock.Now())
}

func (s *SentimentStore) UpdateConfig(ctx context.Context, sessionUUID uuid.UUID, config domain.ConfigSnapshot) error {
	return s.sessions.UpdateConfig(ctx, sessionUUID, config)
}

func (s *SentimentStore) ListOrphans(ctx context.Context, maxAge time.Duration) ([]uuid.UUID, error) {
	return s.sessions.ListOrphanSessions(ctx, maxAge)
}

func (s *SentimentStore) IncrRefCount(ctx context.Context, sessionUUID uuid.UUID) (int64, error) {
	return s.sessions.IncrRefCount(ctx, sessionUUID)
}

func (s *SentimentStore) DecrRefCount(ctx context.Context, sessionUUID uuid.UUID) (int64, error) {
	return s.sessions.DecrRefCount(ctx, sessionUUID)
}

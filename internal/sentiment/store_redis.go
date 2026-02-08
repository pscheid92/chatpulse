package sentiment

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/models"
	iredis "github.com/pscheid92/chatpulse/internal/redis"
)

// RedisStore provides Redis-backed session state for multi-instance mode.
// Vote and decay operations use Lua scripts for atomicity and include PUBLISH
// so the Hub's Pub/Sub listener handles broadcasting.
type RedisStore struct {
	sessions *iredis.SessionStore
	scripts  *iredis.ScriptRunner
	clock    clockwork.Clock
}

func NewRedisStore(sessions *iredis.SessionStore, scripts *iredis.ScriptRunner, clock clockwork.Clock) *RedisStore {
	return &RedisStore{sessions: sessions, scripts: scripts, clock: clock}
}

func (s *RedisStore) ActivateSession(ctx context.Context, sessionUUID uuid.UUID, broadcasterUserID string, config models.ConfigSnapshot) error {
	return s.sessions.ActivateSession(ctx, sessionUUID, broadcasterUserID, config)
}

func (s *RedisStore) ResumeSession(ctx context.Context, sessionUUID uuid.UUID) error {
	return s.sessions.ClearDisconnected(ctx, sessionUUID)
}

func (s *RedisStore) SessionExists(ctx context.Context, sessionUUID uuid.UUID) (bool, error) {
	_, cfg, err := s.sessions.GetSession(ctx, sessionUUID)
	if err != nil {
		return false, err
	}
	return cfg != nil, nil
}

func (s *RedisStore) DeleteSession(ctx context.Context, sessionUUID uuid.UUID) error {
	return s.sessions.DeleteSession(ctx, sessionUUID)
}

func (s *RedisStore) GetSessionByBroadcaster(ctx context.Context, broadcasterUserID string) (uuid.UUID, bool, error) {
	id, err := s.sessions.GetSessionByBroadcaster(ctx, broadcasterUserID)
	if err != nil {
		return uuid.Nil, false, err
	}
	if id == uuid.Nil {
		return uuid.Nil, false, nil
	}
	return id, true, nil
}

func (s *RedisStore) GetSessionConfig(ctx context.Context, sessionUUID uuid.UUID) (*models.ConfigSnapshot, error) {
	_, cfg, err := s.sessions.GetSession(ctx, sessionUUID)
	return cfg, err
}

func (s *RedisStore) GetSessionValue(ctx context.Context, sessionUUID uuid.UUID) (float64, bool, error) {
	return s.sessions.GetValue(ctx, sessionUUID)
}

func (s *RedisStore) CheckDebounce(ctx context.Context, sessionUUID uuid.UUID, twitchUserID string) (bool, error) {
	return s.sessions.CheckAndSetDebounce(ctx, sessionUUID, twitchUserID)
}

func (s *RedisStore) ApplyVote(ctx context.Context, sessionUUID uuid.UUID, delta float64) (float64, error) {
	return s.scripts.ApplyVote(ctx, sessionUUID, delta)
}

func (s *RedisStore) ApplyDecay(ctx context.Context, sessionUUID uuid.UUID, decayFactor float64, nowMs int64, minIntervalMs int64) (float64, error) {
	return s.scripts.ApplyDecay(ctx, sessionUUID, decayFactor, nowMs, minIntervalMs)
}

func (s *RedisStore) ResetValue(ctx context.Context, sessionUUID uuid.UUID) error {
	return s.sessions.UpdateValue(ctx, sessionUUID, 0)
}

func (s *RedisStore) MarkDisconnected(ctx context.Context, sessionUUID uuid.UUID) error {
	return s.sessions.MarkDisconnected(ctx, sessionUUID, s.clock.Now())
}

func (s *RedisStore) UpdateConfig(ctx context.Context, sessionUUID uuid.UUID, config models.ConfigSnapshot) error {
	return s.sessions.UpdateConfig(ctx, sessionUUID, config)
}

func (s *RedisStore) ListOrphans(ctx context.Context, maxAge time.Duration) ([]uuid.UUID, error) {
	return s.sessions.ListOrphanSessions(ctx, maxAge)
}

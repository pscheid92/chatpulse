package sentiment

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/models"
)

const (
	debounceInterval    = 1 * time.Second
	debouncePruneExpiry = 5 * time.Second
)

type memorySession struct {
	Value                float64
	LastDecayMs          int64
	LastClientDisconnect time.Time
	Config               models.ConfigSnapshot
}

type debounceKey struct {
	SessionUUID  uuid.UUID
	TwitchUserID string
}

// InMemoryStore provides in-memory session state for single-instance mode.
// All methods are called from the Engine actor goroutine (no concurrent access).
type InMemoryStore struct {
	clock                clockwork.Clock
	sessions             map[uuid.UUID]*memorySession
	broadcasterToSession map[string]uuid.UUID
	sessionToBroadcaster map[uuid.UUID]string
	debounceMap          map[debounceKey]time.Time
}

func NewInMemoryStore(clock clockwork.Clock) *InMemoryStore {
	return &InMemoryStore{
		clock:                clock,
		sessions:             make(map[uuid.UUID]*memorySession),
		broadcasterToSession: make(map[string]uuid.UUID),
		sessionToBroadcaster: make(map[uuid.UUID]string),
		debounceMap:          make(map[debounceKey]time.Time),
	}
}

func (s *InMemoryStore) ActivateSession(_ context.Context, sessionUUID uuid.UUID, broadcasterUserID string, config models.ConfigSnapshot) error {
	if session, exists := s.sessions[sessionUUID]; exists {
		session.LastClientDisconnect = time.Time{}
		return nil
	}
	s.sessions[sessionUUID] = &memorySession{
		Value:  0,
		Config: config,
	}
	s.broadcasterToSession[broadcasterUserID] = sessionUUID
	s.sessionToBroadcaster[sessionUUID] = broadcasterUserID
	return nil
}

func (s *InMemoryStore) ResumeSession(_ context.Context, sessionUUID uuid.UUID) error {
	if session, exists := s.sessions[sessionUUID]; exists {
		session.LastClientDisconnect = time.Time{}
	}
	return nil
}

func (s *InMemoryStore) SessionExists(_ context.Context, sessionUUID uuid.UUID) (bool, error) {
	_, exists := s.sessions[sessionUUID]
	return exists, nil
}

func (s *InMemoryStore) DeleteSession(_ context.Context, sessionUUID uuid.UUID) error {
	if broadcasterID, ok := s.sessionToBroadcaster[sessionUUID]; ok {
		delete(s.broadcasterToSession, broadcasterID)
		delete(s.sessionToBroadcaster, sessionUUID)
	}
	delete(s.sessions, sessionUUID)
	for key := range s.debounceMap {
		if key.SessionUUID == sessionUUID {
			delete(s.debounceMap, key)
		}
	}
	return nil
}

func (s *InMemoryStore) GetSessionByBroadcaster(_ context.Context, broadcasterUserID string) (uuid.UUID, bool, error) {
	id, ok := s.broadcasterToSession[broadcasterUserID]
	return id, ok, nil
}

func (s *InMemoryStore) GetSessionConfig(_ context.Context, sessionUUID uuid.UUID) (*models.ConfigSnapshot, error) {
	session, exists := s.sessions[sessionUUID]
	if !exists {
		return nil, nil
	}
	config := session.Config
	return &config, nil
}

func (s *InMemoryStore) GetSessionValue(_ context.Context, sessionUUID uuid.UUID) (float64, bool, error) {
	session, exists := s.sessions[sessionUUID]
	if !exists {
		return 0, false, nil
	}
	return session.Value, true, nil
}

func (s *InMemoryStore) CheckDebounce(_ context.Context, sessionUUID uuid.UUID, twitchUserID string) (bool, error) {
	key := debounceKey{SessionUUID: sessionUUID, TwitchUserID: twitchUserID}
	lastVote, exists := s.debounceMap[key]
	if exists && s.clock.Since(lastVote) < debounceInterval {
		return false, nil
	}
	s.debounceMap[key] = s.clock.Now()
	return true, nil
}

func (s *InMemoryStore) ApplyVote(_ context.Context, sessionUUID uuid.UUID, delta float64) (float64, error) {
	session, exists := s.sessions[sessionUUID]
	if !exists {
		return 0, nil
	}
	session.Value = clamp(session.Value+delta, -100, 100)
	return session.Value, nil
}

func (s *InMemoryStore) ApplyDecay(_ context.Context, sessionUUID uuid.UUID, decayFactor float64, nowMs int64, minIntervalMs int64) (float64, error) {
	session, exists := s.sessions[sessionUUID]
	if !exists {
		return 0, nil
	}
	if session.LastDecayMs > 0 && nowMs-session.LastDecayMs < minIntervalMs {
		return session.Value, nil
	}
	session.Value *= decayFactor
	session.LastDecayMs = nowMs
	return session.Value, nil
}

func (s *InMemoryStore) ResetValue(_ context.Context, sessionUUID uuid.UUID) error {
	if session, exists := s.sessions[sessionUUID]; exists {
		session.Value = 0
	}
	return nil
}

func (s *InMemoryStore) MarkDisconnected(_ context.Context, sessionUUID uuid.UUID) error {
	if session, exists := s.sessions[sessionUUID]; exists {
		session.LastClientDisconnect = s.clock.Now()
	}
	return nil
}

func (s *InMemoryStore) UpdateConfig(_ context.Context, sessionUUID uuid.UUID, config models.ConfigSnapshot) error {
	if session, exists := s.sessions[sessionUUID]; exists {
		session.Config = config
	}
	return nil
}

func (s *InMemoryStore) ListOrphans(_ context.Context, maxAge time.Duration) ([]uuid.UUID, error) {
	var orphans []uuid.UUID
	now := s.clock.Now()
	for id, session := range s.sessions {
		if !session.LastClientDisconnect.IsZero() && now.Sub(session.LastClientDisconnect) > maxAge {
			orphans = append(orphans, id)
		}
	}
	return orphans, nil
}

func (s *InMemoryStore) PruneDebounce(_ context.Context) error {
	now := s.clock.Now()
	for key, lastVote := range s.debounceMap {
		if now.Sub(lastVote) > debouncePruneExpiry {
			delete(s.debounceMap, key)
		}
	}
	return nil
}

func (s *InMemoryStore) NeedsBroadcast() bool {
	return true
}

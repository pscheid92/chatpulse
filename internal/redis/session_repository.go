package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/domain"
	goredis "github.com/redis/go-redis/v9"
)

const (
	// Redis hash field names for session keys.
	fieldValue         = "value"
	fieldBroadcasterID = "broadcaster_user_id"
	fieldConfigJSON    = "config_json"
	fieldConfigVersion = "config_version"
	fieldLastUpdate    = "last_update"
)

type SessionRepo struct {
	rdb   *goredis.Client
	clock clockwork.Clock
}

func NewSessionRepo(rdb *goredis.Client, clock clockwork.Clock) *SessionRepo {
	return &SessionRepo{rdb: rdb, clock: clock}
}

// --- Session queries ---

// GetBroadcasterID returns the broadcaster user ID for a session.
func (s *SessionRepo) GetBroadcasterID(ctx context.Context, sid uuid.UUID) (string, error) {
	sk := sessionKey(sid)
	broadcasterID, err := s.rdb.HGet(ctx, sk, fieldBroadcasterID).Result()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return "", fmt.Errorf("session not found: %s", sid)
		}
		return "", fmt.Errorf("failed to get broadcaster ID: %w", err)
	}
	return broadcasterID, nil
}

// GetSessionByBroadcaster returns the overlay UUID for a broadcaster.
func (s *SessionRepo) GetSessionByBroadcaster(ctx context.Context, broadcasterUserID string) (uuid.UUID, bool, error) {
	bk := broadcasterKey(broadcasterUserID)

	result, err := s.rdb.Get(ctx, bk).Result()
	if errors.Is(err, goredis.Nil) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("failed to get session by broadcaster: %w", err)
	}

	id, err := uuid.Parse(result)
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("failed to parse session UUID: %w", err)
	}

	return id, true, nil
}

func (s *SessionRepo) GetSessionConfig(ctx context.Context, sid uuid.UUID) (*domain.ConfigSnapshot, error) {
	sk := sessionKey(sid)

	configJSON, err := s.rdb.HGet(ctx, sk, fieldConfigJSON).Result()
	if errors.Is(err, goredis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to fetch session config: %w", err)
	}

	var config domain.ConfigSnapshot
	if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	return &config, nil
}

// --- Key helpers ---

func sessionKey(sid uuid.UUID) string {
	return "session:" + sid.String()
}

func broadcasterKey(twitchUserID string) string {
	return "broadcaster:" + twitchUserID
}

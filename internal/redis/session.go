package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pscheid92/chatpulse/internal/models"
	"github.com/redis/go-redis/v9"
)

const debounceInterval = 1 * time.Second

// Key schema:
//   session:{overlayUUID}          — hash: value, broadcaster_user_id, config_json, last_disconnect, last_decay
//   broadcaster:{twitchUserID}     — string → overlayUUID
//   debounce:{overlayUUID}:{uid}   — key with 1s TTL (auto-expires)

func sessionKey(overlayUUID uuid.UUID) string {
	return "session:" + overlayUUID.String()
}

func broadcasterKey(twitchUserID string) string {
	return "broadcaster:" + twitchUserID
}

func debounceKey(overlayUUID uuid.UUID, twitchUserID string) string {
	return "debounce:" + overlayUUID.String() + ":" + twitchUserID
}

// SessionStore provides Redis-backed session state for the sentiment engine.
type SessionStore struct {
	rdb *redis.Client
}

// NewSessionStore creates a new SessionStore.
func NewSessionStore(client *Client) *SessionStore {
	return &SessionStore{rdb: client.rdb}
}

// ActivateSession creates or resumes a session in Redis.
func (s *SessionStore) ActivateSession(ctx context.Context, overlayUUID uuid.UUID, broadcasterUserID string, config models.ConfigSnapshot) error {
	configJSON, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	key := sessionKey(overlayUUID)

	// Check if session exists
	exists, err := s.rdb.Exists(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("failed to check session existence: %w", err)
	}

	if exists > 0 {
		// Resume: clear last_disconnect
		return s.rdb.HSet(ctx, key, "last_disconnect", "0").Err()
	}

	// Create new session
	pipe := s.rdb.Pipeline()
	pipe.HSet(ctx, key, map[string]interface{}{
		"value":               "0",
		"broadcaster_user_id": broadcasterUserID,
		"config_json":         string(configJSON),
		"last_disconnect":     "0",
		"last_decay":          "0",
	})
	pipe.Set(ctx, broadcasterKey(broadcasterUserID), overlayUUID.String(), 0)
	_, err = pipe.Exec(ctx)
	return err
}

// GetSession returns the current value and config for a session.
func (s *SessionStore) GetSession(ctx context.Context, overlayUUID uuid.UUID) (float64, *models.ConfigSnapshot, error) {
	key := sessionKey(overlayUUID)
	result, err := s.rdb.HGetAll(ctx, key).Result()
	if err != nil {
		return 0, nil, err
	}
	if len(result) == 0 {
		return 0, nil, nil // Session doesn't exist
	}

	value, err := strconv.ParseFloat(result["value"], 64)
	if err != nil {
		log.Printf("Warning: failed to parse session value for key %s: %v", key, err)
	}

	var config models.ConfigSnapshot
	if configJSON, ok := result["config_json"]; ok {
		if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
			return 0, nil, fmt.Errorf("failed to unmarshal config: %w", err)
		}
	}

	return value, &config, nil
}

// DeleteSession removes a session and its broadcaster mapping.
func (s *SessionStore) DeleteSession(ctx context.Context, overlayUUID uuid.UUID) error {
	key := sessionKey(overlayUUID)

	// Get broadcaster ID before deleting
	broadcasterID, err := s.rdb.HGet(ctx, key, "broadcaster_user_id").Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return err
	}

	pipe := s.rdb.Pipeline()
	pipe.Del(ctx, key)
	if broadcasterID != "" {
		pipe.Del(ctx, broadcasterKey(broadcasterID))
	}
	_, err = pipe.Exec(ctx)
	return err
}

// GetSessionByBroadcaster returns the overlay UUID for a broadcaster.
func (s *SessionStore) GetSessionByBroadcaster(ctx context.Context, twitchUserID string) (uuid.UUID, error) {
	result, err := s.rdb.Get(ctx, broadcasterKey(twitchUserID)).Result()
	if errors.Is(err, redis.Nil) {
		return uuid.Nil, nil
	}
	if err != nil {
		return uuid.Nil, err
	}
	return uuid.Parse(result)
}

// CheckAndSetDebounce atomically checks if a user has voted recently.
// Returns true if the vote should be allowed (no recent vote).
func (s *SessionStore) CheckAndSetDebounce(ctx context.Context, overlayUUID uuid.UUID, twitchUserID string) (bool, error) {
	key := debounceKey(overlayUUID, twitchUserID)
	set, err := s.rdb.SetNX(ctx, key, "1", debounceInterval).Result()
	if err != nil {
		return false, err
	}
	return set, nil // true = key was set (allowed), false = key already existed (debounced)
}

// UpdateValue sets the current sentiment value for a session.
func (s *SessionStore) UpdateValue(ctx context.Context, overlayUUID uuid.UUID, value float64) error {
	return s.rdb.HSet(ctx, sessionKey(overlayUUID), "value", strconv.FormatFloat(value, 'f', -1, 64)).Err()
}

// GetValue returns the current sentiment value for a session.
func (s *SessionStore) GetValue(ctx context.Context, overlayUUID uuid.UUID) (float64, bool, error) {
	result, err := s.rdb.HGet(ctx, sessionKey(overlayUUID), "value").Result()
	if errors.Is(err, redis.Nil) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	val, err := strconv.ParseFloat(result, 64)
	return val, true, err
}

// MarkDisconnected records the disconnect timestamp for a session.
func (s *SessionStore) MarkDisconnected(ctx context.Context, overlayUUID uuid.UUID, now time.Time) error {
	return s.rdb.HSet(ctx, sessionKey(overlayUUID), "last_disconnect", strconv.FormatInt(now.UnixMilli(), 10)).Err()
}

// ClearDisconnected clears the disconnect timestamp (client reconnected).
func (s *SessionStore) ClearDisconnected(ctx context.Context, overlayUUID uuid.UUID) error {
	return s.rdb.HSet(ctx, sessionKey(overlayUUID), "last_disconnect", "0").Err()
}

// UpdateConfig updates the session's config in Redis.
func (s *SessionStore) UpdateConfig(ctx context.Context, overlayUUID uuid.UUID, config models.ConfigSnapshot) error {
	configJSON, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}
	return s.rdb.HSet(ctx, sessionKey(overlayUUID), "config_json", string(configJSON)).Err()
}

// ListOrphanSessions scans for sessions with a last_disconnect older than maxAge.
func (s *SessionStore) ListOrphanSessions(ctx context.Context, maxAge time.Duration) ([]uuid.UUID, error) {
	var orphans []uuid.UUID
	now := time.Now()

	var cursor uint64
	for {
		keys, nextCursor, err := s.rdb.Scan(ctx, cursor, "session:*", 100).Result()
		if err != nil {
			return nil, err
		}

		for _, key := range keys {
			disconnectStr, err := s.rdb.HGet(ctx, key, "last_disconnect").Result()
			if err != nil || disconnectStr == "0" || disconnectStr == "" {
				continue
			}

			disconnectMs, err := strconv.ParseInt(disconnectStr, 10, 64)
			if err != nil || disconnectMs == 0 {
				continue
			}

			disconnectTime := time.UnixMilli(disconnectMs)
			if now.Sub(disconnectTime) > maxAge {
				// Extract UUID from key "session:{uuid}"
				uuidStr := strings.TrimPrefix(key, "session:")
				if id, err := uuid.Parse(uuidStr); err == nil {
					orphans = append(orphans, id)
				}
			}
		}

		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	return orphans, nil
}

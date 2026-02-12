package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/domain"
	goredis "github.com/redis/go-redis/v9"
)

const (
	// Redis hash field names for session keys.
	fieldValue          = "value"
	fieldBroadcasterID  = "broadcaster_user_id"
	fieldConfigJSON     = "config_json"
	fieldLastDisconnect = "last_disconnect"
	fieldLastUpdate     = "last_update"
)

type SessionRepo struct {
	rdb   *goredis.Client
	clock clockwork.Clock
}

func NewSessionRepo(rdb *goredis.Client, clock clockwork.Clock) *SessionRepo {
	return &SessionRepo{rdb: rdb, clock: clock}
}

// --- Session lifecycle ---

func (s *SessionRepo) ActivateSession(ctx context.Context, sid uuid.UUID, broadcasterUserID string, config domain.ConfigSnapshot) error {
	configJSON, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	sk := sessionKey(sid)
	bk := broadcasterKey(broadcasterUserID)

	// Check if a session exists
	exists, err := s.rdb.Exists(ctx, sk).Result()
	if err != nil {
		return fmt.Errorf("failed to check session existence: %w", err)
	}

	if exists != 0 {
		// Resume: clear last_disconnect
		return s.rdb.HSet(ctx, sk, fieldLastDisconnect, "0").Err()
	}

	// Create a new session
	pipe := s.rdb.Pipeline()
	pipe.HSet(ctx, sk, map[string]any{
		fieldValue:          "0",
		fieldBroadcasterID:  broadcasterUserID,
		fieldConfigJSON:     string(configJSON),
		fieldLastDisconnect: "0",
		fieldLastUpdate:     "0",
	})
	pipe.Set(ctx, bk, sid.String(), 0)
	_, err = pipe.Exec(ctx)
	return err
}

func (s *SessionRepo) ResumeSession(ctx context.Context, sid uuid.UUID) error {
	sk := sessionKey(sid)
	return s.rdb.HSet(ctx, sk, fieldLastDisconnect, "0").Err()
}

func (s *SessionRepo) SessionExists(ctx context.Context, sid uuid.UUID) (bool, error) {
	sk := sessionKey(sid)
	n, err := s.rdb.Exists(ctx, sk).Result()
	if err != nil {
		return false, fmt.Errorf("failed to check session existence: %w", err)
	}
	return n > 0, nil
}

func (s *SessionRepo) DeleteSession(ctx context.Context, sid uuid.UUID) error {
	sk := sessionKey(sid)
	rk := refCountKey(sid)

	broadcasterID, err := s.rdb.HGet(ctx, sk, fieldBroadcasterID).Result()
	if err != nil && !errors.Is(err, goredis.Nil) {
		return err
	}

	pipe := s.rdb.Pipeline()
	pipe.Del(ctx, sk)
	pipe.Del(ctx, rk)

	if broadcasterID != "" {
		bk := broadcasterKey(broadcasterID)
		pipe.Del(ctx, bk)
	}
	_, err = pipe.Exec(ctx)
	return err
}

func (s *SessionRepo) MarkDisconnected(ctx context.Context, sid uuid.UUID) error {
	sk := sessionKey(sid)
	now := s.clock.Now()
	formattedNow := strconv.FormatInt(now.UnixMilli(), 10)
	return s.rdb.HSet(ctx, sk, fieldLastDisconnect, formattedNow).Err()
}

// --- Session queries ---

// GetSessionByBroadcaster returns the overlay UUID for a broadcaster.
func (s *SessionRepo) GetSessionByBroadcaster(ctx context.Context, broadcasterUserID string) (uuid.UUID, bool, error) {
	bk := broadcasterKey(broadcasterUserID)

	result, err := s.rdb.Get(ctx, bk).Result()
	if errors.Is(err, goredis.Nil) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, err
	}

	id, err := uuid.Parse(result)
	if err != nil {
		return uuid.Nil, false, err
	}

	return id, true, nil
}

func (s *SessionRepo) GetSessionConfig(ctx context.Context, sid uuid.UUID) (*domain.ConfigSnapshot, error) {
	sk := sessionKey(sid)
	result, err := s.rdb.HGet(ctx, sk, fieldConfigJSON).Result()
	if errors.Is(err, goredis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var config domain.ConfigSnapshot
	if err := json.Unmarshal([]byte(result), &config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}
	return &config, nil
}

func (s *SessionRepo) UpdateConfig(ctx context.Context, sid uuid.UUID, config domain.ConfigSnapshot) error {
	configJSON, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	sk := sessionKey(sid)
	return s.rdb.HSet(ctx, sk, fieldConfigJSON, string(configJSON)).Err()
}

// --- Ref counting ---

func (s *SessionRepo) IncrRefCount(ctx context.Context, sid uuid.UUID) (int64, error) {
	rk := refCountKey(sid)
	return s.rdb.Incr(ctx, rk).Result()
}

func (s *SessionRepo) DecrRefCount(ctx context.Context, sid uuid.UUID) (int64, error) {
	rk := refCountKey(sid)
	return s.rdb.Decr(ctx, rk).Result()
}

// --- Orphan cleanup ---

func (s *SessionRepo) ListOrphans(ctx context.Context, maxAge time.Duration) ([]uuid.UUID, error) {
	now := s.clock.Now()
	var orphans []uuid.UUID
	var cursor uint64

	for {
		// Check context cancellation/timeout before each scan iteration
		select {
		case <-ctx.Done():
			return orphans, fmt.Errorf("scan cancelled after finding %d orphans: %w", len(orphans), ctx.Err())
		default:
		}

		keys, nextCursor, err := s.rdb.Scan(ctx, cursor, "session:*", 100).Result()
		if err != nil {
			return nil, fmt.Errorf("scan failed: %w", err)
		}

		for _, key := range keys {
			if id, isOrphan := s.checkOrphan(ctx, key, now, maxAge); isOrphan {
				orphans = append(orphans, id)
			}
		}

		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	return orphans, nil
}

func (s *SessionRepo) checkOrphan(ctx context.Context, key string, now time.Time, maxAge time.Duration) (uuid.UUID, bool) {
	val, err := s.rdb.HGet(ctx, key, fieldLastDisconnect).Result()
	if err != nil {
		if !errors.Is(err, goredis.Nil) {
			slog.Error("ListOrphans: failed to read key", "key", key, "error", err)
		}
		return uuid.Nil, false
	}

	ts, err := strconv.ParseInt(val, 10, 64)
	if err != nil || ts == 0 {
		return uuid.Nil, false
	}

	if now.Sub(time.UnixMilli(ts)) < maxAge {
		return uuid.Nil, false
	}

	id, err := uuid.Parse(strings.TrimPrefix(key, "session:"))
	if err != nil {
		slog.Warn("ListOrphans: invalid UUID key", "key", key, "error", err)
		return uuid.Nil, false
	}

	return id, true
}

// --- Key helpers ---

func sessionKey(sid uuid.UUID) string {
	return "session:" + sid.String()
}

func refCountKey(sid uuid.UUID) string {
	return "ref_count:" + sid.String()
}

func broadcasterKey(twitchUserID string) string {
	return "broadcaster:" + twitchUserID
}

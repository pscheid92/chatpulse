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
	"github.com/pscheid92/chatpulse/internal/metrics"
	goredis "github.com/redis/go-redis/v9"
)

const (
	// Redis hash field names for session keys.
	fieldValue          = "value"
	fieldBroadcasterID  = "broadcaster_user_id"
	fieldConfigJSON     = "config_json"
	fieldConfigVersion  = "config_version"
	fieldLastDisconnect = "last_disconnect"
	fieldLastUpdate     = "last_update"

	// Redis sorted set key for orphan cleanup optimization.
	disconnectedSessionsKey = "disconnected_sessions"
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
		// Resume: clear last_disconnect and remove from disconnected set
		pipe := s.rdb.Pipeline()
		pipe.HSet(ctx, sk, fieldLastDisconnect, "0")
		pipe.ZRem(ctx, disconnectedSessionsKey, sid.String())
		if _, err = pipe.Exec(ctx); err != nil {
			return fmt.Errorf("failed to resume session: %w", err)
		}
		return nil
	}

	// Create a new session
	pipe := s.rdb.Pipeline()
	pipe.HSet(ctx, sk, map[string]any{
		fieldValue:          "0",
		fieldBroadcasterID:  broadcasterUserID,
		fieldConfigJSON:     string(configJSON),
		fieldConfigVersion:  config.Version,
		fieldLastDisconnect: "0",
		fieldLastUpdate:     "0",
	})
	// Defensive 24h TTL: prevents indefinite memory leak if orphan cleanup fails
	// Active sessions get TTL refreshed on every vote
	pipe.Expire(ctx, sk, 24*time.Hour)
	pipe.Set(ctx, bk, sid.String(), 0)
	if _, err = pipe.Exec(ctx); err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	return nil
}

func (s *SessionRepo) ResumeSession(ctx context.Context, sid uuid.UUID) error {
	sk := sessionKey(sid)
	pipe := s.rdb.Pipeline()
	pipe.HSet(ctx, sk, fieldLastDisconnect, "0")
	pipe.ZRem(ctx, disconnectedSessionsKey, sid.String())
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("failed to resume session: %w", err)
	}
	return nil
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

	// Check ref count before deleting to prevent race condition deletions
	refCount, err := s.rdb.Get(ctx, rk).Int64()
	if err != nil && !errors.Is(err, goredis.Nil) {
		return fmt.Errorf("failed to check ref count before delete: %w", err)
	}

	if refCount > 0 {
		// Session is still active on another instance, skip deletion
		return fmt.Errorf("%w: ref_count=%d", domain.ErrSessionActive, refCount)
	}

	broadcasterID, err := s.rdb.HGet(ctx, sk, fieldBroadcasterID).Result()
	if err != nil && !errors.Is(err, goredis.Nil) {
		return fmt.Errorf("failed to get broadcaster ID: %w", err)
	}

	pipe := s.rdb.Pipeline()
	pipe.Del(ctx, sk)
	pipe.Del(ctx, rk)
	pipe.ZRem(ctx, disconnectedSessionsKey, sid.String())

	if broadcasterID != "" {
		bk := broadcasterKey(broadcasterID)
		pipe.Del(ctx, bk)
	}
	if _, err = pipe.Exec(ctx); err != nil {
		return fmt.Errorf("failed to delete session: %w", err)
	}
	return nil
}

func (s *SessionRepo) MarkDisconnected(ctx context.Context, sid uuid.UUID) error {
	sk := sessionKey(sid)
	now := s.clock.Now()
	formattedNow := strconv.FormatInt(now.UnixMilli(), 10)

	// Update session hash and add to disconnected sorted set atomically
	pipe := s.rdb.Pipeline()
	pipe.HSet(ctx, sk, fieldLastDisconnect, formattedNow)
	pipe.ZAdd(ctx, disconnectedSessionsKey, goredis.Z{
		Score:  float64(now.Unix()),
		Member: sid.String(),
	})
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("failed to mark session as disconnected: %w", err)
	}
	return nil
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

	// Fetch both config JSON and version field
	pipe := s.rdb.Pipeline()
	configCmd := pipe.HGet(ctx, sk, fieldConfigJSON)
	versionCmd := pipe.HGet(ctx, sk, fieldConfigVersion)
	_, err := pipe.Exec(ctx)

	if errors.Is(err, goredis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to fetch session config: %w", err)
	}

	configJSON, err := configCmd.Result()
	if errors.Is(err, goredis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get config JSON: %w", err)
	}

	var config domain.ConfigSnapshot
	if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Check for version mismatch (drift detection)
	versionStr, err := versionCmd.Result()
	if err == nil {
		redisVersion, parseErr := strconv.Atoi(versionStr)
		if parseErr == nil && config.Version != redisVersion {
			slog.Warn("config version mismatch detected",
				"session_uuid", sid.String(),
				"redis_version", redisVersion,
				"config_json_version", config.Version)
			metrics.ConfigDriftDetected.WithLabelValues(sid.String()).Inc()
		}
	}

	return &config, nil
}

func (s *SessionRepo) UpdateConfig(ctx context.Context, sid uuid.UUID, config domain.ConfigSnapshot) error {
	configJSON, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	sk := sessionKey(sid)
	if err := s.rdb.HSet(ctx, sk,
		fieldConfigJSON, string(configJSON),
		fieldConfigVersion, config.Version,
	).Err(); err != nil {
		return fmt.Errorf("failed to update config: %w", err)
	}
	return nil
}

// --- Ref counting ---

func (s *SessionRepo) IncrRefCount(ctx context.Context, sid uuid.UUID) (int64, error) {
	rk := refCountKey(sid)
	count, err := s.rdb.Incr(ctx, rk).Result()
	if err != nil {
		return 0, fmt.Errorf("failed to increment ref count: %w", err)
	}

	// Defensive check: suspiciously high ref count (likely leak or race condition)
	if count >= 10 {
		metrics.RefCountAnomaliesTotal.WithLabelValues("high_incr").Inc()
		slog.Warn("Ref count suspiciously high on increment - possible leak",
			"session_uuid", sid.String(),
			"count", count)
	}

	return count, nil
}

func (s *SessionRepo) DecrRefCount(ctx context.Context, sid uuid.UUID) (int64, error) {
	rk := refCountKey(sid)
	count, err := s.rdb.Decr(ctx, rk).Result()
	if err != nil {
		return 0, fmt.Errorf("failed to decrement ref count: %w", err)
	}

	// Defensive check: ref count went negative (underflow - more decrements than increments)
	if count < 0 {
		metrics.RefCountAnomaliesTotal.WithLabelValues("negative").Inc()
		slog.Warn("Ref count went negative - resetting to 0 (self-healing)",
			"session_uuid", sid.String(),
			"count", count)

		// Self-heal: reset to 0 to prevent permanent negative state
		if err := s.rdb.Set(ctx, rk, 0, 0).Err(); err != nil {
			slog.Error("Failed to reset negative ref count", "error", err)
		}
		return 0, nil
	}

	return count, nil
}

// --- Orphan cleanup ---

// DisconnectedCount returns the number of sessions in the disconnected set.
func (s *SessionRepo) DisconnectedCount(ctx context.Context) (int64, error) {
	count, err := s.rdb.ZCard(ctx, disconnectedSessionsKey).Result()
	if err != nil {
		return 0, fmt.Errorf("zcard failed: %w", err)
	}
	return count, nil
}

func (s *SessionRepo) ListOrphans(ctx context.Context, maxAge time.Duration) ([]uuid.UUID, error) {
	now := s.clock.Now()
	cutoff := now.Add(-maxAge).Unix()

	// Use ZRANGEBYSCORE to efficiently query disconnected sessions older than cutoff
	// Score is Unix timestamp, so this returns all sessions disconnected before cutoff
	members, err := s.rdb.ZRangeByScore(ctx, disconnectedSessionsKey, &goredis.ZRangeBy{
		Min: "-inf",
		Max: fmt.Sprintf("%d", cutoff),
	}).Result()

	if err != nil {
		return nil, fmt.Errorf("zrangebyscore failed: %w", err)
	}

	// Parse UUIDs from sorted set members
	orphans := make([]uuid.UUID, 0, len(members))
	for _, member := range members {
		id, err := uuid.Parse(member)
		if err != nil {
			slog.Warn("ListOrphans: invalid UUID in sorted set", "member", member, "error", err)
			continue
		}
		orphans = append(orphans, id)
	}

	return orphans, nil
}

// --- Reconciliation ---

func (s *SessionRepo) ListActiveSessions(ctx context.Context) ([]domain.ActiveSession, error) {
	var sessions []domain.ActiveSession
	var cursor uint64

	for {
		// Check context cancellation before each scan iteration
		select {
		case <-ctx.Done():
			return sessions, fmt.Errorf("scan cancelled: %w", ctx.Err())
		default:
		}

		keys, nextCursor, err := s.rdb.Scan(ctx, cursor, "session:*", 100).Result()
		if err != nil {
			return nil, fmt.Errorf("scan failed: %w", err)
		}

		for _, key := range keys {
			sessionUUID, err := uuid.Parse(strings.TrimPrefix(key, "session:"))
			if err != nil {
				slog.Warn("ListActiveSessions: invalid UUID key", "key", key, "error", err)
				continue
			}

			// Fetch broadcaster_user_id from session hash
			broadcasterUserID, err := s.rdb.HGet(ctx, key, fieldBroadcasterID).Result()
			if err != nil {
				if !errors.Is(err, goredis.Nil) {
					slog.Error("ListActiveSessions: failed to read broadcaster_user_id", "key", key, "error", err)
				}
				continue
			}

			// For now, we don't have user_id stored in Redis session hash
			// The reconciler will need to look it up via GetUserByOverlayUUID if needed
			sessions = append(sessions, domain.ActiveSession{
				OverlayUUID:       sessionUUID,
				BroadcasterUserID: broadcasterUserID,
				UserID:            uuid.Nil, // Not stored in Redis, reconciler must lookup if needed
			})
		}

		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	return sessions, nil
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

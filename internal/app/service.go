package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/coordination"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/chatpulse/internal/metrics"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
)

const (
	// Performance-critical constants (intentionally not configurable - see CLAUDE.md)
	cleanupScanTimeout    = 30 * time.Second // Maximum time for ListOrphans SCAN operation
	maxCleanupBackoff     = 5 * time.Minute  // Maximum exponential backoff duration
	maxKeysPerCleanupRun  = 1000             // Redis SCAN batch size
	maxUnsubscribeRetries = 5                // Twitch API retry attempts
)

// Service is the application layer — the only component that references multiple
// domain components. It orchestrates all use cases.
type Service struct {
	users            domain.UserRepository
	configs          domain.ConfigRepository
	store            domain.SessionRepository
	engine           domain.Engine
	twitch           domain.TwitchService
	redis            *redis.Client
	activationGroup  singleflight.Group
	clock            clockwork.Clock
	cleanupStopCh    chan struct{}
	stopOnce         sync.Once
	cleanupWg        sync.WaitGroup
	cleanupFailures  int           // consecutive cleanup failures
	cleanupBackoff   time.Duration // current backoff duration
	cleanupMu        sync.Mutex    // protects cleanup state
	orphanMaxAge     time.Duration
	cleanupInterval  time.Duration
	leaderElector    *LeaderElector // Redis-based leader election for cleanup
	isLeader         bool           // true if this instance is cleanup leader
}

// NewService creates the application layer service.
// twitch may be nil if webhooks are not configured.
// orphanMaxAge and cleanupInterval control orphan session cleanup timing.
// rdb is used for leader election to ensure only one instance runs cleanup.
func NewService(users domain.UserRepository, configs domain.ConfigRepository, store domain.SessionRepository, engine domain.Engine, twitch domain.TwitchService, clock clockwork.Clock, rdb *redis.Client, orphanMaxAge, cleanupInterval time.Duration) *Service {
	instanceID := generateInstanceID()

	s := &Service{
		users:           users,
		configs:         configs,
		store:           store,
		engine:          engine,
		twitch:          twitch,
		redis:           rdb,
		clock:           clock,
		cleanupStopCh:   make(chan struct{}),
		orphanMaxAge:    orphanMaxAge,
		cleanupInterval: cleanupInterval,
		leaderElector:   NewLeaderElector(rdb, instanceID),
		isLeader:        false,
	}

	s.startCleanupTimer()
	return s
}

// generateInstanceID creates a unique instance identifier.
// Format: hostname-PID for uniqueness across restarts and instances.
func generateInstanceID() string {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}
	return fmt.Sprintf("%s-%d", hostname, os.Getpid())
}

// GetUserByID retrieves a user by internal ID.
func (s *Service) GetUserByID(ctx context.Context, userID uuid.UUID) (*domain.User, error) {
	return s.users.GetByID(ctx, userID)
}

// GetUserByOverlayUUID retrieves a user by overlay UUID.
func (s *Service) GetUserByOverlayUUID(ctx context.Context, overlayUUID uuid.UUID) (*domain.User, error) {
	return s.users.GetByOverlayUUID(ctx, overlayUUID)
}

// GetConfig retrieves a user's config.
func (s *Service) GetConfig(ctx context.Context, userID uuid.UUID) (*domain.Config, error) {
	return s.configs.GetByUserID(ctx, userID)
}

// UpsertUser creates or updates a user.
func (s *Service) UpsertUser(ctx context.Context, twitchUserID, twitchUsername, accessToken, refreshToken string, tokenExpiry time.Time) (*domain.User, error) {
	return s.users.Upsert(ctx, twitchUserID, twitchUsername, accessToken, refreshToken, tokenExpiry)
}

// EnsureSessionActive activates a session if not already active, or resumes it.
// Uses singleflight to collapse concurrent activations for the same session.
// EnsureSessionActive ensures a session is active (exists in Redis and has EventSub subscription).
// Uses singleflight with DoChan to provide per-caller timeout protection while deduplicating
// concurrent activation attempts.
//
// Timeout behavior:
// - Activation runs with 30s generous timeout (shared across all concurrent callers)
// - Each caller respects their own context timeout (e.g., 5s WebSocket deadline)
// - If a caller times out, activation continues in background (benefits future callers)
// - If activation fails, all waiting callers receive the same error (shared fate)
func (s *Service) EnsureSessionActive(ctx context.Context, overlayUUID uuid.UUID) error {
	// Use DoChan for non-blocking singleflight with per-caller timeout control
	ch := s.activationGroup.DoChan(overlayUUID.String(), func() (any, error) {
		// Create activation context with generous 30s timeout
		// This is the shared execution - should be generous to handle slow DB/Twitch API
		activationCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		exists, err := s.store.SessionExists(activationCtx, overlayUUID)
		if err != nil {
			return nil, err
		}

		if exists {
			return nil, s.store.ResumeSession(activationCtx, overlayUUID)
		}

		// New session — need DB lookup
		user, err := s.users.GetByOverlayUUID(activationCtx, overlayUUID)
		if err != nil {
			return nil, err
		}

		config, err := s.configs.GetByUserID(activationCtx, user.ID)
		if err != nil {
			return nil, err
		}

		snapshot := domain.ConfigSnapshot{
			ForTrigger:     config.ForTrigger,
			AgainstTrigger: config.AgainstTrigger,
			LeftLabel:      config.LeftLabel,
			RightLabel:     config.RightLabel,
			DecaySpeed:     config.DecaySpeed,
			Version:        config.Version,
		}

		if err := s.store.ActivateSession(activationCtx, overlayUUID, user.TwitchUserID, snapshot); err != nil {
			return nil, err
		}

		if s.twitch != nil {
			if err := s.twitch.Subscribe(activationCtx, user.ID, user.TwitchUserID); err != nil {
				if delErr := s.store.DeleteSession(activationCtx, overlayUUID); delErr != nil {
					slog.Error("Failed to rollback session after subscribe failure", "session_uuid", overlayUUID.String(), "error", delErr)
				}
				return nil, err
			}
		}

		return nil, nil
	})

	// Each caller waits with their own context timeout
	select {
	case result := <-ch:
		return result.Err
	case <-ctx.Done():
		// Caller's context timed out (e.g., 5s WebSocket deadline)
		// Activation may still be running in background (benefits future callers)
		slog.Warn("session activation timeout (caller-specific)",
			"session_uuid", overlayUUID,
			"error", ctx.Err())
		metrics.SessionActivationTimeoutsTotal.WithLabelValues("context_deadline").Inc()
		return fmt.Errorf("session activation timeout: %w", ctx.Err())
	}
}

// OnSessionEmpty is called when the last local client disconnects from a session.
// It decrements the ref count and marks the session as disconnected if ref count
// reaches 0 (meaning no instances have active clients for this session).
func (s *Service) OnSessionEmpty(ctx context.Context, sessionUUID uuid.UUID) {
	count, err := s.store.DecrRefCount(ctx, sessionUUID)
	if err != nil {
		slog.Error("DecrRefCount error", "session_uuid", sessionUUID.String(), "error", err)
		return
	}

	if count <= 0 {
		if err := s.store.MarkDisconnected(ctx, sessionUUID); err != nil {
			slog.Error("MarkDisconnected error", "session_uuid", sessionUUID.String(), "error", err)
		}
		slog.Info("Session marked as disconnected", "session_uuid", sessionUUID.String(), "ref_count", count)
	}
}

// IncrRefCount increments the reference count for a session.
// Called when the first local client connects to a session (after EnsureSessionActive).
func (s *Service) IncrRefCount(ctx context.Context, sessionUUID uuid.UUID) error {
	_, err := s.store.IncrRefCount(ctx, sessionUUID)
	return err
}

// ResetSentiment resets the sentiment value for a session to zero.
func (s *Service) ResetSentiment(ctx context.Context, overlayUUID uuid.UUID) error {
	return s.engine.ResetSentiment(ctx, overlayUUID)
}

// SaveConfig validates and saves the config, updating the live session if active.
func (s *Service) SaveConfig(ctx context.Context, userID uuid.UUID, forTrigger, againstTrigger, leftLabel, rightLabel string, decaySpeed float64, overlayUUID uuid.UUID) error {
	if err := s.configs.Update(ctx, userID, forTrigger, againstTrigger, leftLabel, rightLabel, decaySpeed); err != nil {
		return err
	}

	// Fetch updated config to get new version (incremented by trigger)
	config, err := s.configs.GetByUserID(ctx, userID)
	if err != nil {
		return fmt.Errorf("failed to fetch updated config: %w", err)
	}

	snapshot := domain.ConfigSnapshot{
		ForTrigger:     forTrigger,
		AgainstTrigger: againstTrigger,
		LeftLabel:      leftLabel,
		RightLabel:     rightLabel,
		DecaySpeed:     decaySpeed,
		Version:        config.Version,
	}

	// Update live session in Redis - no longer best-effort
	// Return error to caller if Redis update fails (prevents silent drift)
	if err := s.store.UpdateConfig(ctx, overlayUUID, snapshot); err != nil {
		slog.Error("Failed to update session config in Redis",
			"user_id", userID,
			"overlay_uuid", overlayUUID,
			"error", err)

		// Return error to handler - user should see error and can retry
		return fmt.Errorf("config saved to database but failed to update active session: %w", err)
	}

	// Invalidate local config cache so next GetCurrentValue() fetches fresh config
	s.engine.InvalidateConfigCache(overlayUUID)

	// Broadcast invalidation to other instances via pub/sub
	if err := coordination.PublishConfigInvalidation(ctx, s.redis, overlayUUID); err != nil {
		slog.Warn("Failed to publish config invalidation",
			"overlay_uuid", overlayUUID,
			"error", err)
		// Non-fatal: other instances will get update via TTL expiry
	}

	return nil
}

// RotateOverlayUUID generates a new overlay UUID for a user.
func (s *Service) RotateOverlayUUID(ctx context.Context, userID uuid.UUID) (uuid.UUID, error) {
	return s.users.RotateOverlayUUID(ctx, userID)
}

// CleanupOrphans removes sessions that have been disconnected longer than orphanMaxAge.
// Uses a timeout to prevent unbounded scan operations on large Redis datasets.
// Race condition handling: skips sessions with ref_count > 0 (reconnected during scan).
// Returns error if cleanup fails (triggers failure budget backoff).
func (s *Service) CleanupOrphans(ctx context.Context) error {
	start := s.clock.Now()
	defer func() {
		metrics.OrphanCleanupScansTotal.Inc()
		metrics.OrphanCleanupDurationSeconds.Observe(s.clock.Since(start).Seconds())
	}()

	// Update disconnected sessions count metric
	if count, err := s.store.DisconnectedCount(ctx); err == nil {
		metrics.DisconnectedSessionsCount.Set(float64(count))
	}

	// Add timeout to prevent unbounded scan operations
	scanCtx, cancel := context.WithTimeout(ctx, cleanupScanTimeout)
	defer cancel()

	orphans, err := s.store.ListOrphans(scanCtx, s.orphanMaxAge)
	if err != nil {
		slog.Error("ListOrphans error", "error", err)
		return err
	}

	var deletedSessions []uuid.UUID

	for _, id := range orphans {
		if err := s.store.DeleteSession(ctx, id); err != nil {
			if errors.Is(err, domain.ErrSessionActive) {
				// Expected: session reconnected during scan, skip deletion
				slog.Debug("Skipped active session during cleanup",
					"session_uuid", id.String(),
					"ref_count", "positive")
				metrics.OrphanSessionsSkippedTotal.WithLabelValues("active").Inc()
				continue
			}
			// Unexpected error
			slog.Error("DeleteSession error",
				"session_uuid", id.String(),
				"error", err)
			metrics.OrphanSessionsSkippedTotal.WithLabelValues("error").Inc()
			continue
		}

		// Successfully deleted
		metrics.OrphanSessionsDeletedTotal.Inc()
		deletedSessions = append(deletedSessions, id)
	}

	// Background unsubscribe for deleted sessions only
	if s.twitch != nil && len(deletedSessions) > 0 {
		s.cleanupWg.Go(func() {
			bgCtx := context.Background()
			for _, overlayUUID := range deletedSessions {
				user, err := s.users.GetByOverlayUUID(bgCtx, overlayUUID)
				if err != nil {
					if errors.Is(err, domain.ErrUserNotFound) {
						// User was deleted, subscription is orphaned but can't unsubscribe
						slog.Debug("User not found for orphan session", "session_uuid", overlayUUID.String())
						continue
					}
					slog.Error("Failed to look up user for orphan session",
						"session_uuid", overlayUUID.String(),
						"error", err)
					metrics.CleanupUnsubscribeErrorsTotal.Inc()
					continue
				}

				if err := s.twitch.Unsubscribe(bgCtx, user.ID); err != nil {
					if errors.Is(err, domain.ErrSubscriptionNotFound) {
						// Idempotent: already unsubscribed, success
						slog.Debug("Subscription already removed",
							"session_uuid", overlayUUID.String(),
							"user_id", user.ID.String())
					} else {
						slog.Error("Failed to unsubscribe orphan session",
							"session_uuid", overlayUUID.String(),
							"user_id", user.ID.String(),
							"error", err)
						metrics.CleanupUnsubscribeErrorsTotal.Inc()
					}
					continue
				}

				slog.Info("Cleaned up orphan session",
					"session_uuid", overlayUUID.String(),
					"user_id", user.ID.String())
			}
		})
	}

	return nil
}

func (s *Service) startCleanupTimer() {
	// Add random jitter (0-10s) to spread cleanup load across instances
	// This reduces thundering herd problem (all instances starting cleanup simultaneously)
	jitter := time.Duration(rand.Intn(10)) * time.Second
	firstInterval := s.cleanupInterval + jitter

	// Use regular interval after first tick
	ticker := s.clock.NewTicker(s.cleanupInterval)

	// Initial delay with jitter
	initialTimer := s.clock.NewTimer(firstInterval)

	// Leader election: renew lease every 15s (half of 30s TTL)
	renewTicker := s.clock.NewTicker(15 * time.Second)

	go func() {
		defer ticker.Stop()
		defer renewTicker.Stop()

		// Wait for initial jittered delay
		select {
		case <-initialTimer.Chan():
			// First cleanup after jittered interval
		case <-s.cleanupStopCh:
			initialTimer.Stop()
			return
		}

		// Then continue with regular ticker
		for {
			select {
			case <-ticker.Chan():
				// Check failure budget before running
				s.cleanupMu.Lock()
				failures := s.cleanupFailures
				s.cleanupMu.Unlock()

				if failures > 0 {
					// Exponential backoff: 30s, 60s, 120s, 240s, 300s (max)
					backoff := time.Duration(1<<uint(failures-1)) * s.cleanupInterval
					if backoff > maxCleanupBackoff {
						backoff = maxCleanupBackoff
					}

					slog.Info("cleanup backoff after failures",
						"failures", failures,
						"backoff_seconds", backoff.Seconds())

					// Sleep with interruption support
					timer := s.clock.NewTimer(backoff)
					select {
					case <-timer.Chan():
						// Backoff complete
					case <-s.cleanupStopCh:
						timer.Stop()
						return
					}
				}

				// Try to become leader (if not already)
				if !s.isLeader {
					acquired, err := s.leaderElector.TryAcquire(context.Background())
					if err != nil {
						slog.Error("failed to acquire leader lock", "error", err)
						metrics.LeaderElectionFailuresTotal.WithLabelValues("acquire_failed").Inc()
						continue
					}
					if !acquired {
						slog.Debug("skipping cleanup (not leader)")
						metrics.CleanupSkippedTotal.WithLabelValues("not_leader").Inc()
						continue
					}
					s.isLeader = true
					metrics.CleanupLeaderGauge.WithLabelValues(s.leaderElector.instanceID).Set(1)
					slog.Info("became cleanup leader", "instance_id", s.leaderElector.instanceID)
				}

				// Run cleanup as leader
				err := s.CleanupOrphans(context.Background())

				s.cleanupMu.Lock()
				if err != nil {
					s.cleanupFailures++
					metrics.OrphanCleanupFailuresTotal.Inc()
					slog.Error("cleanup failed", "error", err, "failures", s.cleanupFailures)
				} else {
					// Reset on success
					if s.cleanupFailures > 0 {
						slog.Info("cleanup recovered", "previous_failures", s.cleanupFailures)
					}
					s.cleanupFailures = 0
				}
				s.cleanupMu.Unlock()

			case <-renewTicker.Chan():
				// Renew leadership lease
				if s.isLeader {
					if err := s.leaderElector.Renew(context.Background()); err != nil {
						slog.Warn("lost leader lock", "error", err)
						metrics.LeaderElectionFailuresTotal.WithLabelValues("renew_failed").Inc()
						metrics.CleanupLeaderGauge.WithLabelValues(s.leaderElector.instanceID).Set(0)
						s.isLeader = false
					}
				}

			case <-s.cleanupStopCh:
				return
			}
		}
	}()
	slog.Info("Cleanup timer started with leader election", "interval", "30s")
}

// Stop stops the cleanup timer and waits for in-flight cleanup goroutines to finish.
// Gracefully releases leadership lock if this instance is the leader.
func (s *Service) Stop() {
	s.stopOnce.Do(func() {
		// Release leadership before exiting
		if s.isLeader {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := s.leaderElector.Release(ctx); err != nil {
				slog.Error("failed to release leader lock", "error", err)
			} else {
				slog.Info("released cleanup leader lock")
			}
		}

		close(s.cleanupStopCh)
	})
	s.cleanupWg.Wait()
}

package app

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/chatpulse/internal/metrics"
	"golang.org/x/sync/singleflight"
)

const (
	orphanMaxAge      = 30 * time.Second
	cleanupInterval   = 30 * time.Second
	cleanupScanTimeout = 30 * time.Second
)

// Service is the application layer — the only component that references multiple
// domain components. It orchestrates all use cases.
type Service struct {
	users           domain.UserRepository
	configs         domain.ConfigRepository
	store           domain.SessionRepository
	engine          domain.Engine
	twitch          domain.TwitchService
	activationGroup singleflight.Group
	clock           clockwork.Clock
	cleanupStopCh   chan struct{}
	stopOnce        sync.Once
	cleanupWg       sync.WaitGroup
}

// NewService creates the application layer service.
// twitch may be nil if webhooks are not configured.
func NewService(users domain.UserRepository, configs domain.ConfigRepository, store domain.SessionRepository, engine domain.Engine, twitch domain.TwitchService, clock clockwork.Clock) *Service {
	s := &Service{
		users:         users,
		configs:       configs,
		store:         store,
		engine:        engine,
		twitch:        twitch,
		clock:         clock,
		cleanupStopCh: make(chan struct{}),
	}

	s.startCleanupTimer()
	return s
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
func (s *Service) EnsureSessionActive(ctx context.Context, overlayUUID uuid.UUID) error {
	_, err, _ := s.activationGroup.Do(overlayUUID.String(), func() (any, error) {
		exists, err := s.store.SessionExists(ctx, overlayUUID)
		if err != nil {
			return nil, err
		}

		if exists {
			return nil, s.store.ResumeSession(ctx, overlayUUID)
		}

		// New session — need DB lookup
		user, err := s.users.GetByOverlayUUID(ctx, overlayUUID)
		if err != nil {
			return nil, err
		}

		config, err := s.configs.GetByUserID(ctx, user.ID)
		if err != nil {
			return nil, err
		}

		snapshot := domain.ConfigSnapshot{
			ForTrigger:     config.ForTrigger,
			AgainstTrigger: config.AgainstTrigger,
			LeftLabel:      config.LeftLabel,
			RightLabel:     config.RightLabel,
			DecaySpeed:     config.DecaySpeed,
		}

		if err := s.store.ActivateSession(ctx, overlayUUID, user.TwitchUserID, snapshot); err != nil {
			return nil, err
		}

		if s.twitch != nil {
			if err := s.twitch.Subscribe(ctx, user.ID, user.TwitchUserID); err != nil {
				if delErr := s.store.DeleteSession(ctx, overlayUUID); delErr != nil {
					slog.Error("Failed to rollback session after subscribe failure", "session_uuid", overlayUUID.String(), "error", delErr)
				}
				return nil, err
			}
		}

		return nil, nil
	})
	return err
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

	snapshot := domain.ConfigSnapshot{
		ForTrigger:     forTrigger,
		AgainstTrigger: againstTrigger,
		LeftLabel:      leftLabel,
		RightLabel:     rightLabel,
		DecaySpeed:     decaySpeed,
	}
	// Best-effort update of live session
	if err := s.store.UpdateConfig(ctx, overlayUUID, snapshot); err != nil {
		slog.Error("Failed to update live session config", "error", err)
	}

	// Invalidate config cache so next GetCurrentValue() fetches fresh config
	s.engine.InvalidateConfigCache(overlayUUID)

	return nil
}

// RotateOverlayUUID generates a new overlay UUID for a user.
func (s *Service) RotateOverlayUUID(ctx context.Context, userID uuid.UUID) (uuid.UUID, error) {
	return s.users.RotateOverlayUUID(ctx, userID)
}

// CleanupOrphans removes sessions that have been disconnected longer than orphanMaxAge.
// Uses a timeout to prevent unbounded scan operations on large Redis datasets.
// Race condition handling: skips sessions with ref_count > 0 (reconnected during scan).
func (s *Service) CleanupOrphans(ctx context.Context) {
	start := s.clock.Now()
	defer func() {
		metrics.OrphanCleanupScansTotal.Inc()
		metrics.OrphanCleanupDurationSeconds.Observe(s.clock.Since(start).Seconds())
	}()

	// Add timeout to prevent unbounded scan operations
	scanCtx, cancel := context.WithTimeout(ctx, cleanupScanTimeout)
	defer cancel()

	orphans, err := s.store.ListOrphans(scanCtx, orphanMaxAge)
	if err != nil {
		slog.Error("ListOrphans error", "error", err)
		return
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
}

func (s *Service) startCleanupTimer() {
	ticker := s.clock.NewTicker(cleanupInterval)
	go func() {
		for {
			select {
			case <-ticker.Chan():
				s.CleanupOrphans(context.Background())
			case <-s.cleanupStopCh:
				ticker.Stop()
				return
			}
		}
	}()
	slog.Info("Cleanup timer started", "interval", "30s")
}

// Stop stops the cleanup timer and waits for in-flight cleanup goroutines to finish.
func (s *Service) Stop() {
	s.stopOnce.Do(func() {
		close(s.cleanupStopCh)
	})
	s.cleanupWg.Wait()
}

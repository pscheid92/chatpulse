package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/chatpulse/internal/metrics"
)

// ConfigReconciler periodically checks for config drift between Redis and PostgreSQL.
type ConfigReconciler struct {
	configRepo  domain.ConfigRepository
	sessionRepo domain.SessionRepository
	userRepo    domain.UserRepository
	interval    time.Duration
	clock       clockwork.Clock
	stopCh      chan struct{}
}

// NewConfigReconciler creates a new config reconciliation background job.
func NewConfigReconciler(
	configRepo domain.ConfigRepository,
	sessionRepo domain.SessionRepository,
	userRepo domain.UserRepository,
	clock clockwork.Clock,
) *ConfigReconciler {
	return &ConfigReconciler{
		configRepo:  configRepo,
		sessionRepo: sessionRepo,
		userRepo:    userRepo,
		interval:    5 * time.Minute, // Check every 5 minutes
		clock:       clock,
		stopCh:      make(chan struct{}),
	}
}

// Start runs the reconciliation loop until Stop is called.
func (r *ConfigReconciler) Start(ctx context.Context) {
	ticker := r.clock.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.Chan():
			if err := r.reconcile(ctx); err != nil {
				slog.Error("Config reconciliation failed", "error", err)
			}
		case <-r.stopCh:
			slog.Info("Config reconciler stopped")
			return
		case <-ctx.Done():
			slog.Info("Config reconciler context cancelled")
			return
		}
	}
}

// Stop gracefully stops the reconciliation loop.
func (r *ConfigReconciler) Stop() {
	close(r.stopCh)
}

// reconcile checks all active sessions for config drift and auto-fixes mismatches.
func (r *ConfigReconciler) reconcile(ctx context.Context) error {
	sessions, err := r.sessionRepo.ListActiveSessions(ctx)
	if err != nil {
		return fmt.Errorf("failed to list active sessions: %w", err)
	}

	for _, session := range sessions {
		// Get Redis config
		redisConfig, err := r.sessionRepo.GetSessionConfig(ctx, session.OverlayUUID)
		if err != nil {
			slog.Warn("Failed to get Redis config during reconciliation",
				"session_uuid", session.OverlayUUID,
				"error", err)
			continue
		}
		if redisConfig == nil {
			continue
		}

		// Lookup user_id via overlay_uuid (Redis doesn't store user_id)
		user, err := r.userRepo.GetByOverlayUUID(ctx, session.OverlayUUID)
		if err != nil {
			slog.Warn("Failed to get user during reconciliation",
				"overlay_uuid", session.OverlayUUID,
				"error", err)
			continue
		}

		// Get PostgreSQL config (source of truth)
		dbConfig, err := r.configRepo.GetByUserID(ctx, user.ID)
		if err != nil {
			slog.Warn("Failed to get DB config during reconciliation",
				"user_id", user.ID,
				"error", err)
			continue
		}

		// Check for version mismatch
		if redisConfig.Version != dbConfig.Version {
			slog.Warn("Config drift detected during reconciliation",
				"user_id", user.ID,
				"session_uuid", session.OverlayUUID,
				"redis_version", redisConfig.Version,
				"db_version", dbConfig.Version)

			metrics.ConfigDriftDetected.WithLabelValues(session.OverlayUUID.String()).Inc()

			// Auto-fix: Update Redis with latest DB config
			configSnapshot := domain.ConfigSnapshot{
				ForTrigger:     dbConfig.ForTrigger,
				AgainstTrigger: dbConfig.AgainstTrigger,
				LeftLabel:      dbConfig.LeftLabel,
				RightLabel:     dbConfig.RightLabel,
				DecaySpeed:     dbConfig.DecaySpeed,
				Version:        dbConfig.Version,
			}

			if err := r.sessionRepo.UpdateConfig(ctx, session.OverlayUUID, configSnapshot); err != nil {
				slog.Error("Failed to fix config drift",
					"session_uuid", session.OverlayUUID,
					"error", err)
			} else {
				slog.Info("Config drift auto-fixed",
					"session_uuid", session.OverlayUUID,
					"old_version", redisConfig.Version,
					"new_version", dbConfig.Version)
				metrics.ConfigDriftFixed.WithLabelValues(session.OverlayUUID.String()).Inc()
			}
		}
	}

	return nil
}

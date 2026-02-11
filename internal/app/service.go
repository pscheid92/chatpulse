package app

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/domain"
	"golang.org/x/sync/singleflight"
)

const (
	orphanMaxAge    = 30 * time.Second
	cleanupInterval = 30 * time.Second
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
					log.Printf("Failed to rollback session %s after subscribe failure: %v", overlayUUID, delErr)
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
		log.Printf("DecrRefCount error for session %s: %v", sessionUUID, err)
		return
	}

	if count <= 0 {
		if err := s.store.MarkDisconnected(ctx, sessionUUID); err != nil {
			log.Printf("MarkDisconnected error for session %s: %v", sessionUUID, err)
		}
		log.Printf("Session %s marked as disconnected (ref_count=%d)", sessionUUID, count)
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
		log.Printf("Failed to update live session config: %v", err)
	}

	return nil
}

// RotateOverlayUUID generates a new overlay UUID for a user.
func (s *Service) RotateOverlayUUID(ctx context.Context, userID uuid.UUID) (uuid.UUID, error) {
	return s.users.RotateOverlayUUID(ctx, userID)
}

// CleanupOrphans removes sessions that have been disconnected longer than orphanMaxAge.
func (s *Service) CleanupOrphans(ctx context.Context) {
	orphans, err := s.store.ListOrphans(ctx, orphanMaxAge)
	if err != nil {
		log.Printf("ListOrphans error: %v", err)
		return
	}

	for _, id := range orphans {
		if err := s.store.DeleteSession(ctx, id); err != nil {
			log.Printf("DeleteSession error for %s: %v", id, err)
		}
	}

	if s.twitch != nil {
		s.cleanupWg.Go(func() {
			bgCtx := context.Background()
			for _, overlayUUID := range orphans {
				user, err := s.users.GetByOverlayUUID(bgCtx, overlayUUID)
				if err != nil {
					log.Printf("Failed to look up user for orphan session %s: %v", overlayUUID, err)
					continue
				}
				if err := s.twitch.Unsubscribe(bgCtx, user.ID); err != nil {
					log.Printf("Failed to unsubscribe orphan session %s: %v", overlayUUID, err)
					continue
				}
				log.Printf("Cleaned up orphan session %s", overlayUUID)
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
	log.Println("Cleanup timer started (30s interval)")
}

// Stop stops the cleanup timer and waits for in-flight cleanup goroutines to finish.
func (s *Service) Stop() {
	s.stopOnce.Do(func() {
		close(s.cleanupStopCh)
	})
	s.cleanupWg.Wait()
}

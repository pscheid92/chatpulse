package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/coordination"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/uuid"
	"github.com/redis/go-redis/v9"
)

// Service is the application layer — the only component that references multiple
// domain components. It orchestrates all use cases.
type Service struct {
	users                  domain.UserRepository
	configs                domain.ConfigRepository
	store                  domain.SessionRepository
	engine                 domain.Engine
	twitch                 domain.TwitchService
	configCacheInvalidator domain.ConfigCacheInvalidator
	redis                  *redis.Client
	clock                  clockwork.Clock
}

// NewService creates the application layer service.
// twitch may be nil if webhooks are not configured.
func NewService(users domain.UserRepository, configs domain.ConfigRepository, store domain.SessionRepository, engine domain.Engine, twitch domain.TwitchService, configCacheInvalidator domain.ConfigCacheInvalidator, clock clockwork.Clock, rdb *redis.Client) *Service {
	return &Service{
		users:                  users,
		configs:                configs,
		store:                  store,
		engine:                 engine,
		twitch:                 twitch,
		configCacheInvalidator: configCacheInvalidator,
		redis:                  rdb,
		clock:                  clock,
	}
}

// GetUserByID retrieves a user by internal ID.
func (s *Service) GetUserByID(ctx context.Context, userID uuid.UUID) (*domain.User, error) {
	user, err := s.users.GetByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get user by ID: %w", err)
	}
	return user, nil
}

// GetUserByOverlayUUID retrieves a user by overlay UUID.
func (s *Service) GetUserByOverlayUUID(ctx context.Context, overlayUUID uuid.UUID) (*domain.User, error) {
	user, err := s.users.GetByOverlayUUID(ctx, overlayUUID)
	if err != nil {
		return nil, fmt.Errorf("failed to get user by overlay UUID: %w", err)
	}
	return user, nil
}

// GetConfig retrieves a user's config.
func (s *Service) GetConfig(ctx context.Context, userID uuid.UUID) (*domain.Config, error) {
	config, err := s.configs.GetByUserID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get config by user ID: %w", err)
	}
	return config, nil
}

// UpsertUser creates or updates a user.
func (s *Service) UpsertUser(ctx context.Context, twitchUserID, twitchUsername, accessToken, refreshToken string, tokenExpiry time.Time) (*domain.User, error) {
	user, err := s.users.Upsert(ctx, twitchUserID, twitchUsername, accessToken, refreshToken, tokenExpiry)
	if err != nil {
		return nil, fmt.Errorf("failed to upsert user: %w", err)
	}
	return user, nil
}

// ResetSentiment resets the sentiment value for a session to zero.
func (s *Service) ResetSentiment(ctx context.Context, overlayUUID uuid.UUID) error {
	broadcasterID, err := s.store.GetBroadcasterID(ctx, overlayUUID)
	if err != nil {
		return fmt.Errorf("failed to get broadcaster ID: %w", err)
	}
	if err := s.engine.ResetSentiment(ctx, broadcasterID); err != nil {
		return fmt.Errorf("failed to reset sentiment: %w", err)
	}
	return nil
}

// SaveConfig saves the config to PostgreSQL and invalidates all cache layers.
func (s *Service) SaveConfig(ctx context.Context, req domain.SaveConfigRequest) error {
	current, err := s.configs.GetByUserID(ctx, req.UserID)
	if err != nil {
		return fmt.Errorf("failed to get current config: %w", err)
	}

	if err := s.configs.Update(ctx, req.UserID, req.ForTrigger, req.AgainstTrigger, req.LeftLabel, req.RightLabel, req.DecaySpeed, current.Version+1); err != nil {
		return fmt.Errorf("failed to update config: %w", err)
	}

	// Invalidate Redis config cache (best-effort)
	if err := s.configCacheInvalidator.InvalidateCache(ctx, req.BroadcasterID); err != nil {
		slog.Warn("Failed to invalidate Redis config cache",
			"broadcaster_id", req.BroadcasterID,
			"error", err)
		// Non-fatal: cache will expire via TTL
	}

	// Broadcast invalidation to all instances via pub/sub
	// (each instance evicts its local in-memory cache + Redis cache)
	if err := coordination.PublishConfigInvalidation(ctx, s.redis, req.BroadcasterID); err != nil {
		slog.Warn("Failed to publish config invalidation",
			"broadcaster_id", req.BroadcasterID,
			"error", err)
		// Non-fatal: other instances will get update via TTL expiry
	}

	return nil
}

// RotateOverlayUUID generates a new overlay UUID for a user.
func (s *Service) RotateOverlayUUID(ctx context.Context, userID uuid.UUID) (uuid.UUID, error) {
	newUUID, err := s.users.RotateOverlayUUID(ctx, userID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to rotate overlay UUID: %w", err)
	}
	return newUUID, nil
}

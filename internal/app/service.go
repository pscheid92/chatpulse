package app

import (
	"context"
	"fmt"

	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/uuid"
)

// SaveConfigRequest bundles all parameters for a config save operation.
type SaveConfigRequest struct {
	StreamerID    uuid.UUID
	BroadcasterID string

	ForTrigger     string
	ForLabel       string
	AgainstTrigger string
	AgainstLabel   string

	MemorySeconds int

	DisplayMode string
}

type Service struct {
	users     domain.StreamerRepository
	configs   domain.ConfigRepository
	overlay   domain.Overlay
	twitch    domain.EventSubService
	publisher domain.EventPublisher
}

func NewService(users domain.StreamerRepository, configs domain.ConfigRepository, overlay domain.Overlay, twitch domain.EventSubService, publisher domain.EventPublisher) *Service {
	return &Service{
		users:     users,
		configs:   configs,
		overlay:   overlay,
		twitch:    twitch,
		publisher: publisher,
	}
}

// GetStreamerByID retrieves a streamer by internal ID.
func (s *Service) GetStreamerByID(ctx context.Context, streamerID uuid.UUID) (*domain.Streamer, error) {
	streamer, err := s.users.GetByID(ctx, streamerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get streamer by ID: %w", err)
	}
	return streamer, nil
}

func (s *Service) GetStreamerByOverlayUUID(ctx context.Context, overlayUUID uuid.UUID) (*domain.Streamer, error) {
	streamer, err := s.users.GetByOverlayUUID(ctx, overlayUUID)
	if err != nil {
		return nil, fmt.Errorf("failed to get streamer by overlay UUID: %w", err)
	}
	return streamer, nil
}

func (s *Service) GetConfig(ctx context.Context, streamerID uuid.UUID) (*domain.OverlayConfigWithVersion, error) {
	config, err := s.configs.GetByStreamerID(ctx, streamerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get config by streamer ID: %w", err)
	}
	return config, nil
}

func (s *Service) UpsertStreamer(ctx context.Context, twitchUserID, twitchUsername string) (*domain.Streamer, error) {
	streamer, err := s.users.Upsert(ctx, twitchUserID, twitchUsername)
	if err != nil {
		return nil, fmt.Errorf("failed to upsert streamer: %w", err)
	}
	return streamer, nil
}

func (s *Service) ResetSentiment(ctx context.Context, overlayUUID uuid.UUID) error {
	streamer, err := s.users.GetByOverlayUUID(ctx, overlayUUID)
	if err != nil {
		return fmt.Errorf("failed to get streamer by overlay UUID: %w", err)
	}
	if err := s.overlay.Reset(ctx, streamer.TwitchUserID); err != nil {
		return fmt.Errorf("failed to reset sentiment: %w", err)
	}
	return nil
}

func (s *Service) SaveConfig(ctx context.Context, req SaveConfigRequest) error {
	current, err := s.configs.GetByStreamerID(ctx, req.StreamerID)
	if err != nil {
		return fmt.Errorf("failed to get current config: %w", err)
	}

	overlayConfig := domain.OverlayConfig{
		ForTrigger:     req.ForTrigger,
		ForLabel:       req.ForLabel,
		AgainstTrigger: req.AgainstTrigger,
		AgainstLabel:   req.AgainstLabel,
		MemorySeconds:  req.MemorySeconds,
		DisplayMode:    domain.ParseDisplayMode(req.DisplayMode),
	}

	if err := s.configs.Update(ctx, req.StreamerID, overlayConfig, current.Version+1); err != nil {
		return fmt.Errorf("failed to update config: %w", err)
	}

	// Non-fatal: publisher handles errors internally (cache will expire via TTL)
	_ = s.publisher.PublishConfigChanged(ctx, req.BroadcasterID)

	return nil
}

func (s *Service) RotateOverlayUUID(ctx context.Context, streamerID uuid.UUID) (uuid.UUID, error) {
	newUUID, err := s.users.RotateOverlayUUID(ctx, streamerID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to rotate overlay UUID: %w", err)
	}
	return newUUID, nil
}

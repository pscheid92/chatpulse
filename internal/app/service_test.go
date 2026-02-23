package app

import (
	"context"
	"errors"
	"testing"

	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Mock implementations ---

type mockStreamerRepo struct {
	getByIDFn           func(ctx context.Context, streamerID uuid.UUID) (*domain.Streamer, error)
	getByOverlayUUIDFn  func(ctx context.Context, overlayUUID uuid.UUID) (*domain.Streamer, error)
	upsertFn            func(ctx context.Context, twitchUserID, twitchUsername string) (*domain.Streamer, error)
	rotateOverlayUUIDFn func(ctx context.Context, streamerID uuid.UUID) (uuid.UUID, error)
}

func (m *mockStreamerRepo) GetByID(ctx context.Context, streamerID uuid.UUID) (*domain.Streamer, error) {
	if m.getByIDFn != nil {
		return m.getByIDFn(ctx, streamerID)
	}
	return nil, errors.New("not implemented")
}

func (m *mockStreamerRepo) GetByOverlayUUID(ctx context.Context, overlayUUID uuid.UUID) (*domain.Streamer, error) {
	if m.getByOverlayUUIDFn != nil {
		return m.getByOverlayUUIDFn(ctx, overlayUUID)
	}
	return nil, errors.New("not implemented")
}

func (m *mockStreamerRepo) Upsert(ctx context.Context, twitchUserID, twitchUsername string) (*domain.Streamer, error) {
	if m.upsertFn != nil {
		return m.upsertFn(ctx, twitchUserID, twitchUsername)
	}
	return nil, errors.New("not implemented")
}

func (m *mockStreamerRepo) RotateOverlayUUID(ctx context.Context, streamerID uuid.UUID) (uuid.UUID, error) {
	if m.rotateOverlayUUIDFn != nil {
		return m.rotateOverlayUUIDFn(ctx, streamerID)
	}
	return uuid.NewV4(), nil
}

type mockConfigRepo struct {
	getByStreamerIDFn    func(ctx context.Context, streamerID uuid.UUID) (*domain.OverlayConfigWithVersion, error)
	getByBroadcasterIDFn func(ctx context.Context, broadcasterID string) (*domain.OverlayConfigWithVersion, error)
	updateFn             func(ctx context.Context, streamerID uuid.UUID, config domain.OverlayConfig, version int) error
}

func (m *mockConfigRepo) GetByStreamerID(ctx context.Context, streamerID uuid.UUID) (*domain.OverlayConfigWithVersion, error) {
	if m.getByStreamerIDFn != nil {
		return m.getByStreamerIDFn(ctx, streamerID)
	}
	return nil, errors.New("not implemented")
}

func (m *mockConfigRepo) GetByBroadcasterID(ctx context.Context, broadcasterID string) (*domain.OverlayConfigWithVersion, error) {
	if m.getByBroadcasterIDFn != nil {
		return m.getByBroadcasterIDFn(ctx, broadcasterID)
	}
	return nil, domain.ErrConfigNotFound
}

func (m *mockConfigRepo) Update(ctx context.Context, streamerID uuid.UUID, config domain.OverlayConfig, version int) error {
	if m.updateFn != nil {
		return m.updateFn(ctx, streamerID, config, version)
	}
	return nil
}

type mockOverlay struct {
	resetFn func(ctx context.Context, broadcasterID string) error
}

func (m *mockOverlay) ProcessMessage(context.Context, string, string, string) (*domain.WindowSnapshot, domain.VoteResult, domain.VoteTarget, error) {
	return nil, domain.VoteNoMatch, domain.VoteTargetNone, nil
}

func (m *mockOverlay) Reset(ctx context.Context, broadcasterID string) error {
	if m.resetFn != nil {
		return m.resetFn(ctx, broadcasterID)
	}
	return nil
}

type mockEventPublisher struct {
	publishSentimentUpdatedFn func(ctx context.Context, broadcasterID string, snapshot *domain.WindowSnapshot) error
	publishConfigChangedFn    func(ctx context.Context, broadcasterID string) error
}

func (m *mockEventPublisher) PublishSentimentUpdated(ctx context.Context, broadcasterID string, snapshot *domain.WindowSnapshot) error {
	if m.publishSentimentUpdatedFn != nil {
		return m.publishSentimentUpdatedFn(ctx, broadcasterID, snapshot)
	}
	return nil
}

func (m *mockEventPublisher) PublishConfigChanged(ctx context.Context, broadcasterID string) error {
	if m.publishConfigChangedFn != nil {
		return m.publishConfigChangedFn(ctx, broadcasterID)
	}
	return nil
}

// newTestService creates a Service for testing.
func newTestService(users domain.StreamerRepository, configs domain.ConfigRepository, overlay domain.Overlay) *Service {
	return &Service{
		users:     users,
		configs:   configs,
		overlay:   overlay,
		publisher: &mockEventPublisher{},
	}
}

// --- ResetSentiment tests ---

func TestResetSentiment(t *testing.T) {
	overlayUUID := uuid.NewV4()
	var called bool

	users := &mockStreamerRepo{
		getByOverlayUUIDFn: func(_ context.Context, _ uuid.UUID) (*domain.Streamer, error) {
			return &domain.Streamer{TwitchUserID: "broadcaster-1"}, nil
		},
	}
	ovl := &mockOverlay{
		resetFn: func(_ context.Context, broadcasterID string) error {
			assert.Equal(t, "broadcaster-1", broadcasterID)
			called = true
			return nil
		},
	}

	svc := newTestService(users, &mockConfigRepo{}, ovl)

	err := svc.ResetSentiment(context.Background(), overlayUUID)
	require.NoError(t, err)
	assert.True(t, called)
}

func TestResetSentiment_Error(t *testing.T) {
	users := &mockStreamerRepo{
		getByOverlayUUIDFn: func(_ context.Context, _ uuid.UUID) (*domain.Streamer, error) {
			return &domain.Streamer{TwitchUserID: "broadcaster-1"}, nil
		},
	}
	ovl := &mockOverlay{
		resetFn: func(_ context.Context, _ string) error {
			return errors.New("overlay error")
		},
	}

	svc := newTestService(users, &mockConfigRepo{}, ovl)

	err := svc.ResetSentiment(context.Background(), uuid.NewV4())
	assert.Error(t, err)
}

// --- SaveConfig tests ---

func TestSaveConfig_Success(t *testing.T) {
	userID := uuid.NewV4()
	var configUpdated bool

	configs := &mockConfigRepo{
		getByStreamerIDFn: func(_ context.Context, _ uuid.UUID) (*domain.OverlayConfigWithVersion, error) {
			return &domain.OverlayConfigWithVersion{Version: 3}, nil
		},
		updateFn: func(_ context.Context, id uuid.UUID, config domain.OverlayConfig, version int) error {
			configUpdated = true
			assert.Equal(t, userID, id)
			assert.Equal(t, "yes", config.ForTrigger)
			assert.Equal(t, "no", config.AgainstTrigger)
			assert.Equal(t, 30, config.MemorySeconds)
			assert.Equal(t, domain.DisplayModeCombined, config.DisplayMode)
			assert.Equal(t, 4, version)
			return nil
		},
	}

	svc := newTestService(&mockStreamerRepo{}, configs, &mockOverlay{})

	err := svc.SaveConfig(context.Background(), SaveConfigRequest{
		StreamerID:     userID,
		ForTrigger:     "yes",
		AgainstTrigger: "no",
		AgainstLabel:   "Left",
		ForLabel:       "Right",
		MemorySeconds:  30,
		DisplayMode:    "combined",
		BroadcasterID:  "broadcaster-1",
	})
	require.NoError(t, err)
	assert.True(t, configUpdated)
}

func TestSaveConfig_DBError(t *testing.T) {
	configs := &mockConfigRepo{
		getByStreamerIDFn: func(_ context.Context, _ uuid.UUID) (*domain.OverlayConfigWithVersion, error) {
			return &domain.OverlayConfigWithVersion{Version: 1}, nil
		},
		updateFn: func(_ context.Context, _ uuid.UUID, _ domain.OverlayConfig, _ int) error {
			return errors.New("db error")
		},
	}

	svc := newTestService(&mockStreamerRepo{}, configs, &mockOverlay{})

	err := svc.SaveConfig(context.Background(), SaveConfigRequest{
		StreamerID:     uuid.NewV4(),
		ForTrigger:     "yes",
		AgainstTrigger: "no",
		AgainstLabel:   "L",
		ForLabel:       "R",
		MemorySeconds:  30,
		DisplayMode:    "combined",
		BroadcasterID:  "broadcaster-1",
	})
	assert.Error(t, err)
}

// --- RotateOverlayUUID tests ---

func TestRotateOverlayUUID(t *testing.T) {
	userID := uuid.NewV4()
	newUUID := uuid.NewV4()

	users := &mockStreamerRepo{
		rotateOverlayUUIDFn: func(_ context.Context, id uuid.UUID) (uuid.UUID, error) {
			assert.Equal(t, userID, id)
			return newUUID, nil
		},
	}

	svc := newTestService(users, &mockConfigRepo{}, &mockOverlay{})

	got, err := svc.RotateOverlayUUID(context.Background(), userID)
	require.NoError(t, err)
	assert.Equal(t, newUUID, got)
}

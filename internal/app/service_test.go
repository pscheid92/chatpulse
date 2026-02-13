package app

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Mock implementations ---

type mockUserRepo struct {
	getByIDFn                   func(ctx context.Context, userID uuid.UUID) (*domain.User, error)
	getByOverlayUUIDFn          func(ctx context.Context, overlayUUID uuid.UUID) (*domain.User, error)
	upsertFn                    func(ctx context.Context, twitchUserID, twitchUsername, accessToken, refreshToken string, tokenExpiry time.Time) (*domain.User, error)
	updateTokensFn              func(ctx context.Context, userID uuid.UUID, accessToken, refreshToken string, tokenExpiry time.Time) error
	rotateOverlayUUIDFn         func(ctx context.Context, userID uuid.UUID) (uuid.UUID, error)
	listUsersWithLegacyTokensFn func(ctx context.Context, currentVersion string, limit int) ([]*domain.User, error)
}

func (m *mockUserRepo) GetByID(ctx context.Context, userID uuid.UUID) (*domain.User, error) {
	if m.getByIDFn != nil {
		return m.getByIDFn(ctx, userID)
	}
	return nil, fmt.Errorf("not implemented")
}

func (m *mockUserRepo) GetByOverlayUUID(ctx context.Context, overlayUUID uuid.UUID) (*domain.User, error) {
	if m.getByOverlayUUIDFn != nil {
		return m.getByOverlayUUIDFn(ctx, overlayUUID)
	}
	return nil, fmt.Errorf("not implemented")
}

func (m *mockUserRepo) Upsert(ctx context.Context, twitchUserID, twitchUsername, accessToken, refreshToken string, tokenExpiry time.Time) (*domain.User, error) {
	if m.upsertFn != nil {
		return m.upsertFn(ctx, twitchUserID, twitchUsername, accessToken, refreshToken, tokenExpiry)
	}
	return nil, fmt.Errorf("not implemented")
}

func (m *mockUserRepo) UpdateTokens(ctx context.Context, userID uuid.UUID, accessToken, refreshToken string, tokenExpiry time.Time) error {
	if m.updateTokensFn != nil {
		return m.updateTokensFn(ctx, userID, accessToken, refreshToken, tokenExpiry)
	}
	return nil
}

func (m *mockUserRepo) RotateOverlayUUID(ctx context.Context, userID uuid.UUID) (uuid.UUID, error) {
	if m.rotateOverlayUUIDFn != nil {
		return m.rotateOverlayUUIDFn(ctx, userID)
	}
	return uuid.New(), nil
}

func (m *mockUserRepo) ListUsersWithLegacyTokens(ctx context.Context, currentVersion string, limit int) ([]*domain.User, error) {
	if m.listUsersWithLegacyTokensFn != nil {
		return m.listUsersWithLegacyTokensFn(ctx, currentVersion, limit)
	}
	return nil, fmt.Errorf("not implemented")
}

type mockConfigRepo struct {
	getByUserIDFn        func(ctx context.Context, userID uuid.UUID) (*domain.Config, error)
	getByBroadcasterIDFn func(ctx context.Context, broadcasterID string) (*domain.Config, error)
	updateFn             func(ctx context.Context, userID uuid.UUID, forTrigger, againstTrigger, leftLabel, rightLabel string, decaySpeed float64) error
}

func (m *mockConfigRepo) GetByUserID(ctx context.Context, userID uuid.UUID) (*domain.Config, error) {
	if m.getByUserIDFn != nil {
		return m.getByUserIDFn(ctx, userID)
	}
	return nil, fmt.Errorf("not implemented")
}

func (m *mockConfigRepo) GetByBroadcasterID(ctx context.Context, broadcasterID string) (*domain.Config, error) {
	if m.getByBroadcasterIDFn != nil {
		return m.getByBroadcasterIDFn(ctx, broadcasterID)
	}
	return nil, domain.ErrConfigNotFound
}

func (m *mockConfigRepo) Update(ctx context.Context, userID uuid.UUID, forTrigger, againstTrigger, leftLabel, rightLabel string, decaySpeed float64) error {
	if m.updateFn != nil {
		return m.updateFn(ctx, userID, forTrigger, againstTrigger, leftLabel, rightLabel, decaySpeed)
	}
	return nil
}

type mockSessionRepo struct {
	getSessionByBroadFn func(ctx context.Context, broadcasterUserID string) (uuid.UUID, bool, error)
	getSessionConfigFn  func(ctx context.Context, sessionUUID uuid.UUID) (*domain.ConfigSnapshot, error)
}

func (m *mockSessionRepo) GetSessionByBroadcaster(ctx context.Context, broadcasterUserID string) (uuid.UUID, bool, error) {
	if m.getSessionByBroadFn != nil {
		return m.getSessionByBroadFn(ctx, broadcasterUserID)
	}
	return uuid.Nil, false, nil
}

func (m *mockSessionRepo) GetBroadcasterID(_ context.Context, _ uuid.UUID) (string, error) {
	return "broadcaster-1", nil
}

func (m *mockSessionRepo) GetSessionConfig(ctx context.Context, sessionUUID uuid.UUID) (*domain.ConfigSnapshot, error) {
	if m.getSessionConfigFn != nil {
		return m.getSessionConfigFn(ctx, sessionUUID)
	}
	return nil, fmt.Errorf("not implemented")
}

type mockEngine struct {
	resetSentimentFn func(ctx context.Context, broadcasterID string) error
}

func (m *mockEngine) GetBroadcastData(context.Context, string) (*domain.BroadcastData, error) {
	return nil, nil
}
func (m *mockEngine) ProcessVote(context.Context, string, string, string) (float64, domain.VoteResult, error) {
	return 0, domain.VoteNoMatch, nil
}

func (m *mockEngine) ResetSentiment(ctx context.Context, broadcasterID string) error {
	if m.resetSentimentFn != nil {
		return m.resetSentimentFn(ctx, broadcasterID)
	}
	return nil
}

type mockConfigCacheInvalidator struct {
	invalidateCacheFn func(ctx context.Context, broadcasterID string) error
}

func (m *mockConfigCacheInvalidator) InvalidateCache(ctx context.Context, broadcasterID string) error {
	if m.invalidateCacheFn != nil {
		return m.invalidateCacheFn(ctx, broadcasterID)
	}
	return nil
}

// newTestService creates a Service for testing.
func newTestService(users domain.UserRepository, configs domain.ConfigRepository, store *mockSessionRepo, engine domain.Engine, clock clockwork.Clock) *Service {
	// Create a mock Redis client that won't actually connect
	// The Publish call in SaveConfig will fail gracefully (just logs a warning)
	rdb := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})

	return &Service{
		users:                  users,
		configs:                configs,
		store:                  store,
		engine:                 engine,
		configCacheInvalidator: &mockConfigCacheInvalidator{},
		redis:                  rdb,
		clock:                  clock,
	}
}

// --- ResetSentiment tests ---

func TestResetSentiment(t *testing.T) {
	overlayUUID := uuid.New()
	var called bool

	engine := &mockEngine{
		resetSentimentFn: func(_ context.Context, _ string) error {
			called = true
			return nil
		},
	}

	clock := clockwork.NewFakeClock()
	svc := newTestService(&mockUserRepo{}, &mockConfigRepo{}, &mockSessionRepo{}, engine, clock)

	err := svc.ResetSentiment(context.Background(), overlayUUID)
	require.NoError(t, err)
	assert.True(t, called)
}

func TestResetSentiment_Error(t *testing.T) {
	engine := &mockEngine{
		resetSentimentFn: func(_ context.Context, _ string) error {
			return fmt.Errorf("engine error")
		},
	}

	clock := clockwork.NewFakeClock()
	svc := newTestService(&mockUserRepo{}, &mockConfigRepo{}, &mockSessionRepo{}, engine, clock)

	err := svc.ResetSentiment(context.Background(), uuid.New())
	assert.Error(t, err)
}

// --- SaveConfig tests ---

func TestSaveConfig_Success(t *testing.T) {
	userID := uuid.New()
	var configUpdated bool

	configs := &mockConfigRepo{
		updateFn: func(_ context.Context, id uuid.UUID, forT, againstT, leftL, rightL string, decay float64) error {
			configUpdated = true
			assert.Equal(t, userID, id)
			assert.Equal(t, "yes", forT)
			assert.Equal(t, "no", againstT)
			assert.Equal(t, 1.5, decay)
			return nil
		},
	}

	clock := clockwork.NewFakeClock()
	svc := newTestService(&mockUserRepo{}, configs, &mockSessionRepo{}, &mockEngine{}, clock)

	err := svc.SaveConfig(context.Background(), userID, "yes", "no", "Left", "Right", 1.5, "broadcaster-1")
	require.NoError(t, err)
	assert.True(t, configUpdated)
}

func TestSaveConfig_DBError(t *testing.T) {
	configs := &mockConfigRepo{
		updateFn: func(_ context.Context, _ uuid.UUID, _, _, _, _ string, _ float64) error {
			return fmt.Errorf("db error")
		},
	}

	clock := clockwork.NewFakeClock()
	svc := newTestService(&mockUserRepo{}, configs, &mockSessionRepo{}, &mockEngine{}, clock)

	err := svc.SaveConfig(context.Background(), uuid.New(), "yes", "no", "L", "R", 1.0, "broadcaster-1")
	assert.Error(t, err)
}

// --- RotateOverlayUUID tests ---

func TestRotateOverlayUUID(t *testing.T) {
	userID := uuid.New()
	newUUID := uuid.New()

	users := &mockUserRepo{
		rotateOverlayUUIDFn: func(_ context.Context, id uuid.UUID) (uuid.UUID, error) {
			assert.Equal(t, userID, id)
			return newUUID, nil
		},
	}

	clock := clockwork.NewFakeClock()
	svc := newTestService(users, &mockConfigRepo{}, &mockSessionRepo{}, &mockEngine{}, clock)

	got, err := svc.RotateOverlayUUID(context.Background(), userID)
	require.NoError(t, err)
	assert.Equal(t, newUUID, got)
}

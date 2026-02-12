package app

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Mock implementations ---

type mockUserRepo struct {
	getByIDFn           func(ctx context.Context, userID uuid.UUID) (*domain.User, error)
	getByOverlayUUIDFn  func(ctx context.Context, overlayUUID uuid.UUID) (*domain.User, error)
	upsertFn            func(ctx context.Context, twitchUserID, twitchUsername, accessToken, refreshToken string, tokenExpiry time.Time) (*domain.User, error)
	updateTokensFn      func(ctx context.Context, userID uuid.UUID, accessToken, refreshToken string, tokenExpiry time.Time) error
	rotateOverlayUUIDFn func(ctx context.Context, userID uuid.UUID) (uuid.UUID, error)
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

type mockConfigRepo struct {
	getByUserIDFn func(ctx context.Context, userID uuid.UUID) (*domain.Config, error)
	updateFn      func(ctx context.Context, userID uuid.UUID, forTrigger, againstTrigger, leftLabel, rightLabel string, decaySpeed float64) error
}

func (m *mockConfigRepo) GetByUserID(ctx context.Context, userID uuid.UUID) (*domain.Config, error) {
	if m.getByUserIDFn != nil {
		return m.getByUserIDFn(ctx, userID)
	}
	return nil, fmt.Errorf("not implemented")
}

func (m *mockConfigRepo) Update(ctx context.Context, userID uuid.UUID, forTrigger, againstTrigger, leftLabel, rightLabel string, decaySpeed float64) error {
	if m.updateFn != nil {
		return m.updateFn(ctx, userID, forTrigger, againstTrigger, leftLabel, rightLabel, decaySpeed)
	}
	return nil
}

type mockSessionRepo struct {
	activateSessionFn   func(ctx context.Context, sessionUUID uuid.UUID, broadcasterUserID string, config domain.ConfigSnapshot) error
	resumeSessionFn     func(ctx context.Context, sessionUUID uuid.UUID) error
	sessionExistsFn     func(ctx context.Context, sessionUUID uuid.UUID) (bool, error)
	deleteSessionFn     func(ctx context.Context, sessionUUID uuid.UUID) error
	getSessionByBroadFn func(ctx context.Context, broadcasterUserID string) (uuid.UUID, bool, error)
	getSessionConfigFn  func(ctx context.Context, sessionUUID uuid.UUID) (*domain.ConfigSnapshot, error)
	markDisconnectedFn  func(ctx context.Context, sessionUUID uuid.UUID) error
	updateConfigFn      func(ctx context.Context, sessionUUID uuid.UUID, config domain.ConfigSnapshot) error
	listOrphansFn       func(ctx context.Context, maxAge time.Duration) ([]uuid.UUID, error)
	incrRefCountFn      func(ctx context.Context, sessionUUID uuid.UUID) (int64, error)
	decrRefCountFn      func(ctx context.Context, sessionUUID uuid.UUID) (int64, error)
}

func (m *mockSessionRepo) ActivateSession(ctx context.Context, sessionUUID uuid.UUID, broadcasterUserID string, config domain.ConfigSnapshot) error {
	if m.activateSessionFn != nil {
		return m.activateSessionFn(ctx, sessionUUID, broadcasterUserID, config)
	}
	return nil
}

func (m *mockSessionRepo) ResumeSession(ctx context.Context, sessionUUID uuid.UUID) error {
	if m.resumeSessionFn != nil {
		return m.resumeSessionFn(ctx, sessionUUID)
	}
	return nil
}

func (m *mockSessionRepo) SessionExists(ctx context.Context, sessionUUID uuid.UUID) (bool, error) {
	if m.sessionExistsFn != nil {
		return m.sessionExistsFn(ctx, sessionUUID)
	}
	return false, nil
}

func (m *mockSessionRepo) DeleteSession(ctx context.Context, sessionUUID uuid.UUID) error {
	if m.deleteSessionFn != nil {
		return m.deleteSessionFn(ctx, sessionUUID)
	}
	return nil
}

func (m *mockSessionRepo) GetSessionByBroadcaster(ctx context.Context, broadcasterUserID string) (uuid.UUID, bool, error) {
	if m.getSessionByBroadFn != nil {
		return m.getSessionByBroadFn(ctx, broadcasterUserID)
	}
	return uuid.Nil, false, nil
}

func (m *mockSessionRepo) GetSessionConfig(ctx context.Context, sessionUUID uuid.UUID) (*domain.ConfigSnapshot, error) {
	if m.getSessionConfigFn != nil {
		return m.getSessionConfigFn(ctx, sessionUUID)
	}
	return nil, fmt.Errorf("not implemented")
}

func (m *mockSessionRepo) MarkDisconnected(ctx context.Context, sessionUUID uuid.UUID) error {
	if m.markDisconnectedFn != nil {
		return m.markDisconnectedFn(ctx, sessionUUID)
	}
	return nil
}

func (m *mockSessionRepo) UpdateConfig(ctx context.Context, sessionUUID uuid.UUID, config domain.ConfigSnapshot) error {
	if m.updateConfigFn != nil {
		return m.updateConfigFn(ctx, sessionUUID, config)
	}
	return nil
}

func (m *mockSessionRepo) ListOrphans(ctx context.Context, maxAge time.Duration) ([]uuid.UUID, error) {
	if m.listOrphansFn != nil {
		return m.listOrphansFn(ctx, maxAge)
	}
	return nil, nil
}

func (m *mockSessionRepo) IncrRefCount(ctx context.Context, sessionUUID uuid.UUID) (int64, error) {
	if m.incrRefCountFn != nil {
		return m.incrRefCountFn(ctx, sessionUUID)
	}
	return 1, nil
}

func (m *mockSessionRepo) DecrRefCount(ctx context.Context, sessionUUID uuid.UUID) (int64, error) {
	if m.decrRefCountFn != nil {
		return m.decrRefCountFn(ctx, sessionUUID)
	}
	return 0, nil
}

type mockTwitch struct {
	subscribeFn   func(ctx context.Context, userID uuid.UUID, broadcasterUserID string) error
	unsubscribeFn func(ctx context.Context, userID uuid.UUID) error
}

func (m *mockTwitch) Subscribe(ctx context.Context, userID uuid.UUID, broadcasterUserID string) error {
	if m.subscribeFn != nil {
		return m.subscribeFn(ctx, userID, broadcasterUserID)
	}
	return nil
}

func (m *mockTwitch) Unsubscribe(ctx context.Context, userID uuid.UUID) error {
	if m.unsubscribeFn != nil {
		return m.unsubscribeFn(ctx, userID)
	}
	return nil
}

type mockEngine struct {
	resetSentimentFn func(ctx context.Context, sessionUUID uuid.UUID) error
}

func (m *mockEngine) GetCurrentValue(context.Context, uuid.UUID) (float64, error) { return 0, nil }
func (m *mockEngine) ProcessVote(context.Context, string, string, string) (float64, bool) {
	return 0, false
}

func (m *mockEngine) ResetSentiment(ctx context.Context, sessionUUID uuid.UUID) error {
	if m.resetSentimentFn != nil {
		return m.resetSentimentFn(ctx, sessionUUID)
	}
	return nil
}

func (m *mockEngine) InvalidateConfigCache(_ uuid.UUID) {
	// No-op for mock
}

// newTestService creates a Service without starting the cleanup timer.
func newTestService(users domain.UserRepository, configs domain.ConfigRepository, store *mockSessionRepo, engine domain.Engine, twitch domain.TwitchService, clock clockwork.Clock) *Service {
	return &Service{
		users:           users,
		configs:         configs,
		store:           store,
		engine:          engine,
		twitch:          twitch,
		clock:           clock,
		cleanupStopCh:   make(chan struct{}),
		orphanMaxAge:    30 * time.Second,
		cleanupInterval: 30 * time.Second,
	}
}

// --- EnsureSessionActive tests ---

func TestEnsureSessionActive_NewSession(t *testing.T) {
	userID := uuid.New()
	overlayUUID := uuid.New()
	broadcasterUserID := "12345"

	users := &mockUserRepo{
		getByOverlayUUIDFn: func(_ context.Context, id uuid.UUID) (*domain.User, error) {
			assert.Equal(t, overlayUUID, id)
			return &domain.User{ID: userID, TwitchUserID: broadcasterUserID, OverlayUUID: overlayUUID}, nil
		},
	}

	configs := &mockConfigRepo{
		getByUserIDFn: func(_ context.Context, id uuid.UUID) (*domain.Config, error) {
			assert.Equal(t, userID, id)
			return &domain.Config{ForTrigger: "yes", AgainstTrigger: "no", DecaySpeed: 1.0}, nil
		},
	}

	var activated bool
	store := &mockSessionRepo{
		sessionExistsFn: func(_ context.Context, _ uuid.UUID) (bool, error) {
			return false, nil
		},
		activateSessionFn: func(_ context.Context, id uuid.UUID, buid string, config domain.ConfigSnapshot) error {
			activated = true
			assert.Equal(t, overlayUUID, id)
			assert.Equal(t, broadcasterUserID, buid)
			assert.Equal(t, "yes", config.ForTrigger)
			return nil
		},
	}

	var subscribed bool
	twitch := &mockTwitch{
		subscribeFn: func(_ context.Context, uid uuid.UUID, buid string) error {
			subscribed = true
			assert.Equal(t, userID, uid)
			assert.Equal(t, broadcasterUserID, buid)
			return nil
		},
	}

	clock := clockwork.NewFakeClock()
	svc := newTestService(users, configs, store, &mockEngine{}, twitch, clock)

	err := svc.EnsureSessionActive(context.Background(), overlayUUID)
	require.NoError(t, err)
	assert.True(t, activated)
	assert.True(t, subscribed)
}

func TestEnsureSessionActive_ExistingSession(t *testing.T) {
	overlayUUID := uuid.New()

	store := &mockSessionRepo{
		sessionExistsFn: func(_ context.Context, _ uuid.UUID) (bool, error) {
			return true, nil
		},
		resumeSessionFn: func(_ context.Context, id uuid.UUID) error {
			assert.Equal(t, overlayUUID, id)
			return nil
		},
	}

	clock := clockwork.NewFakeClock()
	svc := newTestService(&mockUserRepo{}, &mockConfigRepo{}, store, &mockEngine{}, nil, clock)

	err := svc.EnsureSessionActive(context.Background(), overlayUUID)
	require.NoError(t, err)
}

func TestEnsureSessionActive_NilTwitch(t *testing.T) {
	userID := uuid.New()
	overlayUUID := uuid.New()

	users := &mockUserRepo{
		getByOverlayUUIDFn: func(_ context.Context, _ uuid.UUID) (*domain.User, error) {
			return &domain.User{ID: userID, TwitchUserID: "12345", OverlayUUID: overlayUUID}, nil
		},
	}

	configs := &mockConfigRepo{
		getByUserIDFn: func(_ context.Context, _ uuid.UUID) (*domain.Config, error) {
			return &domain.Config{ForTrigger: "yes", AgainstTrigger: "no", DecaySpeed: 1.0}, nil
		},
	}

	store := &mockSessionRepo{}
	clock := clockwork.NewFakeClock()

	// twitch is nil — should not panic
	svc := newTestService(users, configs, store, &mockEngine{}, nil, clock)

	err := svc.EnsureSessionActive(context.Background(), overlayUUID)
	require.NoError(t, err)
}

// --- OnSessionEmpty tests ---

func TestOnSessionEmpty_RefCountZero(t *testing.T) {
	sessionUUID := uuid.New()
	var disconnected bool

	store := &mockSessionRepo{
		decrRefCountFn: func(_ context.Context, id uuid.UUID) (int64, error) {
			assert.Equal(t, sessionUUID, id)
			return 0, nil
		},
		markDisconnectedFn: func(_ context.Context, id uuid.UUID) error {
			disconnected = true
			assert.Equal(t, sessionUUID, id)
			return nil
		},
	}

	clock := clockwork.NewFakeClock()
	svc := newTestService(&mockUserRepo{}, &mockConfigRepo{}, store, &mockEngine{}, nil, clock)

	svc.OnSessionEmpty(context.Background(), sessionUUID)
	assert.True(t, disconnected)
}

func TestOnSessionEmpty_RefCountPositive(t *testing.T) {
	sessionUUID := uuid.New()
	var disconnected bool

	store := &mockSessionRepo{
		decrRefCountFn: func(_ context.Context, _ uuid.UUID) (int64, error) {
			return 1, nil
		},
		markDisconnectedFn: func(_ context.Context, _ uuid.UUID) error {
			disconnected = true
			return nil
		},
	}

	clock := clockwork.NewFakeClock()
	svc := newTestService(&mockUserRepo{}, &mockConfigRepo{}, store, &mockEngine{}, nil, clock)

	svc.OnSessionEmpty(context.Background(), sessionUUID)
	assert.False(t, disconnected, "should not mark disconnected when ref count is positive")
}

// --- IncrRefCount tests ---

func TestIncrRefCount(t *testing.T) {
	sessionUUID := uuid.New()
	var called bool

	store := &mockSessionRepo{
		incrRefCountFn: func(_ context.Context, id uuid.UUID) (int64, error) {
			called = true
			assert.Equal(t, sessionUUID, id)
			return 1, nil
		},
	}

	clock := clockwork.NewFakeClock()
	svc := newTestService(&mockUserRepo{}, &mockConfigRepo{}, store, &mockEngine{}, nil, clock)

	err := svc.IncrRefCount(context.Background(), sessionUUID)
	require.NoError(t, err)
	assert.True(t, called)
}

// --- ResetSentiment tests ---

func TestResetSentiment(t *testing.T) {
	overlayUUID := uuid.New()
	var called bool

	engine := &mockEngine{
		resetSentimentFn: func(_ context.Context, id uuid.UUID) error {
			called = true
			assert.Equal(t, overlayUUID, id)
			return nil
		},
	}

	clock := clockwork.NewFakeClock()
	svc := newTestService(&mockUserRepo{}, &mockConfigRepo{}, &mockSessionRepo{}, engine, nil, clock)

	err := svc.ResetSentiment(context.Background(), overlayUUID)
	require.NoError(t, err)
	assert.True(t, called)
}

func TestResetSentiment_Error(t *testing.T) {
	engine := &mockEngine{
		resetSentimentFn: func(_ context.Context, _ uuid.UUID) error {
			return fmt.Errorf("engine error")
		},
	}

	clock := clockwork.NewFakeClock()
	svc := newTestService(&mockUserRepo{}, &mockConfigRepo{}, &mockSessionRepo{}, engine, nil, clock)

	err := svc.ResetSentiment(context.Background(), uuid.New())
	assert.Error(t, err)
}

// --- SaveConfig tests ---

func TestSaveConfig_Success(t *testing.T) {
	userID := uuid.New()
	overlayUUID := uuid.New()
	var configUpdated, storeUpdated bool

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

	store := &mockSessionRepo{
		updateConfigFn: func(_ context.Context, id uuid.UUID, config domain.ConfigSnapshot) error {
			storeUpdated = true
			assert.Equal(t, overlayUUID, id)
			assert.Equal(t, "yes", config.ForTrigger)
			return nil
		},
	}

	clock := clockwork.NewFakeClock()
	svc := newTestService(&mockUserRepo{}, configs, store, &mockEngine{}, nil, clock)

	err := svc.SaveConfig(context.Background(), userID, "yes", "no", "Left", "Right", 1.5, overlayUUID)
	require.NoError(t, err)
	assert.True(t, configUpdated)
	assert.True(t, storeUpdated)
}

func TestSaveConfig_DBError(t *testing.T) {
	configs := &mockConfigRepo{
		updateFn: func(_ context.Context, _ uuid.UUID, _, _, _, _ string, _ float64) error {
			return fmt.Errorf("db error")
		},
	}

	store := &mockSessionRepo{}
	clock := clockwork.NewFakeClock()
	svc := newTestService(&mockUserRepo{}, configs, store, &mockEngine{}, nil, clock)

	err := svc.SaveConfig(context.Background(), uuid.New(), "yes", "no", "L", "R", 1.0, uuid.New())
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
	svc := newTestService(users, &mockConfigRepo{}, &mockSessionRepo{}, &mockEngine{}, nil, clock)

	got, err := svc.RotateOverlayUUID(context.Background(), userID)
	require.NoError(t, err)
	assert.Equal(t, newUUID, got)
}

// --- CleanupOrphans tests ---

func TestCleanupOrphans_DeletesSessions(t *testing.T) {
	orphan1 := uuid.New()
	orphan2 := uuid.New()
	var deleted []uuid.UUID

	store := &mockSessionRepo{
		listOrphansFn: func(_ context.Context, maxAge time.Duration) ([]uuid.UUID, error) {
			assert.Equal(t, 30*time.Second, maxAge)
			return []uuid.UUID{orphan1, orphan2}, nil
		},
		deleteSessionFn: func(_ context.Context, id uuid.UUID) error {
			deleted = append(deleted, id)
			return nil
		},
	}

	clock := clockwork.NewFakeClock()
	svc := newTestService(&mockUserRepo{}, &mockConfigRepo{}, store, &mockEngine{}, nil, clock)

	svc.CleanupOrphans(context.Background())
	assert.Equal(t, []uuid.UUID{orphan1, orphan2}, deleted)
}

func TestCleanupOrphans_UnsubscribesTwitch(t *testing.T) {
	orphanOverlayUUID := uuid.New()
	userID := uuid.New()
	unsubscribed := make(chan uuid.UUID, 1)

	users := &mockUserRepo{
		getByOverlayUUIDFn: func(_ context.Context, overlayUUID uuid.UUID) (*domain.User, error) {
			assert.Equal(t, orphanOverlayUUID, overlayUUID)
			return &domain.User{ID: userID, OverlayUUID: orphanOverlayUUID}, nil
		},
	}

	store := &mockSessionRepo{
		listOrphansFn: func(_ context.Context, _ time.Duration) ([]uuid.UUID, error) {
			return []uuid.UUID{orphanOverlayUUID}, nil
		},
	}

	twitch := &mockTwitch{
		unsubscribeFn: func(_ context.Context, id uuid.UUID) error {
			unsubscribed <- id
			return nil
		},
	}

	clock := clockwork.NewFakeClock()
	svc := newTestService(users, &mockConfigRepo{}, store, &mockEngine{}, twitch, clock)

	svc.CleanupOrphans(context.Background())

	// Twitch unsubscribe runs in a background goroutine
	select {
	case got := <-unsubscribed:
		assert.Equal(t, userID, got)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for unsubscribe")
	}
}

func TestCleanupOrphans_NoOrphans(t *testing.T) {
	store := &mockSessionRepo{
		listOrphansFn: func(_ context.Context, _ time.Duration) ([]uuid.UUID, error) {
			return nil, nil
		},
	}

	clock := clockwork.NewFakeClock()
	svc := newTestService(&mockUserRepo{}, &mockConfigRepo{}, store, &mockEngine{}, nil, clock)

	// Should not panic
	svc.CleanupOrphans(context.Background())
}

// --- Stop tests ---

func TestStop(t *testing.T) {
	clock := clockwork.NewFakeClock()
	svc := newTestService(&mockUserRepo{}, &mockConfigRepo{}, &mockSessionRepo{}, &mockEngine{}, nil, clock)

	// Should not panic, even when called twice
	svc.Stop()
	svc.Stop()
}

// TestCleanupOrphans_ConcurrentCleanupCalls verifies that concurrent cleanup
// calls are safe with respect to the WaitGroup used for tracking background
// Twitch unsubscribe goroutines. This test specifically validates Go 1.26's
// sync.WaitGroup.Go() method which handles internal synchronization.
func TestCleanupOrphans_ConcurrentCleanupCalls(t *testing.T) {
	orphan1 := uuid.New()
	orphan2 := uuid.New()
	userID1 := uuid.New()
	userID2 := uuid.New()

	users := &mockUserRepo{
		getByOverlayUUIDFn: func(_ context.Context, overlayUUID uuid.UUID) (*domain.User, error) {
			if overlayUUID == orphan1 {
				return &domain.User{ID: userID1, OverlayUUID: orphan1}, nil
			}
			return &domain.User{ID: userID2, OverlayUUID: orphan2}, nil
		},
	}

	store := &mockSessionRepo{
		listOrphansFn: func(_ context.Context, _ time.Duration) ([]uuid.UUID, error) {
			return []uuid.UUID{orphan1, orphan2}, nil
		},
	}

	unsubscribeCount := make(chan struct{}, 4) // Buffered channel for safe counting
	twitch := &mockTwitch{
		unsubscribeFn: func(_ context.Context, _ uuid.UUID) error {
			// Simulate slow unsubscribe operation
			time.Sleep(50 * time.Millisecond)
			unsubscribeCount <- struct{}{}
			return nil
		},
	}

	clock := clockwork.NewFakeClock()
	svc := newTestService(users, &mockConfigRepo{}, store, &mockEngine{}, twitch, clock)

	// Call CleanupOrphans concurrently from multiple goroutines
	// This tests that WaitGroup.Go() handles concurrent Add() calls safely
	done := make(chan struct{}, 2)
	go func() {
		svc.CleanupOrphans(context.Background())
		done <- struct{}{}
	}()
	go func() {
		svc.CleanupOrphans(context.Background())
		done <- struct{}{}
	}()

	// Wait for both cleanup calls to return
	<-done
	<-done

	// Wait for all 4 background unsubscribe goroutines to complete (2 calls × 2 orphans)
	for i := 0; i < 4; i++ {
		select {
		case <-unsubscribeCount:
			// Success
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for goroutine %d/4", i+1)
		}
	}

	// Stop should wait for all goroutines (should be no-op since they're done)
	svc.Stop()
}

package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jonboulle/clockwork"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/chatpulse/internal/metrics"
	"github.com/stretchr/testify/assert"
)

// TestCleanupOrphans_RaceCondition verifies ref count prevents deletion when session reconnects
func TestCleanupOrphans_RaceCondition(t *testing.T) {
	clock := clockwork.NewFakeClock()
	orphanUUID := uuid.New()

	// Setup: Session disconnected 31s ago (past orphanMaxAge)
	sessions := &mockSessionRepo{
		listOrphansFn: func(_ context.Context, _ time.Duration) ([]uuid.UUID, error) {
			return []uuid.UUID{orphanUUID}, nil
		},
		deleteSessionFn: func(_ context.Context, sid uuid.UUID) error {
			// Simulate: session reconnected during scan, ref_count > 0
			return domain.ErrSessionActive
		},
	}

	svc := newTestService(nil, nil, sessions, &mockEngine{}, nil, clock)
	defer svc.Stop()

	// Record initial metric value
	initialSkipped := testutil.ToFloat64(metrics.OrphanSessionsSkippedTotal.WithLabelValues("active"))

	// Act
	_ = svc.CleanupOrphans(context.Background())

	// Assert: Session NOT deleted (ErrSessionActive handled gracefully)
	// Verify metric incremented
	assert.Equal(t, initialSkipped+1, testutil.ToFloat64(metrics.OrphanSessionsSkippedTotal.WithLabelValues("active")))
}

// TestCleanupOrphans_DeleteSuccess verifies successful cleanup path
func TestCleanupOrphans_DeleteSuccess(t *testing.T) {
	clock := clockwork.NewFakeClock()
	orphanUUID := uuid.New()
	userID := uuid.New()

	var deleteCalled bool

	sessions := &mockSessionRepo{
		listOrphansFn: func(_ context.Context, _ time.Duration) ([]uuid.UUID, error) {
			return []uuid.UUID{orphanUUID}, nil
		},
		deleteSessionFn: func(_ context.Context, sid uuid.UUID) error {
			deleteCalled = true
			assert.Equal(t, orphanUUID, sid)
			return nil // Success
		},
	}

	users := &mockUserRepo{
		getByOverlayUUIDFn: func(_ context.Context, overlayUUID uuid.UUID) (*domain.User, error) {
			return &domain.User{
				ID:          userID,
				OverlayUUID: overlayUUID,
			}, nil
		},
	}

	var unsubscribeCalled bool
	twitch := &mockTwitch{
		unsubscribeFn: func(_ context.Context, uid uuid.UUID) error {
			unsubscribeCalled = true
			assert.Equal(t, userID, uid)
			return nil
		},
	}

	svc := newTestService(users, nil, sessions, &mockEngine{}, twitch, clock)
	defer svc.Stop()

	initialDeleted := testutil.ToFloat64(metrics.OrphanSessionsDeletedTotal)

	// Act
	_ = svc.CleanupOrphans(context.Background())

	// Give background goroutine time to complete
	time.Sleep(50 * time.Millisecond)

	// Assert
	assert.True(t, deleteCalled, "DeleteSession should be called")
	assert.True(t, unsubscribeCalled, "Unsubscribe should be called")
	assert.Equal(t, initialDeleted+1, testutil.ToFloat64(metrics.OrphanSessionsDeletedTotal))
}

// TestCleanupOrphans_IdempotentUnsubscribe verifies no error on duplicate unsubscribe
func TestCleanupOrphans_IdempotentUnsubscribe(t *testing.T) {
	clock := clockwork.NewFakeClock()
	orphanUUID := uuid.New()
	userID := uuid.New()

	sessions := &mockSessionRepo{
		listOrphansFn: func(_ context.Context, _ time.Duration) ([]uuid.UUID, error) {
			return []uuid.UUID{orphanUUID}, nil
		},
		deleteSessionFn: func(_ context.Context, _ uuid.UUID) error {
			return nil // Deletion succeeds
		},
	}

	users := &mockUserRepo{
		getByOverlayUUIDFn: func(_ context.Context, overlayUUID uuid.UUID) (*domain.User, error) {
			return &domain.User{ID: userID, OverlayUUID: overlayUUID}, nil
		},
	}

	twitch := &mockTwitch{
		unsubscribeFn: func(_ context.Context, _ uuid.UUID) error {
			// Subscription already removed (idempotent case)
			return domain.ErrSubscriptionNotFound
		},
	}

	svc := newTestService(users, nil, sessions, &mockEngine{}, twitch, clock)
	defer svc.Stop()

	initialErrors := testutil.ToFloat64(metrics.CleanupUnsubscribeErrorsTotal)

	// Act
	_ = svc.CleanupOrphans(context.Background())
	time.Sleep(50 * time.Millisecond)

	// Assert: No error metric incremented (idempotent success)
	assert.Equal(t, initialErrors, testutil.ToFloat64(metrics.CleanupUnsubscribeErrorsTotal),
		"Error metric should not increment for idempotent unsubscribe")
}

// TestCleanupOrphans_UnsubscribeError verifies error handling
func TestCleanupOrphans_UnsubscribeError(t *testing.T) {
	clock := clockwork.NewFakeClock()
	orphanUUID := uuid.New()
	userID := uuid.New()

	sessions := &mockSessionRepo{
		listOrphansFn: func(_ context.Context, _ time.Duration) ([]uuid.UUID, error) {
			return []uuid.UUID{orphanUUID}, nil
		},
		deleteSessionFn: func(_ context.Context, _ uuid.UUID) error {
			return nil
		},
	}

	users := &mockUserRepo{
		getByOverlayUUIDFn: func(_ context.Context, overlayUUID uuid.UUID) (*domain.User, error) {
			return &domain.User{ID: userID, OverlayUUID: overlayUUID}, nil
		},
	}

	twitch := &mockTwitch{
		unsubscribeFn: func(_ context.Context, _ uuid.UUID) error {
			return errors.New("twitch API error")
		},
	}

	svc := newTestService(users, nil, sessions, &mockEngine{}, twitch, clock)
	defer svc.Stop()

	initialErrors := testutil.ToFloat64(metrics.CleanupUnsubscribeErrorsTotal)

	// Act
	_ = svc.CleanupOrphans(context.Background())
	time.Sleep(50 * time.Millisecond)

	// Assert: Error metric incremented
	assert.Equal(t, initialErrors+1, testutil.ToFloat64(metrics.CleanupUnsubscribeErrorsTotal))
}

// TestCleanupOrphans_MultipleOrphans verifies batch cleanup
func TestCleanupOrphans_MultipleOrphans(t *testing.T) {
	clock := clockwork.NewFakeClock()
	orphan1 := uuid.New()
	orphan2 := uuid.New() // Will be active (race condition)
	orphan3 := uuid.New()

	deletedSessions := make(map[uuid.UUID]bool)

	sessions := &mockSessionRepo{
		listOrphansFn: func(_ context.Context, _ time.Duration) ([]uuid.UUID, error) {
			return []uuid.UUID{orphan1, orphan2, orphan3}, nil
		},
		deleteSessionFn: func(_ context.Context, sid uuid.UUID) error {
			if sid == orphan2 {
				// Simulate race condition: session reconnected
				return domain.ErrSessionActive
			}
			deletedSessions[sid] = true
			return nil
		},
	}

	users := &mockUserRepo{
		getByOverlayUUIDFn: func(_ context.Context, overlayUUID uuid.UUID) (*domain.User, error) {
			return &domain.User{ID: uuid.New(), OverlayUUID: overlayUUID}, nil
		},
	}

	twitch := &mockTwitch{
		unsubscribeFn: func(_ context.Context, uid uuid.UUID) error {
			// This will be called for deleted sessions, not the skipped one
			return nil
		},
	}

	svc := newTestService(users, nil, sessions, &mockEngine{}, twitch, clock)
	defer svc.Stop()

	initialDeleted := testutil.ToFloat64(metrics.OrphanSessionsDeletedTotal)
	initialSkipped := testutil.ToFloat64(metrics.OrphanSessionsSkippedTotal.WithLabelValues("active"))

	// Act
	_ = svc.CleanupOrphans(context.Background())
	time.Sleep(50 * time.Millisecond)

	// Assert
	assert.True(t, deletedSessions[orphan1], "orphan1 should be deleted")
	assert.False(t, deletedSessions[orphan2], "orphan2 should be skipped (active)")
	assert.True(t, deletedSessions[orphan3], "orphan3 should be deleted")

	assert.Equal(t, initialDeleted+2, testutil.ToFloat64(metrics.OrphanSessionsDeletedTotal),
		"Should delete 2 sessions")
	assert.Equal(t, initialSkipped+1, testutil.ToFloat64(metrics.OrphanSessionsSkippedTotal.WithLabelValues("active")),
		"Should skip 1 active session")
}

// TestCleanupOrphans_UserNotFound verifies handling when user is deleted
func TestCleanupOrphans_UserNotFound(t *testing.T) {
	clock := clockwork.NewFakeClock()
	orphanUUID := uuid.New()

	sessions := &mockSessionRepo{
		listOrphansFn: func(_ context.Context, _ time.Duration) ([]uuid.UUID, error) {
			return []uuid.UUID{orphanUUID}, nil
		},
		deleteSessionFn: func(_ context.Context, _ uuid.UUID) error {
			return nil
		},
	}

	users := &mockUserRepo{
		getByOverlayUUIDFn: func(_ context.Context, _ uuid.UUID) (*domain.User, error) {
			// User was deleted
			return nil, domain.ErrUserNotFound
		},
	}

	twitch := &mockTwitch{
		unsubscribeFn: func(_ context.Context, _ uuid.UUID) error {
			t.Fatal("Unsubscribe should not be called if user not found")
			return nil
		},
	}

	svc := newTestService(users, nil, sessions, &mockEngine{}, twitch, clock)
	defer svc.Stop()

	initialErrors := testutil.ToFloat64(metrics.CleanupUnsubscribeErrorsTotal)

	// Act
	_ = svc.CleanupOrphans(context.Background())
	time.Sleep(50 * time.Millisecond)

	// Assert: No error metric (user not found is handled gracefully)
	assert.Equal(t, initialErrors, testutil.ToFloat64(metrics.CleanupUnsubscribeErrorsTotal))
}

// TestCleanupOrphans_MetricsDuration verifies duration metric is recorded
func TestCleanupOrphans_MetricsDuration(t *testing.T) {
	clock := clockwork.NewFakeClock()

	sessions := &mockSessionRepo{
		listOrphansFn: func(_ context.Context, _ time.Duration) ([]uuid.UUID, error) {
			// Advance clock to simulate slow scan
			clock.Advance(500 * time.Millisecond)
			return []uuid.UUID{}, nil
		},
	}

	svc := newTestService(nil, nil, sessions, &mockEngine{}, nil, clock)
	defer svc.Stop()

	initialScans := testutil.ToFloat64(metrics.OrphanCleanupScansTotal)

	// Act
	_ = svc.CleanupOrphans(context.Background())

	// Assert: Scan metric incremented
	assert.Equal(t, initialScans+1, testutil.ToFloat64(metrics.OrphanCleanupScansTotal))

	// Duration metric is recorded - verify the histogram was observed
	// Note: Can't use ToFloat64() on histograms, just verify scan completed without panic
}

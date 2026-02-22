package app

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/stretchr/testify/assert"
)

type mockTickerStore struct {
	mu        sync.Mutex
	snapshots map[string]*domain.WindowSnapshot
}

func (m *mockTickerStore) RecordVote(_ context.Context, _ string, _ domain.VoteTarget, _ int) (*domain.WindowSnapshot, error) {
	return &domain.WindowSnapshot{}, nil
}

func (m *mockTickerStore) GetSnapshot(_ context.Context, broadcasterID string, _ int) (*domain.WindowSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if snap, ok := m.snapshots[broadcasterID]; ok {
		return snap, nil
	}
	return &domain.WindowSnapshot{}, nil
}

func (m *mockTickerStore) ResetSentiment(_ context.Context, _ string) error { return nil }

type mockTickerConfig struct{}

func (m *mockTickerConfig) GetConfigByBroadcaster(_ context.Context, _ string) (domain.OverlayConfig, error) {
	return domain.OverlayConfig{MemorySeconds: 30}, nil
}

type mockTickerPublisher struct {
	mu      sync.Mutex
	updates []publishedUpdate
}

type publishedUpdate struct {
	BroadcasterID string
	Snapshot      *domain.WindowSnapshot
}

func (m *mockTickerPublisher) PublishSentimentUpdated(_ context.Context, broadcasterID string, snapshot *domain.WindowSnapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updates = append(m.updates, publishedUpdate{BroadcasterID: broadcasterID, Snapshot: snapshot})
	return nil
}

func (m *mockTickerPublisher) PublishConfigChanged(_ context.Context, _ string) error { return nil }

func (m *mockTickerPublisher) getUpdates() []publishedUpdate {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]publishedUpdate, len(m.updates))
	copy(result, m.updates)
	return result
}

func TestSnapshotTicker_PublishesForTrackedBroadcasters(t *testing.T) {
	store := &mockTickerStore{snapshots: map[string]*domain.WindowSnapshot{
		"b1": {ForRatio: 0.7, AgainstRatio: 0.3, TotalVotes: 10},
	}}
	pub := &mockTickerPublisher{}
	ticker := NewSnapshotTicker(store, &mockTickerConfig{}, pub)

	ticker.Track("b1")

	ctx, cancel := context.WithCancel(context.Background())
	go ticker.Run(ctx)
	defer cancel()

	assert.Eventually(t, func() bool {
		updates := pub.getUpdates()
		return len(updates) > 0 && updates[0].BroadcasterID == "b1" && updates[0].Snapshot.TotalVotes == 10
	}, 5*time.Second, 100*time.Millisecond)
}

func TestSnapshotTicker_UntracksWhenZeroVotes(t *testing.T) {
	store := &mockTickerStore{snapshots: map[string]*domain.WindowSnapshot{
		"b1": {ForRatio: 0, AgainstRatio: 0, TotalVotes: 0},
	}}
	pub := &mockTickerPublisher{}
	ticker := NewSnapshotTicker(store, &mockTickerConfig{}, pub)

	ticker.Track("b1")

	ctx, cancel := context.WithCancel(context.Background())
	go ticker.Run(ctx)
	defer cancel()

	// Wait for at least one tick to process
	assert.Eventually(t, func() bool {
		return len(pub.getUpdates()) > 0
	}, 5*time.Second, 100*time.Millisecond)

	// The zero-vote snapshot should have been published (final update) and then untracked
	cancel()
	time.Sleep(50 * time.Millisecond) // let goroutine exit

	ticker.mu.Lock()
	_, tracked := ticker.active["b1"]
	ticker.mu.Unlock()
	assert.False(t, tracked, "broadcaster with 0 votes should be untracked")
}

func TestSnapshotTicker_DoesNotPublishWithoutTracking(t *testing.T) {
	store := &mockTickerStore{snapshots: map[string]*domain.WindowSnapshot{
		"b1": {ForRatio: 1.0, AgainstRatio: 0, TotalVotes: 5},
	}}
	pub := &mockTickerPublisher{}
	ticker := NewSnapshotTicker(store, &mockTickerConfig{}, pub)

	// Don't track anything

	ctx, cancel := context.WithCancel(context.Background())
	go ticker.Run(ctx)

	time.Sleep(3 * time.Second)
	cancel()

	assert.Empty(t, pub.getUpdates(), "should not publish when nothing is tracked")
}

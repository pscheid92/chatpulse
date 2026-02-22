package app

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/chatpulse/internal/platform/correlation"
)

const defaultTickInterval = 2 * time.Second

// SnapshotTicker periodically recomputes and publishes sentiment snapshots
// for broadcasters with recent vote activity. This ensures old votes visually
// expire from the sliding window even when no new votes are arriving.
type SnapshotTicker struct {
	store     domain.SentimentStore
	configs   domain.ConfigSource
	publisher domain.EventPublisher

	mu     sync.Mutex
	active map[string]struct{}
}

func NewSnapshotTicker(store domain.SentimentStore, configs domain.ConfigSource, publisher domain.EventPublisher) *SnapshotTicker {
	return &SnapshotTicker{
		store:     store,
		configs:   configs,
		publisher: publisher,
		active:    make(map[string]struct{}),
	}
}

// Track registers a broadcaster for periodic snapshot refresh.
func (t *SnapshotTicker) Track(broadcasterID string) {
	t.mu.Lock()
	t.active[broadcasterID] = struct{}{}
	t.mu.Unlock()
}

// Run starts the periodic refresh loop. It blocks until ctx is cancelled.
func (t *SnapshotTicker) Run(ctx context.Context) {
	ticker := time.NewTicker(defaultTickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.refresh(ctx)
		}
	}
}

func (t *SnapshotTicker) refresh(ctx context.Context) {
	t.mu.Lock()
	ids := make([]string, 0, len(t.active))
	for id := range t.active {
		ids = append(ids, id)
	}
	t.mu.Unlock()

	for _, id := range ids {
		tickCtx := correlation.WithID(ctx, correlation.NewID())

		cfg, err := t.configs.GetConfigByBroadcaster(tickCtx, id)
		if err != nil {
			slog.DebugContext(tickCtx, "Ticker: config lookup failed, removing broadcaster", "broadcaster", id, "error", err)
			t.untrack(id)
			continue
		}

		snap, err := t.store.GetSnapshot(tickCtx, id, cfg.MemorySeconds)
		if err != nil {
			slog.WarnContext(tickCtx, "Ticker: snapshot failed", "broadcaster", id, "error", err)
			continue
		}

		if err := t.publisher.PublishSentimentUpdated(tickCtx, id, snap); err != nil {
			slog.WarnContext(tickCtx, "Ticker: publish failed", "broadcaster", id, "error", err)
			continue
		}

		slog.DebugContext(tickCtx, "Ticker: refreshed snapshot", "broadcaster", id, "forRatio", snap.ForRatio, "againstRatio", snap.AgainstRatio, "totalVotes", snap.TotalVotes)

		if snap.TotalVotes == 0 {
			t.untrack(id)
		}
	}
}

func (t *SnapshotTicker) untrack(id string) {
	t.mu.Lock()
	delete(t.active, id)
	t.mu.Unlock()
}

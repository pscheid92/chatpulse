package redis

import (
	"context"
	"sync"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pscheid92/chatpulse/internal/domain"
)

func TestConfigInvalidationSubscriber_HandleInvalidation(t *testing.T) {
	client := goredis.NewClient(&goredis.Options{})
	repo := NewConfigCacheRepo(client, &mockConfigRepository{}, 10*time.Second)
	sub := NewConfigInvalidationSubscriber(client, repo)

	// Pre-populate the in-memory cache
	repo.mem.set("broadcaster-123", domain.OverlayConfig{ForTrigger: "test"})

	sub.handleInvalidation(context.Background(), "broadcaster-123")

	// In-memory cache should be invalidated
	_, hit := repo.mem.get("broadcaster-123")
	assert.False(t, hit, "In-memory cache should be invalidated")
}

func TestConfigInvalidationSubscriber_EmptyPayload(t *testing.T) {
	client := goredis.NewClient(&goredis.Options{})
	repo := NewConfigCacheRepo(client, &mockConfigRepository{}, 10*time.Second)
	sub := NewConfigInvalidationSubscriber(client, repo)

	// Pre-populate the in-memory cache
	repo.mem.set("broadcaster-123", domain.OverlayConfig{ForTrigger: "test"})

	sub.handleInvalidation(context.Background(), "")

	// In-memory cache should NOT be invalidated (empty payload is ignored)
	_, hit := repo.mem.get("broadcaster-123")
	assert.True(t, hit, "In-memory cache should not be affected by empty payload")
}

func TestPublishConfigInvalidation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	ctx := context.Background()
	client := setupTestClient(t)

	err := PublishConfigInvalidation(ctx, client, "broadcaster-123")
	assert.NoError(t, err)
}

func TestConfigInvalidation_MultiInstance(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := setupTestClient(t)

	// Create 3 subscriber instances, each with its own ConfigCacheRepo
	repos := make([]*ConfigCacheRepo, 3)
	subscribers := make([]*ConfigInvalidationSubscriber, 3)
	for i := range 3 {
		repos[i] = NewConfigCacheRepo(client, &mockConfigRepository{}, 10*time.Second)
		// Pre-populate each in-memory cache
		repos[i].mem.set("broadcaster-test-123", domain.OverlayConfig{ForTrigger: "test"})
		subscribers[i] = NewConfigInvalidationSubscriber(client, repos[i])
	}

	// Start all subscribers
	var wg sync.WaitGroup
	for _, sub := range subscribers {
		wg.Add(1)
		go func(sub *ConfigInvalidationSubscriber) {
			defer wg.Done()
			sub.Start(ctx)
		}(sub)
	}

	// Wait for subscriptions to be ready
	time.Sleep(100 * time.Millisecond)

	// Publish invalidation
	broadcasterID := "broadcaster-test-123"
	err := PublishConfigInvalidation(ctx, client, broadcasterID)
	require.NoError(t, err)

	// Wait for pub/sub delivery
	time.Sleep(200 * time.Millisecond)

	// All instances should have their in-memory cache invalidated
	for i, repo := range repos {
		_, hit := repo.mem.get(broadcasterID)
		assert.False(t, hit,
			"Instance %d should have invalidated in-memory cache", i+1)
	}

	cancel()
	wg.Wait()
}

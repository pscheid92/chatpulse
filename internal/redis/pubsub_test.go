package redis

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublishAndSubscribe(t *testing.T) {
	client := setupTestClient(t)
	ps := NewPubSub(client)
	ctx := context.Background()

	sessionUUID := uuid.New()

	// Subscribe first
	sub := ps.SubscribeSession(ctx, sessionUUID)
	defer sub.Close()

	// Give subscription time to establish
	time.Sleep(100 * time.Millisecond)

	// Publish
	err := ps.PublishUpdate(ctx, sessionUUID, 42.5, "active")
	require.NoError(t, err)

	// Receive
	select {
	case update := <-sub.Ch:
		assert.Equal(t, 42.5, update.Value)
		assert.Equal(t, "active", update.Status)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for pubsub message")
	}
}

func TestSubscribe_MultipleMessages(t *testing.T) {
	client := setupTestClient(t)
	ps := NewPubSub(client)
	ctx := context.Background()

	sessionUUID := uuid.New()
	sub := ps.SubscribeSession(ctx, sessionUUID)
	defer sub.Close()

	time.Sleep(100 * time.Millisecond)

	for i := 0; i < 5; i++ {
		err := ps.PublishUpdate(ctx, sessionUUID, float64(i*10), "active")
		require.NoError(t, err)
	}

	received := 0
	timeout := time.After(2 * time.Second)
	for received < 5 {
		select {
		case <-sub.Ch:
			received++
		case <-timeout:
			t.Fatalf("timed out, received %d/5 messages", received)
		}
	}
	assert.Equal(t, 5, received)
}

func TestSubscribe_DifferentSessions(t *testing.T) {
	client := setupTestClient(t)
	ps := NewPubSub(client)
	ctx := context.Background()

	session1 := uuid.New()
	session2 := uuid.New()

	sub1 := ps.SubscribeSession(ctx, session1)
	defer sub1.Close()
	sub2 := ps.SubscribeSession(ctx, session2)
	defer sub2.Close()

	time.Sleep(100 * time.Millisecond)

	// Publish to session1 only
	err := ps.PublishUpdate(ctx, session1, 10.0, "active")
	require.NoError(t, err)

	// sub1 should receive
	select {
	case update := <-sub1.Ch:
		assert.Equal(t, 10.0, update.Value)
	case <-time.After(2 * time.Second):
		t.Fatal("sub1 timed out")
	}

	// sub2 should NOT receive
	select {
	case <-sub2.Ch:
		t.Fatal("sub2 should not have received a message")
	case <-time.After(200 * time.Millisecond):
		// Expected: no message
	}
}

func TestSubscribe_Close(t *testing.T) {
	client := setupTestClient(t)
	ps := NewPubSub(client)
	ctx := context.Background()

	sessionUUID := uuid.New()
	sub := ps.SubscribeSession(ctx, sessionUUID)

	sub.Close()

	// Channel should be closed eventually
	select {
	case _, ok := <-sub.Ch:
		assert.False(t, ok, "channel should be closed")
	case <-time.After(2 * time.Second):
		t.Fatal("channel was not closed after Close()")
	}
}

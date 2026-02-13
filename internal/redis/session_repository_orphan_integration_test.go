package redis

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/domain"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOrphanCleanup_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	ctx := context.Background()
	rdb := setupTestClient(t)
	clock := clockwork.NewRealClock()
	repo := NewSessionRepo(rdb, clock)

	// Create test sessions
	session1 := uuid.New()
	session2 := uuid.New()
	session3 := uuid.New()

	config := domain.ConfigSnapshot{
		ForTrigger:     "yes",
		AgainstTrigger: "no",
		LeftLabel:      "Against",
		RightLabel:     "For",
		DecaySpeed:     1.0,
	}

	// Activate all sessions
	require.NoError(t, repo.ActivateSession(ctx, session1, "broadcaster1", config))
	require.NoError(t, repo.ActivateSession(ctx, session2, "broadcaster2", config))
	require.NoError(t, repo.ActivateSession(ctx, session3, "broadcaster3", config))

	// Verify sorted set is empty (all active)
	count, err := repo.DisconnectedCount(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), count, "no sessions should be in disconnected set")

	// Mark session1 and session2 as disconnected
	require.NoError(t, repo.MarkDisconnected(ctx, session1))
	require.NoError(t, repo.MarkDisconnected(ctx, session2))

	// Verify sorted set has 2 entries
	count, err = repo.DisconnectedCount(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(2), count, "two sessions should be disconnected")

	// List orphans with 0 grace period (should return both)
	orphans, err := repo.ListOrphans(ctx, 0)
	require.NoError(t, err)
	assert.Len(t, orphans, 2)
	assert.Contains(t, orphans, session1)
	assert.Contains(t, orphans, session2)

	// List orphans with 10s grace period (should return none since they just disconnected)
	orphans, err = repo.ListOrphans(ctx, 10*time.Second)
	require.NoError(t, err)
	assert.Len(t, orphans, 0, "sessions just disconnected should not be orphans yet with 10s grace period")

	// Resume session1
	require.NoError(t, repo.ResumeSession(ctx, session1))

	// Verify sorted set now has 1 entry
	count, err = repo.DisconnectedCount(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count, "only session2 should be disconnected")

	// List orphans (should only return session2)
	orphans, err = repo.ListOrphans(ctx, 0)
	require.NoError(t, err)
	assert.Len(t, orphans, 1)
	assert.Equal(t, session2, orphans[0])

	// Delete session2
	require.NoError(t, repo.DeleteSession(ctx, session2))

	// Verify sorted set is now empty
	count, err = repo.DisconnectedCount(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), count, "sorted set should be empty after deletion")

	// Verify session2 is gone
	exists, err := repo.SessionExists(ctx, session2)
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestOrphanCleanup_GracePeriod(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	ctx := context.Background()
	rdb := setupTestClient(t)
	clock := clockwork.NewFakeClock()
	repo := NewSessionRepo(rdb, clock)

	session := uuid.New()
	config := domain.ConfigSnapshot{
		ForTrigger:     "yes",
		AgainstTrigger: "no",
		LeftLabel:      "Against",
		RightLabel:     "For",
		DecaySpeed:     1.0,
	}

	// Activate and disconnect
	require.NoError(t, repo.ActivateSession(ctx, session, "broadcaster1", config))
	require.NoError(t, repo.MarkDisconnected(ctx, session))

	// Verify in sorted set
	count, err := repo.DisconnectedCount(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)

	// List orphans with 30s grace (session just disconnected, shouldn't appear)
	orphans, err := repo.ListOrphans(ctx, 30*time.Second)
	require.NoError(t, err)
	assert.Empty(t, orphans, "session should not be orphan yet (grace period)")

	// Advance clock by 31 seconds
	clock.Advance(31 * time.Second)

	// Now it should appear
	orphans, err = repo.ListOrphans(ctx, 30*time.Second)
	require.NoError(t, err)
	assert.Len(t, orphans, 1)
	assert.Equal(t, session, orphans[0])
}

func TestOrphanCleanup_MultipleTimestamps(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	ctx := context.Background()
	rdb := setupTestClient(t)
	clock := clockwork.NewFakeClock()
	repo := NewSessionRepo(rdb, clock)

	config := domain.ConfigSnapshot{
		ForTrigger:     "yes",
		AgainstTrigger: "no",
		LeftLabel:      "Against",
		RightLabel:     "For",
		DecaySpeed:     1.0,
	}

	// Create and disconnect 3 sessions at different times
	session1 := uuid.New()
	require.NoError(t, repo.ActivateSession(ctx, session1, "b1", config))
	require.NoError(t, repo.MarkDisconnected(ctx, session1))

	clock.Advance(20 * time.Second)

	session2 := uuid.New()
	require.NoError(t, repo.ActivateSession(ctx, session2, "b2", config))
	require.NoError(t, repo.MarkDisconnected(ctx, session2))

	clock.Advance(20 * time.Second)

	session3 := uuid.New()
	require.NoError(t, repo.ActivateSession(ctx, session3, "b3", config))
	require.NoError(t, repo.MarkDisconnected(ctx, session3))

	// Total time: session1 at T+0, session2 at T+20, session3 at T+40
	// Current time: T+40

	// 30s grace: only session1 should be orphan
	orphans, err := repo.ListOrphans(ctx, 30*time.Second)
	require.NoError(t, err)
	assert.Len(t, orphans, 1)
	assert.Equal(t, session1, orphans[0])

	// 10s grace: session1 and session2 should be orphans
	orphans, err = repo.ListOrphans(ctx, 10*time.Second)
	require.NoError(t, err)
	assert.Len(t, orphans, 2)
	assert.Contains(t, orphans, session1)
	assert.Contains(t, orphans, session2)

	// 0s grace: all 3 should be orphans
	orphans, err = repo.ListOrphans(ctx, 0)
	require.NoError(t, err)
	assert.Len(t, orphans, 3)
}

func BenchmarkListOrphans_SortedSet(b *testing.B) {
	if testing.Short() {
		b.Skip("Skipping integration benchmark")
	}

	ctx := context.Background()
	rdb := setupTestClient(&testing.T{})
	clock := clockwork.NewRealClock()
	repo := NewSessionRepo(rdb, clock)

	config := domain.ConfigSnapshot{
		ForTrigger:     "yes",
		AgainstTrigger: "no",
		LeftLabel:      "Against",
		RightLabel:     "For",
		DecaySpeed:     1.0,
	}

	// Create 1000 disconnected sessions
	sessions := make([]uuid.UUID, 1000)
	for i := 0; i < 1000; i++ {
		sid := uuid.New()
		sessions[i] = sid
		if err := repo.ActivateSession(ctx, sid, fmt.Sprintf("broadcaster%d", i), config); err != nil {
			b.Fatal(err)
		}
		if err := repo.MarkDisconnected(ctx, sid); err != nil {
			b.Fatal(err)
		}
	}

	b.ResetTimer()

	// Benchmark sorted set approach
	for i := 0; i < b.N; i++ {
		orphans, err := repo.ListOrphans(ctx, 30*time.Second)
		if err != nil {
			b.Fatal(err)
		}
		if len(orphans) != 1000 {
			b.Fatalf("expected 1000 orphans, got %d", len(orphans))
		}
	}
}

func BenchmarkListOrphans_Scale(b *testing.B) {
	if testing.Short() {
		b.Skip("Skipping integration benchmark")
	}

	sizes := []int{100, 1000, 10000}

	for _, size := range sizes {
		b.Run(fmt.Sprintf("size=%d", size), func(b *testing.B) {
			ctx := context.Background()
			rdb := setupTestClient(&testing.T{})
			clock := clockwork.NewRealClock()
			repo := NewSessionRepo(rdb, clock)

			config := domain.ConfigSnapshot{
				ForTrigger:     "yes",
				AgainstTrigger: "no",
				LeftLabel:      "Against",
				RightLabel:     "For",
				DecaySpeed:     1.0,
			}

			// Create disconnected sessions
			for i := 0; i < size; i++ {
				sid := uuid.New()
				if err := repo.ActivateSession(ctx, sid, fmt.Sprintf("b%d", i), config); err != nil {
					b.Fatal(err)
				}
				if err := repo.MarkDisconnected(ctx, sid); err != nil {
					b.Fatal(err)
				}
			}

			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				orphans, err := repo.ListOrphans(ctx, 30*time.Second)
				if err != nil {
					b.Fatal(err)
				}
				if len(orphans) != size {
					b.Fatalf("expected %d orphans, got %d", size, len(orphans))
				}
			}
		})
	}
}

func TestOrphanCleanup_SortedSetIntegrity(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	ctx := context.Background()
	rdb := setupTestClient(t)
	clock := clockwork.NewRealClock()
	repo := NewSessionRepo(rdb, clock)

	config := domain.ConfigSnapshot{
		ForTrigger:     "yes",
		AgainstTrigger: "no",
		LeftLabel:      "Against",
		RightLabel:     "For",
		DecaySpeed:     1.0,
	}

	session := uuid.New()

	// Lifecycle: activate -> disconnect -> resume -> disconnect -> delete
	require.NoError(t, repo.ActivateSession(ctx, session, "broadcaster1", config))

	// Verify sorted set empty
	count, err := repo.DisconnectedCount(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)

	// Disconnect
	require.NoError(t, repo.MarkDisconnected(ctx, session))
	count, err = repo.DisconnectedCount(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)

	// Resume
	require.NoError(t, repo.ResumeSession(ctx, session))
	count, err = repo.DisconnectedCount(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)

	// Disconnect again
	require.NoError(t, repo.MarkDisconnected(ctx, session))
	count, err = repo.DisconnectedCount(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)

	// Delete
	require.NoError(t, repo.DeleteSession(ctx, session))
	count, err = repo.DisconnectedCount(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)

	// Verify not in sorted set
	members, err := rdb.ZRangeByScore(ctx, disconnectedSessionsKey, &goredis.ZRangeBy{
		Min: "-inf",
		Max: "+inf",
	}).Result()
	require.NoError(t, err)
	assert.NotContains(t, members, session.String())
}

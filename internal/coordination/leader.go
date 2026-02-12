package coordination

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// LeaderElection implements single-leader election using Redis SETNX.
// The leader holds a key with a TTL. Other instances try to acquire
// leadership if the key expires (previous leader crashed or network partition).
type LeaderElection struct {
	redis      *redis.Client
	instanceID string
	ttl        time.Duration
	key        string
}

// NewLeaderElection creates a new leader election instance.
// key is the Redis key used for the election (e.g., "leader:orphan_cleanup").
// ttl is how long the leader holds the lock before it expires.
func NewLeaderElection(redis *redis.Client, instanceID string, key string, ttl time.Duration) *LeaderElection {
	return &LeaderElection{
		redis:      redis,
		instanceID: instanceID,
		ttl:        ttl,
		key:        key,
	}
}

// TryBecomeLeader attempts to acquire leadership.
// Returns true if this instance is now the leader, false otherwise.
func (l *LeaderElection) TryBecomeLeader(ctx context.Context) (bool, error) {
	success, err := l.redis.SetNX(ctx, l.key, l.instanceID, l.ttl).Result()
	return success, err
}

// RenewLease extends the leader's TTL.
// Only succeeds if this instance is still the leader.
// Should be called periodically (e.g., every ttl/2) to maintain leadership.
func (l *LeaderElection) RenewLease(ctx context.Context) error {
	// Lua script ensures atomic check-and-renew
	script := `
	if redis.call("GET", KEYS[1]) == ARGV[1] then
		return redis.call("EXPIRE", KEYS[1], ARGV[2])
	else
		return 0
	end
	`

	result, err := l.redis.Eval(ctx, script, []string{l.key}, l.instanceID, int(l.ttl.Seconds())).Result()
	if err != nil {
		return err
	}

	if result == int64(0) {
		// We're no longer the leader
		return ErrNotLeader
	}

	return nil
}

// IsLeader checks if this instance is currently the leader.
func (l *LeaderElection) IsLeader(ctx context.Context) (bool, error) {
	currentLeader, err := l.redis.Get(ctx, l.key).Result()
	if err == redis.Nil {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	return currentLeader == l.instanceID, nil
}

// ReleaseLease voluntarily gives up leadership.
// Should be called during graceful shutdown.
func (l *LeaderElection) ReleaseLease(ctx context.Context) error {
	// Only delete if we're still the leader
	script := `
	if redis.call("GET", KEYS[1]) == ARGV[1] then
		return redis.call("DEL", KEYS[1])
	else
		return 0
	end
	`

	return l.redis.Eval(ctx, script, []string{l.key}, l.instanceID).Err()
}

// ErrNotLeader is returned by RenewLease when this instance is no longer the leader.
var ErrNotLeader = &notLeaderError{}

type notLeaderError struct{}

func (e *notLeaderError) Error() string {
	return "not leader"
}

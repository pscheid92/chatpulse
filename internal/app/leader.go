package app

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// LeaderElector implements Redis-based leader election using SETNX with TTL.
// Used to ensure only one instance runs orphan cleanup at a time.
type LeaderElector struct {
	rdb        *redis.Client
	instanceID string
	lockKey    string
	lockTTL    time.Duration
}

// NewLeaderElector creates a leader election coordinator.
// instanceID should be unique per instance (e.g., hostname-PID).
func NewLeaderElector(rdb *redis.Client, instanceID string) *LeaderElector {
	return &LeaderElector{
		rdb:        rdb,
		instanceID: instanceID,
		lockKey:    "cleanup:leader",
		lockTTL:    30 * time.Second,
	}
}

// TryAcquire attempts to become the leader.
// Returns true if this instance acquired leadership, false if another instance is leader.
func (l *LeaderElector) TryAcquire(ctx context.Context) (bool, error) {
	// SETNX: set only if key doesn't exist
	ok, err := l.rdb.SetNX(ctx, l.lockKey, l.instanceID, l.lockTTL).Result()
	if err != nil {
		return false, fmt.Errorf("failed to acquire leader lock: %w", err)
	}
	return ok, nil
}

// Renew extends the leader lease (should be called every 15s by the leader).
// Returns error if we're no longer the leader.
func (l *LeaderElector) Renew(ctx context.Context) error {
	// Check current value is our instance ID (don't steal lock)
	currentLeader, err := l.rdb.Get(ctx, l.lockKey).Result()
	if err == redis.Nil {
		return fmt.Errorf("leader lock lost")
	}
	if err != nil {
		return fmt.Errorf("failed to check leader: %w", err)
	}

	if currentLeader != l.instanceID {
		return fmt.Errorf("leader lock stolen by %s", currentLeader)
	}

	// Renew TTL
	ok, err := l.rdb.Expire(ctx, l.lockKey, l.lockTTL).Result()
	if err != nil {
		return fmt.Errorf("failed to renew leader lock: %w", err)
	}
	if !ok {
		return fmt.Errorf("leader lock lost during renewal")
	}

	return nil
}

// Release voluntarily releases leadership.
// Should be called on graceful shutdown.
func (l *LeaderElector) Release(ctx context.Context) error {
	// Delete only if we're still the leader (avoid deleting another instance's lock)
	script := `
		if redis.call("GET", KEYS[1]) == ARGV[1] then
			return redis.call("DEL", KEYS[1])
		else
			return 0
		end
	`

	_, err := l.rdb.Eval(ctx, script, []string{l.lockKey}, l.instanceID).Result()
	return err
}

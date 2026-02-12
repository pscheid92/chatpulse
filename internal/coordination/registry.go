package coordination

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
)

// InstanceRegistry tracks active ChatPulse instances in Redis.
// Each instance sends periodic heartbeats to a shared hash.
// Instances without heartbeat for >60s are considered inactive.
type InstanceRegistry struct {
	redis      *redis.Client
	instanceID string
	heartbeat  time.Duration
	version    string
}

// InstanceInfo holds metadata about an instance.
type InstanceInfo struct {
	InstanceID string `json:"instance_id"`
	Timestamp  int64  `json:"timestamp"`
	Version    string `json:"version"`
}

// NewInstanceRegistry creates a new instance registry.
// instanceID should be unique per instance (e.g., hostname or UUID).
// heartbeat determines how frequently this instance updates its registration.
// version is the Git commit hash or version string.
func NewInstanceRegistry(redis *redis.Client, instanceID string, heartbeat time.Duration, version string) *InstanceRegistry {
	return &InstanceRegistry{
		redis:      redis,
		instanceID: instanceID,
		heartbeat:  heartbeat,
		version:    version,
	}
}

// Start begins the heartbeat loop.
// Registers immediately, then sends heartbeats on the ticker interval.
// Blocks until ctx is cancelled, then unregisters and returns.
func (r *InstanceRegistry) Start(ctx context.Context) {
	// Register immediately
	r.register(ctx)

	ticker := time.NewTicker(r.heartbeat)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			r.register(ctx)
		case <-ctx.Done():
			r.unregister()
			return
		}
	}
}

// register writes this instance's heartbeat to Redis.
// Uses HSET on "instances" hash with instanceID as field.
func (r *InstanceRegistry) register(ctx context.Context) {
	key := "instances"
	value := InstanceInfo{
		InstanceID: r.instanceID,
		Timestamp:  time.Now().Unix(),
		Version:    r.version,
	}

	data, err := json.Marshal(value)
	if err != nil {
		return
	}

	r.redis.HSet(ctx, key, r.instanceID, data)
}

// unregister removes this instance from the registry.
// Called during graceful shutdown.
func (r *InstanceRegistry) unregister() {
	ctx := context.Background()
	key := "instances"
	r.redis.HDel(ctx, key, r.instanceID)
}

// GetActiveInstances returns a list of instance IDs with heartbeats within the last 60 seconds.
func (r *InstanceRegistry) GetActiveInstances(ctx context.Context) ([]string, error) {
	key := "instances"
	instances, err := r.redis.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, err
	}

	active := []string{}
	now := time.Now().Unix()

	for instanceID, data := range instances {
		var info InstanceInfo
		if err := json.Unmarshal([]byte(data), &info); err != nil {
			continue
		}

		// Instance is active if heartbeat within 60 seconds
		if now-info.Timestamp < 60 {
			active = append(active, instanceID)
		}
	}

	return active, nil
}

// GetInstanceInfo returns detailed information about all registered instances.
func (r *InstanceRegistry) GetInstanceInfo(ctx context.Context) ([]InstanceInfo, error) {
	key := "instances"
	instances, err := r.redis.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, err
	}

	infos := []InstanceInfo{}
	now := time.Now().Unix()

	for _, data := range instances {
		var info InstanceInfo
		if err := json.Unmarshal([]byte(data), &info); err != nil {
			continue
		}

		// Only return active instances
		if now-info.Timestamp < 60 {
			infos = append(infos, info)
		}
	}

	return infos, nil
}

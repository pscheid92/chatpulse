package redis

import (
	"context"
	"fmt"
	"strconv"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

// Lua scripts for atomic vote and decay operations.
// These run server-side in Redis, ensuring atomicity even under concurrent access.

var applyVoteScript = goredis.NewScript(`
local current = tonumber(redis.call('HGET', KEYS[1], 'value')) or 0
local new = math.max(-100, math.min(100, current + tonumber(ARGV[1])))
redis.call('HSET', KEYS[1], 'value', tostring(new))
redis.call('PUBLISH', 'sentiment:' .. ARGV[2], cjson.encode({value=new, status='active'}))
return tostring(new)
`)

var applyDecayScript = goredis.NewScript(`
local current = tonumber(redis.call('HGET', KEYS[1], 'value')) or 0
local last_decay = tonumber(redis.call('HGET', KEYS[1], 'last_decay')) or 0
local now = tonumber(ARGV[1])
local min_interval = tonumber(ARGV[4])
if now - last_decay < min_interval then return tostring(current) end
local new = current * tonumber(ARGV[2])
redis.call('HSET', KEYS[1], 'value', tostring(new))
redis.call('HSET', KEYS[1], 'last_decay', tostring(now))
redis.call('PUBLISH', 'sentiment:' .. ARGV[3], cjson.encode({value=new, status='active'}))
return tostring(new)
`)

// ScriptRunner executes Lua scripts on Redis for atomic operations.
type ScriptRunner struct {
	rdb *goredis.Client
}

// NewScriptRunner creates a new ScriptRunner.
func NewScriptRunner(client *Client) *ScriptRunner {
	return &ScriptRunner{rdb: client.rdb}
}

// ApplyVote atomically applies a vote delta, clamps to [-100, 100], and publishes the update.
// Returns the new value.
func (sr *ScriptRunner) ApplyVote(ctx context.Context, overlayUUID uuid.UUID, delta float64) (float64, error) {
	key := sessionKey(overlayUUID)
	result, err := applyVoteScript.Run(ctx, sr.rdb, []string{key},
		strconv.FormatFloat(delta, 'f', -1, 64),
		overlayUUID.String(),
	).Result()
	if err != nil {
		return 0, fmt.Errorf("apply vote script failed: %w", err)
	}
	return strconv.ParseFloat(result.(string), 64)
}

// ApplyDecay atomically applies decay with a timestamp guard to prevent double-decay
// across multiple instances. minIntervalMs is the minimum time between decays in milliseconds.
// Returns the new value.
func (sr *ScriptRunner) ApplyDecay(ctx context.Context, overlayUUID uuid.UUID, decayFactor float64, nowMs int64, minIntervalMs int64) (float64, error) {
	key := sessionKey(overlayUUID)
	result, err := applyDecayScript.Run(ctx, sr.rdb, []string{key},
		strconv.FormatInt(nowMs, 10),
		strconv.FormatFloat(decayFactor, 'f', -1, 64),
		overlayUUID.String(),
		strconv.FormatInt(minIntervalMs, 10),
	).Result()
	if err != nil {
		return 0, fmt.Errorf("apply decay script failed: %w", err)
	}
	return strconv.ParseFloat(result.(string), 64)
}

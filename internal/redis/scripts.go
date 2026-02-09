package redis

import (
	"context"
	"fmt"
	"strconv"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

// Lua scripts for atomic vote and decay-on-read operations.
// Both use time-decayed accumulator math: value * exp(-decay_rate * Δt/1000).

// applyVoteScript atomically reads the current value, applies time-decay based on
// elapsed time since last update, adds the vote delta, clamps to [-100, 100],
// and writes back both the new value and the current timestamp.
// ARGV: [1]=delta, [2]=decay_rate, [3]=now_ms
var applyVoteScript = goredis.NewScript(`
local value = tonumber(redis.call('HGET', KEYS[1], 'value')) or 0
local last_update = tonumber(redis.call('HGET', KEYS[1], 'last_update')) or tonumber(ARGV[3])
local dt = (tonumber(ARGV[3]) - last_update) / 1000.0
local decayed = value * math.exp(-tonumber(ARGV[2]) * dt)
local new_val = math.max(-100, math.min(100, decayed + tonumber(ARGV[1])))
redis.call('HSET', KEYS[1], 'value', tostring(new_val), 'last_update', ARGV[3])
return tostring(new_val)
`)

// getDecayedValueScript reads the current value and computes time-decay
// without writing anything back. Pure read operation.
// ARGV: [1]=decay_rate, [2]=now_ms
var getDecayedValueScript = goredis.NewScript(`
local value = tonumber(redis.call('HGET', KEYS[1], 'value')) or 0
local last_update = tonumber(redis.call('HGET', KEYS[1], 'last_update')) or tonumber(ARGV[2])
local dt = (tonumber(ARGV[2]) - last_update) / 1000.0
return tostring(value * math.exp(-tonumber(ARGV[1]) * dt))
`)

// ScriptRunner executes Lua scripts on Redis for atomic operations.
type ScriptRunner struct {
	rdb *goredis.Client
}

// NewScriptRunner creates a new ScriptRunner.
func NewScriptRunner(client *Client) *ScriptRunner {
	return &ScriptRunner{rdb: client.rdb}
}

// ApplyVote atomically applies time-decay, adds a vote delta, and clamps to [-100, 100].
// Returns the new value.
func (sr *ScriptRunner) ApplyVote(ctx context.Context, overlayUUID uuid.UUID, delta, decayRate float64, nowMs int64) (float64, error) {
	key := sessionKey(overlayUUID)
	result, err := applyVoteScript.Run(ctx, sr.rdb, []string{key},
		strconv.FormatFloat(delta, 'f', -1, 64),
		strconv.FormatFloat(decayRate, 'f', -1, 64),
		strconv.FormatInt(nowMs, 10),
	).Result()
	if err != nil {
		return 0, fmt.Errorf("apply vote script failed: %w", err)
	}
	return strconv.ParseFloat(result.(string), 64)
}

// GetDecayedValue reads the current value with time-decay applied.
// Pure read — no writes to Redis.
func (sr *ScriptRunner) GetDecayedValue(ctx context.Context, overlayUUID uuid.UUID, decayRate float64, nowMs int64) (float64, error) {
	key := sessionKey(overlayUUID)
	result, err := getDecayedValueScript.Run(ctx, sr.rdb, []string{key},
		strconv.FormatFloat(decayRate, 'f', -1, 64),
		strconv.FormatInt(nowMs, 10),
	).Result()
	if err != nil {
		return 0, fmt.Errorf("get decayed value script failed: %w", err)
	}
	return strconv.ParseFloat(result.(string), 64)
}

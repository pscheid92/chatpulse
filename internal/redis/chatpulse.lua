#!lua name=chatpulse

-- apply_vote atomically reads the current value, applies time-decay based on
-- elapsed time since last update, adds the vote delta, clamps to [-100, 100],
-- and writes back both the new value and the current timestamp.
-- KEYS[1] = session hash key
-- ARGS: [1]=delta, [2]=decay_rate, [3]=now_ms
local function apply_vote(keys, args)
    local value = tonumber(redis.call('HGET', keys[1], 'value')) or 0
    local last_update = tonumber(redis.call('HGET', keys[1], 'last_update')) or tonumber(args[3])
    local dt = math.max(0, (tonumber(args[3]) - last_update) / 1000.0)
    local decayed = value * math.exp(-tonumber(args[2]) * dt)
    local new_val = math.max(-100, math.min(100, decayed + tonumber(args[1])))
    redis.call('HSET', keys[1], 'value', tostring(new_val), 'last_update', args[3])
    return tostring(new_val)
end

-- get_decayed_value reads the current value and computes time-decay
-- without writing anything back. Pure read operation.
-- KEYS[1] = session hash key
-- ARGS: [1]=decay_rate, [2]=now_ms
local function get_decayed_value(keys, args)
    local value = tonumber(redis.call('HGET', keys[1], 'value')) or 0
    local last_update = tonumber(redis.call('HGET', keys[1], 'last_update')) or tonumber(args[2])
    local dt = math.max(0, (tonumber(args[2]) - last_update) / 1000.0)
    return tostring(value * math.exp(-tonumber(args[1]) * dt))
end

redis.register_function('apply_vote', apply_vote)
redis.register_function{function_name='get_decayed_value', callback=get_decayed_value, flags={'no-writes'}}

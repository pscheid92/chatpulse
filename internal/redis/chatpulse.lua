#!lua name=chatpulse

local LIBRARY_VERSION = "2"

-- Helper: Validate and clamp numeric value with bounds checking
local function validate_number(value, default, min, max, name)
    local num = tonumber(value)
    if num == nil then
        redis.log(redis.LOG_WARNING, string.format("Invalid %s: %s, using default %s", name, tostring(value), tostring(default)))
        return default
    end
    if num ~= num then
        redis.log(redis.LOG_WARNING, string.format("Invalid %s (NaN), using default %s", name, tostring(default)))
        return default
    end
    if num == math.huge or num == -math.huge then
        redis.log(redis.LOG_WARNING, string.format("Invalid %s (Inf), using default %s", name, tostring(default)))
        return default
    end
    if num < min then return min elseif num > max then return max else return num end
end

-- apply_vote_v2: Atomically apply time-decayed vote with full input validation
-- KEYS[1] = session hash key, ARGS: [1]=delta, [2]=decay_rate, [3]=now_ms
-- Returns: {value_str, version, status}
local function apply_vote_v2(keys, args)
    local delta = validate_number(args[1], 0, -100, 100, "delta")
    local decay_rate = validate_number(args[2], 1.0, 0.1, 10.0, "decay_rate")
    local now_ms = validate_number(args[3], 0, 0, 9999999999999, "now_ms")
    local value = validate_number(redis.call('HGET', keys[1], 'value'), 0, -100, 100, "value")
    local last_update = validate_number(redis.call('HGET', keys[1], 'last_update'), now_ms, 0, 9999999999999, "last_update")
    local dt_ms = math.max(0, now_ms - last_update)
    local dt_seconds = dt_ms / 1000.0
    if dt_seconds > 3600 then value = 0 else value = value * math.exp(-decay_rate * dt_seconds) end
    value = value + delta
    if value < -100 then value = -100 elseif value > 100 then value = 100 end
    redis.call('HSET', keys[1], 'value', tostring(value), 'last_update', tostring(now_ms))
    return {tostring(value), LIBRARY_VERSION, "ok"}
end

-- get_decayed_value_v2: Read time-decayed value with full input validation
-- Returns: {value_str, version, status}
local function get_decayed_value_v2(keys, args)
    local decay_rate = validate_number(args[1], 1.0, 0.1, 10.0, "decay_rate")
    local now_ms = validate_number(args[2], 0, 0, 9999999999999, "now_ms")
    local value_str = redis.call('HGET', keys[1], 'value')
    if not value_str then return {"0", LIBRARY_VERSION, "ok"} end
    local value = validate_number(value_str, 0, -100, 100, "value")
    local last_update = validate_number(redis.call('HGET', keys[1], 'last_update'), now_ms, 0, 9999999999999, "last_update")
    local dt_seconds = math.max(0, now_ms - last_update) / 1000.0
    if dt_seconds > 3600 then value = 0 else value = value * math.exp(-decay_rate * dt_seconds) end
    if value < -100 then value = -100 elseif value > 100 then value = 100 end
    return {tostring(value), LIBRARY_VERSION, "ok"}
end

-- Legacy wrappers for backward compatibility
local function apply_vote(keys, args)
    return apply_vote_v2(keys, args)[1]
end
local function get_decayed_value(keys, args)
    return get_decayed_value_v2(keys, args)[1]
end

redis.register_function('apply_vote', apply_vote)
redis.register_function{function_name='get_decayed_value', callback=get_decayed_value, flags={'no-writes'}}
redis.register_function('apply_vote_v2', apply_vote_v2)
redis.register_function{function_name='get_decayed_value_v2', callback=get_decayed_value_v2, flags={'no-writes'}}

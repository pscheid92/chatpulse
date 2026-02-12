#!lua name=vote_rate_limit

-- check_vote_rate_limit implements a token bucket rate limiter.
-- Allows burst traffic (up to capacity tokens) while limiting sustained rate.
--
-- Token bucket algorithm:
-- - Start with 'capacity' tokens
-- - Each vote consumes 1 token
-- - Tokens refill at 'rate_per_minute' tokens/minute
-- - If tokens < 1, vote is rejected (rate limited)
--
-- KEYS[1] = rate_limit:votes:{session_uuid} (hash with 'tokens' and 'last_update')
-- ARGS: [1]=now_ms, [2]=capacity, [3]=rate_per_minute
-- Returns: 1 if allowed (token consumed), 0 if rejected (rate limited)
local function check_vote_rate_limit(keys, args)
    local key = keys[1]
    local now = tonumber(args[1])
    local capacity = tonumber(args[2])
    local rate_per_minute = tonumber(args[3])

    -- Get current state (tokens remaining, last update timestamp)
    local tokens = redis.call('HGET', key, 'tokens')
    local last_update = redis.call('HGET', key, 'last_update')

    -- Initialize on first call: start with full capacity
    if not tokens then
        tokens = capacity
        last_update = now
    else
        tokens = tonumber(tokens)
        last_update = tonumber(last_update)
    end

    -- Calculate tokens to add based on elapsed time
    -- elapsed_ms / 60000 = elapsed_minutes
    -- elapsed_minutes * rate_per_minute = tokens_to_add
    local elapsed_ms = math.max(0, now - last_update)
    local tokens_to_add = (elapsed_ms / 60000.0) * rate_per_minute
    tokens = math.min(capacity, tokens + tokens_to_add)

    -- Check if we have at least 1 token available
    if tokens < 1 then
        -- Rate limit exceeded: update timestamp but don't consume token
        redis.call('HSET', key, 'tokens', tostring(tokens), 'last_update', tostring(now))
        redis.call('EXPIRE', key, 300)  -- 5-minute TTL (auto-cleanup inactive sessions)
        return 0  -- Rejected
    end

    -- Consume one token
    tokens = tokens - 1
    redis.call('HSET', key, 'tokens', tostring(tokens), 'last_update', tostring(now))
    redis.call('EXPIRE', key, 300)  -- Reset TTL on each vote

    return 1  -- Allowed
end

redis.register_function('check_vote_rate_limit', check_vote_rate_limit)

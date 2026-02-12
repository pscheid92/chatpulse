# Vote Rate Limiting Operations Guide

## Overview

The vote rate limiting system prevents coordinated bot attacks on sentiment manipulation while allowing legitimate burst activity during high-engagement moments. This guide explains how it works, how to monitor it, and how to tune parameters for different use cases.

## How It Works

### Token Bucket Algorithm

The system uses a **token bucket** rate limiter implemented in Redis Lua for multi-instance safety:

1. **Initial State**: Each session starts with a full bucket (100 tokens by default)
2. **Vote Processing**: Each vote consumes 1 token
3. **Refill**: Tokens refill gradually at a sustained rate (100 tokens/minute by default)
4. **Capacity Cap**: Bucket cannot exceed capacity (allows bursts but limits sustained spam)

**Key Properties:**
- Allows **burst traffic** (100 votes immediately)
- Limits **sustained rate** (100 votes/minute long-term)
- Fair refill (no boundary gaming like fixed windows)

### Architecture

**Implementation:**
- **Storage**: Redis hash `rate_limit:votes:{session_uuid}` with fields: `tokens`, `last_update`
- **Computation**: Redis Lua function (`check_vote_rate_limit` in `vote_rate_limit` library)
- **TTL**: 5-minute auto-expire for inactive sessions
- **Multi-instance**: Atomic via Redis Functions (no race conditions across instances)

**Vote Processing Pipeline** (Engine.ProcessVote):
1. Trigger match check
2. Per-user debounce check (1 second)
3. **Per-session rate limit check** ⬅️ New
4. Apply vote to sentiment

### Fail-Open Design

**Philosophy**: Availability over strict enforcement

If the rate limiter encounters a Redis error:
- Vote is **ALLOWED** (fail-open)
- Error logged at ERROR level
- No metric incremented for rejection

**Rationale**: Better to allow a few extra votes during Redis outage than block all legitimate activity.

## Configuration

### Environment Variables

```bash
# .env or environment
VOTE_RATE_LIMIT_CAPACITY=100  # Burst allowance (tokens)
VOTE_RATE_LIMIT_RATE=100      # Sustained rate (tokens per minute)
```

### Default Values

| Parameter | Default | Description |
|-----------|---------|-------------|
| Capacity | 100 | Maximum burst size (votes allowed immediately) |
| Rate | 100 | Sustained rate (votes per minute after burst) |

### Tuning Guidelines

**For Small Streams** (10-100 viewers):
```bash
VOTE_RATE_LIMIT_CAPACITY=50
VOTE_RATE_LIMIT_RATE=50
```
- Less burst tolerance
- Lower sustained rate
- Still allows natural engagement

**For Large Streams** (1000+ viewers):
```bash
VOTE_RATE_LIMIT_CAPACITY=200
VOTE_RATE_LIMIT_RATE=200
```
- Higher burst for hype moments
- Higher sustained rate for high engagement
- Still protects against bot armies

**Calculation Formula**:
```
Capacity = (Peak Viewers × Engagement Rate × Burst Duration)
Rate = (Avg Viewers × Engagement Rate × 60)

Where:
- Engagement Rate: 5-10% for normal chat, 50%+ for hype moments
- Burst Duration: 1-2 seconds
```

**Example** (500 viewers, 10% sustained engagement, 50% burst):
- Capacity: 500 × 0.50 × 1 = 250 tokens
- Rate: 500 × 0.10 × 60 = 3000 tokens/min

## Metrics

### Prometheus Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `vote_rate_limit_checks_total` | Counter | `result` | Rate limit checks by result (`allowed`, `rejected`, `error`) |
| `vote_rate_limit_tokens_remaining` | Histogram | - | Remaining tokens in bucket (sampled on each check) |
| `vote_processing_total{result="rate_limited"}` | Counter | `result` | Total votes rejected due to rate limiting |

### PromQL Queries

**Rate limit rejection rate:**
```promql
# Percentage of votes rejected by rate limiter
rate(vote_processing_total{result="rate_limited"}[5m]) /
  (rate(vote_processing_total{result="applied"}[5m]) +
   rate(vote_processing_total{result="rate_limited"}[5m]))
```

**Votes allowed vs rejected:**
```promql
# Should see rejected votes during bot attacks
rate(vote_rate_limit_checks_total{result="allowed"}[5m])
rate(vote_rate_limit_checks_total{result="rejected"}[5m])
```

**Rate limiter errors:**
```promql
# Should be near zero in healthy system
rate(vote_rate_limit_checks_total{result="error"}[5m])
```

**Token bucket utilization:**
```promql
# Histogram of remaining tokens (low = high load)
histogram_quantile(0.50, rate(vote_rate_limit_tokens_remaining_bucket[5m]))
```

## Monitoring & Alerting

### Recommended Alerts

**High Rejection Rate (Bot Attack):**
```yaml
alert: VoteRateLimitHighRejectionRate
expr: |-
  rate(vote_processing_total{result="rate_limited"}[5m]) /
  (rate(vote_processing_total{result="applied"}[5m]) +
   rate(vote_processing_total{result="rate_limited"}[5m])) > 0.50
for: 2m
severity: warning
summary: "High vote rate limit rejection rate (>50%)"
description: "Possible coordinated bot attack - {{$value}} of votes are being rate limited"
```

**Rate Limiter Errors:**
```yaml
alert: VoteRateLimiterErrors
expr: rate(vote_rate_limit_checks_total{result="error"}[5m]) > 1
for: 5m
severity: warning
summary: "Vote rate limiter encountering errors"
description: "Rate limit checks failing - check Redis health"
```

**Bucket Exhaustion (Sustained High Load):**
```yaml
alert: VoteRateLimitBucketExhaustion
expr: histogram_quantile(0.95, rate(vote_rate_limit_tokens_remaining_bucket[5m])) < 10
for: 10m
severity: info
summary: "Vote rate limit buckets frequently exhausted"
description: "Most sessions have <10 tokens remaining - may need capacity increase"
```

## Operational Scenarios

### Normal Operation

**Metrics:**
- Rejection rate: <1% (few votes rejected)
- Error rate: 0
- Bucket utilization: 50-100 tokens remaining (p50)

**Expected Behavior:**
- Legitimate votes always succeed
- No bot attacks detected

### Hype Moment (Legitimate Burst)

**Scenario**: Streamer makes exciting play, 500 viewers spam "yes" within 2 seconds

**Metrics:**
- Initial burst: 100 votes allowed immediately
- Sustained: 100 votes/minute after burst
- Rejection rate: ~80% (400/500 votes rejected)
- Bucket utilization: 0 tokens remaining

**Behavior:**
- Sentiment bar moves significantly (100 votes is substantial)
- Rate limiter smooths extreme spikes
- No action needed (working as designed)

### Bot Attack (Coordinated Spam)

**Scenario**: 1000 bots each vote once within 1 second

**Metrics:**
- Allowed: 100 votes (burst capacity)
- Rejected: 900 votes (rate limited)
- Rejection rate: 90%
- Alert: VoteRateLimitHighRejectionRate fires

**Behavior:**
- Sentiment bar moves slightly (100 votes)
- Attack mitigated (900 votes blocked)
- Manual investigation: Check chatter_user_ids for patterns

**Action:**
- Review logs for repeated user IDs
- Consider ban if attack persists
- Rate limiter is working correctly (no code changes needed)

### Redis Outage

**Scenario**: Redis becomes unavailable

**Metrics:**
- Error rate: spikes to 100%
- Vote processing: continues (fail-open)
- Alert: VoteRateLimiterErrors fires

**Behavior:**
- All votes are ALLOWED (fail-open)
- Temporary vulnerability to bot attacks
- Sentiment data continues flowing

**Action:**
1. Check Redis health (circuit breaker may trip)
2. Restore Redis service
3. Rate limiter resumes automatically
4. No data loss (rate limit state rebuilds naturally)

## Troubleshooting

### Q: Why are legitimate votes being rejected?

**Check:**
1. Is rejection rate >50% for extended period?
2. Are multiple chatters reporting vote failures?
3. Check bucket capacity/rate for stream size

**Common causes:**
- Capacity too low for stream size
- Rate too low for engagement level
- Sustained hype moment (expected behavior)

**Action:**
- Review metrics: `vote_rate_limit_tokens_remaining`
- Increase `VOTE_RATE_LIMIT_CAPACITY` if burst is too small
- Increase `VOTE_RATE_LIMIT_RATE` if sustained rate is too low

### Q: Bot attack bypassing rate limiter?

**Symptoms:**
- Sentiment bar manipulated despite rate limiting
- 1000+ unique chatter IDs voting

**Root Cause:**
- Rate limiter is **per-session**, not per-user
- 1000 unique users each voting once = 1000 votes
- Rate limiter allows 100 votes, blocks 900

**Reality Check:**
- Rate limiter is working correctly
- Bot attack requires 1000+ unique Twitch accounts
- Per-user debounce (1 second) already in place

**Action:**
- If attack persists: Manual ban via Twitch moderation
- Consider IP-based rate limiting at infrastructure level (not application)

### Q: High error rate from rate limiter?

**Check:**
1. Redis health (circuit breaker state)
2. Redis Function loaded (`vote_rate_limit` library)
3. Redis logs for Lua errors

**Common causes:**
- Redis down or unreachable
- Redis Function not loaded (deployment error)
- Redis version <7.0 (Functions not supported)

**Action:**
1. Check Redis connectivity: `redis-cli PING`
2. Verify library loaded: `redis-cli FUNCTION LIST`
3. Reload if missing: Restart application (auto-loads on startup)

### Q: How to test rate limiter without bot army?

**Manual Test Script:**
```bash
# Exhaust bucket (100 votes)
for i in {1..100}; do
  curl -X POST http://localhost:8080/test/vote \
    -d "session_uuid=test-uuid&trigger=yes"
done

# 101st vote should fail (rate limited)
curl -X POST http://localhost:8080/test/vote \
  -d "session_uuid=test-uuid&trigger=yes"

# Wait 60 seconds, bucket refills (100 votes available again)
sleep 60

# Votes succeed again
curl -X POST http://localhost:8080/test/vote \
  -d "session_uuid=test-uuid&trigger=yes"
```

## Redis Key Schema

### Rate Limit Key

**Key:** `rate_limit:votes:{session_uuid}`

**Type:** Hash

**Fields:**
- `tokens`: Remaining tokens (float, starts at capacity)
- `last_update`: Last check timestamp (milliseconds since epoch)

**TTL:** 300 seconds (5 minutes) - Auto-expires inactive sessions

**Example:**
```redis
redis> HGETALL rate_limit:votes:550e8400-e29b-41d4-a716-446655440000
1) "tokens"
2) "87.5"
3) "last_update"
4) "1707763200000"

redis> TTL rate_limit:votes:550e8400-e29b-41d4-a716-446655440000
(integer) 298
```

## Performance Characteristics

### Latency

- **Lua Function**: <1ms per check (atomic, in-memory)
- **Network RTT**: +0.5-2ms (Redis connection)
- **Total Overhead**: ~1-3ms per vote

### Throughput

- **Redis Capacity**: 100,000+ ops/sec (single-threaded)
- **Rate Limiter**: ~50,000 votes/sec (limited by Lua execution)
- **Bottleneck**: Redis itself, not the algorithm

### Memory Usage

- **Per Session**: ~100 bytes (2 fields + key overhead)
- **10,000 Sessions**: ~1 MB
- **TTL Cleanup**: Auto-expires after 5 minutes of inactivity

## Related Documentation

- **Architecture**: `CLAUDE.md` (Vote Processing Pipeline section)
- **Metrics**: `internal/metrics/metrics.go` (metric definitions)
- **Code**: `internal/redis/vote_rate_limit.lua` (Lua implementation)
- **Tests**: `internal/redis/vote_rate_limit_integration_test.go` (test scenarios)

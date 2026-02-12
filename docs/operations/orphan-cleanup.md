# Orphan Cleanup Operations Guide

## Overview

The orphan cleanup system automatically removes sessions that have been disconnected for more than 30 seconds. This guide explains how it works, how to monitor it, and how to troubleshoot issues.

## How It Works

### Cleanup Cycle

The cleanup timer runs every **30 seconds** and performs the following steps:

1. **Scan for orphans**: Query Redis for sessions with `last_disconnect` timestamp older than 30 seconds
2. **Validate ref count**: For each orphan, check if `ref_count > 0` (session reconnected during scan)
3. **Delete if safe**: Only delete sessions with `ref_count = 0` (truly disconnected)
4. **Background unsubscribe**: For deleted sessions, unsubscribe from Twitch EventSub in a background goroutine

### Race Condition Prevention

**Problem:** A session can reconnect while cleanup is processing, creating a race condition:

```
T=0s:  Client disconnects, last_disconnect=T0
T=31s: Cleanup scanner finds session (31s old)
T=31.5s: Client reconnects (IncrRefCount, ref_count=1)
T=32s: Cleanup attempts to delete session
```

**Solution:** Ref count validation before deletion:

```go
func DeleteSession(ctx, sessionUUID) error {
    refCount := redis.GET(ref_count:{sessionUUID})

    if refCount > 0 {
        // Session reconnected, skip deletion
        return ErrSessionActive
    }

    // Safe to delete (ref_count=0)
    redis.DEL(session:{sessionUUID})
    redis.DEL(ref_count:{sessionUUID})
}
```

### Idempotent Unsubscribe

Background Twitch unsubscribe is idempotent - no error if subscription already removed:

```go
err := twitch.Unsubscribe(ctx, userID)
if errors.Is(err, ErrSubscriptionNotFound) {
    // Already unsubscribed, success (DEBUG log)
    return nil
}
```

## Metrics

### Prometheus Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `orphan_cleanup_scans_total` | Counter | Total number of cleanup scans |
| `orphan_sessions_deleted_total` | Counter | Successfully deleted orphan sessions |
| `orphan_sessions_skipped_total{reason}` | Counter | Skipped sessions by reason (`active`, `error`) |
| `orphan_cleanup_duration_seconds` | Histogram | Cleanup scan duration |
| `cleanup_unsubscribe_errors_total` | Counter | Twitch unsubscribe failures |

### PromQL Queries

**Cleanup success rate:**
```promql
# Should be >99% (most orphans are truly disconnected)
rate(orphan_sessions_deleted_total[5m]) /
  (rate(orphan_sessions_deleted_total[5m]) + rate(orphan_sessions_skipped_total[5m]))
```

**Skip rate by reason:**
```promql
# Active sessions skipped (race condition prevented)
rate(orphan_sessions_skipped_total{reason="active"}[5m])

# Errors during deletion (unexpected)
rate(orphan_sessions_skipped_total{reason="error"}[5m])
```

**Cleanup scan duration (p95):**
```promql
histogram_quantile(0.95, rate(orphan_cleanup_duration_seconds_bucket[5m]))
```

**Unsubscribe error rate:**
```promql
# Should be near zero in healthy system
rate(cleanup_unsubscribe_errors_total[5m])
```

**Sessions cleaned per minute:**
```promql
rate(orphan_sessions_deleted_total[1m]) * 60
```

## Monitoring & Alerting

### Recommended Alerts

**High Skip Rate (Reconnect Storm):**
```yaml
alert: OrphanCleanupHighSkipRate
expr: |
  rate(orphan_sessions_skipped_total{reason="active"}[5m]) /
  rate(orphan_cleanup_scans_total[5m]) > 0.10
for: 5m
severity: warning
summary: "High orphan cleanup skip rate (>10%)"
description: "Many sessions reconnecting during cleanup - possible connection instability"
```

**Cleanup Errors:**
```yaml
alert: OrphanCleanupErrors
expr: rate(orphan_sessions_skipped_total{reason="error"}[5m]) > 0
for: 5m
severity: warning
summary: "Orphan cleanup errors detected"
description: "Cleanup encountering errors during session deletion"
```

**Slow Cleanup Scans:**
```yaml
alert: OrphanCleanupSlow
expr: histogram_quantile(0.95, rate(orphan_cleanup_duration_seconds_bucket[5m])) > 10
for: 10m
severity: warning
summary: "Orphan cleanup scans taking >10s (p95)"
description: "Cleanup scans are slow - Redis may be overloaded or dataset is large"
```

**Unsubscribe Failures:**
```yaml
alert: OrphanCleanupUnsubscribeFailures
expr: rate(cleanup_unsubscribe_errors_total[5m]) > 1
for: 10m
severity: warning
summary: "High Twitch unsubscribe failure rate during cleanup"
description: "Background unsubscribe operations failing - check Twitch API status"
```

## Operational Scenarios

### Normal Operation

**Metrics:**
- Skip rate: <1% (few reconnections during scan)
- Scan duration: <1s (p95)
- Unsubscribe errors: 0

**Logs (DEBUG level):**
```
Cleanup timer started interval=30s
```

### Reconnection Storm (High Skip Rate)

**Symptoms:**
- `orphan_sessions_skipped_total{reason="active"}` spiking
- Skip rate: 10-50%

**Cause:**
- Network instability causing rapid disconnect/reconnect cycles
- Load balancer health check failures
- WebSocket timeouts

**Action:**
- Investigate network or infrastructure issues
- Check broadcaster logs for connection errors
- No cleanup intervention needed (working as designed)

### Twitch API Outage

**Symptoms:**
- `cleanup_unsubscribe_errors_total` increasing
- Logs show "Failed to unsubscribe orphan session" errors

**Cause:**
- Twitch API temporarily unavailable
- Rate limiting from Twitch

**Action:**
- Background unsubscribe is best-effort, retries on next cleanup
- Sessions are still deleted from Redis (cleanup successful)
- EventSub subscriptions will timeout naturally on Twitch side
- Monitor Twitch API status page

### Slow Cleanup Scans

**Symptoms:**
- `orphan_cleanup_duration_seconds` p95 >10s
- Cleanup blocking main event loop

**Cause:**
- Large number of orphaned sessions (>10K)
- Redis slow query (network latency, overload)

**Action:**
- Check Redis metrics (latency, CPU)
- Review `ListOrphans` query performance
- Consider tuning `cleanupScanTimeout` (default: 30s)

## Configuration

### Tunable Parameters

**File:** `internal/app/service.go`

```go
const (
    orphanMaxAge      = 30 * time.Second  // Disconnected >30s considered orphan
    cleanupInterval   = 30 * time.Second  // Cleanup runs every 30s
    cleanupScanTimeout = 30 * time.Second // Max time for ListOrphans query
)
```

**Recommendations:**
- **orphanMaxAge**: 30s is balanced (not too aggressive, not too slow)
  - Lower: More frequent cleanup, higher reconnect race window
  - Higher: Sessions linger longer, fewer race conditions
- **cleanupInterval**: Match orphanMaxAge for consistent behavior
- **cleanupScanTimeout**: Prevent unbounded Redis scans
  - Should be < cleanupInterval to avoid overlap

## Troubleshooting

### Q: Why are sessions not being cleaned up?

**Check:**
1. Is cleanup timer running? (Look for "Cleanup timer started" log)
2. Are orphans detected? (`orphan_cleanup_scans_total` increasing?)
3. Check skip metrics (`orphan_sessions_skipped_total`)

**Common causes:**
- All sessions have `ref_count > 0` (active, not orphans)
- DeleteSession errors (check `reason="error"` skip metric)

### Q: Why do I see "Subscription already removed" logs?

**Answer:** This is normal and expected (idempotent behavior). Possible reasons:
- Session was already cleaned up by another instance
- Manual unsubscribe via admin tool
- Twitch auto-removed expired subscription

**Action:** No action needed (DEBUG level, not an error)

### Q: High skip rate - is cleanup broken?

**Answer:** No, this is working as designed! High skip rate means:
- Many sessions reconnecting during cleanup scan
- Ref count validation preventing unsafe deletions
- This is the race condition prevention in action

**Action:** Investigate why reconnects are frequent (network/infrastructure)

### Q: Cleanup duration increasing over time?

**Possible causes:**
- Session count growing (more orphans to scan)
- Redis performance degradation
- Network latency to Redis

**Investigation:**
1. Check `ListOrphans` query duration in Redis
2. Review total session count in Redis
3. Check Redis SLOWLOG

### Q: User reported orphaned EventSub subscription?

**Check:**
1. Was session deleted? (search logs for session_uuid)
2. Was unsubscribe called? (look for "Cleaned up orphan session" log)
3. Unsubscribe error? (check `cleanup_unsubscribe_errors_total`)

**Manual cleanup:**
```bash
# List subscriptions for user
bd show <user-id>

# Force unsubscribe (if cleanup failed)
twitch-cli api delete /eventsub/subscriptions?id=<subscription-id>
```

## Graceful Shutdown

When the application shuts down:

1. `Service.Stop()` closes `cleanupStopCh`
2. Cleanup timer goroutine exits
3. `cleanupWg.Wait()` blocks until in-flight background unsubscribes complete
4. Database and Redis connections close

**Guarantees:**
- No orphan cleanup during shutdown
- In-flight unsubscribe operations complete before exit
- No leaked goroutines

## Related Documentation

- **Architecture:** `CLAUDE.md` (Application Layer section)
- **Metrics:** `internal/metrics/metrics.go` (metric definitions)
- **Code:** `internal/app/service.go` (CleanupOrphans implementation)
- **Tests:** `internal/app/service_orphan_test.go` (test scenarios)

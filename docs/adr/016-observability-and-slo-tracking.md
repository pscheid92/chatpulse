# ADR-016: Observability and SLO Tracking

**Status:** Accepted
**Date:** 2026-02-12
**Authors:** Infrastructure Team
**Tags:** operations, observability, reliability

## Context

ChatPulse must be observable to detect issues, debug problems, and meet reliability targets. The system handles real-time WebSocket connections and processes votes from Twitch webhooks - latency and availability are critical.

### Requirements

1. **Detect issues quickly** - Alert on degradation within 1 minute
2. **Debug efficiently** - Logs + metrics provide enough context
3. **Track reliability** - Measure against SLO targets
4. **Low overhead** - Monitoring doesn't impact performance

## Decision

Use **Prometheus metrics + structured logging** with defined SLOs. Defer distributed tracing until needed.

### Architecture

**Metrics stack:**
- Prometheus (scrapes `/metrics` endpoint every 15s)
- Grafana (dashboards for visualization)
- Alertmanager (alert routing)

**Logging:**
- Structured logs via `log/slog` (JSON format)
- Log aggregation via Loki or CloudWatch
- Log levels: DEBUG, INFO, WARN, ERROR

**Health checks:**
- `/health` - Liveness probe (process alive)
- `/ready` - Readiness probe (dependencies healthy)
- `/metrics` - Prometheus metrics

### Key Metrics

**System health:**
```go
// Active sessions (gauge)
chatpulse_active_sessions

// WebSocket connections (gauge)
chatpulse_websocket_connections_current{state="active|idle|total|max"}

// Redis operations (counter + histogram)
chatpulse_redis_operations_total{operation="get|set|fcall", status="success|error"}
chatpulse_redis_operation_duration_seconds{operation}

// Database queries (counter + histogram)
chatpulse_db_queries_total{query, status}
chatpulse_db_query_duration_seconds{query}
```

**Business metrics:**
```go
// Votes processed (counter)
chatpulse_votes_total{result="applied|rate_limited|debounced|trigger_not_matched"}

// Broadcasts sent (counter)
chatpulse_broadcasts_total{status="success|error"}

// Orphan cleanup (counter + histogram)
chatpulse_orphan_cleanup_scans_total
chatpulse_orphan_cleanup_duration_seconds
```

**Error tracking:**
```go
// Redis circuit breaker (gauge + counter)
chatpulse_redis_circuit_breaker_state{state="closed|open|half_open"}
chatpulse_redis_circuit_breaker_trips_total

// Twitch webhook errors (counter)
chatpulse_twitch_webhook_errors_total{reason="signature|parse|processing"}

// Connection rejections (counter)
chatpulse_websocket_connections_rejected_total{reason="global_limit|ip_limit|rate_limit"}
```

### Service Level Objectives (SLOs)

**Availability SLO: 99.5% (43.8 minutes downtime/month)**

Measurement:
```promql
# Availability = successful health checks / total health checks
sum(rate(health_check_total{status="success"}[5m]))
/
sum(rate(health_check_total[5m]))
```

**Vote Processing Latency SLO: P95 < 500ms**

Measurement:
```promql
# P95 latency from webhook received to Redis write
histogram_quantile(0.95,
  rate(vote_processing_duration_seconds_bucket[5m])
)
```

**Broadcast Latency SLO: P95 < 100ms**

Measurement:
```promql
# P95 latency from tick to WebSocket send
histogram_quantile(0.95,
  rate(broadcast_duration_seconds_bucket[5m])
)
```

**Error Budget:**
- 99.5% availability = 0.5% error budget = ~22 minutes/month
- Burn rate alerts: 2x burn rate → page, 10x burn rate → critical page

### Alert Examples

**High error rate:**
```yaml
alert: HighErrorRate
expr: |
  rate(http_requests_total{status=~"5.."}[5m])
  /
  rate(http_requests_total[5m])
  > 0.01
for: 5m
annotations:
  summary: "High 5xx error rate (>1%)"
```

**Redis circuit breaker open:**
```yaml
alert: RedisCircuitBreakerOpen
expr: chatpulse_redis_circuit_breaker_state{state="open"} == 1
for: 1m
annotations:
  summary: "Redis circuit breaker is open (Redis degraded)"
```

**SLO burn rate:**
```yaml
alert: AvailabilitySLOBurnRateHigh
expr: |
  # 2x normal burn rate for 1 hour
  (1 - chatpulse_availability) > 0.01
for: 1h
annotations:
  summary: "Availability SLO burning at 2x rate"
```

### Structured Logging

**Format:** JSON for machine parsing

**Log levels:**
- **DEBUG** - Internal state changes (actor commands, Redis calls)
- **INFO** - Normal operations (session activated, vote processed)
- **WARN** - Degraded but functional (rate limit exceeded, cache miss)
- **ERROR** - Failures requiring attention (DB connection lost, webhook invalid)

**Example logs:**
```json
{"level":"INFO","time":"2026-02-12T10:15:30Z","msg":"Vote processed via webhook","user":"user_123","delta":10,"session_uuid":"abc-123"}
{"level":"WARN","time":"2026-02-12T10:15:31Z","msg":"Rate limit exceeded","session_uuid":"abc-123"}
{"level":"ERROR","time":"2026-02-12T10:15:32Z","msg":"Redis operation failed","operation":"fcall","error":"connection refused"}
```

**Context propagation:**
```go
// Add request ID to context
ctx = context.WithValue(ctx, "request_id", uuid.New())

// Log with context
slog.InfoContext(ctx, "Processing vote", "user", userID)
```

## Alternatives Considered

### 1. DataDog - Commercial Observability Platform

All-in-one: metrics, logs, traces, dashboards.

**Rejected because:**
- Cost ($15-50/host/month)
- Vendor lock-in (hard to migrate away)
- Overkill (self-hosted Prometheus sufficient)

### 2. OpenTelemetry Tracing - Distributed Tracing from Day One

Add trace spans for every operation.

**Rejected for initial launch:**
- Overhead (cardinality explosion, storage costs)
- Complexity (span propagation, sampling strategies)
- Not needed yet (single-service architecture, logs sufficient)

**Deferred:** Add if debugging requires it (cross-service calls in future)

### 3. Log Aggregation Only - No Metrics

Rely solely on logs for monitoring.

**Rejected because:**
- Hard to query (no aggregation like "rate over 5m")
- Can't build dashboards easily (text search vs time-series)
- High cardinality (logs more expensive than metrics)

### 4. StatsD + Graphite

Alternative time-series metrics stack.

**Rejected because:**
- Prometheus ecosystem richer (Grafana, Alertmanager)
- StatsD push model (less reliable than Prometheus pull)
- Prometheus query language more powerful (PromQL)

## Consequences

### Positive

✅ **Standard tools** - Prometheus + Grafana ecosystem (battle-tested)
✅ **Low overhead** - Metrics cheap (counters, gauges), logs sampled
✅ **SLO tracking** - Error budgets enable data-driven decisions
✅ **Open source** - No vendor lock-in, full control
✅ **Rich ecosystem** - Exporters, dashboards, integrations

### Negative

❌ **Requires infrastructure** - Prometheus server, Grafana, Alertmanager
❌ **Cardinality management** - Must avoid per-user metrics (explosion)
❌ **No tracing** - Harder to debug distributed issues (mitigated: single service)
❌ **Alert fatigue** - Too many alerts → noise (must tune carefully)

### Trade-offs

🔄 **Metrics over logs for dashboards** - Time-series > text search
🔄 **Pull over push** - Prometheus scrapes > StatsD pushes
🔄 **Defer tracing** - Add complexity only when needed

## Dashboard Examples

**System Overview:**
- Active sessions (gauge)
- WebSocket connections (gauge)
- Request rate (graph, 5m rate)
- Error rate (graph, 5m rate)
- P50/P95/P99 latencies (heatmap)

**Redis Health:**
- Circuit breaker state (indicator)
- Operation rate by type (stacked area)
- Error rate (graph)
- Latency percentiles (graph)

**SLO Tracking:**
- Availability (gauge, target=99.5%)
- Error budget remaining (gauge)
- Burn rate (graph)
- SLO violations (timeline)

## Cardinality Guidelines

**Low cardinality (safe):**
- status (success/error) - 2 values
- operation (get/set/fcall) - ~10 values
- query (GetUserByID/UpdateConfig) - ~20 values

**High cardinality (avoid):**
- user_id (thousands of users)
- session_uuid (thousands of sessions)
- IP address (thousands of IPs)

**Rule:** Labels with >100 unique values → don't use as metric label

## Related Decisions

- **ADR-015: Deployment** - Health checks enable zero-downtime deploys
- **Structured logging** - JSON format for machine parsing
- **Error budget** - SLO tracking enables data-driven reliability decisions

## Implementation

**Files:**
- `internal/metrics/metrics.go` - Metric definitions (Prometheus)
- `internal/server/routes.go` - `/metrics` endpoint
- `internal/logging/logging.go` - Structured logging setup
- `k8s/prometheus-config.yaml` - Prometheus scrape config
- `grafana/dashboards/*.json` - Grafana dashboard definitions

**Commit:** Initial metrics (2025-12), enhanced (2026-02)

## Alerting Strategy

**Alert severity levels:**
- **Critical (page):** Service down, SLO burn rate >10x
- **Warning (notify):** Degraded performance, error rate elevated
- **Info (log only):** Anomalies, non-urgent issues

**Alert routing:**
- Critical → PagerDuty (24/7 on-call)
- Warning → Slack #alerts channel
- Info → Prometheus Alertmanager (no notification)

**Alert fatigue prevention:**
- Use `for: 5m` to avoid flapping
- Group related alerts (e.g., all Redis errors)
- Tune thresholds based on actual noise

## Future Considerations

**If tracing needed:**
- OpenTelemetry SDK for Go
- Trace vote processing: webhook → engine → Redis
- Sample 1% of traces (reduce storage costs)

**If more complex:**
- Service mesh (Istio) for network-level observability
- Distributed tracing (Jaeger, Tempo)
- Log-based metrics (Loki metrics from logs)

## References

- [Prometheus Best Practices](https://prometheus.io/docs/practices/)
- [SRE Book - Monitoring Distributed Systems](https://sre.google/sre-book/monitoring-distributed-systems/)
- [Four Golden Signals](https://sre.google/sre-book/monitoring-distributed-systems/#xref_monitoring_golden-signals)

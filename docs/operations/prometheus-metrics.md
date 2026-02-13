# Prometheus Metrics

ChatPulse exposes comprehensive Prometheus metrics at the `/metrics` endpoint for monitoring application health, performance, and business metrics.

## Metrics Endpoint

**URL**: `GET /metrics`

**Authentication**: None (publicly accessible for Prometheus scraping)

**Content-Type**: `text/plain; version=0.0.4; charset=utf-8`

**Format**: Prometheus exposition format

## Scrape Configuration

```yaml
# prometheus.yml
scrape_configs:
  - job_name: 'chatpulse'
    scrape_interval: 15s
    scrape_timeout: 10s
    metrics_path: '/metrics'
    static_configs:
      - targets: ['chatpulse:8080']
    # Or for Kubernetes:
    kubernetes_sd_configs:
      - role: pod
    relabel_configs:
      - source_labels: [__meta_kubernetes_pod_label_app]
        regex: chatpulse
        action: keep
```

## Available Metrics

### Redis Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `redis_operations_total` | Counter | `operation`, `status` | Total Redis operations by type (get, set, fcall, etc.) and status (success, error) |
| `redis_operation_duration_seconds` | Histogram | `operation` | Redis operation latency distribution |
| `redis_connection_errors_total` | Counter | - | Total Redis connection errors |
| `redis_pool_connections` | Gauge | `state` | Redis connection pool size by state (active, idle) |

**Operations**: `get`, `set`, `fcall`, `fcall_ro`, `hget`, `hset`, `setnx`, `del`, `expire`, `scan`

**Alert Examples**:
```yaml
# High Redis error rate
- alert: HighRedisErrorRate
  expr: rate(redis_operations_total{status="error"}[5m]) > 0.05
  for: 2m
  annotations:
    summary: "High Redis error rate ({{ $value }} errors/sec)"

# Slow Redis operations
- alert: SlowRedisOperations
  expr: histogram_quantile(0.99, rate(redis_operation_duration_seconds_bucket[5m])) > 0.1
  for: 5m
  annotations:
    summary: "99th percentile Redis latency > 100ms"
```

### Broadcaster Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `broadcaster_active_sessions` | Gauge | - | Number of active broadcast sessions |
| `broadcaster_connected_clients_total` | Gauge | - | Total number of connected WebSocket clients |
| `broadcaster_slow_clients_evicted_total` | Counter | - | Slow WebSocket clients evicted due to buffer full |
| `pubsub_messages_received_total` | Counter | `channel` | Total pub/sub messages received by channel |
| `pubsub_message_latency_seconds` | Histogram | - | Latency from pub/sub message receive to WebSocket client send |
| `pubsub_reconnections_total` | Counter | - | Total pub/sub reconnection attempts after disconnect |
| `pubsub_subscription_active` | Gauge | - | 1 if pub/sub subscription is active, 0 if disconnected |

**Alert Examples**:
```yaml
# High pub/sub latency
- alert: HighPubSubLatency
  expr: histogram_quantile(0.99, rate(pubsub_message_latency_seconds_bucket[5m])) > 0.05
  for: 5m
  annotations:
    summary: "99th percentile pub/sub to WebSocket latency > 50ms"

# Pub/sub subscription down
- alert: PubSubSubscriptionDown
  expr: pubsub_subscription_active == 0
  for: 30s
  annotations:
    summary: "Redis pub/sub subscription is disconnected"

# Client evictions
- alert: ClientEvictions
  expr: rate(broadcaster_slow_clients_evicted_total[5m]) > 0
  for: 2m
  annotations:
    summary: "WebSocket clients being evicted due to slow delivery"
```

### WebSocket Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `websocket_connections_current` | Gauge | - | Current number of active WebSocket connections |
| `websocket_connections_total` | Counter | `result` | Total connection attempts by result (success, error, rejected) |
| `websocket_connections_rejected_total` | Counter | `reason` | Total connections rejected by limiter (rate_limit, global_limit, ip_limit) |
| `websocket_connection_capacity_percent` | Gauge | - | Connection capacity utilization (0-100%) |
| `websocket_unique_ips` | Gauge | - | Number of unique IP addresses with active connections |
| `websocket_connection_duration_seconds` | Histogram | - | WebSocket connection lifetime |
| `websocket_message_send_duration_seconds` | Histogram | - | Time to send a message to a WebSocket client |
| `websocket_ping_failures_total` | Counter | - | WebSocket ping failures (client not responding) |

**Alert Examples**:
```yaml
# Connection capacity
- alert: HighWebSocketCapacity
  expr: websocket_connection_capacity_percent > 80
  for: 5m
  annotations:
    summary: "WebSocket capacity at {{ $value }}%"

# High rejection rate
- alert: HighWebSocketRejectionRate
  expr: rate(websocket_connections_rejected_total[5m]) > 10
  for: 2m
  annotations:
    summary: "High WebSocket rejection rate ({{ $value }} rejects/sec)"

# Ping failures
- alert: WebSocketPingFailures
  expr: rate(websocket_ping_failures_total[5m]) > 0.1
  for: 5m
  annotations:
    summary: "WebSocket clients not responding to pings"
```

### Vote Processing Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `vote_processing_total` | Counter | `result` | Total votes processed by result (applied, debounced, invalid, no_session, error) |
| `vote_processing_duration_seconds` | Histogram | - | Vote processing latency |
| `vote_trigger_matches_total` | Counter | `trigger_type` | Trigger matches by type (for, against) |
| `vote_rate_limit_tokens_remaining` | Histogram | - | Vote rate limiter token bucket state |

**Results**: `applied` (vote applied), `debounced` (rate limited), `invalid` (no trigger match), `no_session` (session not found), `error` (processing error)

**Alert Examples**:
```yaml
# High debounce rate
- alert: HighVoteDebounceRate
  expr: rate(vote_processing_total{result="debounced"}[5m]) / rate(vote_processing_total[5m]) > 0.3
  for: 5m
  annotations:
    summary: "{{ $value | humanizePercentage }} of votes being debounced"

# Vote processing errors
- alert: VoteProcessingErrors
  expr: rate(vote_processing_total{result="error"}[5m]) > 0.01
  for: 2m
  annotations:
    summary: "Vote processing errors detected"
```

### Database Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `db_query_duration_seconds` | Histogram | `query` | Database query latency by query name |
| `db_connections_current` | Gauge | `state` | Database connections by state (active, idle) |
| `db_errors_total` | Counter | `query` | Total database errors by query |

**Query Names**: Extracted from SQL comments (e.g., `-- name: GetUserByID`) or first 50 chars

**Alert Examples**:
```yaml
# Slow database queries
- alert: SlowDatabaseQueries
  expr: histogram_quantile(0.99, rate(db_query_duration_seconds_bucket[5m])) > 1
  for: 5m
  annotations:
    summary: "99th percentile database query latency > 1s"

# Database errors
- alert: DatabaseErrors
  expr: rate(db_errors_total[5m]) > 0.01
  for: 2m
  annotations:
    summary: "Database errors detected"

# Connection pool exhaustion
- alert: DatabaseConnectionPoolLow
  expr: db_connections_current{state="idle"} < 2
  for: 5m
  annotations:
    summary: "Low idle database connections (potential pool exhaustion)"
```

### Application Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `config_cache_hits_total` | Counter | - | Config cache hits |
| `config_cache_misses_total` | Counter | - | Config cache misses |
| `config_cache_entries` | Gauge | - | Current number of config cache entries |
| `config_cache_evictions_total` | Counter | - | Config cache evictions due to TTL expiry |

**Alert Examples**:
```yaml
# Low cache hit rate
- alert: LowConfigCacheHitRate
  expr: rate(config_cache_hits_total[5m]) / (rate(config_cache_hits_total[5m]) + rate(config_cache_misses_total[5m])) < 0.8
  for: 10m
  annotations:
    summary: "Config cache hit rate < 80%"
```

### Standard Go Metrics

Prometheus client library automatically exports standard Go runtime metrics:

- `go_goroutines` - Number of goroutines
- `go_memstats_*` - Memory statistics
- `go_gc_duration_seconds` - GC pause durations
- `process_*` - Process CPU, memory, file descriptors

## Useful PromQL Queries

### Performance Monitoring

```promql
# Redis operation rate by type
rate(redis_operations_total[5m])

# 95th percentile Redis latency by operation
histogram_quantile(0.95, rate(redis_operation_duration_seconds_bucket[5m]))

# Pub/sub message throughput
rate(pubsub_messages_received_total[5m])

# Pub/sub message latency (p99)
histogram_quantile(0.99, rate(pubsub_message_latency_seconds_bucket[5m]))

# WebSocket message throughput
rate(websocket_message_send_duration_seconds_count[5m])
```

### Business Metrics

```promql
# Vote application rate
rate(vote_processing_total{result="applied"}[5m])

# Vote debounce rate (rate limiting)
rate(vote_processing_total{result="debounced"}[5m])

# Trigger match distribution
rate(vote_trigger_matches_total[5m])

# Active sessions
broadcaster_active_sessions

# Connected viewers
broadcaster_connected_clients_total
```

### Capacity Planning

```promql
# WebSocket connection capacity utilization
websocket_connection_capacity_percent

# Connection growth rate
deriv(websocket_connections_current[5m])

# Database connection pool utilization
db_connections_current{state="active"} / (db_connections_current{state="active"} + db_connections_current{state="idle"})

# Memory growth rate
deriv(go_memstats_heap_alloc_bytes[5m])
```

### Error Detection

```promql
# Redis error rate
rate(redis_operations_total{status="error"}[5m])

# Vote processing errors
rate(vote_processing_total{result="error"}[5m])

# Database query errors
rate(db_errors_total[5m])

# WebSocket rejections by reason
rate(websocket_connections_rejected_total[5m])
```

## Grafana Dashboard

Example Grafana dashboard JSON (import via UI):

```json
{
  "dashboard": {
    "title": "ChatPulse Observability",
    "panels": [
      {
        "title": "Active Sessions",
        "targets": [{"expr": "broadcaster_active_sessions"}],
        "type": "graph"
      },
      {
        "title": "Connected Clients",
        "targets": [{"expr": "broadcaster_connected_clients_total"}],
        "type": "graph"
      },
      {
        "title": "Vote Rate",
        "targets": [
          {"expr": "rate(vote_processing_total{result=\"applied\"}[5m])", "legendFormat": "Applied"},
          {"expr": "rate(vote_processing_total{result=\"debounced\"}[5m])", "legendFormat": "Debounced"}
        ],
        "type": "graph"
      },
      {
        "title": "Redis Latency (p99)",
        "targets": [{"expr": "histogram_quantile(0.99, rate(redis_operation_duration_seconds_bucket[5m]))"}],
        "type": "graph"
      },
      {
        "title": "WebSocket Capacity",
        "targets": [{"expr": "websocket_connection_capacity_percent"}],
        "type": "gauge",
        "thresholds": [{"value": 80, "color": "yellow"}, {"value": 90, "color": "red"}]
      }
    ]
  }
}
```

## Best Practices

1. **Scrape interval**: 15 seconds (balance freshness vs. load)
2. **Scrape timeout**: 10 seconds (metrics endpoint should respond in <1s)
3. **Retention**: 15 days for detailed metrics, 1 year for downsampled aggregates
4. **Alert tuning**: Use `for: Xm` clauses to reduce flapping
5. **Label cardinality**: All labels have bounded cardinality (<100 unique values)

## Troubleshooting

### Metrics not appearing

- Verify `/metrics` endpoint is accessible: `curl http://localhost:8080/metrics`
- Check Prometheus targets page: should show `chatpulse` job as UP
- Some metrics only appear after events (e.g., `vote_processing_total` after first vote)

### High cardinality warnings

- All labels have bounded cardinality by design
- Query names are extracted from SQL (not full query text)
- Operations are from a fixed set (not user-controlled)

### Scrape timeouts

- Increase `scrape_timeout` in prometheus.yml
- Check server logs for slow metrics collection (shouldn't happen)

## Related Documentation

- [Load Balancer Health Checks](../deployment/load-balancer-health-checks.md)
- [Prometheus Documentation](https://prometheus.io/docs/)
- [PromQL Basics](https://prometheus.io/docs/prometheus/latest/querying/basics/)

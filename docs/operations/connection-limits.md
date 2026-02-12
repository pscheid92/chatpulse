# WebSocket Connection Limits

ChatPulse implements multi-layer connection limits to protect against DoS attacks and resource exhaustion.

## Overview

Three independent layers of defense:

1. **Global Limit** - Maximum total connections per instance
2. **Per-IP Limit** - Maximum connections from a single IP address
3. **Rate Limiting** - Maximum new connections per second per IP

All limits are configurable via environment variables.

---

## Configuration

### Environment Variables

Add to your `.env` file:

```bash
# WebSocket Connection Limits
MAX_WEBSOCKET_CONNECTIONS=10000  # Global limit per instance
MAX_CONNECTIONS_PER_IP=100       # Per-IP limit
CONNECTION_RATE_PER_IP=10        # Connections per second per IP
CONNECTION_RATE_BURST=20         # Rate limiter burst allowance
```

### Default Values

| Variable | Default | Purpose |
|----------|---------|---------|
| `MAX_WEBSOCKET_CONNECTIONS` | 10000 | Prevents single instance from exhausting file descriptors |
| `MAX_CONNECTIONS_PER_IP` | 100 | Prevents single-source attacks while allowing NATs |
| `CONNECTION_RATE_PER_IP` | 10 | Prevents connection spam (10 connections/sec) |
| `CONNECTION_RATE_BURST` | 20 | Allows bursts up to 20 immediate connections |

---

## How It Works

### Layer 1: Global Connection Limit

**Purpose**: Protect against total resource exhaustion

**Implementation**:
- Lock-free atomic counter
- Tracks total active connections across all IPs
- Rejects new connections when at capacity

**Response when exceeded**:
- HTTP 503 Service Unavailable
- Error: "Server at capacity. Please try again later."
- Metric: `websocket_connections_rejected_total{reason="global_limit"}`

**When to adjust**:
- Increase if you have high file descriptor limits (`ulimit -n > 20000`)
- Decrease if running on constrained environments

### Layer 2: Per-IP Connection Limit

**Purpose**: Protect against single-source attacks

**Implementation**:
- Mutex-protected map tracking connections per IP
- Handles NAT scenarios (multiple users behind same IP)
- Automatically cleans up when IP count reaches zero

**Response when exceeded**:
- HTTP 429 Too Many Requests
- Error: "Too many connections from your IP address."
- Metric: `websocket_connections_rejected_total{reason="ip_limit"}`

**When to adjust**:
- Increase for corporate NATs (many users, one IP)
- Monitor `websocket_unique_ips` metric for IP distribution

### Layer 3: Connection Rate Limiting

**Purpose**: Prevent connection spam and rapid reconnect attacks

**Implementation**:
- Token bucket algorithm (golang.org/x/time/rate)
- Per-IP rate limiters with automatic cleanup (inactive for 10 minutes)
- Burst allows initial rapid connections, then sustained rate

**Response when exceeded**:
- HTTP 429 Too Many Requests
- Error: "Too many connection attempts. Please slow down."
- Metric: `websocket_connections_rejected_total{reason="rate_limit"}`

**When to adjust**:
- Increase rate for legitimate high-frequency reconnects
- Increase burst for batch connection scenarios

---

## Monitoring

### Prometheus Metrics

**Rejection Counters**:
```promql
# Total rejections by reason
rate(websocket_connections_rejected_total[5m])

# Alert on high rejection rate
rate(websocket_connections_rejected_total{reason="global_limit"}[5m]) > 10
```

**Capacity Monitoring**:
```promql
# Current capacity utilization (0-100%)
websocket_connection_capacity_percent

# Alert when approaching capacity
websocket_connection_capacity_percent > 80
```

**IP Distribution**:
```promql
# Number of unique IPs connected
websocket_unique_ips

# Detect potential DDoS (many IPs, low per-IP count)
websocket_unique_ips > 1000 AND
  websocket_connections_current / websocket_unique_ips < 2
```

### Grafana Dashboard Queries

**Connection Limit Panel**:
```promql
# Current vs. Max
websocket_connections_current
# Capacity line
(websocket_connection_capacity_percent / 100) * 10000
```

**Rejection Rate Panel**:
```promql
sum(rate(websocket_connections_rejected_total[5m])) by (reason)
```

**Top Rejected IPs** (requires access logs):
```logql
# Loki query
{job="chatpulse"}
  |= "WebSocket connection rejected"
  | json
  | __error__=""
  | topk by (ip) (10)
```

---

## Alerts

### Prometheus AlertManager Rules

```yaml
groups:
  - name: websocket_limits
    interval: 30s
    rules:
      # Alert when capacity > 80% for 5 minutes
      - alert: WebSocketCapacityHigh
        expr: websocket_connection_capacity_percent > 80
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "WebSocket connections at {{ $value }}% capacity"
          description: "Consider scaling horizontally or increasing MAX_WEBSOCKET_CONNECTIONS"

      # Alert when capacity > 95%
      - alert: WebSocketCapacityCritical
        expr: websocket_connection_capacity_percent > 95
        for: 1m
        labels:
          severity: critical
        annotations:
          summary: "WebSocket connections at {{ $value }}% capacity (CRITICAL)"
          description: "Immediate action required: scale up or connections will be rejected"

      # Alert on sustained high rejection rate
      - alert: WebSocketHighRejectionRate
        expr: rate(websocket_connections_rejected_total[5m]) > 50
        for: 10m
        labels:
          severity: warning
        annotations:
          summary: "High WebSocket rejection rate: {{ $value }} req/sec"
          description: "Check for DoS attack or misconfigured limits"

      # Alert when global limit frequently hit
      - alert: WebSocketGlobalLimitReached
        expr: rate(websocket_connections_rejected_total{reason="global_limit"}[5m]) > 1
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "Global connection limit being reached"
          description: "Scale horizontally or increase MAX_WEBSOCKET_CONNECTIONS"
```

---

## Tuning Guide

### Scenario: Corporate NAT (Many Users, One IP)

**Problem**: Legitimate users behind corporate NAT hitting per-IP limit

**Solution**:
```bash
MAX_CONNECTIONS_PER_IP=500  # Increase from 100
```

**Verification**:
```promql
# Check if single IPs have many connections
topk(10, sum(websocket_connections_per_ip) by (ip))
```

### Scenario: High-Capacity Instance

**Problem**: Server can handle more than 10K connections

**Solution**:
1. Verify file descriptor limit: `ulimit -n`
2. Increase system limit: `ulimit -n 65536`
3. Update config:
```bash
MAX_WEBSOCKET_CONNECTIONS=50000  # Increase from 10K
```

**Verification**:
```bash
# Check actual limits
cat /proc/$(pidof server)/limits | grep "open files"
```

### Scenario: Bot Attack (Rapid Reconnects)

**Problem**: Attacker rapidly connecting/disconnecting

**Solution**:
```bash
CONNECTION_RATE_PER_IP=5    # Decrease from 10
CONNECTION_RATE_BURST=5     # Decrease from 20
```

**Detection**:
```promql
# High rate limit rejections
rate(websocket_connections_rejected_total{reason="rate_limit"}[1m]) > 10
```

### Scenario: Legitimate Burst Traffic

**Problem**: Users legitimately opening many connections at once

**Solution**:
```bash
CONNECTION_RATE_BURST=50    # Increase from 20
# Keep sustained rate low to prevent abuse
CONNECTION_RATE_PER_IP=10   # Keep at 10
```

---

## Load Testing

### Using the Provided Script

```bash
# Test with 100 connections
./scripts/load-test-connections.sh <overlay-uuid> 100

# Test global limit (should hit at 10K)
./scripts/load-test-connections.sh <overlay-uuid> 11000

# Test rate limit (rapid connections)
./scripts/load-test-connections.sh <overlay-uuid> 50 ws://localhost:8080 50
```

### Using Custom Tools

**wscat** (Node.js):
```bash
npm install -g wscat

# Open many connections
for i in {1..200}; do
  wscat -c ws://localhost:8080/ws/overlay/<uuid> &
done
```

**websocat** (Rust):
```bash
# Install
cargo install websocat

# Open connections with delay
for i in {1..100}; do
  websocat ws://localhost:8080/ws/overlay/<uuid> &
  sleep 0.1
done
```

### Expected Results

| Test Scenario | Expected Behavior | HTTP Status |
|---------------|-------------------|-------------|
| < 10K connections | All succeed | 101 (WebSocket Upgrade) |
| > 10K connections | First 10K succeed, rest fail | 503 (global limit) |
| >100 from same IP | First 100 succeed, rest fail | 429 (per-IP limit) |
| >10/sec from same IP (sustained) | Rate limited | 429 (rate limit) |
| Burst of 20, then sustained 10/sec | Burst succeeds, sustained throttled | 101 / 429 |

---

## Troubleshooting

### Issue: Legitimate Users Getting 429

**Symptoms**: Users report "Too many connections" errors

**Diagnosis**:
```bash
# Check rejection metrics
curl -s http://localhost:8080/metrics | grep websocket_connections_rejected

# Check per-IP distribution
curl -s http://localhost:8080/metrics | grep websocket_unique_ips
```

**Solutions**:
1. Increase `MAX_CONNECTIONS_PER_IP` if corporate NAT
2. Increase `CONNECTION_RATE_BURST` if burst traffic
3. Check for connection leaks (connections not closing properly)

### Issue: Server At Capacity (503)

**Symptoms**: All new connections get 503 errors

**Diagnosis**:
```bash
# Check capacity
curl -s http://localhost:8080/metrics | grep websocket_connection_capacity_percent

# Check current connections
curl -s http://localhost:8080/metrics | grep websocket_connections_current
```

**Solutions**:
1. Scale horizontally (add more instances)
2. Increase `MAX_WEBSOCKET_CONNECTIONS` if server has capacity
3. Check for connection leaks (old connections not closing)
4. Verify file descriptor limits: `ulimit -n`

### Issue: Connection Leaks

**Symptoms**: Capacity keeps increasing, never decreases

**Diagnosis**:
```bash
# Monitor connections over time
watch 'curl -s http://localhost:8080/metrics | grep websocket_connections_current'

# Check for stuck connections
netstat -an | grep :8080 | grep ESTABLISHED | wc -l
```

**Solutions**:
1. Check application logs for WebSocket handler errors
2. Verify `defer s.connLimits.Release(clientIP)` is called
3. Check for panics in WebSocket handlers
4. Review broadcaster registration/unregistration logic

---

## Security Considerations

### DDoS Protection

**Built-in defenses**:
- Rate limiting prevents rapid connection spam
- Per-IP limit prevents single-source exhaustion
- Global limit prevents total resource exhaustion

**Additional recommendations**:
1. Use a reverse proxy (Nginx, HAProxy) with additional limits
2. Implement IP allowlisting for known corporate NATs
3. Use Cloudflare or similar CDN for L7 DDoS protection
4. Monitor `websocket_unique_ips` for distributed attacks

### Resource Limits

**File Descriptors**:
```bash
# Check current limit
ulimit -n

# Set higher limit (requires root)
ulimit -n 65536

# Make permanent (add to /etc/security/limits.conf)
* soft nofile 65536
* hard nofile 65536
```

**Memory**:
- Each WebSocket connection: ~4KB overhead
- 10K connections: ~40MB overhead
- Monitor with: `ps aux | grep server`

---

## References

- [Prometheus Metrics Documentation](../metrics/README.md)
- [Load Balancer Health Checks](./load-balancer-health-checks.md)
- [WebSocket Security Best Practices](../security/websocket-security.md)
- [golang.org/x/time/rate package](https://pkg.go.dev/golang.org/x/time/rate)

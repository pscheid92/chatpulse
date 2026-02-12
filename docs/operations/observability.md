# Observability Guide

This guide covers health checks, metrics, and monitoring for ChatPulse.

## Health Check Endpoints

ChatPulse provides three health check endpoints for different use cases:

### Startup Probe: `GET /health/startup`

**Purpose**: Kubernetes startup probe to detect when the application has fully initialized.

**Checks**:
- Redis connectivity (`PING`)
- PostgreSQL connectivity (`Ping`)
- Redis Functions loaded (`chatpulse` library)

**Response**:
- `200 OK`: All checks passed, application ready
  ```json
  {"status": "ready"}
  ```
- `503 Service Unavailable`: At least one check failed
  ```json
  {
    "status": "unhealthy",
    "failed_check": "redis",
    "error": "connection refused"
  }
  ```

**Timeout**: 2 seconds

**Use case**: Set as Kubernetes `startupProbe` to prevent premature traffic routing during initialization.

### Liveness Probe: `GET /health/live`

**Purpose**: Kubernetes liveness probe to detect deadlocked or hung processes.

**Checks**:
- HTTP server responsiveness
- Application uptime

**Response**:
- `200 OK`: Application is alive
  ```json
  {
    "status": "ok",
    "uptime": 3600.5
  }
  ```

**Timeout**: N/A (instant response)

**Use case**: Set as Kubernetes `livenessProbe` to restart crashed or deadlocked pods. Kubernetes will restart the pod if this endpoint returns non-200 or times out.

### Readiness Probe: `GET /health/ready`

**Purpose**: Kubernetes readiness probe to control traffic routing.

**Checks**:
- Redis connectivity (`PING`)
- PostgreSQL connectivity (`Ping`)
- Redis Functions loaded (`chatpulse` library)

**Response**:
- `200 OK`: All checks passed, ready to serve traffic
  ```json
  {"status": "ready"}
  ```
- `503 Service Unavailable`: At least one check failed
  ```json
  {
    "status": "unhealthy",
    "failed_check": "postgres",
    "error": "database unreachable"
  }
  ```

**Timeout**: 5 seconds

**Use case**: Set as Kubernetes `readinessProbe` to temporarily remove unhealthy pods from load balancer rotation during transient failures.

## Version Endpoint

### Build Information: `GET /version`

**Purpose**: Query application build information for debugging and deployment tracking.

**Response**:
```json
{
  "version": "v1.2.3",
  "commit": "a1b2c3d",
  "build_time": "2026-02-12T10:30:00Z",
  "go_version": "go1.26.0"
}
```

**Fields**:
- `version`: Git tag or `"dev"` if built from untagged commit
- `commit`: Short git commit SHA (7 chars) or `"unknown"`
- `build_time`: ISO 8601 UTC timestamp of build or `"unknown"`
- `go_version`: Go compiler version (e.g., `go1.26.0`)

**Build-time injection**: Values are injected via `-ldflags` during `make build` (see Makefile).

## Prometheus Metrics

ChatPulse exposes Prometheus metrics at `GET /metrics` (no authentication required).

### Build Information Metric

The `build_info` gauge provides build metadata as labels:

```prometheus
# HELP build_info Build information with version, commit, build_time, and go_version labels (value is always 1)
# TYPE build_info gauge
build_info{version="v1.2.3",commit="a1b2c3d",build_time="2026-02-12T10:30:00Z",go_version="go1.26.0"} 1
```

**Use case**: Join with other metrics in Prometheus queries to correlate behavior with specific builds.

**Example queries**:
```promql
# Current version running on each instance
build_info * on(instance) group_left() up

# Count instances by version
count by (version) (build_info)

# Alert on version mismatch across replicas
count(count by (version) (build_info)) > 1
```

For a complete list of available metrics, see [docs/operations/prometheus-metrics.md](prometheus-metrics.md).

## Kubernetes Configuration

Example pod spec with all three probes:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: chatpulse
spec:
  containers:
  - name: chatpulse
    image: chatpulse:latest
    ports:
    - containerPort: 8080
      name: http

    # Startup probe: Wait for application initialization (max 60s)
    startupProbe:
      httpGet:
        path: /health/startup
        port: http
      initialDelaySeconds: 0
      periodSeconds: 5
      timeoutSeconds: 2
      failureThreshold: 12  # 12 * 5s = 60s max startup time

    # Liveness probe: Detect deadlocks/crashes
    livenessProbe:
      httpGet:
        path: /health/live
        port: http
      initialDelaySeconds: 0
      periodSeconds: 10
      timeoutSeconds: 1
      failureThreshold: 3  # Restart after 30s of failed liveness checks

    # Readiness probe: Control traffic routing
    readinessProbe:
      httpGet:
        path: /health/ready
        port: http
      initialDelaySeconds: 0
      periodSeconds: 5
      timeoutSeconds: 5
      failureThreshold: 2  # Remove from LB after 10s of failed readiness checks
```

### Probe Configuration Guidelines

**Startup Probe**:
- Use `failureThreshold * periodSeconds` to set max startup time (e.g., 12 * 5s = 60s)
- Prevents liveness probe from killing slow-starting containers
- Required for applications with variable startup time (Redis Function loading, migrations)

**Liveness Probe**:
- Keep checks lightweight (no expensive operations)
- Set `failureThreshold` high enough to avoid false positives (3-5 failures)
- Avoid checking external dependencies (Redis/Postgres failures should not restart the pod)
- ChatPulse liveness checks only HTTP responsiveness + uptime

**Readiness Probe**:
- Check all critical dependencies (Redis + Postgres connectivity, Redis Functions loaded)
- Set `periodSeconds` low for fast failure detection (5s)
- Use longer `timeoutSeconds` than liveness (5s vs 1s) to allow for slow queries
- Temporary failures remove pod from load balancer but don't restart it

## Monitoring Best Practices

### 1. Track Version Across Deployments

Monitor version distribution during rollouts:

```promql
count by (version) (build_info)
```

Alert on version skew:

```promql
count(count by (version) (build_info)) > 1
```

### 2. Correlate Errors with Builds

Join error rate with version:

```promql
rate(http_requests_total{status=~"5.."}[5m])
  * on(instance) group_left(version, commit)
  build_info
```

### 3. Health Check Failure Alerts

Alert when pods fail readiness checks:

```promql
up{job="chatpulse"} == 0
```

Alert on Redis connection failures:

```promql
rate(redis_connection_errors_total[5m]) > 0
```

### 4. Startup Time Tracking

Track how long pods take to become ready after deployment:

```promql
time() - process_start_time_seconds
```

Combine with build info to compare startup times across versions:

```promql
(time() - process_start_time_seconds)
  * on(instance) group_left(version)
  build_info
```

## Troubleshooting

### Health check failing: "chatpulse library not loaded"

**Cause**: Redis Functions failed to load at startup.

**Resolution**:
1. Check Redis version (requires Redis 7.0+)
2. Check server logs for Function loading errors
3. Verify Redis connectivity and permissions
4. Restart the pod to retry Function loading

### Startup probe timeout (pod stuck in "Not Ready")

**Cause**: Database migrations or Redis initialization taking >60s.

**Resolution**:
1. Check pod logs: `kubectl logs <pod-name>`
2. Verify database connectivity: `kubectl exec <pod-name> -- psql $DATABASE_URL -c '\l'`
3. Increase `startupProbe.failureThreshold` if migrations are slow
4. Check for database locks blocking migrations

### Readiness probe intermittent failures

**Cause**: Transient network issues or database connection pool exhaustion.

**Resolution**:
1. Check Redis/Postgres metrics for latency spikes
2. Increase database connection pool size
3. Verify network policies aren't blocking health checks
4. Check for resource contention (CPU throttling)

### Version shows "dev" in production

**Cause**: Binary built without version injection (missing `-ldflags`).

**Resolution**:
- Always build with `make build` (not `go build` directly)
- In CI/CD, ensure `VERSION` env var is set before build
- For Docker builds, pass `--build-arg VERSION=$(git describe --tags)`

## Integration Examples

### Docker Healthcheck

```dockerfile
FROM golang:1.26 AS builder
WORKDIR /app
COPY . .
RUN make build

FROM debian:bookworm-slim
COPY --from=builder /app/server /usr/local/bin/server

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD curl -f http://localhost:8080/health/live || exit 1

ENTRYPOINT ["/usr/local/bin/server"]
```

### Prometheus ServiceMonitor

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: chatpulse
spec:
  selector:
    matchLabels:
      app: chatpulse
  endpoints:
  - port: http
    path: /metrics
    interval: 30s
    scrapeTimeout: 10s
```

### Grafana Dashboard Query

Show current version for each instance:

```promql
build_info{job="chatpulse"}
```

Create a "Version" table panel:
- Query: `build_info`
- Format: Table
- Transform: "Organize fields" → Show `instance`, `version`, `commit`, `build_time`

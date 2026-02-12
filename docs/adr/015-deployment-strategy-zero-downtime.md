# ADR-015: Deployment Strategy and Zero-Downtime Deploys

**Status:** Accepted
**Date:** 2026-02-12
**Authors:** Infrastructure Team
**Tags:** operations, deployment, reliability

## Context

ChatPulse must deploy new versions without service interruption. Overlays are embedded in streamers' broadcasts - downtime is visible to thousands of viewers and reflects poorly on the service.

### Requirements

1. **Zero downtime** - Viewers never see "disconnected" overlay
2. **Backward compatibility** - Old pods + new schema must coexist during rollout
3. **Safe rollback** - Can revert to previous version if issues detected
4. **Kubernetes-native** - Use standard K8s primitives (no custom controllers)

## Decision

Use **Kubernetes rolling deployment** with backward-compatible migrations and graceful shutdown.

### Architecture

**Kubernetes Deployment:**
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: chatpulse
spec:
  replicas: 3
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxUnavailable: 1   # At most 1 pod down during rollout
      maxSurge: 1         # At most 1 extra pod during rollout
  template:
    spec:
      containers:
      - name: server
        image: chatpulse:v1.2.3
        ports:
        - containerPort: 8080
        livenessProbe:
          httpGet:
            path: /health
            port: 8080
          initialDelaySeconds: 5
          periodSeconds: 10
        readinessProbe:
          httpGet:
            path: /ready
            port: 8080
          initialDelaySeconds: 10
          periodSeconds: 5
        lifecycle:
          preStop:
            exec:
              command: ["/bin/sh", "-c", "sleep 10"]  # Drain connections
```

### Migration Strategy: Expand/Contract Pattern

**Phase 1: Expand** (add new column, old code ignores it)
```sql
-- Migration 005_add_column.sql
ALTER TABLE users ADD COLUMN new_field TEXT;
---- create above / drop below ----
ALTER TABLE users DROP COLUMN new_field;
```

**Deploy:** Rolling update
- Old pods: Ignore new_field (not in sqlc queries)
- New pods: Use new_field

**Phase 2: Contract** (remove old column, next deploy)
```sql
-- Migration 006_remove_old_column.sql
ALTER TABLE users DROP COLUMN old_field;
```

**Two-phase rule:** Schema changes require 2 deploys minimum
- Deploy 1: Add new column
- Deploy 2: (weeks later) Remove old column

### Startup Sequence

**Pod initialization:**
1. Load config (env vars)
2. Connect to PostgreSQL (with retry, see ADR database resilience)
3. Run migrations with advisory lock (coordinate across instances)
4. Connect to Redis (load Lua functions)
5. Start health check endpoints
6. Mark ready (`/ready` returns 200)
7. Start HTTP server

**Migration coordination:**
- Advisory lock (`pg_advisory_lock(0x636861747075)`)
- First pod to start acquires lock, runs migrations
- Other pods wait for lock, then skip (already applied)

### Graceful Shutdown

**SIGTERM handler:**
```go
func runGracefulShutdown(srv, appSvc, broadcaster, eventsubMgr) {
    sigCh := make(chan os.Signal, 1)
    signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

    go func() {
        <-sigCh
        slog.Info("Shutdown signal received")

        // 1. Stop accepting new connections
        shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
        defer cancel()
        srv.Shutdown(shutdownCtx)

        // 2. Stop internal services
        appSvc.Stop()          // Stop orphan cleanup timer
        broadcaster.Stop()     // Close all WebSocket connections

        // 3. Cleanup external resources
        if eventsubMgr != nil {
            eventsubMgr.Cleanup(shutdownCtx)  // Delete conduit
        }

        close(done)
    }()
}
```

**Pre-stop hook (10s drain):**
- Kubernetes stops routing traffic
- Existing WebSocket connections continue
- 10s allows clients to reconnect to new pods

### Health Checks

**Liveness probe (`/health`):**
- Returns 200 if server process alive
- Failure → K8s restarts pod

**Readiness probe (`/ready`):**
- Checks: PostgreSQL ping, Redis ping, Lua functions loaded
- Returns 200 only when ready to serve traffic
- Failure → K8s stops routing traffic (pod removed from service)

### Redis Lua Function Versioning

**Zero-downtime Lua updates:**
1. Old pods have old functions loaded
2. New pod calls `FUNCTION LOAD REPLACE` (overwrites library)
3. Old pods still work (functions cached in Redis)
4. Atomic cutover when last old pod stops

**Version tracking:**
- Store version in Redis key: `chatpulse:function:version = "2"`
- New pods check version, log warning if mismatch
- Both versions can coexist (function names include version: `apply_vote_v2`)

## Alternatives Considered

### 1. Blue/Green Deployment

Two full environments (blue + green), swap traffic atomically.

**Rejected because:**
- 2x infrastructure cost (duplicate everything)
- More complex (manage two environments, database state)
- Overkill for simple rolling updates

### 2. Separate Migration Job

Kubernetes Job runs migrations before deployment.

**Rejected because:**
- Timing issues (job must complete before pods start)
- Race conditions (multiple job pods)
- More complex (coordinate job + deployment in CI/CD)

### 3. Downtime Window

Stop all pods, run migrations, start new pods.

**Rejected because:**
- Unacceptable downtime (overlays go dark for streamers)
- Poor UX (viewers see disconnected state)
- Defeats purpose of zero-downtime requirement

## Consequences

### Positive

✅ **Standard pattern** - Kubernetes native, well-understood
✅ **Zero downtime** - Viewers never see disconnection
✅ **Safe rollback** - Can revert to previous version easily
✅ **No extra infrastructure** - Uses K8s built-in features
✅ **Gradual rollout** - One pod at a time, can stop if issues

### Negative

❌ **Two-phase migrations** - Schema changes require 2 deploys
❌ **Longer rollout** - Rolling updates slower than recreate
❌ **Backward compat burden** - Must support N-1 schema version

### Trade-offs

🔄 **Zero downtime over fast deploys** - Accept slower rollout for reliability
🔄 **Backward compat over simplicity** - Two-phase migrations for safety
🔄 **Standard K8s over custom solution** - Use platform primitives

## Deployment Checklist

**Pre-deploy:**
- [ ] Review migration for backward compatibility
- [ ] Check Lua function version (increment if breaking change)
- [ ] Test in staging with rolling update
- [ ] Verify health check endpoints work

**Deploy:**
- [ ] Kubectl apply deployment
- [ ] Monitor rollout: `kubectl rollout status deployment/chatpulse`
- [ ] Watch metrics: WebSocket connections, error rates
- [ ] Verify all pods healthy: `kubectl get pods`

**Post-deploy:**
- [ ] Smoke test: Connect to overlay, trigger vote
- [ ] Check logs for errors: `kubectl logs -l app=chatpulse --tail=100`
- [ ] Monitor for 10 minutes (catch delayed issues)

**Rollback if needed:**
- [ ] `kubectl rollout undo deployment/chatpulse`
- [ ] Monitor rollback completion
- [ ] Investigate issues in logs/metrics

## Monitoring During Rollout

**Key metrics to watch:**
- `websocket_connections_active` - Should stay constant (no drops)
- `redis_errors_total` - Should not spike
- `http_requests_total{status="5xx"}` - Should stay near zero
- `db_connection_errors_total` - Should not increase

**Alert thresholds:**
- WebSocket connections drop >10% → investigate
- 5xx error rate >1% → rollback
- Redis errors spike → check Redis health

## Related Decisions

- **ADR-002: Database resilience** - Retry logic handles transient failures during rollout
- **ADR-004: Lua function versioning** - Safe Lua updates during deploys
- **Advisory lock for migrations** - Coordinate migration execution

## Implementation

**Files:**
- `k8s/deployment.yaml` - Kubernetes deployment manifest
- `cmd/server/main.go` - Startup sequence (migrations, health checks)
- `.github/workflows/deploy.yml` - CI/CD pipeline
- `internal/database/postgres.go` - Migration advisory lock

**Commit:** Initial implementation (2025-12), enhanced (2026-02)

## CI/CD Pipeline

**GitHub Actions workflow:**
```yaml
name: Deploy
on:
  push:
    tags: ['v*']

jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - name: Build Docker image
        run: docker build -t chatpulse:${{ github.ref_name }} .

      - name: Push to registry
        run: docker push chatpulse:${{ github.ref_name }}

      - name: Update deployment
        run: |
          kubectl set image deployment/chatpulse \
            server=chatpulse:${{ github.ref_name }}

      - name: Wait for rollout
        run: kubectl rollout status deployment/chatpulse --timeout=5m
```

## Future Considerations

**If blue/green needed:**
- Add for major version changes (v2.0)
- Use Kubernetes Services to swap traffic

**If canary deploys needed:**
- Deploy new version to 10% of pods
- Monitor error rates for 1 hour
- Gradually increase to 100%

## References

- [Kubernetes Rolling Updates](https://kubernetes.io/docs/tutorials/kubernetes-basics/update/update-intro/)
- [Expand/Contract Pattern](https://martinfowler.com/bliki/ParallelChange.html)
- [Advisory Locks in PostgreSQL](https://www.postgresql.org/docs/current/explicit-locking.html#ADVISORY-LOCKS)

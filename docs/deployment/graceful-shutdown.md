# Graceful Shutdown Guide

ChatPulse implements graceful shutdown to avoid dropping active WebSocket connections during deployments, restarts, or scaling events. This guide explains how it works and how to configure it correctly for different deployment environments.

## Overview

Graceful shutdown ensures:
1. **No new connections accepted** after shutdown signal
2. **Existing WebSocket connections complete naturally** (overlay streams continue)
3. **In-flight HTTP requests finish** (no 502 errors)
4. **Redis ref counts updated** (session state cleaned up)
5. **EventSub subscriptions preserved** (no webhook interruptions)

## How It Works

### Shutdown Sequence

```
1. SIGTERM/SIGINT received
2. HTTP server stops accepting new connections
3. Existing connections continue processing (with timeout)
4. Broadcaster stops (WebSocket clients disconnect)
5. App service cleanup timer stops
6. EventSub manager cleanup (only if conduit owned by this instance)
7. Database pool closed
8. Redis client closed
9. Process exits
```

### Code Flow

From `cmd/server/main.go`:

```go
func runGracefulShutdown(srv *server.Server, ...) <-chan struct{} {
    done := make(chan struct{})
    go func() {
        defer close(done)

        // Wait for signal
        sigCh := make(chan os.Signal, 1)
        signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
        sig := <-sigCh
        log.Printf("Received signal %v, shutting down gracefully...", sig)

        // 1. Stop HTTP server (no new connections)
        ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
        defer cancel()
        if err := srv.Shutdown(ctx); err != nil {
            log.Printf("HTTP server shutdown error: %v", err)
        }

        // 2. Stop application services
        appSvc.Stop()           // Cleanup timer stops
        broadcaster.Stop()      // WebSocket clients disconnect

        // 3. Cleanup EventSub (if configured)
        if eventsubManager != nil {
            if err := eventsubManager.Cleanup(ctx); err != nil {
                log.Printf("EventSub cleanup error: %v", err)
            }
        }

        log.Println("Shutdown complete")
    }()
    return done
}

// main() calls this and waits:
done := runGracefulShutdown(srv, appSvc, broadcaster, eventsubManager)
srv.Start(":8080")  // Blocks until shutdown
<-done              // Wait for cleanup to finish
```

### Timeout Configuration

| Component | Timeout | Purpose |
|-----------|---------|---------|
| HTTP Server | 30s | Finish in-flight requests |
| WebSocket Clients | Immediate | Disconnect overlay clients (they reconnect) |
| Broadcaster Stop | 5s | Drain command channel |
| EventSub Cleanup | 30s | Delete conduit (if owned) |

## Deployment Environment Configuration

### Docker / Standalone

**Docker Compose:**
```yaml
services:
  chatpulse:
    image: ghcr.io/pscheid92/chatpulse:latest
    stop_grace_period: 60s  # Allow 60s for graceful shutdown
    stop_signal: SIGTERM
```

**Docker run:**
```bash
docker run -d \
  --name chatpulse \
  --stop-timeout 60 \
  ghcr.io/pscheid92/chatpulse:latest
```

**Systemd:**
```ini
# /etc/systemd/system/chatpulse.service
[Service]
ExecStart=/usr/local/bin/chatpulse
KillSignal=SIGTERM
TimeoutStopSec=60
```

### Kubernetes

**Deployment manifest:**
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: chatpulse
spec:
  template:
    spec:
      # Allow 60s for shutdown before SIGKILL
      terminationGracePeriodSeconds: 60

      containers:
      - name: chatpulse
        lifecycle:
          preStop:
            exec:
              # Sleep 15s to allow load balancer to deregister
              command: ["/bin/sh", "-c", "sleep 15"]

      # Readiness probe ensures no traffic during shutdown
      - readinessProbe:
          httpGet:
            path: /health/ready
            port: 8080
          periodSeconds: 5
```

**Why `sleep 15`?**

Kubernetes sends SIGTERM and removes pod from service endpoints simultaneously. The sleep ensures:
1. Load balancer stops sending new traffic (takes ~5-10s)
2. Then application starts shutdown
3. No 502 errors from in-flight requests

### AWS ECS

**Task definition:**
```json
{
  "containerDefinitions": [{
    "name": "chatpulse",
    "stopTimeout": 60,
    "healthCheck": {
      "command": ["CMD-SHELL", "curl -f http://localhost:8080/health/ready || exit 1"],
      "interval": 30,
      "timeout": 5,
      "retries": 3
    }
  }]
}
```

**ECS Service:**
- **Deregistration delay**: 30s (ALB target group attribute)
- **Health check grace period**: 300s (allow startup time)

### Nginx (Upstream Backend)

When a backend instance shuts down, Nginx detects via passive health checks:

```nginx
upstream chatpulse_backend {
    least_conn;
    server 10.0.1.10:8080 max_fails=3 fail_timeout=30s;
    server 10.0.1.11:8080 max_fails=3 fail_timeout=30s;
    server 10.0.1.12:8080 max_fails=3 fail_timeout=30s;
}
```

**Best practice:**
1. Stop sending new traffic to instance (remove from upstream config or use `down` parameter)
2. Wait 30-60s for existing connections to drain
3. Send SIGTERM to application
4. Wait up to 60s for shutdown
5. Process exits

## WebSocket Connection Behavior

### What Happens to Overlay Clients?

When a backend instance shuts down:

1. **Broadcaster stops** → sends final message to all clients
2. **WebSocket connections close** gracefully (close frame sent)
3. **Browser receives close event** → overlay reconnects automatically

### Client Reconnection

The overlay HTML (`web/templates/overlay.html`) handles reconnection:

```javascript
let ws;
let reconnectTimer;

function connect() {
    ws = new WebSocket(`wss://${location.host}/ws/overlay/${uuid}`);

    ws.onclose = () => {
        console.log("WebSocket closed, reconnecting in 2s...");
        showStatus("reconnecting...");
        reconnectTimer = setTimeout(connect, 2000);
    };

    ws.onmessage = (event) => {
        const data = JSON.parse(event.data);
        if (data.status === "active") {
            updateBar(data.value);
            showStatus("");
        }
    };
}

connect();
```

**User experience:**
- Overlay shows "reconnecting..." for ~2-5 seconds
- Reconnects to another healthy instance
- Bar continues from last known value (stored in Redis)
- **No manual refresh needed**

## Testing Graceful Shutdown

### Local Testing

```bash
# Start server
make run

# In another terminal, connect overlay
websocat ws://localhost:8080/ws/overlay/YOUR-UUID

# Send SIGTERM
kill -TERM $(pgrep chatpulse)

# Expected output:
# 1. WebSocket receives close frame
# 2. Server logs "Received signal terminated, shutting down gracefully..."
# 3. Server logs "Shutdown complete"
# 4. Process exits with code 0
```

### Docker Testing

```bash
# Start container
docker run -d --name test-chatpulse ghcr.io/pscheid92/chatpulse:latest

# Check it's healthy
docker exec test-chatpulse curl -f http://localhost:8080/health/ready

# Graceful stop
time docker stop test-chatpulse

# Expected: stops in <60s (stop_timeout)
# Check logs
docker logs test-chatpulse
```

### Kubernetes Testing

```bash
# Deploy
kubectl apply -f k8s/

# Connect to overlay via ingress
websocat wss://chatpulse.example.com/ws/overlay/YOUR-UUID

# Delete pod (simulates rolling update)
kubectl delete pod -n chatpulse chatpulse-abc123-xyz

# Expected:
# 1. WebSocket closes gracefully
# 2. Client reconnects to new pod
# 3. No 502 errors
# 4. Pod terminates cleanly (not killed)

# Check pod events
kubectl describe pod -n chatpulse chatpulse-abc123-xyz
```

### Load Testing Graceful Shutdown

```bash
#!/bin/bash
# test-graceful-shutdown.sh

# Start 100 WebSocket connections
for i in {1..100}; do
    websocat wss://chatpulse.example.com/ws/overlay/test-uuid &
done

# Wait for connections to establish
sleep 5

# Trigger rolling update
kubectl rollout restart deployment/chatpulse -n chatpulse

# Monitor reconnections
watch kubectl get pods -n chatpulse

# Expected:
# - Old pods terminate gracefully
# - New pods start
# - WebSocket clients reconnect (2-5s interruption)
# - No errors in logs
```

## Troubleshooting

### Process Killed Before Shutdown Completes

**Symptom**: Logs show "signal: killed" instead of "Shutdown complete"

**Cause**: Shutdown timeout too short (process killed by SIGKILL)

**Fix**:
- **Docker**: Increase `stop_timeout` / `stop_grace_period`
- **Kubernetes**: Increase `terminationGracePeriodSeconds`
- **Systemd**: Increase `TimeoutStopSec`

### WebSocket Connections Dropped Immediately

**Symptom**: Clients disconnect instantly, no reconnection

**Cause**: Load balancer removed backend too quickly

**Fix (Kubernetes)**:
```yaml
lifecycle:
  preStop:
    exec:
      command: ["/bin/sh", "-c", "sleep 15"]  # Wait for deregistration
```

**Fix (ALB)**:
```hcl
deregistration_delay = 30  # Terraform ALB target group
```

### 502 Errors During Shutdown

**Symptom**: Clients get 502 Bad Gateway during deployments

**Cause**: Load balancer sending traffic to shutting-down instance

**Fix**:
1. Ensure health check fails immediately when shutdown starts
2. Use `preStop` hook (Kubernetes) or deregistration delay (ALB)
3. Set proper `terminationGracePeriodSeconds` (60s)

### Redis Ref Count Not Decremented

**Symptom**: Sessions marked as "active" but no instances serving them

**Cause**: Broadcaster didn't call `OnSessionEmpty` during shutdown

**Check**:
```bash
# View ref count in Redis
redis-cli GET ref_count:YOUR-OVERLAY-UUID

# Should be 0 if no instances serving
```

**Fix**: Ensure `broadcaster.Stop()` completes before process exits (check timeout)

## Multi-Instance Considerations

### Scenario: Rolling Update with 3 Instances

```
Initial state:
- Instance A, B, C all serving overlay UUID abc-123
- ref_count:abc-123 = 3

Rolling update starts:
1. Kubernetes kills Instance A
   - A.Shutdown() → broadcaster.Stop() → OnSessionEmpty()
   - Redis: DECR ref_count:abc-123 → 2
   - Overlay clients reconnect to B or C
2. Instance A exits cleanly
3. Instance D starts, overlay reconnects
   - D.EnsureSessionActive() → INCR ref_count:abc-123 → 3
4. Kubernetes kills Instance B (repeat)
5. Kubernetes kills Instance C (repeat)

Final state:
- Instances D, E, F serving overlay
- ref_count:abc-123 = 3
- No dropped connections
```

### Orphan Cleanup During Shutdown

If an instance crashes without decrementing ref count:

- **Cleanup timer** (in `app.Service`) runs every 30s
- Finds sessions with `last_disconnect > 30s` and `ref_count > 0`
- Deletes from Redis, unsubscribes from EventSub
- **Runs on all instances** (distributed cleanup)

Graceful shutdown prevents orphans by ensuring:
1. `broadcaster.Stop()` → `OnSessionEmpty()` → `DecrRefCount()`
2. Cleanup timer stops cleanly (no mid-cleanup exit)

## Production Recommendations

1. **Set shutdown timeout to 60s** (covers HTTP drain + cleanup)
2. **Use `preStop` hook in Kubernetes** (15s sleep for deregistration)
3. **Monitor shutdown duration** (alerts if >30s)
4. **Test rolling updates under load** (ensure <5s reconnect time)
5. **Enable connection draining** on load balancers (30s)
6. **Verify ref counts in Redis** after deployments (should match instance count)

## Monitoring Shutdown

### Prometheus Metrics

Track shutdown success rate:

```promql
# Shutdown duration (from logs)
histogram_quantile(0.95, rate(shutdown_duration_seconds_bucket[5m]))

# Graceful vs forceful kills
rate(process_killed_total{signal="SIGKILL"}[5m])  # Should be 0
```

### Logs to Watch

```bash
# Successful shutdown
grep "Shutdown complete" /var/log/chatpulse.log

# Killed before completion
grep "signal: killed" /var/log/chatpulse.log

# WebSocket disconnect count
grep "WebSocket connection closed" /var/log/chatpulse.log | wc -l
```

## See Also

- [Load Balancer - Nginx](load-balancer-nginx.md)
- [Load Balancer - AWS ALB](load-balancer-aws-alb.md)
- [Load Balancer - Kubernetes](load-balancer-kubernetes.md)
- [Troubleshooting Guide](troubleshooting-load-balancer.md)

# Load Balancer Health Check Configuration

This document provides configuration examples for integrating ChatPulse health check endpoints with various load balancers and orchestration platforms.

## Overview

ChatPulse exposes two health check endpoints:

- **`GET /health/live`** — Liveness probe
  - Always returns `200 OK` if the process is alive
  - Use for detecting crashed/deadlocked processes
  - Response: `{"status":"ok"}`

- **`GET /health/ready`** — Readiness probe
  - Returns `200 OK` when all dependencies are healthy
  - Returns `503 Service Unavailable` when any dependency fails
  - Checks: Redis connection, PostgreSQL connection, Redis Functions library loaded
  - Response (healthy): `{"status":"ready"}`
  - Response (unhealthy): `{"status":"unhealthy","failed_check":"redis","error":"connection timeout"}`

**Recommended polling intervals:**
- Liveness: every 10 seconds
- Readiness: every 5 seconds with 2 consecutive failures before marking unhealthy

---

## Kubernetes

Use `livenessProbe` and `readinessProbe` in your Pod spec:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: chatpulse
spec:
  replicas: 3
  selector:
    matchLabels:
      app: chatpulse
  template:
    metadata:
      labels:
        app: chatpulse
    spec:
      containers:
      - name: chatpulse
        image: chatpulse:latest
        ports:
        - containerPort: 8080
          name: http
        livenessProbe:
          httpGet:
            path: /health/live
            port: http
          initialDelaySeconds: 5
          periodSeconds: 10
          timeoutSeconds: 2
          failureThreshold: 3
        readinessProbe:
          httpGet:
            path: /health/ready
            port: http
          initialDelaySeconds: 10
          periodSeconds: 5
          timeoutSeconds: 5
          failureThreshold: 2
          successThreshold: 1
```

## AWS Application Load Balancer (ALB)

Configure health checks via Terraform:

```hcl
resource "aws_lb_target_group" "chatpulse" {
  name     = "chatpulse-tg"
  port     = 8080
  protocol = "HTTP"
  vpc_id   = var.vpc_id

  health_check {
    enabled             = true
    path                = "/health/ready"
    protocol            = "HTTP"
    port                = "traffic-port"
    interval            = 5
    timeout             = 5
    healthy_threshold   = 2
    unhealthy_threshold = 2
    matcher             = "200"
  }

  deregistration_delay = 30
}
```

## HAProxy

Configure backend health checks in `haproxy.cfg`:

```haproxy
backend chatpulse_servers
    mode http
    balance roundrobin
    option httpchk GET /health/ready HTTP/1.1\r\nHost:\ chatpulse.local
    http-check expect status 200

    default-server inter 5s fall 2 rise 2

    server chatpulse1 192.168.1.101:8080 check
    server chatpulse2 192.168.1.102:8080 check
    server chatpulse3 192.168.1.103:8080 check
```

## Docker Compose

Use the `healthcheck` directive:

```yaml
services:
  chatpulse:
    image: chatpulse:latest
    healthcheck:
      test: ["CMD", "wget", "--quiet", "--tries=1", "--spider", "http://localhost:8080/health/ready"]
      interval: 5s
      timeout: 5s
      retries: 2
      start_period: 10s
```

## Manual Testing

Test health endpoints with `curl`:

```bash
# Liveness (should always return 200)
curl -i http://localhost:8080/health/live

# Readiness (200 if healthy, 503 if dependencies down)
curl -i http://localhost:8080/health/ready
```

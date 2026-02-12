# Load Balancer Troubleshooting Guide

This guide covers common load balancer issues when running ChatPulse in production, organized by symptom with diagnostic steps and solutions.

## Quick Diagnostic Checklist

```bash
# 1. Check backend health
curl https://chatpulse.example.com/health/ready
curl https://chatpulse.example.com/health/live

# 2. Test WebSocket upgrade
websocat wss://chatpulse.example.com/ws/overlay/test-uuid

# 3. Check backend instances
# Nginx:
curl http://10.0.1.10:8080/health/ready
curl http://10.0.1.11:8080/health/ready

# Kubernetes:
kubectl get pods -n chatpulse
kubectl logs -n chatpulse -l app=chatpulse --tail=50

# AWS ALB:
aws elbv2 describe-target-health --target-group-arn arn:...

# 4. Check load balancer logs
# Nginx:
tail -f /var/log/nginx/error.log

# ALB:
aws s3 cp s3://my-alb-logs/... - | grep -i error

# Kubernetes:
kubectl logs -n ingress-nginx -l app.kubernetes.io/component=controller
```

## Symptom: WebSocket Connects Then Immediately Closes

### Diagnostic

```bash
# Test WebSocket connection with verbose output
websocat -v wss://chatpulse.example.com/ws/overlay/YOUR-UUID

# Expected: Connection upgrade successful, then data stream
# Actual: Connection closes immediately after handshake
```

### Cause 1: Missing WebSocket Upgrade Headers

**Nginx:**
```bash
# Check nginx config
grep -A 10 "location /ws/" /etc/nginx/conf.d/chatpulse.conf

# Should have:
# proxy_http_version 1.1;
# proxy_set_header Upgrade $http_upgrade;
# proxy_set_header Connection "upgrade";
```

**Fix:**
```nginx
location /ws/ {
    proxy_pass http://chatpulse_backend;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_buffering off;
}
```

**Kubernetes:**
```bash
# Check ingress annotations
kubectl get ingress -n chatpulse chatpulse -o yaml | grep -A 5 annotations

# Should have:
# nginx.ingress.kubernetes.io/proxy-read-timeout: "3600"
# nginx.ingress.kubernetes.io/configuration-snippet with Upgrade headers
```

**Fix:**
```yaml
metadata:
  annotations:
    nginx.ingress.kubernetes.io/configuration-snippet: |
      proxy_set_header Upgrade $http_upgrade;
      proxy_set_header Connection "upgrade";
      proxy_http_version 1.1;
```

### Cause 2: Backend Not Responding to Upgrade

**Check backend logs:**
```bash
# Look for upgrade errors
docker logs chatpulse | grep -i upgrade
kubectl logs -n chatpulse chatpulse-xxx | grep -i websocket
```

**Common backend issues:**
- Application crashed
- Port 8080 not listening
- Database/Redis connection failed (prevents server startup)

**Fix:**
```bash
# Restart backend
docker restart chatpulse
kubectl rollout restart deployment/chatpulse -n chatpulse
```

### Cause 3: Load Balancer Timeout Too Short

**Nginx:**
```bash
# Check timeout settings
grep proxy_read_timeout /etc/nginx/conf.d/chatpulse.conf

# Should be: proxy_read_timeout 3600s;
```

**ALB:**
```bash
# Check idle timeout
aws elbv2 describe-load-balancer-attributes \
  --load-balancer-arn arn:... | grep idle_timeout

# Should be: "idle_timeout.timeout_seconds": "3600"
```

**Kubernetes:**
```bash
# Check ingress timeout annotation
kubectl get ingress -n chatpulse chatpulse -o yaml | grep read-timeout

# Should have: nginx.ingress.kubernetes.io/proxy-read-timeout: "3600"
```

## Symptom: 502 Bad Gateway

### Cause 1: All Backends Unhealthy

**Nginx:**
```bash
# Test each backend directly
for ip in 10.0.1.10 10.0.1.11 10.0.1.12; do
    echo "Testing $ip:"
    curl -sf http://$ip:8080/health/ready || echo "FAIL"
done
```

**ALB:**
```bash
# Check target health
aws elbv2 describe-target-health --target-group-arn arn:...

# Look for:
# "TargetHealth": {
#   "State": "unhealthy",
#   "Reason": "Target.FailedHealthChecks"
# }
```

**Kubernetes:**
```bash
# Check pod status
kubectl get pods -n chatpulse

# Look for:
# NAME                        READY   STATUS    RESTARTS
# chatpulse-xxx-yyy           0/1     Running   3

# 0/1 means readiness probe failing
```

**Fix:**
```bash
# Check backend logs for errors
docker logs chatpulse --tail=100
kubectl logs -n chatpulse chatpulse-xxx --tail=100

# Common causes:
# - Database connection error (check DATABASE_URL)
# - Redis connection error (check REDIS_URL)
# - Missing environment variables
# - Port 8080 already in use
```

### Cause 2: Security Group Blocking Traffic (AWS)

**Check security groups:**
```bash
# Verify ALB can reach backend instances
aws ec2 describe-security-groups --group-ids sg-alb sg-backend

# ALB security group should allow outbound to backend port
# Backend security group should allow inbound from ALB security group
```

**Fix:**
```bash
# Allow ALB → Backend
aws ec2 authorize-security-group-ingress \
  --group-id sg-backend \
  --protocol tcp \
  --port 8080 \
  --source-group sg-alb
```

### Cause 3: Backend Crashed or Killed

**Check container status:**
```bash
# Docker
docker ps -a | grep chatpulse

# Kubernetes
kubectl describe pod -n chatpulse chatpulse-xxx

# Look for:
# Last State: Terminated
#   Reason: Error / OOMKilled / Completed
#   Exit Code: 137 (SIGKILL) or 1 (error)
```

**Fix for OOMKilled (Kubernetes):**
```yaml
resources:
  requests:
    memory: 128Mi
  limits:
    memory: 512Mi  # Increase this
```

### Cause 4: Health Check Grace Period Not Elapsed

**Kubernetes:**
```bash
# Check how long pod has been running
kubectl get pod -n chatpulse chatpulse-xxx -o yaml | grep startedAt

# If <30s and failing readiness, wait for initialDelaySeconds
```

**Fix:**
```yaml
readinessProbe:
  initialDelaySeconds: 10  # Increase if app takes longer to start
```

## Symptom: WebSocket Times Out After 60 Seconds

### Cause: Default Load Balancer Timeout

**Nginx:**
```nginx
# Missing or too short timeout in /ws/ location
location /ws/ {
    proxy_read_timeout 3600s;  # Add this
    proxy_send_timeout 3600s;  # Add this
}
```

**ALB:**
```hcl
# Terraform
resource "aws_lb" "main" {
  idle_timeout = 3600  # Default is 60
}
```

**Kubernetes (nginx-ingress):**
```yaml
metadata:
  annotations:
    nginx.ingress.kubernetes.io/proxy-read-timeout: "3600"
    nginx.ingress.kubernetes.io/proxy-send-timeout: "3600"
```

## Symptom: Uneven Load Distribution

### Diagnostic

```bash
# Check active connections per backend
for ip in 10.0.1.10 10.0.1.11 10.0.1.12; do
    echo "$ip: $(curl -s http://$ip:8080/metrics | grep websocket_connections_current | awk '{print $2}')"
done

# Expected: Roughly even (±20%)
# Actual: One instance has 80% of connections
```

### Cause: Round-Robin with Long-Lived Connections

**Nginx:**
```nginx
# Using default round_robin algorithm
upstream chatpulse_backend {
    server 10.0.1.10:8080;
    server 10.0.1.11:8080;
}
```

**Fix:**
```nginx
# Use least_conn for better distribution
upstream chatpulse_backend {
    least_conn;  # Add this
    server 10.0.1.10:8080;
    server 10.0.1.11:8080;
}
```

**ALB:**

ALB uses round-robin by default. Enable sticky sessions:

```hcl
resource "aws_lb_target_group" "backend" {
  stickiness {
    enabled         = true
    type            = "lb_cookie"
    cookie_duration = 86400
  }
}
```

**Kubernetes:**
```yaml
spec:
  sessionAffinity: ClientIP
  sessionAffinityConfig:
    clientIP:
      timeoutSeconds: 3600
```

## Symptom: SSL/TLS Errors

### Cause 1: Certificate Mismatch

**Error:** `curl: (60) SSL certificate problem: unable to get local issuer certificate`

**Check certificate:**
```bash
openssl s_client -connect chatpulse.example.com:443 -servername chatpulse.example.com

# Look for:
# subject=CN=chatpulse.example.com
# issuer=C=US, O=Let's Encrypt, CN=R3

# Common issues:
# - Certificate expired
# - Wrong domain in certificate
# - Self-signed certificate
```

**Fix (nginx):**
```nginx
ssl_certificate /etc/nginx/ssl/chatpulse.crt;
ssl_certificate_key /etc/nginx/ssl/chatpulse.key;

# Ensure certificate chain is complete
# cat domain.crt intermediate.crt > chatpulse.crt
```

**Fix (Kubernetes + cert-manager):**
```bash
# Check certificate status
kubectl describe certificate -n chatpulse chatpulse-tls

# If failed, check cert-manager logs
kubectl logs -n cert-manager -l app=cert-manager
```

### Cause 2: Outdated TLS Version

**Error:** `curl: (35) error:1408F10B:SSL routines:ssl3_get_record:wrong version number`

**Check TLS version:**
```bash
openssl s_client -connect chatpulse.example.com:443 -tls1_2
openssl s_client -connect chatpulse.example.com:443 -tls1_3
```

**Fix (nginx):**
```nginx
ssl_protocols TLSv1.2 TLSv1.3;  # Remove TLSv1.0 and TLSv1.1
```

**Fix (ALB):**
```hcl
resource "aws_lb_listener" "https" {
  ssl_policy = "ELBSecurityPolicy-TLS13-1-2-2021-06"
}
```

## Symptom: High Latency / Slow Responses

### Diagnostic

```bash
# Test response time
time curl https://chatpulse.example.com/health/ready

# Expected: <100ms
# Actual: >1000ms
```

### Cause 1: Backend Overloaded

**Check backend metrics:**
```bash
# CPU usage
docker stats chatpulse
kubectl top pod -n chatpulse chatpulse-xxx

# If >80%, scale up
```

**Fix (Kubernetes):**
```bash
# Scale manually
kubectl scale deployment/chatpulse -n chatpulse --replicas=6

# Or adjust HPA
kubectl edit hpa -n chatpulse chatpulse
```

**Fix (AWS):**
```bash
# Increase ASG desired capacity
aws autoscaling set-desired-capacity \
  --auto-scaling-group-name chatpulse-asg \
  --desired-capacity 6
```

### Cause 2: Database Connection Pool Exhausted

**Check logs:**
```bash
grep "connection pool exhausted" /var/log/chatpulse.log
```

**Fix:**
```bash
# Increase PostgreSQL max_connections
# Or increase pool size in application (default: pgxpool auto-sizes)
```

### Cause 3: Redis Latency

**Test Redis:**
```bash
redis-cli --latency -h redis.example.com

# If >10ms, investigate Redis performance
```

## Symptom: Connections Drop During Deployments

### Cause: No Graceful Shutdown

**Kubernetes:**
```yaml
spec:
  terminationGracePeriodSeconds: 60  # Increase from default 30

  containers:
  - lifecycle:
      preStop:
        exec:
          command: ["/bin/sh", "-c", "sleep 15"]  # Add this
```

**ALB:**
```hcl
deregistration_delay = 30  # Target group attribute
```

**Nginx:**

Before reloading config:
```bash
# Graceful reload (doesn't drop connections)
nginx -s reload

# NOT: systemctl restart nginx (drops connections)
```

## Symptom: Health Checks Failing but App Works

### Cause: Wrong Health Check Path

**Check health endpoint directly:**
```bash
curl -v http://backend-ip:8080/health/ready

# If returns 404, wrong path
# If returns 503, app not ready (database/redis down)
```

**Fix (nginx):**
```nginx
location /health/ {
    proxy_pass http://chatpulse_backend;
    access_log off;
}
```

**Fix (ALB):**
```hcl
health_check {
  path = "/health/ready"  # Ensure correct path
  matcher = "200"         # Expect HTTP 200
}
```

**Fix (Kubernetes):**
```yaml
readinessProbe:
  httpGet:
    path: /health/ready  # Ensure correct path
    port: 8080
```

## Symptom: WebSocket Reconnects Every Few Minutes

### Cause: Intermediate Proxy Timeout

Some corporate firewalls/proxies have short WebSocket timeouts.

**Workaround:**

Implement WebSocket ping/pong (already in `broadcast/writer.go`):

```go
// Ping every 30s to keep connection alive
ticker := time.NewTicker(30 * time.Second)
defer ticker.Stop()

for {
    select {
    case <-ticker.C:
        if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
            return
        }
    }
}
```

**Verify client receives pings:**
```javascript
// In overlay.html
ws.onping = () => console.log("Received ping");
```

## Monitoring & Alerts

### Key Metrics to Monitor

```promql
# Unhealthy backends
up{job="chatpulse"} == 0

# High error rate
rate(http_requests_total{status=~"5.."}[5m]) > 0.05

# Long response time
histogram_quantile(0.95, rate(http_request_duration_seconds_bucket[5m])) > 1

# Active WebSocket connections per instance
websocket_connections_current > 1000
```

### Recommended Alerts

```yaml
groups:
  - name: load-balancer
    rules:
      - alert: BackendUnhealthy
        expr: up{job="chatpulse"} == 0
        for: 1m
        annotations:
          summary: "Backend {{ $labels.instance }} is unhealthy"

      - alert: HighErrorRate
        expr: rate(http_requests_total{status=~"5.."}[5m]) > 0.05
        for: 5m
        annotations:
          summary: "High 5xx error rate: {{ $value }}"

      - alert: HighLatency
        expr: histogram_quantile(0.95, rate(http_request_duration_seconds_bucket[5m])) > 1
        for: 5m
        annotations:
          summary: "P95 latency > 1s: {{ $value }}s"
```

## Getting Help

If issues persist:

1. **Collect logs:**
```bash
# Backend logs
docker logs chatpulse > backend.log
kubectl logs -n chatpulse chatpulse-xxx > backend.log

# Load balancer logs
tail -n 1000 /var/log/nginx/error.log > lb.log
```

2. **Collect metrics:**
```bash
curl http://backend-ip:8080/metrics > metrics.txt
```

3. **Collect configuration:**
```bash
# Nginx
nginx -T > nginx-config.txt

# Kubernetes
kubectl get all -n chatpulse -o yaml > k8s-config.yaml
```

4. **Open GitHub issue** with logs, metrics, and config attached.

## See Also

- [Load Balancer - Nginx](load-balancer-nginx.md)
- [Load Balancer - AWS ALB](load-balancer-aws-alb.md)
- [Load Balancer - Kubernetes](load-balancer-kubernetes.md)
- [Graceful Shutdown Guide](graceful-shutdown.md)

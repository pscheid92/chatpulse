# Load Balancer Configuration - Kubernetes

This guide shows how to deploy ChatPulse on Kubernetes with ingress controllers, including WebSocket support, health checks, and horizontal pod autoscaling.

## Architecture

```
Internet → Ingress Controller → Service → Pods (3+ replicas on :8080)
                                   ↓
                           Health Checks (liveness + readiness)
```

## Prerequisites

- Kubernetes cluster (1.25+)
- kubectl configured
- Ingress controller installed (nginx-ingress, AWS ALB Ingress, or Traefik)
- Valid SSL certificate (cert-manager or external)
- PostgreSQL and Redis (in-cluster or external)

## Complete Kubernetes Manifests

### Namespace

```yaml
# k8s/namespace.yaml
apiVersion: v1
kind: Namespace
metadata:
  name: chatpulse
  labels:
    app: chatpulse
```

### ConfigMap for Configuration

```yaml
# k8s/configmap.yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: chatpulse-config
  namespace: chatpulse
data:
  APP_ENV: "production"
  TWITCH_REDIRECT_URI: "https://chatpulse.example.com/auth/callback"
  WEBHOOK_CALLBACK_URL: "https://chatpulse.example.com/webhooks/eventsub"
```

### Secret for Sensitive Data

```yaml
# k8s/secret.yaml
apiVersion: v1
kind: Secret
metadata:
  name: chatpulse-secrets
  namespace: chatpulse
type: Opaque
stringData:
  DATABASE_URL: "postgres://user:pass@postgres.chatpulse.svc.cluster.local:5432/chatpulse?sslmode=require"
  REDIS_URL: "redis://redis.chatpulse.svc.cluster.local:6379"
  TWITCH_CLIENT_ID: "your-client-id"
  TWITCH_CLIENT_SECRET: "your-client-secret"
  SESSION_SECRET: "your-session-secret-64-hex-chars"
  WEBHOOK_SECRET: "your-webhook-secret-32-chars"
  BOT_USER_ID: "123456789"
  TOKEN_ENCRYPTION_KEY: "your-encryption-key-64-hex-chars"
```

**Note**: In production, use external secret management (AWS Secrets Manager, HashiCorp Vault, or sealed-secrets).

### Deployment

```yaml
# k8s/deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: chatpulse
  namespace: chatpulse
  labels:
    app: chatpulse
spec:
  replicas: 3  # Minimum for high availability

  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxSurge: 1
      maxUnavailable: 0  # Zero-downtime deployments

  selector:
    matchLabels:
      app: chatpulse

  template:
    metadata:
      labels:
        app: chatpulse
      annotations:
        prometheus.io/scrape: "true"
        prometheus.io/port: "8080"
        prometheus.io/path: "/metrics"

    spec:
      # Graceful shutdown
      terminationGracePeriodSeconds: 60

      containers:
      - name: chatpulse
        image: ghcr.io/pscheid92/chatpulse:latest
        imagePullPolicy: Always

        ports:
        - name: http
          containerPort: 8080
          protocol: TCP

        # Environment variables from ConfigMap
        envFrom:
        - configMapRef:
            name: chatpulse-config
        - secretRef:
            name: chatpulse-secrets

        # Resource limits
        resources:
          requests:
            cpu: 100m
            memory: 128Mi
          limits:
            cpu: 500m
            memory: 512Mi

        # Startup probe (initial application initialization)
        startupProbe:
          httpGet:
            path: /health/startup
            port: http
          initialDelaySeconds: 0
          periodSeconds: 5
          timeoutSeconds: 2
          successThreshold: 1
          failureThreshold: 12  # 12 * 5s = 60s max startup time

        # Liveness probe (restart if unhealthy)
        livenessProbe:
          httpGet:
            path: /health/live
            port: http
          initialDelaySeconds: 0
          periodSeconds: 10
          timeoutSeconds: 1
          successThreshold: 1
          failureThreshold: 3

        # Readiness probe (remove from service if not ready)
        readinessProbe:
          httpGet:
            path: /health/ready
            port: http
          initialDelaySeconds: 0
          periodSeconds: 5
          timeoutSeconds: 5
          successThreshold: 1
          failureThreshold: 2

        # Graceful shutdown hook
        lifecycle:
          preStop:
            exec:
              command: ["/bin/sh", "-c", "sleep 15"]

      # Anti-affinity (spread pods across nodes)
      affinity:
        podAntiAffinity:
          preferredDuringSchedulingIgnoredDuringExecution:
          - weight: 100
            podAffinityTerm:
              labelSelector:
                matchExpressions:
                - key: app
                  operator: In
                  values:
                  - chatpulse
              topologyKey: kubernetes.io/hostname
```

### Service

```yaml
# k8s/service.yaml
apiVersion: v1
kind: Service
metadata:
  name: chatpulse
  namespace: chatpulse
  labels:
    app: chatpulse
spec:
  type: ClusterIP
  selector:
    app: chatpulse
  ports:
  - name: http
    port: 80
    targetPort: http
    protocol: TCP
  sessionAffinity: ClientIP  # Sticky sessions for WebSocket
  sessionAffinityConfig:
    clientIP:
      timeoutSeconds: 3600  # 1 hour
```

### Ingress (nginx-ingress)

```yaml
# k8s/ingress-nginx.yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: chatpulse
  namespace: chatpulse
  annotations:
    # Use nginx ingress controller
    kubernetes.io/ingress.class: "nginx"

    # SSL/TLS
    cert-manager.io/cluster-issuer: "letsencrypt-prod"

    # WebSocket support
    nginx.ingress.kubernetes.io/proxy-read-timeout: "3600"
    nginx.ingress.kubernetes.io/proxy-send-timeout: "3600"
    nginx.ingress.kubernetes.io/proxy-connect-timeout: "10"

    # WebSocket upgrade headers
    nginx.ingress.kubernetes.io/configuration-snippet: |
      proxy_set_header Upgrade $http_upgrade;
      proxy_set_header Connection "upgrade";
      proxy_http_version 1.1;

    # Sticky sessions
    nginx.ingress.kubernetes.io/affinity: "cookie"
    nginx.ingress.kubernetes.io/session-cookie-name: "chatpulse-route"
    nginx.ingress.kubernetes.io/session-cookie-max-age: "3600"

    # Rate limiting (optional)
    nginx.ingress.kubernetes.io/limit-rps: "100"

spec:
  tls:
  - hosts:
    - chatpulse.example.com
    secretName: chatpulse-tls

  rules:
  - host: chatpulse.example.com
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: chatpulse
            port:
              name: http
```

### Ingress (AWS ALB Ingress Controller)

```yaml
# k8s/ingress-alb.yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: chatpulse
  namespace: chatpulse
  annotations:
    # Use AWS ALB Ingress Controller
    kubernetes.io/ingress.class: "alb"

    # ALB configuration
    alb.ingress.kubernetes.io/scheme: "internet-facing"
    alb.ingress.kubernetes.io/target-type: "ip"

    # SSL/TLS
    alb.ingress.kubernetes.io/certificate-arn: "arn:aws:acm:us-east-1:123456789012:certificate/..."
    alb.ingress.kubernetes.io/listen-ports: '[{"HTTP": 80}, {"HTTPS": 443}]'
    alb.ingress.kubernetes.io/ssl-redirect: "443"

    # Health check
    alb.ingress.kubernetes.io/healthcheck-path: "/health/ready"
    alb.ingress.kubernetes.io/healthcheck-interval-seconds: "30"
    alb.ingress.kubernetes.io/healthcheck-timeout-seconds: "5"
    alb.ingress.kubernetes.io/healthy-threshold-count: "2"
    alb.ingress.kubernetes.io/unhealthy-threshold-count: "3"

    # Sticky sessions
    alb.ingress.kubernetes.io/target-group-attributes: "stickiness.enabled=true,stickiness.lb_cookie.duration_seconds=3600"

    # WebSocket idle timeout (1 hour)
    alb.ingress.kubernetes.io/load-balancer-attributes: "idle_timeout.timeout_seconds=3600"

spec:
  rules:
  - host: chatpulse.example.com
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: chatpulse
            port:
              name: http
```

### HorizontalPodAutoscaler

```yaml
# k8s/hpa.yaml
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: chatpulse
  namespace: chatpulse
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: chatpulse

  minReplicas: 3
  maxReplicas: 10

  metrics:
  # CPU-based scaling
  - type: Resource
    resource:
      name: cpu
      target:
        type: Utilization
        averageUtilization: 70

  # Memory-based scaling
  - type: Resource
    resource:
      name: memory
      target:
        type: Utilization
        averageUtilization: 80

  behavior:
    scaleDown:
      stabilizationWindowSeconds: 300
      policies:
      - type: Percent
        value: 50
        periodSeconds: 60
    scaleUp:
      stabilizationWindowSeconds: 60
      policies:
      - type: Percent
        value: 100
        periodSeconds: 60
```

### PodDisruptionBudget

```yaml
# k8s/pdb.yaml
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: chatpulse
  namespace: chatpulse
spec:
  minAvailable: 2
  selector:
    matchLabels:
      app: chatpulse
```

### ServiceMonitor (Prometheus Operator)

```yaml
# k8s/servicemonitor.yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: chatpulse
  namespace: chatpulse
  labels:
    app: chatpulse
spec:
  selector:
    matchLabels:
      app: chatpulse
  endpoints:
  - port: http
    path: /metrics
    interval: 30s
```

## Deployment

### 1. Apply Manifests

```bash
kubectl apply -f k8s/namespace.yaml
kubectl apply -f k8s/configmap.yaml
kubectl apply -f k8s/secret.yaml
kubectl apply -f k8s/deployment.yaml
kubectl apply -f k8s/service.yaml
kubectl apply -f k8s/ingress-nginx.yaml  # or ingress-alb.yaml
kubectl apply -f k8s/hpa.yaml
kubectl apply -f k8s/pdb.yaml
kubectl apply -f k8s/servicemonitor.yaml
```

### 2. Verify Deployment

```bash
# Check pods
kubectl get pods -n chatpulse

# Check service
kubectl get svc -n chatpulse

# Check ingress
kubectl get ingress -n chatpulse

# Check HPA
kubectl get hpa -n chatpulse

# View pod logs
kubectl logs -n chatpulse -l app=chatpulse --tail=50 -f
```

### 3. Test Health Checks

```bash
# Port-forward to test locally
kubectl port-forward -n chatpulse svc/chatpulse 8080:80

# Test health endpoints
curl http://localhost:8080/health/live
curl http://localhost:8080/health/ready

# Test overlay endpoint
curl http://localhost:8080/overlay/test-uuid
```

### 4. Test WebSocket Connection

```bash
# Get ingress URL
INGRESS_URL=$(kubectl get ingress -n chatpulse chatpulse -o jsonpath='{.status.loadBalancer.ingress[0].hostname}')

# Test WebSocket
websocat wss://$INGRESS_URL/ws/overlay/YOUR-UUID-HERE
```

## WebSocket Support

### nginx-ingress

WebSocket support is enabled via annotations:
- `nginx.ingress.kubernetes.io/proxy-read-timeout: "3600"` — Long-lived connections
- `proxy_set_header Upgrade $http_upgrade` — WebSocket upgrade header
- `proxy_http_version 1.1` — Required for WebSocket

### AWS ALB Ingress

ALB automatically supports WebSocket:
- Set `idle_timeout.timeout_seconds=3600` for long connections
- No special headers needed (ALB detects `Upgrade: websocket` automatically)

### Traefik

```yaml
apiVersion: traefik.containo.us/v1alpha1
kind: IngressRoute
metadata:
  name: chatpulse
  namespace: chatpulse
spec:
  entryPoints:
    - websecure
  routes:
  - match: Host(`chatpulse.example.com`)
    kind: Rule
    services:
    - name: chatpulse
      port: 80
  tls:
    secretName: chatpulse-tls
```

## Monitoring

### View Logs

```bash
# All pods
kubectl logs -n chatpulse -l app=chatpulse --tail=100 -f

# Specific pod
kubectl logs -n chatpulse chatpulse-abc123-xyz -f

# Previous container (after crash)
kubectl logs -n chatpulse chatpulse-abc123-xyz --previous
```

### Check Metrics

```bash
# HPA status
kubectl get hpa -n chatpulse chatpulse -w

# Pod resource usage
kubectl top pods -n chatpulse

# Node resource usage
kubectl top nodes
```

### Prometheus Queries

```promql
# Active WebSocket connections
sum(websocket_connections_current{namespace="chatpulse"})

# Request rate
rate(http_requests_total{namespace="chatpulse"}[5m])

# Error rate
rate(http_requests_total{namespace="chatpulse", status=~"5.."}[5m])

# P95 latency
histogram_quantile(0.95, rate(http_request_duration_seconds_bucket{namespace="chatpulse"}[5m]))
```

## Troubleshooting

### Pods Not Starting

```bash
# Describe pod
kubectl describe pod -n chatpulse chatpulse-abc123-xyz

# Common causes:
# 1. ImagePullBackOff — image not found or auth issue
# 2. CrashLoopBackOff — app crashes on startup
# 3. Pending — insufficient resources or node affinity issues
```

### Health Check Failures

```bash
# Test health endpoint inside pod
kubectl exec -n chatpulse chatpulse-abc123-xyz -- curl -sf http://localhost:8080/health/ready

# Common causes:
# 1. Database connection failing
# 2. Redis connection failing
# 3. Application not listening on port 8080
# 4. Readiness probe timing too aggressive
```

### WebSocket Not Connecting

```bash
# Check ingress controller logs
kubectl logs -n ingress-nginx -l app.kubernetes.io/component=controller --tail=100 -f

# Verify ingress annotations
kubectl get ingress -n chatpulse chatpulse -o yaml

# Common issues:
# 1. Missing proxy-read-timeout annotation
# 2. Missing Upgrade/Connection headers
# 3. Certificate issues (wss:// requires valid cert)
```

### HPA Not Scaling

```bash
# Check HPA status
kubectl describe hpa -n chatpulse chatpulse

# Common causes:
# 1. Metrics server not installed
# 2. Resource requests not defined
# 3. Current usage below threshold
# 4. Stabilization window preventing scale
```

## Production Checklist

- [ ] SSL/TLS certificates configured (cert-manager or external)
- [ ] Ingress controller installed and configured
- [ ] Health checks (liveness + readiness) tested
- [ ] Resource requests and limits set
- [ ] HPA configured and tested
- [ ] PodDisruptionBudget configured (minAvailable: 2)
- [ ] Anti-affinity configured (spread across nodes)
- [ ] Secrets stored securely (external secret manager)
- [ ] Prometheus metrics enabled
- [ ] Logging centralized (Fluentd/Fluentbit → ELK/Loki)
- [ ] WebSocket timeout set to 3600s
- [ ] Sticky sessions enabled
- [ ] Zero-downtime deployments tested (rolling update)
- [ ] Graceful shutdown tested (terminationGracePeriodSeconds: 60)

## Helm Chart (Optional)

For easier deployment, consider creating a Helm chart:

```bash
helm create chatpulse
# Edit values.yaml with configurable parameters
helm install chatpulse ./chatpulse -n chatpulse
```

## See Also

- [Nginx Configuration](load-balancer-nginx.md)
- [AWS ALB Configuration](load-balancer-aws-alb.md)
- [Graceful Shutdown Guide](graceful-shutdown.md)
- [Troubleshooting Guide](troubleshooting-load-balancer.md)

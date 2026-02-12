# Load Balancer Configuration - Nginx

This guide shows how to configure Nginx as a reverse proxy and load balancer for ChatPulse, including WebSocket support and health checks.

## Architecture

```
Internet → Nginx (443) → Backend Pool (3+ instances on :8080)
                       ↓
                   Health Checks (/health/ready every 10s)
```

## Complete Nginx Configuration

### Main Configuration

```nginx
# /etc/nginx/nginx.conf
user nginx;
worker_processes auto;
error_log /var/log/nginx/error.log warn;
pid /var/run/nginx.pid;

events {
    worker_connections 4096;  # Increase for many WebSocket connections
    use epoll;                # Linux-specific optimization
}

http {
    include /etc/nginx/mime.types;
    default_type application/octet-stream;

    # Logging
    log_format main '$remote_addr - $remote_user [$time_local] "$request" '
                    '$status $body_bytes_sent "$http_referer" '
                    '"$http_user_agent" "$http_x_forwarded_for"';
    access_log /var/log/nginx/access.log main;

    # Performance
    sendfile on;
    tcp_nopush on;
    tcp_nodelay on;
    keepalive_timeout 65;
    types_hash_max_size 2048;

    # Gzip compression
    gzip on;
    gzip_vary on;
    gzip_types text/plain text/css application/json application/javascript text/xml application/xml;

    # Include site configurations
    include /etc/nginx/conf.d/*.conf;
}
```

### Site Configuration

```nginx
# /etc/nginx/conf.d/chatpulse.conf

# Upstream backend pool
upstream chatpulse_backend {
    # Use least_conn for better distribution of long-lived WebSocket connections
    # This sends new connections to the instance with fewest active connections
    least_conn;
    
    # Backend instances
    # Adjust IPs to match your infrastructure
    server 10.0.1.10:8080 max_fails=3 fail_timeout=30s weight=1;
    server 10.0.1.11:8080 max_fails=3 fail_timeout=30s weight=1;
    server 10.0.1.12:8080 max_fails=3 fail_timeout=30s weight=1;
    
    # Passive health checks:
    # - After 3 consecutive failures, mark server as down
    # - Keep it down for 30 seconds before retrying
    # - Active health checks require nginx-plus or third-party module
    
    # Connection keepalive to backend (improves performance)
    keepalive 32;
}

# HTTP → HTTPS redirect
server {
    listen 80;
    listen [::]:80;
    server_name chatpulse.example.com;
    
    # Allow health checks over HTTP (for internal monitoring)
    location /health/ {
        proxy_pass http://chatpulse_backend;
        access_log off;
    }
    
    # Redirect everything else to HTTPS
    location / {
        return 301 https://$host$request_uri;
    }
}

# HTTPS server
server {
    listen 443 ssl http2;
    listen [::]:443 ssl http2;
    server_name chatpulse.example.com;
    
    # SSL certificate (use Let's Encrypt or your CA)
    ssl_certificate /etc/nginx/ssl/chatpulse.crt;
    ssl_certificate_key /etc/nginx/ssl/chatpulse.key;
    
    # SSL configuration (Mozilla Intermediate compatibility)
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers 'ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES128-GCM-SHA256:ECDHE-ECDSA-AES256-GCM-SHA384:ECDHE-RSA-AES256-GCM-SHA384';
    ssl_prefer_server_ciphers off;
    
    # SSL session caching
    ssl_session_cache shared:SSL:10m;
    ssl_session_timeout 10m;
    
    # HSTS (optional, uncomment for production)
    # add_header Strict-Transport-Security "max-age=31536000; includeSubDomains" always;
    
    # WebSocket endpoints (/ws/*)
    location /ws/ {
        proxy_pass http://chatpulse_backend;
        
        # WebSocket upgrade headers (REQUIRED)
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        
        # Preserve client information
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Host $host;
        
        # Timeouts for long-lived WebSocket connections
        # OBS overlays stay open for hours
        proxy_read_timeout 3600s;    # 1 hour
        proxy_send_timeout 3600s;    # 1 hour
        proxy_connect_timeout 10s;   # Initial connection
        
        # Disable buffering for WebSocket (REQUIRED)
        proxy_buffering off;
        
        # Disable request/response buffering
        proxy_request_buffering off;
        proxy_http_version 1.1;
    }
    
    # Overlay pages (/overlay/*)
    location /overlay/ {
        proxy_pass http://chatpulse_backend;
        
        # Standard proxy headers
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Host $host;
        
        # Normal HTTP timeouts
        proxy_read_timeout 30s;
        proxy_send_timeout 30s;
        proxy_connect_timeout 5s;
    }
    
    # REST API and web pages (/)
    location / {
        proxy_pass http://chatpulse_backend;
        
        # Standard proxy headers
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Host $host;
        
        # Normal HTTP timeouts
        proxy_read_timeout 30s;
        proxy_send_timeout 30s;
        proxy_connect_timeout 5s;
        
        # Enable keepalive to backend
        proxy_http_version 1.1;
        proxy_set_header Connection "";
    }
    
    # Health check endpoint (internal monitoring)
    location /health/ {
        proxy_pass http://chatpulse_backend;
        access_log off;  # Don't log health checks (reduces noise)
        
        # Short timeout for health checks
        proxy_read_timeout 5s;
        proxy_connect_timeout 2s;
    }
    
    # Metrics endpoint (Prometheus scraping)
    location /metrics {
        proxy_pass http://chatpulse_backend;
        access_log off;  # Don't log metrics scrapes
        
        # Optional: Restrict to internal IPs only
        # allow 10.0.0.0/8;
        # deny all;
    }
}
```

## Health Check Monitoring

Nginx open-source doesn't support active health checks. Use one of these approaches:

### Option 1: External Monitoring (Recommended)

Use Prometheus blackbox_exporter to actively probe health endpoints:

```yaml
# prometheus.yml
scrape_configs:
  - job_name: 'chatpulse-health'
    metrics_path: /probe
    params:
      module: [http_2xx]
    static_configs:
      - targets:
        - http://10.0.1.10:8080/health/ready
        - http://10.0.1.11:8080/health/ready
        - http://10.0.1.12:8080/health/ready
    relabel_configs:
      - source_labels: [__address__]
        target_label: __param_target
      - source_labels: [__param_target]
        target_label: instance
      - target_label: __address__
        replacement: blackbox-exporter:9115
```

### Option 2: Nginx Plus (Commercial)

```nginx
upstream chatpulse_backend {
    zone chatpulse_zone 64k;
    
    server 10.0.1.10:8080;
    server 10.0.1.11:8080;
    server 10.0.1.12:8080;
    
    # Active health checks (nginx-plus only)
    health_check interval=10s fails=2 passes=2 uri=/health/ready;
}
```

### Option 3: Third-party Module

Install `nginx_upstream_check_module` (requires recompiling Nginx):

```nginx
upstream chatpulse_backend {
    server 10.0.1.10:8080;
    server 10.0.1.11:8080;
    server 10.0.1.12:8080;
    
    check interval=10000 rise=2 fall=3 timeout=5000 type=http;
    check_http_send "GET /health/ready HTTP/1.0\r\n\r\n";
    check_http_expect_alive http_2xx;
}
```

## Testing the Configuration

### 1. Validate Nginx Config

```bash
sudo nginx -t
```

### 2. Reload Nginx

```bash
sudo nginx -s reload
```

### 3. Test WebSocket Connection

```bash
# Install websocat: https://github.com/vi/websocat
websocat wss://chatpulse.example.com/ws/overlay/YOUR-UUID-HERE
```

You should see sentiment updates every 50ms.

### 4. Test Health Checks

```bash
curl https://chatpulse.example.com/health/ready
# Should return: {"status":"ready"}

curl https://chatpulse.example.com/health/live
# Should return: {"status":"ok"}
```

### 5. Test Load Distribution

```bash
# Open 100 WebSocket connections
for i in {1..100}; do
    websocat wss://chatpulse.example.com/ws/overlay/YOUR-UUID &
done

# Check distribution across backends
for ip in 10.0.1.10 10.0.1.11 10.0.1.12; do
    echo "$ip: $(curl -s http://$ip:8080/metrics | grep websocket_connections_current | awk '{print $2}')"
done
```

## Monitoring & Alerts

### Prometheus Alerts

```yaml
groups:
  - name: nginx-chatpulse
    rules:
      - alert: NginxHighErrorRate
        expr: rate(nginx_http_requests_total{status=~"5.."}[5m]) > 0.05
        for: 5m
        annotations:
          summary: "High 5xx error rate from Nginx"
      
      - alert: BackendUnhealthy
        expr: up{job="chatpulse-health"} == 0
        for: 1m
        annotations:
          summary: "ChatPulse backend {{ $labels.instance }} is unhealthy"
```

### Nginx Logs

Monitor for WebSocket upgrade failures:

```bash
tail -f /var/log/nginx/error.log | grep -i upgrade
```

Common errors:
- "upstream prematurely closed connection" → Backend crashed
- "no live upstreams" → All backends are down
- "upstream timed out" → Backend timeout (increase proxy_read_timeout)

## Performance Tuning

### For High WebSocket Load

```nginx
# nginx.conf (main context)
worker_processes auto;  # One per CPU core

events {
    worker_connections 8192;  # Increase from default 1024
    use epoll;                # Linux-specific
    multi_accept on;          # Accept multiple connections at once
}

http {
    # Keep-alive settings
    keepalive_timeout 65;
    keepalive_requests 1000;
    
    # Upstream keepalive pool
    upstream chatpulse_backend {
        least_conn;
        server 10.0.1.10:8080;
        server 10.0.1.11:8080;
        server 10.0.1.12:8080;
        
        keepalive 128;  # Maintain 128 idle connections to each backend
    }
}
```

### System Limits

```bash
# /etc/security/limits.conf
nginx soft nofile 65536
nginx hard nofile 65536

# /etc/sysctl.conf
net.core.somaxconn = 4096
net.ipv4.tcp_max_syn_backlog = 4096
```

## Troubleshooting

| Symptom | Cause | Solution |
|---------|-------|----------|
| WebSocket connects then immediately closes | Missing `Upgrade` header | Verify `proxy_set_header Upgrade $http_upgrade` |
| 502 Bad Gateway | All backends down | Check health endpoints, verify backends running |
| Connections timeout after 60s | Default nginx timeout | Increase `proxy_read_timeout` to 3600s |
| Uneven load distribution | Using `round_robin` algorithm | Switch to `least_conn` for long-lived connections |
| SSL handshake errors | Outdated TLS config | Use Mozilla SSL Config Generator |

## Production Checklist

- [ ] SSL/TLS configured with valid certificates
- [ ] Health checks monitored via external system
- [ ] `least_conn` load balancing for WebSocket
- [ ] `proxy_read_timeout` set to 3600s for WebSocket
- [ ] `proxy_buffering off` for WebSocket
- [ ] Worker connections increased (8192+)
- [ ] Logs monitored for errors
- [ ] Prometheus alerts configured
- [ ] Graceful reload tested (`nginx -s reload`)
- [ ] Failover tested (kill one backend, verify no drops)

## See Also

- [AWS ALB Configuration](load-balancer-aws-alb.md)
- [Kubernetes Configuration](load-balancer-kubernetes.md)
- [Graceful Shutdown Guide](graceful-shutdown.md)
- [Troubleshooting Guide](troubleshooting-load-balancer.md)

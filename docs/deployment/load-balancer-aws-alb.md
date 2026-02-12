# Load Balancer Configuration - AWS Application Load Balancer

This guide shows how to deploy ChatPulse behind an AWS Application Load Balancer (ALB) with Terraform, including WebSocket support, health checks, and auto-scaling integration.

## Architecture

```
Internet → ALB (443) → Target Group → ECS/EC2 instances (3+ on :8080)
                            ↓
                    Health Checks (/health/ready every 30s)
```

## Prerequisites

- AWS Account with appropriate permissions (EC2, ELB, VPC, IAM)
- Terraform 1.5+
- Valid SSL certificate in AWS Certificate Manager (ACM)
- VPC with public and private subnets

## Complete Terraform Configuration

### Main Configuration

```hcl
# terraform/main.tf

terraform {
  required_version = ">= 1.5"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

provider "aws" {
  region = var.aws_region
}

# Variables
variable "aws_region" {
  description = "AWS region"
  type        = string
  default     = "us-east-1"
}

variable "vpc_id" {
  description = "VPC ID for ALB and target group"
  type        = string
}

variable "public_subnet_ids" {
  description = "Public subnet IDs for ALB"
  type        = list(string)
}

variable "private_subnet_ids" {
  description = "Private subnet IDs for instances"
  type        = list(string)
}

variable "certificate_arn" {
  description = "ACM certificate ARN for HTTPS"
  type        = string
}

variable "domain_name" {
  description = "Domain name for Route 53 record"
  type        = string
  default     = "chatpulse.example.com"
}

variable "app_port" {
  description = "Application port"
  type        = number
  default     = 8080
}

variable "min_instances" {
  description = "Minimum number of instances"
  type        = number
  default     = 3
}

variable "max_instances" {
  description = "Maximum number of instances"
  type        = number
  default     = 10
}
```

### Security Groups

```hcl
# terraform/security_groups.tf

# ALB Security Group
resource "aws_security_group" "alb" {
  name_prefix = "chatpulse-alb-"
  description = "Security group for ChatPulse ALB"
  vpc_id      = var.vpc_id

  # Allow inbound HTTP (redirect to HTTPS)
  ingress {
    description = "HTTP from internet"
    from_port   = 80
    to_port     = 80
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  # Allow inbound HTTPS
  ingress {
    description = "HTTPS from internet"
    from_port   = 443
    to_port     = 443
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  # Allow all outbound to backend instances
  egress {
    description = "To backend instances"
    from_port   = var.app_port
    to_port     = var.app_port
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  lifecycle {
    create_before_destroy = true
  }

  tags = {
    Name = "chatpulse-alb"
  }
}

# Backend Instance Security Group
resource "aws_security_group" "backend" {
  name_prefix = "chatpulse-backend-"
  description = "Security group for ChatPulse backend instances"
  vpc_id      = var.vpc_id

  # Allow inbound from ALB only
  ingress {
    description     = "From ALB"
    from_port       = var.app_port
    to_port         = var.app_port
    protocol        = "tcp"
    security_groups = [aws_security_group.alb.id]
  }

  # Allow all outbound (for database, Redis, Twitch API)
  egress {
    description = "All outbound"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  lifecycle {
    create_before_destroy = true
  }

  tags = {
    Name = "chatpulse-backend"
  }
}
```

### Application Load Balancer

```hcl
# terraform/alb.tf

resource "aws_lb" "main" {
  name               = "chatpulse-alb"
  internal           = false
  load_balancer_type = "application"
  security_groups    = [aws_security_group.alb.id]
  subnets            = var.public_subnet_ids

  # Enable deletion protection in production
  enable_deletion_protection = false

  # Access logs (optional, requires S3 bucket)
  # access_logs {
  #   bucket  = aws_s3_bucket.alb_logs.bucket
  #   prefix  = "chatpulse-alb"
  #   enabled = true
  # }

  # Connection settings for long-lived WebSocket connections
  idle_timeout = 3600  # 1 hour (matches nginx proxy_read_timeout)

  tags = {
    Name = "chatpulse-alb"
  }
}

# Target Group
resource "aws_lb_target_group" "backend" {
  name_prefix = "cp-"
  port        = var.app_port
  protocol    = "HTTP"
  vpc_id      = var.vpc_id
  target_type = "instance"  # Use "ip" for Fargate

  # Health check configuration
  health_check {
    enabled             = true
    path                = "/health/ready"
    protocol            = "HTTP"
    port                = "traffic-port"
    healthy_threshold   = 2
    unhealthy_threshold = 3
    timeout             = 5
    interval            = 30
    matcher             = "200"  # Expect HTTP 200 OK
  }

  # Deregistration delay (graceful shutdown)
  deregistration_delay = 30

  # Stickiness for WebSocket connections (optional)
  # ALB already supports WebSocket upgrade, but stickiness
  # ensures overlay clients reconnect to the same instance
  stickiness {
    enabled         = true
    type            = "lb_cookie"
    cookie_duration = 86400  # 24 hours
  }

  lifecycle {
    create_before_destroy = true
  }

  tags = {
    Name = "chatpulse-backend"
  }
}

# HTTP Listener (redirect to HTTPS)
resource "aws_lb_listener" "http" {
  load_balancer_arn = aws_lb.main.arn
  port              = "80"
  protocol          = "HTTP"

  default_action {
    type = "redirect"

    redirect {
      port        = "443"
      protocol    = "HTTPS"
      status_code = "HTTP_301"
    }
  }
}

# HTTPS Listener
resource "aws_lb_listener" "https" {
  load_balancer_arn = aws_lb.main.arn
  port              = "443"
  protocol          = "HTTPS"
  ssl_policy        = "ELBSecurityPolicy-TLS13-1-2-2021-06"  # TLS 1.2 and 1.3
  certificate_arn   = var.certificate_arn

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.backend.arn
  }
}

# Outputs
output "alb_dns_name" {
  description = "ALB DNS name"
  value       = aws_lb.main.dns_name
}

output "alb_zone_id" {
  description = "ALB hosted zone ID"
  value       = aws_lb.main.zone_id
}

output "target_group_arn" {
  description = "Target group ARN"
  value       = aws_lb_target_group.backend.arn
}
```

### Route 53 DNS Record

```hcl
# terraform/route53.tf

data "aws_route53_zone" "main" {
  name         = var.domain_name
  private_zone = false
}

resource "aws_route53_record" "chatpulse" {
  zone_id = data.aws_route53_zone.main.zone_id
  name    = var.domain_name
  type    = "A"

  alias {
    name                   = aws_lb.main.dns_name
    zone_id                = aws_lb.main.zone_id
    evaluate_target_health = true
  }
}
```

### Auto Scaling (EC2 Launch Template + ASG)

```hcl
# terraform/autoscaling.tf

# Launch Template
resource "aws_launch_template" "backend" {
  name_prefix   = "chatpulse-"
  image_id      = data.aws_ami.amazon_linux_2023.id  # Or your custom AMI
  instance_type = "t3.medium"

  iam_instance_profile {
    name = aws_iam_instance_profile.backend.name
  }

  vpc_security_group_ids = [aws_security_group.backend.id]

  # User data script to start the application
  user_data = base64encode(templatefile("${path.module}/user_data.sh", {
    database_url     = var.database_url
    redis_url        = var.redis_url
    twitch_client_id = var.twitch_client_id
    # ... other environment variables
  }))

  tag_specifications {
    resource_type = "instance"
    tags = {
      Name = "chatpulse-backend"
    }
  }
}

# Auto Scaling Group
resource "aws_autoscaling_group" "backend" {
  name_prefix         = "chatpulse-"
  vpc_zone_identifier = var.private_subnet_ids
  target_group_arns   = [aws_lb_target_group.backend.arn]
  health_check_type   = "ELB"
  health_check_grace_period = 300

  min_size         = var.min_instances
  max_size         = var.max_instances
  desired_capacity = var.min_instances

  launch_template {
    id      = aws_launch_template.backend.id
    version = "$Latest"
  }

  # Termination policies
  termination_policies = ["OldestInstance"]

  # Instance refresh for zero-downtime deployments
  instance_refresh {
    strategy = "Rolling"
    preferences {
      min_healthy_percentage = 50
    }
  }

  tag {
    key                 = "Name"
    value               = "chatpulse-backend"
    propagate_at_launch = true
  }
}

# Auto Scaling Policies
resource "aws_autoscaling_policy" "scale_up" {
  name                   = "chatpulse-scale-up"
  scaling_adjustment     = 1
  adjustment_type        = "ChangeInCapacity"
  cooldown               = 300
  autoscaling_group_name = aws_autoscaling_group.backend.name
}

resource "aws_autoscaling_policy" "scale_down" {
  name                   = "chatpulse-scale-down"
  scaling_adjustment     = -1
  adjustment_type        = "ChangeInCapacity"
  cooldown               = 300
  autoscaling_group_name = aws_autoscaling_group.backend.name
}

# CloudWatch Alarms for Auto Scaling
resource "aws_cloudwatch_metric_alarm" "cpu_high" {
  alarm_name          = "chatpulse-cpu-high"
  comparison_operator = "GreaterThanThreshold"
  evaluation_periods  = 2
  metric_name         = "CPUUtilization"
  namespace           = "AWS/EC2"
  period              = 120
  statistic           = "Average"
  threshold           = 70
  alarm_description   = "This metric monitors EC2 CPU utilization"
  alarm_actions       = [aws_autoscaling_policy.scale_up.arn]

  dimensions = {
    AutoScalingGroupName = aws_autoscaling_group.backend.name
  }
}

resource "aws_cloudwatch_metric_alarm" "cpu_low" {
  alarm_name          = "chatpulse-cpu-low"
  comparison_operator = "LessThanThreshold"
  evaluation_periods  = 2
  metric_name         = "CPUUtilization"
  namespace           = "AWS/EC2"
  period              = 120
  statistic           = "Average"
  threshold           = 30
  alarm_description   = "This metric monitors EC2 CPU utilization"
  alarm_actions       = [aws_autoscaling_policy.scale_down.arn]

  dimensions = {
    AutoScalingGroupName = aws_autoscaling_group.backend.name
  }
}

# Data source for Amazon Linux 2023 AMI
data "aws_ami" "amazon_linux_2023" {
  most_recent = true
  owners      = ["amazon"]

  filter {
    name   = "name"
    values = ["al2023-ami-*-x86_64"]
  }

  filter {
    name   = "virtualization-type"
    values = ["hvm"]
  }
}
```

### IAM Role for Instances

```hcl
# terraform/iam.tf

# IAM Role for EC2 instances
resource "aws_iam_role" "backend" {
  name_prefix = "chatpulse-backend-"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Action = "sts:AssumeRole"
        Effect = "Allow"
        Principal = {
          Service = "ec2.amazonaws.com"
        }
      }
    ]
  })
}

# Attach policies
resource "aws_iam_role_policy_attachment" "backend_ssm" {
  role       = aws_iam_role.backend.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

# Instance Profile
resource "aws_iam_instance_profile" "backend" {
  name_prefix = "chatpulse-backend-"
  role        = aws_iam_role.backend.name
}
```

### User Data Script

```bash
#!/bin/bash
# user_data.sh - Startup script for ChatPulse backend instances

set -e

# Update system
yum update -y

# Install Docker
yum install -y docker
systemctl start docker
systemctl enable docker

# Pull and run ChatPulse container
docker run -d \
  --name chatpulse \
  --restart always \
  -p 8080:8080 \
  -e DATABASE_URL="${database_url}" \
  -e REDIS_URL="${redis_url}" \
  -e TWITCH_CLIENT_ID="${twitch_client_id}" \
  -e TWITCH_CLIENT_SECRET="${twitch_client_secret}" \
  -e TWITCH_REDIRECT_URI="https://${domain_name}/auth/callback" \
  -e SESSION_SECRET="${session_secret}" \
  -e WEBHOOK_CALLBACK_URL="https://${domain_name}/webhooks/eventsub" \
  -e WEBHOOK_SECRET="${webhook_secret}" \
  -e BOT_USER_ID="${bot_user_id}" \
  -e TOKEN_ENCRYPTION_KEY="${token_encryption_key}" \
  -e APP_ENV=production \
  ghcr.io/pscheid92/chatpulse:latest

# Wait for health check
timeout 60 bash -c 'until curl -sf http://localhost:8080/health/ready; do sleep 2; done'

echo "ChatPulse started successfully"
```

## WebSocket Support

ALB supports WebSocket connections natively:

- **Automatic Upgrade**: ALB automatically detects `Upgrade: websocket` headers and switches to WebSocket mode
- **No special configuration needed**: Unlike Nginx, ALB handles WebSocket upgrade transparently
- **Idle timeout**: Set `idle_timeout = 3600` on the ALB resource (default is 60s)
- **Stickiness**: Optional but recommended for overlay clients to reconnect to the same instance

## Deployment

### 1. Initialize Terraform

```bash
cd terraform
terraform init
```

### 2. Create `terraform.tfvars`

```hcl
aws_region         = "us-east-1"
vpc_id             = "vpc-0123456789abcdef0"
public_subnet_ids  = ["subnet-abc123", "subnet-def456"]
private_subnet_ids = ["subnet-ghi789", "subnet-jkl012"]
certificate_arn    = "arn:aws:acm:us-east-1:123456789012:certificate/..."
domain_name        = "chatpulse.example.com"

# Instance configuration
min_instances = 3
max_instances = 10

# Application secrets (use AWS Secrets Manager in production)
database_url           = "postgres://..."
redis_url              = "redis://..."
twitch_client_id       = "..."
twitch_client_secret   = "..."
session_secret         = "..."
webhook_secret         = "..."
bot_user_id            = "..."
token_encryption_key   = "..."
```

### 3. Apply Configuration

```bash
terraform plan
terraform apply
```

### 4. Verify Deployment

```bash
# Check ALB DNS
terraform output alb_dns_name

# Test health check
curl https://chatpulse.example.com/health/ready

# Test WebSocket connection
websocat wss://chatpulse.example.com/ws/overlay/YOUR-UUID-HERE
```

## Monitoring

### CloudWatch Metrics

ALB publishes metrics to CloudWatch automatically:

```bash
# View ALB metrics
aws cloudwatch get-metric-statistics \
  --namespace AWS/ApplicationELB \
  --metric-name TargetResponseTime \
  --dimensions Name=LoadBalancer,Value=app/chatpulse-alb/... \
  --statistics Average \
  --start-time 2026-02-12T00:00:00Z \
  --end-time 2026-02-12T23:59:59Z \
  --period 300
```

### Key Metrics

| Metric | Description | Alarm Threshold |
|--------|-------------|-----------------|
| `TargetResponseTime` | Backend response time | > 1000ms |
| `UnHealthyHostCount` | Number of unhealthy targets | > 0 |
| `HTTPCode_Target_5XX_Count` | 5xx errors from backend | > 10/min |
| `RejectedConnectionCount` | Rejected connections | > 0 |
| `ActiveConnectionCount` | Active WebSocket connections | > 10000 |

### CloudWatch Alarms (Terraform)

```hcl
# terraform/alarms.tf

resource "aws_cloudwatch_metric_alarm" "unhealthy_hosts" {
  alarm_name          = "chatpulse-unhealthy-hosts"
  comparison_operator = "GreaterThanThreshold"
  evaluation_periods  = 1
  metric_name         = "UnHealthyHostCount"
  namespace           = "AWS/ApplicationELB"
  period              = 60
  statistic           = "Maximum"
  threshold           = 0
  alarm_description   = "Alert when any backend instance is unhealthy"
  alarm_actions       = [aws_sns_topic.alerts.arn]

  dimensions = {
    LoadBalancer = aws_lb.main.arn_suffix
    TargetGroup  = aws_lb_target_group.backend.arn_suffix
  }
}

resource "aws_cloudwatch_metric_alarm" "target_5xx" {
  alarm_name          = "chatpulse-backend-5xx"
  comparison_operator = "GreaterThanThreshold"
  evaluation_periods  = 1
  metric_name         = "HTTPCode_Target_5XX_Count"
  namespace           = "AWS/ApplicationELB"
  period              = 60
  statistic           = "Sum"
  threshold           = 10
  alarm_description   = "Alert on high 5xx error rate"
  alarm_actions       = [aws_sns_topic.alerts.arn]

  dimensions = {
    LoadBalancer = aws_lb.main.arn_suffix
  }
}
```

## Troubleshooting

### Health Check Failures

```bash
# Check target health
aws elbv2 describe-target-health \
  --target-group-arn $(terraform output -raw target_group_arn)

# Common causes:
# 1. Security group blocking ALB → instance traffic
# 2. Instance not listening on port 8080
# 3. Health check path returning non-200
# 4. Database/Redis connection failing
```

### WebSocket Connection Issues

```bash
# Verify ALB idle timeout
aws elbv2 describe-load-balancer-attributes \
  --load-balancer-arn $(terraform output -raw alb_arn) | \
  grep idle_timeout

# Should show: "idle_timeout.timeout_seconds": "3600"

# Test WebSocket upgrade
curl -i -N \
  -H "Connection: Upgrade" \
  -H "Upgrade: websocket" \
  -H "Sec-WebSocket-Version: 13" \
  -H "Sec-WebSocket-Key: SGVsbG8sIHdvcmxkIQ==" \
  https://chatpulse.example.com/ws/overlay/test
```

### 502 Bad Gateway

Causes:
- All backend instances unhealthy
- Security group blocking traffic
- Backend crashed/not responding
- Health check grace period not elapsed

Fix:
```bash
# Check instance status
aws autoscaling describe-auto-scaling-groups \
  --auto-scaling-group-names chatpulse-asg

# SSH to instance (via Systems Manager)
aws ssm start-session --target i-0123456789abcdef0

# Check logs
docker logs chatpulse
```

## Production Checklist

- [ ] SSL certificate valid and auto-renewing
- [ ] Route 53 DNS record pointing to ALB
- [ ] Security groups follow least-privilege (ALB → backend only)
- [ ] Health checks configured on `/health/ready`
- [ ] ALB idle timeout set to 3600s (1 hour)
- [ ] Auto-scaling policies tested (scale up/down)
- [ ] CloudWatch alarms configured and tested
- [ ] Secrets stored in AWS Secrets Manager (not plaintext)
- [ ] Deletion protection enabled on ALB
- [ ] Access logs enabled (optional, requires S3 bucket)
- [ ] Instance refresh tested for zero-downtime deployments
- [ ] Health check grace period appropriate (300s)
- [ ] Deregistration delay set to match graceful shutdown (30s)

## Cost Optimization

1. **Use Savings Plans** for EC2 instances (1-year commitment)
2. **Right-size instances** based on actual CPU/memory usage
3. **Enable ALB access logs only if needed** (S3 storage costs)
4. **Use Spot instances** for non-critical workloads (not recommended for WebSocket)
5. **Set appropriate min/max ASG sizes** to avoid over-provisioning

## See Also

- [Nginx Configuration](load-balancer-nginx.md)
- [Kubernetes Configuration](load-balancer-kubernetes.md)
- [Graceful Shutdown Guide](graceful-shutdown.md)
- [Troubleshooting Guide](troubleshooting-load-balancer.md)

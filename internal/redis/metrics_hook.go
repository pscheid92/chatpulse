package redis

import (
	"context"
	"net"
	"time"

	"github.com/pscheid92/chatpulse/internal/metrics"
	"github.com/redis/go-redis/v9"
)

// MetricsHook implements redis.Hook to collect metrics on all Redis operations
type MetricsHook struct{}

var _ redis.Hook = (*MetricsHook)(nil)

// DialHook is called when establishing a new Redis connection
func (h *MetricsHook) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := next(ctx, network, addr)
		if err != nil {
			metrics.RedisConnectionErrors.Inc()
		}
		return conn, err
	}
}

// ProcessHook is called for every Redis command execution
func (h *MetricsHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		start := time.Now()
		err := next(ctx, cmd)
		duration := time.Since(start).Seconds()

		operation := cmd.Name()
		status := "success"
		if err != nil && err != redis.Nil {
			status = "error"
		}

		metrics.RedisOpsTotal.WithLabelValues(operation, status).Inc()
		metrics.RedisOpDuration.WithLabelValues(operation).Observe(duration)

		return err
	}
}

// ProcessPipelineHook is called for pipelined Redis commands
func (h *MetricsHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		start := time.Now()
		err := next(ctx, cmds)
		duration := time.Since(start).Seconds()

		// Track pipeline as a single operation
		status := "success"
		if err != nil {
			status = "error"
		}

		metrics.RedisOpsTotal.WithLabelValues("pipeline", status).Inc()
		metrics.RedisOpDuration.WithLabelValues("pipeline").Observe(duration)

		return err
	}
}

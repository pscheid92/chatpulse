package server

import (
	"context"
	"fmt"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/pscheid92/chatpulse/internal/version"
	goredis "github.com/redis/go-redis/v9"
)

// redisHealthChecker is a minimal interface for Redis health checks
type redisHealthChecker interface {
	Ping(ctx context.Context) *goredis.StatusCmd
	FunctionList(ctx context.Context, q goredis.FunctionListQuery) *goredis.FunctionListCmd
}

// postgresHealthChecker is a minimal interface for PostgreSQL health checks
type postgresHealthChecker interface {
	Ping(ctx context.Context) error
}

func (s *Server) handleStartup(c echo.Context) error {
	ctx, cancel := context.WithTimeout(c.Request().Context(), 2*time.Second)
	defer cancel()

	checks := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"redis", s.checkRedis},
		{"postgres", s.checkPostgres},
		{"redis_functions", s.checkRedisFunc},
	}

	for _, check := range checks {
		if err := check.fn(ctx); err != nil {
			if err := c.JSON(503, map[string]any{
				"status":       "unhealthy",
				"failed_check": check.name,
				"error":        err.Error(),
			}); err != nil {
				return fmt.Errorf("failed to send JSON response: %w", err)
			}
			return nil
		}
	}

	if err := c.JSON(200, map[string]string{"status": "ready"}); err != nil {
		return fmt.Errorf("failed to send JSON response: %w", err)
	}
	return nil
}

func (s *Server) handleLiveness(c echo.Context) error {
	uptime := time.Since(s.startTime).Seconds()
	if err := c.JSON(200, map[string]any{
		"status": "ok",
		"uptime": uptime,
	}); err != nil {
		return fmt.Errorf("failed to write liveness response: %w", err)
	}
	return nil
}

func (s *Server) handleReadiness(c echo.Context) error {
	ctx, cancel := context.WithTimeout(c.Request().Context(), 5*time.Second)
	defer cancel()

	checks := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"redis", s.checkRedis},
		{"postgres", s.checkPostgres},
		{"redis_functions", s.checkRedisFunc},
	}

	for _, check := range checks {
		if err := check.fn(ctx); err != nil {
			if err := c.JSON(503, map[string]any{
				"status":       "unhealthy",
				"failed_check": check.name,
				"error":        err.Error(),
			}); err != nil {
				return fmt.Errorf("failed to send JSON response: %w", err)
			}
			return nil
		}
	}

	if err := c.JSON(200, map[string]string{"status": "ready"}); err != nil {
		return fmt.Errorf("failed to send JSON response: %w", err)
	}
	return nil
}

func (s *Server) checkRedis(ctx context.Context) error {
	client := s.getRedisHealthChecker()
	if err := client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis ping failed: %w", err)
	}
	return nil
}

func (s *Server) checkPostgres(ctx context.Context) error {
	checker := s.getPostgresHealthChecker()
	if err := checker.Ping(ctx); err != nil {
		return fmt.Errorf("postgres ping failed: %w", err)
	}
	return nil
}

func (s *Server) checkRedisFunc(ctx context.Context) error {
	client := s.getRedisHealthChecker()
	result := client.FunctionList(ctx, goredis.FunctionListQuery{
		LibraryNamePattern: "chatpulse",
	})
	if err := result.Err(); err != nil {
		return fmt.Errorf("redis function list failed: %w", err)
	}
	libs, _ := result.Result()
	if len(libs) == 0 {
		return fmt.Errorf("chatpulse library not loaded")
	}
	return nil
}

func (s *Server) getRedisHealthChecker() redisHealthChecker {
	if s.redisHealthCheck != nil {
		return s.redisHealthCheck
	}
	return s.redisClient
}

func (s *Server) getPostgresHealthChecker() postgresHealthChecker {
	if s.postgresHealthCheck != nil {
		return s.postgresHealthCheck
	}
	return s.db
}

func (s *Server) handleVersion(c echo.Context) error {
	if err := c.JSON(200, version.Get()); err != nil {
		return fmt.Errorf("failed to write version response: %w", err)
	}
	return nil
}

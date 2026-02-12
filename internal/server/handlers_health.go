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
			return c.JSON(503, map[string]any{
				"status":       "unhealthy",
				"failed_check": check.name,
				"error":        err.Error(),
			})
		}
	}

	return c.JSON(200, map[string]string{"status": "ready"})
}

func (s *Server) handleLiveness(c echo.Context) error {
	uptime := time.Since(s.startTime).Seconds()
	return c.JSON(200, map[string]any{
		"status": "ok",
		"uptime": uptime,
	})
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
			return c.JSON(503, map[string]any{
				"status":       "unhealthy",
				"failed_check": check.name,
				"error":        err.Error(),
			})
		}
	}

	return c.JSON(200, map[string]string{"status": "ready"})
}

func (s *Server) checkRedis(ctx context.Context) error {
	client := s.getRedisHealthChecker()
	return client.Ping(ctx).Err()
}

func (s *Server) checkPostgres(ctx context.Context) error {
	checker := s.getPostgresHealthChecker()
	return checker.Ping(ctx)
}

func (s *Server) checkRedisFunc(ctx context.Context) error {
	client := s.getRedisHealthChecker()
	result := client.FunctionList(ctx, goredis.FunctionListQuery{
		LibraryNamePattern: "chatpulse",
	})
	if result.Err() != nil {
		return result.Err()
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
	return c.JSON(200, version.Get())
}

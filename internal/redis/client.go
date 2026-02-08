package redis

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// Client wraps a go-redis client with convenience methods.
type Client struct {
	rdb *redis.Client
}

// NewClient creates a new Redis client from a URL (e.g., "redis://localhost:6379").
func NewClient(redisURL string) (*Client, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse redis URL: %w", err)
	}

	rdb := redis.NewClient(opts)
	return &Client{rdb: rdb}, nil
}

// Ping verifies the Redis connection.
func (c *Client) Ping(ctx context.Context) error {
	return c.rdb.Ping(ctx).Err()
}

// Close closes the Redis connection.
func (c *Client) Close() error {
	return c.rdb.Close()
}

// Underlying returns the raw go-redis client for advanced operations.
func (c *Client) Underlying() *redis.Client {
	return c.rdb
}

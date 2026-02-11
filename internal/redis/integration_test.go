package redis

import (
	"context"
	"fmt"
	"os"
	"testing"

	goredis "github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go/modules/redis"
)

var testRedisURL string

func TestMain(m *testing.M) {
	ctx := context.Background()
	container, err := redis.Run(ctx, "redis:7-alpine")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to start redis container: %v\n", err)
		os.Exit(1)
	}

	endpoint, err := container.Endpoint(ctx, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to get redis endpoint: %v\n", err)
		os.Exit(1)
	}
	testRedisURL = "redis://" + endpoint

	defer func() {
		if err := container.Terminate(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "failed to terminate redis container: %v\n", err)
		}
	}()
	os.Exit(m.Run())
}

func setupTestClient(t *testing.T) *goredis.Client {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx := context.Background()
	client, err := NewClient(ctx, testRedisURL)
	if err != nil {
		t.Fatalf("failed to create redis client: %v", err)
	}

	// Flush all keys before each test
	if err := client.FlushAll(ctx).Err(); err != nil {
		t.Fatalf("failed to flush redis: %v", err)
	}

	t.Cleanup(func() {
		_ = client.Close()
	})

	return client
}

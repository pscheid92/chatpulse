package redis

import (
	"context"
	"fmt"
	"os"
	"testing"

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

	code := m.Run()

	_ = container.Terminate(ctx)
	os.Exit(code)
}

func setupTestClient(t *testing.T) *Client {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	client, err := NewClient(testRedisURL)
	if err != nil {
		t.Fatalf("failed to create redis client: %v", err)
	}

	// Flush all keys before each test
	ctx := context.Background()
	if err := client.rdb.FlushAll(ctx).Err(); err != nil {
		t.Fatalf("failed to flush redis: %v", err)
	}

	t.Cleanup(func() {
		_ = client.Close()
	})

	return client
}

package redis

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewClient_Connects(t *testing.T) {
	client := setupTestClient(t)
	ctx := context.Background()

	err := client.Ping(ctx).Err()
	require.NoError(t, err)
}

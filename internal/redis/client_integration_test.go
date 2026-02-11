package redis

import (
	"context"
	"testing"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClient_LoadsFunctions(t *testing.T) {
	client := setupTestClient(t)
	ctx := context.Background()

	libs, err := client.FunctionList(ctx, goredis.FunctionListQuery{LibraryNamePattern: "chatpulse"}).Result()
	require.NoError(t, err)
	require.Len(t, libs, 1)
	assert.Equal(t, "chatpulse", libs[0].Name)
}

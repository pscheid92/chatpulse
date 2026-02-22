package redis

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsDebounced(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	client := setupTestClient(t)
	debouncer := NewDebouncer(client)
	ctx := context.Background()

	broadcasterID := "broadcaster123"
	userID := "user123"

	// First vote: not debounced (allowed)
	debounced, err := debouncer.IsDebounced(ctx, broadcasterID, userID)
	require.NoError(t, err)
	assert.False(t, debounced)

	// Second vote immediately: debounced (blocked)
	debounced, err = debouncer.IsDebounced(ctx, broadcasterID, userID)
	require.NoError(t, err)
	assert.True(t, debounced)

	// Different user: not debounced (allowed)
	debounced, err = debouncer.IsDebounced(ctx, broadcasterID, "otheruser")
	require.NoError(t, err)
	assert.False(t, debounced)
}

package redis

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckDebounce(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	client := setupTestClient(t)
	debouncer := NewDebouncer(client)
	ctx := context.Background()

	overlayUUID := uuid.New()
	userID := "user123"

	// First vote: allowed
	allowed, err := debouncer.CheckDebounce(ctx, overlayUUID, userID)
	require.NoError(t, err)
	assert.True(t, allowed)

	// Second vote immediately: debounced
	allowed, err = debouncer.CheckDebounce(ctx, overlayUUID, userID)
	require.NoError(t, err)
	assert.False(t, allowed)

	// Different user: allowed
	allowed, err = debouncer.CheckDebounce(ctx, overlayUUID, "otheruser")
	require.NoError(t, err)
	assert.True(t, allowed)
}

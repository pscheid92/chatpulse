package twitch

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTokenRefreshError_Revoked(t *testing.T) {
	err := &TokenRefreshError{
		Revoked: true,
		Err:     fmt.Errorf("token was revoked by user"),
	}

	assert.Contains(t, err.Error(), "token revoked:")
	assert.Contains(t, err.Error(), "token was revoked by user")
}

func TestTokenRefreshError_NotRevoked(t *testing.T) {
	err := &TokenRefreshError{
		Revoked: false,
		Err:     fmt.Errorf("network error"),
	}

	assert.Contains(t, err.Error(), "token refresh failed:")
	assert.Contains(t, err.Error(), "network error")
}

package websocket

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewCheckOrigin(t *testing.T) {
	appURL := "https://chatpulse.example.com/webhooks/eventsub"

	tests := []struct {
		name          string
		origin        string
		isDevelopment bool
		want          bool
	}{
		// Always allowed
		{"empty origin", "", false, true},
		{"obs origin", "obs://", false, true},
		{"obs origin with host", "obs://obs-studio", false, true},
		{"app origin", "https://chatpulse.example.com", false, true},

		// Rejected in production
		{"different host", "https://evil.com", false, false},
		{"different port", "https://chatpulse.example.com:9090", false, false},
		{"http instead of https", "http://chatpulse.example.com", false, false},
		{"subdomain", "https://sub.chatpulse.example.com", false, false},

		// Localhost: allowed in dev, rejected in prod
		{"localhost dev", "http://localhost:8080", true, true},
		{"localhost no port dev", "http://localhost", true, true},
		{"127.0.0.1 dev", "http://127.0.0.1:3000", true, true},
		{"localhost prod rejected", "http://localhost:8080", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checker := NewCheckOrigin(appURL, tt.isDevelopment)
			r, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "/connection/websocket", nil)
			if tt.origin != "" {
				r.Header.Set("Origin", tt.origin)
			}
			assert.Equal(t, tt.want, checker(r))
		})
	}
}

func TestExtractOrigin(t *testing.T) {
	tests := []struct {
		name   string
		rawURL string
		want   string
	}{
		{"full URL with path", "https://example.com/webhooks/eventsub", "https://example.com"},
		{"URL with port", "https://example.com:8443/path", "https://example.com:8443"},
		{"http URL", "http://localhost:8080/callback", "http://localhost:8080"},
		{"empty string", "", ""},
		{"no host", "mailto:user@example.com", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, extractOrigin(tt.rawURL))
		})
	}
}

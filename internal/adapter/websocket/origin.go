package websocket

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

// NewCheckOrigin returns a CheckOrigin function for the Centrifuge WebSocket handler.
// It allows empty origins (same-origin / non-browser clients), obs:// origins
// (OBS browser sources), and the app's own origin (derived from appURL).
// When isDevelopment is true, localhost origins are additionally allowed.
func NewCheckOrigin(appURL string, isDevelopment bool) func(r *http.Request) bool {
	appOrigin := extractOrigin(appURL)

	return func(r *http.Request) bool {
		origin := r.Header.Get("Origin")

		if origin == "" {
			return true
		}

		if strings.HasPrefix(origin, "obs://") {
			return true
		}

		if origin == appOrigin {
			return true
		}

		if isDevelopment && isLocalhostOrigin(origin) {
			return true
		}

		slog.Warn("WebSocket origin rejected", "origin", origin, "remote_addr", r.RemoteAddr)
		return false
	}
}

func extractOrigin(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func isLocalhostOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := u.Hostname()
	return host == "localhost" || host == "127.0.0.1"
}

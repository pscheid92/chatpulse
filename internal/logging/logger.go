package logging

import (
	"log/slog"
	"os"
)

// Logger is the application-wide structured logger instance.
var Logger *slog.Logger

// InitLogger initializes the global logger with the specified level and format.
// level: "debug", "info", "warn", "error" (defaults to "info")
// format: "json" or "text" (defaults to "text")
func InitLogger(level, format string) {
	// Parse log level
	var logLevel slog.Level
	switch level {
	case "debug":
		logLevel = slog.LevelDebug
	case "info":
		logLevel = slog.LevelInfo
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}

	// Create handler based on format
	var handler slog.Handler
	opts := &slog.HandlerOptions{
		Level: logLevel,
	}

	if format == "json" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}

	Logger = slog.New(handler)
	slog.SetDefault(Logger)
}

// WithSession returns a logger with session_uuid field.
func WithSession(sessionUUID string) *slog.Logger {
	return Logger.With("session_uuid", sessionUUID)
}

// WithUser returns a logger with user_id field.
func WithUser(userID string) *slog.Logger {
	return Logger.With("user_id", userID)
}

// WithBroadcaster returns a logger with broadcaster_user_id field.
func WithBroadcaster(broadcasterUserID string) *slog.Logger {
	return Logger.With("broadcaster_user_id", broadcasterUserID)
}

// WithError returns a logger with error field.
func WithError(err error) *slog.Logger {
	return Logger.With("error", err)
}

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

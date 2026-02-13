package errors

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// HTTPErrorsTotal tracks HTTP errors by type
	HTTPErrorsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_errors_total",
			Help: "Total HTTP errors by error type",
		},
		[]string{"type"},
	)
)

// Middleware returns an Echo middleware that handles structured errors.
// It catches errors returned by handlers and converts them to appropriate HTTP responses.
func Middleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			err := next(c)
			if err == nil {
				return nil
			}

			// Check if it's an Echo HTTPError (from middleware like CSRF)
			// If so, pass it through unchanged to preserve the status code
			var httpErr *echo.HTTPError
			if errors.As(err, &httpErr) {
				// Still record metrics for Echo errors
				structuredErr := WrapHTTPError(httpErr)
				HTTPErrorsTotal.WithLabelValues(string(structuredErr.Type)).Inc()
				// Let Echo's default error handler deal with it
				return err
			}

			// Convert to structured error
			structuredErr := AsStructuredError(err)

			// Record metric
			HTTPErrorsTotal.WithLabelValues(string(structuredErr.Type)).Inc()

			// Log error with context
			logError(c, structuredErr)

			// Return JSON response
			if err := c.JSON(structuredErr.HTTPStatus(), structuredErr.ToResponse()); err != nil {
				return fmt.Errorf("failed to write error response: %w", err)
			}
			return nil
		}
	}
}

// logError logs an error with request context.
func logError(c echo.Context, err *Error) {
	attrs := []any{
		"error_type", err.Type,
		"message", err.Message,
		"path", c.Request().URL.Path,
		"method", c.Request().Method,
		"status", err.HTTPStatus(),
	}

	// Add context fields
	for k, v := range err.Context {
		attrs = append(attrs, k, v)
	}

	// Add user ID if present in context
	if userID := c.Get("userID"); userID != nil {
		attrs = append(attrs, "user_id", userID)
	}

	// Log at appropriate level
	switch err.Type {
	case TypeValidation:
		slog.Info("Validation error", attrs...)
	case TypeNotFound:
		slog.Info("Not found", attrs...)
	case TypeConflict:
		slog.Warn("Conflict", attrs...)
	case TypeInternal:
		if err.Cause != nil {
			attrs = append(attrs, "cause", err.Cause)
		}
		slog.Error("Internal error", attrs...)
	case TypeExternal:
		if err.Cause != nil {
			attrs = append(attrs, "cause", err.Cause)
		}
		slog.Error("External service error", attrs...)
	default:
		slog.Error("Unknown error type", attrs...)
	}
}

// HandleError is a helper for handlers to return structured errors.
// It's meant to be used in handlers that need to return errors directly.
func HandleError(c echo.Context, err error) error {
	if err == nil {
		return nil
	}

	structuredErr := AsStructuredError(err)
	HTTPErrorsTotal.WithLabelValues(string(structuredErr.Type)).Inc()
	logError(c, structuredErr)
	if err := c.JSON(structuredErr.HTTPStatus(), structuredErr.ToResponse()); err != nil {
		return fmt.Errorf("failed to write error response: %w", err)
	}
	return nil
}

// HandleValidationError is a convenience function for validation errors.
func HandleValidationError(c echo.Context, message string) error {
	return HandleError(c, ValidationError(message))
}

// HandleNotFoundError is a convenience function for not-found errors.
func HandleNotFoundError(c echo.Context, message string) error {
	return HandleError(c, NotFoundError(message))
}

// HandleInternalError is a convenience function for internal errors.
func HandleInternalError(c echo.Context, message string, cause error) error {
	return HandleError(c, InternalError(message, cause))
}

// WrapHTTPError converts Echo's HTTPError to a structured error.
// This is useful for compatibility with existing Echo error handling.
func WrapHTTPError(httpErr *echo.HTTPError) *Error {
	message := "internal server error"
	if httpErr.Message != nil {
		if msg, ok := httpErr.Message.(string); ok {
			message = msg
		}
	}

	// Map HTTP status to error type
	var errType ErrorType
	switch httpErr.Code {
	case http.StatusBadRequest:
		errType = TypeValidation
	case http.StatusNotFound:
		errType = TypeNotFound
	case http.StatusConflict:
		errType = TypeConflict
	case http.StatusBadGateway, http.StatusServiceUnavailable:
		errType = TypeExternal
	default:
		errType = TypeInternal
	}

	err := &Error{
		Type:    errType,
		Message: message,
		Context: make(map[string]any),
	}

	if httpErr.Internal != nil {
		err.Cause = httpErr.Internal
	}

	return err
}

package httpserver

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/pscheid92/chatpulse/internal/platform/correlation"
	apperrors "github.com/pscheid92/chatpulse/internal/platform/errors"
)

func correlationMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		ctx := correlation.WithID(c.Request().Context(), correlation.NewID())
		c.SetRequest(c.Request().WithContext(ctx))
		return next(c)
	}
}

func ErrorHandlingMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			err := next(c)
			if err == nil {
				return nil
			}

			if _, ok := errors.AsType[*echo.HTTPError](err); ok {
				return err
			}

			structuredErr := apperrors.AsStructuredError(err)
			logError(c, structuredErr)

			if err := c.JSON(structuredErr.HTTPStatus(), structuredErr.ToResponse()); err != nil {
				return fmt.Errorf("failed to write error response: %w", err)
			}
			return nil
		}
	}
}

func logError(c echo.Context, err *apperrors.Error) {
	attrs := []any{
		"error_type", err.Type,
		"message", err.Message,
		"path", c.Request().URL.Path,
		"method", c.Request().Method,
		"status", err.HTTPStatus(),
	}

	for k, v := range err.Context {
		attrs = append(attrs, k, v)
	}

	if userID := c.Get("userID"); userID != nil {
		attrs = append(attrs, "user_id", userID)
	}

	switch err.Type {
	case apperrors.TypeValidation:
		slog.Info("Validation error", attrs...)
	case apperrors.TypeNotFound:
		slog.Info("Not found", attrs...)
	case apperrors.TypeConflict:
		slog.Warn("Conflict", attrs...)
	case apperrors.TypeInternal:
		if err.Cause != nil {
			attrs = append(attrs, "cause", err.Cause)
		}
		slog.Error("Internal error", attrs...)
	case apperrors.TypeExternal:
		if err.Cause != nil {
			attrs = append(attrs, "cause", err.Cause)
		}
		slog.Error("External service error", attrs...)
	default:
		slog.Error("Unknown error type", attrs...)
	}
}

func HandleError(c echo.Context, err error) error {
	if err == nil {
		return nil
	}

	structuredErr := apperrors.AsStructuredError(err)
	logError(c, structuredErr)
	if err := c.JSON(structuredErr.HTTPStatus(), structuredErr.ToResponse()); err != nil {
		return fmt.Errorf("failed to write error response: %w", err)
	}
	return nil
}

func HandleValidationError(c echo.Context, message string) error {
	return HandleError(c, apperrors.ValidationError(message))
}

func HandleNotFoundError(c echo.Context, message string) error {
	return HandleError(c, apperrors.NotFoundError(message))
}

func HandleInternalError(c echo.Context, message string, cause error) error {
	return HandleError(c, apperrors.InternalError(message, cause))
}

func WrapHTTPError(httpErr *echo.HTTPError) *apperrors.Error {
	message := "internal server error"
	if httpErr.Message != nil {
		if msg, ok := httpErr.Message.(string); ok {
			message = msg
		}
	}

	var errType apperrors.ErrorType
	switch httpErr.Code {
	case http.StatusBadRequest:
		errType = apperrors.TypeValidation
	case http.StatusNotFound:
		errType = apperrors.TypeNotFound
	case http.StatusConflict:
		errType = apperrors.TypeConflict
	case http.StatusBadGateway, http.StatusServiceUnavailable:
		errType = apperrors.TypeExternal
	default:
		errType = apperrors.TypeInternal
	}

	err := &apperrors.Error{
		Type:    errType,
		Message: message,
		Context: make(map[string]any),
	}

	if httpErr.Internal != nil {
		err.Cause = httpErr.Internal
	}

	return err
}

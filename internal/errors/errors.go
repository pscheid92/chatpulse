// Package errors provides structured error handling with context propagation and HTTP status code mapping.
package errors

import (
	"errors"
	"fmt"
	"net/http"
)

// ErrorType represents the category of error for metrics and response formatting.
type ErrorType string

const (
	// TypeValidation indicates invalid input (HTTP 400)
	TypeValidation ErrorType = "validation"
	// TypeNotFound indicates resource not found (HTTP 404)
	TypeNotFound ErrorType = "not_found"
	// TypeConflict indicates resource conflict (HTTP 409)
	TypeConflict ErrorType = "conflict"
	// TypeInternal indicates server-side error (HTTP 500)
	TypeInternal ErrorType = "internal"
	// TypeExternal indicates external service error (HTTP 502/503)
	TypeExternal ErrorType = "external"
)

// Error represents a structured error with type, message, and context.
type Error struct {
	Type    ErrorType
	Message string
	Cause   error
	Context map[string]any
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Type, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Type, e.Message)
}

// Unwrap returns the underlying cause for errors.Is/As support.
func (e *Error) Unwrap() error {
	return e.Cause
}

// HTTPStatus returns the appropriate HTTP status code for this error type.
func (e *Error) HTTPStatus() int {
	switch e.Type {
	case TypeValidation:
		return http.StatusBadRequest
	case TypeNotFound:
		return http.StatusNotFound
	case TypeConflict:
		return http.StatusConflict
	case TypeExternal:
		return http.StatusBadGateway
	case TypeInternal:
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}

// ValidationError creates a new validation error (HTTP 400).
func ValidationError(message string) *Error {
	return &Error{
		Type:    TypeValidation,
		Message: message,
		Context: make(map[string]any),
	}
}

// NotFoundError creates a new not-found error (HTTP 404).
func NotFoundError(message string) *Error {
	return &Error{
		Type:    TypeNotFound,
		Message: message,
		Context: make(map[string]any),
	}
}

// ConflictError creates a new conflict error (HTTP 409).
func ConflictError(message string) *Error {
	return &Error{
		Type:    TypeConflict,
		Message: message,
		Context: make(map[string]any),
	}
}

// InternalError creates a new internal error (HTTP 500).
func InternalError(message string, cause error) *Error {
	return &Error{
		Type:    TypeInternal,
		Message: message,
		Cause:   cause,
		Context: make(map[string]any),
	}
}

// ExternalError creates a new external service error (HTTP 502).
func ExternalError(message string, cause error) *Error {
	return &Error{
		Type:    TypeExternal,
		Message: message,
		Cause:   cause,
		Context: make(map[string]any),
	}
}

// WithContext adds context fields to the error (chainable).
func (e *Error) WithContext(key string, value any) *Error {
	if e.Context == nil {
		e.Context = make(map[string]any)
	}
	e.Context[key] = value
	return e
}

// WithField is an alias for WithContext (chainable).
func (e *Error) WithField(key string, value any) *Error {
	return e.WithContext(key, value)
}

// ErrorResponse represents the JSON structure sent to clients.
type ErrorResponse struct {
	Error   string         `json:"error"`
	Type    ErrorType      `json:"type"`
	Context map[string]any `json:"context,omitempty"`
}

// ToResponse converts an Error to an ErrorResponse for JSON serialization.
func (e *Error) ToResponse() ErrorResponse {
	return ErrorResponse{
		Error:   e.Message,
		Type:    e.Type,
		Context: e.Context,
	}
}

// AsStructuredError converts any error into a structured Error.
// If err is already an *Error, returns it unchanged.
// Otherwise wraps it as an internal error.
func AsStructuredError(err error) *Error {
	if err == nil {
		return nil
	}

	var structuredErr *Error
	if errors.As(err, &structuredErr) {
		return structuredErr
	}

	return InternalError("internal server error", err)
}

package errors

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidationError(t *testing.T) {
	err := ValidationError("invalid input")

	assert.Equal(t, TypeValidation, err.Type)
	assert.Equal(t, "invalid input", err.Message)
	assert.Nil(t, err.Cause)
	assert.NotNil(t, err.Context)
	assert.Equal(t, http.StatusBadRequest, err.HTTPStatus())
	assert.Contains(t, err.Error(), "validation")
	assert.Contains(t, err.Error(), "invalid input")
}

func TestNotFoundError(t *testing.T) {
	err := NotFoundError("user not found")

	assert.Equal(t, TypeNotFound, err.Type)
	assert.Equal(t, "user not found", err.Message)
	assert.Nil(t, err.Cause)
	assert.NotNil(t, err.Context)
	assert.Equal(t, http.StatusNotFound, err.HTTPStatus())
	assert.Contains(t, err.Error(), "not_found")
	assert.Contains(t, err.Error(), "user not found")
}

func TestConflictError(t *testing.T) {
	err := ConflictError("resource already exists")

	assert.Equal(t, TypeConflict, err.Type)
	assert.Equal(t, "resource already exists", err.Message)
	assert.Nil(t, err.Cause)
	assert.NotNil(t, err.Context)
	assert.Equal(t, http.StatusConflict, err.HTTPStatus())
	assert.Contains(t, err.Error(), "conflict")
	assert.Contains(t, err.Error(), "resource already exists")
}

func TestInternalError(t *testing.T) {
	cause := fmt.Errorf("database connection failed")
	err := InternalError("failed to save user", cause)

	assert.Equal(t, TypeInternal, err.Type)
	assert.Equal(t, "failed to save user", err.Message)
	assert.Equal(t, cause, err.Cause)
	assert.NotNil(t, err.Context)
	assert.Equal(t, http.StatusInternalServerError, err.HTTPStatus())
	assert.Contains(t, err.Error(), "internal")
	assert.Contains(t, err.Error(), "failed to save user")
	assert.Contains(t, err.Error(), "database connection failed")
}

func TestInternalErrorWithoutCause(t *testing.T) {
	err := InternalError("something went wrong", nil)

	assert.Equal(t, TypeInternal, err.Type)
	assert.Nil(t, err.Cause)
	assert.NotContains(t, err.Error(), "<nil>")
}

func TestExternalError(t *testing.T) {
	cause := fmt.Errorf("twitch api timeout")
	err := ExternalError("failed to call twitch api", cause)

	assert.Equal(t, TypeExternal, err.Type)
	assert.Equal(t, "failed to call twitch api", err.Message)
	assert.Equal(t, cause, err.Cause)
	assert.NotNil(t, err.Context)
	assert.Equal(t, http.StatusBadGateway, err.HTTPStatus())
	assert.Contains(t, err.Error(), "external")
	assert.Contains(t, err.Error(), "failed to call twitch api")
	assert.Contains(t, err.Error(), "twitch api timeout")
}

func TestWithContext(t *testing.T) {
	err := ValidationError("invalid config")
	err = err.WithContext("field", "trigger_for")
	err = err.WithContext("value", "")

	assert.Len(t, err.Context, 2)
	assert.Equal(t, "trigger_for", err.Context["field"])
	assert.Equal(t, "", err.Context["value"])
}

func TestWithContextChaining(t *testing.T) {
	err := ValidationError("invalid input").
		WithContext("user_id", "123").
		WithContext("request_id", "req-456")

	assert.Len(t, err.Context, 2)
	assert.Equal(t, "123", err.Context["user_id"])
	assert.Equal(t, "req-456", err.Context["request_id"])
}

func TestWithField(t *testing.T) {
	err := NotFoundError("session not found").
		WithField("session_uuid", "abc-123").
		WithField("broadcaster_id", "456")

	assert.Len(t, err.Context, 2)
	assert.Equal(t, "abc-123", err.Context["session_uuid"])
	assert.Equal(t, "456", err.Context["broadcaster_id"])
}

func TestWithContextNilMap(t *testing.T) {
	// Create error and clear context to test nil handling
	err := &Error{
		Type:    TypeValidation,
		Message: "test",
		Context: nil,
	}

	err = err.WithContext("key", "value")

	assert.NotNil(t, err.Context)
	assert.Equal(t, "value", err.Context["key"])
}

func TestToResponse(t *testing.T) {
	err := ValidationError("invalid trigger").
		WithContext("field", "trigger_against").
		WithContext("max_length", 500)

	resp := err.ToResponse()

	assert.Equal(t, "invalid trigger", resp.Error)
	assert.Equal(t, TypeValidation, resp.Type)
	assert.Len(t, resp.Context, 2)
	assert.Equal(t, "trigger_against", resp.Context["field"])
	assert.Equal(t, 500, resp.Context["max_length"])
}

func TestToResponseEmptyContext(t *testing.T) {
	err := NotFoundError("user not found")

	resp := err.ToResponse()

	assert.Equal(t, "user not found", resp.Error)
	assert.Equal(t, TypeNotFound, resp.Type)
	assert.NotNil(t, resp.Context) // Should be empty map, not nil
	assert.Empty(t, resp.Context)
}

func TestUnwrap(t *testing.T) {
	cause := fmt.Errorf("root cause")
	err := InternalError("wrapped", cause)

	unwrapped := errors.Unwrap(err)
	assert.Equal(t, cause, unwrapped)
}

func TestUnwrapNil(t *testing.T) {
	err := ValidationError("test")

	unwrapped := errors.Unwrap(err)
	assert.Nil(t, unwrapped)
}

func TestErrorsIs(t *testing.T) {
	rootCause := fmt.Errorf("root")
	wrapped := InternalError("wrapped", rootCause)

	assert.True(t, errors.Is(wrapped, rootCause))
}

func TestErrorsAs(t *testing.T) {
	err := ValidationError("test")

	var target *Error
	require.True(t, errors.As(err, &target))
	assert.Equal(t, TypeValidation, target.Type)
}

func TestAsStructuredErrorWithStructuredError(t *testing.T) {
	original := ValidationError("original")
	result := AsStructuredError(original)

	assert.Equal(t, original, result)
	assert.Equal(t, TypeValidation, result.Type)
}

func TestAsStructuredErrorWithStandardError(t *testing.T) {
	original := fmt.Errorf("standard error")
	result := AsStructuredError(original)

	assert.NotNil(t, result)
	assert.Equal(t, TypeInternal, result.Type)
	assert.Equal(t, "internal server error", result.Message)
	assert.Equal(t, original, result.Cause)
}

func TestAsStructuredErrorWithNil(t *testing.T) {
	result := AsStructuredError(nil)
	assert.Nil(t, result)
}

func TestAsStructuredErrorWithWrappedStructuredError(t *testing.T) {
	original := NotFoundError("user not found")
	wrapped := fmt.Errorf("wrapped: %w", original)

	result := AsStructuredError(wrapped)

	assert.NotNil(t, result)
	assert.Equal(t, TypeNotFound, result.Type)
	assert.Equal(t, "user not found", result.Message)
}

func TestHTTPStatusAllTypes(t *testing.T) {
	tests := []struct {
		name       string
		errorType  ErrorType
		wantStatus int
	}{
		{"validation", TypeValidation, http.StatusBadRequest},
		{"not_found", TypeNotFound, http.StatusNotFound},
		{"conflict", TypeConflict, http.StatusConflict},
		{"internal", TypeInternal, http.StatusInternalServerError},
		{"external", TypeExternal, http.StatusBadGateway},
		{"unknown", ErrorType("unknown"), http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &Error{Type: tt.errorType}
			assert.Equal(t, tt.wantStatus, err.HTTPStatus())
		})
	}
}

func TestErrorStringWithoutCause(t *testing.T) {
	err := ValidationError("test message")
	errStr := err.Error()

	assert.Contains(t, errStr, "validation")
	assert.Contains(t, errStr, "test message")
	assert.NotContains(t, errStr, "nil")
}

func TestErrorStringWithCause(t *testing.T) {
	cause := fmt.Errorf("underlying issue")
	err := InternalError("wrapper message", cause)
	errStr := err.Error()

	assert.Contains(t, errStr, "internal")
	assert.Contains(t, errStr, "wrapper message")
	assert.Contains(t, errStr, "underlying issue")
}

func TestContextFieldOverwrite(t *testing.T) {
	err := ValidationError("test")
	err = err.WithContext("field", "original")
	err = err.WithContext("field", "overwritten")

	assert.Equal(t, "overwritten", err.Context["field"])
}

func TestMultipleContextFields(t *testing.T) {
	err := InternalError("database error", fmt.Errorf("connection lost")).
		WithContext("user_id", "user-123").
		WithContext("session_id", "sess-456").
		WithContext("query", "SELECT * FROM users").
		WithContext("retry_count", 3).
		WithContext("timeout_ms", 5000)

	assert.Len(t, err.Context, 5)
	assert.Equal(t, "user-123", err.Context["user_id"])
	assert.Equal(t, "sess-456", err.Context["session_id"])
	assert.Equal(t, "SELECT * FROM users", err.Context["query"])
	assert.Equal(t, 3, err.Context["retry_count"])
	assert.Equal(t, 5000, err.Context["timeout_ms"])
}

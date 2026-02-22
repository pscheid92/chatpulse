package httpserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	apperrors "github.com/pscheid92/chatpulse/internal/platform/errors"
	"github.com/pscheid92/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMiddlewareWithStructuredError(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := ErrorHandlingMiddleware()(func(c echo.Context) error {
		return apperrors.ValidationError("invalid input")
	})

	err := handler(c)
	require.NoError(t, err) // ErrorHandlingMiddleware handles the error, doesn't return it

	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var resp apperrors.ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "invalid input", resp.Error)
	assert.Equal(t, apperrors.TypeValidation, resp.Type)
}

func TestMiddlewareWithStandardError(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := ErrorHandlingMiddleware()(func(c echo.Context) error {
		return errors.New("standard error")
	})

	err := handler(c)
	require.NoError(t, err)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	var resp apperrors.ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "internal server error", resp.Error)
	assert.Equal(t, apperrors.TypeInternal, resp.Type)
}

func TestMiddlewareWithNoError(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := ErrorHandlingMiddleware()(func(c echo.Context) error {
		return c.String(http.StatusOK, "success")
	})

	err := handler(c)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "success", rec.Body.String())
}

func TestMiddlewareWithContext(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("userID", uuid.NewV4())

	handler := ErrorHandlingMiddleware()(func(c echo.Context) error {
		return apperrors.NotFoundError("user not found").
			WithContext("user_id", "123").
			WithContext("query", "SELECT * FROM users")
	})

	err := handler(c)
	require.NoError(t, err)

	assert.Equal(t, http.StatusNotFound, rec.Code)

	var resp apperrors.ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "user not found", resp.Error)
	assert.Equal(t, apperrors.TypeNotFound, resp.Type)
	assert.Len(t, resp.Context, 2)
	assert.Equal(t, "123", resp.Context["user_id"])
	assert.Equal(t, "SELECT * FROM users", resp.Context["query"])
}

func TestMiddlewareAllErrorTypes(t *testing.T) {
	tests := []struct {
		name       string
		err        *apperrors.Error
		wantStatus int
		wantType   apperrors.ErrorType
	}{
		{
			name:       "validation",
			err:        apperrors.ValidationError("invalid"),
			wantStatus: http.StatusBadRequest,
			wantType:   apperrors.TypeValidation,
		},
		{
			name:       "not_found",
			err:        apperrors.NotFoundError("missing"),
			wantStatus: http.StatusNotFound,
			wantType:   apperrors.TypeNotFound,
		},
		{
			name:       "conflict",
			err:        apperrors.ConflictError("duplicate"),
			wantStatus: http.StatusConflict,
			wantType:   apperrors.TypeConflict,
		},
		{
			name:       "internal",
			err:        apperrors.InternalError("failed", errors.New("cause")),
			wantStatus: http.StatusInternalServerError,
			wantType:   apperrors.TypeInternal,
		},
		{
			name:       "external",
			err:        apperrors.ExternalError("api failed", errors.New("timeout")),
			wantStatus: http.StatusBadGateway,
			wantType:   apperrors.TypeExternal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := echo.New()
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			handler := ErrorHandlingMiddleware()(func(c echo.Context) error {
				return tt.err
			})

			err := handler(c)
			require.NoError(t, err)

			assert.Equal(t, tt.wantStatus, rec.Code)

			var resp apperrors.ErrorResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			assert.Equal(t, tt.wantType, resp.Type)
		})
	}
}

func TestHandleError(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := HandleError(c, apperrors.ValidationError("test"))
	require.NoError(t, err)

	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var resp apperrors.ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "test", resp.Error)
	assert.Equal(t, apperrors.TypeValidation, resp.Type)
}

func TestHandleErrorWithNil(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := HandleError(c, nil)
	assert.NoError(t, err)
}

func TestHandleValidationError(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := HandleValidationError(c, "invalid input")
	require.NoError(t, err)

	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var resp apperrors.ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "invalid input", resp.Error)
	assert.Equal(t, apperrors.TypeValidation, resp.Type)
}

func TestHandleNotFoundError(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := HandleNotFoundError(c, "user not found")
	require.NoError(t, err)

	assert.Equal(t, http.StatusNotFound, rec.Code)

	var resp apperrors.ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "user not found", resp.Error)
	assert.Equal(t, apperrors.TypeNotFound, resp.Type)
}

func TestHandleInternalError(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	cause := errors.New("db error")
	err := HandleInternalError(c, "failed to save", cause)
	require.NoError(t, err)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	var resp apperrors.ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "failed to save", resp.Error)
	assert.Equal(t, apperrors.TypeInternal, resp.Type)
}

func TestWrapHTTPError(t *testing.T) {
	tests := []struct {
		name       string
		httpErr    *echo.HTTPError
		wantType   apperrors.ErrorType
		wantStatus int
	}{
		{
			name:       "bad_request",
			httpErr:    echo.NewHTTPError(http.StatusBadRequest, "bad request"),
			wantType:   apperrors.TypeValidation,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "not_found",
			httpErr:    echo.NewHTTPError(http.StatusNotFound, "not found"),
			wantType:   apperrors.TypeNotFound,
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "conflict",
			httpErr:    echo.NewHTTPError(http.StatusConflict, "conflict"),
			wantType:   apperrors.TypeConflict,
			wantStatus: http.StatusConflict,
		},
		{
			name:       "bad_gateway",
			httpErr:    echo.NewHTTPError(http.StatusBadGateway, "bad gateway"),
			wantType:   apperrors.TypeExternal,
			wantStatus: http.StatusBadGateway,
		},
		{
			name:       "service_unavailable",
			httpErr:    echo.NewHTTPError(http.StatusServiceUnavailable, "unavailable"),
			wantType:   apperrors.TypeExternal,
			wantStatus: http.StatusBadGateway, // External errors map to 502
		},
		{
			name:       "internal_server_error",
			httpErr:    echo.NewHTTPError(http.StatusInternalServerError, "internal error"),
			wantType:   apperrors.TypeInternal,
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := WrapHTTPError(tt.httpErr)

			assert.Equal(t, tt.wantType, err.Type)
			assert.Equal(t, tt.wantStatus, err.HTTPStatus())
		})
	}
}

func TestWrapHTTPErrorWithInternalCause(t *testing.T) {
	cause := errors.New("underlying cause")
	httpErr := echo.NewHTTPError(http.StatusInternalServerError, "wrapped")
	httpErr.Internal = cause

	err := WrapHTTPError(httpErr)

	assert.Equal(t, apperrors.TypeInternal, err.Type)
	assert.Equal(t, cause, err.Cause)
}

func TestWrapHTTPErrorWithNonStringMessage(t *testing.T) {
	httpErr := echo.NewHTTPError(http.StatusBadRequest, 12345)

	err := WrapHTTPError(httpErr)

	assert.Equal(t, "internal server error", err.Message) // Fallback message
	assert.Equal(t, apperrors.TypeValidation, err.Type)
}

func TestWrapHTTPErrorWithNilMessage(t *testing.T) {
	httpErr := &echo.HTTPError{
		Code:    http.StatusBadRequest,
		Message: nil,
	}

	err := WrapHTTPError(httpErr)

	assert.Equal(t, "internal server error", err.Message) // Fallback message
	assert.Equal(t, apperrors.TypeValidation, err.Type)
}

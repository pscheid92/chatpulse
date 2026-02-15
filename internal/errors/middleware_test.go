package errors

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/pscheid92/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMiddlewareWithStructuredError(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	// Reset metric for clean test
	HTTPErrorsTotal.Reset()

	handler := Middleware()(func(c echo.Context) error {
		return ValidationError("invalid input")
	})

	err := handler(c)
	require.NoError(t, err) // Middleware handles the error, doesn't return it

	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var resp ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "invalid input", resp.Error)
	assert.Equal(t, TypeValidation, resp.Type)

	// Check metric
	metricValue := getCounterValue(HTTPErrorsTotal.WithLabelValues("validation"))
	assert.Equal(t, 1.0, metricValue)
}

func TestMiddlewareWithStandardError(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	HTTPErrorsTotal.Reset()

	handler := Middleware()(func(c echo.Context) error {
		return fmt.Errorf("standard error")
	})

	err := handler(c)
	require.NoError(t, err)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	var resp ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "internal server error", resp.Error)
	assert.Equal(t, TypeInternal, resp.Type)

	metricValue := getCounterValue(HTTPErrorsTotal.WithLabelValues("internal"))
	assert.Equal(t, 1.0, metricValue)
}

func TestMiddlewareWithNoError(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	HTTPErrorsTotal.Reset()

	handler := Middleware()(func(c echo.Context) error {
		return c.String(http.StatusOK, "success")
	})

	err := handler(c)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "success", rec.Body.String())

	// No metrics should be recorded
	metricValue := getCounterValue(HTTPErrorsTotal.WithLabelValues("validation"))
	assert.Equal(t, 0.0, metricValue)
}

func TestMiddlewareWithContext(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("userID", uuid.NewV4())

	HTTPErrorsTotal.Reset()

	handler := Middleware()(func(c echo.Context) error {
		return NotFoundError("user not found").
			WithContext("user_id", "123").
			WithContext("query", "SELECT * FROM users")
	})

	err := handler(c)
	require.NoError(t, err)

	assert.Equal(t, http.StatusNotFound, rec.Code)

	var resp ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "user not found", resp.Error)
	assert.Equal(t, TypeNotFound, resp.Type)
	assert.Len(t, resp.Context, 2)
	assert.Equal(t, "123", resp.Context["user_id"])
	assert.Equal(t, "SELECT * FROM users", resp.Context["query"])
}

func TestMiddlewareAllErrorTypes(t *testing.T) {
	tests := []struct {
		name       string
		err        *Error
		wantStatus int
		wantType   ErrorType
	}{
		{
			name:       "validation",
			err:        ValidationError("invalid"),
			wantStatus: http.StatusBadRequest,
			wantType:   TypeValidation,
		},
		{
			name:       "not_found",
			err:        NotFoundError("missing"),
			wantStatus: http.StatusNotFound,
			wantType:   TypeNotFound,
		},
		{
			name:       "conflict",
			err:        ConflictError("duplicate"),
			wantStatus: http.StatusConflict,
			wantType:   TypeConflict,
		},
		{
			name:       "internal",
			err:        InternalError("failed", fmt.Errorf("cause")),
			wantStatus: http.StatusInternalServerError,
			wantType:   TypeInternal,
		},
		{
			name:       "external",
			err:        ExternalError("api failed", fmt.Errorf("timeout")),
			wantStatus: http.StatusBadGateway,
			wantType:   TypeExternal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := echo.New()
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			HTTPErrorsTotal.Reset()

			handler := Middleware()(func(c echo.Context) error {
				return tt.err
			})

			err := handler(c)
			require.NoError(t, err)

			assert.Equal(t, tt.wantStatus, rec.Code)

			var resp ErrorResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			assert.Equal(t, tt.wantType, resp.Type)

			metricValue := getCounterValue(HTTPErrorsTotal.WithLabelValues(string(tt.wantType)))
			assert.Equal(t, 1.0, metricValue)
		})
	}
}

func TestHandleError(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	HTTPErrorsTotal.Reset()

	err := HandleError(c, ValidationError("test"))
	require.NoError(t, err)

	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var resp ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "test", resp.Error)
	assert.Equal(t, TypeValidation, resp.Type)
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

	HTTPErrorsTotal.Reset()

	err := HandleValidationError(c, "invalid input")
	require.NoError(t, err)

	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var resp ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "invalid input", resp.Error)
	assert.Equal(t, TypeValidation, resp.Type)
}

func TestHandleNotFoundError(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	HTTPErrorsTotal.Reset()

	err := HandleNotFoundError(c, "user not found")
	require.NoError(t, err)

	assert.Equal(t, http.StatusNotFound, rec.Code)

	var resp ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "user not found", resp.Error)
	assert.Equal(t, TypeNotFound, resp.Type)
}

func TestHandleInternalError(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	HTTPErrorsTotal.Reset()

	cause := fmt.Errorf("db error")
	err := HandleInternalError(c, "failed to save", cause)
	require.NoError(t, err)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	var resp ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "failed to save", resp.Error)
	assert.Equal(t, TypeInternal, resp.Type)
}

func TestWrapHTTPError(t *testing.T) {
	tests := []struct {
		name       string
		httpErr    *echo.HTTPError
		wantType   ErrorType
		wantStatus int
	}{
		{
			name:       "bad_request",
			httpErr:    echo.NewHTTPError(http.StatusBadRequest, "bad request"),
			wantType:   TypeValidation,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "not_found",
			httpErr:    echo.NewHTTPError(http.StatusNotFound, "not found"),
			wantType:   TypeNotFound,
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "conflict",
			httpErr:    echo.NewHTTPError(http.StatusConflict, "conflict"),
			wantType:   TypeConflict,
			wantStatus: http.StatusConflict,
		},
		{
			name:       "bad_gateway",
			httpErr:    echo.NewHTTPError(http.StatusBadGateway, "bad gateway"),
			wantType:   TypeExternal,
			wantStatus: http.StatusBadGateway,
		},
		{
			name:       "service_unavailable",
			httpErr:    echo.NewHTTPError(http.StatusServiceUnavailable, "unavailable"),
			wantType:   TypeExternal,
			wantStatus: http.StatusBadGateway, // External errors map to 502
		},
		{
			name:       "internal_server_error",
			httpErr:    echo.NewHTTPError(http.StatusInternalServerError, "internal error"),
			wantType:   TypeInternal,
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
	cause := fmt.Errorf("underlying cause")
	httpErr := echo.NewHTTPError(http.StatusInternalServerError, "wrapped")
	httpErr.Internal = cause

	err := WrapHTTPError(httpErr)

	assert.Equal(t, TypeInternal, err.Type)
	assert.Equal(t, cause, err.Cause)
}

func TestWrapHTTPErrorWithNonStringMessage(t *testing.T) {
	httpErr := echo.NewHTTPError(http.StatusBadRequest, 12345)

	err := WrapHTTPError(httpErr)

	assert.Equal(t, "internal server error", err.Message) // Fallback message
	assert.Equal(t, TypeValidation, err.Type)
}

func TestWrapHTTPErrorWithNilMessage(t *testing.T) {
	httpErr := &echo.HTTPError{
		Code:    http.StatusBadRequest,
		Message: nil,
	}

	err := WrapHTTPError(httpErr)

	assert.Equal(t, "internal server error", err.Message) // Fallback message
	assert.Equal(t, TypeValidation, err.Type)
}

// Helper function to get counter value from Prometheus metric
func getCounterValue(counter prometheus.Counter) float64 {
	ch := make(chan prometheus.Metric, 1)
	counter.Collect(ch)
	close(ch)

	metric := <-ch
	m := &dto.Metric{}
	_ = metric.Write(m)
	return m.GetCounter().GetValue()
}

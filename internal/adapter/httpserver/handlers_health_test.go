package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func healthOK(_ context.Context) error { return nil }

func healthErr(msg string) func(context.Context) error {
	return func(_ context.Context) error { return errors.New(msg) }
}

func TestHandleStartup(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/health/startup", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	srv := newTestServer(t, &mockAppService{},
		withHealthChecks(
			HealthCheck{Name: "redis", Check: healthOK},
			HealthCheck{Name: "postgres", Check: healthOK},
			HealthCheck{Name: "redis_functions", Check: healthOK},
		),
	)

	err := srv.handleStartup(c)

	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"status":"ready"}`, rec.Body.String())
}

func TestHandleStartup_RedisDown(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/health/startup", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	srv := newTestServer(t, &mockAppService{},
		withHealthChecks(
			HealthCheck{Name: "redis", Check: healthErr("connection refused")},
			HealthCheck{Name: "postgres", Check: healthOK},
			HealthCheck{Name: "redis_functions", Check: healthOK},
		),
	)

	err := srv.handleStartup(c)

	require.NoError(t, err)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), `"status":"unhealthy"`)
	assert.Contains(t, rec.Body.String(), `"failed_check":"redis"`)
}

func TestHandleLiveness(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/health/live", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	srv := newTestServer(t, &mockAppService{})
	err := srv.handleLiveness(c)

	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	assert.Contains(t, body, `"status":"ok"`)
	assert.Contains(t, body, `"uptime"`)
}

func TestHandleReadiness_AllHealthy(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	srv := newTestServer(t, &mockAppService{},
		withHealthChecks(
			HealthCheck{Name: "redis", Check: healthOK},
			HealthCheck{Name: "postgres", Check: healthOK},
			HealthCheck{Name: "redis_functions", Check: healthOK},
		),
	)

	err := srv.handleReadiness(c)

	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"status":"ready"}`, rec.Body.String())
}

func TestHandleReadiness_RedisDown(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	srv := newTestServer(t, &mockAppService{},
		withHealthChecks(
			HealthCheck{Name: "redis", Check: healthErr("connection refused")},
			HealthCheck{Name: "postgres", Check: healthOK},
			HealthCheck{Name: "redis_functions", Check: healthOK},
		),
	)

	err := srv.handleReadiness(c)

	require.NoError(t, err)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), `"status":"unhealthy"`)
	assert.Contains(t, rec.Body.String(), `"failed_check":"redis"`)
	assert.Contains(t, rec.Body.String(), `"error":"connection refused"`)
}

func TestHandleReadiness_PostgresDown(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	srv := newTestServer(t, &mockAppService{},
		withHealthChecks(
			HealthCheck{Name: "redis", Check: healthOK},
			HealthCheck{Name: "postgres", Check: healthErr("database unreachable")},
			HealthCheck{Name: "redis_functions", Check: healthOK},
		),
	)

	err := srv.handleReadiness(c)

	require.NoError(t, err)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), `"status":"unhealthy"`)
	assert.Contains(t, rec.Body.String(), `"failed_check":"postgres"`)
	assert.Contains(t, rec.Body.String(), `"error":"database unreachable"`)
}

func TestHandleReadiness_RedisFunctionsNotLoaded(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	srv := newTestServer(t, &mockAppService{},
		withHealthChecks(
			HealthCheck{Name: "redis", Check: healthOK},
			HealthCheck{Name: "postgres", Check: healthOK},
			HealthCheck{Name: "redis_functions", Check: healthErr("chatpulse library not loaded")},
		),
	)

	err := srv.handleReadiness(c)

	require.NoError(t, err)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), `"status":"unhealthy"`)
	assert.Contains(t, rec.Body.String(), `"failed_check":"redis_functions"`)
	assert.Contains(t, rec.Body.String(), `"error":"chatpulse library not loaded"`)
}

func TestHandleReadiness_RedisFunctionListError(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	srv := newTestServer(t, &mockAppService{},
		withHealthChecks(
			HealthCheck{Name: "redis", Check: healthOK},
			HealthCheck{Name: "postgres", Check: healthOK},
			HealthCheck{Name: "redis_functions", Check: healthErr("FUNCTION LIST failed")},
		),
	)

	err := srv.handleReadiness(c)

	require.NoError(t, err)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), `"status":"unhealthy"`)
	assert.Contains(t, rec.Body.String(), `"failed_check":"redis_functions"`)
	assert.Contains(t, rec.Body.String(), `"error":"FUNCTION LIST failed"`)
}

func TestHandleVersion(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	srv := newTestServer(t, &mockAppService{})
	err := srv.handleVersion(c)

	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	assert.Contains(t, body, `"version"`)
	assert.Contains(t, body, `"commit"`)
	assert.Contains(t, body, `"build_time"`)
	assert.Contains(t, body, `"go_version"`)
}

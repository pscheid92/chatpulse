package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockRedisClient provides a minimal mock for health check testing
type mockRedisClient struct {
	pingErr         error
	functionListErr error
	libraries       []goredis.Library
}

func (m *mockRedisClient) Ping(ctx context.Context) *goredis.StatusCmd {
	cmd := goredis.NewStatusCmd(ctx)
	if m.pingErr != nil {
		cmd.SetErr(m.pingErr)
	} else {
		cmd.SetVal("PONG")
	}
	return cmd
}

func (m *mockRedisClient) FunctionList(ctx context.Context, q goredis.FunctionListQuery) *goredis.FunctionListCmd {
	cmd := goredis.NewFunctionListCmd(ctx)
	if m.functionListErr != nil {
		cmd.SetErr(m.functionListErr)
	} else {
		cmd.SetVal(m.libraries)
	}
	return cmd
}

// mockPgxPool provides a minimal mock for PostgreSQL health checks
type mockPgxPool struct {
	pingErr error
}

func (m *mockPgxPool) Ping(ctx context.Context) error {
	return m.pingErr
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
	assert.JSONEq(t, `{"status":"ok"}`, rec.Body.String())
}

func TestHandleReadiness_AllHealthy(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	srv := newTestServer(t, &mockAppService{},
		withRedisHealthCheck(&mockRedisClient{
			libraries: []goredis.Library{
				{Name: "chatpulse"},
			},
		}),
		withPostgresHealthCheck(&mockPgxPool{}),
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
		withRedisHealthCheck(&mockRedisClient{pingErr: errors.New("connection refused")}),
		withPostgresHealthCheck(&mockPgxPool{}),
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
		withRedisHealthCheck(&mockRedisClient{
			libraries: []goredis.Library{
				{Name: "chatpulse"},
			},
		}),
		withPostgresHealthCheck(&mockPgxPool{pingErr: errors.New("database unreachable")}),
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
		withRedisHealthCheck(&mockRedisClient{libraries: []goredis.Library{}}), // Empty list
		withPostgresHealthCheck(&mockPgxPool{}),
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
		withRedisHealthCheck(&mockRedisClient{functionListErr: errors.New("FUNCTION LIST failed")}),
		withPostgresHealthCheck(&mockPgxPool{}),
	)

	err := srv.handleReadiness(c)

	require.NoError(t, err)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), `"status":"unhealthy"`)
	assert.Contains(t, rec.Body.String(), `"failed_check":"redis_functions"`)
	assert.Contains(t, rec.Body.String(), `"error":"FUNCTION LIST failed"`)
}

func TestCheckRedis(t *testing.T) {
	tests := []struct {
		name    string
		pingErr error
		wantErr bool
	}{
		{
			name:    "success",
			pingErr: nil,
			wantErr: false,
		},
		{
			name:    "connection error",
			pingErr: errors.New("redis: connection timeout"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestServer(t, &mockAppService{},
				withRedisHealthCheck(&mockRedisClient{pingErr: tt.pingErr}),
			)

			err := srv.checkRedis(context.Background())

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestCheckPostgres(t *testing.T) {
	tests := []struct {
		name    string
		pingErr error
		wantErr bool
	}{
		{
			name:    "success",
			pingErr: nil,
			wantErr: false,
		},
		{
			name:    "connection error",
			pingErr: errors.New("postgres: connection refused"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestServer(t, &mockAppService{},
				withPostgresHealthCheck(&mockPgxPool{pingErr: tt.pingErr}),
			)

			err := srv.checkPostgres(context.Background())

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestCheckRedisFunc(t *testing.T) {
	tests := []struct {
		name            string
		functionListErr error
		libraries       []goredis.Library
		wantErr         bool
		wantErrContains string
	}{
		{
			name:      "success - library loaded",
			libraries: []goredis.Library{{Name: "chatpulse"}},
			wantErr:   false,
		},
		{
			name:            "error - library not loaded",
			libraries:       []goredis.Library{},
			wantErr:         true,
			wantErrContains: "chatpulse library not loaded",
		},
		{
			name:            "error - FUNCTION LIST command failed",
			functionListErr: errors.New("redis: command error"),
			wantErr:         true,
			wantErrContains: "redis: command error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestServer(t, &mockAppService{},
				withRedisHealthCheck(&mockRedisClient{
					functionListErr: tt.functionListErr,
					libraries:       tt.libraries,
				}),
			)

			err := srv.checkRedisFunc(context.Background())

			if tt.wantErr {
				require.Error(t, err)
				if tt.wantErrContains != "" {
					assert.Contains(t, err.Error(), tt.wantErrContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

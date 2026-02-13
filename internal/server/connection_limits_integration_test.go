package server

import (
	"context"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/sessions"
	"github.com/gorilla/websocket"
	"github.com/jonboulle/clockwork"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/pscheid92/chatpulse/internal/broadcast"
	"github.com/pscheid92/chatpulse/internal/config"
	"github.com/pscheid92/chatpulse/internal/domain"
	apperrors "github.com/pscheid92/chatpulse/internal/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConnectionLimitsIntegration_GlobalLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Setup server with low global limit
	cfg := &config.Config{
		MaxWebSocketConnections: 3, // Low limit for testing
		MaxConnectionsPerIP:     100,
		ConnectionRatePerIP:     100,
		ConnectionRateBurst:     100,
	}

	mockApp := &mockAppService{
		getUserByOverlayFn: func(ctx context.Context, overlayUUID uuid.UUID) (*domain.User, error) {
			return &domain.User{
				ID:          uuid.New(),
				OverlayUUID: overlayUUID,
			}, nil
		},
		getConfigFn: func(ctx context.Context, userID uuid.UUID) (*domain.Config, error) {
			return &domain.Config{}, nil
		},
	}

	clock := clockwork.NewFakeClock()
	broadcaster := broadcast.NewBroadcaster(nil, nil, clock, 50, 5*time.Second)
	defer broadcaster.Stop()

	srv := newTestServerWithLimits(t, cfg, mockApp, broadcaster)

	// Create test HTTP server
	ts := httptest.NewServer(srv.echo)
	defer ts.Close()

	overlayUUID := uuid.New()
	wsURL := strings.Replace(ts.URL, "http://", "ws://", 1) + "/ws/overlay/" + overlayUUID.String()

	// Connect up to the limit (3 connections)
	var conns []*websocket.Conn
	for i := 0; i < 3; i++ {
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		require.NoError(t, err, "Connection %d should succeed", i+1)
		conns = append(conns, conn)
	}

	// 4th connection should be rejected with 503 (global limit)
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.Error(t, err, "4th connection should fail")
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, "Should return 503 for global limit")

	// Cleanup
	for _, c := range conns {
		c.Close()
	}
	if conn != nil {
		conn.Close()
	}
}

func TestConnectionLimitsIntegration_PerIPLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Setup server with low per-IP limit
	cfg := &config.Config{
		MaxWebSocketConnections: 100,
		MaxConnectionsPerIP:     2, // Low limit for testing
		ConnectionRatePerIP:     100,
		ConnectionRateBurst:     100,
	}

	mockApp := &mockAppService{
		getUserByOverlayFn: func(ctx context.Context, overlayUUID uuid.UUID) (*domain.User, error) {
			return &domain.User{
				ID:          uuid.New(),
				OverlayUUID: overlayUUID,
			}, nil
		},
		getConfigFn: func(ctx context.Context, userID uuid.UUID) (*domain.Config, error) {
			return &domain.Config{}, nil
		},
	}

	clock := clockwork.NewFakeClock()
	broadcaster := broadcast.NewBroadcaster(nil, nil, clock, 50, 5*time.Second)
	defer broadcaster.Stop()

	srv := newTestServerWithLimits(t, cfg, mockApp, broadcaster)

	// Create test HTTP server
	ts := httptest.NewServer(srv.echo)
	defer ts.Close()

	overlayUUID := uuid.New()
	wsURL := strings.Replace(ts.URL, "http://", "ws://", 1) + "/ws/overlay/" + overlayUUID.String()

	// All connections come from same IP (127.0.0.1)
	// Connect up to the per-IP limit (2 connections)
	var conns []*websocket.Conn
	for i := 0; i < 2; i++ {
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		require.NoError(t, err, "Connection %d should succeed", i+1)
		conns = append(conns, conn)
	}

	// 3rd connection from same IP should be rejected with 429
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.Error(t, err, "3rd connection from same IP should fail")
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode, "Should return 429 for per-IP limit")

	// Cleanup
	for _, c := range conns {
		c.Close()
	}
	if conn != nil {
		conn.Close()
	}
}

func TestConnectionLimitsIntegration_RateLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Setup server with low rate limit
	cfg := &config.Config{
		MaxWebSocketConnections: 100,
		MaxConnectionsPerIP:     100,
		ConnectionRatePerIP:     2, // 2 per second
		ConnectionRateBurst:     2, // Burst of 2
	}

	mockApp := &mockAppService{
		getUserByOverlayFn: func(ctx context.Context, overlayUUID uuid.UUID) (*domain.User, error) {
			return &domain.User{
				ID:          uuid.New(),
				OverlayUUID: overlayUUID,
			}, nil
		},
		getConfigFn: func(ctx context.Context, userID uuid.UUID) (*domain.Config, error) {
			return &domain.Config{}, nil
		},
	}

	clock := clockwork.NewFakeClock()
	broadcaster := broadcast.NewBroadcaster(nil, nil, clock, 50, 5*time.Second)
	defer broadcaster.Stop()

	srv := newTestServerWithLimits(t, cfg, mockApp, broadcaster)

	// Create test HTTP server
	ts := httptest.NewServer(srv.echo)
	defer ts.Close()

	overlayUUID := uuid.New()
	wsURL := strings.Replace(ts.URL, "http://", "ws://", 1) + "/ws/overlay/" + overlayUUID.String()

	// Rapidly open connections (exhaust burst)
	for i := 0; i < 2; i++ {
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		require.NoError(t, err, "Connection %d should succeed (burst)", i+1)
		conn.Close() // Close immediately to free up slots
	}

	// 3rd rapid connection should be rate limited
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.Error(t, err, "3rd rapid connection should fail")
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode, "Should return 429 for rate limit")

	// Wait for rate limiter to refill
	time.Sleep(600 * time.Millisecond) // > 1/2 second at 2/sec rate

	// Now connection should succeed
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err, "Connection after rate limit refill should succeed")
	if conn != nil {
		conn.Close()
	}
}

func TestConnectionLimitsIntegration_ReleaseOnDisconnect(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Setup server with limit of 2
	cfg := &config.Config{
		MaxWebSocketConnections: 2,
		MaxConnectionsPerIP:     2,
		ConnectionRatePerIP:     100,
		ConnectionRateBurst:     100,
	}

	mockApp := &mockAppService{
		getUserByOverlayFn: func(ctx context.Context, overlayUUID uuid.UUID) (*domain.User, error) {
			return &domain.User{
				ID:          uuid.New(),
				OverlayUUID: overlayUUID,
			}, nil
		},
		getConfigFn: func(ctx context.Context, userID uuid.UUID) (*domain.Config, error) {
			return &domain.Config{}, nil
		},
	}

	clock := clockwork.NewFakeClock()
	broadcaster := broadcast.NewBroadcaster(nil, nil, clock, 50, 5*time.Second)
	defer broadcaster.Stop()

	srv := newTestServerWithLimits(t, cfg, mockApp, broadcaster)

	// Create test HTTP server
	ts := httptest.NewServer(srv.echo)
	defer ts.Close()

	overlayUUID := uuid.New()
	wsURL := strings.Replace(ts.URL, "http://", "ws://", 1) + "/ws/overlay/" + overlayUUID.String()

	// Fill up the limit
	conn1, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	conn2, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)

	// 3rd should fail
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.Error(t, err)
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)

	// Close one connection
	conn1.Close()
	time.Sleep(100 * time.Millisecond) // Give handler time to cleanup

	// Now a new connection should succeed (slot released)
	conn3, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err, "Connection after disconnect should succeed")

	// Cleanup
	conn2.Close()
	conn3.Close()
}

func TestConnectionLimitsIntegration_ConcurrentConnections(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Setup server
	cfg := &config.Config{
		MaxWebSocketConnections: 20,
		MaxConnectionsPerIP:     20,
		ConnectionRatePerIP:     100,
		ConnectionRateBurst:     100,
	}

	mockApp := &mockAppService{
		getUserByOverlayFn: func(ctx context.Context, overlayUUID uuid.UUID) (*domain.User, error) {
			return &domain.User{
				ID:          uuid.New(),
				OverlayUUID: overlayUUID,
			}, nil
		},
		getConfigFn: func(ctx context.Context, userID uuid.UUID) (*domain.Config, error) {
			return &domain.Config{}, nil
		},
	}

	clock := clockwork.NewFakeClock()
	broadcaster := broadcast.NewBroadcaster(nil, nil, clock, 50, 5*time.Second)
	defer broadcaster.Stop()

	srv := newTestServerWithLimits(t, cfg, mockApp, broadcaster)

	// Create test HTTP server
	ts := httptest.NewServer(srv.echo)
	defer ts.Close()

	overlayUUID := uuid.New()
	wsURL := strings.Replace(ts.URL, "http://", "ws://", 1) + "/ws/overlay/" + overlayUUID.String()

	// Try to open 40 connections concurrently (limit is 20)
	var wg sync.WaitGroup
	successCount := 0
	failCount := 0
	var mu sync.Mutex
	var conns []*websocket.Conn

	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				successCount++
				conns = append(conns, conn)
			} else {
				failCount++
				// Expected global limit rejection - verify status code
				assert.True(t, resp != nil && resp.StatusCode == http.StatusServiceUnavailable, "Failed connection should return 503")
			}
		}()
	}

	wg.Wait()

	// Should have exactly 20 successes and 20 failures
	assert.Equal(t, 20, successCount, "Should have 20 successful connections")
	assert.Equal(t, 20, failCount, "Should have 20 failed connections")

	// Cleanup
	for _, c := range conns {
		c.Close()
	}
}

// Helper function to create a test server with custom limits
func newTestServerWithLimits(t *testing.T, cfg *config.Config, app domain.AppService, broadcaster *broadcast.Broadcaster) *Server {
	t.Helper()

	// Create minimal templates for testing
	loginTmpl := template.Must(template.New("login.html").Parse(`Login`))
	dashTmpl := template.Must(template.New("dashboard.html").Parse(`Dashboard`))
	overlayTmpl := template.Must(template.New("overlay.html").Parse(`Overlay`))

	// Create session store
	store := sessions.NewCookieStore([]byte("test-secret-key-32-bytes-long!!!"))
	store.Options = &sessions.Options{
		Path:   "/",
		MaxAge: 3600,
	}

	// Create Echo instance without Prometheus middleware to avoid duplicate registration
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		LogStatus: true,
		LogURI:    true,
		LogMethod: true,
		LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
			return nil
		},
	}))
	e.Use(middleware.Recover())
	e.Use(apperrors.Middleware())

	// Create connection limiter
	connLimits := NewConnectionLimits(
		int64(cfg.MaxWebSocketConnections),
		cfg.MaxConnectionsPerIP,
		cfg.ConnectionRatePerIP,
		cfg.ConnectionRateBurst,
	)

	// Manually construct server to avoid Prometheus middleware registration
	srv := &Server{
		echo:              e,
		config:            cfg,
		app:               app,
		broadcaster:       broadcaster,
		sessionStore:      store,
		loginTemplate:     loginTmpl,
		dashboardTemplate: dashTmpl,
		overlayTemplate:   overlayTmpl,
		connLimits:        connLimits,
		startTime:         time.Now(),
	}

	// Register routes
	srv.registerRoutes()

	return srv
}

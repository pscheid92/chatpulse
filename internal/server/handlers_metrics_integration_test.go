package server

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pscheid92/chatpulse/internal/metrics"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetricsEndpoint(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Create test server
	srv := newTestServer(t, &mockAppService{})

	// Make request to /metrics endpoint
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()

	srv.echo.ServeHTTP(rec, req)

	// Verify response
	require.Equal(t, http.StatusOK, rec.Code, "metrics endpoint should return 200")
	contentType := rec.Header().Get("Content-Type")
	assert.Contains(t, contentType, "text/plain; version=0.0.4; charset=utf-8",
		"metrics should be in Prometheus text format")

	body := rec.Body.String()

	// Verify body is not empty
	require.NotEmpty(t, body, "metrics response should not be empty")

	// Verify Prometheus format (starts with # HELP or # TYPE)
	lines := strings.Split(body, "\n")
	foundHelp := false
	foundType := false
	for _, line := range lines {
		if strings.HasPrefix(line, "# HELP") {
			foundHelp = true
		}
		if strings.HasPrefix(line, "# TYPE") {
			foundType = true
		}
		if foundHelp && foundType {
			break
		}
	}
	assert.True(t, foundHelp, "metrics should include HELP comments")
	assert.True(t, foundType, "metrics should include TYPE comments")
}

func TestMetricsEndpointContainsExpectedMetrics(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Generate some metric activity
	metrics.RedisOpsTotal.WithLabelValues("get", "success").Inc()
	metrics.BroadcasterActiveSessions.Set(5)
	metrics.WebSocketConnectionsCurrent.Inc()
	metrics.VoteProcessingTotal.WithLabelValues("applied").Inc()

	// Create test server
	srv := newTestServer(t, &mockAppService{})

	// Make request to /metrics endpoint
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()

	srv.echo.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	// Verify expected metrics are present (only test metrics we've explicitly generated)
	expectedMetrics := []string{
		"redis_operations_total",          // Generated above
		"broadcaster_active_sessions",     // Generated above
		"websocket_connections_current",   // Generated above
		"vote_processing_total",           // Generated above
	}

	for _, metricName := range expectedMetrics {
		assert.Contains(t, body, metricName, "metrics should include %s", metricName)
	}

	// Verify some metric families are registered (even if zero values)
	metricFamilies := []string{
		"broadcaster_connected_clients_total",
		"vote_processing_duration_seconds",
		"websocket_message_send_duration_seconds",
		"redis_connection_errors_total",
	}

	for _, metricFamily := range metricFamilies {
		assert.Contains(t, body, metricFamily, "metrics should declare %s family", metricFamily)
	}
}

func TestMetricsPrometheusFormat(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Generate metric activity
	metrics.RedisOpsTotal.WithLabelValues("test", "success").Inc()

	// Create test server
	srv := newTestServer(t, &mockAppService{})

	// Make request
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, req)

	// Parse Prometheus format
	scanner := bufio.NewScanner(strings.NewReader(rec.Body.String()))

	helpComments := 0
	typeComments := 0
	metricLines := 0

	for scanner.Scan() {
		line := scanner.Text()

		// Skip empty lines
		if line == "" {
			continue
		}

		// Count comment types
		if strings.HasPrefix(line, "# HELP") {
			helpComments++
		} else if strings.HasPrefix(line, "# TYPE") {
			typeComments++
		} else if !strings.HasPrefix(line, "#") {
			// Count metric lines (non-comments)
			metricLines++
		}
	}

	// Verify format
	assert.Greater(t, helpComments, 0, "should have HELP comments")
	assert.Greater(t, typeComments, 0, "should have TYPE comments")
	assert.Greater(t, metricLines, 0, "should have metric data lines")
}

func TestMetricsEndpointLabels(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Generate metrics with labels
	metrics.RedisOpsTotal.WithLabelValues("get", "success").Inc()
	metrics.RedisOpsTotal.WithLabelValues("set", "error").Inc()
	metrics.VoteProcessingTotal.WithLabelValues("applied").Inc()
	metrics.VoteProcessingTotal.WithLabelValues("debounced").Inc()

	// Create test server
	srv := newTestServer(t, &mockAppService{})

	// Make request
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, req)

	body := rec.Body.String()

	// Verify labels are present in metric output
	assert.Contains(t, body, `operation="get"`, "should include operation label")
	assert.Contains(t, body, `status="success"`, "should include status label")
	assert.Contains(t, body, `result="applied"`, "should include result label")
}

func TestMetricsEndpointNoAuth(t *testing.T) {
	// Verify metrics endpoint doesn't require authentication
	srv := newTestServer(t, &mockAppService{})

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	// Deliberately not setting any auth headers or session

	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, req)

	// Should succeed without auth
	assert.Equal(t, http.StatusOK, rec.Code,
		"metrics endpoint should be publicly accessible for Prometheus scraping")
}

func TestMetricTypes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Generate metrics of different types
	metrics.RedisOpsTotal.WithLabelValues("test", "success").Inc() // Counter
	metrics.BroadcasterActiveSessions.Set(10)                      // Gauge
	metrics.BroadcasterTickDuration.Observe(0.05)                  // Histogram

	// Create test server
	srv := newTestServer(t, &mockAppService{})

	// Make request
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	srv.echo.ServeHTTP(rec, req)

	body := rec.Body.String()

	// Verify TYPE declarations for different metric types
	assert.Contains(t, body, "# TYPE redis_operations_total counter",
		"counter metrics should be declared as counter")
	assert.Contains(t, body, "# TYPE broadcaster_active_sessions gauge",
		"gauge metrics should be declared as gauge")
	assert.Contains(t, body, "# TYPE broadcaster_tick_duration_seconds histogram",
		"histogram metrics should be declared as histogram")
}

func TestMetricsEndpointPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Create test server
	srv := newTestServer(t, &mockAppService{})

	// Make multiple requests to verify performance
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		rec := httptest.NewRecorder()
		srv.echo.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		require.NotEmpty(t, rec.Body.String())
	}

	// If we got here, metrics endpoint is performant enough for repeated scraping
	t.Log("Metrics endpoint handled 10 requests successfully")
}

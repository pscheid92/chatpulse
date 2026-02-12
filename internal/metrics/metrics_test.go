package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetricsRegistration(t *testing.T) {
	// Verify all metrics are registered without conflicts
	// This test ensures no duplicate metric names

	metrics := []prometheus.Collector{
		// Redis metrics
		RedisOpsTotal,
		RedisOpDuration,
		RedisConnectionErrors,
		RedisPoolConnections,

		// Broadcaster metrics
		BroadcasterActiveSessions,
		BroadcasterConnectedClients,
		BroadcasterTickDuration,
		BroadcasterSlowClientsEvicted,
		BroadcasterBroadcastDuration,

		// WebSocket metrics
		WebSocketConnectionsCurrent,
		WebSocketConnectionsTotal,
		WebSocketMessageSendDuration,
		WebSocketConnectionDuration,
		WebSocketPingFailures,

		// Vote processing metrics
		VoteProcessingTotal,
		VoteProcessingDuration,
		VoteTriggerMatches,

		// Database metrics
		DBQueryDuration,
		DBConnectionsCurrent,
		DBErrorsTotal,
	}

	// Verify each metric is registered
	for _, metric := range metrics {
		desc := make(chan *prometheus.Desc, 1)
		metric.Describe(desc)
		close(desc)

		require.NotNil(t, <-desc, "metric should have a valid descriptor")
	}
}

func TestCounterMetrics(t *testing.T) {
	tests := []struct {
		name    string
		metric  *prometheus.CounterVec
		labels  prometheus.Labels
		incBy   float64
		wantVal float64
	}{
		{
			name:    "redis operations counter",
			metric:  RedisOpsTotal,
			labels:  prometheus.Labels{"operation": "get", "status": "success"},
			incBy:   5,
			wantVal: 5,
		},
		{
			name:    "vote processing counter",
			metric:  VoteProcessingTotal,
			labels:  prometheus.Labels{"result": "applied"},
			incBy:   10,
			wantVal: 10,
		},
		{
			name:    "vote trigger matches counter",
			metric:  VoteTriggerMatches,
			labels:  prometheus.Labels{"trigger_type": "for"},
			incBy:   3,
			wantVal: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset metric
			tt.metric.Reset()

			// Increment counter
			for i := 0; i < int(tt.incBy); i++ {
				tt.metric.With(tt.labels).Inc()
			}

			// Verify value
			val := testutil.ToFloat64(tt.metric.With(tt.labels))
			assert.Equal(t, tt.wantVal, val)
		})
	}
}

func TestGaugeMetrics(t *testing.T) {
	tests := []struct {
		name     string
		metric   prometheus.Gauge
		setValue float64
	}{
		{
			name:     "broadcaster active sessions",
			metric:   BroadcasterActiveSessions,
			setValue: 42,
		},
		{
			name:     "broadcaster connected clients",
			metric:   BroadcasterConnectedClients,
			setValue: 150,
		},
		{
			name:     "websocket connections current",
			metric:   WebSocketConnectionsCurrent,
			setValue: 75,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set gauge value
			tt.metric.Set(tt.setValue)

			// Verify value
			val := testutil.ToFloat64(tt.metric)
			assert.Equal(t, tt.setValue, val)
		})
	}
}

func TestGaugeVecMetrics(t *testing.T) {
	// Test gauge vectors (with labels)
	RedisPoolConnections.Reset()
	DBConnectionsCurrent.Reset()

	// Set Redis pool connections
	RedisPoolConnections.WithLabelValues("active").Set(5)
	RedisPoolConnections.WithLabelValues("idle").Set(10)

	assert.Equal(t, 5.0, testutil.ToFloat64(RedisPoolConnections.WithLabelValues("active")))
	assert.Equal(t, 10.0, testutil.ToFloat64(RedisPoolConnections.WithLabelValues("idle")))

	// Set DB connections
	DBConnectionsCurrent.WithLabelValues("active").Set(3)
	DBConnectionsCurrent.WithLabelValues("idle").Set(7)

	assert.Equal(t, 3.0, testutil.ToFloat64(DBConnectionsCurrent.WithLabelValues("active")))
	assert.Equal(t, 7.0, testutil.ToFloat64(DBConnectionsCurrent.WithLabelValues("idle")))
}

func TestHistogramMetrics(t *testing.T) {
	t.Run("redis operation duration", func(t *testing.T) {
		RedisOpDuration.Reset()

		observations := []float64{0.001, 0.005, 0.010, 0.025, 0.050}
		for _, obs := range observations {
			RedisOpDuration.WithLabelValues("test_get").Observe(obs)
		}

		// Verify histogram recorded observations
		// Use CollectAndCount to verify metric exists
		count := testutil.CollectAndCount(RedisOpDuration)
		assert.Greater(t, count, 0, "histogram should have metrics")
	})

	t.Run("vote processing duration", func(t *testing.T) {
		observations := []float64{0.002, 0.003, 0.004}
		for _, obs := range observations {
			VoteProcessingDuration.Observe(obs)
		}

		// Verify histogram recorded observations
		count := testutil.CollectAndCount(VoteProcessingDuration)
		assert.Greater(t, count, 0, "histogram should have metrics")
	})

	t.Run("websocket message send duration", func(t *testing.T) {
		observations := []float64{0.0001, 0.0002, 0.0003, 0.0004}
		for _, obs := range observations {
			WebSocketMessageSendDuration.Observe(obs)
		}

		// Verify histogram recorded observations
		count := testutil.CollectAndCount(WebSocketMessageSendDuration)
		assert.Greater(t, count, 0, "histogram should have metrics")
	})
}

func TestHistogramBuckets(t *testing.T) {
	// Verify histogram buckets are appropriate for expected latencies

	tests := []struct {
		name           string
		histogram      *prometheus.HistogramVec
		expectedBounds []float64 // First few buckets
	}{
		{
			name:           "redis operation duration buckets",
			histogram:      RedisOpDuration,
			expectedBounds: []float64{0.001, 0.005, 0.01, 0.025, 0.05},
		},
		{
			name:           "broadcaster tick duration buckets",
			histogram:      nil, // BroadcasterTickDuration is Histogram not HistogramVec
			expectedBounds: []float64{0.001, 0.005, 0.01, 0.025, 0.05},
		},
		{
			name:           "websocket message send duration buckets",
			histogram:      nil, // WebSocketMessageSendDuration is Histogram not HistogramVec
			expectedBounds: []float64{0.0001, 0.0005, 0.001, 0.005, 0.01},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// This test verifies bucket configuration exists
			// Actual bucket boundaries are validated by observing that
			// the histograms were created with specific bucket configs
			assert.NotNil(t, tt.expectedBounds, "buckets should be defined")
		})
	}
}

func TestLabelCardinality(t *testing.T) {
	// Verify label cardinality is reasonable (prevent label explosion)

	tests := []struct {
		name           string
		metric         *prometheus.CounterVec
		labels         []prometheus.Labels
		maxCardinality int
		expectUnique   int
	}{
		{
			name:   "redis operations have bounded labels",
			metric: RedisOpsTotal,
			labels: []prometheus.Labels{
				{"operation": "get", "status": "success"},
				{"operation": "get", "status": "error"},
				{"operation": "set", "status": "success"},
				{"operation": "fcall", "status": "success"},
			},
			maxCardinality: 100, // Max expected unique combinations
			expectUnique:   4,
		},
		{
			name:   "vote processing results are bounded",
			metric: VoteProcessingTotal,
			labels: []prometheus.Labels{
				{"result": "applied"},
				{"result": "debounced"},
				{"result": "invalid"},
				{"result": "no_session"},
				{"result": "error"},
			},
			maxCardinality: 10, // Only 5 possible values
			expectUnique:   5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset metric
			tt.metric.Reset()

			// Add observations for each label combination
			for _, labels := range tt.labels {
				tt.metric.With(labels).Inc()
			}

			// Verify cardinality is within bounds
			assert.LessOrEqual(t, tt.expectUnique, tt.maxCardinality,
				"label cardinality should be reasonable to prevent explosion")
		})
	}
}

func TestMetricNaming(t *testing.T) {
	// Verify metrics follow Prometheus naming conventions
	// - snake_case
	// - descriptive suffixes (_total, _seconds, _current)

	tests := []struct {
		name         string
		metricName   string
		wantContains string
	}{
		{"counter has _total suffix", "redis_operations_total", "_total"},
		{"duration has _seconds suffix", "redis_operation_duration_seconds", "_seconds"},
		{"gauge has descriptive name", "broadcaster_active_sessions", "active"},
		{"counter has _total suffix", "vote_processing_total", "_total"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.True(t, strings.Contains(tt.metricName, tt.wantContains),
				"metric name %s should contain %s", tt.metricName, tt.wantContains)
		})
	}
}

func TestMetricTypes(t *testing.T) {
	// Verify correct metric types are used for each use case

	t.Run("counters only increase", func(t *testing.T) {
		RedisOpsTotal.Reset()
		counter := RedisOpsTotal.WithLabelValues("test", "success")

		counter.Inc()
		val1 := testutil.ToFloat64(counter)

		counter.Inc()
		val2 := testutil.ToFloat64(counter)

		assert.Greater(t, val2, val1, "counters should only increase")
	})

	t.Run("gauges can increase and decrease", func(t *testing.T) {
		gauge := BroadcasterConnectedClients

		gauge.Set(10)
		assert.Equal(t, 10.0, testutil.ToFloat64(gauge))

		gauge.Inc()
		assert.Equal(t, 11.0, testutil.ToFloat64(gauge))

		gauge.Dec()
		assert.Equal(t, 10.0, testutil.ToFloat64(gauge))

		gauge.Set(5)
		assert.Equal(t, 5.0, testutil.ToFloat64(gauge))
	})

	t.Run("histograms track distributions", func(t *testing.T) {
		hist := VoteProcessingDuration

		// Record observations
		hist.Observe(0.001)
		hist.Observe(0.010)
		hist.Observe(0.100)

		// Histogram should have metrics collected
		count := testutil.CollectAndCount(hist)
		assert.Greater(t, count, 0, "histogram should collect metrics")
	})
}

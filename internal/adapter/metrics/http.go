package metrics

import (
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/prometheus/client_golang/prometheus"
)

// HTTPMetrics holds Prometheus metrics for HTTP request tracking.
type HTTPMetrics struct {
	RequestDuration *prometheus.HistogramVec
	RequestsTotal   *prometheus.CounterVec
	InFlightGauge   prometheus.Gauge
}

// NewHTTPMetrics creates and registers HTTP metrics on the given registry.
func NewHTTPMetrics(reg prometheus.Registerer) *HTTPMetrics {
	m := &HTTPMetrics{
		RequestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: "http",
			Name:      "request_duration_seconds",
			Help:      "Duration of HTTP requests in seconds.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"method", "route", "status_code"}),
		RequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "http",
			Name:      "requests_total",
			Help:      "Total number of HTTP requests.",
		}, []string{"method", "route", "status_code"}),
		InFlightGauge: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: "http",
			Name:      "in_flight_requests",
			Help:      "Number of HTTP requests currently being processed.",
		}),
	}

	reg.MustRegister(m.RequestDuration, m.RequestsTotal, m.InFlightGauge)
	return m
}

// Middleware returns an Echo middleware that records HTTP metrics.
// It skips /metrics and /health/* endpoints.
func (m *HTTPMetrics) Middleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			path := c.Path()
			if path == "/metrics" || strings.HasPrefix(path, "/health/") {
				return next(c)
			}

			m.InFlightGauge.Inc()
			defer m.InFlightGauge.Dec()

			timer := prometheus.NewTimer(prometheus.ObserverFunc(func(v float64) {
				status := strconv.Itoa(c.Response().Status)
				m.RequestDuration.WithLabelValues(c.Request().Method, path, status).Observe(v)
				m.RequestsTotal.WithLabelValues(c.Request().Method, path, status).Inc()
			}))

			err := next(c)
			timer.ObserveDuration()
			return err
		}
	}
}

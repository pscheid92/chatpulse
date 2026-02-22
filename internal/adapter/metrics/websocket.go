package metrics

import "github.com/prometheus/client_golang/prometheus"

// WebSocketMetrics holds Prometheus metrics for WebSocket connections.
type WebSocketMetrics struct {
	ActiveConnections prometheus.Gauge
	MessagesPublished prometheus.Counter
}

// NewWebSocketMetrics creates and registers WebSocket metrics on the given registry.
func NewWebSocketMetrics(reg prometheus.Registerer) *WebSocketMetrics {
	m := &WebSocketMetrics{
		ActiveConnections: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: "websocket",
			Name:      "active_connections",
			Help:      "Number of active WebSocket connections.",
		}),
		MessagesPublished: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "websocket",
			Name:      "messages_published_total",
			Help:      "Total number of WebSocket messages published.",
		}),
	}

	reg.MustRegister(m.ActiveConnections, m.MessagesPublished)
	return m
}

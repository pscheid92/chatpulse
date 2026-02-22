package metrics

import "github.com/prometheus/client_golang/prometheus"

// CacheMetrics holds Prometheus metrics for config cache performance.
type CacheMetrics struct {
	Hits          *prometheus.CounterVec
	Misses        *prometheus.CounterVec
	Invalidations prometheus.Counter
}

// NewCacheMetrics creates and registers cache metrics on the given registry.
func NewCacheMetrics(reg prometheus.Registerer) *CacheMetrics {
	m := &CacheMetrics{
		Hits: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "config_cache",
			Name:      "hits_total",
			Help:      "Total number of config cache hits, by layer.",
		}, []string{"layer"}),
		Misses: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "config_cache",
			Name:      "misses_total",
			Help:      "Total number of config cache misses, by layer.",
		}, []string{"layer"}),
		Invalidations: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "config_cache",
			Name:      "invalidations_total",
			Help:      "Total number of config cache invalidations.",
		}),
	}

	reg.MustRegister(m.Hits, m.Misses, m.Invalidations)
	return m
}

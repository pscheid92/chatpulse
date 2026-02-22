package metrics

import "github.com/prometheus/client_golang/prometheus"

// VoteMetrics holds Prometheus metrics for the vote processing pipeline.
type VoteMetrics struct {
	VotesProcessed     *prometheus.CounterVec
	ProcessingDuration prometheus.Histogram
	VotesByTarget      *prometheus.CounterVec
}

// NewVoteMetrics creates and registers vote pipeline metrics on the given registry.
func NewVoteMetrics(reg prometheus.Registerer) *VoteMetrics {
	m := &VoteMetrics{
		VotesProcessed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "votes_processed_total",
			Help:      "Total number of votes processed, by result.",
		}, []string{"result"}),
		ProcessingDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "votes_processing_duration_seconds",
			Help:      "Duration of vote processing in seconds.",
			Buckets:   []float64{0.0005, 0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0},
		}),
		VotesByTarget: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "votes_by_target_total",
			Help:      "Total number of applied votes, by target direction.",
		}, []string{"target"}),
	}

	reg.MustRegister(m.VotesProcessed, m.ProcessingDuration, m.VotesByTarget)
	return m
}

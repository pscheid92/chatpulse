package database

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pscheid92/chatpulse/internal/metrics"
)

// MetricsTracer implements pgx.QueryTracer to collect database metrics
type MetricsTracer struct{}

var _ pgx.QueryTracer = (*MetricsTracer)(nil)

type queryContextKey struct{}

type queryContext struct {
	startTime time.Time
	queryName string
}

// TraceQueryStart is called at the start of a query
func (t *MetricsTracer) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	queryName := extractQueryName(data.SQL)
	qctx := queryContext{
		startTime: time.Now(),
		queryName: queryName,
	}
	return context.WithValue(ctx, queryContextKey{}, qctx)
}

// TraceQueryEnd is called at the end of a query
func (t *MetricsTracer) TraceQueryEnd(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryEndData) {
	// Extract query context
	qctx, ok := ctx.Value(queryContextKey{}).(queryContext)
	if !ok {
		return
	}

	duration := time.Since(qctx.startTime).Seconds()

	// Track query duration
	metrics.DBQueryDuration.WithLabelValues(qctx.queryName).Observe(duration)

	// Track errors
	if data.Err != nil {
		metrics.DBErrorsTotal.WithLabelValues(qctx.queryName).Inc()
	}
}

// extractQueryName extracts a simplified query name from SQL for metric labels
// This prevents high cardinality by grouping similar queries
func extractQueryName(sql string) string {
	if len(sql) == 0 {
		return "unknown"
	}

	// Extract first word (SELECT, INSERT, UPDATE, DELETE, etc.)
	for i, c := range sql {
		if c == ' ' || c == '\n' || c == '\t' {
			if i > 0 {
				return sql[:i]
			}
			break
		}
	}

	// If no space found, return first 20 chars
	if len(sql) > 20 {
		return sql[:20]
	}
	return sql
}

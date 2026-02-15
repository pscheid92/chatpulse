package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Redis Operations Metrics
var (
	// RedisOpsTotal tracks total Redis operations by operation type and status
	RedisOpsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "redis_operations_total",
			Help: "Total Redis operations by operation and status",
		},
		[]string{"operation", "status"},
	)

	// RedisOpDuration tracks Redis operation latency in seconds
	RedisOpDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "redis_operation_duration_seconds",
			Help:    "Redis operation duration in seconds",
			Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1},
		},
		[]string{"operation"},
	)

	// RedisConnectionErrors tracks Redis connection errors
	RedisConnectionErrors = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "redis_connection_errors_total",
			Help: "Total Redis connection errors",
		},
	)

	// RedisPoolConnections tracks current Redis pool connections by state
	RedisPoolConnections = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "redis_pool_connections_current",
			Help: "Current Redis pool connections by state (active/idle)",
		},
		[]string{"state"},
	)

	// CircuitBreakerStateChanges tracks circuit breaker state transitions
	CircuitBreakerStateChanges = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "circuit_breaker_state_changes_total",
			Help: "Circuit breaker state transitions by component and new state",
		},
		[]string{"component", "state"},
	)

	// CircuitBreakerState tracks current circuit breaker state (0=closed, 1=half-open, 2=open)
	CircuitBreakerState = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "circuit_breaker_state",
			Help: "Current circuit breaker state (0=closed, 1=half-open, 2=open)",
		},
		[]string{"component"},
	)
)

// Broadcaster Metrics
var (
	// BroadcasterActiveSessions tracks number of active broadcast sessions
	BroadcasterActiveSessions = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "broadcaster_active_sessions",
			Help: "Number of active broadcast sessions",
		},
	)

	// BroadcasterConnectedClients tracks total number of connected WebSocket clients
	BroadcasterConnectedClients = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "broadcaster_connected_clients_total",
			Help: "Total number of connected WebSocket clients across all sessions",
		},
	)

	// BroadcasterSlowClientsEvicted tracks number of slow clients evicted
	BroadcasterSlowClientsEvicted = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "broadcaster_slow_clients_evicted_total",
			Help: "Total number of slow WebSocket clients evicted due to buffer full",
		},
	)

	// BroadcasterPanicsTotal tracks broadcaster panic recoveries
	BroadcasterPanicsTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "broadcaster_panics_total",
			Help: "Total broadcaster panic recoveries",
		},
	)

	// BroadcasterCommandChannelDepth tracks current command channel depth
	BroadcasterCommandChannelDepth = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "broadcaster_command_channel_depth",
			Help: "Current command channel depth",
		},
	)

	// BroadcasterStopTimeoutsTotal tracks broadcaster stops that exceeded timeout
	BroadcasterStopTimeoutsTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "broadcaster_stop_timeouts_total",
			Help: "Broadcaster stops that exceeded timeout",
		},
	)

	// BroadcasterCallbackErrorsTotal tracks onFirstClient callback errors
	BroadcasterCallbackErrorsTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "broadcaster_callback_errors_total",
			Help: "onFirstClient callback errors",
		},
	)

	// PubSubMessageLatency tracks time from pub/sub receive to WebSocket fan-out
	PubSubMessageLatency = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "pubsub_message_latency_seconds",
			Help:    "Latency from pub/sub message receive to WebSocket client send",
			Buckets: []float64{.0001, .0005, .001, .005, .01, .025, .05, .1, .25},
		},
	)

	// PubSubReconnectionsTotal tracks pub/sub reconnection attempts
	PubSubReconnectionsTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "pubsub_reconnections_total",
			Help: "Total pub/sub reconnection attempts after disconnect",
		},
	)

	// PubSubSubscriptionActive tracks whether the pub/sub subscription is active (1) or disconnected (0)
	PubSubSubscriptionActive = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "pubsub_subscription_active",
			Help: "1 if pub/sub subscription is active, 0 if disconnected",
		},
	)
)

// WebSocket Metrics
var (
	// WebSocketConnectionsCurrent tracks current active WebSocket connections
	WebSocketConnectionsCurrent = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "websocket_connections_current",
			Help: "Current number of active WebSocket connections",
		},
	)

	// WebSocketConnectionsTotal tracks total WebSocket connection attempts by result
	WebSocketConnectionsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "websocket_connections_total",
			Help: "Total WebSocket connection attempts by result (success/error/rejected)",
		},
		[]string{"result"},
	)

	// WebSocketMessageSendDuration tracks WebSocket message send duration
	WebSocketMessageSendDuration = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "websocket_message_send_duration_seconds",
			Help:    "WebSocket message send duration in seconds",
			Buckets: []float64{.0001, .0005, .001, .005, .01, .025, .05, .1, .25},
		},
	)

	// WebSocketConnectionDuration tracks WebSocket connection duration
	WebSocketConnectionDuration = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "websocket_connection_duration_seconds",
			Help:    "WebSocket connection duration in seconds",
			Buckets: []float64{1, 5, 10, 30, 60, 300, 600, 1800, 3600},
		},
	)

	// WebSocketPingFailures tracks WebSocket ping failures
	WebSocketPingFailures = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "websocket_ping_failures_total",
			Help: "Total WebSocket ping failures (client not responding)",
		},
	)

	// WebSocketConnectionsRejected tracks rejected connection attempts by reason
	WebSocketConnectionsRejected = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "websocket_connections_rejected_total",
			Help: "Total WebSocket connections rejected by reason (rate_limit/ip_limit/global_limit)",
		},
		[]string{"reason"},
	)

	// WebSocketConnectionCapacity tracks current connection capacity utilization as percentage
	WebSocketConnectionCapacity = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "websocket_connection_capacity_percent",
			Help: "Current WebSocket connection capacity utilization (0-100%)",
		},
	)

	// WebSocketUniqueIPs tracks number of unique IP addresses with active connections
	WebSocketUniqueIPs = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "websocket_unique_ips",
			Help: "Number of unique IP addresses with active WebSocket connections",
		},
	)

	// WebSocketIdleDisconnects tracks disconnects due to idle timeout
	WebSocketIdleDisconnects = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "websocket_idle_disconnects_total",
			Help: "Total WebSocket connections closed due to idle timeout (>5 minutes no pong)",
		},
	)
)

// Vote Processing Metrics
var (
	// VoteProcessingTotal tracks total votes processed by result
	VoteProcessingTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "vote_processing_total",
			Help: "Total votes processed by result (applied/no_session/no_match/debounced/rate_limited/error)",
		},
		[]string{"result"},
	)

	// VoteProcessingDuration tracks vote processing latency
	VoteProcessingDuration = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "vote_processing_duration_seconds",
			Help:    "Vote processing duration in seconds",
			Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5},
		},
	)

	// VoteTriggerMatches tracks trigger matches by type
	VoteTriggerMatches = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "vote_trigger_matches_total",
			Help: "Total vote trigger matches by type (for/against)",
		},
		[]string{"trigger_type"},
	)
)

// EventSub Metrics
var (
	// EventSubSetupFailuresTotal tracks EventSub setup failures at startup
	EventSubSetupFailuresTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "eventsub_setup_failures_total",
			Help: "Total number of EventSub setup failures at startup (app continues without webhooks)",
		},
	)

	// EventSubSubscribeAttemptsTotal tracks subscribe attempts by result
	EventSubSubscribeAttemptsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "eventsub_subscribe_attempts_total",
			Help: "Total EventSub subscribe attempts by result",
		},
		[]string{"result"}, // "success", "exhausted", "permanent_error"
	)

	// EventSubStaleSubscriptionsTotal tracks stale subscriptions detected
	EventSubStaleSubscriptionsTotal = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "eventsub_stale_subscriptions_total",
			Help: "Total stale subscriptions detected (no webhooks >10min)",
		},
	)
)

// Config Cache Metrics
var (
	// ConfigCacheHits tracks successful config cache hits
	ConfigCacheHits = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "config_cache_hits_total",
			Help: "Total number of config cache hits",
		},
	)

	// ConfigCacheMisses tracks local in-memory config cache misses
	ConfigCacheMisses = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "config_cache_misses_total",
			Help: "Total number of local config cache misses",
		},
	)

	// ConfigCacheRedisHits tracks config fetches served from Redis cache
	ConfigCacheRedisHits = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "config_cache_redis_hits_total",
			Help: "Total number of config lookups served from Redis cache",
		},
	)

	// ConfigCachePostgresHits tracks config fetches that fell through to PostgreSQL
	ConfigCachePostgresHits = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "config_cache_postgres_hits_total",
			Help: "Total number of config lookups that fell through to PostgreSQL",
		},
	)

	// ConfigCacheSize tracks current number of entries in cache
	ConfigCacheSize = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "config_cache_entries",
			Help: "Current number of entries in config cache",
		},
	)

	// ConfigCacheEvictions tracks number of expired entries evicted
	ConfigCacheEvictions = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "config_cache_evictions_total",
			Help: "Total number of expired config cache entries evicted",
		},
	)

	// RedisUpdateFailures tracks Redis config update failures
	RedisUpdateFailures = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "redis_update_failures_total",
			Help: "Total Redis config update failures in SaveConfig",
		},
		[]string{"reason"},
	)
)

// Database Metrics
var (
	// DBQueryDuration tracks database query duration by query name
	DBQueryDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "db_query_duration_seconds",
			Help:    "Database query duration in seconds",
			Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1},
		},
		[]string{"query"},
	)

	// DBConnectionsCurrent tracks current database connections by state
	DBConnectionsCurrent = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "db_connections_current",
			Help: "Current database connections by state (active/idle)",
		},
		[]string{"state"},
	)

	// DBErrorsTotal tracks database errors by query name
	DBErrorsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "db_errors_total",
			Help: "Total database errors by query",
		},
		[]string{"query"},
	)
)

// Build Information Metrics
var (
	// BuildInfo is a gauge that always returns 1, with build metadata as labels
	BuildInfo = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "build_info",
			Help: "Build information with version, commit, build_time, and go_version labels (value is always 1)",
		},
		[]string{"version", "commit", "build_time", "go_version"},
	)
)

// Instance Coordination Metrics
var (
	// InstanceRegistrySize tracks number of active instances in the registry
	InstanceRegistrySize = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "instance_registry_size",
			Help: "Number of active instances in the registry",
		},
	)

	// PubSubMessagesReceived tracks config invalidation messages received
	PubSubMessagesReceived = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "pubsub_messages_received_total",
			Help: "Total pub/sub messages received by channel",
		},
		[]string{"channel"},
	)

	// LeaderElections tracks successful leader elections by key
	LeaderElections = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "leader_elections_total",
			Help: "Total successful leader elections by key",
		},
		[]string{"key"},
	)

	// IsLeader tracks whether this instance is the leader for a given key
	IsLeader = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "is_leader",
			Help: "1 if this instance is the leader for the given key, 0 otherwise",
		},
		[]string{"key"},
	)
)

// HTTP Request Metrics
// Note: These are automatically provided by echoprometheus middleware
// - http_requests_total{method, path, status}
// - http_request_duration_seconds{method, path}

// HTTP Error Metrics
// Note: http_errors_total{type} is provided by internal/errors package

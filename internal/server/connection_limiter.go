package server

import (
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

// GlobalConnectionLimiter limits total concurrent connections per instance.
// Uses atomic operations for lock-free counting.
type GlobalConnectionLimiter struct {
	current atomic.Int64
	max     int64
}

// NewGlobalConnectionLimiter creates a limiter with the specified maximum connections.
func NewGlobalConnectionLimiter(max int64) *GlobalConnectionLimiter {
	return &GlobalConnectionLimiter{max: max}
}

// Acquire attempts to acquire a connection slot.
// Returns true if successful, false if at capacity.
func (l *GlobalConnectionLimiter) Acquire() bool {
	for {
		current := l.current.Load()
		if current >= l.max {
			return false
		}
		if l.current.CompareAndSwap(current, current+1) {
			return true
		}
	}
}

// Release releases a connection slot.
func (l *GlobalConnectionLimiter) Release() {
	l.current.Add(-1)
}

// Current returns the current number of connections.
func (l *GlobalConnectionLimiter) Current() int64 {
	return l.current.Load()
}

// Max returns the maximum allowed connections.
func (l *GlobalConnectionLimiter) Max() int64 {
	return l.max
}

// CapacityPct returns the current capacity utilization as a percentage.
func (l *GlobalConnectionLimiter) CapacityPct() float64 {
	if l.max == 0 {
		return 0
	}
	return float64(l.Current()) / float64(l.max) * 100
}

// IPConnectionLimiter limits concurrent connections per IP address.
// Protects against single-source attacks.
type IPConnectionLimiter struct {
	mu     sync.RWMutex
	ips    map[string]int
	maxPer int
}

// NewIPConnectionLimiter creates a limiter with the specified per-IP maximum.
func NewIPConnectionLimiter(maxPer int) *IPConnectionLimiter {
	return &IPConnectionLimiter{
		ips:    make(map[string]int),
		maxPer: maxPer,
	}
}

// Acquire attempts to acquire a connection slot for the given IP.
// Returns true if successful, false if IP is at its limit.
func (l *IPConnectionLimiter) Acquire(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.ips[ip] >= l.maxPer {
		return false
	}
	l.ips[ip]++
	return true
}

// Release releases a connection slot for the given IP.
func (l *IPConnectionLimiter) Release(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if count := l.ips[ip]; count > 0 {
		l.ips[ip] = count - 1
		if l.ips[ip] == 0 {
			delete(l.ips, ip)
		}
	}
}

// Count returns the current connection count for the given IP.
func (l *IPConnectionLimiter) Count(ip string) int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.ips[ip]
}

// UniqueIPs returns the number of unique IPs with active connections.
func (l *IPConnectionLimiter) UniqueIPs() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.ips)
}

// MaxPer returns the maximum connections allowed per IP.
func (l *IPConnectionLimiter) MaxPer() int {
	return l.maxPer
}

// ConnectionRateLimiter limits the rate of new connections per IP.
// Uses token bucket algorithm via golang.org/x/time/rate.
type ConnectionRateLimiter struct {
	mu        sync.Mutex
	limiters  map[string]*rateLimiterEntry
	rate      rate.Limit
	burst     int
	cleanupAt time.Time
}

type rateLimiterEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// NewConnectionRateLimiter creates a rate limiter with the specified connections per second and burst.
// connectionsPerSecond: sustained rate (e.g., 10.0 for 10/sec)
// burst: maximum burst size (e.g., 10 to allow 10 immediate connections)
func NewConnectionRateLimiter(connectionsPerSecond float64, burst int) *ConnectionRateLimiter {
	return &ConnectionRateLimiter{
		limiters:  make(map[string]*rateLimiterEntry),
		rate:      rate.Limit(connectionsPerSecond),
		burst:     burst,
		cleanupAt: time.Now().Add(5 * time.Minute),
	}
}

// Allow checks if a new connection from the given IP should be allowed.
// Returns true if allowed (token available), false if rate limited.
func (l *ConnectionRateLimiter) Allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Periodic cleanup of inactive limiters (every 5 minutes)
	if time.Now().After(l.cleanupAt) {
		l.cleanup()
		l.cleanupAt = time.Now().Add(5 * time.Minute)
	}

	entry, exists := l.limiters[ip]
	if !exists {
		entry = &rateLimiterEntry{
			limiter:  rate.NewLimiter(l.rate, l.burst),
			lastSeen: time.Now(),
		}
		l.limiters[ip] = entry
	}

	entry.lastSeen = time.Now()
	return entry.limiter.Allow()
}

// cleanup removes limiters that haven't been used in 10 minutes.
// Must be called with mu held.
func (l *ConnectionRateLimiter) cleanup() {
	cutoff := time.Now().Add(-10 * time.Minute)
	for ip, entry := range l.limiters {
		if entry.lastSeen.Before(cutoff) {
			delete(l.limiters, ip)
		}
	}
}

// ActiveLimiters returns the number of active rate limiters.
func (l *ConnectionRateLimiter) ActiveLimiters() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.limiters)
}

// Rate returns the configured rate limit (connections per second).
func (l *ConnectionRateLimiter) Rate() float64 {
	return float64(l.rate)
}

// Burst returns the configured burst size.
func (l *ConnectionRateLimiter) Burst() int {
	return l.burst
}

// ConnectionLimits combines all three limiters into a single interface.
type ConnectionLimits struct {
	global *GlobalConnectionLimiter
	perIP  *IPConnectionLimiter
	rate   *ConnectionRateLimiter
}

// NewConnectionLimits creates a combined connection limiter.
func NewConnectionLimits(globalMax int64, perIPMax int, connectionsPerSecond float64, burst int) *ConnectionLimits {
	return &ConnectionLimits{
		global: NewGlobalConnectionLimiter(globalMax),
		perIP:  NewIPConnectionLimiter(perIPMax),
		rate:   NewConnectionRateLimiter(connectionsPerSecond, burst),
	}
}

// LimitReason describes why a connection was rejected.
type LimitReason string

const (
	LimitReasonGlobal LimitReason = "global_limit"
	LimitReasonPerIP  LimitReason = "per_ip_limit"
	LimitReasonRate   LimitReason = "rate_limit"
)

// Acquire attempts to acquire all three limits for the given IP.
// Returns true and empty reason if successful.
// Returns false and the reason if any limit is exceeded.
func (l *ConnectionLimits) Acquire(ip string) (bool, LimitReason) {
	// Check rate limit first (cheapest check)
	if !l.rate.Allow(ip) {
		return false, LimitReasonRate
	}

	// Check global limit
	if !l.global.Acquire() {
		return false, LimitReasonGlobal
	}

	// Check per-IP limit
	if !l.perIP.Acquire(ip) {
		l.global.Release() // Rollback global
		return false, LimitReasonPerIP
	}

	return true, ""
}

// Release releases all limits for the given IP.
func (l *ConnectionLimits) Release(ip string) {
	l.perIP.Release(ip)
	l.global.Release()
}

// Global returns the global connection limiter.
func (l *ConnectionLimits) Global() *GlobalConnectionLimiter {
	return l.global
}

// PerIP returns the per-IP connection limiter.
func (l *ConnectionLimits) PerIP() *IPConnectionLimiter {
	return l.perIP
}

// Rate returns the connection rate limiter.
func (l *ConnectionLimits) Rate() *ConnectionRateLimiter {
	return l.rate
}

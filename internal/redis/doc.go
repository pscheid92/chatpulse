// Package redis implements Redis-backed repositories and stores.
//
// Provides SessionRepository (session lifecycle + ref counting), SentimentStore (vote application + decay),
// Debouncer (per-user rate limiting), and VoteRateLimiter (token bucket rate limiting). Uses Redis Functions for atomic operations.
package redis

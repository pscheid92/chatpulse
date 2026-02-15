// Package redis implements Redis-backed repositories and stores.
//
// Provides SessionRepository (session lifecycle + ref counting), SentimentStore (vote application + decay),
// and Debouncer (per-user rate limiting). Uses Redis Functions for atomic operations.
package redis

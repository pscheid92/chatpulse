// Package broadcast implements the WebSocket broadcaster using the actor pattern.
//
// The Broadcaster subscribes to Redis pub/sub for sentiment updates and fans out to connected WebSocket clients.
// Uses single goroutine + command channel (no mutexes). Per-connection write goroutines handle slow clients gracefully.
// A health-check tick (5s) handles connection liveness only; data flow is push-based via pub/sub.
package broadcast

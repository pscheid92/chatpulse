// Package broadcast implements the WebSocket broadcaster using the actor pattern.
//
// The Broadcaster pulls current sentiment values from Redis on a 50ms tick and fans out to connected clients.
// Uses single goroutine + command channel (no mutexes). Per-connection write goroutines handle slow clients gracefully.
package broadcast

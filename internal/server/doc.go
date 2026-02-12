// Package server implements the HTTP server using Echo framework.
//
// Routes: auth (OAuth), dashboard (config UI), overlay (WebSocket), API (reset/rotate), webhooks (EventSub).
// Handlers split by domain: handlers_auth.go, handlers_dashboard.go, handlers_api.go, handlers_overlay.go.
package server

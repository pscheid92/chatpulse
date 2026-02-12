// Package app provides the application service layer.
//
// Orchestrates use cases: session activation, config saves, overlay UUID rotation, orphan cleanup.
// Sits between HTTP handlers and domain repositories. Depends on domain interfaces, not concrete implementations.
package app

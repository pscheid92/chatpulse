// Package domain defines the core domain types and interfaces.
//
// This package contains concept-oriented files (errors.go, user.go, config.go, session.go, etc.)
// with shared types and cross-cutting interfaces. No implementation code - just contracts.
// Prevents circular imports by keeping interfaces on the consumer side.
package domain

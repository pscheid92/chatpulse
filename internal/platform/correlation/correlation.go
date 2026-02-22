package correlation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
)

type contextKey struct{}

// NewID generates an 8-character hex correlation ID (4 random bytes).
func NewID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// WithID returns a new context carrying the given correlation ID.
func WithID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

// ID extracts the correlation ID from ctx, returning ("", false) if not present.
func ID(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(contextKey{}).(string)
	return id, ok && id != ""
}

// Handler wraps an existing slog.Handler to automatically inject a
// "correlation_id" attribute when the context carries one.
type Handler struct {
	inner slog.Handler
}

// NewHandler creates a correlation-aware handler wrapping the given handler.
func NewHandler(inner slog.Handler) *Handler {
	return &Handler{inner: inner}
}

func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	if id, ok := ID(ctx); ok {
		r.AddAttrs(slog.String("correlation_id", id))
	}
	if err := h.inner.Handle(ctx, r); err != nil {
		return fmt.Errorf("correlation handler: %w", err)
	}
	return nil
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &Handler{inner: h.inner.WithAttrs(attrs)}
}

func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{inner: h.inner.WithGroup(name)}
}

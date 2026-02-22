package correlation

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewID_Length(t *testing.T) {
	id := NewID()
	assert.Len(t, id, 8)
}

func TestNewID_Unique(t *testing.T) {
	ids := make(map[string]struct{}, 100)
	for range 100 {
		ids[NewID()] = struct{}{}
	}
	assert.Len(t, ids, 100)
}

func TestWithID_and_ID_Roundtrip(t *testing.T) {
	ctx := WithID(context.Background(), "abc12345")
	id, ok := ID(ctx)
	assert.True(t, ok)
	assert.Equal(t, "abc12345", id)
}

func TestID_Missing(t *testing.T) {
	id, ok := ID(context.Background())
	assert.False(t, ok)
	assert.Empty(t, id)
}

func TestID_EmptyString(t *testing.T) {
	ctx := WithID(context.Background(), "")
	id, ok := ID(ctx)
	assert.False(t, ok)
	assert.Empty(t, id)
}

func TestHandler_AddsCorrelationID(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	handler := NewHandler(inner)
	logger := slog.New(handler)

	ctx := WithID(context.Background(), "test1234")
	logger.InfoContext(ctx, "test message", "key", "value")

	output := buf.String()
	assert.Contains(t, output, "correlation_id=test1234")
	assert.Contains(t, output, "key=value")
	assert.Contains(t, output, "test message")
}

func TestHandler_NoCorrelationID_WhenMissing(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	handler := NewHandler(inner)
	logger := slog.New(handler)

	logger.InfoContext(context.Background(), "no correlation")

	output := buf.String()
	assert.NotContains(t, output, "correlation_id")
}

func TestHandler_WithAttrs_PreservesCorrelation(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	handler := NewHandler(inner)
	logger := slog.New(handler).With("component", "test")

	ctx := WithID(context.Background(), "attr1234")
	logger.InfoContext(ctx, "with attrs")

	output := buf.String()
	assert.Contains(t, output, "correlation_id=attr1234")
	assert.Contains(t, output, "component=test")
}

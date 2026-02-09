package sentiment

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jonboulle/clockwork"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Mock Store ---

type mockStore struct {
	config       *domain.ConfigSnapshot
	configErr    error
	decayedValue float64
	decayedErr   error

	lastDecayRate float64
	lastNowMs     int64
}

func (m *mockStore) GetSessionConfig(_ context.Context, _ uuid.UUID) (*domain.ConfigSnapshot, error) {
	return m.config, m.configErr
}

func (m *mockStore) GetDecayedValue(_ context.Context, _ uuid.UUID, decayRate float64, nowMs int64) (float64, error) {
	m.lastDecayRate = decayRate
	m.lastNowMs = nowMs
	return m.decayedValue, m.decayedErr
}

// Satisfy the rest of SessionStateStore — only GetSessionConfig and GetDecayedValue are used by Engine.
func (m *mockStore) ActivateSession(context.Context, uuid.UUID, string, domain.ConfigSnapshot) error {
	return nil
}
func (m *mockStore) ResumeSession(context.Context, uuid.UUID) error         { return nil }
func (m *mockStore) SessionExists(context.Context, uuid.UUID) (bool, error) { return false, nil }
func (m *mockStore) DeleteSession(context.Context, uuid.UUID) error         { return nil }
func (m *mockStore) GetSessionByBroadcaster(context.Context, string) (uuid.UUID, bool, error) {
	return uuid.Nil, false, nil
}
func (m *mockStore) GetSessionValue(context.Context, uuid.UUID) (float64, bool, error) {
	return 0, false, nil
}
func (m *mockStore) CheckDebounce(context.Context, uuid.UUID, string) (bool, error) {
	return false, nil
}
func (m *mockStore) ApplyVote(context.Context, uuid.UUID, float64, float64, int64) (float64, error) {
	return 0, nil
}
func (m *mockStore) ResetValue(context.Context, uuid.UUID) error       { return nil }
func (m *mockStore) MarkDisconnected(context.Context, uuid.UUID) error { return nil }
func (m *mockStore) UpdateConfig(context.Context, uuid.UUID, domain.ConfigSnapshot) error {
	return nil
}
func (m *mockStore) ListOrphans(_ context.Context, _ time.Duration) ([]uuid.UUID, error) {
	return nil, nil
}
func (m *mockStore) IncrRefCount(context.Context, uuid.UUID) (int64, error) { return 0, nil }
func (m *mockStore) DecrRefCount(context.Context, uuid.UUID) (int64, error) { return 0, nil }

// --- Tests ---

func TestGetCurrentValue_ReturnsDecayedValue(t *testing.T) {
	fakeClock := clockwork.NewFakeClock()
	store := &mockStore{
		config:       &domain.ConfigSnapshot{DecaySpeed: 0.5},
		decayedValue: 42.5,
	}
	engine := NewEngine(store, fakeClock)

	val, err := engine.GetCurrentValue(context.Background(), uuid.New())
	require.NoError(t, err)
	assert.Equal(t, 42.5, val)

	// Verify decay rate was passed correctly
	assert.Equal(t, 0.5, store.lastDecayRate)
	assert.Equal(t, fakeClock.Now().UnixMilli(), store.lastNowMs)
}

func TestGetCurrentValue_NoSession(t *testing.T) {
	fakeClock := clockwork.NewFakeClock()
	store := &mockStore{config: nil}
	engine := NewEngine(store, fakeClock)

	val, err := engine.GetCurrentValue(context.Background(), uuid.New())
	require.NoError(t, err)
	assert.Equal(t, 0.0, val)
}

func TestGetCurrentValue_UsesClockTime(t *testing.T) {
	fakeClock := clockwork.NewFakeClock()
	store := &mockStore{
		config:       &domain.ConfigSnapshot{DecaySpeed: 1.0},
		decayedValue: 10.0,
	}
	engine := NewEngine(store, fakeClock)

	expectedMs := fakeClock.Now().UnixMilli()
	_, err := engine.GetCurrentValue(context.Background(), uuid.New())
	require.NoError(t, err)
	assert.Equal(t, expectedMs, store.lastNowMs)
}

func TestGetCurrentValue_DecayMath(t *testing.T) {
	// Verify the mathematical relationship: value * exp(-decayRate * dt/1000)
	// This test demonstrates the expected behavior when store returns the correct value.
	decayRate := 0.5
	initialValue := 80.0
	dtSeconds := 2.0

	expected := initialValue * math.Exp(-decayRate*dtSeconds)

	fakeClock := clockwork.NewFakeClock()
	store := &mockStore{
		config:       &domain.ConfigSnapshot{DecaySpeed: decayRate},
		decayedValue: expected,
	}
	engine := NewEngine(store, fakeClock)

	val, err := engine.GetCurrentValue(context.Background(), uuid.New())
	require.NoError(t, err)
	assert.InDelta(t, expected, val, 0.001)
}

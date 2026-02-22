package retry_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pscheid92/chatpulse/internal/platform/retry"
)

var fastPolicy = retry.Policy{
	MaxAttempts:      3,
	InitialBackoff:   1 * time.Millisecond,
	RateLimitBackoff: 5 * time.Millisecond,
}

func TestDo_SuccessFirstAttempt(t *testing.T) {
	_, err := retry.Do(context.Background(), fastPolicy, alwaysRetry, func() (struct{}, error) {
		return struct{}{}, nil
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestDo_SuccessAfterRetries(t *testing.T) {
	calls := 0
	_, err := retry.Do(context.Background(), fastPolicy, alwaysRetry, func() (struct{}, error) {
		calls++
		if calls < 3 {
			return struct{}{}, errors.New("transient")
		}
		return struct{}{}, nil
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
}

func TestDo_ReturnsValue(t *testing.T) {
	calls := 0
	val, err := retry.Do(context.Background(), fastPolicy, alwaysRetry, func() (int, error) {
		calls++
		if calls < 2 {
			return 0, errors.New("transient")
		}
		return 42, nil
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if val != 42 {
		t.Fatalf("expected 42, got %d", val)
	}
}

func TestDo_PermanentErrorStopsImmediately(t *testing.T) {
	permanent := errors.New("permanent")
	calls := 0
	_, err := retry.Do(context.Background(), fastPolicy, alwaysStop, func() (struct{}, error) {
		calls++
		return struct{}{}, permanent
	})
	var permErr *retry.PermanentError
	if !errors.As(err, &permErr) {
		t.Fatalf("expected PermanentError, got %T: %v", err, err)
	}
	if !errors.Is(err, permanent) {
		t.Fatalf("expected wrapped permanent error, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}
}

func TestDo_ExhaustedRetries(t *testing.T) {
	underlying := errors.New("transient")
	calls := 0
	_, err := retry.Do(context.Background(), fastPolicy, alwaysRetry, func() (struct{}, error) {
		calls++
		return struct{}{}, underlying
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, underlying) {
		t.Fatalf("expected wrapped underlying error, got %v", err)
	}
	if calls != fastPolicy.MaxAttempts {
		t.Fatalf("expected %d calls, got %d", fastPolicy.MaxAttempts, calls)
	}
}

func TestDo_RateLimitBackoff(t *testing.T) {
	var observedBackoff time.Duration
	p := retry.Policy{
		MaxAttempts:      2,
		InitialBackoff:   1 * time.Millisecond,
		RateLimitBackoff: 5 * time.Millisecond,
		OnRetry: func(_ int, _ error, backoff time.Duration) {
			observedBackoff = backoff
		},
	}

	classify := func(error) retry.Action { return retry.After }

	_, _ = retry.Do(context.Background(), p, classify, func() (struct{}, error) {
		return struct{}{}, errors.New("rate limited")
	})

	if observedBackoff != 5*time.Millisecond {
		t.Fatalf("expected rate-limit backoff of 5ms, got %v", observedBackoff)
	}
}

func TestDo_ContextCancellationDuringWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	p := retry.Policy{
		MaxAttempts:      3,
		InitialBackoff:   10 * time.Second, // long enough that context cancels first
		RateLimitBackoff: 10 * time.Second,
	}

	calls := 0
	_, err := retry.Do(ctx, p, alwaysRetry, func() (struct{}, error) {
		calls++
		cancel() // cancel context after the first attempt
		return struct{}{}, errors.New("transient")
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call before cancel, got %d", calls)
	}
}

func TestDo_OnRetryCallback(t *testing.T) {
	var recorded []int
	p := fastPolicy
	p.OnRetry = func(attempt int, _ error, _ time.Duration) {
		recorded = append(recorded, attempt)
	}

	_, _ = retry.Do(context.Background(), p, alwaysRetry, func() (struct{}, error) {
		return struct{}{}, errors.New("fail")
	})

	// OnRetry should be called for attempts 1 and 2 (not 3, because that's exhaustion)
	expected := []int{1, 2}
	if len(recorded) != len(expected) {
		t.Fatalf("expected %d OnRetry calls, got %d", len(expected), len(recorded))
	}
	for i, v := range expected {
		if recorded[i] != v {
			t.Fatalf("OnRetry call %d: expected attempt %d, got %d", i, v, recorded[i])
		}
	}
}

func TestDoVoid_Success(t *testing.T) {
	calls := 0
	err := retry.DoVoid(context.Background(), fastPolicy, alwaysRetry, func() error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}
}

func TestDoVoid_PropagatesError(t *testing.T) {
	underlying := errors.New("fail")
	err := retry.DoVoid(context.Background(), fastPolicy, alwaysStop, func() error {
		return underlying
	})
	if !errors.Is(err, underlying) {
		t.Fatalf("expected wrapped underlying error, got %v", err)
	}
	var permErr *retry.PermanentError
	if !errors.As(err, &permErr) {
		t.Fatalf("expected PermanentError wrapper, got %T", err)
	}
}

func alwaysRetry(error) retry.Action { return retry.Retry }
func alwaysStop(error) retry.Action  { return retry.Stop }

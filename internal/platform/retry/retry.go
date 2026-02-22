package retry

import (
	"context"
	"fmt"
	"time"
)

type Action int

const (
	Stop  Action = iota // permanent error, abort immediately
	Retry               // transient error, use normal backoff
	After               // rate-limited, use longer backoff
)

type Policy struct {
	MaxAttempts      int
	InitialBackoff   time.Duration
	RateLimitBackoff time.Duration
	OnRetry          func(attempt int, err error, backoff time.Duration)
}

type Classify func(err error) Action
type Operation[T any] func() (T, error)
type VoidOperation func() error

func Do[T any](ctx context.Context, p Policy, classify Classify, op Operation[T]) (T, error) {
	backoff := p.InitialBackoff

	for attempt := 1; attempt <= p.MaxAttempts; attempt++ {
		val, err := op()
		if err == nil {
			return val, nil
		}

		action := classify(err)
		if action == Stop {
			var zero T
			return zero, &PermanentError{Err: err}
		}

		if attempt == p.MaxAttempts {
			var zero T
			return zero, fmt.Errorf("failed after %d attempts: %w", p.MaxAttempts, err)
		}

		if action == After {
			backoff = p.RateLimitBackoff
		}

		if p.OnRetry != nil {
			p.OnRetry(attempt, err, backoff)
		}

		select {
		case <-time.After(backoff):
			backoff *= 2
		case <-ctx.Done():
			var zero T
			return zero, fmt.Errorf("context cancelled during retry: %w", ctx.Err())
		}
	}

	panic("unreachable: MaxAttempts must be >= 1")
}

func DoVoid(ctx context.Context, p Policy, classify Classify, op VoidOperation) error {
	_, err := Do(ctx, p, classify, func() (struct{}, error) { return struct{}{}, op() })
	return err
}

type PermanentError struct {
	Err error
}

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

package domain

import "errors"

var (
	ErrUserNotFound         = errors.New("user not found")
	ErrConfigNotFound       = errors.New("config not found")
	ErrSubscriptionNotFound = errors.New("subscription not found")
	ErrSessionActive        = errors.New("session is active")
)

package domain

import "errors"

var (
	ErrStreamerNotFound     = errors.New("streamer not found")
	ErrConfigNotFound       = errors.New("config not found")
	ErrSubscriptionNotFound = errors.New("subscription not found")
)

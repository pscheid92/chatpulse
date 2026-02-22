package twitch

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/Its-donkey/kappopher/helix"
	"github.com/pscheid92/chatpulse/internal/platform/retry"
)

func TestClassifyEventSubError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want retry.Action
	}{
		{
			name: "429 rate limit returns After",
			err:  &helix.APIError{StatusCode: http.StatusTooManyRequests},
			want: retry.After,
		},
		{
			name: "500 internal server error returns Retry",
			err:  &helix.APIError{StatusCode: http.StatusInternalServerError},
			want: retry.Retry,
		},
		{
			name: "502 bad gateway returns Retry",
			err:  &helix.APIError{StatusCode: http.StatusBadGateway},
			want: retry.Retry,
		},
		{
			name: "503 service unavailable returns Retry",
			err:  &helix.APIError{StatusCode: http.StatusServiceUnavailable},
			want: retry.Retry,
		},
		{
			name: "400 bad request returns Stop",
			err:  &helix.APIError{StatusCode: http.StatusBadRequest},
			want: retry.Stop,
		},
		{
			name: "401 unauthorized returns Stop",
			err:  &helix.APIError{StatusCode: http.StatusUnauthorized},
			want: retry.Stop,
		},
		{
			name: "403 forbidden returns Stop",
			err:  &helix.APIError{StatusCode: http.StatusForbidden},
			want: retry.Stop,
		},
		{
			name: "404 not found returns Stop",
			err:  &helix.APIError{StatusCode: http.StatusNotFound},
			want: retry.Stop,
		},
		{
			name: "409 conflict returns Stop",
			err:  &helix.APIError{StatusCode: http.StatusConflict},
			want: retry.Stop,
		},
		{
			name: "non-API error returns Retry",
			err:  errors.New("connection refused"),
			want: retry.Retry,
		},
		{
			name: "wrapped non-API error returns Retry",
			err:  fmt.Errorf("request failed: %w", errors.New("timeout")),
			want: retry.Retry,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyEventSubError(tt.err)
			if got != tt.want {
				t.Errorf("classifyEventSubError() = %v, want %v", got, tt.want)
			}
		})
	}
}

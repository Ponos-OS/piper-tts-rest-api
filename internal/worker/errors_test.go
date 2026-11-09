package worker

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestJobError_Retryable(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   bool
	}{
		{"no HTTP response, for example a network error or a crash", 0, true},
		{"request timeout", http.StatusRequestTimeout, true},
		{"too many requests", http.StatusTooManyRequests, true},
		{"server error", http.StatusInternalServerError, true},
		{"service unavailable", http.StatusServiceUnavailable, true},
		{"bad request", http.StatusBadRequest, false},
		{"unauthorized", http.StatusUnauthorized, false},
		{"forbidden", http.StatusForbidden, false},
		{"not found", http.StatusNotFound, false},
		{"redirect", http.StatusFound, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, (&jobError{status: tt.status}).retryable())
		})
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, time.October, 21, 7, 28, 0, 0, time.UTC)
	seconds := func(n int) *time.Duration { d := time.Duration(n) * time.Second; return &d }
	tests := []struct {
		name  string
		value string
		want  *time.Duration
	}{
		{"seconds", "120", seconds(120)},
		{"zero", "0", seconds(0)},
		{"an HTTP date in the future", "Wed, 21 Oct 2026 07:28:30 GMT", seconds(30)},
		{"an HTTP date in the past", "Wed, 21 Oct 2026 07:20:00 GMT", seconds(0)},
		{"missing", "", nil},
		{"not a number or a date", "soon", nil},
		{"negative", "-5", nil},
		{"a huge number of seconds is capped", "99999999999999999", seconds(3600)},
		{"a far future date is capped", "Wed, 21 Oct 2099 07:28:00 GMT", seconds(3600)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, parseRetryAfter(tt.value, now))
		})
	}
}

func TestJobError_RetryDelayGrowsWithTheAttempt(t *testing.T) {
	err := &jobError{status: http.StatusServiceUnavailable}

	assert.Equal(t, 10*time.Second, err.retryDelay(10*time.Second, 1))
	assert.Equal(t, 30*time.Second, err.retryDelay(10*time.Second, 3))
}

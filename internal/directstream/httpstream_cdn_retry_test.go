package directstream

import (
	"net/http"
	"testing"
	"time"
)

func TestIsCDNTransientStatus(t *testing.T) {
	retry := []int{http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout}
	for _, s := range retry {
		if !isCDNTransientStatus(s) {
			t.Fatalf("status %d should be retried", s)
		}
	}
	noRetry := []int{200, 206, 301, 403, 404, 416, 500}
	for _, s := range noRetry {
		if isCDNTransientStatus(s) {
			t.Fatalf("status %d should NOT be retried", s)
		}
	}
}

func TestCDNBackoffDuration(t *testing.T) {
	// Exponential backoff, capped at 3s.
	if got := cdnBackoffDuration(0, ""); got != 300*time.Millisecond {
		t.Fatalf("attempt 0: got %v, want 300ms", got)
	}
	if got := cdnBackoffDuration(1, ""); got != 600*time.Millisecond {
		t.Fatalf("attempt 1: got %v, want 600ms", got)
	}
	if got := cdnBackoffDuration(5, ""); got != 3*time.Second {
		t.Fatalf("attempt 5: got %v, want 3s cap", got)
	}

	// Numeric Retry-After wins, capped at 5s.
	if got := cdnBackoffDuration(0, "2"); got != 2*time.Second {
		t.Fatalf("Retry-After 2: got %v, want 2s", got)
	}
	if got := cdnBackoffDuration(0, "100"); got != 5*time.Second {
		t.Fatalf("Retry-After 100: got %v, want 5s cap", got)
	}
	// Non-numeric / empty Retry-After falls back to backoff.
	if got := cdnBackoffDuration(0, "Wed, 21 Oct 2026 07:28:00 GMT"); got != 300*time.Millisecond {
		t.Fatalf("HTTP-date Retry-After: got %v, want backoff 300ms", got)
	}
}

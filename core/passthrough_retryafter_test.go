package bifrost

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestPassthrough429RetryAfterHint(t *testing.T) {
	response := &schemas.BifrostPassthroughResponse{
		StatusCode: 429,
		Headers:    map[string]string{"Retry-After": "1"},
		Body:       []byte(`{"error":{"code":"rate_limit_exceeded"}}`),
	}
	err := passthrough429RetryError(response)
	if err == nil {
		t.Fatal("expected retry error")
	}
	if got := err.ExtraFields.RetryAfter; got != 1000 {
		t.Fatalf("RetryAfter=%d, want 1000ms", got)
	}
}

func TestPassthroughRetryDelay(t *testing.T) {
	err := &schemas.BifrostError{}
	err.ExtraFields.RetryAfter = 2000
	if got := passthroughRetryDelay(100*time.Millisecond, err); got != 2*time.Second {
		t.Fatalf("got %s, want 2s", got)
	}
	if got := passthroughRetryDelay(3*time.Second, err); got != 3*time.Second {
		t.Fatalf("got %s, want 3s", got)
	}
	if got := passthroughRetryDelay(150*time.Millisecond, nil); got != 150*time.Millisecond {
		t.Fatalf("nil hint changed delay: %s", got)
	}
}

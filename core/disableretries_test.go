package bifrost

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestExecuteRequestWithRetries_DisableRetriesMakesOneAttempt pins
// BifrostContextKeyDisableRetries: with a retry budget configured, a retryable
// status, or a per-key failure that would otherwise walk to the next key, still
// produces exactly one upstream call, and its error is the one returned.
func TestExecuteRequestWithRetries_DisableRetriesMakesOneAttempt(t *testing.T) {
	cases := []struct {
		name   string
		status int
	}{
		{name: "retryable 503", status: 503},
		{name: "rate limit 429 rotates keys", status: 429},
		{name: "rejected credential 401 walks to the next key", status: 401},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			ctx.SetValue(schemas.BifrostContextKeyTracer, &schemas.NoOpTracer{})
			ctx.SetValue(schemas.BifrostContextKeyDisableRetries, true)
			keys := []schemas.Key{{ID: "k1", Value: *schemas.NewSecretVar("sk-1")}, {ID: "k2", Value: *schemas.NewSecretVar("sk-2")}}
			keyProvider := func(used, dead map[string]bool) (schemas.Key, error) {
				for _, key := range keys {
					if !used[key.ID] && !dead[key.ID] {
						return key, nil
					}
				}
				return keys[0], nil
			}
			calls := 0
			handler := func(schemas.Key) (string, *schemas.BifrostError) {
				calls++
				return "", createBifrostError("upstream failed", Ptr(tc.status), nil, false)
			}

			_, err := executeRequestWithRetries(ctx, createTestConfig(3, time.Millisecond, time.Millisecond), handler, keyProvider,
				schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4o", nil, NewDefaultLogger(schemas.LogLevelError))

			if calls != 1 {
				t.Fatalf("upstream calls got %d, want 1", calls)
			}
			if err == nil || err.StatusCode == nil || *err.StatusCode != tc.status {
				t.Fatalf("error got %v, want the first attempt's %d", err, tc.status)
			}
		})
	}
}

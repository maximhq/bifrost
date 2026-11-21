package bifrost

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestPassthrough429RetryError(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		headers   map[string]string
		body      string
		wantRetry bool
	}{
		{"temporary 429", 429, map[string]string{"Retry-After": "1"}, `{"error":{"code":"rate_limit_exceeded"}}`, true},
		{"retry forbidden 429", 429, map[string]string{"X-Should-Retry": "false"}, `{"error":{"code":"rate_limit_exceeded"}}`, false},
		{"retry forbidden mixed case", 429, map[string]string{"x-should-retry": " FALSE "}, `{"error":{"code":"rate_limit_exceeded"}}`, false},
		{"upstream reset refused", 429, map[string]string{}, `{"error":{"code":"upstream_reset_replay_refused"}}`, false},
		{"exhausted quota not retried", 429, map[string]string{}, `{"error":{"code":"insufficient_quota"}}`, false},
		{"non 429 unchanged", 400, map[string]string{}, `{"error":{"code":"invalid_request"}}`, false},
		{"successful unchanged", 200, map[string]string{}, `{"id":"resp_test"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &schemas.BifrostPassthroughResponse{StatusCode: tc.status, Headers: tc.headers, Body: []byte(tc.body)}
			err := passthrough429RetryError(p)
			if (err != nil) != tc.wantRetry {
				t.Fatalf("retry=%v want=%v", err != nil, tc.wantRetry)
			}
			if err != nil && (err.StatusCode == nil || *err.StatusCode != 429 || err.IsBifrostError) {
				t.Fatalf("invalid synthetic retry error: %+v", err)
			}
		})
	}
}

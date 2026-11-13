package bifrost

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// deadlineUpstream answers every request with 503 after hold, or earlier if the
// caller hangs up, and counts the requests it received per model.
type deadlineUpstream struct {
	hold   time.Duration
	mu     sync.Mutex
	models map[string]int
}

func newDeadlineUpstream(t *testing.T, hold time.Duration) (*deadlineUpstream, string) {
	t.Helper()
	u := &deadlineUpstream{hold: hold, models: map[string]int{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		u.mu.Lock()
		u.models[body.Model]++
		u.mu.Unlock()
		if u.hold > 0 {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(u.hold):
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"error":{"message":"overloaded","type":"overloaded_error"}}`)
	}))
	t.Cleanup(server.Close)
	return u, server.URL
}

func (u *deadlineUpstream) hits(model string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.models[model]
}

// TestRequestDeadlineCapsRetriesAndFallbacks pins that a deadline on the request context bounds the
// whole request: retries, the backoff between them and the fallback chain. Once it passes no further
// attempt or fallback is dispatched, and the caller gets a 504 request timeout.
func TestRequestDeadlineCapsRetriesAndFallbacks(t *testing.T) {
	const deadline = 400 * time.Millisecond
	// Generous slack for a loaded CI host; without the deadline these requests run for minutes.
	const bound = deadline + 1500*time.Millisecond

	for _, tc := range []struct {
		name string
		// Hold applied by each upstream before it answers 503.
		primaryHold, fallbackHold time.Duration
		primaryRetries            int
	}{
		// The primary fails fast and keeps being retried until the deadline passes in a backoff.
		{name: "deadline passes during retry backoff", primaryRetries: 1000},
		// The primary attempt itself outlives the deadline.
		{name: "deadline passes during a primary attempt", primaryHold: 5 * time.Second, primaryRetries: 1000},
		// The primary fails at once, and the deadline passes while the first fallback is running.
		{name: "deadline passes during a fallback", fallbackHold: 5 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			primary, primaryURL := newDeadlineUpstream(t, tc.primaryHold)
			fallback, fallbackURL := newDeadlineUpstream(t, tc.fallbackHold)

			account := NewMockAccount()
			account.AddProviderWithBaseURL(schemas.OpenAI, 4, 10, primaryURL)
			account.AddProviderWithBaseURL(schemas.Anthropic, 4, 10, fallbackURL)
			for provider, retries := range map[schemas.ModelProvider]int{schemas.OpenAI: tc.primaryRetries, schemas.Anthropic: 1000} {
				network := &account.configs[provider].NetworkConfig
				network.MaxRetries = retries
				network.RetryBackoffInitial = 50 * time.Millisecond
				network.RetryBackoffMax = 100 * time.Millisecond
				account.SetKeysForProvider(provider, []schemas.Key{
					{ID: string(provider) + "-key", Value: *schemas.NewSecretVar("sk-" + string(provider)), Models: schemas.WhiteList{"*"}, Weight: 100},
				})
			}
			client := newStreamTestClient(t, account)

			ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(deadline))
			defer ctx.Cancel()
			start := time.Now()
			_, bifrostErr := client.ChatCompletionRequest(ctx, &schemas.BifrostChatRequest{
				Provider: schemas.OpenAI,
				Model:    "gpt-4o-mini",
				Input: []schemas.ChatMessage{
					{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}},
				},
				Fallbacks: []schemas.Fallback{
					{Provider: schemas.Anthropic, Model: "claude-3-5-haiku-20241022"},
					{Provider: schemas.OpenAI, Model: "gpt-4o"},
				},
			})
			elapsed := time.Since(start)

			if elapsed > bound {
				t.Fatalf("request took %s, want it bounded by the %s deadline", elapsed, deadline)
			}
			if bifrostErr == nil {
				t.Fatal("request succeeded, want a timeout")
			}
			status := 0
			if bifrostErr.StatusCode != nil {
				status = *bifrostErr.StatusCode
			}
			if status != http.StatusGatewayTimeout || bifrostErr.Error == nil || bifrostErr.Error.Type == nil || *bifrostErr.Error.Type != schemas.RequestTimedOut {
				t.Fatalf("error = %d %q, want a 504 %s", status, bifrostErr.GetErrorString(), schemas.RequestTimedOut)
			}
			if got := primary.hits("gpt-4o"); got != 0 {
				t.Errorf("second fallback reached its upstream %d time(s) after the deadline", got)
			}
			if tc.fallbackHold == 0 {
				if got := fallback.hits("claude-3-5-haiku-20241022"); got != 0 {
					t.Errorf("first fallback reached its upstream %d time(s) after the deadline", got)
				}
			}
			var tried []string
			for _, entry := range ctx.GetRoutingEngineLogs() {
				if strings.HasPrefix(entry.Message, "Trying fallback") {
					tried = append(tried, entry.Message)
				}
			}
			wantTried := 0
			if tc.fallbackHold > 0 {
				wantTried = 1
			}
			if len(tried) != wantTried {
				t.Errorf("fallbacks tried = %q, want %d: none may start once the deadline has passed", tried, wantTried)
			}
		})
	}
}

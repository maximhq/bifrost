package bifrost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

func TestIsCallerFaultRefusal(t *testing.T) {
	errWith := func(status int, errType, message string) *schemas.BifrostError {
		return &schemas.BifrostError{
			StatusCode: schemas.Ptr(status),
			Error:      &schemas.ErrorField{Type: schemas.Ptr(errType), Message: message},
		}
	}
	for _, tc := range []struct {
		name string
		err  *schemas.BifrostError
		want bool
	}{
		{"nil", nil, false},
		{"400 invalid request", errWith(400, "invalid_request_error", "messages: field required"), true},
		{"422 validation", errWith(422, "invalid_request_error", "unprocessable"), true},
		{"413 stays open to another provider", errWith(413, "invalid_request_error", "payload too large"), false},
		{"409", errWith(409, "invalid_request_error", "conflict"), false},
		{"400 with no provider body", &schemas.BifrostError{StatusCode: schemas.Ptr(400)}, false},
		{"400 bad key (Gemini)", errWith(400, "INVALID_ARGUMENT", "API key not valid. Please pass a valid API key."), false},
		{"400 empty balance (Anthropic)", errWith(400, "invalid_request_error", "Your credit balance is too low to access the API"), false},
		{"400 retired model (Bedrock)", errWith(400, "ValidationException", "The model id is no longer available"), false},
		{"429", errWith(429, "rate_limit_error", "slow down"), false},
		{"500", errWith(500, "server_error", "boom"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isCallerFaultRefusal(tc.err); got != tc.want {
				t.Fatalf("isCallerFaultRefusal = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCallerFaultRefusalOfPrimaryEndsTheChain pins that a 400/422 the primary attributes to the request
// goes back to the caller without the fallbacks being tried, that the context key restores the old
// behaviour, and that a 400 which is really about the key still falls back.
func TestCallerFaultRefusalOfPrimaryEndsTheChain(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body       string
		optIn      bool
		wantServed bool
	}{
		{"400 invalid request", 400, `{"error":{"message":"messages: field required","type":"invalid_request_error"}}`, false, false},
		{"422 invalid request", 422, `{"error":{"message":"unprocessable","type":"invalid_request_error"}}`, false, false},
		{"400 with the opt-in", 400, `{"error":{"message":"messages: field required","type":"invalid_request_error"}}`, true, true},
		{"400 that is a bad key", 400, `{"error":{"message":"API key not valid. Please pass a valid API key.","type":"INVALID_ARGUMENT"}}`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var primaryHits, fallbackHits atomic.Int32
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				primaryHits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer primary.Close()
			fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fallbackHits.Add(1)
				anthropicMessageJSON(w, r)
			}))
			defer fallback.Close()

			account := NewMockAccount()
			account.AddProviderWithBaseURL(schemas.OpenAI, 1, 1, primary.URL)
			account.AddProviderWithBaseURL(schemas.Anthropic, 1, 1, fallback.URL)
			account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{{ID: "openai-key", Value: *schemas.NewSecretVar("sk-openai"), Models: schemas.WhiteList{"*"}, Weight: 1}})
			account.SetKeysForProvider(schemas.Anthropic, []schemas.Key{{ID: "anthropic-key", Value: *schemas.NewSecretVar("sk-anthropic"), Models: schemas.WhiteList{"*"}, Weight: 1}})
			client, err := Init(context.Background(), schemas.BifrostConfig{
				Account: account,
				Logger:  NewDefaultLogger(schemas.LogLevelError),
			})
			if err != nil {
				t.Fatalf("Init: %v", err)
			}
			t.Cleanup(client.Shutdown)

			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			if tc.optIn {
				ctx.SetValue(schemas.BifrostContextKeyFallbackOnCallerFault, true)
			}
			resp, bifrostErr := client.ChatCompletionRequest(ctx, &schemas.BifrostChatRequest{
				Provider:  schemas.OpenAI,
				Model:     "gpt-4o-mini",
				Input:     []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}},
				Fallbacks: []schemas.Fallback{{Provider: schemas.Anthropic, Model: "claude-3-5-haiku-20241022"}},
			})
			if tc.wantServed {
				if bifrostErr != nil || resp == nil || resp.ExtraFields.RoutingInfo.Provider != schemas.Anthropic {
					t.Fatalf("want the anthropic fallback to serve, got resp=%v err=%v", resp, bifrostErr)
				}
				if n := fallbackHits.Load(); n != 1 {
					t.Fatalf("fallback hits = %d, want 1", n)
				}
				return
			}
			if bifrostErr == nil || bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != tc.status {
				t.Fatalf("want the %d back, got resp=%v err=%v", tc.status, resp, bifrostErr)
			}
			if n := fallbackHits.Load(); n != 0 {
				t.Fatalf("the fallback ran %d time(s) after the primary refused the request", n)
			}
			if n := primaryHits.Load(); n != 1 {
				t.Fatalf("primary hits = %d, want 1", n)
			}
		})
	}
}

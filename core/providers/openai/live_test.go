package openai

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

var _ schemas.LiveProvider = (*OpenAIProvider)(nil)

func TestLiveWebSocketURL(t *testing.T) {
	t.Parallel()

	provider := &OpenAIProvider{networkConfig: schemas.NetworkConfig{BaseURL: "https://api.openai.com"}}
	cases := []struct {
		kind      schemas.LiveConnectionKind
		sessionID string
		want      string
	}{
		{schemas.LiveConnectionPrimary, "", "wss://api.openai.com/v1/live/sessions"},
		{schemas.LiveConnectionSideband, "live_123", "wss://api.openai.com/v1/live/sessions/live_123/attach"},
		{schemas.LiveConnectionFork, "live_123", "wss://api.openai.com/v1/live/sessions/live_123/fork"},
	}
	for _, tc := range cases {
		got, err := provider.LiveWebSocketURL(schemas.Key{}, tc.kind, tc.sessionID)
		if err != nil || got != tc.want {
			t.Fatalf("LiveWebSocketURL(%s, %q) = %q, %v; want %q", tc.kind, tc.sessionID, got, err, tc.want)
		}
	}

	local := &OpenAIProvider{networkConfig: schemas.NetworkConfig{BaseURL: "http://localhost:9000"}}
	if got, err := local.LiveWebSocketURL(schemas.Key{}, schemas.LiveConnectionPrimary, ""); err != nil || got != "ws://localhost:9000/v1/live/sessions" {
		t.Fatalf("LiveWebSocketURL() = %q, %v", got, err)
	}
}

func TestLiveWebSocketURLRejectsUnsafeSessionID(t *testing.T) {
	t.Parallel()

	provider := &OpenAIProvider{networkConfig: schemas.NetworkConfig{BaseURL: "https://api.openai.com"}}
	for _, sessionID := range []string{"", "..", "live_1/../../responses", "live_1%2Ffork", "live_1?x=1", "live_1#frag"} {
		for _, kind := range []schemas.LiveConnectionKind{schemas.LiveConnectionSideband, schemas.LiveConnectionFork} {
			if got, err := provider.LiveWebSocketURL(schemas.Key{}, kind, sessionID); err == nil {
				t.Fatalf("LiveWebSocketURL(%s, %q) = %q, want error", kind, sessionID, got)
			}
		}
	}
	if got, err := provider.LiveWebSocketURL(schemas.Key{}, schemas.LiveConnectionKind("bogus"), "live_123"); err == nil {
		t.Fatalf("LiveWebSocketURL(bogus) = %q, want error", got)
	}
}

func TestLiveWebSocketURLHonorsAllowedRequests(t *testing.T) {
	t.Parallel()

	blocked := &OpenAIProvider{
		networkConfig:        schemas.NetworkConfig{BaseURL: "https://api.openai.com"},
		customProviderConfig: &schemas.CustomProviderConfig{AllowedRequests: &schemas.AllowedRequests{Realtime: true}},
	}
	if got, err := blocked.LiveWebSocketURL(schemas.Key{}, schemas.LiveConnectionPrimary, ""); err == nil {
		t.Fatalf("LiveWebSocketURL() = %q, want unsupported operation error", got)
	}

	allowed := &OpenAIProvider{
		networkConfig:        schemas.NetworkConfig{BaseURL: "https://api.openai.com"},
		customProviderConfig: &schemas.CustomProviderConfig{AllowedRequests: &schemas.AllowedRequests{Live: true}},
	}
	if _, err := allowed.LiveWebSocketURL(schemas.Key{}, schemas.LiveConnectionPrimary, ""); err != nil {
		t.Fatalf("LiveWebSocketURL() error = %v", err)
	}
}

func TestLiveHeaders(t *testing.T) {
	t.Parallel()

	provider := &OpenAIProvider{networkConfig: schemas.NetworkConfig{ExtraHeaders: map[string]string{"X-Extra": "1"}}}
	headers, err := provider.LiveHeaders(nil, schemas.Key{Value: *schemas.NewSecretVar("sk-test")})
	if err != nil {
		t.Fatalf("LiveHeaders() error = %v", err)
	}
	if headers["Authorization"] != "Bearer sk-test" || headers["X-Extra"] != "1" {
		t.Fatalf("LiveHeaders() = %v", headers)
	}
}

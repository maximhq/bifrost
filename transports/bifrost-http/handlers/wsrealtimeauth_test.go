package handlers

import (
	"github.com/maximhq/bifrost/framework/kvstore"
	"github.com/valyala/fasthttp"
	"strings"
	"testing"
)

// TestRealtimeNativeSubprotocolAdmission exercises HTTP admission before model resolution using synthetic credentials, not a live voice session.
func TestRealtimeNativeSubprotocolAdmission(t *testing.T) {
	SetLogger(&mockLogger{})
	store, err := kvstore.New(kvstore.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, tc := range []struct {
		name, protocol string
		status         int
	}{
		{"xai mapped", "xai-client-secret.ek_bf_synthetic", 101},
		{"openai unchanged", "realtime, openai-insecure-api-key.ek_synthetic", 101},
		{"unknown", "future-protocol", 401},
		{"empty xai", "xai-client-secret.", 400},
		{"conflicting", "xai-client-secret.ek_first, openai-insecure-api-key.ek_second", 400},
		{"duplicate same", "xai-client-secret.ek_same, xai-client-secret.ek_same", 101},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := newRealtimeUpgradeRequest("/v1/realtime")
			ctx.Request.Header.Set("Sec-WebSocket-Protocol", tc.protocol)
			handler := newRealtimeUpgradeHandler(true)
			handler.config.KVStore = store
			handler.handleUpgrade(ctx)
			if ctx.Response.StatusCode() != tc.status {
				t.Fatalf("status %d, want %d", ctx.Response.StatusCode(), tc.status)
			}
		})
	}
}

// TestRealtimeInvalidNativeCredentialsRejectOpenDeployment prevents malformed credentials falling back to an operator key when authentication is optional.
func TestRealtimeInvalidNativeCredentialsRejectOpenDeployment(t *testing.T) {
	SetLogger(&mockLogger{})
	ctx := newRealtimeUpgradeRequest("/v1/realtime")
	ctx.Request.Header.Set("Sec-WebSocket-Protocol", "xai-client-secret.")
	newRealtimeUpgradeHandler(false).handleUpgrade(ctx)
	if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
		t.Fatal("invalid credential was treated as anonymous")
	}
}

// TestRealtimeNativeExplicitAuthorizationWins preserves the existing explicit-header precedence over browser credential protocols.
func TestRealtimeNativeExplicitAuthorizationWins(t *testing.T) {
	SetLogger(&mockLogger{})
	store, err := kvstore.New(kvstore.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := newRealtimeUpgradeRequest("/v1/realtime")
	ctx.Request.Header.Set("Authorization", "Bearer ek_explicit")
	ctx.Request.Header.Set("Sec-WebSocket-Protocol", "xai-client-secret.")
	handler := newRealtimeUpgradeHandler(true)
	handler.config.KVStore = store
	handler.handleUpgrade(ctx)
	if ctx.Response.StatusCode() != fasthttp.StatusSwitchingProtocols {
		t.Fatal("explicit authentication precedence changed")
	}
}

// TestRealtimeNativeCredentialParser preserves opaque token bytes and never exposes credentials in errors.
func TestRealtimeNativeCredentialParser(t *testing.T) {
	for _, tc := range []struct {
		protocol, want string
		invalid        bool
	}{
		{"  realtime , xai-client-secret.ek_opaque-ABC_123  ", "ek_opaque-ABC_123", false},
		{"unknown.ek_synthetic", "", false},
		{"xai-client-secret.ek_secret space", "", true},
		{"openai-insecure-api-key.ek_secret, xai-client-secret.ek_different", "", true},
	} {
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.Header.Set("Sec-WebSocket-Protocol", tc.protocol)
		token, err := extractRealtimeSubprotocolAPIKey(ctx)
		if (err != nil) != tc.invalid || token != tc.want {
			t.Fatal("unexpected credential parse result")
		}
		if err != nil && strings.Contains(err.Error(), "ek_") {
			t.Fatal("credential included in error")
		}
	}
}

// TestRealtimeAzureAPIKeyAdmission pins that Azure Voice Live and Azure OpenAI clients authenticate on the
// Azure paths with api-key, as a header or, from a browser, in the query string, and that other paths ignore it.
func TestRealtimeAzureAPIKeyAdmission(t *testing.T) {
	SetLogger(&mockLogger{})
	store, err := kvstore.New(kvstore.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, tc := range []struct {
		name, uri, header string
		status            int
	}{
		{"voice live header", "/azure/voice-live/realtime", "ek_synthetic", 101},
		{"voice live sdk root query", "/voice-live/realtime?api-key=ek_synthetic", "", 101},
		{"azure openai deployment header", "/azure/openai/realtime", "ek_synthetic", 101},
		{"azure openai v1 query", "/azure/openai/v1/realtime?api-key=ek_synthetic", "", 101},
		{"azure path without a key", "/azure/voice-live/realtime", "", 401},
		{"api-key off the azure paths", "/v1/realtime?api-key=ek_synthetic", "ek_synthetic", 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := newRealtimeUpgradeRequest(tc.uri)
			if tc.header != "" {
				ctx.Request.Header.Set("api-key", tc.header)
			}
			handler := newRealtimeUpgradeHandler(true)
			handler.config.KVStore = store
			handler.handleUpgrade(ctx)
			if ctx.Response.StatusCode() != tc.status {
				t.Fatalf("status %d, want %d", ctx.Response.StatusCode(), tc.status)
			}
		})
	}
}

// TestExtractRealtimeAzureAPIKey pins that the header wins over the query string.
func TestExtractRealtimeAzureAPIKey(t *testing.T) {
	ctx := newRealtimeUpgradeRequest("/azure/voice-live/realtime?api-key=sk-bf-query")
	ctx.Request.Header.Set("api-key", "sk-bf-header")
	if got := extractRealtimeAzureAPIKey(ctx, "/azure/voice-live/realtime"); got != "sk-bf-header" {
		t.Fatalf("api-key = %q, want the header value", got)
	}
	ctx.Request.Header.Del("api-key")
	if got := extractRealtimeAzureAPIKey(ctx, "/azure/voice-live/realtime"); got != "sk-bf-query" {
		t.Fatalf("api-key = %q, want the query value", got)
	}
}

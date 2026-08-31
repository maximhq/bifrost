package databricks

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// testLogger is a minimal logger for tests that implements schemas.Logger. The provider
// package cannot import the root core package (which imports it), so a local one is used.
type testLogger struct{}

func (testLogger) Debug(string, ...any)                   {}
func (testLogger) Info(string, ...any)                    {}
func (testLogger) Warn(string, ...any)                    {}
func (testLogger) Error(string, ...any)                   {}
func (testLogger) Fatal(string, ...any)                   {}
func (testLogger) SetLevel(schemas.LogLevel)              {}
func (testLogger) SetOutputType(schemas.LoggerOutputType) {}
func (testLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

// TestOAuthM2M covers the service principal path end to end: the token is minted from the
// workspace OIDC endpoint with a client_credentials grant and HTTP Basic auth, then used as
// the bearer token; and a second request reuses the cached token rather than minting again.
// A regression here would mint a token per request against a rate-limited endpoint.
//
// This lives in the package's own test binary so it can point the token exchange at a stub
// without exposing a test hook on the public API.
func TestOAuthM2M(t *testing.T) {
	var mu sync.Mutex
	var tokenMints int
	var gotGrantType, gotScope, gotBasicUser, gotBasicPass, gotAuth string

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == oauthTokenPath {
			body, _ := io.ReadAll(r.Body)
			form, _ := url.ParseQuery(string(body))
			user, pass, _ := r.BasicAuth()

			mu.Lock()
			tokenMints++
			gotGrantType = form.Get("grant_type")
			gotScope = form.Get("scope")
			gotBasicUser, gotBasicPass = user, pass
			mu.Unlock()

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"minted-token","token_type":"Bearer","expires_in":3600}`))
			return
		}

		mu.Lock()
		gotAuth = r.Header.Get("Authorization")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","created":1700000000,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	// The oauth2 client-credentials flow uses its own HTTP client, which must be told to
	// trust the test server's certificate.
	oauthHTTPClient = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	defer func() { oauthHTTPClient = nil }()

	provider, err := NewDatabricksProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			DefaultRequestTimeoutInSeconds: 10,
			InsecureSkipVerify:             true,
			AllowPrivateNetwork:            true,
		},
	}, testLogger{})
	if err != nil {
		t.Fatalf("NewDatabricksProvider: %v", err)
	}

	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}

	key := schemas.Key{
		Models: []string{"*"},
		DatabricksKeyConfig: &schemas.DatabricksKeyConfig{
			WorkspaceURL: *schemas.NewSecretVar(u.Host),
			ClientID:     schemas.NewSecretVar("sp-client-id"),
			ClientSecret: schemas.NewSecretVar("sp-client-secret"),
		},
	}
	request := &schemas.BifrostChatRequest{
		Provider: schemas.Databricks,
		Model:    "databricks-gpt-oss-120b",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}},
	}

	for i := range 2 {
		ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
		if _, bErr := provider.ChatCompletion(ctx, key, request); bErr != nil {
			cancel()
			t.Fatalf("ChatCompletion (attempt %d) returned an error: %v", i+1, bErr)
		}
		cancel()
	}

	mu.Lock()
	defer mu.Unlock()

	if gotGrantType != "client_credentials" {
		t.Errorf("grant_type: got %q, want %q", gotGrantType, "client_credentials")
	}
	if gotScope != oauthScope {
		t.Errorf("scope: got %q, want %q", gotScope, oauthScope)
	}
	if gotBasicUser != "sp-client-id" || gotBasicPass != "sp-client-secret" {
		t.Errorf("basic auth: got %q/%q, want the service principal credentials", gotBasicUser, gotBasicPass)
	}
	if gotAuth != "Bearer minted-token" {
		t.Errorf("Authorization: got %q, want %q", gotAuth, "Bearer minted-token")
	}
	if tokenMints != 1 {
		t.Errorf("token mints: got %d, want 1 (the token source must cache across requests)", tokenMints)
	}
}

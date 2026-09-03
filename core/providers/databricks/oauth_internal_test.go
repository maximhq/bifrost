package databricks

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"

	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/valyala/fasthttp"
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

// TestChatStreamOptionsFixup covers the Gemini-backed endpoints that reject the
// stream_options Bifrost sets on every stream. Every other endpoint must keep the
// shared handler's default so usage still lands on the final chunk.
func TestChatStreamOptionsFixup(t *testing.T) {
	for _, tc := range []struct {
		model   string
		dropped bool
	}{
		{"databricks-gemini-3-1-pro", true},
		{"databricks-Gemini-2-5-flash", true},
		{"system.ai.gemini-2-5-pro", true},
		{"databricks-claude-sonnet-4-5", false},
		{"databricks-gpt-5", false},
		{"system.ai.claude-sonnet-4-5", false},
	} {
		fixup := chatStreamOptionsFixup(tc.model)
		if !tc.dropped {
			if fixup != nil {
				t.Errorf("%s: expected no fixup, got one", tc.model)
			}
			continue
		}
		if fixup == nil {
			t.Fatalf("%s: expected a fixup, got nil", tc.model)
		}
		reqBody := &openai.OpenAIChatRequest{}
		reqBody.StreamOptions = &schemas.ChatStreamOptions{IncludeUsage: schemas.Ptr(true)}
		if got := fixup(reqBody); got.StreamOptions != nil {
			t.Errorf("%s: stream_options survived the fixup", tc.model)
		}
		if fixup(nil) != nil {
			t.Errorf("%s: fixup must tolerate a nil body", tc.model)
		}
	}
}

// TestParseDatabricksError covers the fallback chain: the OpenAI-shaped envelope first,
// then the workspace REST error_code/message pair, then a status-bearing generic.
func TestParseDatabricksError(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		message string
		code    string
	}{
		{
			name:    "workspace rest shape",
			status:  400,
			body:    `{"error_code":"BAD_REQUEST","message":"Invalid matryoshka_dimension 123, must be one of [32, 64, 128, 256, 512, 1024].\n"}`,
			message: "Invalid matryoshka_dimension 123, must be one of [32, 64, 128, 256, 512, 1024].",
			code:    "BAD_REQUEST",
		},
		{
			name:    "openai inference shape",
			status:  404,
			body:    `{"error":{"message":"endpoint not found","type":"invalid_request_error","code":"not_found"}}`,
			message: "endpoint not found",
			code:    "not_found",
		},
		{
			name:    "permission denied keeps its code",
			status:  403,
			body:    `{"error_code":"PERMISSION_DENIED","message":"User does not have access to the endpoint"}`,
			message: "User does not have access to the endpoint",
			code:    "PERMISSION_DENIED",
		},
		{
			name:    "unrecognised json falls back to the status",
			status:  418,
			body:    `{"something_else":true}`,
			message: "provider API error (status 418)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := fasthttp.AcquireResponse()
			defer fasthttp.ReleaseResponse(resp)
			resp.SetStatusCode(tc.status)
			resp.Header.SetContentType("application/json")
			resp.SetBodyString(tc.body)

			bErr := parseDatabricksError(resp)
			if bErr.Error.Message != tc.message {
				t.Errorf("message: got %q, want %q", bErr.Error.Message, tc.message)
			}
			if tc.code == "" {
				return
			}
			if bErr.Error.Code == nil {
				t.Fatalf("code: got nil, want %q", tc.code)
			}
			if *bErr.Error.Code != tc.code {
				t.Errorf("code: got %q, want %q", *bErr.Error.Code, tc.code)
			}
		})
	}
}

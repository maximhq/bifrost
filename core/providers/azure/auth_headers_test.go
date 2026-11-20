package azure

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// authTestLogger is a minimal no-op logger for provider construction in tests.
type authTestLogger struct{}

func (l *authTestLogger) Debug(msg string, args ...any)                     {}
func (l *authTestLogger) Info(msg string, args ...any)                      {}
func (l *authTestLogger) Warn(msg string, args ...any)                      {}
func (l *authTestLogger) Error(msg string, args ...any)                     {}
func (l *authTestLogger) Fatal(msg string, args ...any)                     {}
func (l *authTestLogger) SetLevel(level schemas.LogLevel)                   {}
func (l *authTestLogger) SetOutputType(outputType schemas.LoggerOutputType) {}
func (l *authTestLogger) LogHTTPRequest(level schemas.LogLevel, msg string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

// TestAzureAuthHeaderForwarding verifies that the non-streaming media operations
// forward the auth header produced by getAzureAuthHeaders to the upstream request,
// rather than the old behavior of always sending "Authorization: Bearer <key.Value>"
// (which silently unauthenticated api-key and service-principal callers).
//
// The context-token path emits the same "Authorization: Bearer <token>" header shape
// that the service-principal path produces, so it guards the SP wiring without needing
// Azure AD. Loopback is exempt from the private-IP dial block, so this needs no creds.
func TestAzureAuthHeaderForwarding(t *testing.T) {
	t.Parallel()

	// Minimal valid requests — each must satisfy its converter so the request reaches the wire.
	ops := []struct {
		name   string
		invoke func(p *AzureProvider, ctx *schemas.BifrostContext, key schemas.Key)
	}{
		{"Speech", func(p *AzureProvider, ctx *schemas.BifrostContext, key schemas.Key) {
			_, _ = p.Speech(ctx, key, &schemas.BifrostSpeechRequest{
				Model: "gpt-4o-mini-tts",
				Input: &schemas.SpeechInput{Input: "hello"},
			})
		}},
		{"Transcription", func(p *AzureProvider, ctx *schemas.BifrostContext, key schemas.Key) {
			_, _ = p.Transcription(ctx, key, &schemas.BifrostTranscriptionRequest{
				Model: "whisper",
				Input: &schemas.TranscriptionInput{File: []byte("fake-audio"), Filename: "a.mp3"},
			})
		}},
		{"ImageGeneration", func(p *AzureProvider, ctx *schemas.BifrostContext, key schemas.Key) {
			_, _ = p.ImageGeneration(ctx, key, &schemas.BifrostImageGenerationRequest{
				Model: "gpt-image-1",
				Input: &schemas.ImageGenerationInput{Prompt: "a green apple"},
			})
		}},
		{"ImageEdit", func(p *AzureProvider, ctx *schemas.BifrostContext, key schemas.Key) {
			_, _ = p.ImageEdit(ctx, key, &schemas.BifrostImageEditRequest{
				Model: "gpt-image-1",
				Input: &schemas.ImageEditInput{
					Images: []schemas.ImageInput{{Image: []byte("fake-image")}},
					Prompt: "make it blue",
				},
			})
		}},
		{"VideoGeneration", func(p *AzureProvider, ctx *schemas.BifrostContext, key schemas.Key) {
			_, _ = p.VideoGeneration(ctx, key, &schemas.BifrostVideoGenerationRequest{
				Model: "sora-2",
				Input: &schemas.VideoGenerationInput{Prompt: "a cat playing piano"},
			})
		}},
	}

	authCases := []struct {
		name string
		// setup mutates the key/ctx to select an auth mode in getAzureAuthHeaders.
		setup func(key *schemas.Key, ctx *schemas.BifrostContext)
		// wantHeader must be present with wantValue; absentHeader must be empty.
		wantHeader   string
		wantValue    string
		absentHeader string
	}{
		{
			// API-key auth must land in the "api-key" header, NOT "Authorization: Bearer <key>".
			name: "api-key",
			setup: func(key *schemas.Key, ctx *schemas.BifrostContext) {
				key.Value = *schemas.NewSecretVar("test-api-key")
			},
			wantHeader:   "api-key",
			wantValue:    "test-api-key",
			absentHeader: "Authorization",
		},
		{
			// Bearer-token auth (same header shape a service-principal AAD token produces)
			// must land in "Authorization: Bearer <token>", NOT be dropped.
			name: "context-token (bearer / service-principal shape)",
			setup: func(key *schemas.Key, ctx *schemas.BifrostContext) {
				ctx.SetValue(AzureAuthorizationTokenKey, "sp-bearer-token")
			},
			wantHeader:   "Authorization",
			wantValue:    "Bearer sp-bearer-token",
			absentHeader: "api-key",
		},
	}

	for _, op := range ops {
		for _, ac := range authCases {
			t.Run(op.name+"/"+ac.name, func(t *testing.T) {
				t.Parallel()

				var mu sync.Mutex
				var called bool
				var gotAuth, gotAPIKey string

				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					called = true
					gotAuth = r.Header.Get("Authorization")
					gotAPIKey = r.Header.Get("api-key")
					mu.Unlock()
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("{}"))
				}))
				defer server.Close()

				provider, err := NewAzureProvider(&schemas.ProviderConfig{
					NetworkConfig: schemas.NetworkConfig{DefaultRequestTimeoutInSeconds: 10},
				}, &authTestLogger{})
				if err != nil {
					t.Fatalf("NewAzureProvider: %v", err)
				}

				key := schemas.Key{
					Models:         []string{"*"},
					AzureKeyConfig: &schemas.AzureKeyConfig{Endpoint: *schemas.NewSecretVar(server.URL)},
				}
				ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				ac.setup(&key, ctx)

				op.invoke(provider, ctx, key)

				mu.Lock()
				defer mu.Unlock()
				if !called {
					t.Fatal("upstream never received the request (handler bailed before the HTTP call)")
				}
				headers := map[string]string{"Authorization": gotAuth, "api-key": gotAPIKey}
				if got := headers[ac.wantHeader]; got != ac.wantValue {
					t.Errorf("auth header %q: got %q, want %q", ac.wantHeader, got, ac.wantValue)
				}
				if got := headers[ac.absentHeader]; got != "" {
					t.Errorf("expected %q header to be absent, got %q", ac.absentHeader, got)
				}
			})
		}
	}
}

// TestAzureServicePrincipalTokenGoesThroughProxyConfig pins that the Entra ID token call
// for a service principal leaves through the provider's proxy_config. azidentity used to
// run on http.DefaultTransport, so behind a proxy-only egress it tried to reach
// login.microsoftonline.com directly and every service-principal request failed there.
//
// The test proxy records the CONNECT target and refuses the tunnel. That is enough: the
// only thing under test is where the token request is sent, not Entra ID itself.
func TestAzureServicePrincipalTokenGoesThroughProxyConfig(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var connectTargets []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			mu.Lock()
			connectTargets = append(connectTargets, r.Host)
			mu.Unlock()
		}
		http.Error(w, "tunnel refused by test proxy", http.StatusBadGateway)
	}))
	defer proxy.Close()

	config := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{DefaultRequestTimeoutInSeconds: 10},
		ProxyConfig:   &schemas.ProxyConfig{Type: schemas.HTTPProxy, URL: schemas.NewSecretVar(proxy.URL)},
	}
	config.CheckAndSetDefaults()
	provider, err := NewAzureProvider(config, &authTestLogger{})
	if err != nil {
		t.Fatalf("NewAzureProvider: %v", err)
	}

	key := schemas.Key{AzureKeyConfig: &schemas.AzureKeyConfig{
		ClientID:     schemas.NewSecretVar("00000000-0000-0000-0000-000000000001"),
		ClientSecret: schemas.NewSecretVar("test-secret"),
		TenantID:     schemas.NewSecretVar("00000000-0000-0000-0000-000000000002"),
	}}
	// azcore retries transport failures with backoff; the first CONNECT is all this needs.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	bctx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
	if _, bifrostErr := provider.getAzureAuthHeaders(bctx, key, false); bifrostErr == nil {
		t.Fatal("expected token acquisition to fail against the refusing test proxy")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(connectTargets) == 0 {
		t.Fatal("the Entra ID token request never reached the configured proxy")
	}
	if connectTargets[0] != "login.microsoftonline.com:443" {
		t.Errorf("first CONNECT target = %q, want login.microsoftonline.com:443", connectTargets[0])
	}
}

// closeTrackingSource is a BodySource whose readers record whether they were closed.
type closeTrackingSource struct {
	opened, closed int
}

func (s *closeTrackingSource) Size() int64 { return 4 }

func (s *closeTrackingSource) Open() (io.ReadCloser, error) {
	s.opened++
	return &closeTrackingReader{Reader: strings.NewReader("data"), source: s}, nil
}

type closeTrackingReader struct {
	io.Reader
	source *closeTrackingSource
}

func (r *closeTrackingReader) Close() error {
	r.source.closed++
	return nil
}

// TestAzureFileUploadClosesFileWhenAuthFails asserts a FileUpload whose authentication fails does
// not leave the uploaded file open. The upload body was opened before authentication and only
// attached to the request after it, so on an auth error nothing closed it, and a disk-backed upload
// held its file descriptor until the garbage collector ran.
func TestAzureFileUploadClosesFileWhenAuthFails(t *testing.T) {
	provider, err := NewAzureProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{DefaultRequestTimeoutInSeconds: 10},
	}, &authTestLogger{})
	if err != nil {
		t.Fatalf("NewAzureProvider: %v", err)
	}
	// A malformed tenant ID makes the service-principal credential fail to build, with no network call.
	key := schemas.Key{
		Models: []string{"*"},
		AzureKeyConfig: &schemas.AzureKeyConfig{
			Endpoint:     *schemas.NewSecretVar("https://example.openai.azure.com"),
			ClientID:     schemas.NewSecretVar("client"),
			ClientSecret: schemas.NewSecretVar("secret"),
			TenantID:     schemas.NewSecretVar("not a tenant/"),
		},
	}
	source := &closeTrackingSource{}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bErr := provider.FileUpload(ctx, key, &schemas.BifrostFileUploadRequest{
		Provider: schemas.Azure, FileSource: source, Filename: "batch.jsonl", Purpose: "batch",
	})
	if bErr == nil {
		t.Fatal("want an authentication error")
	}
	if source.opened != source.closed {
		t.Errorf("auth failure left %d of %d opened upload readers unclosed (%s)", source.opened-source.closed, source.opened, bErr.Error.Message)
	}
}

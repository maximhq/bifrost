package azure

import (
	"context"
	"net/http"
	"net/http/httptest"
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

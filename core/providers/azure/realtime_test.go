package azure

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestRealtimeWebSocketURL(t *testing.T) {
	t.Parallel()

	provider := &AzureProvider{}
	key := schemas.Key{AzureKeyConfig: &schemas.AzureKeyConfig{
		Endpoint: *schemas.NewSecretVar("https://example.openai.azure.com/"),
	}}

	if got, err := provider.RealtimeWebSocketURL(key, "deployment name", ""); err != nil || got != "wss://example.openai.azure.com/openai/v1/realtime?model=deployment+name" {
		t.Fatalf("RealtimeWebSocketURL() = %q, %v", got, err)
	}
	if got, err := provider.RealtimeWebSocketURL(key, "deployment name", "transcription"); err != nil || got != "wss://example.openai.azure.com/openai/v1/realtime?intent=transcription" {
		t.Fatalf("RealtimeWebSocketURL() = %q, %v", got, err)
	}
}

func TestExchangeRealtimeWebRTCSDPUsesIntentForTranscription(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		session   string
		wantQuery string
	}{
		{
			name:      "dedicated transcription",
			session:   `{"type":"transcription","audio":{"input":{"transcription":{"model":"gpt-4o-transcribe"}}}}`,
			wantQuery: "intent=transcription",
		},
		{
			name:      "normal realtime with input transcription",
			session:   `{"type":"realtime","model":"gpt-realtime","audio":{"input":{"transcription":{"model":"whisper-1"}}}}`,
			wantQuery: "model=gpt-realtime",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			handlerResult := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var handlerErr error
				if r.URL.Path != "/openai/v1/realtime" || r.URL.RawQuery != tt.wantQuery {
					handlerErr = fmt.Errorf("upstream URL = %s?%s, want /openai/v1/realtime?%s", r.URL.Path, r.URL.RawQuery, tt.wantQuery)
				}
				reader, err := r.MultipartReader()
				if err != nil {
					if handlerErr == nil {
						handlerErr = fmt.Errorf("MultipartReader() error = %w", err)
					}
				} else {
					fields := make(map[string]string)
					for {
						part, err := reader.NextPart()
						if err == io.EOF {
							break
						}
						if err != nil {
							handlerErr = fmt.Errorf("NextPart() error = %w", err)
							break
						}
						value, err := io.ReadAll(part)
						if err != nil {
							handlerErr = fmt.Errorf("ReadAll() error = %w", err)
							break
						}
						fields[part.FormName()] = string(value)
					}
					if handlerErr == nil && (fields["sdp"] != "v=0\r\n" || fields["session"] != tt.session) {
						handlerErr = fmt.Errorf("multipart fields = %#v", fields)
					}
				}
				handlerResult <- handlerErr
				w.Header().Set("Content-Type", "application/sdp")
				_, _ = w.Write([]byte("v=0\r\nanswer"))
			}))
			defer server.Close()

			provider, err := NewAzureProvider(&schemas.ProviderConfig{
				NetworkConfig: schemas.NetworkConfig{
					DefaultRequestTimeoutInSeconds: 10,
					AllowPrivateNetwork:            true,
				},
			}, &authTestLogger{})
			if err != nil {
				t.Fatalf("NewAzureProvider() error = %v", err)
			}
			key := schemas.Key{
				Value: *schemas.NewSecretVar("test-api-key"),
				AzureKeyConfig: &schemas.AzureKeyConfig{
					Endpoint: *schemas.NewSecretVar(server.URL),
				},
			}
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			answer, bifrostErr := provider.ExchangeRealtimeWebRTCSDP(ctx, key, "gpt-realtime", "v=0\r\n", []byte(tt.session))
			if bifrostErr != nil {
				t.Fatalf("ExchangeRealtimeWebRTCSDP() error = %v", bifrostErr)
			}
			if answer != "v=0\r\nanswer" {
				t.Fatalf("answer = %q, want SDP answer", answer)
			}
			if err := <-handlerResult; err != nil {
				t.Fatal(err)
			}
		})
	}
}

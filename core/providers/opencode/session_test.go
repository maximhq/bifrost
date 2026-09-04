package opencode

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
)

// TestOpencodeProviderSessionHeader verifies the x-opencode-session affinity
// header reaches the upstream on every inference path (chat and responses,
// unary and streaming): present when the transport captured a session, absent
// otherwise, namespaced per virtual key, and never leaked elsewhere.
func TestOpencodeProviderSessionHeader(t *testing.T) {
	const (
		model  = "opencode-test-model"
		apiKey = "opencode-test-key"
	)

	const responsesPayload = `{"id":"resp_session","object":"response","model":"opencode-test-model","output":[]}`

	const chatPayload = `{"id":"chatcmpl-s","object":"chat.completion","created":1,"model":"opencode-test-model",` +
		`"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{}}`

	const chatStreamBody = "data: {\"id\":\"chatcmpl-s\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"opencode-test-model\"," +
		`"choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}` + "\n\n" +
		"data: [DONE]\n\n"

	const responsesStreamBody = "data: {\"type\":\"response.completed\",\"sequence_number\":1," +
		`"response":{"id":"resp_stream","object":"response","model":"opencode-test-model","output":[]}}` + "\n\n"

	// headerCapture records the session header of every upstream request.
	type headerCapture struct {
		mu     sync.Mutex
		values [][]string
	}

	record := func(c *headerCapture) func(*http.Request) {
		return func(r *http.Request) {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.values = append(c.values, r.Header.Values("X-Opencode-Session"))
		}
	}

	// last requires exactly one upstream request and returns its header values.
	last := func(t *testing.T, c *headerCapture) []string {
		t.Helper()
		c.mu.Lock()
		defer c.mu.Unlock()
		if len(c.values) != 1 {
			t.Fatalf("upstream request count = %d, want 1", len(c.values))
		}
		return c.values[0]
	}

	serve := func(t *testing.T, contentType, body string, c *headerCapture) *httptest.Server {
		t.Helper()
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			record(c)(r)
			if _, err := io.Copy(io.Discard, r.Body); err != nil {
				t.Error("drain request body:", err)
			}
			w.Header().Set("Content-Type", contentType)
			if _, err := w.Write([]byte(body)); err != nil {
				t.Error("write payload:", err)
			}
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}))
	}

	newTestProvider := func(t *testing.T, serverURL string) *opencodeProvider {
		t.Helper()
		provider, err := NewOpencodeGoProvider(&schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				BaseURL:                        serverURL,
				DefaultRequestTimeoutInSeconds: 10,
			},
		}, nil)
		if err != nil {
			t.Fatalf("new provider: %v", err)
		}
		return provider
	}

	sessionCtx := func(t *testing.T, session, virtualKey string) (*schemas.BifrostContext, context.CancelFunc) {
		t.Helper()
		ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
		if session != "" {
			ctx.SetValue(schemas.BifrostContextKeyOpencodeSession, session)
		}
		if virtualKey != "" {
			ctx.SetValue(schemas.BifrostContextKeyVirtualKey, virtualKey)
		}
		return ctx, cancel
	}

	testKey := func() schemas.Key {
		return schemas.Key{Value: *schemas.NewSecretVar(apiKey)}
	}

	newResponsesRequest := func() *schemas.BifrostResponsesRequest {
		return &schemas.BifrostResponsesRequest{
			Provider: schemas.OpencodeGo,
			Model:    model,
			Input: []schemas.ResponsesMessage{{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")},
			}},
		}
	}

	newChatRequest := func() *schemas.BifrostChatRequest {
		return &schemas.BifrostChatRequest{
			Provider: schemas.OpencodeGo,
			Model:    model,
			Input: []schemas.ChatMessage{{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")},
			}},
		}
	}

	passthroughPostHook := func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, _ *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return result, nil
	}

	drain := func(t *testing.T, stream chan *schemas.BifrostStreamChunk) {
		t.Helper()
		for chunk := range stream {
			if chunk == nil {
				t.Fatal("nil chunk in stream")
			}
		}
	}

	// Emission across all four inference methods, verbatim and absent.
	emissionCases := []struct {
		name        string
		contentType string
		body        string
		session     string
		want        []string
		call        func(t *testing.T, provider *opencodeProvider, ctx *schemas.BifrostContext)
	}{
		{
			name:        "responses unary forwards verbatim",
			contentType: "application/json",
			body:        responsesPayload,
			session:     "conv-123",
			want:        []string{"conv-123"},
			call: func(t *testing.T, provider *opencodeProvider, ctx *schemas.BifrostContext) {
				t.Helper()
				if _, bifrostErr := provider.Responses(ctx, testKey(), newResponsesRequest()); bifrostErr != nil {
					t.Fatalf("Responses: %v", bifrostErr)
				}
			},
		},
		{
			name:        "chat forwards verbatim",
			contentType: "application/json",
			body:        chatPayload,
			session:     "conv-chat",
			want:        []string{"conv-chat"},
			call: func(t *testing.T, provider *opencodeProvider, ctx *schemas.BifrostContext) {
				t.Helper()
				if _, bifrostErr := provider.ChatCompletion(ctx, testKey(), newChatRequest()); bifrostErr != nil {
					t.Fatalf("ChatCompletion: %v", bifrostErr)
				}
			},
		},
		{
			name:        "chat stream forwards verbatim",
			contentType: "text/event-stream",
			body:        chatStreamBody,
			session:     "conv-chat-stream",
			want:        []string{"conv-chat-stream"},
			call: func(t *testing.T, provider *opencodeProvider, ctx *schemas.BifrostContext) {
				t.Helper()
				stream, bifrostErr := provider.ChatCompletionStream(ctx, passthroughPostHook, nil, testKey(), newChatRequest())
				if bifrostErr != nil {
					t.Fatalf("ChatCompletionStream: %v", bifrostErr)
				}
				drain(t, stream)
			},
		},
		{
			name:        "responses stream forwards verbatim",
			contentType: "text/event-stream",
			body:        responsesStreamBody,
			session:     "conv-resp-stream",
			want:        []string{"conv-resp-stream"},
			call: func(t *testing.T, provider *opencodeProvider, ctx *schemas.BifrostContext) {
				t.Helper()
				stream, bifrostErr := provider.ResponsesStream(ctx, passthroughPostHook, nil, testKey(), newResponsesRequest())
				if bifrostErr != nil {
					t.Fatalf("ResponsesStream: %v", bifrostErr)
				}
				drain(t, stream)
			},
		},
		{
			name:        "no session in context sends no header",
			contentType: "application/json",
			body:        responsesPayload,
			session:     "",
			want:        nil,
			call: func(t *testing.T, provider *opencodeProvider, ctx *schemas.BifrostContext) {
				t.Helper()
				if _, bifrostErr := provider.Responses(ctx, testKey(), newResponsesRequest()); bifrostErr != nil {
					t.Fatalf("Responses: %v", bifrostErr)
				}
			},
		},
	}

	for _, tc := range emissionCases {
		t.Run(tc.name, func(t *testing.T) {
			capture := &headerCapture{}
			server := serve(t, tc.contentType, tc.body, capture)
			defer server.Close()

			ctx, cancel := sessionCtx(t, tc.session, "")
			defer cancel()
			tc.call(t, newTestProvider(t, server.URL), ctx)

			if got := last(t, capture); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("X-Opencode-Session = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("virtual key namespaces the upstream value deterministically", func(t *testing.T) {
		capture := &headerCapture{}
		server := serve(t, "application/json", responsesPayload, capture)
		defer server.Close()
		provider := newTestProvider(t, server.URL)

		doRequest := func(virtualKey string) string {
			t.Helper()
			ctx, cancel := sessionCtx(t, "conv-123", virtualKey)
			defer cancel()
			if _, bifrostErr := provider.Responses(ctx, testKey(), newResponsesRequest()); bifrostErr != nil {
				t.Fatalf("Responses: %v", bifrostErr)
			}
			capture.mu.Lock()
			defer capture.mu.Unlock()
			latest := capture.values[len(capture.values)-1]
			if len(latest) != 1 {
				t.Fatalf("X-Opencode-Session = %q, want single value", latest)
			}
			return latest[0]
		}

		if plain := doRequest(""); plain != "conv-123" {
			t.Fatalf("without virtual key = %q, want raw conv-123", plain)
		}
		namespaced := doRequest("vk-test-1")
		if namespaced == "conv-123" {
			t.Fatalf("namespaced value %q must differ from raw session", namespaced)
		}
		if !strings.HasSuffix(namespaced, ":conv-123") {
			t.Fatalf("namespaced value %q must carry the raw session suffix", namespaced)
		}
		if again := doRequest("vk-test-1"); again != namespaced {
			t.Fatalf("namespacing not deterministic: %q vs %q", again, namespaced)
		}
		if other := doRequest("vk-test-2"); other == namespaced {
			t.Fatalf("different tenants must map to different values, both got %q", other)
		}
	})

	t.Run("ListModels emits no session header", func(t *testing.T) {
		capture := &headerCapture{}
		server := serve(t, "application/json", `{"object":"list","data":[{"id":"m"}]}`, capture)
		defer server.Close()
		provider := newTestProvider(t, server.URL)

		ctx, cancel := sessionCtx(t, "conv-123", "")
		defer cancel()
		if _, bifrostErr := provider.ListModels(ctx, []schemas.Key{testKey()}, &schemas.BifrostListModelsRequest{}); bifrostErr != nil {
			t.Fatalf("ListModels: %v", bifrostErr)
		}

		if got := last(t, capture); len(got) != 0 {
			t.Fatalf("X-Opencode-Session = %q on ListModels, want absent", got)
		}
	})

	t.Run("session key does not leak to the shared OpenAI provider", func(t *testing.T) {
		capture := &headerCapture{}
		server := serve(t, "application/json", responsesPayload, capture)
		defer server.Close()

		provider := openai.NewOpenAIProvider(&schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				BaseURL:                        server.URL,
				DefaultRequestTimeoutInSeconds: 10,
			},
		}, nil)

		ctx, cancel := sessionCtx(t, "conv-123", "")
		defer cancel()
		req := newResponsesRequest()
		req.Provider = schemas.OpenAI
		if _, bifrostErr := provider.Responses(ctx, testKey(), req); bifrostErr != nil {
			t.Fatalf("Responses: %v", bifrostErr)
		}

		if got := last(t, capture); len(got) != 0 {
			t.Fatalf("X-Opencode-Session = %q leaked to OpenAI provider, want absent", got)
		}
	})
}

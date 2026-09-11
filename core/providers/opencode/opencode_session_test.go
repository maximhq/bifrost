package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
)

const (
	wireTestModel  = "opencode-test-model"
	wireTestAPIKey = "opencode-test-key"
)

// passthroughPostHookRunner is a no-op PostHookRunner used by streaming wire
// tests; the real pipeline is not exercised here.
func passthroughPostHookRunner(_ *schemas.BifrostContext, result *schemas.BifrostResponse, _ *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return result, nil
}

// opencodeOp identifies one provider operation to drive against the mock
// upstream.
type opencodeOp string

const (
	opChat            opencodeOp = "chat"
	opResponses       opencodeOp = "responses"
	opChatStream      opencodeOp = "chat-stream"
	opResponsesStream opencodeOp = "responses-stream"
	opListModels      opencodeOp = "list-models"
)

var (
	wireChatJSON = `{"id":"chatcmpl-test","object":"chat.completion","model":"` + wireTestModel + `","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
	wireChatSSE  = "data: " + `{"id":"chatcmpl-stream","object":"chat.completion.chunk","model":"` + wireTestModel + `","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}` + "\n\n" +
		"data: " + `{"id":"chatcmpl-stream","object":"chat.completion.chunk","model":"` + wireTestModel + `","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
		"data: " + `{"id":"chatcmpl-stream","object":"chat.completion.chunk","model":"` + wireTestModel + `","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}` + "\n\n"
	wireResponsesJSON = `{"id":"resp_regular","object":"response","model":"` + wireTestModel + `","output":[]}`
	wireResponsesSSE  = "data: " + `{"type":"response.completed","sequence_number":1,"response":{"id":"resp_stream","object":"response","model":"` + wireTestModel + `","output":[]}}` + "\n\n"
)

// wireCapture is a single request observed by the mock upstream.
type wireCapture struct {
	method          string
	path            string
	opencodeSession string
}

// newOpencodeWireCtx builds a BifrostContext carrying the request headers the
// HTTP transport would have captured under BifrostContextKeyRequestHeaders.
func newOpencodeWireCtx(t *testing.T, requestHeaders map[string]string) *schemas.BifrostContext {
	t.Helper()
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	if len(requestHeaders) > 0 {
		ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, requestHeaders)
	}
	return ctx
}

func newWireChatRequest(providerKey schemas.ModelProvider) *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: providerKey,
		Model:    wireTestModel,
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")},
		}},
	}
}

func newWireResponsesRequest(providerKey schemas.ModelProvider) *schemas.BifrostResponsesRequest {
	return &schemas.BifrostResponsesRequest{
		Provider: providerKey,
		Model:    wireTestModel,
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hello")},
		}},
	}
}

// drainStream consumes a stream channel to completion.
func drainStream(t *testing.T, ch chan *schemas.BifrostStreamChunk) {
	t.Helper()
	for range ch {
	}
}

// runOpencodeOp drives a single provider operation against the mock upstream
// and fails the test if it errors or produces no response.
func runOpencodeOp(t *testing.T, provider *opencodeProvider, ctx *schemas.BifrostContext, key schemas.Key, op opencodeOp) {
	t.Helper()
	switch op {
	case opChat:
		response, bifrostErr := provider.ChatCompletion(ctx, key, newWireChatRequest(provider.providerKey))
		if bifrostErr != nil {
			t.Fatalf("ChatCompletion: %v", bifrostErr)
		}
		if response == nil || response.ID != "chatcmpl-test" {
			t.Fatalf("ChatCompletion returned %#v, want response id chatcmpl-test", response)
		}
	case opResponses:
		response, bifrostErr := provider.Responses(ctx, key, newWireResponsesRequest(provider.providerKey))
		if bifrostErr != nil {
			t.Fatalf("Responses: %v", bifrostErr)
		}
		if response == nil || response.ID == nil || *response.ID != "resp_regular" {
			t.Fatalf("Responses returned %#v, want response id resp_regular", response)
		}
	case opChatStream:
		ch, bifrostErr := provider.ChatCompletionStream(ctx, passthroughPostHookRunner, nil, key, newWireChatRequest(provider.providerKey))
		if bifrostErr != nil {
			t.Fatalf("ChatCompletionStream: %v", bifrostErr)
		}
		drainStream(t, ch)
	case opResponsesStream:
		ch, bifrostErr := provider.ResponsesStream(ctx, passthroughPostHookRunner, nil, key, newWireResponsesRequest(provider.providerKey))
		if bifrostErr != nil {
			t.Fatalf("ResponsesStream: %v", bifrostErr)
		}
		drainStream(t, ch)
	case opListModels:
		response, bifrostErr := provider.ListModels(ctx, []schemas.Key{key}, &schemas.BifrostListModelsRequest{})
		if bifrostErr != nil {
			t.Fatalf("ListModels: %v", bifrostErr)
		}
		if response == nil {
			t.Fatal("ListModels returned nil response")
		}
	default:
		t.Fatalf("unknown op %q", op)
	}
}

// newOpencodeWireProvider starts a mock upstream and returns a provider
// pointed at it plus a capture function the test can read after driving ops.
func newOpencodeWireProvider(t *testing.T, newProvider func(*schemas.ProviderConfig) (*opencodeProvider, error)) (*opencodeProvider, func() []wireCapture) {
	t.Helper()

	var (
		mu       sync.Mutex
		captures []wireCapture
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// GET requests (ListModels) carry no body.
		var payload map[string]any
		if len(body) > 0 {
			if err := json.Unmarshal(body, &payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}

		mu.Lock()
		captures = append(captures, wireCapture{
			method:          r.Method,
			path:            r.URL.Path,
			opencodeSession: r.Header.Get(OpencodeSessionHeader),
		})
		mu.Unlock()

		switch {
		case r.URL.Path == "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"object":"list","data":[]}`)
		case r.URL.Path == "/v1/chat/completions":
			if streaming, _ := payload["stream"].(bool); streaming {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, wireChatSSE)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, wireChatJSON)
		case r.URL.Path == "/v1/responses":
			if streaming, _ := payload["stream"].(bool); streaming {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, wireResponsesSSE)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, wireResponsesJSON)
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	provider, err := newProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        server.URL,
			DefaultRequestTimeoutInSeconds: 10,
		},
	})
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	return provider, func() []wireCapture {
		mu.Lock()
		defer mu.Unlock()
		return append([]wireCapture(nil), captures...)
	}
}

var wireUUIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// TestOpencodeSessionHeaderOnWire pins the upstream-visible x-opencode-session
// header for the OpenCode provider family across every inference path.
func TestOpencodeSessionHeaderOnWire(t *testing.T) {
	for _, tc := range []struct {
		name        string
		newProvider func(*schemas.ProviderConfig) (*opencodeProvider, error)
	}{
		{name: "Zen", newProvider: func(config *schemas.ProviderConfig) (*opencodeProvider, error) {
			return NewOpencodeZenProvider(config, nil)
		}},
		{name: "Go", newProvider: func(config *schemas.ProviderConfig) (*opencodeProvider, error) {
			return NewOpencodeGoProvider(config, nil)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// wantHeader is expected verbatim; when wantUUID is set the captured
			// value must be exactly "<wantHeader>:<UUID>".
			tests := []struct {
				name             string
				ops              []opencodeOp
				setupCtx         func(t *testing.T) *schemas.BifrostContext
				wantHeader       string
				wantHeaders      []string // per-op expectation, overrides wantHeader when set
				wantUUID         bool
				switchVirtualKey bool // re-set the virtual key to vk-2 for the second op
			}{
				{
					name: "client-sent header forwarded verbatim on every inference op",
					ops:  []opencodeOp{opChat, opResponses, opChatStream, opResponsesStream},
					setupCtx: func(t *testing.T) *schemas.BifrostContext {
						ctx := newOpencodeWireCtx(t, map[string]string{OpencodeSessionHeader: "client-sess-abc"})
						ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "vk-1")
						return ctx
					},
					wantHeader: "vk-1:client-sess-abc",
				},
				{
					name: "x-bf-session-id used when client header absent",
					ops:  []opencodeOp{opChat, opResponses, opChatStream, opResponsesStream},
					setupCtx: func(t *testing.T) *schemas.BifrostContext {
						ctx := newOpencodeWireCtx(t, nil)
						ctx.SetValue(schemas.BifrostContextKeySessionID, "bf-sess-123")
						ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "vk-1")
						return ctx
					},
					wantHeader: "vk-1:bf-sess-123",
				},
				{
					name: "harness session id used when neither client header nor x-bf-session-id present",
					ops:  []opencodeOp{opChat, opResponses},
					setupCtx: func(t *testing.T) *schemas.BifrostContext {
						ctx := newOpencodeWireCtx(t, nil)
						ctx.SetValue(schemas.BifrostContextKeySessionID, "harness-sess-42")
						ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "vk-1")
						return ctx
					},
					wantHeader: "vk-1:harness-sess-42",
				},
				{
					name: "synthesized UUID when no signal at all",
					ops:  []opencodeOp{opChat, opResponses, opChatStream, opResponsesStream},
					setupCtx: func(t *testing.T) *schemas.BifrostContext {
						ctx := newOpencodeWireCtx(t, nil)
						ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "vk-1")
						return ctx
					},
					wantHeader: "vk-1",
					wantUUID:   true,
				},
				{
					name: "poisoned client header never reaches the wire; fallback applies",
					ops:  []opencodeOp{opChat, opResponses},
					setupCtx: func(t *testing.T) *schemas.BifrostContext {
						ctx := newOpencodeWireCtx(t, map[string]string{OpencodeSessionHeader: "evil\r\nInjected: x"})
						ctx.SetValue(schemas.BifrostContextKeySessionID, "bf-sess-123")
						ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "vk-1")
						return ctx
					},
					wantHeader: "vk-1:bf-sess-123",
				},
				{
					name: "poisoned client header without fallback synthesizes a UUID",
					ops:  []opencodeOp{opChat},
					setupCtx: func(t *testing.T) *schemas.BifrostContext {
						ctx := newOpencodeWireCtx(t, map[string]string{OpencodeSessionHeader: "abc\x01def"})
						ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "vk-1")
						return ctx
					},
					wantHeader: "vk-1",
					wantUUID:   true,
				},
				{
					name: "sessions from different virtual keys do not collide",
					ops:  []opencodeOp{opChat, opChat},
					setupCtx: func(t *testing.T) *schemas.BifrostContext {
						ctx := newOpencodeWireCtx(t, map[string]string{OpencodeSessionHeader: "shared-session"})
						ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "vk-1")
						return ctx
					},
					wantHeaders:      []string{"vk-1:shared-session", "vk-2:shared-session"},
					switchVirtualKey: true,
				},
			}

			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					provider, captures := newOpencodeWireProvider(t, tc.newProvider)
					key := schemas.Key{Value: *schemas.NewSecretVar(wireTestAPIKey)}

					for i, op := range tt.ops {
						// A fresh context per op: the shared streaming handlers keep
						// per-stream state on the context, so reusing one context
						// across stream ops misbehaves (the existing
						// TestOpencodeResponsesRouting uses separate contexts too).
						ctx := tt.setupCtx(t)
						// Alternate the virtual key for the collision case: the
						// first call uses vk-1, the second vk-2.
						if i == 1 && tt.switchVirtualKey {
							ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "vk-2")
						}
						runOpencodeOp(t, provider, ctx, key, op)
					}

					got := captures()
					if len(got) != len(tt.ops) {
						t.Fatalf("upstream request count = %d, want %d (captures: %+v)", len(got), len(tt.ops), got)
					}
					for i, capture := range got {
						if capture.path == "/v1/models" {
							t.Errorf("unexpected /v1/models request among inference ops: %+v", capture)
						}
						want := tt.wantHeader
						if len(tt.wantHeaders) > 0 {
							want = tt.wantHeaders[i]
						}
						if tt.wantUUID {
							wantPrefix := want + ":"
							if !strings.HasPrefix(capture.opencodeSession, wantPrefix) {
								t.Errorf("request %d x-opencode-session = %q, want %q prefix", i, capture.opencodeSession, wantPrefix)
							}
							if !wireUUIDRe.MatchString(strings.TrimPrefix(capture.opencodeSession, wantPrefix)) {
								t.Errorf("request %d x-opencode-session = %q, want a UUID after the namespace prefix", i, capture.opencodeSession)
							}
							continue
						}
						if capture.opencodeSession != want {
							t.Errorf("request %d x-opencode-session = %q, want %q", i, capture.opencodeSession, want)
						}
					}
				})
			}
		})
	}
}

// TestOpencodeSessionHeaderNotOnListModels pins that ListModels requests never
// carry the header, even when the client sent one.
func TestOpencodeSessionHeaderNotOnListModels(t *testing.T) {
	provider, captures := newOpencodeWireProvider(t, func(config *schemas.ProviderConfig) (*opencodeProvider, error) {
		return NewOpencodeGoProvider(config, nil)
	})
	ctx := newOpencodeWireCtx(t, map[string]string{OpencodeSessionHeader: "client-sess-abc"})
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "vk-1")

	runOpencodeOp(t, provider, ctx, schemas.Key{Value: *schemas.NewSecretVar(wireTestAPIKey)}, opListModels)

	got := captures()
	if len(got) != 1 {
		t.Fatalf("upstream request count = %d, want 1", len(got))
	}
	if got[0].path != "/v1/models" {
		t.Fatalf("request path = %q, want /v1/models", got[0].path)
	}
	if got[0].opencodeSession != "" {
		t.Errorf("ListModels request carried x-opencode-session = %q, want none", got[0].opencodeSession)
	}
}

// TestOpencodeSessionHeaderNeverLeaksToOtherProviders pins that the shared
// OpenAI-compatible handlers (used by 9+ providers) do not emit the header:
// only the OpenCode provider attaches it.
func TestOpencodeSessionHeaderNeverLeaksToOtherProviders(t *testing.T) {
	var (
		mu           sync.Mutex
		seenSessions []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		seenSessions = append(seenSessions, r.Header.Get(OpencodeSessionHeader))
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, wireChatJSON)
	}))
	defer server.Close()

	openAIProvider := openai.NewOpenAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        server.URL,
			DefaultRequestTimeoutInSeconds: 10,
		},
	}, nil)

	// The client sent x-opencode-session AND a session id; neither may leak.
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, map[string]string{OpencodeSessionHeader: "client-sess-abc"})
	ctx.SetValue(schemas.BifrostContextKeySessionID, "bf-sess-123")
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "vk-1")

	key := schemas.Key{Value: *schemas.NewSecretVar(wireTestAPIKey)}
	request := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    wireTestModel,
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")},
		}},
	}
	response, bifrostErr := openAIProvider.ChatCompletion(ctx, key, request)
	if bifrostErr != nil {
		t.Fatalf("ChatCompletion via OpenAI provider: %v", bifrostErr)
	}
	if response == nil {
		t.Fatal("ChatCompletion via OpenAI provider returned nil response")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seenSessions) != 1 {
		t.Fatalf("upstream request count = %d, want 1", len(seenSessions))
	}
	if seenSessions[0] != "" {
		t.Errorf("non-OpenCode provider received x-opencode-session = %q, want none", seenSessions[0])
	}
}

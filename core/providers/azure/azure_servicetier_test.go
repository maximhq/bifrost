package azure_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/providers/azure"
	"github.com/maximhq/bifrost/core/schemas"
)

// serviceTierUpstream is a fake Azure resource that records the headers each
// request arrives with and answers the OpenAI-compatible chat and responses
// shapes (plain and SSE).
type serviceTierUpstream struct {
	*httptest.Server
	mu      sync.Mutex
	calls   int
	headers []map[string]string
}

func newServiceTierUpstream(t *testing.T) *serviceTierUpstream {
	t.Helper()
	u := &serviceTierUpstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.calls++
		h := map[string]string{}
		for k, v := range r.Header {
			if len(v) > 0 {
				h[strings.ToLower(k)] = v[0]
			}
		}
		u.headers = append(u.headers, h)
		u.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		switch {
		case strings.Contains(r.URL.Path, "chat/completions"):
			body := readBodyForTierTest(r)
			if strings.Contains(body, `"stream":true`) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(
					`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"my-deployment","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}` + "\n\n" +
						`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"my-deployment","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}` + "\n\n" +
						"data: [DONE]\n\n"))
				return
			}
			_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"my-deployment","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
		case strings.Contains(r.URL.Path, "responses"):
			body := readBodyForTierTest(r)
			if strings.Contains(body, `"stream":true`) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(
					`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"my-deployment","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` + "\n\n" +
						"data: [DONE]\n\n"))
				return
			}
			_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"my-deployment","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"hi","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(u.Server.Close)
	return u
}

func readBodyForTierTest(r *http.Request) string {
	if r.Body == nil {
		return ""
	}
	buf := make([]byte, 1<<16)
	n, _ := r.Body.Read(buf)
	return string(buf[:n])
}

func (u *serviceTierUpstream) callCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls
}

func (u *serviceTierUpstream) firstHeaders() map[string]string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.headers) == 0 {
		return nil
	}
	return u.headers[0]
}

func newTierTestProvider(t *testing.T) *azure.AzureProvider {
	t.Helper()
	provider, err := azure.NewAzureProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{DefaultRequestTimeoutInSeconds: 10},
	}, routingTestLogger{})
	if err != nil {
		t.Fatalf("NewAzureProvider: %v", err)
	}
	return provider
}

func newTierTestKey(endpoint string) schemas.Key {
	return schemas.Key{
		Value:          *schemas.NewSecretVar("test-key"),
		Models:         []string{"*"},
		AzureKeyConfig: &schemas.AzureKeyConfig{Endpoint: *schemas.NewSecretVar(endpoint)},
	}
}

func tierTestChatRequest(tier *schemas.BifrostServiceTier) *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: schemas.Azure,
		Model:    "my-deployment",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}},
		Params:   &schemas.ChatParameters{ServiceTier: tier},
	}
}

func tierTestResponsesRequest(tier *schemas.BifrostServiceTier) *schemas.BifrostResponsesRequest {
	return &schemas.BifrostResponsesRequest{
		Provider: schemas.Azure,
		Model:    "my-deployment",
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")},
		}},
		Params: &schemas.ResponsesParameters{ServiceTier: tier},
	}
}

// tierTestCtx builds a context carrying the given client request headers, the
// same lowercased map the transport installs from the HTTP request.
func tierTestCtx(t *testing.T, headers map[string]string) *schemas.BifrostContext {
	t.Helper()
	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	if headers != nil {
		ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, headers)
	}
	return ctx
}

func passthroughPostHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return resp, err
}

// TestAzureServiceTierHeader covers the x-ms-service-tier header: Azure's
// documented gateway pattern for selecting Flex or Priority processing without
// touching the request body. When present and valid the header is forwarded to
// Azure (which applies it in preference to the body's service_tier); an invalid
// value is rejected client-side, matching Azure's own 400 with no body
// fallback. See https://learn.microsoft.com/azure/foundry/openai/how-to/flex-processing
func TestAzureServiceTierHeader(t *testing.T) {
	flex := schemas.BifrostServiceTierFlex
	priority := schemas.BifrostServiceTierPriority
	defaultTier := schemas.BifrostServiceTierDefault

	t.Run("ChatForwardsFlexHeader", func(t *testing.T) {
		upstream := newServiceTierUpstream(t)
		ctx := tierTestCtx(t, map[string]string{"x-ms-service-tier": "flex"})

		if _, bifrostErr := newTierTestProvider(t).ChatCompletion(ctx, newTierTestKey(upstream.URL), tierTestChatRequest(nil)); bifrostErr != nil {
			t.Fatalf("ChatCompletion: %+v", bifrostErr)
		}
		if got := upstream.firstHeaders()["x-ms-service-tier"]; got != "flex" {
			t.Fatalf("upstream x-ms-service-tier = %q, want flex (headers: %v)", got, upstream.firstHeaders())
		}
	})

	t.Run("ChatForwardsPriorityHeader", func(t *testing.T) {
		upstream := newServiceTierUpstream(t)
		ctx := tierTestCtx(t, map[string]string{"x-ms-service-tier": "priority"})

		if _, bifrostErr := newTierTestProvider(t).ChatCompletion(ctx, newTierTestKey(upstream.URL), tierTestChatRequest(nil)); bifrostErr != nil {
			t.Fatalf("ChatCompletion: %+v", bifrostErr)
		}
		if got := upstream.firstHeaders()["x-ms-service-tier"]; got != "priority" {
			t.Fatalf("upstream x-ms-service-tier = %q, want priority", got)
		}
	})

	t.Run("ChatStreamForwardsFlexHeader", func(t *testing.T) {
		upstream := newServiceTierUpstream(t)
		ctx := tierTestCtx(t, map[string]string{"x-ms-service-tier": "flex"})

		stream, bifrostErr := newTierTestProvider(t).ChatCompletionStream(ctx, passthroughPostHook, nil, newTierTestKey(upstream.URL), tierTestChatRequest(nil))
		if bifrostErr != nil {
			t.Fatalf("ChatCompletionStream: %+v", bifrostErr)
		}
		drainTierStream(t, stream)
		if got := upstream.firstHeaders()["x-ms-service-tier"]; got != "flex" {
			t.Fatalf("upstream x-ms-service-tier = %q, want flex", got)
		}
	})

	t.Run("ResponsesForwardsFlexHeader", func(t *testing.T) {
		upstream := newServiceTierUpstream(t)
		ctx := tierTestCtx(t, map[string]string{"x-ms-service-tier": "flex"})

		if _, bifrostErr := newTierTestProvider(t).Responses(ctx, newTierTestKey(upstream.URL), tierTestResponsesRequest(nil)); bifrostErr != nil {
			t.Fatalf("Responses: %+v", bifrostErr)
		}
		if got := upstream.firstHeaders()["x-ms-service-tier"]; got != "flex" {
			t.Fatalf("upstream x-ms-service-tier = %q, want flex", got)
		}
	})

	t.Run("ResponsesStreamForwardsFlexHeader", func(t *testing.T) {
		upstream := newServiceTierUpstream(t)
		ctx := tierTestCtx(t, map[string]string{"x-ms-service-tier": "flex"})

		stream, bifrostErr := newTierTestProvider(t).ResponsesStream(ctx, passthroughPostHook, nil, newTierTestKey(upstream.URL), tierTestResponsesRequest(nil))
		if bifrostErr != nil {
			t.Fatalf("ResponsesStream: %+v", bifrostErr)
		}
		drainTierStream(t, stream)
		if got := upstream.firstHeaders()["x-ms-service-tier"]; got != "flex" {
			t.Fatalf("upstream x-ms-service-tier = %q, want flex", got)
		}
	})

	t.Run("HeaderTakesPrecedenceOverBodyTier", func(t *testing.T) {
		upstream := newServiceTierUpstream(t)
		ctx := tierTestCtx(t, map[string]string{"x-ms-service-tier": "flex"})

		// Body asks for Standard; the header asks for Flex. Azure applies the
		// header, so Bifrost forwards it unchanged and leaves the body tier for
		// Azure to ignore.
		if _, bifrostErr := newTierTestProvider(t).ChatCompletion(ctx, newTierTestKey(upstream.URL), tierTestChatRequest(&defaultTier)); bifrostErr != nil {
			t.Fatalf("ChatCompletion: %+v", bifrostErr)
		}
		if got := upstream.firstHeaders()["x-ms-service-tier"]; got != "flex" {
			t.Fatalf("upstream x-ms-service-tier = %q, want flex", got)
		}
	})

	t.Run("BodyTierStillWorksWithoutHeader", func(t *testing.T) {
		upstream := newServiceTierUpstream(t)
		ctx := tierTestCtx(t, nil)

		if _, bifrostErr := newTierTestProvider(t).ChatCompletion(ctx, newTierTestKey(upstream.URL), tierTestChatRequest(&flex)); bifrostErr != nil {
			t.Fatalf("ChatCompletion: %+v", bifrostErr)
		}
		if got := upstream.firstHeaders()["x-ms-service-tier"]; got != "" {
			t.Fatalf("upstream x-ms-service-tier = %q, want absent", got)
		}
	})

	t.Run("InvalidHeaderValueIsRejected", func(t *testing.T) {
		upstream := newServiceTierUpstream(t)
		ctx := tierTestCtx(t, map[string]string{"x-ms-service-tier": "turbo"})

		_, bifrostErr := newTierTestProvider(t).ChatCompletion(ctx, newTierTestKey(upstream.URL), tierTestChatRequest(&priority))
		if bifrostErr == nil {
			t.Fatal("ChatCompletion: want error for invalid x-ms-service-tier value, got nil")
		}
		if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != 400 {
			t.Fatalf("error status = %v, want 400 (message: %s)", bifrostErr.StatusCode, bifrostErr.Error.Message)
		}
		if upstream.callCount() != 0 {
			t.Fatalf("upstream calls = %d, want 0 (invalid tier must not reach Azure)", upstream.callCount())
		}
	})

	t.Run("InvalidHeaderValueRejectedOnStream", func(t *testing.T) {
		upstream := newServiceTierUpstream(t)
		ctx := tierTestCtx(t, map[string]string{"x-ms-service-tier": "ultrafast"})

		stream, bifrostErr := newTierTestProvider(t).ChatCompletionStream(ctx, passthroughPostHook, nil, newTierTestKey(upstream.URL), tierTestChatRequest(nil))
		if bifrostErr == nil {
			t.Fatal("ChatCompletionStream: want error for invalid x-ms-service-tier value, got nil")
		}
		if stream != nil {
			drainTierStream(t, stream)
		}
		if upstream.callCount() != 0 {
			t.Fatalf("upstream calls = %d, want 0", upstream.callCount())
		}
	})
}

func drainTierStream(t *testing.T, stream chan *schemas.BifrostStreamChunk) {
	t.Helper()
	timeout := time.NewTimer(20 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case _, ok := <-stream:
			if !ok {
				return
			}
		case <-timeout.C:
			t.Fatal("timed out waiting for the provider stream to close")
		}
	}
}

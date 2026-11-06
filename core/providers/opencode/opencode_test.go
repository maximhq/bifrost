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

// Compile-time check that opencodeProvider satisfies the full Provider interface.
var _ schemas.Provider = (*opencodeProvider)(nil)

func TestOpencodeProviderConstructors(t *testing.T) {
	t.Parallel()

	t.Run("Zen constructor defaults", func(t *testing.T) {
		zenConfig := &schemas.ProviderConfig{}
		zenConfig.CheckAndSetDefaults()
		provider, err := NewOpencodeZenProvider(zenConfig, nil)
		if err != nil {
			t.Fatalf("NewOpencodeZenProvider failed: %v", err)
		}
		if provider.GetProviderKey() != schemas.OpencodeZen {
			t.Errorf("expected provider key %s, got %s", schemas.OpencodeZen, provider.GetProviderKey())
		}
		if provider.networkConfig.BaseURL != "https://opencode.ai/zen" {
			t.Errorf("expected base URL https://opencode.ai/zen, got %s", provider.networkConfig.BaseURL)
		}
	})

	t.Run("Go constructor defaults", func(t *testing.T) {
		goConfig := &schemas.ProviderConfig{}
		goConfig.CheckAndSetDefaults()
		provider, err := NewOpencodeGoProvider(goConfig, nil)
		if err != nil {
			t.Fatalf("NewOpencodeGoProvider failed: %v", err)
		}
		if provider.GetProviderKey() != schemas.OpencodeGo {
			t.Errorf("expected provider key %s, got %s", schemas.OpencodeGo, provider.GetProviderKey())
		}
		if provider.networkConfig.BaseURL != "https://opencode.ai/zen/go" {
			t.Errorf("expected base URL https://opencode.ai/zen/go, got %s", provider.networkConfig.BaseURL)
		}
	})
}

// unsupportedOp represents an operation that opencodeProvider should reject.
type unsupportedOp struct {
	name        string
	requestType schemas.RequestType
	invoke      func(p *opencodeProvider) *schemas.BifrostError
}

// TestOpencodeUnsupportedOperations verifies that all unsupported operations return
// errors with the correct request type and provider key in the error details.
// Tests against both Zen and Go provider keys to ensure GetProviderKey() is used consistently.
func TestOpencodeUnsupportedOperations(t *testing.T) {
	providers := []struct {
		name string
		key  schemas.ModelProvider
	}{
		{name: "Zen", key: schemas.OpencodeZen},
		{name: "Go", key: schemas.OpencodeGo},
	}

	cases := []unsupportedOp{
		{name: "TextCompletion", requestType: schemas.TextCompletionRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.TextCompletion(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "TextCompletionStream", requestType: schemas.TextCompletionStreamRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.TextCompletionStream(nil, nil, nil, schemas.Key{}, nil)
			return err
		}},
		{name: "Embedding", requestType: schemas.EmbeddingRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.Embedding(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "Rerank", requestType: schemas.RerankRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.Rerank(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "OCR", requestType: schemas.OCRRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.OCR(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "Speech", requestType: schemas.SpeechRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.Speech(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "SpeechStream", requestType: schemas.SpeechStreamRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.SpeechStream(nil, nil, nil, schemas.Key{}, nil)
			return err
		}},
		{name: "Transcription", requestType: schemas.TranscriptionRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.Transcription(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "TranscriptionStream", requestType: schemas.TranscriptionStreamRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.TranscriptionStream(nil, nil, nil, schemas.Key{}, nil)
			return err
		}},
		{name: "ImageGeneration", requestType: schemas.ImageGenerationRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.ImageGeneration(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "ImageGenerationStream", requestType: schemas.ImageGenerationStreamRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.ImageGenerationStream(nil, nil, nil, schemas.Key{}, nil)
			return err
		}},
		{name: "ImageEdit", requestType: schemas.ImageEditRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.ImageEdit(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "ImageEditStream", requestType: schemas.ImageEditStreamRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.ImageEditStream(nil, nil, nil, schemas.Key{}, nil)
			return err
		}},
		{name: "ImageVariation", requestType: schemas.ImageVariationRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.ImageVariation(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "VideoGeneration", requestType: schemas.VideoGenerationRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.VideoGeneration(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "VideoRetrieve", requestType: schemas.VideoRetrieveRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.VideoRetrieve(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "VideoDownload", requestType: schemas.VideoDownloadRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.VideoDownload(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "VideoDelete", requestType: schemas.VideoDeleteRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.VideoDelete(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "VideoList", requestType: schemas.VideoListRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.VideoList(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "VideoRemix", requestType: schemas.VideoRemixRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.VideoRemix(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "CountTokens", requestType: schemas.CountTokensRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.CountTokens(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "Compaction", requestType: schemas.CompactionRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.Compaction(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "BatchCreate", requestType: schemas.BatchCreateRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.BatchCreate(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "BatchList", requestType: schemas.BatchListRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.BatchList(nil, nil, nil)
			return err
		}},
		{name: "BatchRetrieve", requestType: schemas.BatchRetrieveRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.BatchRetrieve(nil, nil, nil)
			return err
		}},
		{name: "BatchCancel", requestType: schemas.BatchCancelRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.BatchCancel(nil, nil, nil)
			return err
		}},
		{name: "BatchDelete", requestType: schemas.BatchDeleteRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.BatchDelete(nil, nil, nil)
			return err
		}},
		{name: "BatchResults", requestType: schemas.BatchResultsRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.BatchResults(nil, nil, nil)
			return err
		}},
		{name: "FileUpload", requestType: schemas.FileUploadRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.FileUpload(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "FileList", requestType: schemas.FileListRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.FileList(nil, nil, nil)
			return err
		}},
		{name: "FileRetrieve", requestType: schemas.FileRetrieveRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.FileRetrieve(nil, nil, nil)
			return err
		}},
		{name: "FileDelete", requestType: schemas.FileDeleteRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.FileDelete(nil, nil, nil)
			return err
		}},
		{name: "FileContent", requestType: schemas.FileContentRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.FileContent(nil, nil, nil)
			return err
		}},
		{name: "ContainerCreate", requestType: schemas.ContainerCreateRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.ContainerCreate(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "ContainerList", requestType: schemas.ContainerListRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.ContainerList(nil, nil, nil)
			return err
		}},
		{name: "ContainerRetrieve", requestType: schemas.ContainerRetrieveRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.ContainerRetrieve(nil, nil, nil)
			return err
		}},
		{name: "ContainerDelete", requestType: schemas.ContainerDeleteRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.ContainerDelete(nil, nil, nil)
			return err
		}},
		{name: "ContainerFileCreate", requestType: schemas.ContainerFileCreateRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.ContainerFileCreate(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "ContainerFileList", requestType: schemas.ContainerFileListRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.ContainerFileList(nil, nil, nil)
			return err
		}},
		{name: "ContainerFileRetrieve", requestType: schemas.ContainerFileRetrieveRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.ContainerFileRetrieve(nil, nil, nil)
			return err
		}},
		{name: "ContainerFileContent", requestType: schemas.ContainerFileContentRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.ContainerFileContent(nil, nil, nil)
			return err
		}},
		{name: "ContainerFileDelete", requestType: schemas.ContainerFileDeleteRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.ContainerFileDelete(nil, nil, nil)
			return err
		}},
		{name: "Passthrough", requestType: schemas.PassthroughRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.Passthrough(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "PassthroughStream", requestType: schemas.PassthroughStreamRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.PassthroughStream(nil, nil, nil, schemas.Key{}, nil)
			return err
		}},
		{name: "CachedContentCreate", requestType: schemas.CachedContentCreateRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.CachedContentCreate(nil, schemas.Key{}, nil)
			return err
		}},
		{name: "CachedContentList", requestType: schemas.CachedContentListRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.CachedContentList(nil, nil, nil)
			return err
		}},
		{name: "CachedContentRetrieve", requestType: schemas.CachedContentRetrieveRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.CachedContentRetrieve(nil, nil, nil)
			return err
		}},
		{name: "CachedContentUpdate", requestType: schemas.CachedContentUpdateRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.CachedContentUpdate(nil, nil, nil)
			return err
		}},
		{name: "CachedContentDelete", requestType: schemas.CachedContentDeleteRequest, invoke: func(p *opencodeProvider) *schemas.BifrostError {
			_, err := p.CachedContentDelete(nil, nil, nil)
			return err
		}},
	}

	for _, provider := range providers {
		p := &opencodeProvider{providerKey: provider.key}

		for _, tc := range cases {
			t.Run(provider.name+"/"+tc.name, func(t *testing.T) {
				err := tc.invoke(p)
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if err.Error == nil {
					t.Fatal("expected Error field, got nil")
				}
				wantMsg := string(tc.requestType) + " is not supported by " + string(provider.key) + " provider"
				if err.Error.Message != wantMsg {
					t.Errorf("Error.Message = %q, want %q", err.Error.Message, wantMsg)
				}
				if err.ExtraFields.Provider != provider.key {
					t.Errorf("ExtraFields.Provider = %q, want %q", err.ExtraFields.Provider, provider.key)
				}
				if err.ExtraFields.RequestType != tc.requestType {
					t.Errorf("ExtraFields.RequestType = %q, want %q", err.ExtraFields.RequestType, tc.requestType)
				}
			})
		}
	}
}

// TestOpencodeResponsesRouting verifies the Responses routing split between the
// two gateways: Go forwards Responses calls to its native /v1/responses endpoint,
// while Zen falls back to /v1/chat/completions because its upstream does not
// implement the Responses API (see https://github.com/maximhq/bifrost/issues/6778).
func TestOpencodeResponsesRouting(t *testing.T) {
	const (
		model     = "opencode-test-model"
		apiKey    = "opencode-test-key"
		inputText = "exercise responses routing"
	)

	// chatResponse is the OpenAI chat-completions payload served to Zen. Its id
	// flows through ToBifrostResponsesResponse, so the provider surfaces it as the
	// Responses response id.
	chatResponse := `{"id":"chatcmpl-regular","object":"chat.completion","model":"opencode-test-model",` +
		`"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`

	// chatStream emits a minimal SSE stream: a role-bearing delta triggers the
	// created/in_progress lifecycle, then a content delta and a terminal chunk.
	chatStream := "" +
		`data: {"id":"chatcmpl-stream","object":"chat.completion.chunk","model":"opencode-test-model",` +
		`"choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}` + "\n\n" +
		`data: {"id":"chatcmpl-stream","object":"chat.completion.chunk","model":"opencode-test-model",` +
		`"choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}` + "\n\n" +
		`data: {"id":"chatcmpl-stream","object":"chat.completion.chunk","model":"opencode-test-model",` +
		`"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
		"data: [DONE]\n\n"

	responsesResponse := `{"id":"resp_regular","object":"response","model":"opencode-test-model","output":[]}`
	responsesStream := `data: {"type":"response.completed","sequence_number":1,"response":{"id":"resp_stream","object":"response","model":"opencode-test-model","output":[]}}` + "\n\n"

	for _, tc := range []struct {
		name        string
		providerKey schemas.ModelProvider
		newProvider func(*schemas.ProviderConfig) (*opencodeProvider, error)
	}{
		{name: "Zen", providerKey: schemas.OpencodeZen, newProvider: func(config *schemas.ProviderConfig) (*opencodeProvider, error) {
			return NewOpencodeZenProvider(config, nil)
		}},
		{name: "Go", providerKey: schemas.OpencodeGo, newProvider: func(config *schemas.ProviderConfig) (*opencodeProvider, error) {
			return NewOpencodeGoProvider(config, nil)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			zen := tc.providerKey == schemas.OpencodeZen
			expectedPath := "/v1/chat/completions"
			if !zen {
				expectedPath = "/v1/responses"
			}

			type capturedRequest struct {
				method        string
				path          string
				authorization string
				body          map[string]any
			}

			var (
				mu       sync.Mutex
				captures []capturedRequest
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
				captures = append(captures, capturedRequest{
					method:        r.Method,
					path:          r.URL.Path,
					authorization: r.Header.Get("Authorization"),
					body:          payload,
				})
				mu.Unlock()

				streaming, _ := payload["stream"].(bool)
				if zen {
					// Chat-completions wire format, as the fallback routes through
					// the OpenAI chat handler.
					w.Header().Set("Content-Type", "application/json")
					if streaming {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = fmt.Fprint(w, chatStream)
					} else {
						_, _ = fmt.Fprint(w, chatResponse)
					}
					return
				}

				// Native responses wire format.
				if streaming {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprint(w, responsesStream)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, responsesResponse)
			}))
			defer server.Close()

			provider, err := tc.newProvider(&schemas.ProviderConfig{
				NetworkConfig: schemas.NetworkConfig{
					BaseURL:                        server.URL,
					DefaultRequestTimeoutInSeconds: 10,
				},
			})
			if err != nil {
				t.Fatalf("new provider: %v", err)
			}

			newRequest := func() *schemas.BifrostResponsesRequest {
				return &schemas.BifrostResponsesRequest{
					Provider: tc.providerKey,
					Model:    model,
					Input: []schemas.ResponsesMessage{{
						Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr(inputText)},
					}},
				}
			}
			key := schemas.Key{Value: *schemas.NewSecretVar(apiKey)}

			ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			response, bifrostErr := provider.Responses(ctx, key, newRequest())
			if bifrostErr != nil {
				t.Fatalf("Responses: %v", bifrostErr)
			}
			if response == nil || response.ID == nil || *response.ID == "" {
				t.Fatalf("Responses returned %#v, want a response id", response)
			}

			streamCtx, streamCancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
			defer streamCancel()
			postHookRunner := func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, _ *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
				return result, nil
			}
			stream, bifrostErr := provider.ResponsesStream(streamCtx, postHookRunner, nil, key, newRequest())
			if bifrostErr != nil {
				t.Fatalf("ResponsesStream: %v", bifrostErr)
			}
			streamed := false
			for chunk := range stream {
				if chunk != nil {
					streamed = true
				}
			}
			if !streamed {
				t.Fatal("ResponsesStream completed without emitting a response chunk")
			}

			mu.Lock()
			defer mu.Unlock()
			if len(captures) != 2 {
				t.Fatalf("upstream request count = %d, want 2", len(captures))
			}
			seenRegular, seenStreaming := false, false
			for _, capture := range captures {
				if capture.method != http.MethodPost {
					t.Errorf("request method = %q, want %q", capture.method, http.MethodPost)
				}
				if capture.path != expectedPath {
					t.Errorf("request path = %q, want %q", capture.path, expectedPath)
				}
				if capture.authorization != "Bearer "+apiKey {
					t.Errorf("Authorization = %q, want %q", capture.authorization, "Bearer "+apiKey)
				}
				if gotModel, _ := capture.body["model"].(string); gotModel != model {
					t.Errorf("request model = %q, want %q", gotModel, model)
				}

				bodyField := "messages"
				if !zen {
					bodyField = "input"
				}
				messages, ok := capture.body[bodyField].([]any)
				if !ok || len(messages) != 1 {
					t.Errorf("request %s = %#v, want one message", bodyField, capture.body[bodyField])
				}

				if streaming, _ := capture.body["stream"].(bool); streaming {
					seenStreaming = true
				} else {
					seenRegular = true
				}
			}
			if !seenRegular || !seenStreaming {
				t.Errorf("saw regular=%t streaming=%t requests, want both", seenRegular, seenStreaming)
			}
		})
	}
}

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

// setOpencodeVirtualKey mimics a governed request: the transport stores the
// virtual key secret under BifrostContextKeyVirtualKey and the governance plugin
// resolves its ID. The secret is derived from the ID so tests can assert it
// never reaches the wire.
func setOpencodeVirtualKey(ctx *schemas.BifrostContext, id string) {
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, opencodeVirtualKeySecret(id))
	ctx.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyID, id)
}

// opencodeVirtualKeySecret is the fake virtual key value paired with id.
func opencodeVirtualKeySecret(id string) string {
	return "sk-bf-secret-" + id
}

// newWireChatRequest builds a minimal user chat completion request for the given
// provider, used by the wire tests to drive inference against the mock upstream.
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

// newWireResponsesRequest builds a minimal user Responses API request for the
// given provider, used by the wire tests to drive inference against the mock
// upstream.
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
		// Zen serves Responses through /v1/chat/completions, so the id comes
		// from the chat payload there.
		wantID := "resp_regular"
		if provider.fallbackResponsesToChat {
			wantID = "chatcmpl-test"
		}
		if response == nil || response.ID == nil || *response.ID != wantID {
			t.Fatalf("Responses returned %#v, want response id %s", response, wantID)
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
// configMutate, when given, is applied to the provider config before the
// provider is constructed (used to inject static ExtraHeaders).
func newOpencodeWireProvider(t *testing.T, newProvider func(*schemas.ProviderConfig) (*opencodeProvider, error), configMutate ...func(*schemas.ProviderConfig)) (*opencodeProvider, func() []wireCapture) {
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

	config := &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        server.URL,
			DefaultRequestTimeoutInSeconds: 10,
		},
	}
	if len(configMutate) > 0 && configMutate[0] != nil {
		configMutate[0](config)
	}
	provider, err := newProvider(config)
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
				configMutate     func(*schemas.ProviderConfig)
			}{
				{
					name: "client-sent header forwarded on every inference op",
					ops:  []opencodeOp{opChat, opResponses, opChatStream, opResponsesStream},
					setupCtx: func(t *testing.T) *schemas.BifrostContext {
						ctx := newOpencodeWireCtx(t, map[string]string{OpencodeSessionHeader: "client-sess-abc"})
						setOpencodeVirtualKey(ctx, "vk-1")
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
						setOpencodeVirtualKey(ctx, "vk-1")
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
						setOpencodeVirtualKey(ctx, "vk-1")
						return ctx
					},
					wantHeader: "vk-1:harness-sess-42",
				},
				{
					name: "synthesized UUID when no signal at all",
					ops:  []opencodeOp{opChat, opResponses, opChatStream, opResponsesStream},
					setupCtx: func(t *testing.T) *schemas.BifrostContext {
						ctx := newOpencodeWireCtx(t, nil)
						setOpencodeVirtualKey(ctx, "vk-1")
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
						setOpencodeVirtualKey(ctx, "vk-1")
						return ctx
					},
					wantHeader: "vk-1:bf-sess-123",
				},
				{
					name: "poisoned client header without fallback synthesizes a UUID",
					ops:  []opencodeOp{opChat},
					setupCtx: func(t *testing.T) *schemas.BifrostContext {
						ctx := newOpencodeWireCtx(t, map[string]string{OpencodeSessionHeader: "abc\x01def"})
						setOpencodeVirtualKey(ctx, "vk-1")
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
						setOpencodeVirtualKey(ctx, "vk-1")
						return ctx
					},
					wantHeaders:      []string{"vk-1:shared-session", "vk-2:shared-session"},
					switchVirtualKey: true,
				},
				{
					name: "client x-bf-eh- forwarded session header cannot override the resolved one",
					ops:  []opencodeOp{opChat, opResponses, opChatStream, opResponsesStream},
					setupCtx: func(t *testing.T) *schemas.BifrostContext {
						ctx := newOpencodeWireCtx(t, map[string]string{OpencodeSessionHeader: "client-sess-abc"})
						ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{OpencodeSessionHeader: {"forged-unnamespaced"}})
						setOpencodeVirtualKey(ctx, "vk-1")
						return ctx
					},
					wantHeader: "vk-1:client-sess-abc",
				},
				{
					name: "oversized client header is treated as absent",
					ops:  []opencodeOp{opChat},
					setupCtx: func(t *testing.T) *schemas.BifrostContext {
						ctx := newOpencodeWireCtx(t, map[string]string{OpencodeSessionHeader: strings.Repeat("a", schemas.MaxSessionIDLength+1)})
						ctx.SetValue(schemas.BifrostContextKeySessionID, "bf-sess-123")
						setOpencodeVirtualKey(ctx, "vk-1")
						return ctx
					},
					wantHeader: "vk-1:bf-sess-123",
				},
				{
					name: "virtual key value without a governance ID leaves the session unqualified",
					ops:  []opencodeOp{opChat, opChatStream},
					setupCtx: func(t *testing.T) *schemas.BifrostContext {
						ctx := newOpencodeWireCtx(t, map[string]string{OpencodeSessionHeader: "client-sess-abc"})
						ctx.SetValue(schemas.BifrostContextKeyVirtualKey, opencodeVirtualKeySecret("vk-1"))
						return ctx
					},
					wantHeader: "client-sess-abc",
				},
				{
					name: "resolved session beats case-variant static config header",
					ops:  []opencodeOp{opChat, opResponses, opChatStream, opResponsesStream},
					configMutate: func(config *schemas.ProviderConfig) {
						config.NetworkConfig.ExtraHeaders = schemas.NewSecretVarMap(map[string]string{
							"X-Opencode-Session": "static-sess",
							"Accept":             "application/json",
						})
					},
					setupCtx: func(t *testing.T) *schemas.BifrostContext {
						ctx := newOpencodeWireCtx(t, map[string]string{OpencodeSessionHeader: "client-sess-abc"})
						setOpencodeVirtualKey(ctx, "vk-1")
						return ctx
					},
					wantHeader: "vk-1:client-sess-abc",
				},
			}

			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					provider, captures := newOpencodeWireProvider(t, tc.newProvider, tt.configMutate)
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
							setOpencodeVirtualKey(ctx, "vk-2")
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
						if strings.Contains(capture.opencodeSession, "sk-bf-") {
							t.Errorf("request %d x-opencode-session = %q leaks the virtual key value", i, capture.opencodeSession)
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
	setOpencodeVirtualKey(ctx, "vk-1")

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
	setOpencodeVirtualKey(ctx, "vk-1")

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

// BenchmarkOpencodeInferenceAuthHeaders measures the per-call cost of building
// the auth headers with the resolved session, against the plain bearer header
// every OpenAI-compatible provider already builds.
func BenchmarkOpencodeInferenceAuthHeaders(b *testing.B) {
	provider := &opencodeProvider{}
	key := schemas.Key{Value: *schemas.NewSecretVar(wireTestAPIKey)}
	newCtx := func() *schemas.BifrostContext {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeySessionID, "bf-sess-123")
		ctx.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyID, "vk-1")
		return ctx
	}
	b.Run("baseline_bearer_only", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			ctx := newCtx()
			_ = openai.BearerAuthHeader(key)
			ctx.Cancel()
		}
	})
	b.Run("with_session", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			ctx := newCtx()
			_ = provider.inferenceAuthHeaders(ctx, key)
			ctx.Cancel()
		}
	})
	b.Run("with_session_cached", func(b *testing.B) {
		ctx := newCtx()
		defer ctx.Cancel()
		_ = provider.inferenceAuthHeaders(ctx, key)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = provider.inferenceAuthHeaders(ctx, key)
		}
	})
}

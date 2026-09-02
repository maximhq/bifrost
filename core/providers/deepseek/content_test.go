package deepseek_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// countingAnthropicServer answers like DeepSeek's /anthropic/v1/messages and
// counts arrivals, so a test can prove a request never left the process.
func countingAnthropicServer(hits *int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(hits, 1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"deepseek-v4-flash","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
}

// documentKey is a test key that selects DeepSeek's Anthropic-compatible endpoint.
func documentKey() schemas.Key {
	return schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}, UseAnthropicEndpoints: new(true)}
}

// chatWith builds a single-user-turn Chat request carrying one content block.
func chatWith(block schemas.ChatContentBlock) *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: schemas.DeepSeek,
		Model:    "deepseek-v4-flash",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{block}},
		}},
	}
}

// assertUnsupportedDocument checks the guard's error contract: typed 400,
// the deepseek_unsupported_document_content code, and AllowFallbacks set.
func assertUnsupportedDocument(t *testing.T, bifrostErr *schemas.BifrostError) {
	t.Helper()
	if bifrostErr == nil || bifrostErr.Error == nil {
		t.Fatalf("expected a typed error, got %#v", bifrostErr)
	}
	if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != 400 {
		t.Fatalf("status = %v, want 400", bifrostErr.StatusCode)
	}
	if bifrostErr.Error.Code == nil || *bifrostErr.Error.Code != "deepseek_unsupported_document_content" {
		t.Fatalf("code = %v, want deepseek_unsupported_document_content", bifrostErr.Error.Code)
	}
	if bifrostErr.AllowFallbacks == nil || !*bifrostErr.AllowFallbacks {
		t.Fatalf("AllowFallbacks = %v, want true so an enumerated fallback can carry the request", bifrostErr.AllowFallbacks)
	}
}

// A document block on the Anthropic endpoint is rejected locally: typed,
// fallback-eligible, and with zero arrivals at the upstream.
func TestChatCompletion_AnthropicEndpointRejectsDocumentBeforeEgress(t *testing.T) {
	t.Parallel()
	var hits int32
	server := countingAnthropicServer(&hits)
	defer server.Close()
	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}
	fileData := "data:application/pdf;base64,AAAA"
	resp, bifrostErr := provider.ChatCompletion(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), documentKey(),
		chatWith(schemas.ChatContentBlock{Type: schemas.ChatContentBlockTypeFile, File: &schemas.ChatInputFile{FileData: &fileData}}))
	if resp != nil {
		t.Fatalf("document request produced a response: %#v", resp)
	}
	assertUnsupportedDocument(t, bifrostErr)
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("upstream received %d request(s); want 0", got)
	}
}

// TestChatCompletionStream_AnthropicEndpointRejectsDocumentBeforeEgress is the
// streaming Chat counterpart: no stream is opened and nothing reaches upstream.
func TestChatCompletionStream_AnthropicEndpointRejectsDocumentBeforeEgress(t *testing.T) {
	t.Parallel()
	var hits int32
	server := countingAnthropicServer(&hits)
	defer server.Close()
	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}
	fileData := "data:application/pdf;base64,AAAA"
	stream, bifrostErr := provider.ChatCompletionStream(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), nil, nil, documentKey(),
		chatWith(schemas.ChatContentBlock{Type: schemas.ChatContentBlockTypeFile, File: &schemas.ChatInputFile{FileData: &fileData}}))
	if stream != nil {
		t.Fatalf("document request opened a stream")
	}
	assertUnsupportedDocument(t, bifrostErr)
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("upstream received %d request(s); want 0", got)
	}
}

// Images are documented as supported and must keep flowing unchanged.
func TestChatCompletion_AnthropicEndpointStillForwardsImages(t *testing.T) {
	t.Parallel()
	var hits int32
	server := countingAnthropicServer(&hits)
	defer server.Close()
	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}
	resp, bifrostErr := provider.ChatCompletion(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), documentKey(),
		chatWith(schemas.ChatContentBlock{Type: schemas.ChatContentBlockTypeImage, ImageURLStruct: &schemas.ChatInputImage{URL: "data:image/png;base64,AAAA"}}))
	if bifrostErr != nil {
		t.Fatalf("image request failed: %v", bifrostErr.Error.Message)
	}
	if resp == nil || atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("image request did not reach upstream exactly once (hits=%d, resp nil=%v)", hits, resp == nil)
	}
}

// Responses: a file block in a user turn and a file block inside a function
// tool-call output are both rejected before egress.
func TestResponses_AnthropicEndpointRejectsDocumentBeforeEgress(t *testing.T) {
	t.Parallel()
	var hits int32
	server := countingAnthropicServer(&hits)
	defer server.Close()
	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}
	fileBlock := schemas.ResponsesMessageContentBlock{Type: schemas.ResponsesInputMessageContentBlockTypeFile}

	userTurn := &schemas.BifrostResponsesRequest{Provider: schemas.DeepSeek, Model: "deepseek-v4-pro",
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{fileBlock}},
		}}}
	resp, bifrostErr := provider.Responses(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), documentKey(), userTurn)
	if resp != nil {
		t.Fatalf("document user turn produced a response")
	}
	assertUnsupportedDocument(t, bifrostErr)

	callID := "call_1"
	toolOutput := &schemas.BifrostResponsesRequest{Provider: schemas.DeepSeek, Model: "deepseek-v4-pro",
		Input: []schemas.ResponsesMessage{{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID: &callID,
				Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesFunctionToolCallOutputBlocks: []schemas.ResponsesMessageContentBlock{fileBlock}},
			},
		}}}
	resp, bifrostErr = provider.Responses(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), documentKey(), toolOutput)
	if resp != nil {
		t.Fatalf("document tool output produced a response")
	}
	assertUnsupportedDocument(t, bifrostErr)

	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("upstream received %d request(s); want 0", got)
	}
}

// TestResponsesStream_AnthropicEndpointRejectsDocumentBeforeEgress is the
// streaming Responses counterpart: no stream is opened and nothing reaches upstream.
func TestResponsesStream_AnthropicEndpointRejectsDocumentBeforeEgress(t *testing.T) {
	t.Parallel()
	var hits int32
	server := countingAnthropicServer(&hits)
	defer server.Close()
	provider, err := newTestDeepSeekProvider(server.URL)
	if err != nil {
		t.Fatalf("NewDeepSeekProvider: %v", err)
	}
	req := &schemas.BifrostResponsesRequest{Provider: schemas.DeepSeek, Model: "deepseek-v4-flash",
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{{Type: schemas.ResponsesInputMessageContentBlockTypeFile}}},
		}}}
	stream, bifrostErr := provider.ResponsesStream(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), nil, nil, documentKey(), req)
	if stream != nil {
		t.Fatalf("document request opened a stream")
	}
	assertUnsupportedDocument(t, bifrostErr)
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("upstream received %d request(s); want 0", got)
	}
}

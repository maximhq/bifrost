package openai

// Regression tests for issue #6419: Azure kills a content-filtered /v1/responses
// stream with THREE frames: response.created (seq 0), response.failed (seq 1,
// error null inside the response object), then a terminal type:"error" frame
// (seq 2) whose nested error object carries the content_policy_violation
// code/message while the top-level code/message/param are null. The failed
// branch used to surface the detail-free response.failed and return, so the
// richer error frame was never read and clients got "provider stream error
// (response.failed)" with a nil code.

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func responsesStreamRequest() *schemas.BifrostResponsesRequest {
	return &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "repro-model",
		Input: []schemas.ResponsesMessage{{
			Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")},
		}},
	}
}

func runResponsesStream(t *testing.T, frames string) (chunks []*schemas.BifrostStreamChunk, lastErr *schemas.BifrostError) {
	t.Helper()
	server := completeSSEServer(t, frames)
	defer server.Close()

	provider := newStreamTestProvider(server.URL)
	stream, bifrostErr := provider.ResponsesStream(newStreamTestContext(), passthroughPostHook, nil, testKey(), responsesStreamRequest())
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}
	chunks = collectChunks(t, stream)
	for _, chunk := range chunks {
		if chunk.BifrostError != nil {
			lastErr = chunk.BifrostError
		}
	}
	return chunks, lastErr
}

func TestResponsesStreamErrorFrameAfterDetailFreeFailed(t *testing.T) {
	frames := `data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_1","object":"response","created_at":1,"model":"repro-model","status":"in_progress"}}

data: {"type":"response.failed","sequence_number":1,"response":{"id":"resp_1","object":"response","created_at":1,"model":"repro-model","status":"failed","error":null}}

data: {"type":"error","sequence_number":2,"code":null,"message":null,"param":null,"error":{"type":"invalid_request_error","code":"content_policy_violation","message":"Image processing blocked due to content policy violation.","param":"input"}}

`
	_, got := runResponsesStream(t, frames)
	if got == nil || got.Error == nil {
		t.Fatal("expected a terminal error chunk, got none")
	}
	if got.Error.Code == nil || *got.Error.Code != "content_policy_violation" {
		t.Fatalf("error code = %#v, want content_policy_violation (message = %q)", got.Error.Code, got.Error.Message)
	}
	if got.Error.Message != "Image processing blocked due to content policy violation." {
		t.Fatalf("error message = %q, want the content-policy message from the terminal error frame", got.Error.Message)
	}
}

// A response.failed that carries its own error details (the Fireworks shape)
// must keep surfacing immediately.
func TestResponsesStreamFailedWithDetailSurfacesImmediately(t *testing.T) {
	frames := `data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_1","object":"response","created_at":1,"model":"repro-model","status":"in_progress"}}

data: {"type":"response.failed","sequence_number":1,"response":{"id":"resp_1","object":"response","created_at":1,"model":"repro-model","status":"failed","error":{"code":"server_error","message":"upstream exploded"}}}

`
	_, got := runResponsesStream(t, frames)
	if got == nil || got.Error == nil {
		t.Fatal("expected a terminal error chunk, got none")
	}
	if got.Error.Code == nil || *got.Error.Code != "server_error" {
		t.Fatalf("error code = %#v, want server_error", got.Error.Code)
	}
	if got.Error.Message != "upstream exploded" {
		t.Fatalf("error message = %q, want the failed event's own message", got.Error.Message)
	}
}

// A detail-free response.failed with nothing after it must still produce the
// generic failed-derived error at stream end, not a silent close or a
// truncation error.
func TestResponsesStreamDetailFreeFailedWithoutErrorFrame(t *testing.T) {
	frames := `data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_1","object":"response","created_at":1,"model":"repro-model","status":"in_progress"}}

data: {"type":"response.failed","sequence_number":1,"response":{"id":"resp_1","object":"response","created_at":1,"model":"repro-model","status":"failed","error":null}}

`
	_, got := runResponsesStream(t, frames)
	if got == nil || got.Error == nil {
		t.Fatal("expected a terminal error chunk, got none")
	}
	if got.Error.Message == "" || got.Type == nil || *got.Type != string(schemas.ResponsesStreamResponseTypeFailed) {
		t.Fatalf("expected the failed-derived error at stream end, got type=%v message=%q", got.Type, got.Error.Message)
	}
}

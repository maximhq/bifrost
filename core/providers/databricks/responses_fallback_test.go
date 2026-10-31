package databricks_test

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

const stubResponsesResponse = `{
	"id": "resp-1",
	"object": "response",
	"created_at": 1700000000,
	"model": "m",
	"status": "completed",
	"output": [{"type": "message", "id": "msg-1", "status": "completed", "role": "assistant", "content": [{"type": "output_text", "text": "ok", "annotations": []}]}],
	"usage": {"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}
}`

const stubResponsesStream = "event: response.created\n" +
	`data: {"type":"response.created","sequence_number":0,"response":{"id":"resp-1","object":"response","created_at":1,"model":"m","status":"in_progress"}}` + "\n\n" +
	"event: response.completed\n" +
	`data: {"type":"response.completed","sequence_number":1,"response":{"id":"resp-1","object":"response","created_at":1,"model":"m","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` + "\n\n"

const stubChatStream = `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":null}]}` + "\n\n" +
	`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}` + "\n\n" +
	"data: [DONE]\n\n"

const passthroughUnsupportedBody = `{"error":{"message":"Responses API passthrough is not supported for model databricks-claude-sonnet-4-5.","type":"invalid_request_error"}}`

// pathRecorder is a stub workspace that answers /responses according to responsesStatus and
// serves chat completions normally, remembering every path it was asked for.
type pathRecorder struct {
	mu              sync.Mutex
	paths           []string
	responsesStatus int
	responsesBody   string
}

func (p *pathRecorder) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.paths = append(p.paths, r.URL.Path)
		p.mu.Unlock()

		stream := r.URL.Query().Get("stream") == "true" || r.Header.Get("Accept") == "text/event-stream"
		switch r.URL.Path {
		case "/serving-endpoints/responses":
			if p.responsesStatus != http.StatusOK {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(p.responsesStatus)
				_, _ = w.Write([]byte(p.responsesBody))
				return
			}
			if stream {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(stubResponsesStream))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(stubResponsesResponse))
		case "/serving-endpoints/chat/completions":
			if stream {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(stubChatStream))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(stubChatResponse))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func (p *pathRecorder) seen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.paths...)
}

func responsesRequest(model string) *schemas.BifrostResponsesRequest {
	return &schemas.BifrostResponsesRequest{
		Provider: schemas.Databricks,
		Model:    model,
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")},
		}},
	}
}

func equalPaths(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestDatabricksResponsesFallsBackWhenPassthroughUnsupported reproduces the live failure:
// pay-per-token endpoints answer /serving-endpoints/responses with a 400 saying the surface
// is not supported. The request must then be served through chat completions, and once an
// endpoint has declined, later requests for the same model skip the failing round trip.
func TestDatabricksResponsesFallsBackWhenPassthroughUnsupported(t *testing.T) {
	t.Parallel()

	recorder := &pathRecorder{responsesStatus: http.StatusBadRequest, responsesBody: passthroughUnsupportedBody}
	provider, server := newStubProvider(t, recorder.handler(t))
	key := schemas.Key{
		Models: []string{"*"},
		Value:  *schemas.NewSecretVar("dapi-test"),
		DatabricksKeyConfig: &schemas.DatabricksKeyConfig{
			WorkspaceURL: *schemas.NewSecretVar(serverHost(t, server)),
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	response, bErr := provider.Responses(ctx, key, responsesRequest("databricks-claude-sonnet-4-5"))
	if bErr != nil {
		t.Fatalf("Responses returned an error: %v", bErr)
	}
	if response == nil || len(response.Output) == 0 {
		t.Fatalf("Responses returned no output through the chat fallback: %#v", response)
	}
	want := []string{"/serving-endpoints/responses", "/serving-endpoints/chat/completions"}
	if got := recorder.seen(); !equalPaths(got, want) {
		t.Fatalf("first request paths: got %v, want %v", got, want)
	}

	if _, bErr := provider.Responses(ctx, key, responsesRequest("databricks-claude-sonnet-4-5")); bErr != nil {
		t.Fatalf("second Responses returned an error: %v", bErr)
	}
	want = append(want, "/serving-endpoints/chat/completions")
	if got := recorder.seen(); !equalPaths(got, want) {
		t.Errorf("second request must skip the native route: got %v, want %v", got, want)
	}

	// The decision is per model: another endpoint on the same workspace still gets a try.
	if _, bErr := provider.Responses(ctx, key, responsesRequest("databricks-gpt-oss-120b")); bErr != nil {
		t.Fatalf("Responses for a second model returned an error: %v", bErr)
	}
	want = append(want, "/serving-endpoints/responses", "/serving-endpoints/chat/completions")
	if got := recorder.seen(); !equalPaths(got, want) {
		t.Errorf("a different model must try the native route: got %v, want %v", got, want)
	}
}

// TestDatabricksResponsesStreamFallsBackWhenPassthroughUnsupported covers the streaming
// path, where the 400 arrives before any chunk so retrying through chat loses nothing.
func TestDatabricksResponsesStreamFallsBackWhenPassthroughUnsupported(t *testing.T) {
	t.Parallel()

	recorder := &pathRecorder{responsesStatus: http.StatusBadRequest, responsesBody: passthroughUnsupportedBody}
	provider, server := newStubProvider(t, recorder.handler(t))
	key := schemas.Key{
		Models: []string{"*"},
		Value:  *schemas.NewSecretVar("dapi-test"),
		DatabricksKeyConfig: &schemas.DatabricksKeyConfig{
			WorkspaceURL: *schemas.NewSecretVar(serverHost(t, server)),
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stream, bErr := provider.ResponsesStream(ctx, noopPostHook, nil, key, responsesRequest("databricks-claude-sonnet-4-5"))
	if bErr != nil {
		t.Fatalf("ResponsesStream returned an error: %v", bErr)
	}
	chunks := 0
	for range stream {
		chunks++
	}
	if chunks == 0 {
		t.Fatal("ResponsesStream produced no chunks through the chat fallback")
	}
	want := []string{"/serving-endpoints/responses", "/serving-endpoints/chat/completions"}
	if got := recorder.seen(); !equalPaths(got, want) {
		t.Fatalf("paths: got %v, want %v", got, want)
	}

	stream, bErr = provider.ResponsesStream(ctx, noopPostHook, nil, key, responsesRequest("databricks-claude-sonnet-4-5"))
	if bErr != nil {
		t.Fatalf("second ResponsesStream returned an error: %v", bErr)
	}
	for range stream {
	}
	want = append(want, "/serving-endpoints/chat/completions")
	if got := recorder.seen(); !equalPaths(got, want) {
		t.Errorf("second stream must skip the native route: got %v, want %v", got, want)
	}
}

// TestDatabricksResponsesStaysNativeWhenSupported pins that an endpoint which does serve the
// Responses API is not needlessly emulated.
func TestDatabricksResponsesStaysNativeWhenSupported(t *testing.T) {
	t.Parallel()

	recorder := &pathRecorder{responsesStatus: http.StatusOK}
	provider, server := newStubProvider(t, recorder.handler(t))
	key := schemas.Key{
		Models: []string{"*"},
		Value:  *schemas.NewSecretVar("dapi-test"),
		DatabricksKeyConfig: &schemas.DatabricksKeyConfig{
			WorkspaceURL: *schemas.NewSecretVar(serverHost(t, server)),
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, bErr := provider.Responses(ctx, key, responsesRequest("databricks-claude-sonnet-4-5")); bErr != nil {
		t.Fatalf("Responses returned an error: %v", bErr)
	}
	if _, bErr := provider.Responses(ctx, key, responsesRequest("databricks-claude-sonnet-4-5")); bErr != nil {
		t.Fatalf("second Responses returned an error: %v", bErr)
	}
	want := []string{"/serving-endpoints/responses", "/serving-endpoints/responses"}
	if got := recorder.seen(); !equalPaths(got, want) {
		t.Errorf("paths: got %v, want %v", got, want)
	}
}

// TestDatabricksResponsesOtherErrorsAreNotRetried pins that only the "surface not
// supported" rejection triggers emulation. Any other 400 is the caller's request being
// wrong, and replaying it through chat would only hide the real message.
func TestDatabricksResponsesOtherErrorsAreNotRetried(t *testing.T) {
	t.Parallel()

	recorder := &pathRecorder{
		responsesStatus: http.StatusBadRequest,
		responsesBody:   `{"error":{"message":"max_output_tokens must be positive","type":"invalid_request_error"}}`,
	}
	provider, server := newStubProvider(t, recorder.handler(t))
	key := schemas.Key{
		Models: []string{"*"},
		Value:  *schemas.NewSecretVar("dapi-test"),
		DatabricksKeyConfig: &schemas.DatabricksKeyConfig{
			WorkspaceURL: *schemas.NewSecretVar(serverHost(t, server)),
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, bErr := provider.Responses(ctx, key, responsesRequest("databricks-claude-sonnet-4-5"))
	if bErr == nil {
		t.Fatal("Responses succeeded, want the upstream 400 surfaced")
	}
	if bErr.Error == nil || bErr.Error.Message != "max_output_tokens must be positive" {
		t.Errorf("error message: got %#v, want the upstream message", bErr.Error)
	}
	want := []string{"/serving-endpoints/responses"}
	if got := recorder.seen(); !equalPaths(got, want) {
		t.Errorf("paths: got %v, want %v (no chat retry)", got, want)
	}
}

func noopPostHook(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return result, err
}

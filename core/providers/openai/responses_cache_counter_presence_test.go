package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// TestRawResponsesCacheCountersKnown verifies that only a raw detail object
// counts as observed cache-counter evidence.
func TestRawResponsesCacheCountersKnown(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		streaming bool
		want      bool
	}{
		{"unary present even when empty", `{"usage":{"input_tokens_details":{}}}`, false, true},
		{"unary missing", `{"usage":{"input_tokens":10}}`, false, false},
		{"unary null", `{"usage":{"input_tokens_details":null}}`, false, false},
		{"unary scalar is not a detail object", `{"usage":{"input_tokens_details":0}}`, false, false},
		{"stream present", `{"type":"response.completed","response":{"usage":{"input_tokens_details":{"cached_tokens":0}}}}`, true, true},
		{"stream missing", `{"type":"response.completed","response":{"usage":{"input_tokens":10}}}`, true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rawResponsesCacheCountersKnown([]byte(tt.body), tt.streaming)
			if got != tt.want {
				t.Fatalf("knownness = %t, want %t", got, tt.want)
			}
		})
	}
}

// TestResponsesStreamCacheCounterKnownnessBoundary verifies that only a
// terminal Responses event publishes cache-counter evidence to post hooks.
func TestResponsesStreamCacheCounterKnownnessBoundary(t *testing.T) {
	tests := []struct {
		name        string
		serve       func(*testing.T) *httptest.Server
		wantPresent bool
		wantKnown   bool
	}{
		{
			name: "completed with details publishes known",
			serve: func(t *testing.T) *httptest.Server {
				return completeSSEServer(t, `data: {"type":"response.completed","sequence_number":1,"response":{"id":"r1","object":"response","created_at":1,"model":"repro-model","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2,"input_tokens_details":{"cached_tokens":0}}}}`+"\n\n")
			},
			wantPresent: true,
			wantKnown:   true,
		},
		{
			name: "incomplete without details publishes unknown",
			serve: func(t *testing.T) *httptest.Server {
				return completeSSEServer(t, `data: {"type":"response.incomplete","sequence_number":1,"response":{"id":"r2","object":"response","created_at":1,"model":"repro-model","status":"incomplete","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`+"\n\n")
			},
			wantPresent: true,
			wantKnown:   false,
		},
		{
			name: "truncated before terminal publishes nothing",
			serve: func(t *testing.T) *httptest.Server {
				return truncatingSSEServer(t, `data: {"type":"response.output_text.delta","sequence_number":1,"delta":"partial"}`+"\n\n")
			},
			wantPresent: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := tt.serve(t)
			defer server.Close()

			ctx := newStreamTestContext()
			provider := newStreamTestProvider(server.URL)
			request := &schemas.BifrostResponsesRequest{
				Provider: schemas.OpenAI,
				Model:    "repro-model",
				Input: []schemas.ResponsesMessage{{
					Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
					Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
					Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")},
				}},
			}
			stream, bifrostErr := provider.ResponsesStream(ctx, passthroughPostHook, nil, testKey(), request)
			if bifrostErr != nil {
				t.Fatalf("stream setup failed: %v", bifrostErr)
			}
			collectChunks(t, stream)

			known, present := ctx.Value(schemas.BifrostContextKeyRawResponsesCacheCountersKnown).(bool)
			if present != tt.wantPresent {
				t.Fatalf("raw cache-counter knownness present = %t, want %t", present, tt.wantPresent)
			}
			if present && known != tt.wantKnown {
				t.Fatalf("raw cache-counter knownness = %t, want %t", known, tt.wantKnown)
			}
		})
	}
}

// TestLargeRequestPassthroughPreservesRawCacheCounterPresence exercises the
// early-return boundary that bypasses the ordinary Responses parser.
func TestLargeRequestPassthroughPreservesRawCacheCounterPresence(t *testing.T) {
	tests := []struct {
		name         string
		usageDetails string
		want         bool
	}{
		{"present even when empty", `,"input_tokens_details":{}`, true},
		{"missing before defaults", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, err := io.Copy(io.Discard, r.Body); err != nil {
					http.Error(w, "request read failed", http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"resp_large_request","object":"response","status":"completed","model":"gpt-test","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2` + tt.usageDetails + `}}`))
			}))
			defer server.Close()

			requestBody := `{"model":"gpt-test","input":"large request"}`
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			ctx.SetValue(schemas.BifrostContextKeyLargePayloadMode, true)
			ctx.SetValue(schemas.BifrostContextKeyLargePayloadReader, strings.NewReader(requestBody))
			ctx.SetValue(schemas.BifrostContextKeyLargePayloadContentLength, len(requestBody))
			ctx.SetValue(schemas.BifrostContextKeyLargePayloadContentType, "application/json")

			response, bifrostErr := HandleOpenAIResponsesRequest(
				ctx,
				&fasthttp.Client{},
				server.URL,
				&schemas.BifrostResponsesRequest{Model: "gpt-test"},
				nil,
				nil,
				false,
				false,
				schemas.OpenAI,
				nil,
				nil,
				nil,
				testNoopLogger{},
			)
			if bifrostErr != nil {
				t.Fatalf("large-payload Responses request failed: %v", bifrostErr)
			}
			known, ok := ctx.Value(schemas.BifrostContextKeyRawResponsesCacheCountersKnown).(bool)
			if !ok || known != tt.want {
				t.Fatalf("raw cache-counter knownness = (%t, %t), want (%t, true)", known, ok, tt.want)
			}
			if response == nil {
				t.Fatal("expected a typed response")
			}
			normalized := response.WithDefaults()
			if normalized.Usage == nil || normalized.Usage.InputTokensDetails == nil {
				t.Fatal("expected WithDefaults to materialize input token details")
			}
		})
	}
}

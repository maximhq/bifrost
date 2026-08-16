package openai

import "testing"

// TestErrorInSuccessfulChatBody pins that an error object smuggled into a 2xx
// chat-completions body is surfaced as an error rather than reported as success.
//
// HandleOpenAIChatCompletionRequest only treats a response as failed when the HTTP
// status is non-200; on a 200 it unmarshals into BifrostChatResponse and returns
// whatever came back. OpenAI-compatible providers do not all honour that contract.
// OpenRouter documents that once generation has started "the HTTP 200 OK status and
// headers are already committed - they can't be changed", so a failure is delivered
// in-band as {"error": {code, message}} alongside a 200
// (https://openrouter.ai/docs/api-reference/errors). Without this check the caller
// receives a 200 whose choices and usage are both null - a silent failure.
func TestErrorInSuccessfulChatBody(t *testing.T) {
	tests := []struct {
		name           string
		body           string
		wantErr        bool
		wantMessage    string
		wantStatusCode int
	}{
		{
			// OpenRouter's documented shape: error.code is a NUMBER, unlike
			// OpenAI's string codes.
			name:           "openrouter numeric error code",
			body:           `{"error":{"code":429,"message":"rate limited upstream","metadata":{"error_type":"provider_error"}}}`,
			wantErr:        true,
			wantMessage:    "rate limited upstream",
			wantStatusCode: 429,
		},
		{
			name:           "openai string error code falls back to bad gateway",
			body:           `{"error":{"code":"invalid_request_error","message":"bad request"}}`,
			wantErr:        true,
			wantMessage:    "bad request",
			wantStatusCode: 502,
		},
		{
			// The mid-stream shape carries both an error and a choices entry
			// whose finish_reason is "error". The error still wins.
			name:           "error alongside a finish_reason error choice",
			body:           `{"object":"chat.completion.chunk","choices":[{"finish_reason":"error"}],"error":{"code":500,"message":"provider exploded"}}`,
			wantErr:        true,
			wantMessage:    "provider exploded",
			wantStatusCode: 500,
		},
		{
			// Everything below must stay nil: these are the paths that work
			// today and must not start erroring.
			name: "ordinary successful completion",
			body: `{"id":"cmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}],"usage":{"total_tokens":5}}`,
		},
		{
			name: "explicit null error",
			body: `{"id":"cmpl-1","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}],"error":null}`,
		},
		{
			name: "error object with no message",
			body: `{"id":"cmpl-1","choices":[],"error":{}}`,
		},
		{
			name: "error present but not an object",
			body: `{"id":"cmpl-1","choices":[],"error":"something"}`,
		},
		{
			name: "empty body",
			body: ``,
		},
		{
			name: "invalid json",
			body: `not json at all`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ErrorInSuccessfulChatBody([]byte(tt.body))
			if !tt.wantErr {
				if got != nil {
					t.Fatalf("expected no error, got %+v", got.Error)
				}
				return
			}
			if got == nil {
				t.Fatal("expected an error, got nil")
			}
			if got.Error == nil {
				t.Fatal("expected a populated Error field")
			}
			if got.Error.Message != tt.wantMessage {
				t.Errorf("message = %q, want %q", got.Error.Message, tt.wantMessage)
			}
			if got.StatusCode == nil {
				t.Fatalf("status code unset, want %d", tt.wantStatusCode)
			}
			if *got.StatusCode != tt.wantStatusCode {
				t.Errorf("status code = %d, want %d", *got.StatusCode, tt.wantStatusCode)
			}
			// The upstream produced this, so it must not be attributed to Bifrost.
			if got.IsBifrostError {
				t.Error("IsBifrostError = true; an upstream in-band error is not a Bifrost error")
			}
		})
	}
}

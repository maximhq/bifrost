package openrouter

import (
	"strings"
	"testing"

	"github.com/valyala/fasthttp"
)

// errorResponse builds the fasthttp response the parser sees for one OpenRouter body.
func errorResponse(status int, body string) *fasthttp.Response {
	resp := fasthttp.AcquireResponse()
	resp.SetStatusCode(status)
	resp.Header.SetContentType("application/json")
	resp.SetBodyString(body)
	return resp
}

// TestParseOpenRouterErrorLiftsUpstreamMessage covers every shape OpenRouter has been
// seen to put in error.metadata.raw, plus the bodies where nothing should change.
func TestParseOpenRouterErrorLiftsUpstreamMessage(t *testing.T) {
	const anthropicRefusal = "messages.1.content.0: Invalid `signature` in `thinking` block"
	for _, tc := range []struct {
		name, body, wantMessage string
	}{
		{
			name:        "raw is the provider's error object",
			body:        `{"error":{"code":400,"message":"Provider returned error","metadata":{"provider_name":"Anthropic","raw":{"type":"error","error":{"type":"invalid_request_error","message":"` + anthropicRefusal + `"}}}}}`,
			wantMessage: "Provider returned error (Anthropic): " + anthropicRefusal,
		},
		{
			name:        "raw is a plain string",
			body:        `{"error":{"code":400,"message":"Provider returned error","metadata":{"provider_name":"xAI","raw":"Could not decrypt the provided encrypted_content."}}}`,
			wantMessage: "Provider returned error (xAI): Could not decrypt the provided encrypted_content.",
		},
		{
			name:        "raw is a stringified provider body",
			body:        `{"error":{"code":400,"message":"Provider returned error","metadata":{"provider_name":"OpenAI","raw":"{\"error\":{\"message\":\"The encrypted content for item rs_1 could not be verified.\",\"code\":\"invalid_encrypted_content\"}}"}}}`,
			wantMessage: "Provider returned error (OpenAI): The encrypted content for item rs_1 could not be verified.",
		},
		{
			name:        "raw has a top-level message and no provider name",
			body:        `{"error":{"code":400,"message":"Provider returned error","metadata":{"raw":{"message":"Corrupted thought signature."}}}}`,
			wantMessage: "Provider returned error: Corrupted thought signature.",
		},
		{
			name:        "no metadata keeps OpenRouter's own message",
			body:        `{"error":{"code":402,"message":"Insufficient credits"}}`,
			wantMessage: "Insufficient credits",
		},
		{
			name:        "unrecognised raw shape keeps OpenRouter's own message",
			body:        `{"error":{"code":400,"message":"Provider returned error","metadata":{"provider_name":"Foo","raw":[1,2,3]}}}`,
			wantMessage: "Provider returned error",
		},
		{
			name:        "stringified raw with no message keeps OpenRouter's own message",
			body:        `{"error":{"code":400,"message":"Provider returned error","metadata":{"provider_name":"Foo","raw":"{\"detail\":\"rate limit\",\"request\":{\"reasoning\":{\"signature\":\"x\"}}}"}}}`,
			wantMessage: "Provider returned error",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := errorResponse(400, tc.body)
			defer fasthttp.ReleaseResponse(resp)
			got := parseOpenRouterError(resp)
			if got == nil || got.Error == nil {
				t.Fatal("expected an error")
			}
			if got.Error.Message != tc.wantMessage {
				t.Fatalf("message = %q, want %q", got.Error.Message, tc.wantMessage)
			}
			if got.StatusCode == nil || *got.StatusCode != 400 {
				t.Fatalf("status = %v, want 400", got.StatusCode)
			}
		})
	}
}

// TestParseOpenRouterErrorKeepsCodeAndSurvivesBadBodies pins that lifting the message
// never loses the OpenAI-envelope fields and never panics on a body that is not JSON.
func TestParseOpenRouterErrorKeepsCodeAndSurvivesBadBodies(t *testing.T) {
	resp := errorResponse(400, `{"error":{"code":"400","type":"invalid_request_error","message":"Provider returned error","metadata":{"provider_name":"Anthropic","raw":{"error":{"message":"boom"}}}}}`)
	defer fasthttp.ReleaseResponse(resp)
	got := parseOpenRouterError(resp)
	if got.Error.Code == nil || *got.Error.Code != "400" || got.Error.Type == nil || *got.Error.Type != "invalid_request_error" {
		t.Fatalf("lost envelope fields: %+v", got.Error)
	}
	if !strings.HasSuffix(got.Error.Message, ": boom") {
		t.Fatalf("message = %q", got.Error.Message)
	}

	plain := errorResponse(502, "<html>bad gateway</html>")
	defer fasthttp.ReleaseResponse(plain)
	if got := parseOpenRouterError(plain); got == nil || got.StatusCode == nil || *got.StatusCode != 502 {
		t.Fatalf("non-JSON body: %+v", got)
	}
}

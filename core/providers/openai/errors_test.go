package openai

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

func TestResponsesStreamError_NormalizesAzureShapeAndEmptyDetails(t *testing.T) {
	cases := []struct {
		wire, wantMessage string
	}{
		{`{"type":"error","error":{"type":"too_many_requests","code":"no_capacity","message":"capacity"}}`, "capacity"},
		{`{"type":"response.failed","response":{"error":{"code":"context_length_exceeded","message":"input is too large"}}}`, "input is too large"},
		{`{"type":"error","error":{}}`, "provider stream error (error)"},
	}
	for _, tc := range cases {
		var response schemas.BifrostResponsesStreamResponse
		if err := schemas.Unmarshal([]byte(tc.wire), &response); err != nil {
			t.Fatalf("unmarshal stream error: %v", err)
		}
		got := responsesStreamError(&response)
		if got == nil || got.Error == nil || got.Error.Message != tc.wantMessage {
			t.Fatalf("normalized error = %v, want message %q", got, tc.wantMessage)
		}
	}
}

func TestParseOpenAIError_FallbackMessageWhenProviderBodyIsNonOpenAIShape(t *testing.T) {
	var resp fasthttp.Response
	resp.SetStatusCode(fasthttp.StatusUnprocessableEntity)
	resp.SetBodyString(`{"detail":[{"loc":["body","messages",0,"role"],"msg":"value is not a valid enumeration member"}]}`)

	errResp := ParseOpenAIError(&resp)
	if errResp == nil || errResp.Error == nil {
		t.Fatal("expected non-nil error response")
	}
	if errResp.Error.Message == "" {
		t.Fatal("expected non-empty error message")
	}
	if errResp.Error.Message != "provider API error (status 422)" {
		t.Fatalf("expected fallback message, got %q", errResp.Error.Message)
	}
}

func TestParseOpenAIError_PreservesProviderMessageWhenPresent(t *testing.T) {
	var resp fasthttp.Response
	resp.SetStatusCode(fasthttp.StatusUnprocessableEntity)
	resp.SetBodyString(`{"error":{"message":"unsupported role: developer","type":"invalid_request_error","param":"messages.0.role","code":"invalid_value"}}`)

	errResp := ParseOpenAIError(&resp)
	if errResp == nil || errResp.Error == nil {
		t.Fatal("expected non-nil error response")
	}
	if errResp.Error.Message != "unsupported role: developer" {
		t.Fatalf("expected provider message, got %q", errResp.Error.Message)
	}
}

func TestParseOpenAIError_FallbackMessageWhenBodyIsEmpty(t *testing.T) {
	var resp fasthttp.Response
	resp.SetStatusCode(fasthttp.StatusBadRequest)
	resp.SetBody(nil)

	errResp := ParseOpenAIError(&resp)
	if errResp == nil || errResp.Error == nil {
		t.Fatal("expected non-nil error response")
	}
	// HandleProviderAPIError returns ErrProviderResponseEmpty with HTTP status for empty bodies.
	expectedMsg := schemas.ErrProviderResponseEmpty + " (HTTP 400)"
	if errResp.Error.Message != expectedMsg {
		t.Fatalf("expected %q, got %q", expectedMsg, errResp.Error.Message)
	}
}

func TestParseOpenAIError_WhitespaceProviderMessageFallsBack(t *testing.T) {
	var resp fasthttp.Response
	resp.SetStatusCode(fasthttp.StatusBadRequest)
	resp.SetBodyString(`{"error":{"message":"   ","type":"invalid_request_error"}}`)

	errResp := ParseOpenAIError(&resp)
	if errResp == nil || errResp.Error == nil {
		t.Fatal("expected non-nil error response")
	}
	if errResp.Error.Message != "provider API error (status 400)" {
		t.Fatalf("expected fallback message, got %q", errResp.Error.Message)
	}
}

func TestParseOpenAIError_DefaultStatusCodeFallsBackWithStatusNumber(t *testing.T) {
	var resp fasthttp.Response
	// fasthttp defaults zero-value response status code to 200.
	resp.SetBodyString(`{"error":{"message":""}}`)

	errResp := ParseOpenAIError(&resp)
	if errResp == nil || errResp.Error == nil {
		t.Fatal("expected non-nil error response")
	}
	if errResp.Error.Message != "provider API error (status 200)" {
		t.Fatalf("expected fallback message with default status, got %q", errResp.Error.Message)
	}
}

// An SSE error rides a committed HTTP 200, so the error carries no status of its
// own. Left nil it reaches metrics as a caller 400 and ClassifyFailure has nothing
// to act on.
func TestResponsesStreamError_CarriesGatewayStatus(t *testing.T) {
	cases := []struct {
		name, wire string
		want       int
	}{
		{
			"overload with no recognizable signal defaults to 502",
			`{"type":"response.failed","response":{"error":{"code":"server_error","message":"Our servers are currently overloaded. Please try again later."}}}`,
			fasthttp.StatusBadGateway,
		},
		{
			"empty error object still gets a status",
			`{"type":"error","error":{}}`,
			fasthttp.StatusBadGateway,
		},
		{
			// 502 would read as transient and retry the same key instead of rotating.
			"rate limit by type keeps its 429",
			`{"type":"error","error":{"type":"too_many_requests","code":"no_capacity","message":"capacity"}}`,
			fasthttp.StatusTooManyRequests,
		},
		{
			"quota by code keeps its 429",
			`{"type":"error","error":{"code":"insufficient_quota","message":"quota"}}`,
			fasthttp.StatusTooManyRequests,
		},
		{
			// 502 would retry a request that can never succeed.
			"context length is a caller fault",
			`{"type":"response.failed","response":{"error":{"code":"context_length_exceeded","message":"input is too large"}}}`,
			fasthttp.StatusBadRequest,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var response schemas.BifrostResponsesStreamResponse
			if err := schemas.Unmarshal([]byte(tc.wire), &response); err != nil {
				t.Fatalf("unmarshal stream error: %v", err)
			}
			got := responsesStreamError(&response)
			if got.StatusCode == nil {
				t.Fatalf("StatusCode is nil, want %d", tc.want)
			}
			if *got.StatusCode != tc.want {
				t.Fatalf("StatusCode = %d, want %d", *got.StatusCode, tc.want)
			}
			// The status is what makes this reach 5xx dashboards rather than 400.
			if got.EffectiveHTTPStatus() != tc.want {
				t.Fatalf("EffectiveHTTPStatus() = %d, want %d", got.EffectiveHTTPStatus(), tc.want)
			}
		})
	}
}

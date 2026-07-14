package openai

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

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

// TestStatusCodeForResponsesStreamErrorCode_CanonicalTypesCoverage is a
// regression test (found via greptile review): responsesStreamErrorCodeStatus
// previously only covered a handful of literal OpenAI codes, so a canonical
// schemas.ErrorType* value like context_length_exceeded or
// content_policy_violation fell through to the generic 500 fallback,
// misrepresenting a client-side error as a retryable server failure.
func TestStatusCodeForResponsesStreamErrorCode_CanonicalTypesCoverage(t *testing.T) {
	tests := []struct {
		code           string
		expectedStatus int
	}{
		{schemas.ErrorTypeInvalidRequest, fasthttp.StatusBadRequest},
		{schemas.ErrorTypeContextLengthExceeded, fasthttp.StatusBadRequest},
		{schemas.ErrorTypeContentPolicyViolation, fasthttp.StatusBadRequest},
		{schemas.ErrorTypeAuthentication, fasthttp.StatusUnauthorized},
		{schemas.ErrorTypePermissionDenied, fasthttp.StatusForbidden},
		{schemas.ErrorTypeNotFound, fasthttp.StatusNotFound},
		{schemas.ErrorTypeUnprocessableEntity, fasthttp.StatusUnprocessableEntity},
		{schemas.ErrorTypeRequestTimeout, fasthttp.StatusRequestTimeout},
		{schemas.ErrorTypeServiceUnavailable, fasthttp.StatusServiceUnavailable},
		{schemas.ErrorTypeBadGateway, fasthttp.StatusBadGateway},
	}
	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			code := tt.code
			status := StatusCodeForResponsesStreamErrorCode(&code)
			if status != tt.expectedStatus {
				t.Errorf("expected status %d for code %q, got %d (would have been misclassified as a retryable server error if still falling back to 500)", tt.expectedStatus, tt.code, status)
			}
		})
	}
}

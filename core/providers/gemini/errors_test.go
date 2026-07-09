package gemini

import (
	"testing"

	"github.com/valyala/fasthttp"
)

// TestParseGeminiError_SingleObjectPopulatesStatusType verifies the Gemini
// status (e.g. RESOURCE_EXHAUSTED) is surfaced on error.type when the body is a
// single {"error":{...}} object.
func TestParseGeminiError_SingleObjectPopulatesStatusType(t *testing.T) {
	var resp fasthttp.Response
	resp.SetStatusCode(fasthttp.StatusTooManyRequests)
	resp.SetBodyString(`{"error":{"code":429,"message":"Quota exceeded","status":"RESOURCE_EXHAUSTED"}}`)

	bifrostErr := parseGeminiError(&resp)

	if bifrostErr == nil || bifrostErr.Error == nil {
		t.Fatal("expected non-nil error response")
	}
	if bifrostErr.Error.Type == nil || *bifrostErr.Error.Type != "RESOURCE_EXHAUSTED" {
		t.Fatalf("expected error.type RESOURCE_EXHAUSTED, got %v", bifrostErr.Error.Type)
	}
}

// TestParseGeminiError_ArrayPopulatesStatusType verifies the status is surfaced
// on error.type when the body is an array of errors.
func TestParseGeminiError_ArrayPopulatesStatusType(t *testing.T) {
	var resp fasthttp.Response
	resp.SetStatusCode(fasthttp.StatusBadRequest)
	resp.SetBodyString(`[{"error":{"code":400,"message":"bad request","status":"INVALID_ARGUMENT"}}]`)

	bifrostErr := parseGeminiError(&resp)

	if bifrostErr == nil || bifrostErr.Error == nil {
		t.Fatal("expected non-nil error response")
	}
	if bifrostErr.Error.Type == nil || *bifrostErr.Error.Type != "INVALID_ARGUMENT" {
		t.Fatalf("expected error.type INVALID_ARGUMENT, got %v", bifrostErr.Error.Type)
	}
}

// TestParseGeminiError_RoundTripToGeminiError is the regression test for the
// broken passthrough: ToGeminiError reconstructs the status field from
// error.type, so the round trip must preserve the Gemini status rather than
// returning an empty status.
func TestParseGeminiError_RoundTripToGeminiError(t *testing.T) {
	var resp fasthttp.Response
	resp.SetStatusCode(fasthttp.StatusTooManyRequests)
	resp.SetBodyString(`{"error":{"code":429,"message":"Quota exceeded","status":"RESOURCE_EXHAUSTED"}}`)

	bifrostErr := parseGeminiError(&resp)
	geminiErr := ToGeminiError(bifrostErr)

	if geminiErr == nil || geminiErr.Error == nil {
		t.Fatal("expected non-nil gemini error")
	}
	if geminiErr.Error.Status != "RESOURCE_EXHAUSTED" {
		t.Fatalf("expected status RESOURCE_EXHAUSTED to survive round trip, got %q", geminiErr.Error.Status)
	}
}

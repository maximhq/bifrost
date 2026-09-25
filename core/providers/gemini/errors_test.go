package gemini

import (
	"strings"
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

// TestProcessGeminiStreamChunk_MidStreamError verifies that an error payload
// delivered inside an HTTP 200 stream body (e.g. Vertex aborting with a
// pretty-printed 429 on quota exhaustion) is surfaced as a typed error that
// preserves the upstream code, status, and message.
func TestProcessGeminiStreamChunk_MidStreamError(t *testing.T) {
	chunk := "{\n" +
		"  \"error\": {\n" +
		"    \"code\": 429,\n" +
		"    \"message\": \"Resource exhausted. Please try again later.\",\n" +
		"    \"status\": \"RESOURCE_EXHAUSTED\"\n" +
		"  }\n" +
		"}"

	resp, err := processGeminiStreamChunk([]byte(chunk))
	if resp != nil {
		t.Fatal("expected nil response for error chunk")
	}
	apiErr, ok := err.(*GeminiStreamAPIError)
	if !ok {
		t.Fatalf("expected *GeminiStreamAPIError, got %T: %v", err, err)
	}
	if apiErr.Err.Code != 429 {
		t.Errorf("expected code 429, got %d", apiErr.Err.Code)
	}
	if apiErr.Err.Status != "RESOURCE_EXHAUSTED" {
		t.Errorf("expected status RESOURCE_EXHAUSTED, got %q", apiErr.Err.Status)
	}
	if apiErr.Err.Message != "Resource exhausted. Please try again later." {
		t.Errorf("unexpected message: %q", apiErr.Err.Message)
	}
}

// TestParseGeminiError_RetryInfo verifies a google.rpc.RetryInfo detail becomes the
// retry hint for both body shapes, and wins over a Retry-After header.
func TestParseGeminiError_RetryInfo(t *testing.T) {
	body := `{"error":{"code":429,"message":"You exceeded your current quota.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaId":"GenerateRequestsPerDayPerProjectPerModel-FreeTier"}]},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"39.844676573s"}]}}`
	cases := []struct {
		name       string
		body       string
		retryAfter string
		want       int64
	}{
		{name: "object body", body: body, want: 39844},
		{name: "array body", body: "[" + body + "]", want: 39844},
		{name: "retry info beats the header", body: body, retryAfter: "5", want: 39844},
		{name: "header without retry info", body: `{"error":{"code":429,"message":"Quota exceeded","status":"RESOURCE_EXHAUSTED"}}`, retryAfter: "5", want: 5000},
		{name: "no hint", body: `{"error":{"code":400,"message":"bad request","status":"INVALID_ARGUMENT","details":[{"@type":"type.googleapis.com/google.rpc.BadRequest"}]}}`},
		// The RetryInfo path is clamped like the header path, which is the [1000, 300000]
		// range the OpenAPI schema declares for retry_after_ms.
		{name: "retry info below the floor clamps up to 1000", body: strings.Replace(body, "39.844676573s", "0.5s", 1), want: 1000},
		{name: "retry info above the ceiling clamps down to 300000", body: strings.Replace(body, "39.844676573s", "24h", 1), want: 300000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var resp fasthttp.Response
			resp.SetStatusCode(fasthttp.StatusTooManyRequests)
			if tc.retryAfter != "" {
				resp.Header.Set("Retry-After", tc.retryAfter)
			}
			resp.SetBodyString(tc.body)

			if got := parseGeminiError(&resp).ExtraFields.RetryAfter; got != tc.want {
				t.Errorf("expected RetryAfter %d, got %d", tc.want, got)
			}
		})
	}
}

// TestToGeminiStreamBifrostError_RetryInfo verifies a mid-stream error event keeps
// the RetryInfo delay it carries as the retry hint.
func TestToGeminiStreamBifrostError_RetryInfo(t *testing.T) {
	_, err := processGeminiStreamChunk([]byte(`{"error":{"code":429,"message":"Resource exhausted.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"39s"}]}}`))
	bifrostErr := toGeminiStreamBifrostError(err)
	if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != 429 {
		t.Fatalf("expected status 429, got %v", bifrostErr.StatusCode)
	}
	if bifrostErr.ExtraFields.RetryAfter != 39000 {
		t.Errorf("expected RetryAfter 39000, got %d", bifrostErr.ExtraFields.RetryAfter)
	}

	_, err = processGeminiStreamChunk([]byte(`{"error":{"code":429,"message":"Resource exhausted.","status":"RESOURCE_EXHAUSTED"}}`))
	if got := toGeminiStreamBifrostError(err).ExtraFields.RetryAfter; got != 0 {
		t.Errorf("expected no RetryAfter without RetryInfo, got %d", got)
	}
}

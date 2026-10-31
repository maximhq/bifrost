package elevenlabs

import (
	"testing"

	"github.com/valyala/fasthttp"
)

// A validation error is rebuilt from its details, and keeps the retry hint the shared
// handler read from the headers.
func TestParseElevenlabsError_ValidationKeepsRetryHint(t *testing.T) {
	var resp fasthttp.Response
	resp.SetStatusCode(fasthttp.StatusTooManyRequests)
	resp.Header.Set("Retry-After", "7")
	resp.SetBodyString(`{"detail":{"type":"rate_limited","loc":["body","text"],"message":"Too many concurrent requests"}}`)

	bifrostErr := parseElevenlabsError(&resp)

	if bifrostErr.Error == nil || bifrostErr.Error.Message != "Too many concurrent requests [body.text]" {
		t.Fatalf("unexpected error: %+v", bifrostErr.Error)
	}
	if bifrostErr.ExtraFields.RetryAfter != 7000 {
		t.Fatalf("RetryAfter = %d, want 7000", bifrostErr.ExtraFields.RetryAfter)
	}
}

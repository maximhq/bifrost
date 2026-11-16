package elevenlabs

import (
	"testing"

	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/valyala/fasthttp"
)

// A failure of the gateway is written as ElevenLabs writes its own: {"detail":{"status","message"}}.
func TestToElevenlabsError(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  *schemas.BifrostError
		want string
	}{
		{"governance decision", &schemas.BifrostError{Type: schemas.Ptr("model_blocked"), StatusCode: schemas.Ptr(403), Error: &schemas.ErrorField{Message: "model is not allowed"}}, `{"detail":{"status":"model_blocked","message":"model is not allowed"}}`},
		{"error type", &schemas.BifrostError{StatusCode: schemas.Ptr(400), Error: &schemas.ErrorField{Type: schemas.Ptr("invalid_request_error"), Message: "bad"}}, `{"detail":{"status":"invalid_request_error","message":"bad"}}`},
		{"error code", &schemas.BifrostError{Error: &schemas.ErrorField{Code: schemas.Ptr("some_code"), Message: "bad"}}, `{"detail":{"status":"some_code","message":"bad"}}`},
		{"unauthorized", &schemas.BifrostError{StatusCode: schemas.Ptr(401), Error: &schemas.ErrorField{Message: "no key"}}, `{"detail":{"status":"invalid_api_key","message":"no key"}}`},
		{"not found", &schemas.BifrostError{StatusCode: schemas.Ptr(404), Error: &schemas.ErrorField{Message: "no route"}}, `{"detail":{"status":"not_found","message":"no route"}}`},
		{"budget", &schemas.BifrostError{StatusCode: schemas.Ptr(402), Error: &schemas.ErrorField{Message: "spent"}}, `{"detail":{"status":"quota_exceeded","message":"spent"}}`},
		{"rate limit", &schemas.BifrostError{StatusCode: schemas.Ptr(429), Error: &schemas.ErrorField{Message: "slow"}}, `{"detail":{"status":"rate_limited","message":"slow"}}`},
		{"gateway fault", &schemas.BifrostError{IsBifrostError: true, Error: &schemas.ErrorField{Message: "oops"}}, `{"detail":{"status":"server_error","message":"oops"}}`},
		{"no error field", &schemas.BifrostError{StatusCode: schemas.Ptr(400)}, `{"detail":{"status":"invalid_request","message":""}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := sonic.Marshal(ToElevenlabsError(tc.err))
			assert.NoError(t, err)
			assert.JSONEq(t, tc.want, string(body))
		})
	}
	assert.Nil(t, ToElevenlabsError(nil))
}

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

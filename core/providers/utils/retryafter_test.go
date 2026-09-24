package utils

import (
	"net/http"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

func TestRetryAfterHint(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name    string
		headers map[string]string
		want    time.Duration
	}{
		{name: "no hint", headers: map[string]string{"x-request-id": "req_1"}},
		{name: "azure retry-after-ms", headers: map[string]string{"retry-after-ms": "2000"}, want: 2 * time.Second},
		{name: "retry-after seconds", headers: map[string]string{"Retry-After": "56"}, want: 56 * time.Second},
		{name: "retry-after fractional seconds", headers: map[string]string{"retry-after": "1.5"}, want: 1500 * time.Millisecond},
		{name: "retry-after http date", headers: map[string]string{"Retry-After": now.Add(90 * time.Second).Format(http.TimeFormat)}, want: 90 * time.Second},
		{name: "milliseconds beat seconds", headers: map[string]string{"retry-after-ms": "3000", "Retry-After": "60"}, want: 3 * time.Second},
		{name: "zero clamps to the floor", headers: map[string]string{"Retry-After": "0"}, want: time.Second},
		{name: "past date clamps to the floor", headers: map[string]string{"Retry-After": now.Add(-time.Minute).Format(http.TimeFormat)}, want: time.Second},
		{name: "a day clamps to the ceiling", headers: map[string]string{"Retry-After": "86400"}, want: 5 * time.Minute},
		{name: "unparsable retry-after gives no hint", headers: map[string]string{"Retry-After": "soon"}},
		{name: "nan gives no hint", headers: map[string]string{"Retry-After": "NaN"}},
		{name: "out-of-range seconds clamp to the ceiling", headers: map[string]string{"Retry-After": "10000000000"}, want: 5 * time.Minute},
		{name: "infinite seconds clamp to the ceiling", headers: map[string]string{"Retry-After": "inf"}, want: 5 * time.Minute},
		{name: "out-of-range milliseconds clamp to the ceiling", headers: map[string]string{"retry-after-ms": "1e300"}, want: 5 * time.Minute},
		{name: "negative infinity clamps to the floor", headers: map[string]string{"Retry-After": "-inf"}, want: time.Second},
		{name: "openai reset headers are not a hint", headers: map[string]string{
			"x-ratelimit-remaining-requests": "0", "x-ratelimit-reset-requests": "1s",
			"x-ratelimit-remaining-tokens": "0", "x-ratelimit-reset-tokens": "6.5s",
		}},
		{name: "anthropic reset headers are not a hint", headers: map[string]string{
			"anthropic-ratelimit-input-tokens-remaining": "0", "anthropic-ratelimit-input-tokens-reset": now.Add(45 * time.Second).Format(time.RFC3339),
		}},
		{name: "anthropic retry-after is the hint", headers: map[string]string{
			"retry-after":                          "12",
			"anthropic-ratelimit-tokens-remaining": "0", "anthropic-ratelimit-tokens-reset": now.Add(45 * time.Second).Format(time.RFC3339),
		}, want: 12 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := fasthttp.AcquireResponse()
			defer fasthttp.ReleaseResponse(resp)
			for k, v := range tc.headers {
				resp.Header.Set(k, v)
			}
			header := func(name string) string { return string(resp.Header.Peek(name)) }
			bifrostErr := &schemas.BifrostError{}
			if hint, ok := retryAfterHint(header, now); ok {
				SetRetryAfter(bifrostErr, hint)
			}
			if got := bifrostErr.ExtraFields.RetryAfter; got != tc.want.Milliseconds() {
				t.Errorf("RetryAfter = %d, want %d", got, tc.want.Milliseconds())
			}
		})
	}
}

// Every error the shared handler builds carries the hint, whatever shape the body took.
func TestHandleProviderAPIError_RetryAfter(t *testing.T) {
	bodies := map[string]string{
		"json":  `{"error":{"message":"Rate limit reached for gpt-4o","type":"requests","code":"rate_limit_exceeded"}}`,
		"empty": ``,
		"html":  `<html><body>Too Many Requests</body></html>`,
		"text":  `slow down`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			resp := fasthttp.AcquireResponse()
			defer fasthttp.ReleaseResponse(resp)
			resp.SetStatusCode(429)
			resp.Header.Set("Retry-After", "7")
			if name == "html" {
				resp.Header.Set("Content-Type", "text/html")
			}
			resp.SetBody([]byte(body))
			var target map[string]any
			bifrostErr := HandleProviderAPIError(resp, &target)
			if bifrostErr == nil || bifrostErr.ExtraFields.RetryAfter != 7000 {
				t.Fatalf("RetryAfter = %v, want 7000", bifrostErr)
			}
		})
	}

	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)
	resp.SetStatusCode(400)
	resp.SetBody([]byte(`{"error":{"message":"bad request"}}`))
	var target map[string]any
	if bifrostErr := HandleProviderAPIError(resp, &target); bifrostErr.ExtraFields.RetryAfter != 0 {
		t.Errorf("RetryAfter = %d without a hint, want 0", bifrostErr.ExtraFields.RetryAfter)
	}
}

// A hint an error parser already set from the error body is the provider's own answer for
// that error, so a header never replaces it.
func TestApplyRetryAfter_KeepsParserHint(t *testing.T) {
	var headers fasthttp.ResponseHeader
	headers.Set("Retry-After", "5")

	bifrostErr := &schemas.BifrostError{ExtraFields: schemas.BifrostErrorExtraFields{RetryAfter: 39000}}
	ApplyRetryAfter(bifrostErr, &headers)
	if bifrostErr.ExtraFields.RetryAfter != 39000 {
		t.Errorf("RetryAfter = %d, want the parser's 39000", bifrostErr.ExtraFields.RetryAfter)
	}

	fresh := &schemas.BifrostError{}
	ApplyRetryAfter(fresh, &headers)
	if fresh.ExtraFields.RetryAfter != 5000 {
		t.Errorf("RetryAfter = %d, want 5000", fresh.ExtraFields.RetryAfter)
	}
}

// The hint is part of the error's JSON so an API caller can honour it too.
func TestRetryAfterSerialization(t *testing.T) {
	b, err := sonic.Marshal(schemas.BifrostErrorExtraFields{RetryAfter: 1500})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := sonic.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["retry_after_ms"] != float64(1500) {
		t.Errorf("retry_after_ms = %v in %s", fields["retry_after_ms"], b)
	}
	b, _ = sonic.Marshal(schemas.BifrostErrorExtraFields{})
	empty := map[string]any{}
	if err := sonic.Unmarshal(b, &empty); err != nil {
		t.Fatal(err)
	}
	if _, present := empty["retry_after_ms"]; present {
		t.Errorf("retry_after_ms present without a hint: %s", b)
	}
}

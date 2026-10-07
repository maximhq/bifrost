package utils

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// A provider's retry hint is clamped to this range, so a malformed or extreme value can
// neither invite an immediate retry storm nor hold a key back for hours.
const (
	minRetryAfter = time.Second
	maxRetryAfter = 5 * time.Minute
)

// ApplyRetryAfter stamps the retry hint from the response headers onto the error's
// ExtraFields.RetryAfter, in milliseconds. It leaves the error alone when the headers carry
// no hint, and when a provider's error parser already set one from the error body, which
// is the provider's own answer for that error. Only explicit hints count, in this order:
//
//   - retry-after-ms: milliseconds (Azure OpenAI);
//   - Retry-After: seconds or an HTTP date (OpenAI, Azure OpenAI, Anthropic, Groq, OpenRouter).
//
// Rate-limit reset headers (x-ratelimit-reset-*, anthropic-ratelimit-*-reset) are not read:
// they say when each limit refills, not how long this request must wait, and a request
// that needs more than a limit has left is refused while that limit still reads non-zero.
func ApplyRetryAfter(bifrostErr *schemas.BifrostError, headers *fasthttp.ResponseHeader) {
	if bifrostErr == nil || headers == nil || bifrostErr.ExtraFields.RetryAfter != 0 {
		return
	}
	header := func(name string) string {
		return strings.TrimSpace(string(headers.Peek(name)))
	}
	if hint, ok := retryAfterHint(header, time.Now()); ok {
		SetRetryAfter(bifrostErr, hint)
	}
}

// SetRetryAfter records a provider's retry hint on the error, in milliseconds, clamped to
// between minRetryAfter and maxRetryAfter.
func SetRetryAfter(bifrostErr *schemas.BifrostError, hint time.Duration) {
	if bifrostErr == nil {
		return
	}
	bifrostErr.ExtraFields.RetryAfter = min(max(hint, minRetryAfter), maxRetryAfter).Milliseconds()
}

// retryAfterHint reads a retry hint from the response headers. now anchors an HTTP-date
// Retry-After.
func retryAfterHint(header func(string) string, now time.Time) (time.Duration, bool) {
	if v := header("retry-after-ms"); v != "" {
		if ms, err := strconv.ParseFloat(v, 64); err == nil && !math.IsNaN(ms) {
			return scaledDuration(ms, time.Millisecond), true
		}
	}
	if v := header("Retry-After"); v != "" {
		if seconds, err := strconv.ParseFloat(v, 64); err == nil && !math.IsNaN(seconds) {
			return scaledDuration(seconds, time.Second), true
		}
		if at, err := http.ParseTime(v); err == nil {
			return at.Sub(now), true
		}
	}
	return 0, false
}

// scaledDuration converts a count of units to a duration, bounded to the clamp range first
// so a huge or infinite header value cannot overflow time.Duration.
func scaledDuration(count float64, unit time.Duration) time.Duration {
	bound := float64(maxRetryAfter / unit)
	return time.Duration(min(max(count, 0), bound) * float64(unit))
}

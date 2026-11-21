package bifrost

import (
	"bytes"
	"net/http"
	"strings"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// passthrough429RetryError translates only a retryable upstream 429 into the
// existing Bifrost retry loop. The caller must invoke this only while there
// is retry budget; on the final attempt it must forward the original response.
func passthrough429RetryError(resp *schemas.BifrostPassthroughResponse) *schemas.BifrostError {
	if resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		return nil
	}
	for header, value := range resp.Headers {
		if strings.EqualFold(header, "x-should-retry") &&
			strings.EqualFold(strings.TrimSpace(value), "false") {
			return nil
		}
	}
	// OpenCodex may synthesize 429 for an ambiguous, possibly processed POST.
	// Do not override its no-replay gate even if a future intermediary drops
	// the explicit header. Permanent quota depletion cannot recover by waiting.
	if bytes.Contains(resp.Body, []byte("upstream_reset_replay_refused")) ||
		bytes.Contains(resp.Body, []byte("insufficient_quota")) {
		return nil
	}
	status := http.StatusTooManyRequests
	retryErr := &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     &status,
		Error:          &schemas.ErrorField{Message: "retryable upstream HTTP 429 (passthrough)"},
	}
	// Reuse the existing upstream hint parser; headers are already filtered for
	// safe passthrough forwarding, so this does not expose credentials.
	upstreamHeaders := make(http.Header, len(resp.Headers))
	for key, value := range resp.Headers {
		upstreamHeaders.Set(key, value)
	}
	providerUtils.ApplyRetryAfterHTTP(retryErr, upstreamHeaders)
	return retryErr
}

// The provider hint is a minimum pause for safe passthrough retries. This
// leaves the core jittered exponential delay in place when it is greater.
func passthroughRetryDelay(backoff time.Duration, previous *schemas.BifrostError) time.Duration {
	if previous == nil || previous.ExtraFields.RetryAfter <= 0 {
		return backoff
	}
	return max(backoff, time.Duration(previous.ExtraFields.RetryAfter)*time.Millisecond)
}

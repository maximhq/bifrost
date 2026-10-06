package antigravity

import (
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

const (
	googleRetryInfoType  = "type.googleapis.com/google.rpc.RetryInfo"
	maxUpstreamMsgLength = 500
)

// upstreamError is a google.rpc.Status as Cloud Code Assist returns it.
type upstreamError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
	Details []struct {
		Type       string `json:"@type"`
		Reason     string `json:"reason"`
		RetryDelay string `json:"retryDelay"`
	} `json:"details"`
}

type upstreamErrorEnvelope struct {
	Error *upstreamError `json:"error"`
}

// rateLimitPhrases mark a 429 as a short-window limit even when it also mentions quota.
var rateLimitPhrases = []string{
	"per minute", "per-minute", "per min", "rpm", "requests per minute", "too many requests",
	"rate limit", "retry after", "retry-after", "concurrent request limit",
}

// quotaPhrases mark a 429 as an exhausted subscription quota.
var quotaPhrases = []string{
	"quotafailure", "quota exceeded", "exceeded your current quota", "billing", "individual quota reached",
	"quota reached", "enable overages", "exhausted your capacity", "daily limit reached", "weekly limit reached",
}

// parseAntigravityError converts a non-200 Cloud Code Assist response into a BifrostError.
// The Retry-After header is applied by HandleProviderAPIError; a RetryInfo detail in the
// body overrides it.
func parseAntigravityError(resp *fasthttp.Response) *schemas.BifrostError {
	var envelope upstreamErrorEnvelope
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &envelope)
	apiErr := envelope.Error
	if apiErr == nil {
		var list []upstreamErrorEnvelope
		listErr := providerUtils.HandleProviderAPIError(resp, &list)
		for _, e := range list {
			if e.Error != nil {
				apiErr = e.Error
				bifrostErr = listErr
				break
			}
		}
	}
	return normalizeUpstreamError(bifrostErr, resp.StatusCode(), apiErr)
}

// normalizeUpstreamError fills in status, message, type and retry hint from the upstream
// status and google.rpc.Status, using Antigravity's error vocabulary.
func normalizeUpstreamError(bifrostErr *schemas.BifrostError, status int, apiErr *upstreamError) *schemas.BifrostError {
	if bifrostErr == nil {
		bifrostErr = &schemas.BifrostError{}
	}
	bifrostErr.IsBifrostError = false
	if bifrostErr.Error == nil {
		bifrostErr.Error = &schemas.ErrorField{}
	}
	if status == 0 && apiErr != nil {
		status = apiErr.Code
	}
	if status == 0 {
		status = http.StatusBadGateway
	}
	bifrostErr.StatusCode = schemas.Ptr(status)

	message := bifrostErr.Error.Message
	upstreamStatus := ""
	reasons := map[string]bool{}
	if apiErr != nil {
		if apiErr.Message != "" {
			message = apiErr.Message
		}
		upstreamStatus = apiErr.Status
		if apiErr.Code != 0 {
			bifrostErr.Error.Code = schemas.Ptr(strconv.Itoa(apiErr.Code))
		}
		if upstreamStatus != "" {
			bifrostErr.Error.Type = schemas.Ptr(upstreamStatus)
		}
		for _, d := range apiErr.Details {
			if d.Reason != "" {
				reasons[d.Reason] = true
			}
			if d.Type == googleRetryInfoType {
				if delay, err := time.ParseDuration(d.RetryDelay); err == nil {
					providerUtils.SetRetryAfter(bifrostErr, delay)
				}
			}
		}
	}
	message = truncate(strings.TrimSpace(message), maxUpstreamMsgLength)
	lower := strings.ToLower(message)

	var prefix string
	switch {
	case status == http.StatusTooManyRequests:
		if isQuotaExhausted(upstreamStatus, lower) {
			prefix = "Antigravity quota exhausted"
			// insufficient_quota makes the retry loop treat the key as spent for this
			// request instead of backing off and retrying it.
			bifrostErr.Error.Type = schemas.Ptr("insufficient_quota")
		} else {
			prefix = "Antigravity rate limit exceeded"
		}
	case status == http.StatusForbidden && reasons["VALIDATION_REQUIRED"]:
		prefix = "Antigravity account validation required (VALIDATION_REQUIRED)"
	case status == http.StatusForbidden && strings.Contains(lower, "verify your account"):
		prefix = "Antigravity account verification required"
	case status == http.StatusForbidden && strings.Contains(lower, "location") && strings.Contains(lower, "not supported"):
		prefix = "Antigravity location not supported"
	case status == http.StatusForbidden:
		prefix = "Antigravity access denied (PERMISSION_DENIED)"
	case status == http.StatusUnauthorized:
		prefix = "Antigravity authentication failed"
	case status == http.StatusServiceUnavailable || status == 529:
		prefix = "Antigravity server overloaded"
	case status == http.StatusBadRequest:
		prefix = "Antigravity invalid request"
	default:
		prefix = "Antigravity upstream error"
	}
	if message != "" {
		bifrostErr.Error.Message = prefix + ": " + message
	} else {
		bifrostErr.Error.Message = prefix
	}
	return bifrostErr
}

// isQuotaExhausted separates an exhausted subscription quota from a short-window rate
// limit; both arrive as 429 RESOURCE_EXHAUSTED.
func isQuotaExhausted(upstreamStatus, lowerMessage string) bool {
	if upstreamStatus != "" && upstreamStatus != "RESOURCE_EXHAUSTED" {
		return false
	}
	for _, phrase := range rateLimitPhrases {
		if strings.Contains(lowerMessage, phrase) {
			return false
		}
	}
	for _, phrase := range quotaPhrases {
		if strings.Contains(lowerMessage, phrase) {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

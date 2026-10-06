package kiro

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// errorDetailKeys are the JSON body fields Kiro and the AWS front door put error details in.
var errorDetailKeys = []string{"__type", "code", "error", "name", "reason", "message", "Message", "errorMessage"}

// suspensionWords mark a 403 that quarantines the account rather than refusing one request.
var suspensionWords = []string{"temporarily suspended", "temporarily is suspended", "locked your account", "locked it as a"}

// kiroFailure is the classification of one failed Kiro call.
type kiroFailure struct {
	status    int
	errorType string
	code      string
	message   string
}

// refusalKind is an account-level refusal class.
type refusalKind string

const (
	refusalRate         refusalKind = "rate"
	refusalMonthlyQuota refusalKind = "monthly_quota"
	refusalSuspended    refusalKind = "suspended"
	refusalOther        refusalKind = "other"
)

// classifyKiroRefusal recognises the account-scoped refusals: an exhausted monthly request
// allowance, a suspended account, and a plain rate limit. Unknown bodies never convict the
// account.
func classifyKiroRefusal(status int, body []byte) refusalKind {
	var parsed struct {
		Reason  string `json:"reason"`
		Message string `json:"message"`
	}
	_ = sonic.Unmarshal(body, &parsed)
	if (status == http.StatusBadRequest || status == http.StatusTooManyRequests) && parsed.Reason == "MONTHLY_REQUEST_COUNT" {
		return refusalMonthlyQuota
	}
	if status == http.StatusForbidden {
		lower := strings.ToLower(parsed.Message)
		if parsed.Reason == "TEMPORARILY_SUSPENDED" || containsAny(lower, suspensionWords) {
			return refusalSuspended
		}
	}
	if status == http.StatusTooManyRequests {
		return refusalRate
	}
	return refusalOther
}

// classifyKiroFailure maps an HTTP failure (status > 0) or an in-stream exception frame
// (status 0, exceptionType from the frame header) to a status, error type, code and a bounded
// operator-facing message, in the same precedence OpenCodex uses.
func classifyKiroFailure(status int, exceptionType string, body []byte) kiroFailure {
	details := payloadDetails(body)
	parts := make([]string, 0, len(details)+1)
	if exceptionType != "" {
		parts = append(parts, exceptionType)
	}
	parts = append(parts, details...)
	detail := strings.Join(parts, ": ")
	if detail == "" && status > 0 {
		detail = fmt.Sprintf("HTTP %d", status)
	}
	detail = truncateText(sanitizeErrorText(detail), 500)
	evidence := strings.ToLower(exceptionType + " " + strings.Join(details, " "))

	message := func(prefix string) string {
		if detail == "" {
			return prefix
		}
		return prefix + ": " + detail
	}

	switch {
	case strings.Contains(evidence, "content_length_exceeds_threshold") || strings.Contains(evidence, "content length exceeds"):
		return kiroFailure{http.StatusBadRequest, "invalid_request_error", "context_length_exceeded",
			"Kiro rejected the request because the conversation exceeds the model's context window. Compact or reduce the history, or start a new session."}
	case strings.Contains(evidence, "profilearn") && strings.Contains(evidence, "required"):
		return kiroFailure{http.StatusBadRequest, "invalid_request_error", "kiro_profile_required",
			"kiro_profile_required: Kiro requires a CodeWhisperer profileArn for this account and model; re-login so the profile ARN is captured in the key credential"}
	case strings.Contains(evidence, "insufficient_quota") || strings.Contains(evidence, "quota exhausted") || strings.Contains(evidence, "quota exceeded"):
		return kiroFailure{http.StatusTooManyRequests, "insufficient_quota", "insufficient_quota", message("Kiro quota exhausted")}
	case status == http.StatusTooManyRequests || strings.Contains(evidence, "throttlingexception") ||
		strings.Contains(evidence, "too many requests") || strings.Contains(evidence, "rate limit"):
		return kiroFailure{http.StatusTooManyRequests, "rate_limit_error", "rate_limit_exceeded", message("Kiro rate limit exceeded")}
	case status == http.StatusUnauthorized || status == http.StatusForbidden || containsAny(evidence, authFailureMarkers):
		if status == http.StatusForbidden {
			return kiroFailure{http.StatusForbidden, "permission_denied", "permission_denied", message("Kiro authentication failed")}
		}
		return kiroFailure{http.StatusUnauthorized, "authentication_error", "invalid_api_key", message("Kiro authentication failed")}
	case status == http.StatusBadRequest || containsAny(evidence, invalidRequestMarkers):
		return kiroFailure{http.StatusBadRequest, "invalid_request_error", "invalid_request_error", message("Kiro invalid request")}
	case status == http.StatusServiceUnavailable || containsAny(evidence, overloadedMarkers):
		return kiroFailure{http.StatusServiceUnavailable, "server_error", "server_is_overloaded", message("Kiro server overloaded")}
	default:
		final := http.StatusBadGateway
		if status >= 500 {
			final = status
		}
		text := "Kiro upstream service unavailable"
		if final == http.StatusGatewayTimeout {
			text = "Kiro upstream gateway timeout"
		}
		if status > 0 && status < 500 {
			text = message("Kiro upstream error")
		}
		return kiroFailure{final, "server_error", "upstream_server_error", text}
	}
}

var authFailureMarkers = []string{"accessdenied", "unauthorized", "unrecognizedclient", "expiredtoken", "expired token", "invalid token", "authentication"}

var invalidRequestMarkers = []string{"validationexception", "invalid request", "model unavailable", "model not found", "unsupported model", "profile arn", "malformed"}

var overloadedMarkers = []string{"overloaded", "server is busy", "temporarily unavailable"}

// endpointFallbackMarkers in a 400/403 body mean the canonical host does not serve this operation,
// so the legacy CodeWhisperer host is tried.
var endpointFallbackMarkers = []string{"unknownoperation", "unknown operation", "invalidsignature", "invalid signature", "endpoint not found", "unsupported endpoint"}

// expiredTokenMarkers on a 403 mean the access token went stale and one refresh is worth it.
var expiredTokenMarkers = []string{"expiredtoken", "expired token", "is expired", "has expired", "invalid token", "invalid bearer token"}

// newHTTPError builds the BifrostError for a non-200 Kiro runtime response, layering the
// account-level refusal classes over the generic classification. ExtraFields.RetryAfter is set
// only from an upstream Retry-After header: key rotation treats it as the provider's own hint,
// so a made-up default would override the operator's configured cooldown.
func newHTTPError(status int, body []byte, header *fasthttp.ResponseHeader) *schemas.BifrostError {
	failure := classifyKiroFailure(status, "", body)
	switch classifyKiroRefusal(status, body) {
	case refusalMonthlyQuota:
		failure = kiroFailure{http.StatusTooManyRequests, "insufficient_quota", "monthly_request_count",
			"Kiro monthly request allowance exhausted for this account (MONTHLY_REQUEST_COUNT)"}
	case refusalSuspended:
		failure = kiroFailure{http.StatusForbidden, "account_suspended", "account_suspended",
			"Kiro account is temporarily suspended (TEMPORARILY_SUSPENDED); log in to Kiro to resolve it"}
	}
	bErr := newKiroError(failure.status, failure.errorType, failure.code, failure.message)
	if header != nil {
		providerUtils.ApplyRetryAfter(bErr, header)
	}
	return bErr
}

// newStreamExceptionError builds the BifrostError for an exception/error frame or an error
// event inside the event stream. Exception frames carry no Retry-After, so no hint is set.
func newStreamExceptionError(exceptionType string, payload []byte) *schemas.BifrostError {
	failure := classifyKiroFailure(0, exceptionType, payload)
	return newKiroError(failure.status, failure.errorType, failure.code, failure.message)
}

// newKiroError builds an upstream-attributed error (IsBifrostError false) so the retry and key
// rotation logic classifies it from status, type and code.
func newKiroError(status int, errorType, code, message string) *schemas.BifrostError {
	return &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     schemas.Ptr(status),
		Type:           schemas.Ptr(errorType),
		Error: &schemas.ErrorField{
			Message: message,
			Type:    schemas.Ptr(errorType),
			Code:    schemas.Ptr(code),
		},
	}
}

// shouldRefreshAfter reports whether a runtime rejection is worth one forced token refresh: any
// 401, and a 403 whose body says the token itself is stale (not a suspension or a permission
// refusal).
func shouldRefreshAfter(status int, body []byte) bool {
	if status == http.StatusUnauthorized {
		return true
	}
	if status != http.StatusForbidden || classifyKiroRefusal(status, body) == refusalSuspended {
		return false
	}
	return containsAny(strings.ToLower(strings.Join(payloadDetails(body), " ")), expiredTokenMarkers)
}

// shouldFallbackEndpoint reports whether a canonical-host response warrants one retry on the
// legacy CodeWhisperer host.
func shouldFallbackEndpoint(status int, body []byte) bool {
	switch status {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout,
		http.StatusNotFound, http.StatusMethodNotAllowed:
		return true
	case http.StatusBadRequest, http.StatusForbidden:
		return containsAny(strings.ToLower(string(body)), endpointFallbackMarkers)
	}
	return false
}

// payloadDetails extracts the error detail strings from a JSON or plain-text error body.
func payloadDetails(body []byte) []string {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return nil
	}
	if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		return []string{truncateText(trimmed, 500)}
	}
	var parsed any
	if err := sonic.UnmarshalString(trimmed, &parsed); err != nil {
		return nil
	}
	switch v := parsed.(type) {
	case map[string]any:
		out := make([]string, 0, 2)
		for _, key := range errorDetailKeys {
			if s, ok := v[key].(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	case string:
		if s := strings.TrimSpace(v); s != "" {
			return []string{s}
		}
	}
	return nil
}

// sanitizeErrorText removes control characters so an upstream body cannot inject log lines or
// terminal escapes.
func sanitizeErrorText(s string) string {
	return strings.Map(func(r rune) rune {
		if isControl(r) {
			return ' '
		}
		return r
	}, s)
}

func containsAny(s string, markers []string) bool {
	for _, marker := range markers {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

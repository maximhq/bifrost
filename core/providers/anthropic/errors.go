package anthropic

import (
	"fmt"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// anthropicToOpenAICanonicalType maps Anthropic's full official error.type
// enum (schema-confirmed via BetaErrorType in Anthropic's published OpenAPI
// spec) onto OpenAI's canonical vocabulary. This is Stage 1 (normalize-to-OpenAI) of the two-stage error
// normalization design: every provider's raw error gets mapped onto OpenAI's
// vocabulary here, regardless of which route the client is using.
//
// billing_error and overloaded_error are notable: billing_error is Anthropic's
// distinct budget/quota signal (maps to insufficient_quota, not
// rate_limit_exceeded); overloaded_error (HTTP 529) is explicitly NOT a rate
// limit condition (Anthropic's capacity, not the caller's usage) and maps to
// the canonical service_unavailable *type*.
//
// overloaded_error's statusCode is deliberately kept at Anthropic's real
// native 529, NOT canonicalized to 503 like the type is. anthropic-python's
// SDK dispatches exceptions purely by HTTP status code (_client.py's
// _make_status_error: 529 -> OverloadedError, >=500 -> generic
// InternalServerError) — collapsing 529 into 503 here would make the client
// receive HTTP 503 with body type="overloaded_error", which the real SDK
// would misclassify as InternalServerError despite the body being correct
// (found via a from-scratch SDK-dispatch verification pass against the
// installed anthropic-python source). 529 is not a
// standard 5xx retry status, so it's deliberately NOT added to
// core/utils.go's transientServerStatusCodes globally (that would affect
// every provider); core/bifrost.go's retry loop instead special-cases
// providerKey==Anthropic && StatusCode==529 as transient, scoped to
// Anthropic only.
//
// billing_error is deliberately mapped to StatusCode 402, NOT 429, despite
// insufficient_quota's canonical status being 429 elsewhere (see error.yaml).
// core/utils.go's perKeyFailureStatusCodes classifies 401/402/403 as PERMANENT
// per-key failures (deadKeyIDs, never retried again this request) and 429 as
// TRANSIENT (may retry once other keys are exhausted, since a rate-limited key
// can recover quota over time). billing_error/insufficient_quota is a
// deterministic account-level exhaustion — retrying can never succeed — so
// 429 here would let core/bifrost.go's retry engine misclassify it as
// transient and waste an attempt or leave a dead key eligible for reuse this
// request. Mirrors the identical, deliberate 402 in
// plugins/governance/main.go's DecisionBudgetExceeded case for the same
// canonical Error.Type — found via a follow-up review pass after the
// governance instance of this exact conflict class was fixed via codex review.
// AnthropicOverloadedStatusCode is Anthropic's real (non-standard) HTTP
// status for overloaded_error. fasthttp has no named constant for it since
// it isn't a registered IANA status code.
const AnthropicOverloadedStatusCode = 529

var anthropicToOpenAICanonicalType = map[string]struct {
	canonicalType string
	statusCode    int
}{
	"invalid_request_error": {schemas.ErrorTypeInvalidRequest, fasthttp.StatusBadRequest},
	"authentication_error":  {schemas.ErrorTypeAuthentication, fasthttp.StatusUnauthorized},
	"permission_error":      {schemas.ErrorTypePermissionDenied, fasthttp.StatusForbidden},
	"not_found_error":       {schemas.ErrorTypeNotFound, fasthttp.StatusNotFound},
	"rate_limit_error":      {schemas.ErrorTypeRateLimitExceeded, fasthttp.StatusTooManyRequests},
	"billing_error":         {schemas.ErrorTypeInsufficientQuota, fasthttp.StatusPaymentRequired},
	"overloaded_error":      {schemas.ErrorTypeServiceUnavailable, AnthropicOverloadedStatusCode},
	"timeout_error":         {schemas.ErrorTypeRequestTimeout, fasthttp.StatusGatewayTimeout},
	"api_error":             {schemas.ErrorTypeServerError, fasthttp.StatusInternalServerError},
}

// normalizeAnthropicErrorType returns the OpenAI-canonical error type and
// status code for a raw Anthropic error.type value, plus whether the type was
// actually recognized. Callers with an already-known-correct HTTP status
// (e.g. parseAnthropicError, which gets the real status from the HTTP
// response before this runs) MUST check `recognized` and keep their own
// status when false — schema drift / a future Anthropic error type not yet in
// anthropicToOpenAICanonicalType should never silently downgrade a real,
// specific status (e.g. 403) to a generic 500. Callers with no real status of
// their own to preserve (mid-stream SSE errors, which carry no HTTP status at
// all) can ignore `recognized` and use the (ErrorTypeServerError, 500)
// fallback unconditionally — found via codex review, see chat.go/responses.go
// call sites for the mid-stream case.
func normalizeAnthropicErrorType(anthropicType string) (canonicalType string, statusCode int, recognized bool) {
	if mapped, ok := anthropicToOpenAICanonicalType[anthropicType]; ok {
		return mapped.canonicalType, mapped.statusCode, true
	}
	return schemas.ErrorTypeServerError, fasthttp.StatusInternalServerError, false
}

// openAIToAnthropicType maps OpenAI-canonical error types (the vocabulary
// every BifrostError.Error.Type carries after Stage 1, regardless of which
// provider actually raised it) onto Anthropic's own official error.type enum
// — Stage 2 (translate-from-OpenAI) of the two-stage design, for the
// /anthropic route. This is the inverse of anthropicToOpenAICanonicalType for
// entries sourced from Anthropic itself, plus best-fit mappings for canonical
// keys that have no distinct Anthropic-native equivalent (Anthropic collapses
// several OpenAI-distinguished conditions into its api_error / invalid_request_error
// buckets).
//
// Only the .Type identifier is translated here — .Message passes through
// unchanged regardless of route, by design (translating prose to sound native
// to the target vendor was explicitly decided against: low value, high risk
// of subtly wrong wording, and clients branch on type/code, not message text).
var openAIToAnthropicType = map[string]string{
	schemas.ErrorTypeRateLimitExceeded:      "rate_limit_error",
	schemas.ErrorTypeInsufficientQuota:      "billing_error", // Anthropic HAS a distinct budget signal — do not collapse into rate_limit_error
	schemas.ErrorTypeInvalidRequest:         "invalid_request_error",
	schemas.ErrorTypeAuthentication:         "authentication_error",
	schemas.ErrorTypePermissionDenied:       "permission_error",
	schemas.ErrorTypeNotFound:               "not_found_error",
	schemas.ErrorTypeContextLengthExceeded:  "invalid_request_error", // subtype of invalid_request_error, no distinct Anthropic type
	schemas.ErrorTypeContentPolicyViolation: "invalid_request_error",
	schemas.ErrorTypeUnprocessableEntity:    "invalid_request_error", // Anthropic has no distinct 422 type
	schemas.ErrorTypeRequestTimeout:         "timeout_error",
	schemas.ErrorTypeServerError:            "api_error",
	schemas.ErrorTypeServiceUnavailable:     "overloaded_error",
	schemas.ErrorTypeBadGateway:             "api_error",        // Bifrost-internal, no Anthropic-native equivalent
	schemas.ErrorTypeAPIConnection:          "api_error",        // Bifrost-internal
	schemas.ErrorTypeResponseValidation:     "api_error",        // Bifrost-internal
	schemas.RequestCancelled:                "api_error",        // Anthropic has no cancelled type
	schemas.ErrorTypeGovernanceBlocked:      "permission_error", // Bifrost-internal, closest Anthropic bucket
}

// ToAnthropicChatCompletionError converts a BifrostError to AnthropicMessageError
func ToAnthropicChatCompletionError(bifrostErr *schemas.BifrostError) *AnthropicMessageError {
	if bifrostErr == nil {
		return nil
	}

	// Translate the canonical OpenAI type into Anthropic's own vocabulary
	// (Stage 2). Falls back to Anthropic's generic "api_error" bucket for
	// canonical keys with no explicit mapping, matching Anthropic's own
	// fallback convention.
	errorType := "api_error"
	message := ""
	if bifrostErr.Error != nil {
		if bifrostErr.Error.Type != nil && *bifrostErr.Error.Type != "" {
			// Unmapped canonical types fall back to "api_error" (already the
			// default above) rather than passing the raw string through —
			// leaking an unrecognized/foreign vocabulary string into
			// Anthropic's envelope would defeat the purpose of Stage 2.
			if translated, ok := openAIToAnthropicType[*bifrostErr.Error.Type]; ok {
				errorType = translated
			}
		}
		message = bifrostErr.Error.Message
	}

	// Handle nested error fields with nil checks
	errorStruct := AnthropicMessageErrorStruct{
		Type:    errorType,
		Message: message,
	}

	return &AnthropicMessageError{
		Type:  "error", // always "error" for Anthropic
		Error: errorStruct,
	}
}

// ToAnthropicResponsesStreamError converts a BifrostError to Anthropic responses streaming error in SSE format
func ToAnthropicResponsesStreamError(bifrostErr *schemas.BifrostError) string {
	if bifrostErr == nil {
		return ""
	}

	anthropicErr := ToAnthropicChatCompletionError(bifrostErr)

	// Marshal to JSON
	jsonData, err := providerUtils.MarshalSorted(anthropicErr)
	if err != nil {
		return ""
	}

	// Format as Anthropic SSE error event
	return fmt.Sprintf("event: error\ndata: %s\n\n", jsonData)
}

func parseAnthropicError(resp *fasthttp.Response) *schemas.BifrostError {
	var errorResp AnthropicError
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)
	if errorResp.Error != nil {
		if bifrostErr.Error == nil {
			bifrostErr.Error = &schemas.ErrorField{}
		}
		bifrostErr.Error.Message = errorResp.Error.Message
		// Stage 1 (normalize-to-OpenAI): translate Anthropic's raw error.type
		// into OpenAI-canonical vocabulary + status, regardless of what raw
		// HTTP status Anthropic actually sent (e.g. overloaded_error is
		// documented at 529, which HandleProviderAPIError would have copied
		// verbatim into bifrostErr.StatusCode above — this corrects it to the
		// canonical service_unavailable/503).
		//
		// Only overwrite StatusCode when the type was actually recognized.
		// HandleProviderAPIError already set bifrostErr.StatusCode from the
		// real HTTP response above — for an unrecognized error.type (schema
		// drift, a future Anthropic error type), normalizeAnthropicErrorType's
		// generic 500 fallback must NOT clobber that already-correct, more
		// specific status (e.g. a real 403), or core/utils.go's
		// transientServerStatusCodes would misclassify a permanent failure as
		// retryable. Found via codex review.
		canonicalType, statusCode, recognized := normalizeAnthropicErrorType(errorResp.Error.Type)
		bifrostErr.Error.Type = new(canonicalType)
		if recognized {
			bifrostErr.StatusCode = new(statusCode)
		}
	}
	return bifrostErr
}

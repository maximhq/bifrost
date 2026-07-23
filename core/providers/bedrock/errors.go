package bedrock

import (
	"net/http"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// bedrockExceptionToOpenAICanonicalType maps AWS Bedrock's named exceptions
// (schema-confirmed via docs.aws.amazon.com/bedrock/latest/APIReference —
// InvokeModel-specific errors + CommonErrors) onto OpenAI's canonical
// vocabulary. This is Stage 1 (normalize-to-OpenAI) of the two-stage error
// normalization design, mirroring anthropic/errors.go's
// anthropicToOpenAICanonicalType.
//
// ThrottlingException vs ServiceQuotaExceededException is Bedrock's cleanest
// rate-limit/quota split among all researched providers — two genuinely
// distinct named exceptions, unlike Gemini (single RESOURCE_EXHAUSTED for
// both) or the ambiguous OpenAI "type" field.
//
// ServiceQuotaExceededException is deliberately mapped to StatusCode 402, NOT
// 429, for the same reason as Anthropic's billing_error and governance's
// DecisionBudgetExceeded: core/utils.go's perKeyFailureStatusCodes classifies
// 401/402/403 as PERMANENT per-key failures and 429 as TRANSIENT — a
// deterministic account-level quota exhaustion must be 402 or Bifrost's own
// retry engine will misclassify it as recoverable-by-retry.
//
// AWS's own docs are internally inconsistent about ThrottlingException's
// status (429 on the InvokeModel-specific page, 400 on the CommonErrors
// page) — this table normalizes it to the canonical 429 regardless of which
// raw status Bedrock actually sent, since the exception NAME is the reliable
// signal, not AWS's own documented status.
var bedrockExceptionToOpenAICanonicalType = map[string]struct {
	canonicalType string
	statusCode    int
}{
	"ThrottlingException":           {schemas.ErrorTypeRateLimitExceeded, fasthttp.StatusTooManyRequests},
	"ModelNotReadyException":        {schemas.ErrorTypeRateLimitExceeded, fasthttp.StatusTooManyRequests}, // SDK auto-retries this; treat as transient rate condition
	"ServiceQuotaExceededException": {schemas.ErrorTypeInsufficientQuota, fasthttp.StatusPaymentRequired},
	"ValidationException":           {schemas.ErrorTypeInvalidRequest, fasthttp.StatusBadRequest},
	"ValidationError":               {schemas.ErrorTypeInvalidRequest, fasthttp.StatusBadRequest},
	"MalformedHttpRequestException": {schemas.ErrorTypeInvalidRequest, fasthttp.StatusBadRequest},
	"AccessDeniedException":         {schemas.ErrorTypePermissionDenied, fasthttp.StatusForbidden},
	"OptInRequired":                 {schemas.ErrorTypePermissionDenied, fasthttp.StatusForbidden},
	"NotAuthorized":                 {schemas.ErrorTypeAuthentication, fasthttp.StatusUnauthorized},
	"ExpiredTokenException":         {schemas.ErrorTypeAuthentication, fasthttp.StatusUnauthorized},
	"UnrecognizedClientException":   {schemas.ErrorTypeAuthentication, fasthttp.StatusUnauthorized},
	"ResourceNotFoundException":     {schemas.ErrorTypeNotFound, fasthttp.StatusNotFound},
	"UnknownOperationException":     {schemas.ErrorTypeNotFound, fasthttp.StatusNotFound},
	"ModelTimeoutException":         {schemas.ErrorTypeRequestTimeout, fasthttp.StatusRequestTimeout},
	"RequestTimeoutException":       {schemas.ErrorTypeRequestTimeout, fasthttp.StatusRequestTimeout},
	"InternalServerException":       {schemas.ErrorTypeServerError, fasthttp.StatusInternalServerError},
	"InternalFailure":               {schemas.ErrorTypeServerError, fasthttp.StatusInternalServerError},
	"ServiceUnavailableException":   {schemas.ErrorTypeServiceUnavailable, fasthttp.StatusServiceUnavailable},
	"ServiceUnavailable":            {schemas.ErrorTypeServiceUnavailable, fasthttp.StatusServiceUnavailable},
	"ModelErrorException":           {schemas.ErrorTypeServerError, fasthttp.StatusFailedDependency},
}

// isRecognizedBedrockException reports whether typ matches a known AWS
// Bedrock exception name, in either PascalCase (HTTP) or camelCase
// (EventStream) form. Used by ToBedrockError to decide whether a top-level
// .Type value is a genuine, original-cased Bedrock exception name (safe to
// forward verbatim, preserving whichever casing convention the source API
// surface actually used) versus something else entirely (a governance
// Decision string, an unrelated internal sentinel) that happens to be
// sitting on the same field.
func isRecognizedBedrockException(typ string) bool {
	if typ == "" {
		return false
	}
	if _, ok := bedrockExceptionToOpenAICanonicalType[typ]; ok {
		return true
	}
	capitalized := strings.ToUpper(typ[:1]) + typ[1:]
	_, ok := bedrockExceptionToOpenAICanonicalType[capitalized]
	return ok
}

// normalizeBedrockErrorType returns the OpenAI-canonical error type and
// status code for a raw Bedrock exception name, plus whether the exception
// was actually recognized. Callers with an already-known-correct HTTP status
// (parseBedrockHTTPError, which gets the real status from the HTTP response
// before this runs) MUST check `recognized` and keep their own status when
// false — schema drift / an AWS exception not yet in
// bedrockExceptionToOpenAICanonicalType (AWS has many more than the ~19
// entries listed here) should never silently downgrade a real, specific
// status (e.g. 403) to a generic 500. Callers with no real status of their
// own to preserve (newBedrockStreamException's mid-stream case, which never
// touches .StatusCode from this function at all) can ignore `recognized`.
// Found via codex review.
func normalizeBedrockErrorType(exceptionType string) (canonicalType string, statusCode int, recognized bool) {
	if mapped, ok := bedrockExceptionToOpenAICanonicalType[exceptionType]; ok {
		return mapped.canonicalType, mapped.statusCode, true
	}
	// AWS uses two different casings for the same exception names depending
	// on the API surface: PascalCase for InvokeModel-style HTTP errors
	// ("ThrottlingException", matching bedrockExceptionToOpenAICanonicalType's
	// keys), camelCase for EventStream in-stream exception members
	// ("throttlingException", used by newBedrockStreamException /
	// retryableBedrockExceptions in bedrock.go). Retry once with the first
	// letter capitalized before falling back, so both callers hit the same
	// table without needing two copies of it.
	if exceptionType != "" {
		capitalized := strings.ToUpper(exceptionType[:1]) + exceptionType[1:]
		if mapped, ok := bedrockExceptionToOpenAICanonicalType[capitalized]; ok {
			return mapped.canonicalType, mapped.statusCode, true
		}
	}
	return schemas.ErrorTypeServerError, fasthttp.StatusInternalServerError, false
}

func parseBedrockHTTPError(statusCode int, headers http.Header, body []byte) *schemas.BifrostError {
	fastResp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(fastResp)

	fastResp.SetStatusCode(statusCode)
	for k, values := range headers {
		for _, value := range values {
			fastResp.Header.Add(k, value)
		}
	}
	fastResp.SetBody(body)

	var errorResp BedrockError
	bifrostErr := providerUtils.HandleProviderAPIError(fastResp, &errorResp)
	if errorResp.Message != "" {
		if bifrostErr.Error == nil {
			bifrostErr.Error = &schemas.ErrorField{}
		}
		bifrostErr.Error.Message = errorResp.Message
		bifrostErr.Error.Code = errorResp.Code
	}

	exceptionType := errorResp.Type
	if exceptionType == "" {
		if hv := headers.Get("X-Amzn-Errortype"); hv != "" {
			if i := strings.IndexAny(hv, ":#"); i >= 0 {
				hv = hv[:i]
			}
			exceptionType = strings.TrimSpace(hv)
		}
	}
	if exceptionType != "" {
		// Keep the raw AWS exception name on the top-level .Type field
		// (unchanged behavior — some internal/legacy readers may still rely
		// on it, e.g. ToBedrockError's fallback chain). Stage 1
		// (normalize-to-OpenAI): .Error.Type gets the OpenAI-canonical value
		// instead of the raw exception name, so Stage 2 route translators
		// (ToAnthropicChatCompletionError, etc.) can render Bedrock-backed
		// errors correctly on non-Bedrock routes — mirrors the Anthropic and
		// governance fixes in this same design.
		if bifrostErr.Type == nil {
			bifrostErr.Type = schemas.Ptr(exceptionType)
		}
		if bifrostErr.Error == nil {
			bifrostErr.Error = &schemas.ErrorField{}
		}
		// Only overwrite StatusCode when the exception was actually
		// recognized. HandleProviderAPIError already set bifrostErr.StatusCode
		// from the real HTTP response above — for an unrecognized AWS
		// exception (schema drift, one of the many exceptions not yet in
		// bedrockExceptionToOpenAICanonicalType), normalizeBedrockErrorType's
		// generic 500 fallback must NOT clobber that already-correct, more
		// specific status. Found via codex review.
		canonicalType, canonicalStatus, recognized := normalizeBedrockErrorType(exceptionType)
		bifrostErr.Error.Type = new(canonicalType)
		if recognized {
			bifrostErr.StatusCode = new(canonicalStatus)
		}
	}

	return bifrostErr
}

package anthropic

import (
	"fmt"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// streamErrorStatus maps an Anthropic SSE error event's type to the status the same
// failure carries over HTTP. The event rides a committed 200, so without a status the
// error reaches metrics as a caller 400 and ClassifyFailure has nothing to act on — an
// overloaded_error mid-stream is then neither retried nor rotated away from.
func streamErrorStatus(errType string) int {
	switch errType {
	case "invalid_request_error":
		return fasthttp.StatusBadRequest
	case "authentication_error":
		return fasthttp.StatusUnauthorized
	case "permission_error":
		return fasthttp.StatusForbidden
	case "not_found_error":
		return fasthttp.StatusNotFound
	case "request_too_large":
		return fasthttp.StatusRequestEntityTooLarge
	case "rate_limit_error":
		return fasthttp.StatusTooManyRequests
	case "api_error":
		return fasthttp.StatusInternalServerError
	case "overloaded_error":
		// Anthropic's own non-standard overload status, already in the transient set.
		return 529
	}
	// Unknown: the upstream failed and gave nothing to go on.
	return fasthttp.StatusBadGateway
}

// anthropicErrorTypes is the set of error.type values Anthropic documents for its
// error envelope (https://docs.claude.com/en/api/errors).
var anthropicErrorTypes = map[string]struct{}{
	"invalid_request_error": {},
	"authentication_error":  {},
	"billing_error":         {},
	"permission_error":      {},
	"not_found_error":       {},
	"conflict_error":        {},
	"request_too_large":     {},
	"rate_limit_error":      {},
	"api_error":             {},
	"timeout_error":         {},
	"overloaded_error":      {},
}

// googleStatusToAnthropicErrorType maps the google.rpc.Code names that the Gemini and
// Vertex providers surface as Error.Type onto Anthropic error types.
var googleStatusToAnthropicErrorType = map[string]string{
	"INVALID_ARGUMENT":    "invalid_request_error",
	"FAILED_PRECONDITION": "invalid_request_error",
	"OUT_OF_RANGE":        "invalid_request_error",
	"UNAUTHENTICATED":     "authentication_error",
	"PERMISSION_DENIED":   "permission_error",
	"NOT_FOUND":           "not_found_error",
	"RESOURCE_EXHAUSTED":  "rate_limit_error",
	"UNAVAILABLE":         "overloaded_error",
	"DEADLINE_EXCEEDED":   "api_error",
	"INTERNAL":            "api_error",
}

// anthropicErrorTypeForStatus derives an Anthropic error type from an HTTP status,
// falling back to the generic type for the status class when the status has no
// dedicated type.
func anthropicErrorTypeForStatus(status int) string {
	if errType, ok := dedicatedAnthropicErrorType(status); ok {
		return errType
	}
	if status >= 400 && status < 500 {
		// Anthropic uses invalid_request_error for 4xx statuses without a dedicated type.
		return "invalid_request_error"
	}
	return "api_error"
}

// dedicatedAnthropicErrorType returns the Anthropic error type documented for exactly
// this HTTP status, and false for statuses that only imply a class (e.g. 500, 502,
// 503). Only a dedicated status is specific enough to override a type taken from the
// error body.
func dedicatedAnthropicErrorType(status int) (string, bool) {
	switch status {
	case fasthttp.StatusBadRequest, fasthttp.StatusUnprocessableEntity:
		return "invalid_request_error", true
	case fasthttp.StatusUnauthorized:
		return "authentication_error", true
	case fasthttp.StatusPaymentRequired:
		return "billing_error", true
	case fasthttp.StatusForbidden:
		return "permission_error", true
	case fasthttp.StatusNotFound:
		return "not_found_error", true
	case fasthttp.StatusConflict:
		return "conflict_error", true
	case fasthttp.StatusRequestEntityTooLarge:
		return "request_too_large", true
	case fasthttp.StatusTooManyRequests:
		return "rate_limit_error", true
	case fasthttp.StatusGatewayTimeout:
		return "timeout_error", true
	case 529:
		return "overloaded_error", true
	}
	return "", false
}

// isServerAnthropicErrorType reports whether an Anthropic error type describes a 5xx
// (server-side) failure rather than a 4xx caller error.
func isServerAnthropicErrorType(errType string) bool {
	switch errType {
	case "api_error", "timeout_error", "overloaded_error":
		return true
	}
	return false
}

// anthropicErrorClassMismatch reports whether an Anthropic error type and an HTTP error
// status disagree on 4xx vs 5xx, e.g. rate_limit_error sent with a 500.
func anthropicErrorClassMismatch(errType string, status int) bool {
	return isServerAnthropicErrorType(errType) != (status >= 500)
}

// anthropicErrorType picks the error.type for the Anthropic error envelope. Clients
// branch on this field, so it must be one of Anthropic's documented types and agree
// with the HTTP status the error is sent with:
//   - A documented type from the body is kept unless a real status puts it in the other
//     4xx/5xx class; within a class the upstream's own type is the finer signal (a
//     native Anthropic overloaded_error with 529 or 500 passes through untouched).
//   - A google.rpc.Code mapping is only an approximation, so it yields to the status
//     whenever the status has a dedicated type or a different class (DEADLINE_EXCEEDED
//     sent as 504 becomes timeout_error).
//   - Bifrost's own wire-level types (request_cancelled, request_timed_out, ...) are
//     kept as-is: they are Bifrost's client-visible error contract (core/schemas/errors.go).
func anthropicErrorType(bifrostErr *schemas.BifrostError) string {
	errType := ""
	if bifrostErr.Error != nil && bifrostErr.Error.Type != nil {
		errType = *bifrostErr.Error.Type
	}
	// Only an explicit error status may override a type from the body; the defaults
	// EffectiveHTTPStatus() fills in for a missing status carry no upstream signal.
	status := 0
	if bifrostErr.StatusCode != nil {
		if effective := bifrostErr.EffectiveHTTPStatus(); effective >= 400 {
			status = effective
		}
	}
	if _, ok := anthropicErrorTypes[errType]; ok {
		if status != 0 && anthropicErrorClassMismatch(errType, status) {
			return anthropicErrorTypeForStatus(status)
		}
		return errType
	}
	if mapped, ok := googleStatusToAnthropicErrorType[errType]; ok {
		if status != 0 {
			if statusType, ok := dedicatedAnthropicErrorType(status); ok {
				return statusType
			}
			if anthropicErrorClassMismatch(mapped, status) {
				return anthropicErrorTypeForStatus(status)
			}
		}
		return mapped
	}
	switch errType {
	case schemas.RequestCancelled, schemas.RequestTimedOut, schemas.RequestDropped,
		schemas.ProviderConnectionFailed, schemas.NoKeySupportsModel:
		return errType
	}
	if bifrostErr.StatusCode != nil || errType == "" {
		return anthropicErrorTypeForStatus(bifrostErr.EffectiveHTTPStatus())
	}
	// Unrecognized type and no status to go on (e.g. a mid-stream provider error).
	return "api_error"
}

// ToAnthropicChatCompletionError converts a BifrostError to AnthropicMessageError
func ToAnthropicChatCompletionError(bifrostErr *schemas.BifrostError) *AnthropicMessageError {
	if bifrostErr == nil {
		return nil
	}

	errorStruct := AnthropicMessageErrorStruct{
		Type:    anthropicErrorType(bifrostErr),
		Message: bifrostErr.GetErrorString(),
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

// ParseAnthropicError parses an error response that follows the Anthropic error envelope
// ({"type":"error","error":{"type":...,"message":...}}) into a BifrostError. It is exported
// because providers that front the Anthropic wire format without being Anthropic (for example
// the Bedrock Mantle native-Anthropic surface, whose errors use this envelope rather than the
// AWS JSON error shape bedrock-runtime returns) need the same parsing.
func ParseAnthropicError(resp *fasthttp.Response) *schemas.BifrostError {
	var errorResp AnthropicError
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)
	if errorResp.Error != nil {
		if bifrostErr.Error == nil {
			bifrostErr.Error = &schemas.ErrorField{}
		}
		bifrostErr.Error.Type = &errorResp.Error.Type
		bifrostErr.Error.Message = errorResp.Error.Message
	}
	return bifrostErr
}

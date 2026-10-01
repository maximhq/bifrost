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

// ToAnthropicChatCompletionError converts a BifrostError to AnthropicMessageError
func ToAnthropicChatCompletionError(bifrostErr *schemas.BifrostError) *AnthropicMessageError {
	if bifrostErr == nil {
		return nil
	}

	// Safely extract type and message from nested error
	errorType := "api_error"
	message := bifrostErr.GetErrorString()
	if bifrostErr.Error != nil {
		if bifrostErr.Error.Type != nil && *bifrostErr.Error.Type != "" {
			errorType = *bifrostErr.Error.Type
		}
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

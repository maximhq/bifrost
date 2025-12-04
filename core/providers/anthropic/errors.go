package anthropic

import (
	"encoding/json"
	"fmt"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// ToAnthropicChatCompletionError converts a BifrostError to AnthropicMessageError
func ToAnthropicChatCompletionError(bifrostErr *schemas.BifrostError) *AnthropicMessageError {
	if bifrostErr == nil {
		return nil
	}

	// Provide blank strings for nil pointer fields
	errorType := ""
	if bifrostErr.Type != nil {
		errorType = *bifrostErr.Type
	}

	// Safely extract message from nested error
	message := ""
	if bifrostErr.Error != nil {
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
	jsonData, err := json.Marshal(anthropicErr)
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
		bifrostErr.Error.Type = &errorResp.Error.Type
		bifrostErr.Error.Message = errorResp.Error.Message
	}
	return bifrostErr
}

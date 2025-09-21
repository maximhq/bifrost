package openai

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// ToOpenAIError converts a BifrostError to OpenAIChatError
func ToOpenAIError(bifrostErr *schemas.BifrostError) *OpenAIChatError {
	if bifrostErr == nil {
		return nil
	}

	// Provide blank strings for nil pointer fields
	eventID := ""
	if bifrostErr.EventID != nil {
		eventID = *bifrostErr.EventID
	}

	errorType := ""
	if bifrostErr.Type != nil {
		errorType = *bifrostErr.Type
	}

	// Handle nested error fields with nil checks
	errorStruct := OpenAIChatErrorStruct{
		Type:    "",
		Code:    "",
		Message: bifrostErr.Error.Message,
		Param:   bifrostErr.Error.Param,
		EventID: eventID,
	}

	if bifrostErr.Error.Type != nil {
		errorStruct.Type = *bifrostErr.Error.Type
	}

	if bifrostErr.Error.Code != nil {
		errorStruct.Code = *bifrostErr.Error.Code
	}

	if bifrostErr.Error.EventID != nil {
		errorStruct.EventID = *bifrostErr.Error.EventID
	}

	return &OpenAIChatError{
		EventID: eventID,
		Type:    errorType,
		Error:   errorStruct,
	}
}

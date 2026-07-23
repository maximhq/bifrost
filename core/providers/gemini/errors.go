package gemini

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// GeminiStreamAPIError is an error payload Gemini delivers inside an HTTP 200
// stream body (e.g. mid-stream 429 RESOURCE_EXHAUSTED quota aborts). It keeps
// the upstream code/status/message so they can be preserved on the wire.
type GeminiStreamAPIError struct {
	Err *GeminiGenerationErrorStruct
}

func (e *GeminiStreamAPIError) Error() string {
	return fmt.Sprintf("gemini api error: %d %s - %s", e.Err.Code, e.Err.Status, e.Err.Message)
}

// toGeminiStreamBifrostError builds the BifrostError for an error payload
// detected in a stream chunk, preserving the upstream code/status/message
// (e.g. mid-stream 429s) when the payload parsed as a typed API error.
func toGeminiStreamBifrostError(err error) *schemas.BifrostError {
	bifrostErr := &schemas.BifrostError{
		Type:           schemas.Ptr("gemini_api_error"),
		IsBifrostError: false,
		Error: &schemas.ErrorField{
			Message: err.Error(),
			Error:   err,
		},
	}
	var apiErr *GeminiStreamAPIError
	if errors.As(err, &apiErr) {
		bifrostErr.StatusCode = schemas.Ptr(apiErr.Err.Code)
		bifrostErr.Error.Code = schemas.Ptr(strconv.Itoa(apiErr.Err.Code))
		bifrostErr.Error.Message = apiErr.Err.Message
		if apiErr.Err.Status != "" {
			bifrostErr.Error.Type = schemas.Ptr(apiErr.Err.Status)
		}
	}
	return bifrostErr
}

// ToGeminiError derives a GeminiGenerationError from a BifrostError
func ToGeminiError(bifrostErr *schemas.BifrostError) *GeminiGenerationError {
	if bifrostErr == nil {
		return nil
	}
	code := 500
	status := ""
	if bifrostErr.Error != nil && bifrostErr.Error.Type != nil {
		status = *bifrostErr.Error.Type
	}
	message := ""
	if bifrostErr.Error != nil && bifrostErr.Error.Message != "" {
		message = bifrostErr.Error.Message
	}
	if bifrostErr.StatusCode != nil {
		code = *bifrostErr.StatusCode
	}
	return &GeminiGenerationError{
		Error: &GeminiGenerationErrorStruct{
			Code:    code,
			Message: message,
			Status:  status,
		},
	}
}

// parseGeminiError parses Gemini error responses
func parseGeminiError(resp *fasthttp.Response) *schemas.BifrostError {
	// Try to parse as []GeminiGenerationError
	var errorResps []GeminiGenerationError
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResps)
	if len(errorResps) > 0 {
		var message string
		var firstError *GeminiGenerationErrorStruct
		for _, errorResp := range errorResps {
			if errorResp.Error != nil {
				if firstError == nil {
					firstError = errorResp.Error
				}
				message = message + errorResp.Error.Message + "\n"
			}
		}
		// Trim trailing newline
		message = strings.TrimSuffix(message, "\n")
		if bifrostErr.Error == nil {
			bifrostErr.Error = &schemas.ErrorField{}
		}
		// Set Code from first error if available
		if firstError != nil {
			bifrostErr.Error.Code = schemas.Ptr(strconv.Itoa(firstError.Code))
			if firstError.Status != "" {
				bifrostErr.Error.Type = schemas.Ptr(firstError.Status)
			}
		}
		// Set Message to trimmed concatenated message
		bifrostErr.Error.Message = message
		return bifrostErr
	}

	// Try to parse as GeminiGenerationError
	var errorResp GeminiGenerationError
	bifrostErr = providerUtils.HandleProviderAPIError(resp, &errorResp)
	if errorResp.Error != nil {
		if bifrostErr.Error == nil {
			bifrostErr.Error = &schemas.ErrorField{}
		}
		bifrostErr.Error.Code = schemas.Ptr(strconv.Itoa(errorResp.Error.Code))
		bifrostErr.Error.Message = errorResp.Error.Message
		if errorResp.Error.Status != "" {
			bifrostErr.Error.Type = schemas.Ptr(errorResp.Error.Status)
		}
	}
	return bifrostErr
}

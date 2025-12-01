package gemini

import (
	"strconv"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

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
		for _, errorResp := range errorResps {
			if errorResp.Error != nil {
				message = message + errorResp.Error.Message + "\n"
			}
		}
		if bifrostErr.Error == nil {
			bifrostErr.Error = &schemas.ErrorField{}
		}
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
	}
	return bifrostErr
}

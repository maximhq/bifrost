package xai

import (
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// XAIErrorResponse represents xAI's error response format
type XAIErrorResponse struct {
	Code  string `json:"code"`
	Error string `json:"error"`
}

// ParseXAIError parses xAI-specific error responses.
// xAI returns errors in format: {"code": "...", "error": "..."}
// Unlike OpenAI which uses: {"error": {"message": "...", "type": "...", "code": "..."}}
func ParseXAIError(resp *fasthttp.Response) *schemas.BifrostError {
	// Try to parse xAI error format
	var xaiErr XAIErrorResponse
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &xaiErr)

	if bifrostErr == nil {
		return nil
	}

	// If we successfully parsed xAI format, extract the fields
	if xaiErr.Error != "" {
		if bifrostErr.Error == nil {
			bifrostErr.Error = &schemas.ErrorField{}
		}
		bifrostErr.Error.Message = xaiErr.Error
		if xaiErr.Code != "" {
			bifrostErr.Error.Code = schemas.Ptr(xaiErr.Code)
		}
	}

	return bifrostErr
}

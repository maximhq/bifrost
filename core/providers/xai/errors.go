package xai

import (
	"github.com/bytedance/sonic"
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
func ParseXAIError(resp *fasthttp.Response, requestType schemas.RequestType, providerName schemas.ModelProvider, model string) *schemas.BifrostError {
	statusCode := resp.StatusCode()

	// Decode body
	decodedBody, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return &schemas.BifrostError{
			IsBifrostError: false,
			StatusCode:     &statusCode,
			Error: &schemas.ErrorField{
				Message: err.Error(),
			},
			ExtraFields: schemas.BifrostErrorExtraFields{
				Provider:       providerName,
				ModelRequested: model,
				RequestType:    requestType,
			},
		}
	}

	// Try to parse xAI error format
	var xaiErr XAIErrorResponse
	if err := sonic.Unmarshal(decodedBody, &xaiErr); err == nil && xaiErr.Error != "" {
		code := xaiErr.Code
		return &schemas.BifrostError{
			IsBifrostError: false,
			StatusCode:     &statusCode,
			Error: &schemas.ErrorField{
				Code:    &code,
				Message: xaiErr.Error,
			},
			ExtraFields: schemas.BifrostErrorExtraFields{
				Provider:       providerName,
				ModelRequested: model,
				RequestType:    requestType,
			},
		}
	}

	// Fallback: couldn't parse as xAI format, return raw body
	return &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     &statusCode,
		Error: &schemas.ErrorField{
			Message: string(decodedBody),
		},
		ExtraFields: schemas.BifrostErrorExtraFields{
			Provider:       providerName,
			ModelRequested: model,
			RequestType:    requestType,
		},
	}
}

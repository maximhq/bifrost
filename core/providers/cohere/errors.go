package cohere

import (
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

func parseCohereError(resp *fasthttp.Response) *schemas.BifrostError {
	var errorResp CohereError
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)
	bifrostErr.Type = &errorResp.Type
	if bifrostErr.Error == nil {
		bifrostErr.Error = &schemas.ErrorField{}
	}
	bifrostErr.Error.Message = errorResp.Message
	if errorResp.Code != nil {
		bifrostErr.Error.Code = errorResp.Code
	}
	if errorResp.Type != "" && bifrostErr.Error.Type == nil {
		typeCopy := errorResp.Type
		bifrostErr.Error.Type = &typeCopy
	}
	return bifrostErr
}

// CohereErrorResponse is the body Cohere returns on a failed request. Verified against the live
// v2 API, which replies with a flat {"id", "message"} on 400, 401 and 404 alike.
type CohereErrorResponse struct {
	ID      string `json:"id,omitempty"`
	Message string `json:"message"`
}

// ToCohereError converts a Bifrost error into Cohere's error wire shape.
func ToCohereError(bifrostErr *schemas.BifrostError) *CohereErrorResponse {
	if bifrostErr == nil {
		return nil
	}

	errorResponse := &CohereErrorResponse{}
	if bifrostErr.EventID != nil {
		errorResponse.ID = *bifrostErr.EventID
	}
	if bifrostErr.Error != nil {
		errorResponse.Message = bifrostErr.Error.Message
	}
	return errorResponse
}

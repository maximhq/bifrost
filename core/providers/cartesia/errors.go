package cartesia

import (
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// parseCartesiaError converts a Cartesia HTTP error response into a BifrostError.
// It relies on providerUtils.HandleProviderAPIError for base handling (status code,
// raw response, generic message) and overlays Cartesia-specific fields when present.
func parseCartesiaError(resp *fasthttp.Response) *schemas.BifrostError {
	var errorResp CartesiaError
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)

	message := errorResp.Message
	if message == "" {
		message = errorResp.Error
	}

	if message != "" {
		if bifrostErr.Error == nil {
			bifrostErr.Error = &schemas.ErrorField{}
		}
		bifrostErr.Error.Message = message
		if errorResp.Type != "" {
			bifrostErr.Error.Type = schemas.Ptr(errorResp.Type)
		}
		if errorResp.Code != "" {
			bifrostErr.Error.Code = schemas.Ptr(errorResp.Code)
		}
	}

	return bifrostErr
}

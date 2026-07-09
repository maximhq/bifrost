package sarvam

import (
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// parseSarvamError parses Sarvam's error envelope: {"error":{"message","code","request_id"}}.
func parseSarvamError(resp *fasthttp.Response) *schemas.BifrostError {
	var errorResp SarvamError
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)
	if errorResp.Error != nil {
		if bifrostErr.Error == nil {
			bifrostErr.Error = &schemas.ErrorField{}
		}
		bifrostErr.Error.Message = errorResp.Error.Message
		bifrostErr.Error.Type = new(errorResp.Error.Code)
	}
	return bifrostErr
}

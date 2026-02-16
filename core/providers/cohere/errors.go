package cohere

import (
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

func parseCohereError(resp *fasthttp.Response, meta *providerUtils.RequestMetadata) *schemas.BifrostError {
	var errorResp CohereError
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)
	bifrostErr.Type = &errorResp.Type
	bifrostErr.Error.Message = errorResp.Message
	if errorResp.Code != nil {
		bifrostErr.Error.Code = errorResp.Code
	}
	if meta != nil {
		bifrostErr.ExtraFields.Provider = meta.Provider
		bifrostErr.ExtraFields.ModelRequested = meta.Model
		bifrostErr.ExtraFields.RequestType = meta.RequestType
	}
	return bifrostErr
}

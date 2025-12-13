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
	if bifrostErr.Error == nil {
		bifrostErr.Error = &schemas.ErrorField{}
	}
	bifrostErr.Error.Message = errorResp.Message
	if errorResp.Code != nil {
		bifrostErr.Error.Code = errorResp.Code
	}
	if meta != nil {
		bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
			Provider:       meta.Provider,
			ModelRequested: meta.Model,
			RequestType:    meta.RequestType,
		}
	}
	return bifrostErr
}

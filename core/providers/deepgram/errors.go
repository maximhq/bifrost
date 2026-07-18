package deepgram

import (
	"github.com/valyala/fasthttp"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

func parseDeepgramError(resp *fasthttp.Response) *schemas.BifrostError {
	var errorResp DeepgramErrorResponse
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)
	if errorResp.ErrMsg != nil && *errorResp.ErrMsg != "" {
		if bifrostErr.Error == nil {
			bifrostErr.Error = &schemas.ErrorField{}
		}
		bifrostErr.Error.Message = *errorResp.ErrMsg
		if errorResp.ErrCode != nil {
			bifrostErr.Error.Type = schemas.Ptr(*errorResp.ErrCode)
		}
	}
	return bifrostErr
}

package sarvam

import (
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// parseSarvamError converts a non-2xx Sarvam response into a BifrostError,
// preferring Sarvam's structured error message/code when available.
func parseSarvamError(resp *fasthttp.Response) *schemas.BifrostError {
	var errorResp SarvamError
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)
	if msg := errorResp.Message(); msg != "" {
		if bifrostErr.Error == nil {
			bifrostErr.Error = &schemas.ErrorField{}
		}
		bifrostErr.Error.Message = msg
		if code := errorResp.Code(); code != "" {
			bifrostErr.Error.Type = schemas.Ptr(code)
		}
	}
	return bifrostErr
}

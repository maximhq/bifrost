package machgen

import (
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// parseMachGenError converts a non-2xx MachGen response into a BifrostError.
func parseMachGenError(resp *fasthttp.Response) *schemas.BifrostError {
	var errorResp MachGenAPIError
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)
	if bifrostErr.Error == nil {
		bifrostErr.Error = &schemas.ErrorField{}
	}

	switch detail := errorResp.Detail.(type) {
	case string:
		bifrostErr.Error.Message = detail
	case nil:
	default:
		// 422 validation errors carry a list of entries; keep them readable rather than dropping them.
		if encoded, err := providerUtils.MarshalSorted(detail); err == nil {
			bifrostErr.Error.Message = string(encoded)
		}
	}
	if bifrostErr.Error.Message == "" {
		bifrostErr.Error.Message = "MachGen API request failed"
	}
	return bifrostErr
}

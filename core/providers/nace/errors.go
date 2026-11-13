package nace

import (
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

func parseNaceError(resp *fasthttp.Response) *schemas.BifrostError {
	var native NaceError
	err := providerUtils.HandleProviderAPIError(resp, &native)
	if err.Error == nil {
		err.Error = &schemas.ErrorField{}
	}
	if native.Error.Message != "" {
		err.Error.Message = native.Error.Message
	}
	if native.Error.Type != "" {
		err.Error.Type = &native.Error.Type
	}
	return err
}

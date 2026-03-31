package bedrock

import (
	"net/http"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

func parseBedrockHTTPError(statusCode int, headers http.Header, body []byte) *schemas.BifrostError {
	fastResp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(fastResp)

	fastResp.SetStatusCode(statusCode)
	for k, values := range headers {
		for _, value := range values {
			fastResp.Header.Add(k, value)
		}
	}
	fastResp.SetBody(body)

	var errorResp BedrockError
	bifrostErr := providerUtils.HandleProviderAPIError(fastResp, &errorResp)
	if errorResp.Message != "" {
		if bifrostErr.Error == nil {
			bifrostErr.Error = &schemas.ErrorField{}
		}
		bifrostErr.Error.Message = errorResp.Message
		bifrostErr.Error.Code = errorResp.Code
	}

	return bifrostErr
}

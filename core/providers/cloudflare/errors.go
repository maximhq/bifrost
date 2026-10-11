package cloudflare

import (
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// parseCloudflareError parses a Cloudflare error response into a BifrostError.
// Cloudflare reports every failure, including those of its OpenAI-compatible
// endpoints, in a {"success": false, "errors": [{"code", "message"}]} envelope.
func parseCloudflareError(resp *fasthttp.Response) *schemas.BifrostError {
	var errorResp CloudflareError
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)

	messages := make([]string, 0, len(errorResp.Errors))
	for _, entry := range errorResp.Errors {
		if entry.Message != "" {
			messages = append(messages, entry.Message)
		}
	}

	if bifrostErr.Error == nil {
		bifrostErr.Error = &schemas.ErrorField{}
	}
	if len(messages) > 0 {
		bifrostErr.Error.Message = strings.Join(messages, "; ")
	} else if bifrostErr.Error.Message == "" {
		bifrostErr.Error.Message = "Cloudflare API request failed"
	}

	return bifrostErr
}

// parseCloudflareEnvelopeFailure builds the error for a 200 whose envelope carries no
// result: success:false, or a missing or null result. The upstream status says "ok", so it
// is reported as 502 Bad Gateway: the provider answered, but not with a result.
func parseCloudflareEnvelopeFailure(resp *fasthttp.Response) *schemas.BifrostError {
	bifrostErr := parseCloudflareError(resp)
	bifrostErr.StatusCode = schemas.Ptr(fasthttp.StatusBadGateway)
	return bifrostErr
}

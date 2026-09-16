package replicate

import (
	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// parseReplicateError parses Replicate API error response, with the retry hint from the
// response headers.
func parseReplicateError(resp *fasthttp.Response) *schemas.BifrostError {
	body := resp.Body()
	statusCode := resp.StatusCode()

	bifrostErr := &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     &statusCode,
		Error: &schemas.ErrorField{
			// Fallback to generic error, replaced below when the body carries a detail.
			Message: string(body),
		},
	}

	var replicateErr ReplicateError
	if err := sonic.Unmarshal(body, &replicateErr); err == nil && replicateErr.Detail != "" {
		bifrostErr.Error.Message = replicateErr.Detail
	}

	providerUtils.ApplyRetryAfter(bifrostErr, &resp.Header)
	return bifrostErr
}

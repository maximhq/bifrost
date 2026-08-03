package modelark

import (
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// parseModelArkError parses ModelArk API error responses and converts them to BifrostError.
func parseModelArkError(resp *fasthttp.Response) *schemas.BifrostError {
	var errorResp ModelArkAPIError
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)

	if bifrostErr.Error == nil {
		bifrostErr.Error = &schemas.ErrorField{}
	}
	applyModelArkError(bifrostErr, errorResp.Error)

	if bifrostErr.Error.Message == "" {
		bifrostErr.Error.Message = "ModelArk API request failed"
	}
	bifrostErr.Error.Message = strings.TrimRight(bifrostErr.Error.Message, "\n")

	return bifrostErr
}

// applyModelArkError copies a ModelArk error envelope onto a BifrostError. The code is
// carried over because callers act on it: ModelNotOpen, for instance, means the account
// has not enabled the requested model, so retrying can never succeed.
func applyModelArkError(bifrostErr *schemas.BifrostError, apiErr *ModelArkError) {
	if apiErr == nil {
		return
	}
	if apiErr.Message != "" {
		bifrostErr.Error.Message = apiErr.Message
	}
	if apiErr.Code != "" {
		bifrostErr.Error.Code = schemas.Ptr(apiErr.Code)
	}
}

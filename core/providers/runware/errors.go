package runware

import (
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// parseRunwareError parses a Runware error HTTP response into a BifrostError.
// Runware reports failures in a top-level "errors" array.
func parseRunwareError(resp *fasthttp.Response) *schemas.BifrostError {
	var errorResp RunwareResponse
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)

	if msg := firstRunwareErrorMessage(errorResp.Errors); msg != "" {
		if bifrostErr.Error == nil {
			bifrostErr.Error = &schemas.ErrorField{}
		}
		bifrostErr.Error.Message = msg
	} else if bifrostErr.Error == nil || bifrostErr.Error.Message == "" {
		if bifrostErr.Error == nil {
			bifrostErr.Error = &schemas.ErrorField{}
		}
		bifrostErr.Error.Message = "Runware API request failed"
	}

	if bifrostErr.Error != nil {
		bifrostErr.Error.Message = strings.TrimRight(bifrostErr.Error.Message, "\n")
	}

	return bifrostErr
}

// firstRunwareErrorMessage returns a human-readable message from the first error, if any.
func firstRunwareErrorMessage(errs []RunwareError) string {
	for _, e := range errs {
		if e.Message != "" {
			if e.Parameter != "" {
				return e.Message + " (parameter: " + e.Parameter + ")"
			}
			return e.Message
		}
	}
	return ""
}

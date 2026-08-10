package deepgram

import (
	"strings"
	"strconv"
	"github.com/valyala/fasthttp"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

func parseDeepgramError(resp *fasthttp.Response) *schemas.BifrostError {
	var errorResp DeepgramError
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)

	if bifrostErr == nil {
		bifrostErr = &schemas.BifrostError{
			IsBifrostError: false,
			StatusCode:     schemas.Ptr(resp.StatusCode()),
		}
	}

	if bifrostErr.Error == nil {
		bifrostErr.Error = &schemas.ErrorField{}
	}

	// Modern error format
	if errorResp.Message != "" {
		bifrostErr.Error.Message = errorResp.Message

		if errorResp.Category != "" {
			bifrostErr.Error.Type = schemas.Ptr(errorResp.Category)
		}

		if errorResp.Details != "" {
			bifrostErr.Error.Message += ": " + errorResp.Details
		}

		return bifrostErr
	}

	// Legacy error format
	if errorResp.ErrMsg != "" {
		bifrostErr.Error.Message = errorResp.ErrMsg

		if errorResp.ErrCode != "" {
			bifrostErr.Error.Type = schemas.Ptr(errorResp.ErrCode)
		}

		return bifrostErr
	}

	// Fallback
	rawBody := strings.TrimSpace(string(resp.Body()))
	if rawBody != "" {
		bifrostErr.Error.Message = rawBody
	} else {
		bifrostErr.Error.Message = "deepgram API returned status " +
			strconv.Itoa(resp.StatusCode()) +
			" with no error details"
	}

	return bifrostErr
}
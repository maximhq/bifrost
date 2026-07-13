package cartesia

import (
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// parseCartesiaError converts a Cartesia HTTP error response into a BifrostError.
// It relies on providerUtils.HandleProviderAPIError for base handling (status code,
// raw response, generic message) and overlays Cartesia's structured error fields
// (error_code / title / message / request_id / doc_url) when present.
func parseCartesiaError(resp *fasthttp.Response) *schemas.BifrostError {
	var errorResp CartesiaError
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)

	// Prefer the detailed message, then the human-readable title, then the
	// legacy/plain-text "error" field.
	message := errorResp.Message
	if message == "" {
		message = errorResp.Title
	}
	if message == "" {
		message = errorResp.Error
	}

	if message != "" || errorResp.ErrorCode != "" || errorResp.RequestID != "" || errorResp.DocURL != "" {
		if bifrostErr.Error == nil {
			bifrostErr.Error = &schemas.ErrorField{}
		}
		if message != "" {
			bifrostErr.Error.Message = message
		}
		if errorResp.ErrorCode != "" {
			bifrostErr.Error.Code = schemas.Ptr(errorResp.ErrorCode)
		}
		// Surface request_id / doc_url (no dedicated ErrorField columns) via Param
		// so they reach the caller for support/debugging.
		if errorResp.RequestID != "" || errorResp.DocURL != "" {
			meta := map[string]string{}
			if errorResp.RequestID != "" {
				meta["request_id"] = errorResp.RequestID
			}
			if errorResp.DocURL != "" {
				meta["doc_url"] = errorResp.DocURL
			}
			bifrostErr.Error.Param = meta
		}
	}

	return bifrostErr
}

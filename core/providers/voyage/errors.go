package voyage

import (
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// parseVoyageError converts a Voyage error response into a BifrostError. Voyage replies with
// {"detail": "<text>"} on every failure; a structured detail (validation errors from the
// framework layer) is kept as its JSON text so nothing is dropped.
func parseVoyageError(resp *fasthttp.Response) *schemas.BifrostError {
	var errorResp VoyageError
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)
	if bifrostErr.Error == nil {
		bifrostErr.Error = &schemas.ErrorField{}
	}
	if message := voyageErrorMessage(errorResp); message != "" {
		bifrostErr.Error.Message = message
	}
	return bifrostErr
}

// voyageErrorMessage returns the text of the detail field: the string itself, or the raw JSON when
// the detail is not a string.
func voyageErrorMessage(errorResp VoyageError) string {
	raw := strings.TrimSpace(string(errorResp.Detail))
	if raw == "" || raw == "null" {
		return ""
	}
	var text string
	if err := schemas.Unmarshal(errorResp.Detail, &text); err == nil {
		return text
	}
	return raw
}

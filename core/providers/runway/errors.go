package runway

import (
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// parseRunwayError parses Runway API error responses and converts them to BifrostError.
func parseRunwayError(resp *fasthttp.Response, meta *providerUtils.RequestMetadata) *schemas.BifrostError {
	// Parse as RunwayAPIError
	var errorResp RunwayAPIError
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)

	// Set error message if available
	if errorResp.Error != "" {
		if bifrostErr.Error == nil {
			bifrostErr.Error = &schemas.ErrorField{}
		}
		bifrostErr.Error.Message = errorResp.Error
	} else if bifrostErr.Error != nil && bifrostErr.Error.Message == "" {
		// If no error message was extracted, use a generic one
		bifrostErr.Error.Message = "Runway API request failed"
	} else if bifrostErr.Error == nil {
		bifrostErr.Error = &schemas.ErrorField{
			Message: "Runway API request failed",
		}
	}

	// Remove trailing newlines
	if bifrostErr.Error != nil && bifrostErr.Error.Message != "" {
		bifrostErr.Error.Message = strings.TrimRight(bifrostErr.Error.Message, "\n")
	}

	// Set metadata
	if meta != nil {
		bifrostErr.ExtraFields.Provider = meta.Provider
		bifrostErr.ExtraFields.ModelRequested = meta.Model
		bifrostErr.ExtraFields.RequestType = meta.RequestType
	}

	return bifrostErr
}

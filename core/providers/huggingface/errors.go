package huggingface

import (
	"fmt"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// parseHuggingFaceImageError parses HuggingFace error responses
func parseHuggingFaceImageError(resp *fasthttp.Response, meta *providerUtils.RequestMetadata) *schemas.BifrostError {
	var errorResp HuggingFaceResponseError
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)

	if strings.TrimSpace(errorResp.Type) != "" {
		typeCopy := errorResp.Type
		bifrostErr.Type = &typeCopy
	}

	if bifrostErr.Error == nil {
		bifrostErr.Error = &schemas.ErrorField{}
	}

	// Handle FastAPI validation errors
	if len(errorResp.Detail) > 0 {
		var errorMessages []string
		for _, detail := range errorResp.Detail {
			msg := detail.Msg
			if len(detail.Loc) > 0 {
				// Build location string from loc array
				var locParts []string
				for _, locPart := range detail.Loc {
					if locStr, ok := locPart.(string); ok {
						locParts = append(locParts, locStr)
					} else if locNum, ok := locPart.(float64); ok {
						locParts = append(locParts, fmt.Sprintf("%.0f", locNum))
					}
				}
				if len(locParts) > 0 {
					msg = fmt.Sprintf("%s at %s", msg, strings.Join(locParts, "."))
				}
			}
			errorMessages = append(errorMessages, msg)
		}
		if len(errorMessages) > 0 {
			bifrostErr.Error.Message = strings.Join(errorMessages, "; ")
		}
	} else if strings.TrimSpace(errorResp.Message) != "" {
		bifrostErr.Error.Message = errorResp.Message
	} else if strings.TrimSpace(errorResp.Error) != "" {
		bifrostErr.Error.Message = errorResp.Error
	}

	if meta != nil {
		bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
			Provider:       meta.Provider,
			ModelRequested: meta.Model,
			RequestType:    meta.RequestType,
		}
	}

	return bifrostErr
}

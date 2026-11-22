package crowllm

import (
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// parseCrowLLMImageError parses CrowLLM error responses
func parseCrowLLMImageError(resp *fasthttp.Response) *schemas.BifrostError {
	var crowllmErr CrowLLMError
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &crowllmErr)

	if bifrostErr.Error == nil {
		bifrostErr.Error = &schemas.ErrorField{}
	}

	// Extract error message
	var message string
	if crowllmErr.Error != nil && crowllmErr.Error.Message != nil {
		message = *crowllmErr.Error.Message
	}
	if crowllmErr.Detail != nil {
		if crowllmErr.Detail.Message != nil {
			message = *crowllmErr.Detail.Message
		}

		if len(crowllmErr.Detail.ValidationErrors) > 0 {
			var messages []string
			var locations []string

			if message != "" {
				messages = append(messages, message)
			}

			for _, detail := range crowllmErr.Detail.ValidationErrors {
				if detail.Msg != "" {
					messages = append(messages, detail.Msg)
				}
				if len(detail.Loc) > 0 {
					locations = append(locations, strings.Join(detail.Loc, "."))
				}
			}

			if len(messages) > 0 {
				message = strings.Join(messages, "; ")
			}
			if len(locations) > 0 {
				locationStr := strings.Join(locations, ", ")
				if message == "" {
					message = "[" + locationStr + "]"
				} else {
					message = message + " [" + locationStr + "]"
				}
			}
		}
	}

	// Use the extracted message if available
	if message != "" {
		bifrostErr.Error.Message = message
	}

	return bifrostErr
}

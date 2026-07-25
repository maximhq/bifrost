package munsit

import (
	"strings"
	"strconv"
	"github.com/valyala/fasthttp"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

func parseMunsitError(resp *fasthttp.Response) *schemas.BifrostError {
	var errorResp MunsitError
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)
	if errorResp.Detail != nil {
		var message string
		// Handle validation errors (array format)
		if len(errorResp.Detail.ValidationErrors) > 0 {
			var messages []string
			var locations []string
			var errorTypes []string

			for _, validationErr := range errorResp.Detail.ValidationErrors {
				// Get message from either Message or Msg field
				msg := validationErr.Message
				if msg == "" {
					msg = validationErr.Msg
				}
				if msg != "" {
					messages = append(messages, msg)
				}

				// Collect location if available
				if len(validationErr.Loc) > 0 {
					locations = append(locations, strings.Join(validationErr.Loc, "."))
				}

				// Collect error type if available
				if validationErr.Type != "" {
					errorTypes = append(errorTypes, validationErr.Type)
				}
			}

			// Build combined message
			if len(messages) > 0 {
				message = strings.Join(messages, "; ")
			}
			if len(locations) > 0 {
				locationStr := strings.Join(locations, ", ")
				message = message + " [" + locationStr + "]"
			}

			errorType := ""
			if len(errorTypes) > 0 {
				errorType = strings.Join(errorTypes, ", ")
			}

			if message != "" {
				result := &schemas.BifrostError{
					IsBifrostError: false,
					StatusCode:     schemas.Ptr(resp.StatusCode()),
					Error: &schemas.ErrorField{
						Type:    schemas.Ptr(errorType),
						Message: message,
					},
				}
				return result
			}
		}

		// Handle non-validation errors (single object format)
		if errorResp.Detail.Message != nil {
			message = *errorResp.Detail.Message
		}

		errorType := ""
		if errorResp.Detail.Status != nil {
			errorType = *errorResp.Detail.Status
		}

		if message != "" {
			if bifrostErr.Error == nil {
				bifrostErr.Error = &schemas.ErrorField{}
			}
			bifrostErr.Error.Type = schemas.Ptr(errorType)
			bifrostErr.Error.Message = message
		}
	}

	if bifrostErr != nil && bifrostErr.Error != nil && bifrostErr.Error.Message == "" {
		rawBody := strings.TrimSpace(string(resp.Body()))
		if rawBody != "" {
			bifrostErr.Error.Message = rawBody
		} else {
			bifrostErr.Error.Message = "munsit API returned status " + strconv.Itoa(resp.StatusCode()) + " with no error details"
		}
	}
	return bifrostErr
}

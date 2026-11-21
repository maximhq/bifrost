package elevenlabs

import (
	"strings"

	"github.com/valyala/fasthttp"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

func parseElevenlabsError(resp *fasthttp.Response) *schemas.BifrostError {
	var errorResp ElevenlabsError
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
					ExtraFields: schemas.BifrostErrorExtraFields{
						RetryAfter: bifrostErr.ExtraFields.RetryAfter,
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
	return bifrostErr
}

// ToElevenlabsError renders a failure of the gateway the way ElevenLabs writes its own, as
// {"detail":{"status","message"}}, so an ElevenLabs SDK parses it. The status is the Bifrost error's own
// (a governance decision such as "model_blocked"), else what its HTTP status stands for.
func ToElevenlabsError(bifrostErr *schemas.BifrostError) *ElevenlabsError {
	if bifrostErr == nil {
		return nil
	}
	var status, message string
	if bifrostErr.Type != nil {
		status = *bifrostErr.Type
	}
	if bifrostErr.Error != nil {
		message = bifrostErr.Error.Message
		if status == "" && bifrostErr.Error.Type != nil {
			status = *bifrostErr.Error.Type
		}
		if status == "" && bifrostErr.Error.Code != nil {
			status = *bifrostErr.Error.Code
		}
	}
	if status == "" {
		switch bifrostErr.EffectiveHTTPStatus() {
		case fasthttp.StatusBadRequest:
			status = "invalid_request"
		case fasthttp.StatusUnauthorized:
			status = "invalid_api_key"
		case fasthttp.StatusPaymentRequired:
			status = "quota_exceeded"
		case fasthttp.StatusNotFound:
			status = "not_found"
		case fasthttp.StatusTooManyRequests:
			status = "rate_limited"
		default:
			status = "server_error"
		}
	}
	return &ElevenlabsError{Detail: &ElevenlabsErrorDetail{Status: &status, Message: &message}}
}

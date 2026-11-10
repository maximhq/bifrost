package soniox

import (
	"fmt"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// parseSonioxError converts a non-2xx Soniox response into a BifrostError, preferring
// Soniox's structured message and type, with validation failures appended.
func parseSonioxError(resp *fasthttp.Response) *schemas.BifrostError {
	var errorResp SonioxError
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)

	message := errorResp.Message
	if message == "" {
		message = errorResp.ErrorMessage
	}
	if len(errorResp.ValidationErrors) > 0 {
		details := make([]string, 0, len(errorResp.ValidationErrors))
		for _, validationErr := range errorResp.ValidationErrors {
			details = append(details, validationErr.Location+": "+validationErr.Message)
		}
		message = strings.TrimSpace(message + " " + strings.Join(details, "; "))
	}
	if message == "" {
		return bifrostErr
	}
	if bifrostErr.Error == nil {
		bifrostErr.Error = &schemas.ErrorField{}
	}
	bifrostErr.Error.Message = message
	if errorResp.ErrorType != "" {
		bifrostErr.Error.Type = new(errorResp.ErrorType)
	}
	return bifrostErr
}

// parseSonioxJobError converts a finished transcription job with status "error" into a
// BifrostError. Soniox reports these with HTTP 200, so the job body carries the failure.
func parseSonioxJobError(job *SonioxTranscription) *schemas.BifrostError {
	statusCode := fasthttp.StatusBadGateway
	errorType := ""
	if job.ErrorType != nil {
		errorType = *job.ErrorType
	}
	if errorType == "invalid_audio_file" {
		statusCode = fasthttp.StatusBadRequest
	}
	message := "soniox transcription failed"
	if job.ErrorMessage != nil && *job.ErrorMessage != "" {
		message = *job.ErrorMessage
	}
	bifrostErr := &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     &statusCode,
		Error: &schemas.ErrorField{
			Message: fmt.Sprintf("%s (transcription %s)", message, job.ID),
		},
	}
	if errorType != "" {
		bifrostErr.Error.Type = &errorType
	}
	return bifrostErr
}

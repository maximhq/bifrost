package openai

import (
	"fmt"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// ErrorConverter is a function that converts provider-specific error responses to BifrostError.
type ErrorConverter func(resp *fasthttp.Response) *schemas.BifrostError

// responsesStreamError converts either Responses API error event shape into
// Bifrost's provider-error envelope. Responses-compatible services can report
// an error after the HTTP headers have been committed, so the transport status
// remains 200 and the semantic failure must be carried by the SSE event.
//
// The wire protocol has two shapes in use:
//   - {"type":"error","error":{"type":...,"code":...,"message":...}}
//   - {"type":"response.failed","response":{"error":{"code":...,"message":...}}}
//
// Keep the normalization here so Azure, OpenAI, and other compatible providers
// share one behavior instead of growing provider-specific stream parsers.
func responsesStreamError(response *schemas.BifrostResponsesStreamResponse) *schemas.BifrostError {
	eventType := string(response.Type)
	bifrostErr := &schemas.BifrostError{
		Type:           schemas.Ptr(eventType),
		IsBifrostError: false,
		Error:          &schemas.ErrorField{},
	}

	// Preserve the legacy top-level fields first. The nested error object is
	// preferred only when the legacy field was absent, which keeps this helper
	// compatible with providers that emit both forms.
	if response.Message != nil {
		bifrostErr.Error.Message = *response.Message
	}
	if response.Param != nil {
		bifrostErr.Error.Param = *response.Param
	}
	if response.Code != nil {
		bifrostErr.Error.Code = response.Code
	}

	if response.Error != nil {
		if response.Error.Type != "" {
			bifrostErr.Error.Type = schemas.Ptr(response.Error.Type)
		}
		if response.Error.Message != "" && bifrostErr.Error.Message == "" {
			bifrostErr.Error.Message = response.Error.Message
		}
		if response.Error.Code != "" && (bifrostErr.Error.Code == nil || *bifrostErr.Error.Code == "") {
			bifrostErr.Error.Code = schemas.Ptr(response.Error.Code)
		}
	}

	if response.Response != nil && response.Response.Error != nil {
		if response.Response.Error.Type != "" && bifrostErr.Error.Type == nil {
			bifrostErr.Error.Type = schemas.Ptr(response.Response.Error.Type)
		}
		if response.Response.Error.Message != "" && bifrostErr.Error.Message == "" {
			bifrostErr.Error.Message = response.Response.Error.Message
		}
		if response.Response.Error.Code != "" && (bifrostErr.Error.Code == nil || *bifrostErr.Error.Code == "") {
			bifrostErr.Error.Code = schemas.Ptr(response.Response.Error.Code)
		}
	}

	if bifrostErr.Error.Message == "" {
		details := eventType
		if bifrostErr.Error.Type != nil && *bifrostErr.Error.Type != "" && *bifrostErr.Error.Type != eventType {
			details += ", type=" + *bifrostErr.Error.Type
		}
		if bifrostErr.Error.Code != nil && *bifrostErr.Error.Code != "" {
			details += ", code=" + *bifrostErr.Error.Code
		}
		bifrostErr.Error.Message = fmt.Sprintf("provider stream error (%s)", details)
	}

	return bifrostErr
}

// ParseOpenAIError parses OpenAI error responses.
func ParseOpenAIError(resp *fasthttp.Response) *schemas.BifrostError {
	var errorResp schemas.BifrostError

	bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)

	if errorResp.EventID != nil {
		bifrostErr.EventID = errorResp.EventID
	}

	if errorResp.Error != nil {
		if bifrostErr.Error == nil {
			bifrostErr.Error = &schemas.ErrorField{}
		}
		bifrostErr.Error.Type = errorResp.Error.Type
		bifrostErr.Error.Code = errorResp.Error.Code
		if errorResp.Error.Message != "" {
			bifrostErr.Error.Message = errorResp.Error.Message
		}
		bifrostErr.Error.Param = errorResp.Error.Param
		if errorResp.Error.EventID != nil {
			bifrostErr.Error.EventID = errorResp.Error.EventID
		}
	}

	if bifrostErr.Error == nil {
		bifrostErr.Error = &schemas.ErrorField{}
	}
	if strings.TrimSpace(bifrostErr.Error.Message) == "" {
		if bifrostErr.StatusCode != nil {
			bifrostErr.Error.Message = fmt.Sprintf("provider API error (status %d)", *bifrostErr.StatusCode)
		} else {
			bifrostErr.Error.Message = "provider API error"
		}
	}

	// Set ExtraFields unconditionally so provider/model/request metadata is always attached

	return bifrostErr
}

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

// responsesStreamErrorCodeStatus maps OpenAI's Responses API ResponseErrorCode
// enum (confirmed against the vendored OpenAI OpenAPI spec,
// gen/schema-compatability/openai/openapi.yaml:67250) to the HTTP status
// OpenAI would have used had the failure occurred pre-stream instead of via a
// mid-stream `error`/`response.failed` SSE event, which carries no HTTP status
// of its own. insufficient_quota is not part of that schema-enumerated set
// (it's a documented convention, not a schema value) but is included here
// since it's OpenAI's real, empirically observed code for budget exhaustion.
var responsesStreamErrorCodeStatus = map[string]int{
	schemas.ErrorTypeRateLimitExceeded: fasthttp.StatusTooManyRequests,
	schemas.ErrorTypeInsufficientQuota: fasthttp.StatusTooManyRequests,
	"invalid_prompt":                   fasthttp.StatusBadRequest,
	"invalid_image":                    fasthttp.StatusBadRequest,
	"invalid_image_format":             fasthttp.StatusBadRequest,
	"invalid_base64_image":             fasthttp.StatusBadRequest,
	"invalid_image_url":                fasthttp.StatusBadRequest,
	"image_too_large":                  fasthttp.StatusBadRequest,
	"image_too_small":                  fasthttp.StatusBadRequest,
	"image_parse_error":                fasthttp.StatusBadRequest,
	"image_content_policy_violation":   fasthttp.StatusBadRequest,
	"invalid_image_mode":               fasthttp.StatusBadRequest,
	"image_file_too_large":             fasthttp.StatusBadRequest,
	"unsupported_image_media_type":     fasthttp.StatusBadRequest,
	"empty_image_file":                 fasthttp.StatusBadRequest,
	"failed_to_download_image":         fasthttp.StatusBadRequest,
	"image_file_not_found":             fasthttp.StatusNotFound,
	"vector_store_timeout":             fasthttp.StatusGatewayTimeout,
	schemas.ErrorTypeServerError:       fasthttp.StatusInternalServerError,
}

// StatusCodeForResponsesStreamErrorCode returns the canonical HTTP status for
// an OpenAI Responses API streaming error/response.failed event's error code,
// falling back to 500 for unrecognized or absent codes.
func StatusCodeForResponsesStreamErrorCode(code *string) int {
	if code == nil {
		return fasthttp.StatusInternalServerError
	}
	if status, ok := responsesStreamErrorCodeStatus[*code]; ok {
		return status
	}
	return fasthttp.StatusInternalServerError
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

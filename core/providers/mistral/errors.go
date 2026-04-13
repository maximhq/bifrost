package mistral

import (
	"fmt"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// MistralErrorResponse captures both Mistral's top-level error shape and nested OpenAI-style errors.
type MistralErrorResponse struct {
	Object  string              `json:"object,omitempty"`
	Message string              `json:"message,omitempty"`
	Type    string              `json:"type,omitempty"`
	Code    string              `json:"code,omitempty"`
	Error   *schemas.ErrorField `json:"error,omitempty"`
}

// ParseMistralError parses Mistral-specific error responses.
func ParseMistralError(resp *fasthttp.Response) *schemas.BifrostError {
	var errorResp MistralErrorResponse
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)
	if bifrostErr == nil {
		return nil
	}

	if bifrostErr.Error == nil {
		bifrostErr.Error = &schemas.ErrorField{}
	}

	if errorResp.Error != nil {
		if strings.TrimSpace(errorResp.Error.Message) != "" {
			bifrostErr.Error.Message = errorResp.Error.Message
		}
		if errorResp.Error.Type != nil && strings.TrimSpace(*errorResp.Error.Type) != "" {
			bifrostErr.Error.Type = errorResp.Error.Type
			bifrostErr.Type = errorResp.Error.Type
		}
		if errorResp.Error.Code != nil && strings.TrimSpace(*errorResp.Error.Code) != "" {
			bifrostErr.Error.Code = errorResp.Error.Code
		}
		bifrostErr.Error.Param = errorResp.Error.Param
		if errorResp.Error.EventID != nil {
			bifrostErr.Error.EventID = errorResp.Error.EventID
		}
	}

	if strings.TrimSpace(errorResp.Message) != "" {
		bifrostErr.Error.Message = errorResp.Message
	}
	if strings.TrimSpace(errorResp.Type) != "" {
		errorType := schemas.Ptr(errorResp.Type)
		bifrostErr.Error.Type = errorType
		bifrostErr.Type = errorType
	}
	if strings.TrimSpace(errorResp.Code) != "" {
		bifrostErr.Error.Code = schemas.Ptr(errorResp.Code)
	}

	if strings.TrimSpace(bifrostErr.Error.Message) == "" {
		if bifrostErr.StatusCode != nil {
			bifrostErr.Error.Message = fmt.Sprintf("provider API error (status %d)", *bifrostErr.StatusCode)
		} else {
			bifrostErr.Error.Message = "provider API error"
		}
	}

	return bifrostErr
}

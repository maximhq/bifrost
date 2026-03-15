package sapaicore

import (
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// ParseSAPAICoreError parses SAP AI Core error responses and converts them to BifrostError.
// SAP AI Core can return errors in different formats depending on the backend:
//   - OpenAI format:   {"error": {"message": "...", "type": "...", "code": "..."}}
//   - Platform format: {"message": "...", "code": "...", "status": "..."}
//   - Bedrock format:  {"message": "...", "__type": "ValidationException"}
func ParseSAPAICoreError(resp *fasthttp.Response, requestType schemas.RequestType, providerName schemas.ModelProvider, model string) *schemas.BifrostError {
	var errorResp sapaicoreErrorResponse

	bifrostErr := providerUtils.HandleProviderAPIError(resp, &errorResp)

	// Map parsed fields into bifrostErr
	if errorResp.EventID != nil {
		bifrostErr.EventID = errorResp.EventID
	}

	if errorResp.Error != nil {
		// OpenAI-envelope format: {"error": {"message": "...", ...}}
		if bifrostErr.Error == nil {
			bifrostErr.Error = &schemas.ErrorField{}
		}
		bifrostErr.Error.Type = errorResp.Error.Type
		bifrostErr.Error.Code = errorResp.Error.Code
		bifrostErr.Error.Message = errorResp.Error.Message
		bifrostErr.Error.Param = errorResp.Error.Param
		if errorResp.Error.EventID != nil {
			bifrostErr.Error.EventID = errorResp.Error.EventID
		}
	} else if errorResp.Message != "" {
		// Non-OpenAI format: top-level "message", "type", "code", "__type", "status"
		// This covers SAP AI Core platform errors and Bedrock errors.
		if bifrostErr.Error == nil {
			bifrostErr.Error = &schemas.ErrorField{}
		}
		bifrostErr.Error.Message = errorResp.Message

		// Resolve type: prefer explicit "type", fall back to Bedrock "__type"
		if errorResp.Type != nil {
			bifrostErr.Error.Type = errorResp.Type
		} else if errorResp.BedrockType != nil {
			bifrostErr.Error.Type = errorResp.BedrockType
		}

		// Map "code" or "status" into Code
		if errorResp.Code != nil {
			bifrostErr.Error.Code = errorResp.Code
		} else if errorResp.Status != nil {
			bifrostErr.Error.Code = errorResp.Status
		}
	}

	// Set ExtraFields unconditionally so provider/model/request metadata is always attached
	if bifrostErr != nil {
		bifrostErr.ExtraFields.Provider = providerName
		bifrostErr.ExtraFields.ModelRequested = model
		bifrostErr.ExtraFields.RequestType = requestType
	}

	return bifrostErr
}

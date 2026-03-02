package sapaicore

import (
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// ParseSAPAICoreError parses SAP AI Core error responses and converts them to BifrostError.
// SAP AI Core can return errors in different formats depending on the backend (OpenAI, Bedrock, Vertex).
func ParseSAPAICoreError(resp *fasthttp.Response, requestType schemas.RequestType, providerName schemas.ModelProvider, model string) *schemas.BifrostError {
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
		bifrostErr.Error.Message = errorResp.Error.Message
		bifrostErr.Error.Param = errorResp.Error.Param
		if errorResp.Error.EventID != nil {
			bifrostErr.Error.EventID = errorResp.Error.EventID
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

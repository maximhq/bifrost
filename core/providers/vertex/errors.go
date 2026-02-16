package vertex

import (
	"errors"
	"strings"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

func parseVertexError(resp *fasthttp.Response, meta *providerUtils.RequestMetadata) *schemas.BifrostError {
	var providerName schemas.ModelProvider
	if meta != nil {
		providerName = meta.Provider
	}

	var openAIErr schemas.BifrostError
	var vertexErr []VertexError

	decodedBody, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		bifrostErr := providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
		if meta != nil {
			if bifrostErr.ExtraFields == nil {
				bifrostErr.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
			}
			bifrostErr.ExtraFields.Provider = meta.Provider
			bifrostErr.ExtraFields.ModelRequested = meta.Model
			bifrostErr.ExtraFields.RequestType = meta.RequestType
		}
		return bifrostErr
	}

	// Check for empty response
	trimmed := strings.TrimSpace(string(decodedBody))
	if len(trimmed) == 0 {
		bifrostErr := &schemas.BifrostError{
			IsBifrostError: false,
			StatusCode:     schemas.Ptr(resp.StatusCode()),
			Error: &schemas.ErrorField{
				Message: schemas.ErrProviderResponseEmpty,
			},
		}
		if meta != nil {
			if bifrostErr.ExtraFields == nil {
				bifrostErr.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
			}
			bifrostErr.ExtraFields.Provider = meta.Provider
			bifrostErr.ExtraFields.ModelRequested = meta.Model
			bifrostErr.ExtraFields.RequestType = meta.RequestType
		}
		return bifrostErr
	}

	// Check for HTML error response before attempting JSON parsing
	if providerUtils.IsHTMLResponse(resp, decodedBody) {
		bifrostErr := &schemas.BifrostError{
			IsBifrostError: false,
			StatusCode:     schemas.Ptr(resp.StatusCode()),
			Error: &schemas.ErrorField{
				Message: schemas.ErrProviderResponseHTML,
				Error:   errors.New(string(decodedBody)),
			},
		}
		if meta != nil {
			if bifrostErr.ExtraFields == nil {
				bifrostErr.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
			}
			bifrostErr.ExtraFields.Provider = meta.Provider
			bifrostErr.ExtraFields.ModelRequested = meta.Model
			bifrostErr.ExtraFields.RequestType = meta.RequestType
		}
		return bifrostErr
	}

	createError := func(message string) *schemas.BifrostError {
		bifrostErr := providerUtils.NewProviderAPIError(message, nil, resp.StatusCode(), providerName, nil, nil)
		if meta != nil {
			if bifrostErr.ExtraFields == nil {
				bifrostErr.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
			}
			bifrostErr.ExtraFields.Provider = meta.Provider
			bifrostErr.ExtraFields.ModelRequested = meta.Model
			bifrostErr.ExtraFields.RequestType = meta.RequestType
		}
		return bifrostErr
	}

	if err := sonic.Unmarshal(decodedBody, &openAIErr); err != nil || openAIErr.Error == nil {
		// Try Vertex error format if OpenAI format fails or is incomplete
		if err := sonic.Unmarshal(decodedBody, &vertexErr); err != nil {
			//try with single Vertex error format
			var vertexErr VertexError
			if err := sonic.Unmarshal(decodedBody, &vertexErr); err != nil {
				// Try VertexValidationError format (validation errors from Mistral endpoint)
				var validationErr VertexValidationError
				if err := sonic.Unmarshal(decodedBody, &validationErr); err != nil {
					bifrostErr := providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, providerName)
					if meta != nil {
						if bifrostErr.ExtraFields == nil {
							bifrostErr.ExtraFields = schemas.AcquireBifrostErrorExtraFields()
						}
						bifrostErr.ExtraFields.Provider = meta.Provider
						bifrostErr.ExtraFields.ModelRequested = meta.Model
						bifrostErr.ExtraFields.RequestType = meta.RequestType
					}
					return bifrostErr
				}
				if len(validationErr.Detail) > 0 {
					return createError(validationErr.Detail[0].Msg)
				}
				return createError("Unknown error")
			}
			return createError(vertexErr.Error.Message)
		}
		if len(vertexErr) > 0 {
			return createError(vertexErr[0].Error.Message)
		}
		return createError("Unknown error")
	}
	// OpenAI error format succeeded with valid Error field
	return createError(openAIErr.Error.Message)
}

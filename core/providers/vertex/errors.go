package vertex

import (
	"errors"
	"strings"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

func parseVertexError(requestBody []byte, resp *fasthttp.Response) *schemas.BifrostError {
	var openAIErr schemas.BifrostError
	var vertexErr []VertexError

	var rawRequest interface{}
	if len(requestBody) > 0 {
		rawRequest = providerUtils.CompactRawJSON(requestBody)
	}

	decodedBody, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		bifrostErr := providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
		bifrostErr.ExtraFields.RawRequest = rawRequest
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
			ExtraFields: schemas.BifrostErrorExtraFields{
				RawRequest: rawRequest,
			},
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
			ExtraFields: schemas.BifrostErrorExtraFields{
				RawRequest:  rawRequest,
				RawResponse: string(decodedBody),
			},
		}
		return bifrostErr
	}

	createError := func(message string) *schemas.BifrostError {
		bifrostErr := providerUtils.NewProviderAPIError(message, nil, resp.StatusCode(), nil, nil)
		var rawResponse interface{}
		if err := sonic.Unmarshal(decodedBody, &rawResponse); err != nil {
			rawResponse = string(decodedBody)
		}
		bifrostErr.ExtraFields.RawRequest = rawRequest
		bifrostErr.ExtraFields.RawResponse = rawResponse
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
					bifrostErr := providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err)
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

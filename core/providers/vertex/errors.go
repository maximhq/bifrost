package vertex

import (
	"errors"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/gemini"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// parseVertexError converts a Vertex error response into a BifrostError, with the retry
// hint from a google.rpc.RetryInfo error detail or the response headers.
func parseVertexError(resp *fasthttp.Response) *schemas.BifrostError {
	bifrostErr := parseVertexErrorBody(resp)
	providerUtils.ApplyRetryAfter(bifrostErr, &resp.Header)
	return bifrostErr
}

// parseVertexErrorBody builds the error from the response body, with the retry hint when the
// body carries a google.rpc.RetryInfo detail.
func parseVertexErrorBody(resp *fasthttp.Response) *schemas.BifrostError {
	var openAIErr schemas.BifrostError
	var vertexErr []VertexError

	decodedBody, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		bifrostErr := providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
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
				RawResponse: string(decodedBody),
			},
		}
		return bifrostErr
	}

	createError := func(message, status string) *schemas.BifrostError {
		bifrostErr := providerUtils.NewProviderAPIError(message, nil, resp.StatusCode(), nil, nil)
		if status != "" {
			if bifrostErr.Error == nil {
				bifrostErr.Error = &schemas.ErrorField{}
			}
			bifrostErr.Error.Type = &status
		}
		var rawResponse interface{}
		if err := sonic.Unmarshal(decodedBody, &rawResponse); err != nil {
			rawResponse = string(decodedBody)
		}
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
					return createError(validationErr.Detail[0].Msg, "")
				}
				return createError("Unknown error", "")
			}
			bifrostErr := createError(vertexErr.Error.Message, vertexErr.Error.Status)
			gemini.ApplyRetryInfo(bifrostErr, vertexErr.Error.Details)
			return bifrostErr
		}
		if len(vertexErr) > 0 {
			bifrostErr := createError(vertexErr[0].Error.Message, vertexErr[0].Error.Status)
			gemini.ApplyRetryInfo(bifrostErr, vertexErr[0].Error.Details)
			return bifrostErr
		}
		return createError("Unknown error", "")
	}
	// OpenAI error format succeeded with valid Error field.
	openAIStatus := ""
	if openAIErr.Error.Type != nil {
		openAIStatus = *openAIErr.Error.Type
	}
	var single VertexError
	if openAIStatus == "" {
		if err := sonic.Unmarshal(decodedBody, &single); err == nil {
			openAIStatus = single.Error.Status
		}
	}
	bifrostErr := createError(openAIErr.Error.Message, openAIStatus)
	gemini.ApplyRetryInfo(bifrostErr, single.Error.Details)
	return bifrostErr
}

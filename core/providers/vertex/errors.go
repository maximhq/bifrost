package vertex

import (
	"errors"
	"strings"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

func parseVertexError(resp *fasthttp.Response) *schemas.BifrostError {
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
		if violations := extractVertexFieldViolations(decodedBody); violations != "" {
			if message == "" {
				message = violations
			} else {
				message = message + " (" + violations + ")"
			}
		}
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
			return createError(vertexErr.Error.Message, vertexErr.Error.Status)
		}
		if len(vertexErr) > 0 {
			return createError(vertexErr[0].Error.Message, vertexErr[0].Error.Status)
		}
		return createError("Unknown error", "")
	}
	// OpenAI error format succeeded with valid Error field.
	openAIStatus := ""
	if openAIErr.Error.Type != nil {
		openAIStatus = *openAIErr.Error.Type
	}
	if openAIStatus == "" {
		var single VertexError
		if err := sonic.Unmarshal(decodedBody, &single); err == nil {
			openAIStatus = single.Error.Status
		}
	}
	return createError(openAIErr.Error.Message, openAIStatus)
}

// vertexErrorDetailsEnvelope decodes just the error.details payloads of a Vertex
// error body, independent of which error shape the main parse matched.
type vertexErrorDetailsEnvelope struct {
	Error struct {
		Details []struct {
			Type            string `json:"@type"`
			FieldViolations []struct {
				Field       string `json:"field"`
				Description string `json:"description"`
			} `json:"fieldViolations"`
		} `json:"details"`
	} `json:"error"`
}

// extractVertexFieldViolations renders google.rpc.BadRequest fieldViolations from
// error.details as "field: description" pairs. Vertex 400 INVALID_ARGUMENT bodies
// name the rejected field there, and forwarding only error.message left callers
// with a bare "Request contains an invalid argument." (issue #6589).
func extractVertexFieldViolations(body []byte) string {
	var envelope vertexErrorDetailsEnvelope
	if err := sonic.Unmarshal(body, &envelope); err != nil {
		// Vertex also returns array-shaped error bodies; use the first entry.
		var envelopes []vertexErrorDetailsEnvelope
		if err := sonic.Unmarshal(body, &envelopes); err != nil || len(envelopes) == 0 {
			return ""
		}
		envelope = envelopes[0]
	}
	var parts []string
	for _, detail := range envelope.Error.Details {
		if !strings.HasSuffix(detail.Type, "google.rpc.BadRequest") {
			continue
		}
		for _, violation := range detail.FieldViolations {
			switch {
			case violation.Field != "" && violation.Description != "":
				parts = append(parts, violation.Field+": "+violation.Description)
			case violation.Field != "":
				parts = append(parts, violation.Field)
			case violation.Description != "":
				parts = append(parts, violation.Description)
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "violations: " + strings.Join(parts, ", ")
}

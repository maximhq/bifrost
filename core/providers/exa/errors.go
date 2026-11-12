package exa

import (
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// exaErrorEnvelope is the body Exa returns on a failed request:
// {"requestId": "...", "error": "...", "tag": "..."}. The shared OpenAI-compatible parser
// expects an object-valued error, so it would keep only the raw JSON and lose the reason, the
// tag and the request id an operator quotes to Exa support.
type exaErrorEnvelope struct {
	RequestID string `json:"requestId,omitempty"`
	Error     string `json:"error,omitempty"`
	Tag       string `json:"tag,omitempty"`
}

// parseExaError maps Exa's error envelope onto a BifrostError: the HTTP status survives, the
// string error becomes the message, Exa's tag becomes the error type, and its request id is
// kept. A response without the envelope (an empty body, or a 429 with a simpler body) keeps
// the shared parser's status description and raw body.
func parseExaError(resp *fasthttp.Response) *schemas.BifrostError {
	var envelope exaErrorEnvelope
	bifrostErr := providerUtils.HandleProviderAPIError(resp, &envelope)
	if bifrostErr.Error == nil {
		bifrostErr.Error = &schemas.ErrorField{}
	}
	if envelope.Error != "" {
		bifrostErr.Error.Message = envelope.Error
	}
	if envelope.Tag != "" {
		tag := envelope.Tag
		bifrostErr.Type = &tag
		bifrostErr.Error.Type = &tag
	}
	if envelope.RequestID != "" {
		requestID := envelope.RequestID
		bifrostErr.EventID = &requestID
		bifrostErr.Error.EventID = &requestID
	}
	return bifrostErr
}

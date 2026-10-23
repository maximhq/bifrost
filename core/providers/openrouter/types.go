package openrouter

import (
	"github.com/maximhq/bifrost/core/providers/typesafe"
	"github.com/maximhq/bifrost/core/schemas"
)

// OpenRouterDecisionRequest is the body of POST /alpha/decisions: Typesafe's
// systemone request, with images sent in the state (see ToOpenRouterDecisionRequest),
// plus OpenRouter's end-user id. It is only ever encoded; decoding it would run
// Typesafe's UnmarshalJSON on the embedded pointer and fail.
type OpenRouterDecisionRequest struct {
	*typesafe.TypesafeDecisionRequest
	User *string `json:"user,omitempty"` // from SafetyIdentifier
}

// OpenRouterDecisionUsage is Typesafe's usage plus the billed cost in USD.
type OpenRouterDecisionUsage struct {
	typesafe.TypesafeUsage
	Cost *float64 `json:"cost,omitempty"`
}

// OpenRouterDecisionResponse is the native decisions response shape.
type OpenRouterDecisionResponse struct {
	ID       string                             `json:"id,omitempty"`
	Provider string                             `json:"provider,omitempty"`
	Model    string                             `json:"model"`
	Answers  map[string]typesafe.TypesafeAnswer `json:"answers"`
	Usage    *OpenRouterDecisionUsage           `json:"usage,omitempty"`
}

// OpenRouterBatchRequest keeps all batch-level fields before Requests: OpenRouter
// parses this envelope as a stream and rejects metadata following the request array.
type OpenRouterBatchRequest struct {
	Endpoint         schemas.BatchEndpoint      `json:"endpoint"`
	Model            string                     `json:"model"`
	Provider         map[string]any             `json:"provider,omitempty"`
	CompletionWindow string                     `json:"completion_window"`
	CallbackURL      string                     `json:"callback_url,omitempty"`
	Requests         []schemas.BatchRequestItem `json:"requests"`
}

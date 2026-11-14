package openrouter

import (
	"github.com/maximhq/bifrost/core/providers/typesafe"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// openRouterDecisionPath is OpenRouter's native decisions endpoint. It speaks
// Typesafe's systemone request and answer shapes.
const openRouterDecisionPath = "/alpha/decisions"

// openRouterDecisionModelsPath lists the models served on the decisions endpoint.
// OpenRouter's default /v1/models omits them, so ListModels merges them in.
const openRouterDecisionModelsPath = "/v1/models?output_modalities=decisions"

// ToOpenRouterDecisionRequest converts a normalized decision request into
// OpenRouter's body. Questions go through Typesafe's converter, so they
// validate exactly as they do on Typesafe. An input with images is sent as
// plain-string text items and image_url items in their original order, the
// layout OpenRouter documents for multimodal decisions (see
// providerUtils.ExtractDecisionImages). The request is not modified.
func ToOpenRouterDecisionRequest(request *schemas.BifrostDecisionRequest) (*OpenRouterDecisionRequest, error) {
	input, state, err := providerUtils.ExtractDecisionImages(request.Input)
	if err != nil {
		return nil, err
	}
	textOnly := *request
	textOnly.Input = input
	native, err := typesafe.ToTypesafeDecisionRequest(&textOnly)
	if err != nil {
		return nil, err
	}
	if state != nil {
		native.State = state
	}
	return &OpenRouterDecisionRequest{TypesafeDecisionRequest: native, User: request.SafetyIdentifier}, nil
}

// ToBifrostDecisionResponse converts a native decisions response into
// Bifrost's decision shape. The billed cost is carried on usage so cost
// tracking uses OpenRouter's figure instead of a datasheet estimate.
func ToBifrostDecisionResponse(resp *OpenRouterDecisionResponse, request *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
	native := &typesafe.TypesafeDecisionResponse{Model: resp.Model, Answers: resp.Answers}
	if resp.Usage != nil {
		native.Usage = &resp.Usage.TypesafeUsage
	}
	bifrostResp, bifrostErr := typesafe.ToBifrostDecisionResponse(native, request)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	bifrostResp.ID = resp.ID
	if resp.Usage != nil && resp.Usage.Cost != nil && *resp.Usage.Cost > 0 {
		bifrostResp.Usage.Cost = &schemas.BifrostCost{TotalCost: *resp.Usage.Cost}
	}
	return bifrostResp, nil
}

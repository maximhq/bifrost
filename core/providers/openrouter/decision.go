package openrouter

import (
	"github.com/maximhq/bifrost/core/providers/typesafe"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// openRouterDecisionPath is OpenRouter's native decisions endpoint. It speaks
// Typesafe's systemone request and answer shapes.
const openRouterDecisionPath = "/alpha/decisions"

// openRouterDecisionModels are the System One models served on the decisions
// endpoint. OpenRouter's /v1/models omits them, so ListModels merges them in.
var openRouterDecisionModels = []schemas.Model{
	{ID: "typesafe/jev-1.13", Name: schemas.Ptr("TypeSafe: Jev 1.13"), OwnedBy: schemas.Ptr("typesafe")},
	{ID: "~typesafe/jev-latest", Name: schemas.Ptr("TypeSafe: Jev (latest)"), OwnedBy: schemas.Ptr("typesafe")},
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

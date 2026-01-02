package vertex

import (
	"github.com/maximhq/bifrost/core/schemas"
)

func (resp *VertexCountTokensResponse) ToBifrostCountTokensResponse(model string) *schemas.BifrostCountTokensResponse {
	if resp == nil {
		return nil
	}

	inputDetails := &schemas.ResponsesResponseInputTokens{}
	inputTokens := int(resp.TotalTokens) // Vertex response typically represents prompt tokens for countTokens
	total := int(resp.TotalTokens)

	if resp.CachedContentTokenCount > 0 {
		inputDetails.CachedTokens = int(resp.CachedContentTokenCount)
	}

	return &schemas.BifrostCountTokensResponse{
		Model:              model,
		Object:             "response.input_tokens",
		InputTokens:        inputTokens,
		InputTokensDetails: inputDetails,
		TotalTokens:        &total,
	}
}

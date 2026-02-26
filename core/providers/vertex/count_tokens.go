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

	r := schemas.AcquireBifrostCountTokensResponse()
	r.Model = model
	r.Object = "response.input_tokens"
	r.InputTokens = inputTokens
	r.InputTokensDetails = inputDetails
	r.TotalTokens = &total
	return r
}

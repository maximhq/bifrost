package bedrock

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// ToBifrostCountTokensResponse converts a Bedrock count tokens response to Bifrost format
func (resp *BedrockCountTokensResponse) ToBifrostCountTokensResponse(model string) *schemas.BifrostCountTokensResponse {
	if resp == nil {
		return nil
	}

	totalTokens := resp.InputTokens

	return &schemas.BifrostCountTokensResponse{
		Model:       model,
		InputTokens: resp.InputTokens,
		TotalTokens: &totalTokens,
		Object:      "response.input_tokens",
	}
}

// ToBedrockCountTokensResponse converts a Bifrost count tokens response to Bedrock native format
func ToBedrockCountTokensResponse(resp *schemas.BifrostCountTokensResponse) *BedrockCountTokensResponse {
	if resp == nil {
		return nil
	}

	return &BedrockCountTokensResponse{
		InputTokens: resp.InputTokens,
	}
}

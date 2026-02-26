package anthropic

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// ToBifrostCountTokensResponse converts an Anthropic count tokens response to Bifrost format
func (resp *AnthropicCountTokensResponse) ToBifrostCountTokensResponse(model string) *schemas.BifrostCountTokensResponse {
	if resp == nil {
		return nil
	}

	totalTokens := resp.InputTokens

	bifrostResp := schemas.AcquireBifrostCountTokensResponse()
	bifrostResp.Model = model
	bifrostResp.InputTokens = resp.InputTokens
	bifrostResp.TotalTokens = &totalTokens
	bifrostResp.Object = "response.input_tokens"

	return bifrostResp
}

// ToAnthropicCountTokensResponse converts a Bifrost count tokens response to Anthropic format.
func ToAnthropicCountTokensResponse(bifrostResp *schemas.BifrostCountTokensResponse) *AnthropicCountTokensResponse {
	if bifrostResp == nil {
		return nil
	}

	return &AnthropicCountTokensResponse{
		InputTokens: bifrostResp.InputTokens,
	}
}

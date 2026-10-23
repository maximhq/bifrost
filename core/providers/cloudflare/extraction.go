package cloudflare

import (
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// ToCloudflareExtractionRequest converts a normalized extraction request into Clairic's body:
// the input becomes the state, and the model is sent under its bare name, as in the URL. Extra
// params ride along for the passthrough-extra-params flag only. The request is not modified.
func ToCloudflareExtractionRequest(request *schemas.BifrostExtractionRequest) *CloudflareExtractionRequest {
	native := &CloudflareExtractionRequest{
		Model:       cloudflareModelName(request.Model),
		State:       request.Input,
		Fields:      request.Fields,
		Images:      request.Images,
		ExtraParams: request.ExtraParams,
	}
	if params := request.Params; params != nil {
		native.ReferenceDate = params.ReferenceDate
		if params.IncludeQuantiles != nil || params.ReturnStateText != nil {
			native.Options = &CloudflareExtractionOptions{
				IncludeQuantiles: params.IncludeQuantiles,
				ReturnStateText:  params.ReturnStateText,
			}
		}
	}
	return native
}

// ToBifrostExtractionResponse converts Clairic's result into the normalized response. Input
// tokens, which already include the image and schema tokens, are billed as prompt tokens.
func (result *CloudflareExtractionResult) ToBifrostExtractionResponse() *schemas.BifrostExtractionResponse {
	response := &schemas.BifrostExtractionResponse{
		Model:     result.Model,
		Answers:   result.Answers,
		Truncated: result.Truncated,
		StateText: result.StateText,
	}
	if response.Answers == nil {
		response.Answers = map[string]schemas.ExtractionAnswer{}
	}
	if usage := result.Usage; usage != nil {
		response.Usage = &schemas.BifrostLLMUsage{
			PromptTokens:     usage.InputTokens,
			CompletionTokens: usage.OutputTokens,
			TotalTokens:      usage.InputTokens + usage.OutputTokens,
			StateTokens:      schemas.Ptr(usage.StateTokens),
		}
		if result.Truncated {
			response.Usage.Truncated = schemas.Ptr(true)
		}
	}
	return response
}

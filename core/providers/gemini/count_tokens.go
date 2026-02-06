package gemini

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// ToBifrostCountTokensResponse converts a Gemini count tokens response to Bifrost format.
func (resp *GeminiCountTokensResponse) ToBifrostCountTokensResponse(model string) *schemas.BifrostCountTokensResponse {
	if resp == nil {
		return nil
	}

	// Sum prompt tokens and map modality-specific counts
	inputTokens := 0
	inputDetails := &schemas.ResponsesResponseInputTokens{}

	for _, m := range resp.PromptTokensDetails {
		if m == nil {
			continue
		}
		inputTokens += int(m.TokenCount)
		mod := strings.ToLower(string(m.Modality))
		// handle audio modality
		if strings.Contains(mod, "audio") {
			inputDetails.AudioTokens += int(m.TokenCount)
		}
	}

	// Set cached tokens from top-level field if present
	if resp.CachedContentTokenCount != 0 {
		inputDetails.CachedTokens = int(resp.CachedContentTokenCount)
	} else if resp.CacheTokensDetails != nil {
		// If cache tokens details present, sum them
		cachedSum := 0
		for _, m := range resp.CacheTokensDetails {
			if m == nil {
				continue
			}
			cachedSum += int(m.TokenCount)
			if strings.Contains(strings.ToLower(string(m.Modality)), "audio") {
				// also populate audio tokens from cache into AudioTokens (additive)
				inputDetails.AudioTokens += int(m.TokenCount)
			}
		}
		inputDetails.CachedTokens = cachedSum
	}

	total := int(resp.TotalTokens)

	return &schemas.BifrostCountTokensResponse{
		Model:              model,
		Object:             "response.input_tokens",
		InputTokens:        inputTokens,
		InputTokensDetails: inputDetails,
		TotalTokens:        &total,
		ExtraFields:        schemas.BifrostResponseExtraFields{},
	}
}

// ToGeminiCountTokensResponse converts a Bifrost count tokens response to Gemini format.
func ToGeminiCountTokensResponse(bifrostResp *schemas.BifrostCountTokensResponse) *GeminiCountTokensResponse {
	if bifrostResp == nil {
		return nil
	}

	response := &GeminiCountTokensResponse{
		TotalTokens: int32(bifrostResp.InputTokens),
	}

	// Map cached content token count if available
	if bifrostResp.InputTokensDetails != nil && bifrostResp.InputTokensDetails.CachedTokens > 0 {
		response.CachedContentTokenCount = int32(bifrostResp.InputTokensDetails.CachedTokens)
	} else {
		response.CachedContentTokenCount = 0
	}

	return response
}

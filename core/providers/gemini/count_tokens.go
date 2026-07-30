package gemini

import (
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// countTokensEnvelopeField is the only place the Gemini countTokens endpoint reads
// systemInstruction, tools, toolConfig and generationConfig from. They are rejected
// at the top level, and a top-level contents/model is silently ignored once this is
// set, so the body must carry the envelope and nothing beside it.
const countTokensEnvelopeField = "generateContentRequest"

// geminiCountTokensUnsupportedFields lists fields absent from GenerateContentRequest,
// which the endpoint therefore rejects even inside the envelope.
var geminiCountTokensUnsupportedFields = []string{
	"labels",
	"fallbacks",
}

// wrapGeminiCountTokensBody rewrites a generateContent body into the countTokens
// envelope so the whole prompt is counted, not just contents. Bodies that already
// arrive wrapped (the shape Gemini documents) are reduced to the envelope alone.
func wrapGeminiCountTokensBody(jsonData []byte, model string) []byte {
	if len(jsonData) == 0 {
		return jsonData
	}

	inner := jsonData
	if envelope := providerUtils.GetJSONField(jsonData, countTokensEnvelopeField); envelope.Exists() {
		inner = []byte(envelope.Raw)
	}

	for _, field := range geminiCountTokensUnsupportedFields {
		if updated, err := providerUtils.DeleteJSONField(inner, field); err == nil {
			inner = updated
		}
	}

	// generateContentRequest.model is required and must be fully qualified.
	if model = NormalizeModelName(model); model != "" {
		if updated, err := providerUtils.SetJSONField(inner, "model", "models/"+strings.TrimPrefix(model, "models/")); err == nil {
			inner = updated
		}
	}

	wrapped, err := providerUtils.SetRawJSONField([]byte(`{}`), countTokensEnvelopeField, inner)
	if err != nil {
		return jsonData
	}
	return wrapped
}

// ToGeminiGenerationRequest flattens a count tokens request into the generation request
// shape. Top-level contents is dropped when the envelope is present, matching the
// endpoint's own precedence rather than silently merging two sources of truth.
func (request *GeminiCountTokensRequest) ToGeminiGenerationRequest() *GeminiGenerationRequest {
	if request == nil {
		return nil
	}

	// Copy rather than alias: the routing fields below are ours to set, and the parsed
	// request has to come out of this converter unchanged.
	generationRequest := request.GeminiGenerationRequest
	if request.GenerateContentRequest != nil {
		generationRequest = *request.GenerateContentRequest
	}
	// The path model is authoritative, and fallbacks is a Bifrost field callers set
	// beside the envelope rather than inside it.
	generationRequest.Model = request.Model
	if len(request.Fallbacks) > 0 {
		generationRequest.Fallbacks = request.Fallbacks
	}
	generationRequest.IsCountTokens = true

	return &generationRequest
}

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
		AddModalityTokens(inputDetails, string(m.Modality), int(m.TokenCount))
	}

	// Cached tokens are part of the prompt, not extra: promptTokensDetails already
	// counts them, so cacheTokensDetails only says how much of that was cached and
	// must not be added to the modality breakdown again.
	if resp.CachedContentTokenCount != 0 {
		inputDetails.CachedReadTokens = int(resp.CachedContentTokenCount)
	} else {
		cachedSum := 0
		for _, m := range resp.CacheTokensDetails {
			if m == nil {
				continue
			}
			cachedSum += int(m.TokenCount)
		}
		inputDetails.CachedReadTokens = cachedSum
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

// AddModalityTokens accumulates a Gemini modality token count into the neutral details.
func AddModalityTokens(details *schemas.ResponsesResponseInputTokens, modality string, count int) {
	switch mod := strings.ToLower(modality); {
	case strings.Contains(mod, "audio"):
		details.AudioTokens += count
	case strings.Contains(mod, "image"), strings.Contains(mod, "video"):
		details.ImageTokens += count
	default:
		details.TextTokens += count
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
	if bifrostResp.TotalTokens != nil && *bifrostResp.TotalTokens > 0 {
		response.TotalTokens = int32(*bifrostResp.TotalTokens)
	}

	details := bifrostResp.InputTokensDetails
	if details == nil {
		return response
	}

	// Map cached content token count if available
	response.CachedContentTokenCount = int32(details.CachedReadTokens)

	for _, m := range []struct {
		modality Modality
		count    int
	}{
		{ModalityText, details.TextTokens},
		{ModalityImage, details.ImageTokens},
		{ModalityAudio, details.AudioTokens},
	} {
		if m.count > 0 {
			response.PromptTokensDetails = append(response.PromptTokensDetails, &ModalityTokenCount{
				Modality:   m.modality,
				TokenCount: int32(m.count),
			})
		}
	}

	return response
}

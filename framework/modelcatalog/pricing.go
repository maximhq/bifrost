package modelcatalog

import (
	"context"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
)

// GetModelCapabilityEntryForModel returns capability metadata for a
// (model, provider) pair. Prefers chat, then responses, then text-completion
// entries; falls back to the lexicographically first available mode for
// deterministic behavior.
func (mc *ModelCatalog) GetModelCapabilityEntryForModel(model string, provider schemas.ModelProvider) *PricingEntry {
	return mc.datasheet.GetCapabilityEntry(model, provider)
}

// IsRequestTypeSupported preserves the historical (model, provider,
// requestType) signature; provider is ignored (the underlying datasheet
// index is keyed by model only).
func (mc *ModelCatalog) IsRequestTypeSupported(model string, provider schemas.ModelProvider, requestType schemas.RequestType) bool {
	return mc.datasheet.IsRequestTypeSupported(model, requestType)
}

func (mc *ModelCatalog) GetSupportedParameters(model string) []string {
	return mc.datasheet.GetSupportedParameters(model)
}

func (mc *ModelCatalog) IsTextCompletionSupported(model string, provider schemas.ModelProvider) bool {
	return mc.datasheet.IsTextCompletionSupported(model, provider)
}

// GetPricingEntryForModel returns any pricing entry for the model across
// known modes. Used by the inference handler to enrich list-models responses.
func (mc *ModelCatalog) GetPricingEntryForModel(model string, provider schemas.ModelProvider) *PricingEntry {
	return mc.datasheet.GetPricingEntryForModel(model, provider)
}

// CalculateCost computes the dollar cost for a Bifrost response.
func (mc *ModelCatalog) CalculateCost(result *schemas.BifrostResponse, scopes *PricingLookupScopes) float64 {
	return mc.datasheet.CalculateCost(result, (*datasheet.LookupScopes)(scopes))
}

// UpsertModelPricingAttributes writes additional_attributes for every row
// matching (model, provider) and reloads the pricing cache.
func (mc *ModelCatalog) UpsertModelPricingAttributes(ctx context.Context, model string, provider schemas.ModelProvider, attrs map[string]string) (int64, error) {
	return mc.datasheet.UpsertModelPricingAttributes(ctx, model, provider, attrs)
}

func (mc *ModelCatalog) SetPricingOverrides(rows []configstoreTables.TablePricingOverride) error {
	return mc.datasheet.SetOverrides(rows)
}

func (mc *ModelCatalog) UpsertPricingOverrides(rows ...*configstoreTables.TablePricingOverride) error {
	return mc.datasheet.UpsertOverrides(rows...)
}

func (mc *ModelCatalog) DeletePricingOverride(id string) {
	mc.datasheet.DeleteOverride(id)
}

// ---------------------------------------------------------------------------
// Passthrough pricing helpers
// ---------------------------------------------------------------------------

// detectPassthroughRequestType maps a provider + stripped path to a RequestType.
func detectPassthroughRequestType(provider schemas.ModelProvider, path string) schemas.RequestType {
	if idx := strings.IndexByte(path, '?'); idx >= 0 {
		path = path[:idx]
	}
	path = strings.TrimRight(path, "/")
	switch provider {
	case schemas.OpenAI, schemas.Azure:
		switch {
		case strings.HasSuffix(path, "/chat/completions"):
			return schemas.ChatCompletionRequest
		case strings.HasSuffix(path, "/completions"):
			return schemas.TextCompletionRequest
		case strings.HasSuffix(path, "/embeddings"):
			return schemas.EmbeddingRequest
		case strings.HasSuffix(path, "/responses"):
			return schemas.ResponsesRequest
		case strings.HasSuffix(path, "/images/generations"):
			return schemas.ImageGenerationRequest
		case strings.HasSuffix(path, "/images/edits"):
			return schemas.ImageEditRequest
		case strings.HasSuffix(path, "/images/variations"):
			return schemas.ImageVariationRequest
		case strings.HasSuffix(path, "/audio/speech"):
			return schemas.SpeechRequest
		case strings.HasSuffix(path, "/audio/transcriptions"),
			strings.HasSuffix(path, "/audio/translations"):
			return schemas.TranscriptionRequest
		case strings.HasSuffix(path, "/containers"):
			return schemas.ContainerCreateRequest
		case strings.Contains(path, "/video"):
			return schemas.VideoGenerationRequest
		default:
			return schemas.ChatCompletionRequest
		}
	case schemas.Gemini, schemas.Vertex:
		// Interactions API paths carry no colon action suffix.
		if strings.Contains(path, "/interactions") {
			return schemas.ResponsesRequest
		}
		colonIdx := strings.LastIndexByte(path, ':')
		if colonIdx < 0 {
			return schemas.ChatCompletionRequest
		}
		switch path[colonIdx+1:] {
		case "generateContent", "streamGenerateContent":
			return schemas.ResponsesRequest
		case "embedContent", "batchEmbedContents":
			return schemas.EmbeddingRequest
		case "generateImages":
			return schemas.ImageGenerationRequest
		case "predict":
			return schemas.EmbeddingRequest
		case "predictLongRunning":
			return schemas.VideoGenerationRequest
		default:
			return schemas.ChatCompletionRequest
		}
	case schemas.Anthropic:
		switch {
		case strings.HasSuffix(path, "/messages"):
			return schemas.ResponsesRequest
		case strings.HasSuffix(path, "/complete"):
			return schemas.TextCompletionRequest
		default:
			return schemas.ResponsesRequest
		}
	default:
		return schemas.ChatCompletionRequest
	}
}

// inferPassthroughRequestType determines the request type from usage fields (primary)
// and falls back to path detection for text/embedding/responses where LLMUsage is ambiguous.
func inferPassthroughRequestType(provider schemas.ModelProvider, path string, su *schemas.BifrostPassthroughUsage) schemas.RequestType {
	if su != nil {
		if su.ContainerIdentifier != "" {
			return schemas.ContainerCreateRequest
		}
		if su.ImageUsage != nil {
			return schemas.ImageGenerationRequest
		}
		if su.AudioInputChars > 0 {
			return schemas.SpeechRequest
		}
		if su.AudioTokenDetails != nil || su.AudioSeconds != nil {
			return schemas.TranscriptionRequest
		}
		if su.VideoSeconds != nil {
			return schemas.VideoGenerationRequest
		}
	}
	return detectPassthroughRequestType(provider, path)
}

// passthroughUsageToCostInput converts BifrostPassthroughUsage into costInput.
func passthroughUsageToCostInput(su *schemas.BifrostPassthroughUsage) costInput {
	var input costInput
	if su.LLMUsage != nil {
		input.usage = su.LLMUsage
	}
	if su.ServiceTier != nil {
		input.tier = tierFromString(su.ServiceTier)
	}
	if su.ImageUsage != nil {
		input.imageUsage = su.ImageUsage
		input.imageSize = su.ImageSize
		input.imageQuality = su.ImageQuality
	}
	if su.AudioInputChars > 0 {
		input.audioTextInputChars = su.AudioInputChars
	}
	if su.AudioSeconds != nil {
		input.audioSeconds = su.AudioSeconds
	}
	if su.AudioTokenDetails != nil {
		input.audioTokenDetails = su.AudioTokenDetails
	}
	if su.VideoSeconds != nil {
		input.videoSeconds = su.VideoSeconds
	}
	if su.ContainerIdentifier != "" {
		input.containerIdentifierString = su.ContainerIdentifier
	}
	return input
}

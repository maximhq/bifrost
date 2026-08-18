package bifrost

import (
	"fmt"
	"slices"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

const humeIntegrationType = "hume"

// enforceHumeSingleToolConstraint runs on the final provider-bound request, after
// MCP and plugin tool injection. Hume EVI supports at most one tool call per turn,
// so only providers whose wire format can express that constraint are eligible.
func (bifrost *Bifrost) enforceHumeSingleToolConstraint(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) *schemas.BifrostError {
	if integrationType, _ := ctx.Value(schemas.BifrostContextKeyIntegrationType).(string); integrationType != humeIntegrationType {
		return nil
	}
	if req == nil || req.ChatRequest == nil || req.ChatRequest.Params == nil || len(req.ChatRequest.Params.Tools) == 0 {
		return nil
	}

	provider, model, _ := req.GetRequestFields()
	baseProvider := provider
	if bifrost != nil && bifrost.account != nil {
		if config, err := bifrost.account.GetConfigForProvider(provider); err == nil && config != nil &&
			config.CustomProviderConfig != nil && config.CustomProviderConfig.BaseProviderType != "" {
			baseProvider = config.CustomProviderConfig.BaseProviderType
		}
	}
	if !humeProviderSupportsSingleToolControl(ctx, provider, baseProvider, model) {
		return providerUtils.NewBifrostBadRequestError(fmt.Sprintf(
			"provider %q model %q cannot guarantee Hume's single-tool-call requirement",
			provider,
			model,
		))
	}

	req.ChatRequest.Params.ParallelToolCalls = schemas.Ptr(false)
	return nil
}

func humeProviderSupportsSingleToolControl(ctx *schemas.BifrostContext, provider, baseProvider schemas.ModelProvider, model string) bool {
	// Anthropic's Messages wire format expresses the inverse setting as
	// tool_choice.disable_parallel_tool_use. These providers all use the shared
	// Anthropic request builder for Anthropic-family models.
	if baseProvider == schemas.Anthropic ||
		((baseProvider == schemas.Azure || baseProvider == schemas.Vertex || baseProvider == schemas.BedrockMantle) &&
			schemas.IsAnthropicModelFamily(ctx, model)) {
		return true
	}

	if !humeUsesParallelToolCallsWire(ctx, baseProvider, model) {
		return false
	}

	modelInfo := ctx.GetModelInfo(provider, model)
	if modelInfo == nil && baseProvider != provider {
		modelInfo = ctx.GetModelInfo(baseProvider, model)
	}
	return modelInfo != nil && slices.Contains(modelInfo.SupportedParameters, "parallel_tool_calls")
}

func humeUsesParallelToolCallsWire(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string) bool {
	switch provider {
	case schemas.OpenAI,
		schemas.Azure,
		schemas.BedrockMantle,
		schemas.Cerebras,
		schemas.DeepSeek,
		schemas.Fireworks,
		schemas.Groq,
		schemas.Mistral,
		schemas.Nebius,
		schemas.Ollama,
		schemas.OpencodeGo,
		schemas.OpencodeZen,
		schemas.OpenRouter,
		schemas.Parasail,
		schemas.Perplexity,
		schemas.Sarvam,
		schemas.SGL,
		schemas.VLLM,
		schemas.Wafer,
		schemas.XAI:
		return true
	case schemas.Bedrock:
		return schemas.IsOpenAIModelFamily(ctx, model) || strings.Contains(schemas.ResolveCanonicalModel(ctx, model), "gemma-4")
	case schemas.Vertex:
		return !schemas.IsGeminiModelFamily(ctx, model) && !schemas.IsGemmaModelFamily(ctx, model)
	default:
		return false
	}
}

package bifrost

import (
	"fmt"
	"slices"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// enforceSingleToolConstraint runs on the final provider-bound request, after
// plugin processing. Transports whose downstream consumer accepts only one tool
// call can opt into the policy without coupling core to an integration name.
func (bifrost *Bifrost) enforceSingleToolConstraint(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) *schemas.BifrostError {
	requireSerial, _ := ctx.Value(schemas.BifrostContextKeyRequireSerialToolCalls).(bool)
	if !requireSerial || req == nil || req.ChatRequest == nil || req.ChatRequest.Params == nil || len(req.ChatRequest.Params.Tools) == 0 {
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
	if !providerSupportsSingleToolControl(ctx, provider, baseProvider, model) {
		return providerUtils.NewBifrostBadRequestError(fmt.Sprintf(
			"provider %q model %q cannot guarantee serial tool execution",
			provider,
			model,
		))
	}

	req.ChatRequest.Params.ParallelToolCalls = schemas.Ptr(false)
	return nil
}

func providerSupportsSingleToolControl(ctx *schemas.BifrostContext, provider, baseProvider schemas.ModelProvider, model string) bool {
	// Anthropic's Messages wire format expresses the inverse setting as
	// tool_choice.disable_parallel_tool_use. These providers all use the shared
	// Anthropic request builder for Anthropic-family models.
	if baseProvider == schemas.Anthropic ||
		((baseProvider == schemas.Azure || baseProvider == schemas.Vertex || baseProvider == schemas.BedrockMantle) &&
			schemas.IsAnthropicModelFamily(ctx, model)) {
		return true
	}

	if !usesParallelToolCallsWire(ctx, baseProvider, model) {
		return false
	}

	modelInfo := ctx.GetModelInfo(provider, model)
	if modelInfo == nil && baseProvider != provider {
		modelInfo = ctx.GetModelInfo(baseProvider, model)
	}
	// An absent catalog entry is common for self-hosted and custom providers.
	// Their OpenAI-compatible wire still supports parallel_tool_calls=false.
	return modelInfo == nil || slices.Contains(modelInfo.SupportedParameters, "parallel_tool_calls")
}

func usesParallelToolCallsWire(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string) bool {
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

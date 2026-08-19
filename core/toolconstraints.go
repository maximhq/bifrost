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
	baseProvider := bifrost.baseProviderTypeFor(provider)
	for _, candidate := range bifrost.serialToolCandidateModels(ctx, provider, model) {
		if providerSupportsSingleToolControl(ctx, provider, baseProvider, candidate) {
			continue
		}
		detail := ""
		if candidate != model {
			detail = fmt.Sprintf(" (key alias resolves to %q)", candidate)
		}
		return providerUtils.NewBifrostBadRequestError(fmt.Sprintf(
			"provider %q model %q cannot guarantee serial tool execution%s",
			provider,
			model,
			detail,
		))
	}

	req.ChatRequest.Params.ParallelToolCalls = schemas.Ptr(false)
	return nil
}

// serialToolCandidateModels returns every model identifier the request may
// actually reach the provider under. Key aliases are resolved per key by the
// worker after this policy runs, and an alias replaces the requested name
// outright (see the req.SetModel call in the retry closure), so the requested
// name is only a candidate when some eligible key would send it unaliased.
func (bifrost *Bifrost) serialToolCandidateModels(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string) []string {
	if bifrost == nil || bifrost.account == nil {
		return []string{model}
	}
	var keys []schemas.Key
	if directKey, ok := ctx.Value(schemas.BifrostContextKeyDirectKey).(schemas.Key); ok {
		keys = []schemas.Key{directKey}
	} else if providerKeys, err := bifrost.account.GetKeysForProvider(ctx, provider); err == nil {
		keys = providerKeys
	}
	var candidates []string
	eligible := 0
	for _, key := range keys {
		if key.Enabled != nil && !*key.Enabled {
			continue
		}
		if !key.Models.IsAllowed(model) || key.BlacklistedModels.IsBlocked(model) {
			continue
		}
		eligible++
		candidate := model
		if aliasConfig := key.Aliases.ResolveConfig(model); aliasConfig != nil && aliasConfig.ModelID != "" {
			candidate = aliasConfig.ModelID
		}
		if !slices.Contains(candidates, candidate) {
			candidates = append(candidates, candidate)
		}
	}
	// No eligible key means the routing target is unknown here: the key lookup
	// failed, the pool is empty, or the request is served outside the account key
	// pool. Fall back to the requested name so an unknown route keeps the
	// conservative check instead of silently passing every model.
	if eligible == 0 || len(candidates) == 0 {
		return []string{model}
	}
	return candidates
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
		// Mirrors the Vertex chat routing predicate: Gemini- and Gemma-family
		// names and all-digit fine-tuned endpoint IDs use the Gemini request
		// builder, which has no parallel_tool_calls field.
		return !schemas.IsGeminiModelFamily(ctx, model) && !schemas.IsAllDigitsASCII(model) && !schemas.IsGemmaModelFamily(ctx, model)
	default:
		return false
	}
}

package bedrock

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"regexp"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/cespare/xxhash/v2"
	"github.com/tidwall/sjson"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// awsRegionRegex matches valid AWS region identifiers (e.g. "us-east-1", "eu-north-1", "us-gov-east-1").
// (?:-[a-z]+)+ allows multi-segment directional parts so GovCloud regions (us-gov-east-1) are
// recognised alongside standard single-segment ones (eu-north-1, ap-southeast-2).
var awsRegionRegex = regexp.MustCompile(`^[a-z]{2,3}(?:-[a-z]+)+-\d+$`)
var bedrockUnsafeToolNameCharRegex = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// bedrockToolNameAliasKey stores Bedrock wire-name aliases on the request context.
type bedrockToolNameAliasKey struct{}

// resolveMantleProjectID returns the Bedrock project configured for the mantle sub-surface of the
// Bedrock provider, or "" when none is set (AWS then routes to the account's default project).
// Priority: per-alias AliasConfig.ProjectID > key-level BedrockKeyConfig.ProjectID. The per-alias
// override lets one Bedrock credential scope different aliased models to different projects.
func resolveMantleProjectID(ctx *schemas.BifrostContext, key schemas.Key) string {
	if ra := schemas.GetResolvedAlias(ctx); ra != nil && ra.Config != nil && ra.Config.ProjectID != nil {
		if v := ra.Config.ProjectID.GetValue(); v != "" {
			return v
		}
	}
	if key.BedrockKeyConfig != nil && key.BedrockKeyConfig.ProjectID != nil {
		return key.BedrockKeyConfig.ProjectID.GetValue()
	}
	return ""
}

// parseBedrockRegionAndModel splits a model string that optionally carries an AWS region prefix
// into its region and bare model ID components.
// If no region prefix is present the returned region is empty and bareModel equals model.
func parseBedrockRegionAndModel(model string) (region, bareModel string) {
	if idx := strings.IndexByte(model, '/'); idx > 0 {
		prefix := model[:idx]
		if awsRegionRegex.MatchString(prefix) {
			return prefix, model[idx+1:]
		}
	}
	return "", model
}

// resolveBedrockRegion returns the AWS region to use for a request.
// Priority: model-string region prefix > alias-level Region > key-level
// BedrockKeyConfig.Region > DefaultBedrockRegion. The model-string prefix
// stays highest since it's the most explicit signal — when an admin types a
// region into their model ID they expect that to win.
func resolveBedrockRegion(ctx *schemas.BifrostContext, key schemas.Key, model string) string {
	if region, _ := parseBedrockRegionAndModel(model); region != "" {
		return region
	}
	if ra := schemas.GetResolvedAlias(ctx); ra != nil && ra.Config != nil && ra.Config.Region != nil {
		if v := ra.Config.Region.GetValue(); v != "" {
			return v
		}
	}
	if key.BedrockKeyConfig != nil && key.BedrockKeyConfig.Region != nil && key.BedrockKeyConfig.Region.GetValue() != "" {
		return key.BedrockKeyConfig.Region.GetValue()
	}
	return DefaultBedrockRegion
}

// resolveBedrockARN returns the inference-profile / resource ARN prepended
// to the Bedrock URL path. Priority: alias-level BedrockAliasCfg
// InferenceProfileARN > key-level BedrockKeyConfig.ARN. Returns empty when
// neither is set, in which case getModelPathAndRegion emits the bare model
// path.
func resolveBedrockARN(ctx *schemas.BifrostContext, key schemas.Key) string {
	if ra := schemas.GetResolvedAlias(ctx); ra != nil && ra.Config != nil && ra.Config.BedrockAliasCfg != nil && ra.Config.BedrockAliasCfg.InferenceProfileARN != nil {
		if v := ra.Config.BedrockAliasCfg.InferenceProfileARN.GetValue(); v != "" {
			return v
		}
	}
	if key.BedrockKeyConfig != nil && key.BedrockKeyConfig.ARN != nil {
		return key.BedrockKeyConfig.ARN.GetValue()
	}
	return ""
}

var (
	invalidCharRegex = regexp.MustCompile(`[^a-zA-Z0-9\s\-\(\)\[\]]`)
	multiSpaceRegex  = regexp.MustCompile(`\s{2,}`)

	// bedrockFinishReasonToBifrost maps Bedrock Converse API stop reasons to Bifrost format.
	// Unmappable reasons (e.g. guardrail_intervened) are passed through as-is.
	bedrockFinishReasonToBifrost = map[string]string{
		"end_turn":         "stop",
		"max_tokens":       "length",
		"stop_sequence":    "stop",
		"tool_use":         "tool_calls",
		"content_filtered": "content_filter",
	}

	// bifrostToBedrockStopReason is the reverse of bedrockFinishReasonToBifrost.
	bifrostToBedrockStopReason = map[string]string{
		"stop":           "end_turn",
		"length":         "max_tokens",
		"tool_calls":     "tool_use",
		"content_filter": "content_filtered",
	}
)

// convertBedrockStopReason converts a Bedrock stop reason to Bifrost format.
func convertBedrockStopReason(stopReason string) string {
	if reason, ok := bedrockFinishReasonToBifrost[stopReason]; ok {
		return reason
	}
	return stopReason
}

// convertBifrostToBedrockStopReason converts a Bifrost stop reason back to Bedrock format.
func convertBifrostToBedrockStopReason(bifrostReason string) string {
	if reason, ok := bifrostToBedrockStopReason[bifrostReason]; ok {
		return reason
	}
	return bifrostReason
}

// mapBifrostServiceTierToBedrock maps a BifrostServiceTier to a BedrockServiceTierType.
func mapBifrostServiceTierToBedrock(tier schemas.BifrostServiceTier) BedrockServiceTierType {
	switch tier {
	case schemas.BifrostServiceTierPriority:
		return BedrockServiceTierTypePriority
	case schemas.BifrostServiceTierFlex:
		return BedrockServiceTierTypeFlex
	case schemas.BifrostServiceTierDefault, schemas.BifrostServiceTierAuto:
		return BedrockServiceTierTypeDefault
	default:
		return BedrockServiceTierType(tier)
	}
}

// mapBedrockServiceTierToBifrost maps a BedrockServiceTierType to a BifrostServiceTier.
// "reserved" maps to priority as it represents pre-purchased priority capacity.
func mapBedrockServiceTierToBifrost(tier BedrockServiceTierType) schemas.BifrostServiceTier {
	switch tier {
	case BedrockServiceTierTypePriority:
		return schemas.BifrostServiceTierPriority
	case BedrockServiceTierTypeFlex:
		return schemas.BifrostServiceTierFlex
	case BedrockServiceTierTypeDefault:
		return schemas.BifrostServiceTierDefault
	default:
		return schemas.BifrostServiceTier(tier)
	}
}

// normalizeBedrockFilename normalizes a filename to meet Bedrock's requirements:
// - Only alphanumeric characters, whitespace, hyphens, parentheses, and square brackets
// - No more than one consecutive whitespace character
// - Trims leading and trailing whitespace
func normalizeBedrockFilename(filename string) string {
	if filename == "" {
		return "document"
	}

	// Replace invalid characters with underscores
	normalized := invalidCharRegex.ReplaceAllString(filename, "_")

	// Replace multiple consecutive whitespace with a single space
	normalized = multiSpaceRegex.ReplaceAllString(normalized, " ")

	// Trim leading and trailing whitespace
	normalized = strings.TrimSpace(normalized)

	// If the result is empty after normalization, return a default name
	if normalized == "" {
		return "document"
	}

	return normalized
}

// bedrockAliasToolName returns a Bedrock-safe tool name and records a reverse mapping.
func bedrockAliasToolName(ctx context.Context, name string) string {
	if len(name) <= 64 && !bedrockUnsafeToolNameCharRegex.MatchString(name) {
		return name
	}

	semanticName := name
	if parts := strings.Split(name, "__"); len(parts) > 1 {
		semanticName = parts[len(parts)-1]
	}
	semanticName = strings.Trim(bedrockUnsafeToolNameCharRegex.ReplaceAllString(semanticName, "_"), "_")
	if semanticName == "" {
		semanticName = "tool"
	}

	hash := fmt.Sprintf("%08x", uint32(xxhash.Sum64String(name)))
	maxSemanticLen := 64 - len(hash) - 1
	if len(semanticName) > maxSemanticLen {
		semanticName = semanticName[:maxSemanticLen]
	}
	alias := hash + "_" + semanticName

	if bifrostCtx, ok := ctx.(*schemas.BifrostContext); ok && bifrostCtx != nil && alias != name {
		aliases, _ := bifrostCtx.Value(bedrockToolNameAliasKey{}).(map[string]string)
		if aliases == nil {
			aliases = make(map[string]string)
			bifrostCtx.SetValue(bedrockToolNameAliasKey{}, aliases)
		}
		aliases[alias] = name
	}
	return alias
}

// bedrockRestoreToolName maps a Bedrock wire-name alias back to the caller's tool name.
func bedrockRestoreToolName(ctx context.Context, name string) string {
	if bifrostCtx, ok := ctx.(*schemas.BifrostContext); ok && bifrostCtx != nil {
		if aliases, _ := bifrostCtx.Value(bedrockToolNameAliasKey{}).(map[string]string); aliases != nil {
			if original, ok := aliases[name]; ok {
				return original
			}
		}
	}
	return name
}

// convertParameters handles parameter conversion
func convertChatParameters(ctx *schemas.BifrostContext, bifrostReq *schemas.BifrostChatRequest, bedrockReq *BedrockConverseRequest) error {
	// Parameters are optional - if not provided, just skip conversion
	if bifrostReq.Params == nil {
		return nil
	}

	// capModel is the canonical model used only for Anthropic capability gating
	capModel := schemas.ResolveCanonicalModel(ctx, bifrostReq.Model)
	// Convert inference config
	if inferenceConfig := convertInferenceConfig(bifrostReq.Params, capModel); inferenceConfig != nil {
		bedrockReq.InferenceConfig = inferenceConfig
	}

	// Handle structured output conversion through the synthetic `bf_so_*` tool
	// path for all Bedrock models, including Anthropic. We avoid native
	// `output_config.format` because Bedrock Converse rejects it on some Claude
	// variants (e.g. Opus 4.7 returns "output_config.format: Extra inputs are not
	// permitted"), whereas the synthetic-tool path is a regular Converse tool
	// call accepted by all variants.
	responseFormatTool, _ := convertResponseFormatToTool(ctx, bifrostReq.Model, bifrostReq.Params)

	// Filter provider-unsupported server tools once; both convertToolConfig and
	// collectBedrockServerTools consume the same filtered set, and
	// buildBedrockServerToolChoice resolves pinned names against it.
	filteredTools, _ := anthropic.ValidateChatToolsForProvider(bifrostReq.Params.Tools, schemas.Bedrock)

	// Convert tool config (function/custom tools → Converse toolConfig.tools).
	if toolConfig := convertToolConfigFromFiltered(ctx, bifrostReq.Model, bifrostReq.Params, filteredTools); toolConfig != nil {
		bedrockReq.ToolConfig = toolConfig
	}

	// Tunnel Bedrock-supported Anthropic server tools through Converse's
	// additionalModelRequestFields (model-specific passthrough) since Converse's
	// typed toolSpec shape can't express server tools like bash_*, computer_*,
	// memory_*, text_editor_*, tool_search_tool_*. Fields injected:
	//   - tools:          array of server tools in Anthropic-native shape, which
	//                     Bedrock merges into the underlying Messages request.
	//   - anthropic_beta: activation header(s) for the relevant server tool, in
	//                     addition to whatever the existing anthropic-beta HTTP
	//                     header path in bedrock.go:214/447 already forwards.
	//   - tool_choice:    Anthropic-native pin for a kept server tool OR an
	//                     any/required contract when only server tools are
	//                     present. Emitted only when Converse's typed
	//                     toolConfig.toolChoice path can't express the intent
	//                     (see buildBedrockServerToolChoice).
	if serverTools, betaHeaders := collectBedrockServerToolsFromFiltered(filteredTools); len(serverTools) > 0 {
		if bedrockReq.AdditionalModelRequestFields == nil {
			bedrockReq.AdditionalModelRequestFields = schemas.NewOrderedMap()
		}
		bedrockReq.AdditionalModelRequestFields.Set("tools", serverTools)
		for _, h := range betaHeaders {
			appendAnthropicBetaToFields(bedrockReq.AdditionalModelRequestFields, h)
		}
		// Skip the tunneled tool_choice when response_format forces the synthetic
		// bf_so_* tool at lines 263-275 below; otherwise Bedrock receives two
		// conflicting tool-choice directives and the structured-output contract
		// can silently break.
		if responseFormatTool == nil {
			if choice, ok := buildBedrockServerToolChoice(bifrostReq.Params, filteredTools); ok {
				bedrockReq.AdditionalModelRequestFields.Set("tool_choice", choice)
			}
		}
	}

	// Fine-grained tool streaming (eager_input_streaming) is an Anthropic beta
	// that Converse can't express in its typed toolSpec shape, and Bedrock's edge
	// strips it from the outer anthropic-beta HTTP header (see the note on
	// appendAnthropicBetaToFields below). The only channel Converse honors is the
	// body field, so mirror the server-tools tunnel above: when a tool opts in,
	// activate the beta through additionalModelRequestFields.anthropic_beta.
	// Gate on the Anthropic model family: Converse also serves Nova/Llama, and
	// tool filtering keeps custom tools (and their eager_input_streaming flag)
	// verbatim regardless of family, so without this gate a non-Anthropic request
	// could receive a meaningless anthropic_beta field.
	if schemas.IsAnthropicModelFamily(ctx, bifrostReq.Model) {
		for _, tool := range filteredTools {
			if tool.EagerInputStreaming != nil && *tool.EagerInputStreaming {
				if bedrockReq.AdditionalModelRequestFields == nil {
					bedrockReq.AdditionalModelRequestFields = schemas.NewOrderedMap()
				}
				appendAnthropicBetaToFields(bedrockReq.AdditionalModelRequestFields, anthropic.AnthropicEagerInputStreamingBetaHeader)
				break
			}
		}
	}

	// Convert reasoning config
	if bifrostReq.Params.Reasoning != nil {
		if bedrockReq.AdditionalModelRequestFields == nil {
			bedrockReq.AdditionalModelRequestFields = schemas.NewOrderedMap()
		}
		if bifrostReq.Params.Reasoning.MaxTokens != nil {
			tokenBudget := *bifrostReq.Params.Reasoning.MaxTokens
			if *bifrostReq.Params.Reasoning.MaxTokens == -1 {
				// bedrock does not support dynamic reasoning budget like gemini
				// setting it to default max tokens
				tokenBudget = anthropic.MinimumReasoningMaxTokens
			}
			if schemas.IsAnthropicModelFamily(ctx, bifrostReq.Model) {
				if anthropic.IsAdaptiveOnlyThinkingModel(capModel) {
					bedrockReq.AdditionalModelRequestFields.Set("thinking", map[string]any{
						"type": "adaptive",
					})
					// Preserve a co-present effort — these models support effort,
					// and the budget is otherwise dropped.
					if bifrostReq.Params.Reasoning.Effort != nil && *bifrostReq.Params.Reasoning.Effort != "none" {
						setOutputConfigField(bedrockReq.AdditionalModelRequestFields, "effort", anthropic.MapBifrostEffortToAnthropic(*bifrostReq.Params.Reasoning.Effort))
					}
				} else {
					if tokenBudget < anthropic.MinimumReasoningMaxTokens {
						return fmt.Errorf("reasoning.max_tokens must be >= %d for anthropic", anthropic.MinimumReasoningMaxTokens)
					}
					bedrockReq.AdditionalModelRequestFields.Set("thinking", map[string]any{
						"type":          "enabled",
						"budget_tokens": tokenBudget,
					})
				}
			} else if schemas.IsNovaModelFamily(ctx, bifrostReq.Model) {
				minBudgetTokens := MinimumReasoningMaxTokens
				modelDefaultMaxTokens := providerUtils.GetMaxOutputTokensOrDefault(bifrostReq.Model, DefaultCompletionMaxTokens)
				defaultMaxTokens := modelDefaultMaxTokens
				if bedrockReq.InferenceConfig != nil && bedrockReq.InferenceConfig.MaxTokens != nil {
					defaultMaxTokens = *bedrockReq.InferenceConfig.MaxTokens
				} else if bedrockReq.InferenceConfig != nil {
					bedrockReq.InferenceConfig.MaxTokens = schemas.Ptr(modelDefaultMaxTokens)
				} else {
					bedrockReq.InferenceConfig = &BedrockInferenceConfig{
						MaxTokens: schemas.Ptr(modelDefaultMaxTokens),
					}
				}

				maxReasoningEffort := providerUtils.GetReasoningEffortFromBudgetTokens(tokenBudget, minBudgetTokens, defaultMaxTokens)
				typeStr := "enabled"
				switch maxReasoningEffort {
				case "high":
					if bedrockReq.InferenceConfig != nil {
						bedrockReq.InferenceConfig.MaxTokens = nil
						bedrockReq.InferenceConfig.Temperature = nil
						bedrockReq.InferenceConfig.TopP = nil
					}
				case "minimal":
					maxReasoningEffort = "low"
				case "none":
					typeStr = "disabled"
				}

				config := map[string]any{
					"type": typeStr,
				}
				if typeStr != "disabled" {
					config["maxReasoningEffort"] = maxReasoningEffort
				}

				bedrockReq.AdditionalModelRequestFields.Set("reasoningConfig", config)
			} else {
				bedrockReq.AdditionalModelRequestFields.Set("reasoningConfig", map[string]any{
					"type":          "enabled",
					"budget_tokens": tokenBudget,
				})
			}
		} else if bifrostReq.Params.Reasoning.Effort != nil && *bifrostReq.Params.Reasoning.Effort != "none" {
			modelDefaultMaxTokens := providerUtils.GetMaxOutputTokensOrDefault(bifrostReq.Model, DefaultCompletionMaxTokens)
			maxTokens := modelDefaultMaxTokens
			if bedrockReq.InferenceConfig != nil && bedrockReq.InferenceConfig.MaxTokens != nil {
				maxTokens = *bedrockReq.InferenceConfig.MaxTokens
			} else {
				if bedrockReq.InferenceConfig != nil {
					bedrockReq.InferenceConfig.MaxTokens = schemas.Ptr(modelDefaultMaxTokens)
				} else {
					bedrockReq.InferenceConfig = &BedrockInferenceConfig{
						MaxTokens: schemas.Ptr(modelDefaultMaxTokens),
					}
				}
			}
			if schemas.IsNovaModelFamily(ctx, bifrostReq.Model) {
				effort := *bifrostReq.Params.Reasoning.Effort
				typeStr := "enabled"
				switch effort {
				case "high", "xhigh", "max":
					// Nova's maxReasoningEffort enum tops out at "high"; clamp xhigh/max.
					effort = "high"
					if bedrockReq.InferenceConfig != nil {
						bedrockReq.InferenceConfig.MaxTokens = nil
						bedrockReq.InferenceConfig.Temperature = nil
						bedrockReq.InferenceConfig.TopP = nil
					}
				case "minimal":
					effort = "low"
				case "none":
					typeStr = "disabled"
				}

				config := map[string]any{
					"type": typeStr,
				}
				if typeStr != "disabled" {
					config["maxReasoningEffort"] = effort
				}

				bedrockReq.AdditionalModelRequestFields.Set("reasoningConfig", config)
			} else if schemas.IsAnthropicModelFamily(ctx, bifrostReq.Model) {
				if anthropic.SupportsAdaptiveThinking(capModel) {
					// Opus 4.6+: adaptive thinking + output_config.effort
					effort := anthropic.MapBifrostEffortToAnthropic(*bifrostReq.Params.Reasoning.Effort)
					thinkingConfig := map[string]any{
						"type": "adaptive",
					}
					if bifrostReq.Params.Reasoning.Display != nil {
						thinkingConfig["display"] = *bifrostReq.Params.Reasoning.Display
					} else if anthropic.IsAdaptiveOnlyThinkingModel(capModel) {
						thinkingConfig["display"] = "summarized"
					}
					bedrockReq.AdditionalModelRequestFields.Set("thinking", thinkingConfig)
					setOutputConfigField(bedrockReq.AdditionalModelRequestFields, "effort", effort)
				} else {
					// Opus 4.5 and older models: budget_tokens thinking
					budgetTokens, err := providerUtils.GetBudgetTokensFromReasoningEffort(*bifrostReq.Params.Reasoning.Effort, anthropic.MinimumReasoningMaxTokens, maxTokens)
					if err != nil {
						return err
					}
					bedrockReq.AdditionalModelRequestFields.Set("thinking", map[string]any{
						"type":          "enabled",
						"budget_tokens": budgetTokens,
					})
				}
			}
		} else {
			if schemas.IsAnthropicModelFamily(ctx, bifrostReq.Model) {
				if !anthropic.IsFableFamily(capModel) {
					bedrockReq.AdditionalModelRequestFields.Set("thinking", map[string]any{
						"type": "disabled",
					})
				}
			} else if schemas.IsNovaModelFamily(ctx, bifrostReq.Model) {
				bedrockReq.AdditionalModelRequestFields.Set("reasoningConfig", map[string]any{
					"type": "disabled",
				})
			} else {
				bedrockReq.AdditionalModelRequestFields.Set("reasoningConfig", map[string]any{
					"type": "disabled",
				})
			}
		}
	}

	// If response_format was converted to a tool, add it to the tool config
	if responseFormatTool != nil {
		if bedrockReq.ToolConfig == nil {
			bedrockReq.ToolConfig = &BedrockToolConfig{}
		}
		// Add the response format tool to the beginning of the tools list
		bedrockReq.ToolConfig.Tools = append([]BedrockTool{*responseFormatTool}, bedrockReq.ToolConfig.Tools...)
		// Force the model to use this specific tool, EXCEPT on Meta Llama where
		// Bedrock Converse rejects toolConfig.toolChoice.tool with HTTP 400
		// ("This model doesn't support the toolConfig.toolChoice.tool field").
		// With only the synthetic bf_so_* tool bound, omitting tool_choice
		// (Bedrock default = "auto") yields the same outcome on Llama because
		// there's exactly one tool the model can call. See the per-model
		// support matrix at
		// https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ToolChoice.html
		// and the langchain-aws ChatBedrockConverse implementation at
		// https://github.com/langchain-ai/langchain-aws/blob/main/libs/aws/langchain_aws/chat_models/bedrock_converse.py
		// (supports_tool_choice_values), which ships the same model-family gate.
		thinkingEnabled := bifrostReq.Params.Reasoning != nil &&
			(bifrostReq.Params.Reasoning.MaxTokens != nil ||
				(bifrostReq.Params.Reasoning.Effort != nil && *bifrostReq.Params.Reasoning.Effort != "none"))
		if !schemas.IsLlamaModelFamily(ctx, bifrostReq.Model) && !thinkingEnabled {
			bedrockReq.ToolConfig.ToolChoice = &BedrockToolChoice{
				Tool: &BedrockToolChoiceTool{
					Name: responseFormatTool.ToolSpec.Name,
				},
			}
		}
	}
	if bifrostReq.Params.ServiceTier != nil {
		bedrockReq.ServiceTier = &BedrockServiceTier{
			Type: mapBifrostServiceTierToBedrock(*bifrostReq.Params.ServiceTier),
		}
	}
	// Add extra parameters
	if len(bifrostReq.Params.ExtraParams) > 0 {
		bedrockReq.ExtraParams = bifrostReq.Params.ExtraParams
		applyBedrockExtraParams(bedrockReq.ExtraParams, bedrockReq)
		if len(bedrockReq.ExtraParams) == 0 {
			bedrockReq.ExtraParams = nil
		}
	}
	return nil
}

func applyBedrockExtraParams(extraParams map[string]interface{}, bedrockReq *BedrockConverseRequest) {
	if guardrailConfig, exists := extraParams["guardrailConfig"]; exists {
		if gc, ok := guardrailConfig.(map[string]interface{}); ok {
			config := &BedrockGuardrailConfig{}
			if identifier, ok := gc["guardrailIdentifier"].(string); ok {
				config.GuardrailIdentifier = identifier
			}
			if version, ok := gc["guardrailVersion"].(string); ok {
				config.GuardrailVersion = version
			}
			if trace, ok := gc["trace"].(string); ok {
				config.Trace = &trace
			}
			if mode, ok := gc["streamProcessingMode"].(string); ok {
				config.StreamProcessingMode = &mode
			}
			delete(extraParams, "guardrailConfig")
			bedrockReq.GuardrailConfig = config
		}
	}

	if requestFields, exists := extraParams["additionalModelRequestFieldPaths"]; exists {
		if orderedFields, ok := schemas.SafeExtractOrderedMap(requestFields); ok {
			delete(extraParams, "additionalModelRequestFieldPaths")
			bedrockReq.AdditionalModelRequestFields = mergeAdditionalModelRequestFields(
				bedrockReq.AdditionalModelRequestFields,
				orderedFields,
			)
		}
	}

	if responseFields, exists := extraParams["additionalModelResponseFieldPaths"]; exists {
		if fields, ok := responseFields.([]string); ok {
			delete(extraParams, "additionalModelResponseFieldPaths")
			bedrockReq.AdditionalModelResponseFieldPaths = fields
		} else if fieldsInterface, ok := responseFields.([]interface{}); ok {
			stringFields := make([]string, 0, len(fieldsInterface))
			for _, field := range fieldsInterface {
				if fieldStr, ok := field.(string); ok {
					stringFields = append(stringFields, fieldStr)
				}
			}
			if len(stringFields) > 0 {
				delete(extraParams, "additionalModelResponseFieldPaths")
				bedrockReq.AdditionalModelResponseFieldPaths = stringFields
			}
		}
	}

	if perfConfig, exists := extraParams["performanceConfig"]; exists {
		if pc, ok := perfConfig.(map[string]interface{}); ok {
			config := &BedrockPerformanceConfig{}
			if latency, ok := pc["latency"].(string); ok {
				config.Latency = &latency
			}
			delete(extraParams, "performanceConfig")
			bedrockReq.PerformanceConfig = config
		}
	}

	if promptVars, exists := extraParams["promptVariables"]; exists {
		if vars, ok := promptVars.(map[string]interface{}); ok {
			delete(extraParams, "promptVariables")
			variables := make(map[string]BedrockPromptVariable)
			for k, v := range vars {
				if valueMap, ok := v.(map[string]interface{}); ok {
					variable := BedrockPromptVariable{}
					if text, ok := valueMap["text"].(string); ok {
						variable.Text = &text
					}
					variables[k] = variable
				}
			}
			if len(variables) > 0 {
				bedrockReq.PromptVariables = variables
			}
		}
	}

	if reqMetadata, exists := extraParams["requestMetadata"]; exists {
		if metadata, ok := schemas.SafeExtractStringMap(reqMetadata); ok {
			delete(extraParams, "requestMetadata")
			bedrockReq.RequestMetadata = metadata
		}
	}
}

func setOutputConfigField(fields *schemas.OrderedMap, key string, value any) {
	if fields == nil {
		return
	}
	current := schemas.NewOrderedMap()
	if existing, ok := fields.Get("output_config"); ok {
		if om, ok := toOrderedMap(existing); ok && om != nil {
			current = om
		}
	}
	current.Set(key, value)
	fields.Set("output_config", current)
}

func mergeAdditionalModelRequestFields(existing, incoming *schemas.OrderedMap) *schemas.OrderedMap {
	if existing == nil {
		if incoming == nil {
			return nil
		}
		return incoming.Clone()
	}
	if incoming == nil {
		return existing
	}

	merged := existing.Clone()
	incoming.Range(func(key string, value interface{}) bool {
		if key == "output_config" {
			current := schemas.NewOrderedMap()
			if existingValue, ok := merged.Get(key); ok {
				if om, ok := toOrderedMap(existingValue); ok && om != nil {
					current = om
				}
			}
			if incomingMap, ok := toOrderedMap(value); ok && incomingMap != nil {
				mergeOrderedMapInto(current, incomingMap)
				merged.Set(key, current)
			} else {
				merged.Set(key, value)
			}
			return true
		}
		merged.Set(key, value)
		return true
	})
	return merged
}

func toOrderedMap(v any) (*schemas.OrderedMap, bool) {
	switch m := v.(type) {
	case *schemas.OrderedMap:
		if m == nil {
			return nil, false
		}
		return m.Clone(), true
	case schemas.OrderedMap:
		return m.Clone(), true
	case map[string]interface{}:
		// Fallback for callers that still provide a plain map. Order cannot be
		// reconstructed here, but keeping this path preserves compatibility.
		return schemas.OrderedMapFromMap(m), true
	default:
		return nil, false
	}
}

// mergeOrderedMapInto deep-merges src into dst. Nested OrderedMap values are
// merged recursively; non-map values from src overwrite dst. Existing key order
// is preserved and newly introduced keys are appended in source order.
func mergeOrderedMapInto(dst, src *schemas.OrderedMap) {
	if dst == nil || src == nil {
		return
	}
	src.Range(func(key string, srcVal interface{}) bool {
		if srcMap, ok := toOrderedMap(srcVal); ok && srcMap != nil {
			if dstVal, exists := dst.Get(key); exists {
				if dstMap, ok := toOrderedMap(dstVal); ok && dstMap != nil {
					mergeOrderedMapInto(dstMap, srcMap)
					dst.Set(key, dstMap)
					return true
				}
			}
		}
		dst.Set(key, srcVal)
		return true
	})
}

func newAnthropicOutputFormatOrderedMap(schemaObj any) *schemas.OrderedMap {
	// Normalize multi-type arrays (["string","null"], ["string","integer"]) into anyOf branches
	// so Bedrock's schema validator accepts them. Map inputs use the in-memory normalizer;
	// json.RawMessage / []byte inputs use the sjson-based normalizer to avoid map round-trips.
	// OrderedMap schemas are passed through unchanged.
	switch v := schemaObj.(type) {
	case map[string]interface{}:
		schemaObj = anthropic.NormalizeSchemaForAnthropic(v)
	case json.RawMessage:
		schemaObj = anthropic.NormalizeSchemaForAnthropicRaw(v)
	case []byte:
		schemaObj = anthropic.NormalizeSchemaForAnthropicRaw(json.RawMessage(v))
	}
	return schemas.NewOrderedMapFromPairs(
		schemas.KV("type", "json_schema"),
		schemas.KV("schema", schemaObj),
	)
}

// appendAnthropicBetaToFields merges a single beta header value into
// additionalModelRequestFields.anthropic_beta without creating duplicates.
// This is needed for Bedrock: the outer HTTP anthropic-beta header is consumed
// by Bedrock's edge and NOT forwarded to the underlying Claude model; the value
// must live in additionalModelRequestFields so Bedrock passes it through.
func appendAnthropicBetaToFields(fields *schemas.OrderedMap, header string) {
	if fields == nil || header == "" {
		return
	}
	var existing []string
	if raw, ok := fields.Get("anthropic_beta"); ok {
		switch v := raw.(type) {
		case []string:
			existing = v
		case []interface{}:
			for _, item := range v {
				if s, ok := item.(string); ok {
					existing = append(existing, s)
				}
			}
		case string:
			if v != "" {
				existing = []string{v}
			}
		}
	}
	for _, h := range existing {
		if h == header {
			return
		}
	}
	fields.Set("anthropic_beta", append(existing, header))
}

// ensureChatToolConfigForConversation ensures toolConfig is present when tool content exists
func ensureChatToolConfigForConversation(ctx context.Context, bifrostReq *schemas.BifrostChatRequest, bedrockReq *BedrockConverseRequest) {
	if bedrockReq.ToolConfig != nil {
		return // Already has tool config
	}

	hasToolContent, tools := extractToolsFromConversationHistory(ctx, bifrostReq.Input)
	if hasToolContent && len(tools) > 0 {
		bedrockReq.ToolConfig = &BedrockToolConfig{Tools: tools}
	}
}

// convertMessages converts Bifrost messages to Bedrock format
// Returns regular messages and system messages separately.
// The ctx is propagated to URL fetches inside individual messages.
func convertMessages(ctx context.Context, bifrostMessages []schemas.ChatMessage) ([]BedrockMessage, []BedrockSystemMessage, error) {
	var messages []BedrockMessage
	var systemMessages []BedrockSystemMessage

	// if only system / developer message is there, convert it to user message (since openai allows it)
	if len(bifrostMessages) == 1 && (bifrostMessages[0].Role == schemas.ChatMessageRoleSystem || bifrostMessages[0].Role == schemas.ChatMessageRoleDeveloper) {
		msg := bifrostMessages[0]
		msg.Role = schemas.ChatMessageRoleUser
		bedrockMsg, err := convertMessage(ctx, msg)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to convert message: %w", err)
		}
		if len(bedrockMsg.Content) > 0 {
			return []BedrockMessage{bedrockMsg}, nil, nil
		}
	}

	for i := 0; i < len(bifrostMessages); i++ {
		msg := bifrostMessages[i]
		switch msg.Role {
		case schemas.ChatMessageRoleSystem, schemas.ChatMessageRoleDeveloper:
			// Convert system message
			systemMsgs, err := convertSystemMessages(msg)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to convert system message: %w", err)
			}
			systemMessages = append(systemMessages, systemMsgs...)

		case schemas.ChatMessageRoleUser, schemas.ChatMessageRoleAssistant:
			// Convert regular message
			bedrockMsg, err := convertMessage(ctx, msg)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to convert message: %w", err)
			}
			messages = append(messages, bedrockMsg)

		case schemas.ChatMessageRoleTool:
			// Collect all consecutive tool messages and group them into a single user message
			var toolMessages []schemas.ChatMessage
			toolMessages = append(toolMessages, msg)

			// Look ahead for more consecutive tool messages
			for j := i + 1; j < len(bifrostMessages) && bifrostMessages[j].Role == schemas.ChatMessageRoleTool; j++ {
				toolMessages = append(toolMessages, bifrostMessages[j])
				i = j
			}

			// Convert all collected tool messages into a single Bedrock message
			bedrockMsg, err := convertToolMessages(ctx, toolMessages)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to convert tool messages: %w", err)
			}
			messages = append(messages, bedrockMsg)

		default:
			return nil, nil, fmt.Errorf("unsupported message role: %s", msg.Role)
		}
	}

	return messages, systemMessages, nil
}

// reasoningSignatureForBedrock returns sig only when it is a non-empty string.
// A valid reasoning signature is a non-empty crypto token (Anthropic always emits
// one, and Bedrock requires it on those reasoning blocks). Other families emit an
// empty signature (MiniMax sends "") or none (Nova); echoing
// reasoningContent.reasoningText.signature:"" back 400s with "This model doesn't
// support the reasoningContent.reasoningText.signature field". Returning nil lets
// omitempty drop the field (a non-nil *string to "" would still serialize as "").
func reasoningSignatureForBedrock(sig *string) *string {
	if sig == nil || *sig == "" {
		return nil
	}
	return sig
}

// newBedrockCachePoint builds a default cache point, attaching the TTL only for the values
// Bedrock accepts ("5m" | "1h"); anything else (e.g. Anthropic's "1m") is dropped to the default.
func newBedrockCachePoint(ttl *string) *BedrockCachePoint {
	cp := &BedrockCachePoint{Type: BedrockCachePointTypeDefault}
	if ttl != nil && (*ttl == "5m" || *ttl == "1h") {
		cp.TTL = ttl
	}
	return cp
}

// convertSystemMessages converts a Bifrost system message to Bedrock format
func convertSystemMessages(msg schemas.ChatMessage) ([]BedrockSystemMessage, error) {
	systemMsgs := []BedrockSystemMessage{}

	// Convert content
	if msg.Content.ContentStr != nil {
		systemMsgs = append(systemMsgs, BedrockSystemMessage{
			Text: msg.Content.ContentStr,
		})
	} else if msg.Content.ContentBlocks != nil {
		for _, block := range msg.Content.ContentBlocks {
			// Handle Bedrock native format where type may be empty but text is set directly
			blockType := block.Type
			if blockType == "" && block.Text != nil {
				blockType = schemas.ChatContentBlockTypeText
			}

			if blockType == schemas.ChatContentBlockTypeText && block.Text != nil {
				systemMsgs = append(systemMsgs, BedrockSystemMessage{
					Text: block.Text,
				})
				if block.CacheControl != nil {
					systemMsgs = append(systemMsgs, BedrockSystemMessage{
						CachePoint: newBedrockCachePoint(block.CacheControl.TTL),
					})
				}
			} else if block.CachePoint != nil {
				// Handle standalone cache point blocks
				systemMsgs = append(systemMsgs, BedrockSystemMessage{
					CachePoint: newBedrockCachePoint(block.CachePoint.TTL),
				})
			}
		}
	}

	return systemMsgs, nil
}

// convertMessage converts a Bifrost message to Bedrock format.
// The ctx is propagated to URL fetches inside content blocks.
func convertMessage(ctx context.Context, msg schemas.ChatMessage) (BedrockMessage, error) {
	bedrockMsg := BedrockMessage{
		Role: BedrockMessageRole(msg.Role),
	}

	var contentBlocks []BedrockContentBlock

	// Add reasoning content first
	if msg.ChatAssistantMessage != nil && len(msg.ChatAssistantMessage.ReasoningDetails) > 0 {
		for _, detail := range msg.ChatAssistantMessage.ReasoningDetails {
			if detail.Type == schemas.BifrostReasoningDetailsTypeText {
				contentBlocks = append(contentBlocks, BedrockContentBlock{
					ReasoningContent: &BedrockReasoningContent{
						ReasoningText: &BedrockReasoningContentText{
							Text:      detail.Text,
							Signature: reasoningSignatureForBedrock(detail.Signature),
						},
					},
				})
			}
		}
	}

	// Convert text/image content
	if msg.Content != nil {
		textBlocks, err := convertContent(ctx, *msg.Content)
		if err != nil {
			return BedrockMessage{}, fmt.Errorf("failed to convert content: %w", err)
		}
		contentBlocks = append(contentBlocks, textBlocks...)
	}

	// Add tool calls last (for assistant messages)
	if msg.ChatAssistantMessage != nil && msg.ChatAssistantMessage.ToolCalls != nil {
		for _, toolCall := range msg.ChatAssistantMessage.ToolCalls {
			contentBlocks = append(contentBlocks, convertToolCallToContentBlock(ctx, toolCall))
		}
	}

	bedrockMsg.Content = contentBlocks
	return bedrockMsg, nil
}

// convertToolMessages converts multiple consecutive Bifrost tool messages to a single Bedrock message.
// The ctx is propagated to URL fetches inside tool result image blocks.
func convertToolMessages(ctx context.Context, msgs []schemas.ChatMessage) (BedrockMessage, error) {
	if len(msgs) == 0 {
		return BedrockMessage{}, fmt.Errorf("no tool messages provided")
	}

	bedrockMsg := BedrockMessage{
		Role: "user",
	}

	var contentBlocks []BedrockContentBlock

	for _, msg := range msgs {
		var toolResultContent []BedrockContentBlock
		if msg.Content.ContentStr != nil {
			// Bedrock expects JSON to be a parsed object, not a string
			// Validate and compact JSON without parsing into Go types (preserves key ordering)
			var buf bytes.Buffer
			if err := json.Compact(&buf, []byte(*msg.Content.ContentStr)); err != nil {
				// If it's not valid JSON, wrap it as a text block instead
				toolResultContent = append(toolResultContent, BedrockContentBlock{
					Text: msg.Content.ContentStr,
				})
			} else {
				compacted := buf.Bytes()
				// Bedrock does not accept primitives or arrays directly in the json field
				if len(compacted) > 0 && compacted[0] == '{' {
					// Objects are valid as-is
					toolResultContent = append(toolResultContent, BedrockContentBlock{
						JSON: json.RawMessage(compacted),
					})
				} else if len(compacted) > 0 && compacted[0] == '[' {
					// Arrays need to be wrapped
					wrapped := make([]byte, 0, len(compacted)+len(`{"results":}`))
					wrapped = append(wrapped, `{"results":`...)
					wrapped = append(wrapped, compacted...)
					wrapped = append(wrapped, '}')
					toolResultContent = append(toolResultContent, BedrockContentBlock{
						JSON: json.RawMessage(wrapped),
					})
				} else {
					// Primitives (string, number, boolean, null) need to be wrapped
					wrapped := make([]byte, 0, len(compacted)+len(`{"value":}`))
					wrapped = append(wrapped, `{"value":`...)
					wrapped = append(wrapped, compacted...)
					wrapped = append(wrapped, '}')
					toolResultContent = append(toolResultContent, BedrockContentBlock{
						JSON: json.RawMessage(wrapped),
					})
				}
			}
		} else if msg.Content.ContentBlocks != nil {
			for _, block := range msg.Content.ContentBlocks {
				switch block.Type {
				case schemas.ChatContentBlockTypeText:
					if block.Text != nil {
						toolResultContent = append(toolResultContent, BedrockContentBlock{
							Text: block.Text,
						})
						// Cache point must be in a separate block
						if block.CacheControl != nil {
							toolResultContent = append(toolResultContent, BedrockContentBlock{
								CachePoint: newBedrockCachePoint(block.CacheControl.TTL),
							})
						}
					}
				case schemas.ChatContentBlockTypeImage:
					if block.ImageURLStruct != nil {
						imageSource, err := convertImageToBedrockSource(ctx, block.ImageURLStruct.URL)
						if err != nil {
							return BedrockMessage{}, fmt.Errorf("failed to convert image in tool result: %w", err)
						}
						toolResultContent = append(toolResultContent, BedrockContentBlock{
							Image: imageSource,
						})
						// Cache point must be in a separate block
						if block.CacheControl != nil {
							toolResultContent = append(toolResultContent, BedrockContentBlock{
								CachePoint: newBedrockCachePoint(block.CacheControl.TTL),
							})
						}
					}
				}
			}
		}

		if msg.ChatToolMessage == nil {
			return BedrockMessage{}, fmt.Errorf("tool message missing required ChatToolMessage")
		}

		if msg.ChatToolMessage.ToolCallID == nil {
			return BedrockMessage{}, fmt.Errorf("tool message missing required ToolCallID")
		}

		// Create tool result content block for this tool message
		toolResultBlock := BedrockContentBlock{
			ToolResult: &BedrockToolResult{
				ToolUseID: *msg.ChatToolMessage.ToolCallID,
				Content:   toolResultContent,
				Status:    schemas.Ptr("success"), // Default to success
			},
		}

		contentBlocks = append(contentBlocks, toolResultBlock)
	}

	bedrockMsg.Content = contentBlocks
	return bedrockMsg, nil
}

// convertContent converts Bifrost message content to Bedrock content blocks.
// The ctx is propagated to URL fetches inside individual content blocks.
func convertContent(ctx context.Context, content schemas.ChatMessageContent) ([]BedrockContentBlock, error) {
	var contentBlocks []BedrockContentBlock
	if content.ContentStr != nil && *content.ContentStr != "" {
		// Simple text content (skip empty strings as Bedrock rejects blank text)
		contentBlocks = append(contentBlocks, BedrockContentBlock{
			Text: content.ContentStr,
		})
	} else if content.ContentBlocks != nil {
		// Multi-modal content
		for _, block := range content.ContentBlocks {
			bedrockBlocks, err := convertContentBlock(ctx, block)
			if err != nil {
				return nil, fmt.Errorf("failed to convert content block: %w", err)
			}
			contentBlocks = append(contentBlocks, bedrockBlocks...)
		}
	}

	return contentBlocks, nil
}

// convertContentBlock converts a Bifrost content block to Bedrock format.
// The ctx is propagated to URL fetches for image and document blocks.
func convertContentBlock(ctx context.Context, block schemas.ChatContentBlock) ([]BedrockContentBlock, error) {
	// Handle Bedrock native format where type may be empty but text is set directly
	// This occurs when requests are sent in Bedrock's native format (e.g., from Claude Code)
	// In Bedrock format: {"text": "hello"} vs OpenAI format: {"type": "text", "text": "hello"}
	if block.Type == "" && block.Text != nil {
		block.Type = schemas.ChatContentBlockTypeText
	}

	switch block.Type {
	case schemas.ChatContentBlockTypeText:
		// NOTE: we are doing this because LiteLLM does this for empty text blocks.
		// Ideally we should not play with the payload - we should let the provider handle it.
		// But for now, we are doing this to avoid the API error.
		// Once the world onboards on Bifrost - we should remove these shitty patterns.
		if block.Text == nil || *block.Text == "" {
			// Skip nil or empty text as Bedrock rejects blank text content blocks
			return []BedrockContentBlock{}, nil
		}
		blocks := []BedrockContentBlock{
			{
				Text: block.Text,
			},
		}
		// Cache point must be in a separate block
		if block.CacheControl != nil {
			blocks = append(blocks, BedrockContentBlock{
				CachePoint: newBedrockCachePoint(block.CacheControl.TTL),
			})
		}
		return blocks, nil

	case schemas.ChatContentBlockTypeImage:
		if block.ImageURLStruct == nil {
			return nil, fmt.Errorf("image_url block missing image_url field")
		}

		imageSource, err := convertImageToBedrockSource(ctx, block.ImageURLStruct.URL)
		if err != nil {
			return nil, fmt.Errorf("failed to convert image: %w", err)
		}
		blocks := []BedrockContentBlock{
			{
				Image: imageSource,
			},
		}
		// Cache point must be in a separate block
		if block.CacheControl != nil {
			blocks = append(blocks, BedrockContentBlock{
				CachePoint: newBedrockCachePoint(block.CacheControl.TTL),
			})
		}
		return blocks, nil

	case schemas.ChatContentBlockTypeFile:
		if block.File == nil {
			return nil, fmt.Errorf("file block missing file field")
		}

		documentSource := &BedrockDocumentSource{
			Name:   "document",
			Format: "pdf",
			Source: &BedrockDocumentSourceData{},
		}

		// Set filename (normalized for Bedrock)
		if block.File.Filename != nil {
			documentSource.Name = normalizeBedrockFilename(*block.File.Filename)
		}

		// Convert MIME type to Bedrock format
		isText := false
		if block.File.FileType != nil {
			fileType := *block.File.FileType
			switch {
			case fileType == "text/plain" || fileType == "txt":
				documentSource.Format = "txt"
				isText = true
			case fileType == "text/markdown" || fileType == "md":
				documentSource.Format = "md"
				isText = true
			case fileType == "text/html" || fileType == "html":
				documentSource.Format = "html"
				isText = true
			case fileType == "text/csv" || fileType == "csv":
				documentSource.Format = "csv"
				isText = true
			case fileType == "application/msword" || fileType == "doc":
				documentSource.Format = "doc"
			case strings.Contains(fileType, "wordprocessingml") || fileType == "docx":
				documentSource.Format = "docx"
			case fileType == "application/vnd.ms-excel" || fileType == "xls":
				documentSource.Format = "xls"
			case strings.Contains(fileType, "spreadsheetml") || fileType == "xlsx":
				documentSource.Format = "xlsx"
			case strings.Contains(fileType, "pdf") || fileType == "pdf":
				documentSource.Format = "pdf"
			}
		}

		// URL-sourced document: fetch and inline the bytes (Bedrock Converse only
		// accepts inline source bytes, not remote URLs).
		if block.File.FileURL != nil && *block.File.FileURL != "" {
			fetchedMediaType, fetchedB64, fetchErr := providerUtils.FetchAndEncodeURL(ctx, *block.File.FileURL)
			if fetchErr != nil {
				return nil, fetchErr
			}
			// Refine format from response Content-Type when present (more reliable
			// than file extension or upstream-declared media type). Normalize to
			// strip parameters (e.g. "; charset=utf-8") and lowercase the base type.
			if mt, _, err := mime.ParseMediaType(fetchedMediaType); err == nil {
				fetchedMediaType = mt
			}
			switch fetchedMediaType {
			case "application/pdf":
				documentSource.Format = "pdf"
			case "text/plain":
				documentSource.Format = "txt"
				isText = true
			case "text/markdown":
				documentSource.Format = "md"
				isText = true
			case "text/html":
				documentSource.Format = "html"
				isText = true
			case "text/csv":
				documentSource.Format = "csv"
				isText = true
			case "application/msword":
				documentSource.Format = "doc"
			case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
				documentSource.Format = "docx"
			case "application/vnd.ms-excel":
				documentSource.Format = "xls"
			case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
				documentSource.Format = "xlsx"
			}
			documentSource.Source.Bytes = &fetchedB64
			return []BedrockContentBlock{
				{
					Document: documentSource,
				},
			}, nil
		}

		// Handle file data - strip data URL prefix if present
		if block.File.FileData != nil {
			fileData := *block.File.FileData

			// Check if it's a data URL and extract raw base64
			if strings.HasPrefix(fileData, "data:") {
				urlInfo := schemas.ExtractURLTypeInfo(fileData)
				if urlInfo.DataURLWithoutPrefix != nil {
					documentSource.Source.Bytes = urlInfo.DataURLWithoutPrefix
					return []BedrockContentBlock{
						{
							Document: documentSource,
						},
					}, nil
				}
			}

			// Set text or bytes based on file type
			if isText {
				documentSource.Source.Text = &fileData // Plain text
				encoded := base64.StdEncoding.EncodeToString([]byte(fileData))
				documentSource.Source.Bytes = &encoded // Also sets Bytes
			} else {
				documentSource.Source.Bytes = &fileData
			}
		}

		return []BedrockContentBlock{
			{
				Document: documentSource,
			},
		}, nil
	case schemas.ChatContentBlockTypeInputAudio:
		// Bedrock doesn't support audio input in Converse API
		return nil, fmt.Errorf("audio input not supported in Bedrock Converse API")

	default:
		// Handle cache-point-only blocks (Type is empty but CachePoint is set)
		if block.Type == "" && block.CachePoint != nil {
			return []BedrockContentBlock{
				{
					CachePoint: newBedrockCachePoint(block.CachePoint.TTL),
				},
			}, nil
		}
		return nil, fmt.Errorf("unsupported content block type: %s", block.Type)
	}
}

// convertImageToBedrockSource converts a Bifrost image URL to Bedrock image source.
// Bedrock Converse requires inline base64 bytes - it does not accept remote URLs.
// For data: URLs (already base64), use the bytes directly. For http(s) URLs, fetch
// the image and inline it via fetchImageFromURL. The ctx is propagated to the
// fetch so request cancellation/deadlines abort in-flight downloads.
func convertImageToBedrockSource(ctx context.Context, imageURL string) (*BedrockImageSource, error) {
	sanitizedURL, err := schemas.SanitizeImageURL(imageURL)
	if err != nil {
		return nil, fmt.Errorf("failed to sanitize image URL: %w", err)
	}
	urlTypeInfo := schemas.ExtractURLTypeInfo(sanitizedURL)

	var encoded *string
	var mediaType string
	if urlTypeInfo.MediaType != nil {
		mediaType = *urlTypeInfo.MediaType
	}

	if urlTypeInfo.Type == schemas.ImageContentTypeBase64 && urlTypeInfo.DataURLWithoutPrefix != nil {
		encoded = urlTypeInfo.DataURLWithoutPrefix
	} else {
		fetchedMediaType, fetchedB64, fetchErr := providerUtils.FetchAndEncodeURL(ctx, sanitizedURL)
		if fetchErr != nil {
			return nil, fetchErr
		}
		// Prefer the response Content-Type over an extension-inferred media type.
		if fetchedMediaType != "" {
			mediaType = fetchedMediaType
		}
		encoded = &fetchedB64
	}

	if mt, _, err := mime.ParseMediaType(mediaType); err == nil {
		mediaType = mt
	}
	format := "jpeg"
	switch mediaType {
	case "image/png":
		format = "png"
	case "image/gif":
		format = "gif"
	case "image/webp":
		format = "webp"
	case "image/jpeg", "image/jpg":
		format = "jpeg"
	}

	return &BedrockImageSource{
		Format: format,
		Source: BedrockImageSourceData{
			Bytes: encoded,
		},
	}, nil
}

// convertResponseFormatToTool converts a response_format parameter to a Bedrock tool
// Returns nil if no response_format is present or if it's not a json_schema type
// Ref: https://aws.amazon.com/blogs/machine-learning/structured-data-response-with-amazon-bedrock-prompt-engineering-and-tool-use/
func convertResponseFormatToTool(
	ctx *schemas.BifrostContext,
	model string,
	params *schemas.ChatParameters,
) (*BedrockTool, any) {
	if params == nil || params.ResponseFormat == nil {
		return nil, nil
	}

	responseFormatMap, ok := schemas.SafeExtractOrderedMap(*params.ResponseFormat)
	if !ok || responseFormatMap == nil {
		return nil, nil
	}

	// Check if type is "json_schema"
	formatTypeRaw, ok := responseFormatMap.Get("type")
	if !ok {
		return nil, nil
	}
	formatType, ok := schemas.SafeExtractString(formatTypeRaw)
	if !ok || formatType != "json_schema" {
		return nil, nil
	}

	// Extract json_schema object
	jsonSchemaRaw, ok := responseFormatMap.Get("json_schema")
	if !ok {
		return nil, nil
	}
	jsonSchemaObj, ok := schemas.SafeExtractOrderedMap(jsonSchemaRaw)
	if !ok || jsonSchemaObj == nil {
		return nil, nil
	}

	schemaObj, ok := jsonSchemaObj.Get("schema")
	if !ok {
		return nil, nil
	}

	// All Bedrock models (including Anthropic) use the synthetic `bf_so_*` tool
	// path; native `output_config.format` is intentionally avoided due to
	// Converse's inconsistent support across Claude variants.

	// Extract name and schema
	toolNameRaw, hasName := jsonSchemaObj.Get("name")
	toolName, ok := schemas.SafeExtractString(toolNameRaw)
	if !hasName || !ok || toolName == "" {
		toolName = "json_response"
	}

	// Extract description from schema if available
	description := "Returns structured JSON output"
	if schemaMap, ok := schemas.SafeExtractOrderedMap(schemaObj); ok && schemaMap != nil {
		if descRaw, hasDesc := schemaMap.Get("description"); hasDesc {
			if desc, ok := schemas.SafeExtractString(descRaw); ok && desc != "" {
				description = desc
			}
		}
	} else if schemaMap, ok := schemaObj.(map[string]interface{}); ok {
		if desc, ok := schemaMap["description"].(string); ok && desc != "" {
			description = desc
		}
	}

	// set bifrost context key structured output tool name
	toolName = fmt.Sprintf("bf_so_%s", toolName)
	ctx.SetValue(schemas.BifrostContextKeyStructuredOutputToolName, toolName)

	// Create the Bedrock tool
	schemaObjBytes, err := providerUtils.MarshalSorted(schemaObj)
	if err != nil {
		return nil, nil
	}
	return &BedrockTool{
		ToolSpec: &BedrockToolSpec{
			Name:        toolName,
			Description: schemas.Ptr(description),
			InputSchema: BedrockToolInputSchema{
				JSON: json.RawMessage(schemaObjBytes),
			},
		},
	}, nil
}

// extractJSONSchemaObject returns a JSON Schema object from either the composite
// Schema field or the decomposed Type/Properties/Required/AdditionalProperties
// fields at the JSONSchema struct level. OpenAI-compat callers typically use the
// decomposed shape (matches OpenAI's flat `format.schema.{type, properties, ...}`
// wire format); explicit-composite callers use the Schema field.
//
// Returns json.RawMessage so downstream Anthropic normalization can operate on
// bytes (via NormalizeSchemaForAnthropicRaw) without a map round-trip, and so
// MarshalSorted on the result is a passthrough.
func extractJSONSchemaObject(s *schemas.ResponsesTextConfigFormatJSONSchema) json.RawMessage {
	if s == nil {
		return nil
	}
	if s.Schema != nil {
		b, err := providerUtils.MarshalSorted(*s.Schema)
		if err != nil {
			return nil
		}
		return json.RawMessage(b)
	}

	body := []byte(`{}`)
	var err error

	if s.Type != nil {
		body, err = sjson.SetBytes(body, "type", *s.Type)
		if err != nil {
			return nil
		}
	}
	if s.Properties != nil {
		propsB, mErr := providerUtils.MarshalSorted(*s.Properties)
		if mErr != nil {
			return nil
		}
		body, err = sjson.SetRawBytes(body, "properties", propsB)
		if err != nil {
			return nil
		}
	}
	if len(s.Required) > 0 {
		body, err = sjson.SetBytes(body, "required", s.Required)
		if err != nil {
			return nil
		}
	}
	if s.AdditionalProperties != nil {
		b, mErr := providerUtils.MarshalSorted(s.AdditionalProperties)
		if mErr != nil {
			return nil
		}
		body, err = sjson.SetRawBytes(body, "additionalProperties", b)
		if err != nil {
			return nil
		}
	}
	if s.Defs != nil {
		defsB, mErr := providerUtils.MarshalSorted(*s.Defs)
		if mErr != nil {
			return nil
		}
		body, err = sjson.SetRawBytes(body, "$defs", defsB)
		if err != nil {
			return nil
		}
	}
	if s.Definitions != nil {
		defsB, mErr := providerUtils.MarshalSorted(*s.Definitions)
		if mErr != nil {
			return nil
		}
		body, err = sjson.SetRawBytes(body, "definitions", defsB)
		if err != nil {
			return nil
		}
	}
	if s.Ref != nil {
		body, err = sjson.SetBytes(body, "$ref", *s.Ref)
		if err != nil {
			return nil
		}
	}
	if string(body) == `{}` {
		return nil
	}
	return json.RawMessage(body)
}

// convertTextFormatToTool converts a Responses text.format config to either a
// synthetic Bedrock tool or an Anthropic-native output_config.format value.
func convertTextFormatToTool(ctx *schemas.BifrostContext, model string, textConfig *schemas.ResponsesTextConfig) (*BedrockTool, any, error) {
	if textConfig == nil || textConfig.Format == nil {
		return nil, nil, nil
	}

	format := textConfig.Format
	if format.Type != "json_schema" {
		return nil, nil, nil
	}

	toolName := "json_response"
	if format.Name != nil && strings.TrimSpace(*format.Name) != "" {
		toolName = strings.TrimSpace(*format.Name)
	}

	description := "Returns structured JSON output"
	if format.JSONSchema == nil {
		return nil, nil, nil
	}
	_, acceptAll, err := format.JSONSchema.CompositeSchema()
	if err != nil {
		return nil, nil, err
	}
	var schemaObj json.RawMessage
	if acceptAll {
		// Boolean schema `true` accepts any value. Tool input schemas must be
		// JSON Schema objects, so the widest representable form is an
		// unconstrained object.
		schemaObj = json.RawMessage(`{"type":"object"}`)
	} else {
		// Composite object schemas are handled inside extractJSONSchemaObject.
		schemaObj = extractJSONSchemaObject(format.JSONSchema)
	}
	if schemaObj == nil {
		return nil, nil, nil // No schema info — neither composite Schema nor decomposed fields set
	}
	if format.JSONSchema.Description != nil {
		description = *format.JSONSchema.Description
	}

	// All Bedrock models use the synthetic `bf_so_*` tool path here as well.
	// See convertResponseFormatToTool for the rationale.

	toolName = fmt.Sprintf("bf_so_%s", toolName)
	ctx.SetValue(schemas.BifrostContextKeyStructuredOutputToolName, toolName)

	schemaObjBytes2, err := providerUtils.MarshalSorted(schemaObj)
	if err != nil {
		return nil, nil, nil
	}
	return &BedrockTool{
		ToolSpec: &BedrockToolSpec{
			Name:        toolName,
			Description: schemas.Ptr(description),
			InputSchema: BedrockToolInputSchema{
				JSON: json.RawMessage(schemaObjBytes2),
			},
		},
	}, nil, nil
}

// convertInferenceConfig converts Bifrost parameters to Bedrock inference config
func convertInferenceConfig(params *schemas.ChatParameters, model string) *BedrockInferenceConfig {
	var config BedrockInferenceConfig
	if params.MaxCompletionTokens != nil {
		config.MaxTokens = params.MaxCompletionTokens
	}

	if params.Temperature != nil {
		config.Temperature = params.Temperature
	}

	if params.TopP != nil {
		config.TopP = params.TopP
	}

	// GLM models on Bedrock reject the stopSequences field.
	if params.Stop != nil && !schemas.IsGLMModel(model) {
		config.StopSequences = params.Stop
	}

	return &config
}

// collectBedrockServerTools partitions kept tools into the function/custom
// set (which convertToolConfig materializes into Converse's toolConfig.tools)
// and the kept-server-tool set (which cannot be expressed via Converse's
// typed toolSpec slot and must be tunneled via additionalModelRequestFields).
//
// Returns:
//   - serverTools:  each ChatTool serialized to its Anthropic-native JSON shape
//     (e.g. `{"type":"computer_20251124","name":"computer","display_width_px":1280}`)
//     ready to drop into additionalModelRequestFields.tools. Per the comment on
//     ChatTool in core/schemas/chatcompletions.go:340-351, the default marshaler
//     produces this shape directly — no custom codec needed.
//   - betaHeaders:  anthropic-beta header values derived from the server tool
//     Types, filtered through FilterBetaHeadersForProvider(schemas.Bedrock) so
//     only Bedrock-approved headers survive. Only high-confidence mappings are
//     derived here (computer_* and memory_*); callers relying on other betas
//     (e.g. text_editor-specific headers) should continue supplying them via
//     extra-headers / ctx — they flow through bedrock.go's existing
//     anthropic-beta HTTP header path.
//
// Unsupported server tools (e.g. web_search on Bedrock) are dropped upstream
// by ValidateChatToolsForProvider, so they never reach this helper.
func collectBedrockServerTools(params *schemas.ChatParameters) (serverTools []json.RawMessage, betaHeaders []string) {
	if params == nil || len(params.Tools) == 0 {
		return nil, nil
	}
	filtered, _ := anthropic.ValidateChatToolsForProvider(params.Tools, schemas.Bedrock)
	return collectBedrockServerToolsFromFiltered(filtered)
}

// collectBedrockServerToolsFromFiltered is the inner variant that accepts a
// pre-filtered tool set (already run through ValidateChatToolsForProvider).
// convertChatParameters filters once and passes the result to both this helper
// and convertToolConfigFromFiltered to avoid re-filtering twice per request.
func collectBedrockServerToolsFromFiltered(filtered []schemas.ChatTool) (serverTools []json.RawMessage, betaHeaders []string) {
	if len(filtered) == 0 {
		return nil, nil
	}
	seenBeta := make(map[string]struct{})
	for _, tool := range filtered {
		if tool.Function != nil || tool.Custom != nil {
			continue
		}
		bytes, err := providerUtils.MarshalSorted(tool)
		if err != nil {
			continue
		}
		serverTools = append(serverTools, json.RawMessage(bytes))
		for _, h := range deriveBedrockBetaHeadersForToolType(string(tool.Type)) {
			if _, ok := seenBeta[h]; ok {
				continue
			}
			seenBeta[h] = struct{}{}
			betaHeaders = append(betaHeaders, h)
		}
	}
	if len(betaHeaders) > 0 {
		// Gate through the Bedrock-approved beta-header list.
		betaHeaders = anthropic.FilterBetaHeadersForProvider(betaHeaders, schemas.Bedrock)
	}
	return serverTools, betaHeaders
}

// buildBedrockServerToolChoice emits an Anthropic-native tool_choice value
// for tunneling through additionalModelRequestFields.tool_choice ONLY when
// Converse's typed toolConfig.toolChoice path cannot express the caller's
// intent:
//
//   - Named pin of a kept server tool: convertToolConfig builds toolConfig.tools
//     from function/custom tools only, and its reconciliation (around line
//     1274) drops any named pin that doesn't match an entry in that slice.
//     Server-tool names never appear there, so a legitimate pin like
//     tool_choice={type:"function", function:{name:"computer"}} gets silently
//     nuked. We tunnel {"type":"tool","name":"computer"} instead so the
//     forced-tool contract reaches Anthropic via Bedrock's merge.
//   - any/required with only server tools: convertToolConfig returns nil
//     entirely (empty-slice guard since bedrockTools is empty), so the typed
//     "any" contract is lost. We tunnel {"type":"any"} to preserve it.
//
// Returns (nil, false) when the typed Converse path is adequate (auto/none,
// function-tool pin, any with function tools present, or a pin whose name
// doesn't match any kept server tool).
//
// Anthropic tool_choice shape ref: platform.claude.com/docs/en/docs/agents-and-tools/tool-use/define-tools
// ("Controlling Claude's output / Forcing tool use" — four options:
// auto, any, tool, none; forced tool shape is {"type":"tool","name":"..."}).
func buildBedrockServerToolChoice(params *schemas.ChatParameters, filtered []schemas.ChatTool) (json.RawMessage, bool) {
	if params == nil || params.ToolChoice == nil {
		return nil, false
	}

	// Resolve effective type and optional pinned name from either the string
	// or struct representation of ChatToolChoice.
	var (
		choiceType schemas.ChatToolChoiceType
		pinnedName string
	)
	if params.ToolChoice.ChatToolChoiceStr != nil {
		choiceType = schemas.ChatToolChoiceType(*params.ToolChoice.ChatToolChoiceStr)
	} else if params.ToolChoice.ChatToolChoiceStruct != nil {
		s := params.ToolChoice.ChatToolChoiceStruct
		choiceType = s.Type
		if s.Function != nil {
			pinnedName = s.Function.Name
		} else if s.Custom != nil {
			pinnedName = s.Custom.Name
		}
	} else {
		return nil, false
	}

	// Partition kept tools: server-tool name set, plus whether any
	// function/custom tool is present.
	serverToolNames := make(map[string]struct{})
	hasFunctionOrCustom := false
	for _, tool := range filtered {
		if tool.Function != nil || tool.Custom != nil {
			hasFunctionOrCustom = true
			continue
		}
		if tool.Name != "" {
			serverToolNames[tool.Name] = struct{}{}
		}
	}

	switch choiceType {
	case schemas.ChatToolChoiceTypeFunction, schemas.ChatToolChoiceTypeCustom,
		schemas.ChatToolChoiceType("tool"):
		// Only tunnel when the pinned name matches a kept server tool.
		// Function/custom pins stay on the typed Converse path.
		if pinnedName == "" {
			return nil, false
		}
		if _, ok := serverToolNames[pinnedName]; !ok {
			return nil, false
		}
		bytes, err := providerUtils.MarshalSorted(map[string]any{
			"type": "tool",
			"name": pinnedName,
		})
		if err != nil {
			return nil, false
		}
		return json.RawMessage(bytes), true

	case schemas.ChatToolChoiceTypeAny, schemas.ChatToolChoiceTypeRequired:
		// When function/custom tools are present, Converse's typed
		// toolChoice.any handles the any contract — don't double-emit.
		if hasFunctionOrCustom || len(serverToolNames) == 0 {
			return nil, false
		}
		bytes, err := providerUtils.MarshalSorted(map[string]any{"type": "any"})
		if err != nil {
			return nil, false
		}
		return json.RawMessage(bytes), true

	default:
		// auto, none, allowed_tools, empty, unknown — no tunneling.
		return nil, false
	}
}

// deriveBedrockBetaHeadersForToolType maps an Anthropic server-tool Type string
// to the anthropic-beta header(s) Bedrock requires for the feature to activate.
// Only high-confidence mappings are encoded here — both are anchored in
// core/providers/anthropic/types.go (cite: B-header comments around lines 178-183).
// Unknown prefixes return nil; callers can still inject betas via extra-headers.
func deriveBedrockBetaHeadersForToolType(toolType string) []string {
	switch {
	case strings.HasPrefix(toolType, "computer_"):
		// computer_YYYYMMDD → computer-use-YYYY-MM-DD (Bedrock B-header).
		rest := strings.TrimPrefix(toolType, "computer_")
		if len(rest) == 8 {
			return []string{"computer-use-" + rest[0:4] + "-" + rest[4:6] + "-" + rest[6:8]}
		}
		return nil
	case strings.HasPrefix(toolType, "memory_"):
		// Memory activates via the context-management bundle on Bedrock
		// (see anthropic/types.go:179 — "context-management-2025-06-27 per
		// B-header (bundles memory)").
		return []string{"context-management-2025-06-27"}
	}
	return nil
}

// convertToolConfig converts Bifrost tools to Bedrock tool config.
//
// Responsibilities (split from collectBedrockServerTools):
//   - Filters server tools the target provider doesn't support via
//     ValidateChatToolsForProvider (e.g. web_search on Bedrock per cited
//     docs — AWS user guide beta-header list, Anthropic overview feature
//     table). Silently stripped.
//   - Materializes function/custom tools into Converse's typed toolConfig.tools.
//     Kept server tools (bash_*, computer_*, memory_*, text_editor_*,
//     tool_search_tool_*) are NOT emitted here — they are handled separately
//     by collectBedrockServerTools → additionalModelRequestFields.tools, since
//     Converse's toolSpec slot has no shape for them.
//   - Returns nil instead of an empty-slice ToolConfig, since Bedrock's
//     Converse API rejects `"toolConfig": {"tools": []}` with a 400.
func convertToolConfig(model string, params *schemas.ChatParameters) *BedrockToolConfig {
	if params == nil || len(params.Tools) == 0 {
		return nil
	}
	// Strip unsupported server tools before the conversion loop.
	filtered, _ := anthropic.ValidateChatToolsForProvider(params.Tools, schemas.Bedrock)
	return convertToolConfigFromFiltered(nil, model, params, filtered)
}

// convertToolConfigFromFiltered is the inner variant that accepts a
// pre-filtered tool set. convertChatParameters uses this to avoid filtering
// twice (once here, once in collectBedrockServerTools). The public
// convertToolConfig entry point is a thin wrapper preserved for tests.
//
// ctx is the BifrostContext (not context.Context) so the family gates inside
// this function can consult the resolved alias and honor explicit
// AliasConfig.ModelFamily overrides. Test paths may pass nil — family
// detection then falls back to substring matching on model.
func convertToolConfigFromFiltered(ctx *schemas.BifrostContext, model string, params *schemas.ChatParameters, filtered []schemas.ChatTool) *BedrockToolConfig {
	if params == nil {
		return nil
	}

	var bedrockTools []BedrockTool
	for _, tool := range filtered {
		if tool.Function != nil {
			// Serialize the parameters (or a default empty schema) to json.RawMessage
			var schemaObjectBytes []byte
			if tool.Function.Parameters != nil {
				// ToolFunctionParameters.MarshalJSON handles all fields including
				// properties, required, enum, additionalProperties, $defs, etc.
				var err error
				schemaObjectBytes, err = providerUtils.MarshalSorted(tool.Function.Parameters)
				if err != nil {
					continue
				}
			} else {
				// Fallback to empty object schema if no parameters
				schemaObjectBytes = []byte(`{"type":"object","properties":{}}`)
			}

			// Use the tool description if available, otherwise use a generic description
			description := "Function tool"
			if tool.Function.Description != nil {
				description = *tool.Function.Description
			}

			bedrockTool := BedrockTool{
				ToolSpec: &BedrockToolSpec{
					Name:        bedrockAliasToolName(ctx, tool.Function.Name),
					Description: new(description),
					InputSchema: BedrockToolInputSchema{
						JSON: json.RawMessage(schemaObjectBytes),
					},
				},
			}
			bedrockTools = append(bedrockTools, bedrockTool)

			if tool.CacheControl != nil && !schemas.IsNovaModelFamily(ctx, model) {
				bedrockTools = append(bedrockTools, BedrockTool{
					CachePoint: newBedrockCachePoint(tool.CacheControl.TTL),
				})
			}
		}
	}

	// Empty-guard: Bedrock's Converse API rejects {"toolConfig": {"tools": []}}
	// with a 400 "The provided request is not valid". If every incoming tool
	// was filtered out above (e.g. only server tools the target provider
	// doesn't support), omit ToolConfig entirely so the request is valid and
	// the model simply answers without tool access.
	if len(bedrockTools) == 0 {
		return nil
	}

	toolConfig := &BedrockToolConfig{
		Tools: bedrockTools,
	}

	// Convert tool choice
	if params.ToolChoice != nil {
		toolChoice := convertToolChoice(*params.ToolChoice)
		if toolChoice != nil {
			if toolChoice.Tool != nil && toolChoice.Tool.Name != "" {
				toolChoice.Tool.Name = bedrockAliasToolName(ctx, toolChoice.Tool.Name)
			}
			// Reconcile: if the choice forces a specific tool by name,
			// verify that name still exists in the filtered tool set.
			// Without this, a caller that pinned a server tool we just
			// stripped (e.g. web_search on Bedrock) would ship a
			// toolChoice.tool.name ∉ tools, and Bedrock's Converse API
			// rejects that with a 400 ValidationException — defeating
			// the silent-strip contract.
			if toolChoice.Tool != nil && toolChoice.Tool.Name != "" {
				found := false
				for _, bt := range bedrockTools {
					if bt.ToolSpec != nil && bt.ToolSpec.Name == toolChoice.Tool.Name {
						found = true
						break
					}
				}
				if !found {
					toolChoice = nil
				}
			}
			// Per-model gate: Bedrock Converse rejects toolConfig.toolChoice.tool
			// on Meta Llama variants ("This model doesn't support the
			// toolConfig.toolChoice.tool field"). Drop the forced specific-tool
			// pin on Llama; the bound tool list is unaffected so the model can
			// still call the intended tool under Bedrock's default "auto"
			// behavior. See per-model support matrix at
			// https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_ToolChoice.html
			// (mirrors the synthetic-tool gate in convertChatParameters).
			if toolChoice != nil && toolChoice.Tool != nil && schemas.IsLlamaModelFamily(ctx, model) {
				toolChoice = nil
			}
			if toolChoice != nil {
				toolConfig.ToolChoice = toolChoice
			}
		}
	}

	return toolConfig
}

// convertToolChoice converts Bifrost tool choice to Bedrock format
func convertToolChoice(toolChoice schemas.ChatToolChoice) *BedrockToolChoice {
	// String variant
	if toolChoice.ChatToolChoiceStr != nil {
		switch schemas.ChatToolChoiceType(*toolChoice.ChatToolChoiceStr) {
		case schemas.ChatToolChoiceTypeAuto:
			// Auto is Bedrock's default behavior - omit ToolChoice
			return nil
		case schemas.ChatToolChoiceTypeAny, schemas.ChatToolChoiceTypeRequired:
			return &BedrockToolChoice{Any: &BedrockToolChoiceAny{}}
		case schemas.ChatToolChoiceTypeNone:
			// Bedrock doesn't have explicit "none" - omit ToolChoice
			return nil
		case schemas.ChatToolChoiceTypeFunction:
			// Not representable without a name; expect struct form instead.
			return nil
		}
	}
	// Struct variant
	if toolChoice.ChatToolChoiceStruct != nil {
		switch toolChoice.ChatToolChoiceStruct.Type {
		case schemas.ChatToolChoiceTypeFunction:
			name := ""
			if toolChoice.ChatToolChoiceStruct.Function != nil {
				name = toolChoice.ChatToolChoiceStruct.Function.Name
			}
			if name != "" {
				return &BedrockToolChoice{
					Tool: &BedrockToolChoiceTool{Name: name},
				}
			}
			return nil
		case schemas.ChatToolChoiceTypeAny, schemas.ChatToolChoiceTypeRequired:
			return &BedrockToolChoice{Any: &BedrockToolChoiceAny{}}
		case schemas.ChatToolChoiceTypeNone:
			return nil
		}
	}
	return nil
}

// extractToolsFromConversationHistory analyzes conversation history for tool content
func extractToolsFromConversationHistory(ctx context.Context, messages []schemas.ChatMessage) (bool, []BedrockTool) {
	hasToolContent := false
	toolsMap := make(map[string]BedrockTool)

	for _, msg := range messages {
		hasToolContent = checkMessageForToolContent(ctx, msg, toolsMap) || hasToolContent
	}

	tools := make([]BedrockTool, 0, len(toolsMap))
	for _, tool := range toolsMap {
		tools = append(tools, tool)
	}

	return hasToolContent, tools
}

// checkMessageForToolContent checks a single message for tool content and updates the tools map
func checkMessageForToolContent(ctx context.Context, msg schemas.ChatMessage, toolsMap map[string]BedrockTool) bool {
	hasContent := false

	// Check assistant tool calls
	if msg.ChatAssistantMessage != nil && msg.ChatAssistantMessage.ToolCalls != nil {
		hasContent = true
		for _, toolCall := range msg.ChatAssistantMessage.ToolCalls {
			if toolCall.Function.Name != nil {
				toolName := bedrockAliasToolName(ctx, *toolCall.Function.Name)
				if _, exists := toolsMap[toolName]; !exists {
					// Create a complete schema object for extracted tools
					schemaObject := map[string]interface{}{
						"type":       "object",
						"properties": map[string]interface{}{},
					}
					extractedSchemaBytes, _ := providerUtils.MarshalSorted(schemaObject)

					toolsMap[toolName] = BedrockTool{
						ToolSpec: &BedrockToolSpec{
							Name:        toolName,
							Description: schemas.Ptr("Tool extracted from conversation history"),
							InputSchema: BedrockToolInputSchema{
								JSON: json.RawMessage(extractedSchemaBytes),
							},
						},
					}
				}
			}
		}
	}

	// Check tool messages
	if msg.ChatToolMessage != nil && msg.ChatToolMessage.ToolCallID != nil {
		hasContent = true
	}

	// Check content blocks
	if msg.Content != nil && msg.Content.ContentBlocks != nil {
		for _, block := range msg.Content.ContentBlocks {
			if block.Type == "tool_use" || block.Type == "tool_result" {
				hasContent = true
			}
		}
	}

	return hasContent
}

// convertToolCallToContentBlock converts a Bifrost tool call to a Bedrock content block
func convertToolCallToContentBlock(ctx context.Context, toolCall schemas.ChatAssistantMessageToolCall) BedrockContentBlock {
	toolUseID := ""
	if toolCall.ID != nil {
		toolUseID = *toolCall.ID
	}

	toolName := ""
	if toolCall.Function.Name != nil {
		toolName = bedrockAliasToolName(ctx, *toolCall.Function.Name)
	}

	// Preserve original key ordering of tool arguments for prompt caching.
	// Using json.RawMessage avoids the map[string]interface{} round-trip
	// that would destroy key order.
	var input json.RawMessage
	args := strings.TrimSpace(toolCall.Function.Arguments)
	if args == "" {
		input = json.RawMessage("{}")
	} else {
		var buf bytes.Buffer
		if err := json.Compact(&buf, []byte(args)); err == nil {
			input = buf.Bytes()
		} else {
			// invalid json recieved
			input = json.RawMessage("{}")
		}
	}

	return BedrockContentBlock{
		ToolUse: &BedrockToolUse{
			ToolUseID: toolUseID,
			Name:      toolName,
			Input:     input,
		},
	}
}

// ToBedrockError converts a BifrostError to BedrockError
// This is a standalone function similar to ToAnthropicChatCompletionError
func ToBedrockError(bifrostErr *schemas.BifrostError) *BedrockError {
	if bifrostErr == nil || bifrostErr.Error == nil {
		return &BedrockError{
			Type:    "InternalServerError",
			Message: "unknown error",
		}
	}

	// Safely extract message from nested error
	message := ""
	if bifrostErr.Error != nil {
		message = bifrostErr.Error.Message
	}

	bedrockErr := &BedrockError{
		Message: message,
	}

	// Map error type/code
	if bifrostErr.Error != nil && bifrostErr.Error.Code != nil {
		bedrockErr.Type = *bifrostErr.Error.Code
		bedrockErr.Code = bifrostErr.Error.Code
	} else if bifrostErr.Type != nil {
		bedrockErr.Type = *bifrostErr.Type
	} else {
		bedrockErr.Type = "InternalServerError"
	}

	return bedrockErr
}

// convertMapToToolFunctionParameters converts a map[string]interface{} to ToolFunctionParameters
// This handles the conversion from flexible parameter formats to Bifrost's structured format
func convertMapToToolFunctionParameters(paramsMap map[string]interface{}) *schemas.ToolFunctionParameters {
	if paramsMap == nil {
		return nil
	}

	params := &schemas.ToolFunctionParameters{}

	// Extract type
	if typeVal, ok := paramsMap["type"].(string); ok {
		params.Type = typeVal
	}

	// Extract description
	if descVal, ok := paramsMap["description"].(string); ok {
		params.Description = &descVal
	}

	// Extract properties
	if props, ok := schemas.SafeExtractOrderedMap(paramsMap["properties"]); ok {
		params.Properties = props
	}

	// Extract required
	if required, ok := paramsMap["required"].([]interface{}); ok {
		reqStrings := make([]string, 0, len(required))
		for _, r := range required {
			if rStr, ok := r.(string); ok {
				reqStrings = append(reqStrings, rStr)
			}
		}
		params.Required = reqStrings
	} else if required, ok := paramsMap["required"].([]string); ok {
		params.Required = required
	}

	// Extract enum
	if enumVal, ok := paramsMap["enum"].([]interface{}); ok {
		enum := make([]string, 0, len(enumVal))
		for _, v := range enumVal {
			if s, ok := v.(string); ok {
				enum = append(enum, s)
			}
		}
		params.Enum = enum
	}

	// Extract additionalProperties
	if addPropsVal, ok := paramsMap["additionalProperties"].(bool); ok {
		params.AdditionalProperties = &schemas.AdditionalPropertiesStruct{
			AdditionalPropertiesBool: &addPropsVal,
		}
	} else if addPropsVal, ok := schemas.SafeExtractOrderedMap(paramsMap["additionalProperties"]); ok {
		params.AdditionalProperties = &schemas.AdditionalPropertiesStruct{
			AdditionalPropertiesMap: addPropsVal,
		}
	}

	// Extract $defs (JSON Schema draft 2019-09+)
	if defsVal, ok := schemas.SafeExtractOrderedMap(paramsMap["$defs"]); ok {
		params.Defs = defsVal
	}

	// Extract definitions (legacy JSON Schema draft-07)
	if defsVal, ok := schemas.SafeExtractOrderedMap(paramsMap["definitions"]); ok {
		params.Definitions = defsVal
	}

	// Extract $ref
	if refVal, ok := paramsMap["$ref"].(string); ok {
		params.Ref = &refVal
	}

	// Extract items (array element schema)
	if itemsVal, ok := schemas.SafeExtractOrderedMap(paramsMap["items"]); ok {
		params.Items = itemsVal
	}

	// Extract minItems
	if minItemsVal, ok := bedrockExtractInt64(paramsMap["minItems"]); ok {
		params.MinItems = &minItemsVal
	}

	// Extract maxItems
	if maxItemsVal, ok := bedrockExtractInt64(paramsMap["maxItems"]); ok {
		params.MaxItems = &maxItemsVal
	}

	// Extract anyOf
	if anyOfVal, ok := paramsMap["anyOf"].([]interface{}); ok {
		anyOf := make([]schemas.OrderedMap, 0, len(anyOfVal))
		for _, v := range anyOfVal {
			if m, ok := schemas.SafeExtractOrderedMap(v); ok {
				anyOf = append(anyOf, *m)
			}
		}
		params.AnyOf = anyOf
	}

	// Extract oneOf
	if oneOfVal, ok := paramsMap["oneOf"].([]interface{}); ok {
		oneOf := make([]schemas.OrderedMap, 0, len(oneOfVal))
		for _, v := range oneOfVal {
			if m, ok := schemas.SafeExtractOrderedMap(v); ok {
				oneOf = append(oneOf, *m)
			}
		}
		params.OneOf = oneOf
	}

	// Extract allOf
	if allOfVal, ok := paramsMap["allOf"].([]interface{}); ok {
		allOf := make([]schemas.OrderedMap, 0, len(allOfVal))
		for _, v := range allOfVal {
			if m, ok := schemas.SafeExtractOrderedMap(v); ok {
				allOf = append(allOf, *m)
			}
		}
		params.AllOf = allOf
	}

	// Extract format
	if formatVal, ok := paramsMap["format"].(string); ok {
		params.Format = &formatVal
	}

	// Extract pattern
	if patternVal, ok := paramsMap["pattern"].(string); ok {
		params.Pattern = &patternVal
	}

	// Extract minLength
	if minLengthVal, ok := bedrockExtractInt64(paramsMap["minLength"]); ok {
		params.MinLength = &minLengthVal
	}

	// Extract maxLength
	if maxLengthVal, ok := bedrockExtractInt64(paramsMap["maxLength"]); ok {
		params.MaxLength = &maxLengthVal
	}

	// Extract minimum
	if minVal, ok := bedrockExtractFloat64(paramsMap["minimum"]); ok {
		params.Minimum = &minVal
	}

	// Extract maximum
	if maxVal, ok := bedrockExtractFloat64(paramsMap["maximum"]); ok {
		params.Maximum = &maxVal
	}

	// Extract title
	if titleVal, ok := paramsMap["title"].(string); ok {
		params.Title = &titleVal
	}

	// Extract default
	if defaultVal, exists := paramsMap["default"]; exists {
		params.Default = defaultVal
	}

	// Extract nullable
	if nullableVal, ok := paramsMap["nullable"].(bool); ok {
		params.Nullable = &nullableVal
	}

	return params
}

// bedrockExtractInt64 extracts an int64 from various numeric types
func bedrockExtractInt64(v interface{}) (int64, bool) {
	switch val := v.(type) {
	case int:
		return int64(val), true
	case int64:
		return val, true
	case float64:
		return int64(val), true
	case float32:
		return int64(val), true
	default:
		return 0, false
	}
}

// bedrockExtractFloat64 extracts a float64 from various numeric types
func bedrockExtractFloat64(v interface{}) (float64, bool) {
	switch val := v.(type) {
	case float64:
		return val, true
	case float32:
		return float64(val), true
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	default:
		return 0, false
	}
}

// bedrockToolResultEnvelopeKey marks a sentinel-wrapped JSON string that carries a full
// BedrockToolResult.Content array through Bifrost's intermediate format. Used when the
// content includes blocks (e.g. searchResult) that the intermediate cannot model natively,
// so they round-trip losslessly on the Bedrock-native passthrough endpoint.
const bedrockToolResultEnvelopeKey = "__bifrost_bedrock_tool_result_content__"

// encodeBedrockToolResultEnvelope serializes a BedrockToolResult.Content array into a
// sentinel-wrapped JSON object that decodeBedrockToolResultEnvelope can recover.
func encodeBedrockToolResultEnvelope(content []BedrockContentBlock) (string, error) {
	envelope := map[string]any{bedrockToolResultEnvelopeKey: content}
	b, err := sonic.Marshal(envelope)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// decodeBedrockToolResultEnvelope is the inverse of encodeBedrockToolResultEnvelope.
// Returns (blocks, true) if s is a sentinel-wrapped tool-result envelope; (nil, false) otherwise.
// Non-envelope strings are returned untouched so the caller can fall through to tryParseJSONIntoContentBlock.
func decodeBedrockToolResultEnvelope(s string) ([]BedrockContentBlock, bool) {
	if len(s) == 0 || s[0] != '{' || !strings.Contains(s, bedrockToolResultEnvelopeKey) {
		return nil, false
	}
	var envelope map[string]json.RawMessage
	if err := sonic.UnmarshalString(s, &envelope); err != nil {
		return nil, false
	}
	raw, ok := envelope[bedrockToolResultEnvelopeKey]
	if !ok || len(envelope) != 1 {
		return nil, false
	}
	var blocks []BedrockContentBlock
	if err := sonic.Unmarshal(raw, &blocks); err != nil {
		return nil, false
	}
	return blocks, true
}

// tryParseJSONIntoContentBlock try to parse input text into a JSON and returns a proper
// BedrockContentBlock based on the result.
func tryParseJSONIntoContentBlock(text string) BedrockContentBlock {
	// Validate and compact JSON without parsing into Go types (preserves key ordering)
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(text)); err != nil {
		return BedrockContentBlock{Text: schemas.Ptr(text)}
	}
	compacted := buf.Bytes()

	// Bedrock does not accept primitives or arrays directly in the json field
	if len(compacted) > 0 && compacted[0] == '{' {
		// Objects are valid as-is
		return BedrockContentBlock{JSON: json.RawMessage(compacted)}
	} else if len(compacted) > 0 && compacted[0] == '[' {
		// Arrays need to be wrapped
		wrapped := make([]byte, 0, len(compacted)+len(`{"results":}`))
		wrapped = append(wrapped, `{"results":`...)
		wrapped = append(wrapped, compacted...)
		wrapped = append(wrapped, '}')
		return BedrockContentBlock{JSON: json.RawMessage(wrapped)}
	} else {
		// Primitives (string, number, boolean, null) need to be wrapped
		wrapped := make([]byte, 0, len(compacted)+len(`{"value":}`))
		wrapped = append(wrapped, `{"value":`...)
		wrapped = append(wrapped, compacted...)
		wrapped = append(wrapped, '}')
		return BedrockContentBlock{JSON: json.RawMessage(wrapped)}
	}
}

// stripCachePointsFromBedrockRequest removes all CachePoint blocks from a
// BedrockConverseRequest. Called for models that don't support prompt caching
// (e.g. GLM, Llama) so their requests don't get a 400 from the Converse API.
func stripCachePointsFromBedrockRequest(req *BedrockConverseRequest) {
	// Strip cache points from message content blocks (including nested tool results).
	for i := range req.Messages {
		content := req.Messages[i].Content
		n := 0
		for j := range content {
			if content[j].CachePoint != nil {
				continue
			}
			if content[j].ToolResult != nil {
				inner := content[j].ToolResult.Content
				m := 0
				for k := range inner {
					if inner[k].CachePoint == nil {
						inner[m] = inner[k]
						m++
					}
				}
				content[j].ToolResult.Content = inner[:m]
			}
			content[n] = content[j]
			n++
		}
		req.Messages[i].Content = content[:n]
	}
	// Strip cache points from system messages.
	// Filter out entries that were cache-point-only (would become empty objects).
	ns := 0
	for i := range req.System {
		req.System[i].CachePoint = nil
		if req.System[i].Text != nil || req.System[i].GuardContent != nil {
			req.System[ns] = req.System[i]
			ns++
		}
	}
	req.System = req.System[:ns]
	// Strip cache points from tools.
	if req.ToolConfig != nil {
		nt := 0
		for i := range req.ToolConfig.Tools {
			req.ToolConfig.Tools[i].CachePoint = nil
			if req.ToolConfig.Tools[i].ToolSpec != nil || req.ToolConfig.Tools[i].SystemTool != nil {
				req.ToolConfig.Tools[nt] = req.ToolConfig.Tools[i]
				nt++
			}
		}
		req.ToolConfig.Tools = req.ToolConfig.Tools[:nt]
	}
}

// downgradeExtendedCacheTTLInBedrockRequest drops the 1h (extended) cache TTL to
// the default for models that support cache points but not extended TTL (e.g. Nova),
// which otherwise 400 with "Extended TTL prompt caching is only supported for
// Anthropic models". Only 1h TTLs are touched; cache points themselves are kept.
func downgradeExtendedCacheTTLInBedrockRequest(req *BedrockConverseRequest) {
	downgrade := func(cp *BedrockCachePoint) {
		if cp != nil && cp.TTL != nil && *cp.TTL == string(BedrockCacheWriteTTL1h) {
			cp.TTL = nil
		}
	}
	for i := range req.Messages {
		for j := range req.Messages[i].Content {
			downgrade(req.Messages[i].Content[j].CachePoint)
			if req.Messages[i].Content[j].ToolResult != nil {
				for k := range req.Messages[i].Content[j].ToolResult.Content {
					downgrade(req.Messages[i].Content[j].ToolResult.Content[k].CachePoint)
				}
			}
		}
	}
	for i := range req.System {
		downgrade(req.System[i].CachePoint)
	}
	if req.ToolConfig != nil {
		for i := range req.ToolConfig.Tools {
			downgrade(req.ToolConfig.Tools[i].CachePoint)
		}
	}
}

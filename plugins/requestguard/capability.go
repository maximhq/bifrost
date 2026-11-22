package requestguard

import (
	"reflect"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

// EvaluateCapabilities checks only the request features enabled in cfg. Missing
// catalog signals are governed by UnknownPolicy; Mode controls enforcement of
// both known failures and unknowns.
func EvaluateCapabilities(catalog Catalog, cfg CapabilityGuardConfig, features RequestFeatures) CapabilityResult {
	result := CapabilityResult{Action: CapabilityActionAllow, Pass: true}
	if !features.Supported || cfg.Mode == PolicyOff || cfg.Mode == "" {
		return result
	}

	var caps *schemas.ModelCapabilities
	var modelInfo *schemas.Model
	var pricingEntry *modelcatalog.PricingEntry
	if !isNilCatalog(catalog) {
		caps = catalog.GetModelCapabilities(features.Provider, features.Model)
		modelInfo = catalog.GetModelInfo(features.Provider, features.Model)
		pricingEntry = catalog.GetPricingEntryForModel(features.Model, features.Provider)
	}
	knownModel := caps != nil || modelInfo != nil || pricingEntry != nil

	addUnsupported := func(capability, reason string) {
		addCapabilityIssue(&result, CapabilityIssue{
			Capability: capability,
			Severity:   severityForMode(cfg.Mode),
			Reason:     reason,
		})
	}
	addUnknown := func(capability, reason string) {
		policy := cfg.UnknownPolicy
		if policy == "" {
			policy = UnknownCapabilityAllow
		}
		switch policy {
		case UnknownCapabilityAllow:
			return
		case UnknownCapabilityWarn:
			addCapabilityIssue(&result, CapabilityIssue{
				Capability: capability,
				Severity:   IssueSeverityWarning,
				Reason:     reason,
				Unknown:    true,
			})
		case UnknownCapabilityBlock:
			addCapabilityIssue(&result, CapabilityIssue{
				Capability: capability,
				Severity:   severityForMode(cfg.Mode),
				Reason:     reason,
				Unknown:    true,
			})
		default:
			// An invalid policy should fail open here. Config validation rejects
			// it at startup; the evaluator remains safe for direct callers.
			return
		}
	}

	if cfg.CheckTools && features.RequiresTools {
		checkCapabilityFlag("tools", capsFlag(caps, func(c *schemas.ModelCapabilities) *bool { return c.SupportsFunctionCalling }),
			"model does not declare function-calling support", addUnsupported, addUnknown)
	}
	if cfg.CheckParallelTools && features.RequiresParallelTools {
		checkCapabilityFlag("parallel_tools", capsFlag(caps, func(c *schemas.ModelCapabilities) *bool { return c.SupportsParallelFunctionCalling }),
			"model does not declare parallel function-calling support", addUnsupported, addUnknown)
	}
	if cfg.CheckToolChoice && features.RequiresToolChoice {
		checkCapabilityFlag("tool_choice", capsFlag(caps, func(c *schemas.ModelCapabilities) *bool { return c.SupportsToolChoice }),
			"model does not declare tool-choice support", addUnsupported, addUnknown)
		if features.RequiresForcedToolChoice {
			checkCapabilityFlag("forced_tool_choice", capsFlag(caps, func(c *schemas.ModelCapabilities) *bool { return c.SupportsForcedToolChoice }),
				"model does not declare forced tool-choice support", addUnsupported, addUnknown)
		}
	}
	if cfg.CheckStructuredOutput && features.RequiresStructuredOutput {
		responseSchema := capsFlag(caps, func(c *schemas.ModelCapabilities) *bool { return c.SupportsResponseSchema })
		nativeStructured := capsFlag(caps, func(c *schemas.ModelCapabilities) *bool { return c.SupportsNativeStructuredOutput })
		switch {
		case isTrue(responseSchema) || isTrue(nativeStructured):
			// Either catalog signal indicates structured-output support.
		case responseSchema != nil && !*responseSchema && nativeStructured != nil && !*nativeStructured:
			addUnsupported("structured_output", "model does not support structured output")
		default:
			addUnknown("structured_output", "structured-output support is not listed in the model catalog")
		}
		if features.RequiresStructuredOutputWithTools {
			checkCapabilityFlag("structured_output_with_tools", capsFlag(caps, func(c *schemas.ModelCapabilities) *bool { return c.SupportsResponseSchemaWithTools }),
				"model does not support structured output with tools", addUnsupported, addUnknown)
		}
	}
	if cfg.CheckVision && features.RequiresVision {
		supportsImage, hasModalitySignal := modelSupportsImageInput(modelInfo, pricingEntry)
		switch {
		case supportsImage:
			// Image input is explicitly advertised.
		case hasModalitySignal:
			addUnsupported("vision", "model does not advertise image input support")
		default:
			addUnknown("vision", "image input support is not listed in the model catalog")
		}
	}
	if cfg.CheckStreaming && features.IsStreaming {
		if !knownModel {
			addUnknown("streaming", "model is not present in the model catalog")
		} else if !supportsRequestEndpoint(catalog, features.Model, features.Provider, features.RequestType, caps) {
			addUnsupported("streaming", "model does not advertise support for this streaming endpoint")
		}
	}
	if cfg.CheckMaxOutputTokens && features.RequestedMaxOutputTokens > 0 {
		maxOutputTokens := modelMaxOutputTokens(caps, modelInfo, pricingEntry)
		switch {
		case maxOutputTokens == nil:
			addUnknown("max_output_tokens", "maximum output tokens are not listed in the model catalog")
		case features.RequestedMaxOutputTokens > *maxOutputTokens:
			addUnsupported("max_output_tokens", "requested maximum output tokens exceed the model limit")
		}
	}
	finalizeCapabilityResult(&result, cfg.Mode)
	return result
}

// EvaluateContextLimit checks an estimated request against catalog input and
// context-window limits. Request estimates are scalar snapshots, so this helper
// does not retain any part of the pooled request.
func EvaluateContextLimit(catalog Catalog, cfg CapabilityGuardConfig, features RequestFeatures, estimate CostEstimate) *CapabilityIssue {
	if !cfg.CheckContextLimit || cfg.Mode == PolicyOff || cfg.Mode == "" || !features.Supported {
		return nil
	}
	if isNilCatalog(catalog) {
		return contextLimitUnknown(cfg, "model context limits are unavailable because the model catalog is unavailable")
	}

	modelInfo := catalog.GetModelInfo(features.Provider, features.Model)
	entry := catalog.GetPricingEntryForModel(features.Model, features.Provider)
	maxInput := modelMaxInputTokens(modelInfo, entry)
	contextLength := modelContextLength(modelInfo, entry)

	var exceeded []string
	if maxInput != nil && estimate.InputTokens > *maxInput {
		exceeded = append(exceeded, "estimated input tokens exceed the model input limit")
	}
	if contextLength != nil && estimate.TotalTokens > *contextLength {
		exceeded = append(exceeded, "estimated total tokens exceed the model context window")
	}
	if len(exceeded) > 0 {
		return &CapabilityIssue{
			Capability: "context_limit",
			Severity:   severityForMode(cfg.Mode),
			Reason:     strings.Join(exceeded, "; "),
		}
	}
	if maxInput == nil || contextLength == nil {
		missing := "model input limit and context length are not listed in the model catalog"
		if maxInput == nil && contextLength != nil {
			missing = "model input limit is not listed in the model catalog"
		} else if maxInput != nil && contextLength == nil {
			missing = "model context length is not listed in the model catalog"
		}
		return contextLimitUnknown(cfg, missing)
	}
	return nil
}

func contextLimitUnknown(cfg CapabilityGuardConfig, reason string) *CapabilityIssue {
	policy := cfg.UnknownPolicy
	if policy == "" {
		policy = UnknownCapabilityAllow
	}
	switch policy {
	case UnknownCapabilityAllow:
		return nil
	case UnknownCapabilityWarn:
		return &CapabilityIssue{Capability: "context_limit", Severity: IssueSeverityWarning, Reason: reason, Unknown: true}
	case UnknownCapabilityBlock:
		return &CapabilityIssue{Capability: "context_limit", Severity: severityForMode(cfg.Mode), Reason: reason, Unknown: true}
	default:
		return nil
	}
}

func isNilCatalog(catalog Catalog) bool {
	if catalog == nil {
		return true
	}
	value := reflect.ValueOf(catalog)
	return (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface) && value.IsNil()
}

func capsFlag(caps *schemas.ModelCapabilities, field func(*schemas.ModelCapabilities) *bool) *bool {
	if caps == nil {
		return nil
	}
	return field(caps)
}

func checkCapabilityFlag(name string, value *bool, unsupportedReason string,
	addUnsupported func(string, string), addUnknown func(string, string),
) {
	switch {
	case value == nil:
		addUnknown(name, ""+name+" support is not listed in the model catalog")
	case !*value:
		addUnsupported(name, unsupportedReason)
	}
}

func isTrue(value *bool) bool {
	return value != nil && *value
}

func modelSupportsImageInput(modelInfo *schemas.Model, entry *modelcatalog.PricingEntry) (bool, bool) {
	hasSignal := false
	if modelInfo != nil && modelInfo.Architecture != nil && len(modelInfo.Architecture.InputModalities) > 0 {
		hasSignal = true
		if containsImageModality(modelInfo.Architecture.InputModalities) {
			return true, true
		}
	}
	if entry != nil && entry.Architecture != nil && len(entry.Architecture.InputModalities) > 0 {
		hasSignal = true
		if containsImageModality(entry.Architecture.InputModalities) {
			return true, true
		}
	}
	return false, hasSignal
}

func containsImageModality(modalities []string) bool {
	for _, modality := range modalities {
		if strings.EqualFold(strings.TrimSpace(modality), "image") {
			return true
		}
	}
	return false
}

func supportsStreamingEndpoint(caps *schemas.ModelCapabilities, requestType schemas.RequestType) bool {
	if caps == nil {
		return false
	}
	var endpoint string
	switch requestType {
	case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest:
		endpoint = "/v1/chat/completions"
	case schemas.ResponsesRequest, schemas.ResponsesStreamRequest:
		endpoint = "/v1/responses"
	default:
		return false
	}
	for _, supportedEndpoint := range caps.SupportedEndpoints {
		if strings.EqualFold(strings.TrimRight(strings.TrimSpace(supportedEndpoint), "/"), strings.TrimRight(endpoint, "/")) {
			return true
		}
	}
	return false
}

// supportsRequestEndpoint accepts the catalog's request-type index or its
// endpoint metadata fallback. The same predicate is used for unary and stream
// candidates so recommender eligibility matches capability evaluation.
func supportsRequestEndpoint(catalog Catalog, model string, provider schemas.ModelProvider, requestType schemas.RequestType, caps *schemas.ModelCapabilities) bool {
	if !isNilCatalog(catalog) && catalog.IsRequestTypeSupported(model, provider, requestType) {
		return true
	}
	return supportsStreamingEndpoint(caps, requestType)
}

func modelMaxOutputTokens(caps *schemas.ModelCapabilities, modelInfo *schemas.Model, entry *modelcatalog.PricingEntry) *int {
	if caps != nil && caps.MaxOutputTokens != nil && *caps.MaxOutputTokens > 0 {
		return caps.MaxOutputTokens
	}
	if entry != nil && entry.MaxOutputTokens != nil && *entry.MaxOutputTokens > 0 {
		return entry.MaxOutputTokens
	}
	if modelInfo != nil && modelInfo.MaxOutputTokens != nil && *modelInfo.MaxOutputTokens > 0 {
		return modelInfo.MaxOutputTokens
	}
	return nil
}

func modelContextLength(modelInfo *schemas.Model, entry *modelcatalog.PricingEntry) *int {
	if modelInfo != nil && modelInfo.ContextLength != nil && *modelInfo.ContextLength > 0 {
		return modelInfo.ContextLength
	}
	if entry != nil && entry.ContextLength != nil && *entry.ContextLength > 0 {
		return entry.ContextLength
	}
	return nil
}

func modelMaxInputTokens(modelInfo *schemas.Model, entry *modelcatalog.PricingEntry) *int {
	if modelInfo != nil && modelInfo.MaxInputTokens != nil && *modelInfo.MaxInputTokens > 0 {
		return modelInfo.MaxInputTokens
	}
	if entry != nil && entry.MaxInputTokens != nil && *entry.MaxInputTokens > 0 {
		return entry.MaxInputTokens
	}
	return nil
}

func severityForMode(mode PolicyMode) string {
	switch mode {
	case PolicyBlock:
		return IssueSeverityBlock
	case PolicyDowngrade:
		return IssueSeverityDowngrade
	case PolicyWarn:
		return IssueSeverityWarning
	case PolicyAudit:
		return IssueSeverityAudit
	default:
		return IssueSeverityWarning
	}
}

func addCapabilityIssue(result *CapabilityResult, issue CapabilityIssue) {
	result.Issues = append(result.Issues, issue)
	switch issue.Severity {
	case IssueSeverityBlock:
		result.Blocked = true
		result.Pass = false
	case IssueSeverityDowngrade:
		result.NeedsDowngrade = true
		result.Pass = false
	}
}

func finalizeCapabilityResult(result *CapabilityResult, mode PolicyMode) {
	switch {
	case result.Blocked:
		result.Action = CapabilityActionBlock
	case result.NeedsDowngrade:
		result.Action = CapabilityActionDowngrade
	case len(result.Issues) == 0:
		result.Action = CapabilityActionAllow
	case mode == PolicyAudit:
		result.Action = CapabilityActionAudit
	default:
		result.Action = CapabilityActionWarn
	}
}

package schemas

import (
	"slices"
	"strings"
	"sync/atomic"
)

// capabilityResolver answers capability lookups. The catalog that owns the
// datasheet lives above core and installs it at startup, so core holds a
// function pointer rather than a cache: everything about how records are keyed,
// loaded, cached and invalidated belongs next to the data.
var capabilityResolver atomic.Pointer[func(ModelProvider, string) *ModelCapabilities]

// SetCapabilityResolver installs the lookup backing CapabilitiesFor. Passing nil
// clears it, after which every capability check falls back to the hardcoded
// per-provider helpers.
func SetCapabilityResolver(fn func(provider ModelProvider, model string) *ModelCapabilities) {
	if fn == nil {
		capabilityResolver.Store(nil)
		return
	}
	capabilityResolver.Store(&fn)
}

// CapabilitiesFor returns the capability record for a (provider, model) pair, or
// nil when no resolver is installed or the catalog has no record. Callers treat
// nil — and any nil field on a record — as "not specified" and fall back to
// their own detection, so a catalog-less deployment keeps working.
func CapabilitiesFor(provider ModelProvider, model string) *ModelCapabilities {
	if model == "" {
		return nil
	}
	resolve := capabilityResolver.Load()
	if resolve == nil {
		return nil
	}
	return (*resolve)(provider, normalizeCapabilityModel(provider, model))
}

// normalizeCapabilityModel drops a provider prefix that just repeats the
// provider being resolved for — callers reach here with the wire model, which
// may still carry it ("xai/grok-4-0709" on provider "xai"). Left in, the pair
// keys a second cache entry for the same model and hands the name-based
// fallbacks a string they cannot match on.
//
// Only a prefix equal to this provider is stripped, so model namespaces like
// "meta-llama/Llama-3.1-8B" and cross-provider forms like "openai/gpt-5.1" on
// provider "openrouter" survive untouched.
func normalizeCapabilityModel(provider ModelProvider, model string) string {
	if p, bare := ParseModelString(model, ""); p == provider && bare != "" {
		return bare
	}
	return model
}

// ModelCaps answers capability questions for one (provider, model) pair,
// resolving the datasheet record once instead of once per question. Callers
// that would otherwise pass (provider, model) into several helpers should
// resolve this beside their canonical model and call methods on it.
//
// Cheap to construct, so a caller gating on a model other than the request's
// (a server-side fallback entry, say) resolves its own rather than reusing one.
// The zero value is valid: no record, empty name, every capability false.
type ModelCaps struct {
	provider ModelProvider
	model    string
	record   *ModelCapabilities
}

// ResolveModelCaps looks up the capability record for the pair. Pass the
// canonical model (post-ResolveCanonicalModel), since the name-based fallbacks
// match on the bare model.
func ResolveModelCaps(provider ModelProvider, model string) ModelCaps {
	model = normalizeCapabilityModel(provider, model)
	return ModelCaps{
		provider: provider,
		model:    model,
		record:   CapabilitiesFor(provider, model),
	}
}

// Provider returns the provider the capabilities were resolved for.
func (c ModelCaps) Provider() ModelProvider { return c.provider }

// Model returns the model name the capabilities were resolved for.
func (c ModelCaps) Model() string { return c.model }

// Record returns the underlying datasheet record, or nil when none was found.
func (c ModelCaps) Record() *ModelCapabilities { return c.record }

// SupportsFastMode returns true if the model supports speed:"fast" (research
// preview). Supported on Opus 4.6, Opus 4.7, Opus 4.8, and Opus 5; requests
// carrying speed:"fast" to any other model are rejected with 400.
// Beta header: fast-mode-2026-02-01.
//
// Source: https://platform.claude.com/docs/en/build-with-claude/fast-mode
//
// Prefers the datasheet's supports_speed boolean when set, falling back to
// name detection when no record is registered.
func (c ModelCaps) SupportsFastMode(fallback bool) bool {
	if c.record != nil && c.record.SupportsFastMode != nil {
		return *c.record.SupportsFastMode
	}
	return fallback
}

// Wire field names used as UnsupportedFields and ConditionallyUnsupportedFields
// keys. Call sites pass these rather than string literals so a typo fails to
// compile instead of silently reading as "supported".
const (
	FieldTopP                 = "top_p"
	FieldPresencePenalty      = "presence_penalty"
	FieldFrequencyPenalty     = "frequency_penalty"
	FieldStop                 = "stop"
	FieldReasoningEffort      = "reasoning_effort"
	FieldPrediction           = "prediction"
	FieldPromptCacheKey       = "prompt_cache_key"
	FieldPromptCacheRetention = "prompt_cache_retention"
	FieldPromptCacheOptions   = "prompt_cache_options"
	FieldVerbosity            = "verbosity"
	FieldStore                = "store"
	FieldWebSearchOptions     = "web_search_options"
)

// Logical field names used as FieldNames keys, where the value is the wire name
// the model expects. Same reasoning as the constants above.
const (
	// LogicalFieldInputImage is the image an image or video request is derived
	// from. Providers name it very differently per model — Runware nests it as
	// seedImage, referenceImages or frameImages; Replicate takes image_prompt,
	// input_image or image.
	LogicalFieldInputImage = "input_image"
	// LogicalFieldPrompt is the text prompt.
	LogicalFieldPrompt = "prompt"
)

// ConditionWhenEffortNone marks a field the model accepts only while reasoning
// is off — i.e. reasoning.effort is "none" or omitted.
const ConditionWhenEffortNone = "when_effort_none"

// FieldUnsupported reports whether the provider rejects a wire field outright.
// fallback is returned when the datasheet says nothing about the field, so each
// call site keeps its own name-based default until the feed carries the row.
func (c ModelCaps) FieldUnsupported(field string, fallback bool) bool {
	if c.record != nil {
		if unsupported, ok := c.record.UnsupportedFields[field]; ok {
			return unsupported
		}
	}
	return fallback
}

// FieldName resolves the wire name a model expects for a logical field, from the
// datasheet's field_names map. Providers that shape a request per model — which
// key an input image goes under, what a parameter is called — read it here so
// the mapping can move to the datasheet without a release. As with
// FieldUnsupported, fallback is the call site's own answer and is returned
// whenever the datasheet says nothing.
func (c ModelCaps) FieldName(logical string, fallback string) string {
	if c.record != nil {
		if name, ok := c.record.FieldNames[logical]; ok && name != "" {
			return name
		}
	}
	return fallback
}

// SupportsSystemMessages reports whether the (provider, model) pair accepts a
// system message. Providers differ in how they carry one — Replicate exposes a
// system_prompt input field per model — so callers that must reroute the text
// pass their own name-based default as fallback.
func (c ModelCaps) SupportsSystemMessages(fallback bool) bool {
	if c.record != nil && c.record.SupportsSystemMessages != nil {
		return *c.record.SupportsSystemMessages
	}
	return fallback
}

// SupportsAssistantPrefill reports whether a trailing assistant message may be
// sent for the model to continue. Models without it need it trimmed.
func (c ModelCaps) SupportsAssistantPrefill(fallback bool) bool {
	if c.record != nil && c.record.SupportsAssistantPrefill != nil {
		return *c.record.SupportsAssistantPrefill
	}
	return fallback
}

// SupportsCachePoint reports whether the provider accepts explicit cache-point
// markers in the request (Bedrock Converse cachePoint blocks).
func (c ModelCaps) SupportsCachePoint(fallback bool) bool {
	if c.record != nil && c.record.SupportsCachePoint != nil {
		return *c.record.SupportsCachePoint
	}
	return fallback
}

// SupportsPromptCachingScope reports whether the model accepts
// cache_control.scope (Anthropic's prompt-caching-scope beta). Distinct from
// SupportsExtendedCacheTTL, which gates the cache TTL rather than its scope.
func (c ModelCaps) SupportsPromptCachingScope(fallback bool) bool {
	if c.record != nil && c.record.SupportsPromptCachingScope != nil {
		return *c.record.SupportsPromptCachingScope
	}
	return fallback
}

// SupportsExtendedCacheTTL reports whether the model accepts a 1h cache-write
// TTL (Bedrock cachePoint.ttl, Anthropic's extended-cache-ttl beta). Models
// without it need the marker downgraded to the default TTL.
func (c ModelCaps) SupportsExtendedCacheTTL(fallback bool) bool {
	if c.record != nil && c.record.SupportsExtendedCacheTTL != nil {
		return *c.record.SupportsExtendedCacheTTL
	}
	return fallback
}

// ToolChoiceStructSupported reports whether the model accepts the struct form of
// tool_choice that pins a named tool. Models without it need the pin dropped.
func (c ModelCaps) ToolChoiceStructSupported(fallback bool) bool {
	if c.record != nil && c.record.ToolChoiceStructSupported != nil {
		return *c.record.ToolChoiceStructSupported
	}
	return fallback
}

// SyntheticSOToolChoiceOmitted reports whether the synthetic structured-output
// tool must be left unpinned, letting the model reach it under "auto" instead.
func (c ModelCaps) SyntheticSOToolChoiceOmitted(fallback bool) bool {
	if c.record != nil && c.record.SyntheticSOToolChoiceOmitted != nil {
		return *c.record.SyntheticSOToolChoiceOmitted
	}
	return fallback
}

// ServiceTierSupported reports whether the model accepts a service tier.
// fallback is returned when the datasheet publishes no tier list, so each call
// site keeps its own name-based default until the feed carries the row.
func (c ModelCaps) ServiceTierSupported(tier BifrostServiceTier, fallback bool) bool {
	if c.record != nil && len(c.record.ServiceTiers) > 0 {
		return slices.Contains(c.record.ServiceTiers, string(tier))
	}
	return fallback
}

// ServerTool returns the versioned identifier the provider expects for a logical
// server tool ("web_search", "computer_use", …), and false when the datasheet
// publishes none — callers then fall back to their own version selection.
func (c ModelCaps) ServerTool(name string) (string, bool) {
	if c.record == nil {
		return "", false
	}
	version, ok := c.record.ServerTools[name]
	return version, ok
}

// FieldCondition returns the condition label gating a field, or "" when the
// field is not conditionally supported.
func (c ModelCaps) FieldCondition(field string) string {
	if c.record == nil {
		return ""
	}
	return c.record.ConditionallyUnsupportedFields[field]
}

// SupportsMultimodalToolOutput reports whether a function response may carry
// sibling media parts (inlineData/fileData) beside its text output. Models
// without it take text alone — media is dropped rather than rejected.
//
// Prefers the datasheet's supports_multimodal_tool_output boolean, falling back
// to name detection (Gemini 3+).
func (c ModelCaps) SupportsMultimodalToolOutput(fallback bool) bool {
	if c.record != nil && c.record.SupportsMultimodalToolOutput != nil {
		return *c.record.SupportsMultimodalToolOutput
	}
	return fallback
}

// SupportsResponseSchemaWithTools reports whether the model accepts a JSON
// response format alongside function declarations. Models without it reject the
// pairing, so the response-format hint is dropped to keep tool calling working.
//
// Prefers the datasheet's supports_response_schema_with_tools boolean, falling
// back to name detection (Gemini 3+).
func (c ModelCaps) SupportsResponseSchemaWithTools(fallback bool) bool {
	if c.record != nil && c.record.SupportsResponseSchemaWithTools != nil {
		return *c.record.SupportsResponseSchemaWithTools
	}
	return fallback
}

// ---- Anthropic server-tool and beta-gated feature surface ----
//
// Each of these gates a tool or request field that only some (provider, model)
// pairs accept. The fallback every call site passes is the provider-level
// answer from the Anthropic package's ProviderFeatures matrix, so a row that
// says nothing keeps today's behaviour and a row that speaks overrides it for
// that model alone. A datasheet row is provider-scoped, so its answer is
// already a claim about that provider.

// SupportsNativeEffort reports whether the model takes Anthropic's
// output_config.effort knob.
func (c ModelCaps) SupportsNativeEffort(fallback bool) bool {
	if c.record != nil && c.record.SupportsNativeEffort != nil {
		return *c.record.SupportsNativeEffort
	}
	return fallback
}

// SupportsMidConversationSystem reports whether the model accepts a
// role:"system" message mid-conversation rather than only as a leading block.
func (c ModelCaps) SupportsMidConversationSystem(fallback bool) bool {
	if c.record != nil && c.record.SupportsMidConversationSystem != nil {
		return *c.record.SupportsMidConversationSystem
	}
	return fallback
}

// SupportsMCP reports whether the model accepts MCP connector servers.
func (c ModelCaps) SupportsMCP(fallback bool) bool {
	if c.record != nil && c.record.SupportsMCP != nil {
		return *c.record.SupportsMCP
	}
	return fallback
}

// SupportsWebSearch reports whether the model accepts the web_search server tool.
func (c ModelCaps) SupportsWebSearch(fallback bool) bool {
	if c.record != nil && c.record.SupportsWebSearch != nil {
		return *c.record.SupportsWebSearch
	}
	return fallback
}

// SupportsWebSearchDynamic reports whether the model accepts the dynamic
// filtering web_search generation rather than the original one.
func (c ModelCaps) SupportsWebSearchDynamic(fallback bool) bool {
	if c.record != nil && c.record.SupportsWebSearchDynamic != nil {
		return *c.record.SupportsWebSearchDynamic
	}
	return fallback
}

// SupportsWebFetch reports whether the model accepts the web_fetch server tool.
func (c ModelCaps) SupportsWebFetch(fallback bool) bool {
	if c.record != nil && c.record.SupportsWebFetch != nil {
		return *c.record.SupportsWebFetch
	}
	return fallback
}

// SupportsCodeExecution reports whether the model accepts the code_execution
// server tool.
func (c ModelCaps) SupportsCodeExecution(fallback bool) bool {
	if c.record != nil && c.record.SupportsCodeExecution != nil {
		return *c.record.SupportsCodeExecution
	}
	return fallback
}

// SupportsBashTool reports whether the model accepts the bash client tool.
func (c ModelCaps) SupportsBashTool(fallback bool) bool {
	if c.record != nil && c.record.SupportsBashTool != nil {
		return *c.record.SupportsBashTool
	}
	return fallback
}

// SupportsMemoryTool reports whether the model accepts the memory client tool.
func (c ModelCaps) SupportsMemoryTool(fallback bool) bool {
	if c.record != nil && c.record.SupportsMemoryTool != nil {
		return *c.record.SupportsMemoryTool
	}
	return fallback
}

// SupportsToolSearch reports whether the model accepts the tool_search server
// tool and tool.defer_loading.
func (c ModelCaps) SupportsToolSearch(fallback bool) bool {
	if c.record != nil && c.record.SupportsToolSearch != nil {
		return *c.record.SupportsToolSearch
	}
	return fallback
}

// SupportsAdvisorTool reports whether the model accepts advisor_tool_result blocks.
func (c ModelCaps) SupportsAdvisorTool(fallback bool) bool {
	if c.record != nil && c.record.SupportsAdvisorTool != nil {
		return *c.record.SupportsAdvisorTool
	}
	return fallback
}

// SupportsSkills reports whether the model accepts the container.skills object.
func (c ModelCaps) SupportsSkills(fallback bool) bool {
	if c.record != nil && c.record.SupportsSkills != nil {
		return *c.record.SupportsSkills
	}
	return fallback
}

// SupportsTaskBudgets reports whether the model accepts output_config.task_budget.
func (c ModelCaps) SupportsTaskBudgets(fallback bool) bool {
	if c.record != nil && c.record.SupportsTaskBudgets != nil {
		return *c.record.SupportsTaskBudgets
	}
	return fallback
}

// SupportsInferenceGeo reports whether the model accepts the inference_geo field.
func (c ModelCaps) SupportsInferenceGeo(fallback bool) bool {
	if c.record != nil && c.record.SupportsInferenceGeo != nil {
		return *c.record.SupportsInferenceGeo
	}
	return fallback
}

// SupportsCompaction reports whether the model accepts context_management
// compaction edits.
func (c ModelCaps) SupportsCompaction(fallback bool) bool {
	if c.record != nil && c.record.SupportsCompaction != nil {
		return *c.record.SupportsCompaction
	}
	return fallback
}

// SupportsContextEditing reports whether the model accepts context_management
// clear_tool_uses / clear_thinking edits.
func (c ModelCaps) SupportsContextEditing(fallback bool) bool {
	if c.record != nil && c.record.SupportsContextEditing != nil {
		return *c.record.SupportsContextEditing
	}
	return fallback
}

// SupportsAdvancedToolUse reports whether the model accepts tool.allowed_callers.
func (c ModelCaps) SupportsAdvancedToolUse(fallback bool) bool {
	if c.record != nil && c.record.SupportsAdvancedToolUse != nil {
		return *c.record.SupportsAdvancedToolUse
	}
	return fallback
}

// SupportsInputExamples reports whether the model accepts tool.input_examples.
func (c ModelCaps) SupportsInputExamples(fallback bool) bool {
	if c.record != nil && c.record.SupportsInputExamples != nil {
		return *c.record.SupportsInputExamples
	}
	return fallback
}

// SupportsEagerInputStreaming reports whether the model accepts
// tool.eager_input_streaming (fine-grained tool streaming).
func (c ModelCaps) SupportsEagerInputStreaming(fallback bool) bool {
	if c.record != nil && c.record.SupportsEagerInputStreaming != nil {
		return *c.record.SupportsEagerInputStreaming
	}
	return fallback
}

// SupportsInterleavedThinking reports whether the model accepts interleaved
// thinking between tool calls.
func (c ModelCaps) SupportsInterleavedThinking(fallback bool) bool {
	if c.record != nil && c.record.SupportsInterleavedThinking != nil {
		return *c.record.SupportsInterleavedThinking
	}
	return fallback
}

// SupportsFilesAPI reports whether the model accepts file_id content sources.
func (c ModelCaps) SupportsFilesAPI(fallback bool) bool {
	if c.record != nil && c.record.SupportsFilesAPI != nil {
		return *c.record.SupportsFilesAPI
	}
	return fallback
}

// SupportsTextEditorTool reports whether the model accepts the text_editor client tool.
func (c ModelCaps) SupportsTextEditorTool(fallback bool) bool {
	if c.record != nil && c.record.SupportsTextEditorTool != nil {
		return *c.record.SupportsTextEditorTool
	}
	return fallback
}

// SupportsResponsesEndpoint reports whether the datasheet's supported_endpoints list
// includes the Responses API.
func (c ModelCaps) SupportsResponsesEndpoint(fallback bool) bool {
	if c.record == nil || len(c.record.SupportedEndpoints) == 0 {
		return fallback
	}
	for _, endpoint := range c.record.SupportedEndpoints {
		if strings.Contains(endpoint, "/v1/responses") {
			return true
		}
	}
	return false
}

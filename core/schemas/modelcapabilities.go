package schemas

import "slices"

// ModelCapabilities is the per-(model, provider) capability record sourced from
// the bifrost datasheet (https://getbifrost.ai/datasheet). It is the single
// source of truth for behaviour the runtime previously hard-coded — model limits
// (max_output_tokens), beta headers, server-tool versioning, cache_point gating,
// reasoning/thinking shape, parameter-drop rules, request-path routing.
//
// All fields are optional; nil means "not specified, use the runtime default
// or the existing hardcoded helper". The (model, provider) pair is encoded
// in the datasheet key prefix per existing convention:
//
//   - "claude-opus-4-7"                       — Anthropic native
//   - "anthropic.claude-opus-4-7-...-v1:0"    — Bedrock canonical
//   - "us.anthropic.claude-...-v1:0"          — Bedrock regional alias
//   - "azure/claude-opus-4-7"                 — Azure
//   - "vertex_ai/claude-opus-4-7"             — Vertex
//   - "vertex_ai/gemini-2.5-pro"              — Vertex Gemini
//
// New fields should mirror the TypeScript definition in
// bifrost-website/src/types/bifrostOverrides.ts.
type ModelCapabilities struct {
	// ---- Capability flags (mirrors TS supports_*) ----

	SupportsCachePoint              *bool `json:"supports_cache_point,omitempty"`
	SupportsInterleavedThinking     *bool `json:"supports_interleaved_thinking,omitempty"`
	SupportsSkills                  *bool `json:"supports_skills,omitempty"`
	SupportsMCP                     *bool `json:"supports_mcp,omitempty"`
	SupportsWebSearchDynamic        *bool `json:"supports_web_search_dynamic,omitempty"`
	SupportsWebFetch                *bool `json:"supports_web_fetch,omitempty"`
	SupportsCodeExecution           *bool `json:"supports_code_execution,omitempty"`
	SupportsBashTool                *bool `json:"supports_bash_tool,omitempty"`
	SupportsTextEditorTool          *bool `json:"supports_text_editor_tool,omitempty"`
	SupportsMemoryTool              *bool `json:"supports_memory_tool,omitempty"`
	SupportsToolSearch              *bool `json:"supports_tool_search,omitempty"`
	SupportsFilesAPI                *bool `json:"supports_files_api,omitempty"`
	SupportsCompaction              *bool `json:"supports_compaction,omitempty"`
	SupportsContextEditing          *bool `json:"supports_context_editing,omitempty"`
	SupportsContext1M               *bool `json:"supports_context_1m,omitempty"`
	SupportsFastMode                *bool `json:"supports_speed,omitempty"` // datasheet emits fast mode as supports_speed
	SupportsAdaptiveThinking        *bool `json:"supports_adaptive_thinking,omitempty"`
	SupportsNativeEffort            *bool `json:"supports_native_effort,omitempty"`
	SupportsMidConversationSystem   *bool `json:"supports_mid_conversation_system_messages,omitempty"`
	SupportsSamplingParams          *bool `json:"supports_sampling_params,omitempty"` // false ⇒ temperature/top_p/top_k rejected (adaptive-only models)
	SupportsRedactThinking          *bool `json:"supports_redact_thinking,omitempty"`
	SupportsTaskBudgets             *bool `json:"supports_task_budgets,omitempty"`
	SupportsEagerInputStreaming     *bool `json:"supports_eager_input_streaming,omitempty"`
	SupportsAdvancedToolUse         *bool `json:"supports_advanced_tool_use,omitempty"`
	SupportsInputExamples           *bool `json:"supports_input_examples,omitempty"`
	SupportsAdvisorTool             *bool `json:"supports_advisor_tool,omitempty"`
	SupportsInferenceGeo            *bool `json:"supports_inference_geo,omitempty"`
	SupportsPromptCachingScope      *bool `json:"supports_prompt_caching_scope,omitempty"`
	SupportsExtendedCacheTTL        *bool `json:"supports_extended_cache_ttl,omitempty"`
	SupportsReasoningContentBlocks  *bool `json:"supports_reasoning_content_blocks,omitempty"`
	SupportsMultimodalToolOutput    *bool `json:"supports_multimodal_tool_output,omitempty"`
	SupportsResponseSchemaWithTools *bool `json:"supports_response_schema_with_tools,omitempty"`

	// Baseline request-surface flags. These drive the compat plugin's
	// parameter allowlist rather than provider request shaping, so they are
	// broadly populated across the catalog where the flags above are curated.
	// Note the deliberately distinct neighbours: SupportsWebSearch is the base
	// feature while SupportsWebSearchDynamic is the dynamic-query variant, and
	// SupportsPromptCaching is the base feature while SupportsPromptCachingScope
	// is the scope sub-feature.
	SupportsAssistantPrefill        *bool `json:"supports_assistant_prefill,omitempty"`
	SupportsSystemMessages          *bool `json:"supports_system_messages,omitempty"`
	SupportsFunctionCalling         *bool `json:"supports_function_calling,omitempty"`
	SupportsParallelFunctionCalling *bool `json:"supports_parallel_function_calling,omitempty"`
	SupportsToolChoice              *bool `json:"supports_tool_choice,omitempty"`
	SupportsReasoning               *bool `json:"supports_reasoning,omitempty"`
	SupportsResponseSchema          *bool `json:"supports_response_schema,omitempty"`
	SupportsReasoningWithToolCalls  *bool `json:"supports_reasoning_with_tool_calls,omitempty"`
	SupportsNoneReasoningEffort     *bool `json:"supports_none_reasoning_effort,omitempty"`
	SupportsServiceTier             *bool `json:"supports_service_tier,omitempty"`
	SupportsPromptCaching           *bool `json:"supports_prompt_caching,omitempty"`
	SupportsWebSearch               *bool `json:"supports_web_search,omitempty"`

	// ---- Endpoint + parameter surface ----

	// Datasheet "mode" for the row (chat, embedding, image_generation, …).
	Mode *string `json:"mode,omitempty"`

	// Endpoints the model is reachable on. Normalised into the catalog's
	// supported-response-type index.
	SupportedEndpoints []string `json:"supported_endpoints,omitempty"`

	// Service tiers the model accepts, as BifrostServiceTier values — e.g.
	// ["priority", "flex"] for Vertex Priority/Flex PayGo. Empty means the row
	// says nothing, and callers keep their own default. A set rather than a
	// boolean per tier: the vocabulary is provider-specific and open-ended, so a
	// new tier should be a datasheet value, not a schema change.
	ServiceTiers []string `json:"service_tiers,omitempty"`

	// Request parameters the model accepts, as the datasheet describes them for
	// the prompt playground. Only the IDs feed the compat allowlist.
	ModelParameters []ModelParameterDescriptor `json:"model_parameters,omitempty"`

	// ---- Categorical maps (logical_name -> identifier/value) ----

	// Map of logical server-tool name → versioned identifier the provider expects.
	// Example: {"web_search": "web_search_20260209", "computer_use": "computer_20251124"}.
	ServerTools map[string]string `json:"server_tools,omitempty"`

	// Map of logical feature name → anthropic-beta header value.
	// Example: {"compaction": "compact-2026-01-12"}.
	BetaHeaders map[string]string `json:"beta_headers,omitempty"`

	// Map of logical field name → wire field name. Use this for any per-model
	// rename. Examples:
	//   {"max_tokens": "max_completion_tokens"} for OpenAI reasoning models
	//   {"max_tokens": "max_gen_len"}           for Bedrock Llama
	//   {"prompt_cache_key": "prompt_cache_isolation_key"} for Fireworks
	FieldNames map[string]string `json:"field_names,omitempty"`

	// Map of triggering server-tool ID → tool IDs auto-injected alongside it.
	// Example: {"web_search_20260209": ["code_execution_20260120"]}.
	ServerToolAutoInjects map[string][]string `json:"server_tool_auto_injects,omitempty"`

	// Map of server-tool ID → beta header it implicitly enables.
	// Example: {"memory_20250818": "context-management-2025-06-27"}.
	ServerToolImplicitBetas map[string]string `json:"server_tool_implicit_betas,omitempty"`

	// Static HTTP headers always set when targeting this model.
	// Example: {"OpenAI-Beta": "realtime=v1"}.
	ExtraHeaders map[string]string `json:"extra_headers,omitempty"`

	// Set of wire field names the provider rejects (returns 400 if present).
	// Presence with value `true` means unsupported; absence means supported.
	// Examples:
	//   {"top_p": true, "top_k": true, "temperature": true}     // Opus 4.7
	//   {"presence_penalty": true, "prediction": true, ...}     // xAI Grok
	//   {"service_tier": true}                                  // Gemini openai-compat
	//   {"tool_choice_struct": true}                            // Mistral
	UnsupportedFields map[string]bool `json:"unsupported_fields,omitempty"`

	// Fields the provider conditionally accepts. Value carries the condition
	// label the runtime knows how to interpret. Used for cases that don't fit
	// a clean boolean — e.g. gpt-5 accepts top_p only when reasoning.effort
	// defaults to "none".
	// Example: {"top_p": "when_effort_none"}.
	ConditionallyUnsupportedFields map[string]string `json:"conditionally_unsupported_fields,omitempty"`

	// ---- Reasoning ----
	//
	// One flat key per capability, so an absent key always means "the row says
	// nothing" and the caller's fallback answers. No key's presence changes
	// what another key means.

	// Model takes a categorical effort label on the wire (Gemini's
	// thinkingLevel, OpenAI's reasoning.effort) rather than only a numeric
	// budget. Listing ReasoningEffortLevels implies true.
	SupportsReasoningEffort *bool `json:"supports_reasoning_effort,omitempty"`

	// Effort labels the model accepts, in ascending order.
	ReasoningEffortLevels []string `json:"reasoning_effort_levels,omitempty"`

	// Requested label → label actually sent, for models accepting a narrower
	// set. Example for Gemini 3 Pro: {"minimal": "low", "medium": "high"}.
	ReasoningEffortRenames map[string]string `json:"reasoning_effort_renames,omitempty"`

	// Allowed thinking-budget range in tokens.
	ReasoningBudget *BudgetControl `json:"reasoning_budget,omitempty"`

	// Model accepts an explicit "off" — thinking:{type:"disabled"} or a zero
	// budget. False on models that reject it, which need the config omitted.
	SupportsReasoningDisable *bool `json:"supports_reasoning_disable,omitempty"`

	// Model accepts a self-managed thinking budget (Gemini thinkingBudget: -1).
	SupportsDynamicReasoningBudget *bool `json:"supports_dynamic_reasoning_budget,omitempty"`

	// ---- Singletons ----

	// Model's max output-token ceiling from the datasheet params feed. Providers
	// requiring an explicit max_tokens (e.g. Anthropic) default to this.
	MaxOutputTokens *int `json:"max_output_tokens,omitempty"`

	// Default max_tokens when the caller omits it (Anthropic requires a value).
	DefaultMaxTokens *int `json:"default_max_tokens,omitempty"`

	// Floor the provider enforces on max_output_tokens. A request below it is
	// rejected outright rather than clamped upstream, so callers raise the value
	// to this floor. Models served by an OpenAI-compatible backend require 16 —
	// on Bedrock that covers the OpenAI family and xAI Grok, while Claude and
	// Nova accept 1. Absent ⇒ the caller's own fallback; zero ⇒ no floor.
	MinOutputTokens *int `json:"min_output_tokens,omitempty"`

	// Floor for reasoning budget tokens.
	MinReasoningMaxTokens *int `json:"min_reasoning_max_tokens,omitempty"`

	// Bedrock Llama: pinned tool_choice.tool must be dropped (returns 400).
	DropToolChoicePin *bool `json:"drop_tool_choice_pin,omitempty"`

	// Prefix used when synthesising a structured-output tool (e.g. "bf_so_").
	SyntheticStructuredOutputToolPrefix *string `json:"synthetic_structured_output_tool_prefix,omitempty"`

	// True when no tool_choice pin is sent alongside the synthetic SO tool.
	SyntheticSOToolChoiceOmitted *bool `json:"synthetic_so_tool_choice_omitted,omitempty"`

	// Provider request-shape variant. Examples: "converse", "invoke_text",
	// "invoke_messages", "invoke_titan_embed", "invoke_cohere_embed",
	// "invoke_titan_canvas", "invoke_stability", "deepseek_conversation",
	// "openai_compatible", "openai_compatible_structured", "vertex".
	RequestPath *string `json:"request_path,omitempty"`

	// Vertex skips the outer anthropic-beta HTTP header (uses body-injection).
	OuterAnthropicBetaHeaderSkipped *bool `json:"outer_anthropic_beta_header_skipped,omitempty"`

	// Azure: forced api-version. "preview" for Anthropic-on-Azure.
	APIVersion *string `json:"api_version,omitempty"`

	// Vertex: model is only available on multi-region pool endpoints.
	IsVertexMultiRegionOnly *bool `json:"vertex_multi_region_only,omitempty"`

	// ---- Reasoning detection (OpenAI/xAI families) ----

	IsReasoningModel *bool `json:"is_reasoning_model,omitempty"`
	AlwaysReasoning  *bool `json:"always_reasoning,omitempty"`

	// ---- Provider-rule fields duplicated per model (kept alongside UnsupportedFields) ----

	// Mistral: tool_choice struct form not supported, must collapse to "any" string.
	// Mirrors UnsupportedFields["tool_choice_struct"].
	ToolChoiceStructSupported *bool `json:"tool_choice_struct_supported,omitempty"`

	// Fireworks: keep `prediction` field through the openai-compat filter.
	PreservesPrediction *bool `json:"preserves_prediction,omitempty"`

	// Perplexity: reasoning_effort is a required field (not optional).
	ReasoningRequired *bool `json:"reasoning_required,omitempty"`

	// ---- Aliasing & regional inference profiles ----

	// Bedrock regional inference profile aliases that point to a canonical entry.
	AliasOf *string `json:"alias_of,omitempty"`

	// "us" | "eu" | "apac" | "global" — Bedrock cross-region profile prefix.
	RegionInferenceProfile *string `json:"region_inference_profile,omitempty"`

	// ---- Bedrock model-family flags consumed by the runtime ----

	// Bedrock: Cohere Command R/R+ uses native text-completion shape, not Converse.
	//
	// Deprecated: never populated and never read. Subsumed by BedrockAPIs
	// ["invoke"]; remove once the feed drops the key.
	IsCohereCommandR *bool `json:"is_cohere_command_r,omitempty"`

	// Wire APIs this model accepts on the AWS endpoint the row is scoped to.
	// The host is NOT encoded in the value — it comes from the row's provider
	// ("bedrock" ⇒ bedrock-runtime, "bedrock_mantle" ⇒ bedrock-mantle), so the
	// same value means different endpoints on different rows and a model gaining
	// a surface is a new value on the matching row, not a new vocabulary:
	//
	//   openai.gpt-5.6-luna        provider bedrock_mantle → ["responses"]
	//   global.openai.gpt-5.6-luna provider bedrock        → ["converse"]
	//
	// Listed in preference order. Unrecognised entries are skipped rather than
	// rejected, so the datasheet may ship an API ahead of the binary that reads
	// it. An empty list is treated as absent.
	//
	// This is a claim, not a constraint: the configured model identifier wins
	// when the two disagree, because AWS rejects a mismatched identifier
	// outright (a bare id 400s on bedrock-runtime, a cross-region or ARN id 404s
	// on bedrock-mantle). Callers must resolve identifier form first.
	BedrockAPIs []BedrockAPI `json:"bedrock_apis,omitempty"`

	// Wire shape this model uses for reasoning content on Bedrock Converse.
	// Scoped to Converse specifically: the same model reasons in a different
	// shape on the mantle chat-completions surface.
	//
	// Absent or unrecognised falls back to the caller's family detection.
	BedrockReasoningShape BedrockReasoningShape `json:"bedrock_reasoning_shape,omitempty"`
}

// BedrockAPI names one wire API on a Bedrock endpoint. Which endpoint serves it
// is carried by the datasheet row's provider, not by the value — see
// ModelCapabilities.BedrockAPIs.
type BedrockAPI string

const (
	// Served by bedrock-runtime.
	BedrockAPIConverse BedrockAPI = "converse"
	BedrockAPIInvoke   BedrockAPI = "invoke"
	BedrockAPIMessages BedrockAPI = "messages"

	// Served by bedrock-mantle today, and by bedrock-runtime once that surface
	// is wired — the row's provider distinguishes them.
	BedrockAPIChatCompletions BedrockAPI = "chat_completions"
	BedrockAPIResponses       BedrockAPI = "responses"
)

// BedrockAPIValues lists every recognised BedrockAPI.
var BedrockAPIValues = []BedrockAPI{
	BedrockAPIConverse,
	BedrockAPIInvoke,
	BedrockAPIMessages,
	BedrockAPIChatCompletions,
	BedrockAPIResponses,
}

// IsValid reports whether a is an API this binary knows how to reach. Values the
// datasheet publishes ahead of the runtime are simply not valid yet, and callers
// skip them.
func (a BedrockAPI) IsValid() bool {
	return slices.Contains(BedrockAPIValues, a)
}

// BedrockReasoningShape names the reasoning content variant a model uses on
// Bedrock Converse. The two are mutually exclusive and not interchangeable:
// replaying the wrong one is rejected (400 on Anthropic, an opaque 500 on
// OpenAI and xAI).
type BedrockReasoningShape string

const (
	// reasoningContent.reasoningText{text,signature} — Anthropic, DeepSeek.
	BedrockReasoningShapeText BedrockReasoningShape = "reasoning_text"

	// reasoningContent.redactedContent, one opaque blob — OpenAI, xAI.
	BedrockReasoningShapeRedacted BedrockReasoningShape = "redacted_content"
)

// BedrockReasoningShapeValues lists every recognised BedrockReasoningShape.
var BedrockReasoningShapeValues = []BedrockReasoningShape{
	BedrockReasoningShapeText,
	BedrockReasoningShapeRedacted,
}

// IsValid reports whether s is a shape this binary knows how to emit.
func (s BedrockReasoningShape) IsValid() bool {
	return slices.Contains(BedrockReasoningShapeValues, s)
}

// ModelParameterDescriptor is one entry of the datasheet's model_parameters
// array. Only ID is modelled: the remaining keys (label, helpText, type,
// default, range) describe how the prompt playground renders the control and
// are served to the UI straight from the stored row.
type ModelParameterDescriptor struct {
	ID string `json:"id"`
}

// EffortControl carries a provider package's name-based effort fallback into
// ModelCaps. It is not a datasheet shape — a row publishes the same two facts
// as the independent ReasoningEffortLevels and ReasoningEffortRenames keys.
type EffortControl struct {
	// Effort labels the model accepts, in ascending order.
	Levels []string `json:"levels,omitempty"`

	// Requested label → label actually sent, for models that accept a narrower
	// set. Example for Gemini Pro: {"minimal": "low", "medium": "high"}.
	Renames map[string]string `json:"renames,omitempty"`
}

// BudgetControl is the thinking-budget range in tokens. A value object rather
// than a grouping container: one capability whose answer needs two numbers, so
// it is both the datasheet shape of ReasoningBudget and the fallback carrier.
type BudgetControl struct {
	Min *int `json:"min,omitempty"`
	Max *int `json:"max,omitempty"`
}

package schemas

// BifrostOverrides carries per-(model, provider) manipulation hints sourced
// from the bifrost datasheet (https://getbifrost.ai/datasheet). They capture
// behaviour the runtime previously hard-coded (beta headers, server-tool
// versioning, cache_point gating, reasoning/thinking shape, parameter-drop
// rules, request-path routing).
//
// All fields are optional; nil means "no override, use the runtime default
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
type BifrostOverrides struct {
	// ---- Capability flags (mirrors TS supports_*) ----

	SupportsCachePoint             *bool `json:"supports_cache_point,omitempty"`
	SupportsInterleavedThinking    *bool `json:"supports_interleaved_thinking,omitempty"`
	SupportsSkills                 *bool `json:"supports_skills,omitempty"`
	SupportsMCP                    *bool `json:"supports_mcp,omitempty"`
	SupportsWebSearchDynamic       *bool `json:"supports_web_search_dynamic,omitempty"`
	SupportsWebFetch               *bool `json:"supports_web_fetch,omitempty"`
	SupportsCodeExecution          *bool `json:"supports_code_execution,omitempty"`
	SupportsBashTool               *bool `json:"supports_bash_tool,omitempty"`
	SupportsTextEditorTool         *bool `json:"supports_text_editor_tool,omitempty"`
	SupportsMemoryTool             *bool `json:"supports_memory_tool,omitempty"`
	SupportsToolSearch             *bool `json:"supports_tool_search,omitempty"`
	SupportsFilesAPI               *bool `json:"supports_files_api,omitempty"`
	SupportsCompaction             *bool `json:"supports_compaction,omitempty"`
	SupportsContextEditing         *bool `json:"supports_context_editing,omitempty"`
	SupportsContext1M              *bool `json:"supports_context_1m,omitempty"`
	SupportsFastMode               *bool `json:"supports_fast_mode,omitempty"`
	SupportsRedactThinking         *bool `json:"supports_redact_thinking,omitempty"`
	SupportsTaskBudgets            *bool `json:"supports_task_budgets,omitempty"`
	SupportsEagerInputStreaming    *bool `json:"supports_eager_input_streaming,omitempty"`
	SupportsAdvancedToolUse        *bool `json:"supports_advanced_tool_use,omitempty"`
	SupportsInputExamples          *bool `json:"supports_input_examples,omitempty"`
	SupportsAdvisorTool            *bool `json:"supports_advisor_tool,omitempty"`
	SupportsInferenceGeo           *bool `json:"supports_inference_geo,omitempty"`
	SupportsPromptCachingScope     *bool `json:"supports_prompt_caching_scope,omitempty"`
	SupportsReasoningContentBlocks *bool `json:"supports_reasoning_content_blocks,omitempty"`

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

	// Effort label normalisation. Example for OpenAI:
	// {"minimal": "low", "xhigh": "high", "max": "high"}.
	EffortRenames map[string]string `json:"effort_renames,omitempty"`

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

	// ---- Reasoning / thinking sub-objects ----

	Reasoning *BifrostReasoningConfig `json:"reasoning,omitempty"`
	Thinking  *BifrostThinkingConfig  `json:"thinking,omitempty"`

	// ---- Singletons ----

	// Default max_tokens when the caller omits it (Anthropic requires a value).
	DefaultMaxTokens *int `json:"default_max_tokens,omitempty"`

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
	IsVertexMultiRegionOnly *bool `json:"is_vertex_multi_region_only,omitempty"`

	// ---- Reasoning detection (OpenAI/xAI families) ----

	IsReasoningModel *bool `json:"is_reasoning_model,omitempty"`
	AlwaysReasoning  *bool `json:"always_reasoning,omitempty"`

	// AcceptsTopP is intentionally NOT modelled as a Go field because the
	// JSON representation can be either a bool or the string
	// "conditional_when_effort_none" (e.g. gpt-5). Go consumers should read
	// UnsupportedFields["top_p"] and ConditionallyUnsupportedFields["top_p"].
	// The wire JSON still carries `accepts_top_p` for legacy clients;
	// Go's json decoder ignores unknown fields so this is safe.
	AcceptsTopK            *bool `json:"accepts_top_k,omitempty"`
	AcceptsTemperature     *bool `json:"accepts_temperature,omitempty"`
	AcceptsFrequencyPenalty *bool `json:"accepts_frequency_penalty,omitempty"`
	AcceptsPresencePenalty *bool `json:"accepts_presence_penalty,omitempty"`
	AcceptsStop            *bool `json:"accepts_stop,omitempty"`
	AcceptsReasoningEffort *bool `json:"accepts_reasoning_effort,omitempty"`

	// ---- Provider-rule fields duplicated per model (kept alongside UnsupportedFields) ----

	// Mistral: tool_choice struct form not supported, must collapse to "any" string.
	// Mirrors UnsupportedFields["tool_choice_struct"].
	ToolChoiceStructSupported *bool `json:"tool_choice_struct_supported,omitempty"`

	// Fireworks: keep `prediction` field through the openai-compat filter.
	PreservesPrediction *bool `json:"preserves_prediction,omitempty"`

	// Gemini openai-compat: drops `service_tier`. Mirrors UnsupportedFields["service_tier"].
	AcceptsServiceTier *bool `json:"accepts_service_tier,omitempty"`

	// openai-compat passthrough flags. Mirrors UnsupportedFields entries.
	AcceptsPrediction           *bool `json:"accepts_prediction,omitempty"`
	AcceptsPromptCacheKey       *bool `json:"accepts_prompt_cache_key,omitempty"`
	AcceptsPromptCacheRetention *bool `json:"accepts_prompt_cache_retention,omitempty"`
	AcceptsVerbosity            *bool `json:"accepts_verbosity,omitempty"`
	AcceptsStore                *bool `json:"accepts_store,omitempty"`
	AcceptsWebSearchOptions     *bool `json:"accepts_web_search_options,omitempty"`

	// Perplexity: reasoning_effort is a required field (not optional).
	ReasoningRequired *bool `json:"reasoning_required,omitempty"`

	// ---- Aliasing & regional inference profiles ----

	// Bedrock regional inference profile aliases that point to a canonical entry.
	AliasOf *string `json:"alias_of,omitempty"`

	// "us" | "eu" | "apac" | "global" — Bedrock cross-region profile prefix.
	RegionInferenceProfile *string `json:"region_inference_profile,omitempty"`

	// ---- Bedrock model-family flags consumed by the runtime ----

	// Bedrock: Cohere Command R/R+ uses native text-completion shape, not Converse.
	IsCohereCommandR *bool `json:"is_cohere_command_r,omitempty"`
}

// BifrostReasoningConfig describes how the runtime should request reasoning
// from a model. Some shapes:
//
//	{Style: "adaptive",       Field: "output_config.effort"}              // Anthropic 4.6+
//	{Style: "budget_tokens",  Field: "thinking.budget_tokens"}            // Anthropic 4.5 and older
//	{Style: "effort_levels",  Field: "reasoningConfig.maxReasoningEffort"} // Bedrock Nova
//	{Field: "reasoning.effort", DefaultWhenOmitted: "none"}               // OpenAI gpt-5
type BifrostReasoningConfig struct {
	// "adaptive" | "budget_tokens" | "effort_levels" — request shape.
	Style *string `json:"style,omitempty"`

	// Wire field where the runtime writes the reasoning config (dotted path
	// permitted). Examples: "output_config.effort", "thinking",
	// "thinking.budget_tokens", "reasoning.effort",
	// "reasoningConfig.maxReasoningEffort".
	Field *string `json:"field,omitempty"`

	// Allowed effort labels when style ∈ {"effort_levels", "adaptive"}.
	EffortLevels []string `json:"effort_levels,omitempty"`

	// Default effort when the request omits the field (e.g. GPT-5.x = "none").
	DefaultWhenOmitted *string `json:"default_when_omitted,omitempty"`
}

// BifrostThinkingConfig describes Gemini-style thinking budget configuration.
type BifrostThinkingConfig struct {
	// Wire field name. "thinkingBudget" (older Gemini) | "thinkingLevel" (Gemini 3.0+).
	Field *string `json:"field,omitempty"`

	// Minimum allowed budget in tokens.
	Min *int `json:"min,omitempty"`

	// Maximum allowed budget in tokens.
	Max *int `json:"max,omitempty"`

	// Special values keyed by literal value, mapping to a label.
	// Example: {"-1": "dynamic", "0": "disabled"}.
	SpecialValues map[string]string `json:"special_values,omitempty"`
}

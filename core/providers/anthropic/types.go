package anthropic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// Since Anthropic always needs to have a max_tokens parameter, we set a default value if not provided.
const (
	AnthropicDefaultMaxTokens = 4096
	MinimumReasoningMaxTokens = 1024

	// AnthropicBetaHeader is the HTTP header name used to enable Anthropic beta features.
	AnthropicBetaHeader = "anthropic-beta"

	// Beta headers for various Anthropic features
	// AnthropicFilesAPIBetaHeader is the required beta header for the Files API.
	AnthropicFilesAPIBetaHeader = "files-api-2025-04-14"
	// AnthropicStructuredOutputsBetaHeader is required for strict tool validation and output_format.
	AnthropicStructuredOutputsBetaHeader = "structured-outputs-2025-11-13"
	// AnthropicAdvancedToolUseBetaHeader is required for defer_loading, input_examples, and allowed_callers.
	AnthropicAdvancedToolUseBetaHeader = "advanced-tool-use-2025-11-20"
	// AnthropicToolExamplesBetaHeader is required for tool.input_examples as a
	// standalone feature (Bedrock supports this narrow header without the full
	// advanced-tool-use-2025-11-20 bundle).
	// Source: AWS Bedrock user guide beta-header list:
	// https://docs.aws.amazon.com/bedrock/latest/userguide/model-parameters-anthropic-claude-messages.html
	AnthropicToolExamplesBetaHeader = "tool-examples-2025-10-29"
	// AnthropicMCPClientBetaHeader is required for MCP servers (current version).
	AnthropicMCPClientBetaHeader = "mcp-client-2025-11-20"
	// AnthropicMCPClientBetaHeaderDeprecated is the previous MCP beta header (kept for fallback).
	AnthropicMCPClientBetaHeaderDeprecated = "mcp-client-2025-04-04"
	// AnthropicPromptCachingScopeBetaHeader is required for prompt caching scope.
	AnthropicPromptCachingScopeBetaHeader = "prompt-caching-scope-2026-01-05"
	// AnthropicCompactionBetaHeader is required for compaction.
	AnthropicCompactionBetaHeader = "compact-2026-01-12"
	// AnthropicContextManagementBetaHeader is required for context management.
	AnthropicContextManagementBetaHeader = "context-management-2025-06-27"
	// AnthropicInterleavedThinkingBetaHeader is required for interleaved thinking between tool calls.
	// Deprecated on Opus 4.6/Sonnet 4.6 (use adaptive thinking); active on older Claude 4 models.
	AnthropicInterleavedThinkingBetaHeader = "interleaved-thinking-2025-05-14"
	// AnthropicSkillsBetaHeader is required for Agent Skills (also requires code-execution + files-api headers).
	AnthropicSkillsBetaHeader = "skills-2025-10-02"
	// AnthropicContext1MBetaHeader is required for 1M context window on Sonnet 4.5 and Sonnet 4.
	// GA on Opus 4.6 and Sonnet 4.6 (no header needed).
	AnthropicContext1MBetaHeader = "context-1m-2025-08-07"
	// AnthropicFastModeBetaHeader is required for fast mode on Opus 4.6 (research preview).
	AnthropicFastModeBetaHeader = "fast-mode-2026-02-01"
	// AnthropicRedactThinkingBetaHeader is required for redacting thinking blocks in responses.
	AnthropicRedactThinkingBetaHeader = "redact-thinking-2026-02-12"
	// AnthropicTaskBudgetsBetaHeader is required for output_config.task_budget (Opus 4.7+).
	AnthropicTaskBudgetsBetaHeader = "task-budgets-2026-03-13"
	// AnthropicEagerInputStreamingBetaHeader is required for eager_input_streaming
	// on custom tools (streams input_json_delta before full args are determined).
	// Per Table 20: GA on Anthropic/Bedrock/Vertex, Beta on Azure.
	AnthropicEagerInputStreamingBetaHeader = "fine-grained-tool-streaming-2025-05-14"

	// AnthropicComputerUseBetaHeader is required for computer use (version-specific).
	// computer_20251124 (Opus 4.6, Sonnet 4.6, Opus 4.5) uses the newer beta header.
	AnthropicComputerUseBetaHeader20251124 = "computer-use-2025-11-24"
	// computer_20250124 (all other supported models) uses the older beta header.
	AnthropicComputerUseBetaHeader20250124 = "computer-use-2025-01-24"

	// Prefixes for beta headers (version-bump proof).
	// Use these with strings.HasPrefix when filtering headers per provider,
	// so that future date bumps (e.g. structured-outputs-2025-12-15) are still matched.
	AnthropicAdvancedToolUseBetaHeaderPrefix     = "advanced-tool-use-"
	AnthropicToolExamplesBetaHeaderPrefix        = "tool-examples-"
	AnthropicStructuredOutputsBetaHeaderPrefix   = "structured-outputs-"
	AnthropicPromptCachingScopeBetaHeaderPrefix  = "prompt-caching-scope-"
	AnthropicMCPClientBetaHeaderPrefix           = "mcp-client-"
	AnthropicInterleavedThinkingBetaHeaderPrefix = "interleaved-thinking-"
	AnthropicSkillsBetaHeaderPrefix              = "skills-"
	AnthropicContext1MBetaHeaderPrefix           = "context-1m-"
	AnthropicFastModeBetaHeaderPrefix            = "fast-mode-"
	AnthropicRedactThinkingBetaHeaderPrefix      = "redact-thinking-"
	AnthropicTaskBudgetsBetaHeaderPrefix         = "task-budgets-"
	AnthropicEagerInputStreamingBetaHeaderPrefix = "fine-grained-tool-streaming-"
)

// ProviderFeatureSupport defines which Anthropic features a given provider supports.
//
// Authoritative sources (verified 2026-04-17):
//   A  = Anthropic feature-availability table:
//        https://platform.claude.com/docs/en/build-with-claude/overview
//   B-header = AWS Bedrock user guide beta-header list:
//        https://docs.aws.amazon.com/bedrock/latest/userguide/model-parameters-anthropic-claude-messages.html
//   B-platform = https://platform.claude.com/docs/en/build-with-claude/claude-on-amazon-bedrock
//   V-platform = https://platform.claude.com/docs/en/build-with-claude/claude-on-vertex-ai
//   Az-platform = https://platform.claude.com/docs/en/build-with-claude/claude-in-microsoft-foundry
//   MCP-excl = MCP connector explicit Bedrock/Vertex exclusion:
//        https://platform.claude.com/docs/en/agents-and-tools/mcp-connector
//   Advisor-excl = Advisor tool Claude-API-only:
//        https://platform.claude.com/docs/en/agents-and-tools/tool-use/advisor-tool
type ProviderFeatureSupport struct {
	WebSearch           bool // web_search server tool (cite: A)
	WebSearchDynamic    bool // web_search_20260209 dynamic filtering (cite: A)
	WebFetch            bool // web_fetch server tool (cite: A)
	CodeExecution       bool // code_execution server tool (cite: A)
	ComputerUse         bool // computer_use client tool (cite: A, B-header)
	Bash                bool // bash client tool (cite: A, B-header)
	Memory              bool // memory client tool — on Bedrock bundled under context-management-2025-06-27 (cite: A, B-header)
	TextEditor          bool // text_editor client tool (cite: A)
	ToolSearch          bool // tool_search server tool — tool-search-tool-2025-10-19 (cite: A, B-header)
	MCP                 bool // MCP connector — explicit "not supported on Bedrock/Vertex" (cite: MCP-excl)
	AdvancedToolUse     bool // advanced-tool-use-2025-11-20 bundle: defer_loading + input_examples + allowed_callers (cite: A)
	InputExamples       bool // tool.input_examples standalone — tool-examples-2025-10-29. Bedrock supports this independently of the AdvancedToolUse bundle (cite: B-header). On Anthropic / Azure the bundle implicitly covers it.
	StructuredOutputs   bool // strict tool validation / output_format (cite: A)
	PromptCachingScope  bool // cache_control.scope — prompt-caching-scope-2026-01-05 (cite: A)
	Compaction          bool // compact_20260112 (cite: A, B-header)
	ContextEditing      bool // clear_tool_uses / clear_thinking (cite: A, B-header)
	FilesAPI            bool // files-api-2025-04-14, file_id source (cite: A)
	InterleavedThinking bool // interleaved thinking between tool calls (cite: A, B-header; fails on non-allowlisted models on Bedrock/Vertex)
	Skills              bool // Agent Skills — container.skills object (cite: A)
	ContainerBasic      bool // Bare string-form container id — universally supported (cite: A)
	Context1M           bool // 1M context window — context-1m-2025-08-07 (cite: A)
	FastMode            bool // Opus 4.6 research preview — fast-mode-2026-02-01 (cite: A)
	RedactThinking      bool // redact-thinking-2026-02-12 (cite: A) — note Bedrock has its own "thinking encryption" (different mechanism)
	TaskBudgets         bool // output_config.task_budget — task-budgets-2026-03-13 (cite: A)
	InferenceGeo        bool // inference_geo field — Claude API only; Bedrock/Vertex/Azure use their own region-routing mechanisms (cite: A)
	EagerInputStreaming bool // fine-grained-tool-streaming-2025-05-14 (cite: A, B-header)
	AdvisorTool         bool // advisor_tool_result block — Anthropic only (cite: Advisor-excl)
	FileSearch          bool // file_search server tool (OpenAI-only)
	ImageGeneration     bool // image_generation server tool (OpenAI-only)
}

// ProviderFeatures maps each provider to its supported Anthropic features.
//
// Every cell below is sourced from the docs named in ProviderFeatureSupport.
// "Not documented" in upstream docs is treated as unsupported here; if a user
// needs a pass-through, ExtraParams still works.
var ProviderFeatures = map[schemas.ModelProvider]ProviderFeatureSupport{
	// Anthropic Claude API direct (cite: A across the board).
	schemas.Anthropic: {
		WebSearch: true, WebSearchDynamic: true, WebFetch: true, CodeExecution: true,
		ComputerUse: true, Bash: true, Memory: true, TextEditor: true, ToolSearch: true,
		MCP: true, AdvancedToolUse: true, InputExamples: true, StructuredOutputs: true, PromptCachingScope: true,
		Compaction: true, ContextEditing: true, FilesAPI: true,
		InterleavedThinking: true, Skills: true, ContainerBasic: true, Context1M: true,
		FastMode: true, RedactThinking: true, TaskBudgets: true,
		InferenceGeo: true, EagerInputStreaming: true, AdvisorTool: true,
	},
	// Google Vertex AI — cite: A (overview table) and V-platform.
	// Notably NOT supported: MCP (MCP-excl), Skills/container.skills,
	// InferenceGeo, FastMode, TaskBudgets, AdvisorTool, StructuredOutputs,
	// PromptCachingScope (400 "unexpected beta header" per LiteLLM #19984),
	// FilesAPI, WebFetch, CodeExecution, AdvancedToolUse, RedactThinking.
	schemas.Vertex: {
		WebSearch:   true, // web search GA on Vertex per A; earlier code restricted to web_search_20250305 — A doesn't qualify
		ComputerUse: true, Bash: true, Memory: true, TextEditor: true, ToolSearch: true,
		ContainerBasic:      true,
		Compaction:          true,
		ContextEditing:      true,
		InterleavedThinking: true, // V-platform confirms; fails on non-allowlisted 4-series
		Context1M:           true,
		EagerInputStreaming: true, // fine-grained-tool-streaming GA per A
	},
	// AWS Bedrock — cite: A + B-header (definitive beta-header list).
	// Notably NOT supported per docs: MCP, Skills, FilesAPI, WebFetch,
	// WebSearch, CodeExecution, FastMode, TaskBudgets, AdvisorTool,
	// InferenceGeo, RedactThinking, AdvancedToolUse (full), PromptCachingScope.
	schemas.Bedrock: {
		ComputerUse: true, Bash: true, Memory: true, TextEditor: true, ToolSearch: true,
		ContainerBasic: true,
		// StructuredOutputs: kept true to match pre-existing behavior and the
		// provider_feature_support_test.go assertion, but NEITHER B-header
		// NOR B-platform upstream docs document strict tool validation /
		// output_format on Bedrock. Needs live verification. If Bedrock's
		// Converse API actually rejects `strict: true`, flip this to false
		// and update the corresponding test assertion.
		StructuredOutputs:   true,
		Compaction:          true, // compact-2026-01-12 per B-header
		ContextEditing:      true, // context-management-2025-06-27 per B-header (bundles memory)
		InterleavedThinking: true, // per B-header; model-allowlisted
		Context1M:           true, // Opus 4.6 / Sonnet 4.6 per A
		EagerInputStreaming: true, // fine-grained-tool-streaming-2025-05-14 per B-header
		InputExamples:       true, // tool-examples-2025-10-29 per B-header (standalone; Bedrock doesn't accept the full advanced-tool-use-2025-11-20 bundle — see TestFilterBetaHeadersForProvider)
		// AdvancedToolUse intentionally OFF on Bedrock. The bundle header
		// (advanced-tool-use-2025-11-20) is not listed in B-header; only the
		// narrow tool-examples-2025-10-29 header is, gated via InputExamples above.
	},
	// Microsoft Azure AI Foundry — cite: A (most features azureAiBeta) +
	// Az-platform ("supports most of Claude's features"). Excluded per
	// Az-platform: Admin API, Models API, Message Batch API (not in scope).
	schemas.Azure: {
		WebSearch: true, WebSearchDynamic: true, WebFetch: true, CodeExecution: true,
		ComputerUse: true, Bash: true, Memory: true, TextEditor: true, ToolSearch: true,
		MCP: true, AdvancedToolUse: true, InputExamples: true, StructuredOutputs: true, PromptCachingScope: true,
		Compaction: true, ContextEditing: true, FilesAPI: true,
		InterleavedThinking: true, Skills: true, ContainerBasic: true, Context1M: true,
		RedactThinking: true, TaskBudgets: true,
		EagerInputStreaming: true,
		// FastMode, InferenceGeo, AdvisorTool — not in Az-platform; leave off.
	},
}

// ==================== REQUEST TYPES ====================

// AnthropicTextRequest represents an Anthropic text completion request
type AnthropicTextRequest struct {
	Model             string   `json:"model"`
	Prompt            string   `json:"prompt"`
	MaxTokensToSample int      `json:"max_tokens_to_sample"`
	Temperature       *float64 `json:"temperature,omitempty"`
	TopP              *float64 `json:"top_p,omitempty"`
	TopK              *int     `json:"top_k,omitempty"`
	Stream            *bool    `json:"stream,omitempty"`
	StopSequences     []string `json:"stop_sequences,omitempty"`

	// Bifrost specific field (only parsed when converting from Provider -> Bifrost request)
	Fallbacks   []string               `json:"fallbacks,omitempty"`
	ExtraParams map[string]interface{} `json:"-"`
}

// GetExtraParams implements the RequestBodyWithExtraParams interface
func (req *AnthropicTextRequest) GetExtraParams() map[string]interface{} {
	return req.ExtraParams
}

// IsStreamingRequested implements the StreamingRequest interface
func (req *AnthropicTextRequest) IsStreamingRequested() bool {
	return req.Stream != nil && *req.Stream
}

// AnthropicTaskBudget represents an advisory token budget for a full agentic loop (output_config.task_budget).
// The model sees a running countdown and uses it to prioritize work and finish gracefully.
// Requires beta header "task-budgets-2026-03-13". Minimum total: 20 000 tokens.
// This is advisory, not a hard cap — use max_tokens as the per-request hard ceiling.
type AnthropicTaskBudget struct {
	Type      string `json:"type"`                // always "tokens"
	Total     int    `json:"total"`               // total advisory token budget across the agentic loop
	Remaining *int   `json:"remaining,omitempty"` // optional; tracks remaining tokens for client-side compaction
}

// AnthropicOutputConfig represents the GA structured outputs config (output_config.format),
// the effort parameter (output_config.effort), and the task budget (output_config.task_budget).
type AnthropicOutputConfig struct {
	Format     json.RawMessage      `json:"format,omitempty"`      // JSON schema for structured outputs
	Effort     *string              `json:"effort,omitempty"`      // "low" | "medium" | "high" | "xhigh" | "max"
	TaskBudget *AnthropicTaskBudget `json:"task_budget,omitempty"` // advisory token budget; requires task-budgets-2026-03-13 beta header
}

// AnthropicContainerSkill represents a single skill attached to a container.
// Requires beta header "skills-2025-10-02".
type AnthropicContainerSkill struct {
	SkillID string  `json:"skill_id"`          // Unique identifier for the skill
	Type    string  `json:"type"`              // "anthropic" (built-in) | "custom" (user-defined)
	Version *string `json:"version,omitempty"` // Optional version pin
}

// AnthropicContainerObject represents the object form of the container field:
// { id?: string, skills?: [...] }. The skills[] array is gated by the
// skills-2025-10-02 beta header; a bare id-only container is GA.
type AnthropicContainerObject struct {
	ID     *string                   `json:"id,omitempty"`
	Skills []AnthropicContainerSkill `json:"skills,omitempty"`
}

// AnthropicContainer is the "container" field on AnthropicMessageRequest.
// Per Anthropic docs it can be either a bare string (container id) or an
// object with id+skills[]. The object-with-skills form requires beta header
// "skills-2025-10-02"; the string form is GA.
// Source: https://platform.claude.com/docs/en/api/messages/create
type AnthropicContainer struct {
	ContainerStr    *string
	ContainerObject *AnthropicContainerObject
}

// MarshalJSON encodes the union as either a raw string or the object form.
func (c AnthropicContainer) MarshalJSON() ([]byte, error) {
	if c.ContainerStr != nil && c.ContainerObject != nil {
		return nil, fmt.Errorf("both ContainerStr and ContainerObject are set; only one should be non-nil")
	}
	if c.ContainerStr != nil {
		return providerUtils.MarshalSorted(*c.ContainerStr)
	}
	if c.ContainerObject != nil {
		return providerUtils.MarshalSorted(c.ContainerObject)
	}
	return providerUtils.MarshalSorted(nil)
}

// UnmarshalJSON decodes either a string or the object form into the union.
// Clears the inactive arm on each success so a reused struct never ends up
// with both fields populated (which MarshalJSON rejects). Explicitly handles
// JSON null. Matches the ChatContainer / ChatToolChoice union patterns.
func (c *AnthropicContainer) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		c.ContainerStr = nil
		c.ContainerObject = nil
		return nil
	}
	var s string
	if err := sonic.Unmarshal(data, &s); err == nil {
		c.ContainerStr = &s
		c.ContainerObject = nil
		return nil
	}
	var obj AnthropicContainerObject
	if err := sonic.Unmarshal(data, &obj); err == nil {
		c.ContainerStr = nil
		c.ContainerObject = &obj
		return nil
	}
	return fmt.Errorf("container field is neither a string nor a container object")
}

// AnthropicMessageRequest represents an Anthropic messages API request
type AnthropicMessageRequest struct {
	Model             string                 `json:"model"`
	MaxTokens         int                    `json:"max_tokens"`
	Messages          []AnthropicMessage     `json:"messages"`
	Metadata          *AnthropicMetaData     `json:"metadata,omitempty"`
	System            *AnthropicContent      `json:"system,omitempty"`
	CacheControl      *schemas.CacheControl  `json:"cache_control,omitempty"`
	Temperature       *float64               `json:"temperature,omitempty"`
	TopP              *float64               `json:"top_p,omitempty"`
	TopK              *int                   `json:"top_k,omitempty"`
	StopSequences     []string               `json:"stop_sequences,omitempty"`
	Stream            *bool                  `json:"stream,omitempty"`
	Tools             []AnthropicTool        `json:"tools,omitempty"`
	ToolChoice        *AnthropicToolChoice   `json:"tool_choice,omitempty"`
	MCPServers        []AnthropicMCPServerV2 `json:"mcp_servers,omitempty"` // Simplified server definitions (mcp-client-2025-11-20)
	Thinking          *AnthropicThinking     `json:"thinking,omitempty"`
	OutputFormat      json.RawMessage        `json:"output_format,omitempty"` // Beta: requires header "anthropic-beta": "structured-outputs-2025-11-13" (json.RawMessage preserves key ordering)
	OutputConfig      *AnthropicOutputConfig `json:"output_config,omitempty"` // GA: structured outputs without beta header
	Speed             *string                `json:"speed,omitempty"`         // "fast" for fast mode (Opus 4.6 only, requires fast-mode beta header)
	ServiceTier       *string                `json:"service_tier,omitempty"`  // "auto" or "standard_only"
	InferenceGeo      *string                `json:"inference_geo,omitempty"` // the geographic region for inference processing. If not specified, the workspace's default_inference_geo is used.
	ContextManagement *ContextManagement     `json:"context_management,omitempty"`
	Container         *AnthropicContainer    `json:"container,omitempty"` // string id OR object with skills[]; skills require skills-2025-10-02 beta

	// Extra params for advanced use cases
	ExtraParams map[string]interface{} `json:"-"`

	// Bifrost specific field (only parsed when converting from Provider -> Bifrost request)
	Fallbacks []string `json:"fallbacks,omitempty"`

	// Internal field to track whether to strip scope from cache control blocks (for Vertex + prompt caching scope)
	stripCacheControlScope bool `json:"-"`
}

// SetStripCacheControlScope sets the stripCacheControlScope flag
func (req *AnthropicMessageRequest) SetStripCacheControlScope(strip bool) {
	req.stripCacheControlScope = strip
}

// GetExtraParams implements the RequestBodyWithExtraParams interface
func (req *AnthropicMessageRequest) GetExtraParams() map[string]interface{} {
	return req.ExtraParams
}

type AnthropicMetaData struct {
	UserID *string `json:"user_id"`
}

type AnthropicThinking struct {
	Type         string  `json:"type"`                    // "enabled", "disabled", or "adaptive"
	BudgetTokens *int    `json:"budget_tokens,omitempty"` // Only for type "enabled" (not supported on Opus 4.7+)
	Display      *string `json:"display,omitempty"`       // "summarized" | "omitted" — controls whether thinking content appears in the response (Opus 4.7+)
}

type ContextManagementEditType string

const (
	ContextManagementEditTypeClearToolUses ContextManagementEditType = "clear_tool_uses_20250919"
	ContextManagementEditTypeClearThinking ContextManagementEditType = "clear_thinking_20251015"
	ContextManagementEditTypeCompact       ContextManagementEditType = "compact_20260112"
)

type CompactManagementEditTypeAndValueObject struct {
	Type  string `json:"type"`
	Value *int   `json:"value,omitempty"`
}

type CompactManagementEditTypeAndValue struct {
	TypeAndValueString *string
	TypeAndValueObject *CompactManagementEditTypeAndValueObject
}

// MarshalJSON implements custom JSON marshalling for CompactManagementEditTypeAndValue.
// It marshals either TypeAndValueString or TypeAndValueObject directly without wrapping.
func (tv CompactManagementEditTypeAndValue) MarshalJSON() ([]byte, error) {
	// Validation: ensure only one field is set at a time
	if tv.TypeAndValueString != nil && tv.TypeAndValueObject != nil {
		return nil, fmt.Errorf("both TypeAndValueString and TypeAndValueObject are set; only one should be non-nil")
	}

	if tv.TypeAndValueString != nil {
		return providerUtils.MarshalSorted(*tv.TypeAndValueString)
	}
	if tv.TypeAndValueObject != nil {
		return providerUtils.MarshalSorted(tv.TypeAndValueObject)
	}
	return providerUtils.MarshalSorted(nil)
}

// UnmarshalJSON implements custom JSON unmarshalling for CompactManagementEditTypeAndValue.
// It determines whether the field is a string or object and assigns to the appropriate field.
func (tv *CompactManagementEditTypeAndValue) UnmarshalJSON(data []byte) error {
	// First, try to unmarshal as a direct string
	var typeAndValueString string
	if err := sonic.Unmarshal(data, &typeAndValueString); err == nil {
		tv.TypeAndValueString = &typeAndValueString
		return nil
	}

	// Try to unmarshal as an object
	var objectContent CompactManagementEditTypeAndValueObject
	if err := sonic.Unmarshal(data, &objectContent); err == nil {
		tv.TypeAndValueObject = &objectContent
		return nil
	}

	return fmt.Errorf("field is neither a string nor a CompactManagementEditTypeAndValueObject")
}

type CompactManagementEditConfig struct {
	Trigger              *CompactManagementEditTypeAndValue `json:"trigger,omitempty"`
	PauseAfterCompaction *bool                              `json:"pause_after_compaction,omitempty"`
	Instructions         *string                            `json:"instructions,omitempty"`
}

type CompactManagementEditClearThinking struct {
	Keep *CompactManagementEditTypeAndValue `json:"keep,omitempty"`
}

type ClearToolInputs struct {
	ClearToolInputsBoolean *bool
	ClearToolInputsArray   []string
}

// MarshalJSON implements custom JSON marshalling for ClearToolInputs.
// It marshals either ClearToolInputsBoolean or ClearToolInputsArray directly without wrapping.
func (ct ClearToolInputs) MarshalJSON() ([]byte, error) {
	// Validation: ensure only one field is set at a time
	if ct.ClearToolInputsBoolean != nil && ct.ClearToolInputsArray != nil {
		return nil, fmt.Errorf("both ClearToolInputsBoolean and ClearToolInputsArray are set; only one should be non-nil")
	}

	if ct.ClearToolInputsBoolean != nil {
		return providerUtils.MarshalSorted(*ct.ClearToolInputsBoolean)
	}
	if ct.ClearToolInputsArray != nil {
		return providerUtils.MarshalSorted(ct.ClearToolInputsArray)
	}
	return providerUtils.MarshalSorted(nil)
}

// UnmarshalJSON implements custom JSON unmarshalling for ClearToolInputs.
// It determines whether the field is a boolean or array of strings and assigns to the appropriate field.
func (ct *ClearToolInputs) UnmarshalJSON(data []byte) error {
	// First, try to unmarshal as a boolean
	var clearToolInputsBoolean bool
	if err := sonic.Unmarshal(data, &clearToolInputsBoolean); err == nil {
		ct.ClearToolInputsBoolean = &clearToolInputsBoolean
		return nil
	}

	// Try to unmarshal as a direct array of strings
	var arrayContent []string
	if err := sonic.Unmarshal(data, &arrayContent); err == nil {
		ct.ClearToolInputsArray = arrayContent
		return nil
	}

	return fmt.Errorf("clear_tool_inputs field is neither a boolean nor an array of strings")
}

type CompactManagementEditClearToolUses struct {
	ClearToolInputs *ClearToolInputs                   `json:"clear_tool_inputs,omitempty"`
	ClearAtLast     *CompactManagementEditTypeAndValue `json:"clear_at_last,omitempty"`
	Keep            *CompactManagementEditTypeAndValue `json:"keep,omitempty"`
	ExcludeTools    []string                           `json:"exclude_tools,omitempty"`
	Trigger         *CompactManagementEditTypeAndValue `json:"trigger,omitempty"`
}

type ContextManagementEdit struct {
	Type ContextManagementEditType `json:"type"`
	*CompactManagementEditConfig
	*CompactManagementEditClearThinking
	*CompactManagementEditClearToolUses
}

func (edit ContextManagementEdit) MarshalJSON() ([]byte, error) {
	// Create a base map with the type field
	type Alias ContextManagementEdit

	// Marshal based on the type
	switch edit.Type {
	case ContextManagementEditTypeCompact:
		if edit.CompactManagementEditConfig == nil {
			return providerUtils.MarshalSorted(struct {
				Type ContextManagementEditType `json:"type"`
			}{
				Type: edit.Type,
			})
		}
		return providerUtils.MarshalSorted(struct {
			Type ContextManagementEditType `json:"type"`
			*CompactManagementEditConfig
		}{
			Type:                        edit.Type,
			CompactManagementEditConfig: edit.CompactManagementEditConfig,
		})
	case ContextManagementEditTypeClearThinking:
		if edit.CompactManagementEditClearThinking == nil {
			return nil, fmt.Errorf("compact management edit clear thinking is nil for type clear_thinking_20251015")
		}
		return providerUtils.MarshalSorted(struct {
			Type ContextManagementEditType `json:"type"`
			*CompactManagementEditClearThinking
		}{
			Type:                               edit.Type,
			CompactManagementEditClearThinking: edit.CompactManagementEditClearThinking,
		})
	case ContextManagementEditTypeClearToolUses:
		if edit.CompactManagementEditClearToolUses == nil {
			return nil, fmt.Errorf("compact management edit clear tool uses is nil for type clear_tool_uses_20250919")
		}
		return providerUtils.MarshalSorted(struct {
			Type ContextManagementEditType `json:"type"`
			*CompactManagementEditClearToolUses
		}{
			Type:                               edit.Type,
			CompactManagementEditClearToolUses: edit.CompactManagementEditClearToolUses,
		})
	default:
		return nil, fmt.Errorf("unknown context management edit type: %s", edit.Type)
	}
}

func (edit *ContextManagementEdit) UnmarshalJSON(data []byte) error {
	// First, peek at the type field to determine which variant to unmarshal
	var typeStruct struct {
		Type ContextManagementEditType `json:"type"`
	}
	if err := sonic.Unmarshal(data, &typeStruct); err != nil {
		return fmt.Errorf("failed to peek at type field: %w", err)
	}

	// Set the type
	edit.Type = typeStruct.Type

	// Based on the type, unmarshal into the appropriate variant
	switch typeStruct.Type {
	case ContextManagementEditTypeCompact:
		var config CompactManagementEditConfig
		if err := sonic.Unmarshal(data, &config); err != nil {
			return fmt.Errorf("failed to unmarshal compact management edit config: %w", err)
		}
		edit.CompactManagementEditConfig = &config
		return nil

	case ContextManagementEditTypeClearThinking:
		var clearThinking CompactManagementEditClearThinking
		if err := sonic.Unmarshal(data, &clearThinking); err != nil {
			return fmt.Errorf("failed to unmarshal compact management edit clear thinking: %w", err)
		}
		edit.CompactManagementEditClearThinking = &clearThinking
		return nil

	case ContextManagementEditTypeClearToolUses:
		var clearToolUses CompactManagementEditClearToolUses
		if err := sonic.Unmarshal(data, &clearToolUses); err != nil {
			return fmt.Errorf("failed to unmarshal compact management edit clear tool uses: %w", err)
		}
		edit.CompactManagementEditClearToolUses = &clearToolUses
		return nil

	default:
		return fmt.Errorf("unknown context management edit type: %s", typeStruct.Type)
	}
}

type ContextManagement struct {
	Edits []ContextManagementEdit `json:"edits,omitempty"`
}

// IsStreamingRequested implements the StreamingRequest interface
func (req *AnthropicMessageRequest) IsStreamingRequested() bool {
	return req.Stream != nil && *req.Stream
}

// Known fields for AnthropicMessageRequest
var anthropicMessageRequestKnownFields = map[string]bool{
	"model":              true,
	"max_tokens":         true,
	"messages":           true,
	"metadata":           true,
	"system":             true,
	"cache_control":      true,
	"temperature":        true,
	"top_p":              true,
	"top_k":              true,
	"stop_sequences":     true,
	"stream":             true,
	"tools":              true,
	"tool_choice":        true,
	"mcp_servers":        true,
	"thinking":           true,
	"output_format":      true,
	"output_config":      true,
	"speed":              true,
	"service_tier":       true,
	"inference_geo":      true,
	"context_management": true,
	"container":          true,
	"extra_params":       true,
	"fallbacks":          true,
}

// UnmarshalJSON implements custom JSON unmarshalling for AnthropicMessageRequest.
// This captures all unregistered fields into ExtraParams.
func (req *AnthropicMessageRequest) UnmarshalJSON(data []byte) error {
	// Create an alias type to avoid infinite recursion
	type Alias AnthropicMessageRequest

	// First, unmarshal into the alias to populate all known fields
	aux := &struct {
		*Alias
	}{
		Alias: (*Alias)(req),
	}

	if err := sonic.Unmarshal(data, aux); err != nil {
		return err
	}

	// Parse JSON to extract unknown fields
	var rawData map[string]json.RawMessage
	if err := sonic.Unmarshal(data, &rawData); err != nil {
		return err
	}

	// Initialize ExtraParams if not already initialized
	if req.ExtraParams == nil {
		req.ExtraParams = make(map[string]interface{})
	}

	// Extract unknown fields, preserving nested key ordering for prompt caching.
	// Store as json.RawMessage (compacted) instead of parsing into map[string]interface{}
	// which would destroy key order on re-serialization.
	for key, value := range rawData {
		if !anthropicMessageRequestKnownFields[key] {
			var buf bytes.Buffer
			if err := json.Compact(&buf, value); err == nil {
				req.ExtraParams[key] = json.RawMessage(buf.Bytes())
			} else {
				req.ExtraParams[key] = json.RawMessage(value)
			}
		}
	}

	// Compact known json.RawMessage fields for deterministic cache keys
	if len(req.OutputFormat) > 0 {
		var buf bytes.Buffer
		if err := json.Compact(&buf, req.OutputFormat); err == nil {
			req.OutputFormat = json.RawMessage(buf.Bytes())
		}
	}
	if req.OutputConfig != nil && len(req.OutputConfig.Format) > 0 {
		var buf bytes.Buffer
		if err := json.Compact(&buf, req.OutputConfig.Format); err == nil {
			req.OutputConfig.Format = json.RawMessage(buf.Bytes())
		}
	}

	return nil
}

// MarshalJSON implements custom JSON marshalling for AnthropicMessageRequest.
// It validates that OutputFormat and OutputConfig are mutually exclusive.
// When stripCacheControlScope is true (for Vertex + prompt caching scope), it strips
// the scope field from all cache control blocks in tools, system, and messages.
func (req *AnthropicMessageRequest) MarshalJSON() ([]byte, error) {
	// Validation: ensure OutputFormat and OutputConfig are not both set
	if req.OutputFormat != nil && req.OutputConfig != nil {
		return nil, fmt.Errorf("both OutputFormat and OutputConfig are set; only one should be non-nil")
	}

	// Use alias type to avoid infinite recursion
	type Alias AnthropicMessageRequest

	// If stripCacheControlScope is enabled, create a copy and strip scope from all cache control blocks
	if req.stripCacheControlScope {
		reqCopy := *req
		reqCopy.stripCacheControlScope = false

		// Strip scope from top-level cache_control
		if reqCopy.CacheControl != nil && reqCopy.CacheControl.Scope != nil {
			cc := *reqCopy.CacheControl
			cc.Scope = nil
			reqCopy.CacheControl = &cc
		}

		// Strip scope from tools
		if len(reqCopy.Tools) > 0 {
			toolsCopy := make([]AnthropicTool, len(reqCopy.Tools))
			for i, tool := range reqCopy.Tools {
				toolsCopy[i] = tool
				if tool.CacheControl != nil && tool.CacheControl.Scope != nil {
					// Create a copy of cache control without scope
					toolsCopy[i].CacheControl = &schemas.CacheControl{
						Type: tool.CacheControl.Type,
						TTL:  tool.CacheControl.TTL,
						// Scope is intentionally omitted
					}
				}
			}
			reqCopy.Tools = toolsCopy
		}

		// Strip scope from system content
		if reqCopy.System != nil {
			reqCopy.System = stripScopeFromContent(reqCopy.System)
		}

		// Strip scope from messages
		if len(reqCopy.Messages) > 0 {
			messagesCopy := make([]AnthropicMessage, len(reqCopy.Messages))
			for i, msg := range reqCopy.Messages {
				messagesCopy[i] = msg
				messagesCopy[i].Content = *stripScopeFromContent(&msg.Content)
			}
			reqCopy.Messages = messagesCopy
		}

		return providerUtils.MarshalSorted((*Alias)(&reqCopy))
	}

	return providerUtils.MarshalSorted((*Alias)(req))
}

// stripScopeFromContent strips scope from all cache control blocks in content
func stripScopeFromContent(content *AnthropicContent) *AnthropicContent {
	if content == nil {
		return nil
	}

	result := &AnthropicContent{
		ContentStr: content.ContentStr,
	}

	if len(content.ContentBlocks) > 0 {
		blocksCopy := make([]AnthropicContentBlock, len(content.ContentBlocks))
		for i, block := range content.ContentBlocks {
			blocksCopy[i] = block
			if block.CacheControl != nil && block.CacheControl.Scope != nil {
				// Create a copy of cache control without scope
				blocksCopy[i].CacheControl = &schemas.CacheControl{
					Type: block.CacheControl.Type,
					TTL:  block.CacheControl.TTL,
					// Scope is intentionally omitted
				}
			}
		}
		result.ContentBlocks = blocksCopy
	}

	return result
}

type AnthropicMessageRole string

const (
	AnthropicMessageRoleUser      AnthropicMessageRole = "user"
	AnthropicMessageRoleAssistant AnthropicMessageRole = "assistant"
)

// AnthropicMessage represents a message in Anthropic format
type AnthropicMessage struct {
	Role    AnthropicMessageRole `json:"role"`    // "user", "assistant"
	Content AnthropicContent     `json:"content"` // Array of content blocks
}

// AnthropicContent represents content that can be either string or array of blocks
type AnthropicContent struct {
	ContentStr    *string
	ContentBlocks []AnthropicContentBlock
}

// MarshalJSON implements custom JSON marshalling for AnthropicContent.
// It marshals either ContentStr or ContentBlocks directly without wrapping.
func (mc AnthropicContent) MarshalJSON() ([]byte, error) {
	// Validation: ensure only one field is set at a time
	if mc.ContentStr != nil && mc.ContentBlocks != nil {
		return nil, fmt.Errorf("both ContentStr and ContentBlocks are set; only one should be non-nil")
	}

	if mc.ContentStr != nil {
		return providerUtils.MarshalSorted(*mc.ContentStr)
	}
	if mc.ContentBlocks != nil {
		return providerUtils.MarshalSorted(mc.ContentBlocks)
	}
	// If both are nil, return empty array instead of null.
	// Anthropic's API requires content to be an array, not null.
	return []byte("[]"), nil
}

// UnmarshalJSON implements custom JSON unmarshalling for AnthropicContent.
// It determines whether "content" is a string or array and assigns to the appropriate field.
func (mc *AnthropicContent) UnmarshalJSON(data []byte) error {
	// First, try to unmarshal as a direct string
	var stringContent string
	if err := sonic.Unmarshal(data, &stringContent); err == nil {
		mc.ContentStr = &stringContent
		return nil
	}

	// Try to unmarshal as a direct array of ContentBlock
	var arrayContent []AnthropicContentBlock
	if err := sonic.Unmarshal(data, &arrayContent); err == nil {
		mc.ContentBlocks = arrayContent
		return nil
	}

	// Try to unmarshal as a single ContentBlock object (e.g., web_search_tool_result_error)
	// If successful, wrap it in an array
	var singleBlock AnthropicContentBlock
	if err := sonic.Unmarshal(data, &singleBlock); err == nil && singleBlock.Type != "" {
		mc.ContentBlocks = []AnthropicContentBlock{singleBlock}
		return nil
	}

	return fmt.Errorf("content field is neither a string nor an array of ContentBlock")
}

type AnthropicContentBlockType string

const (
	AnthropicContentBlockTypeText                               AnthropicContentBlockType = "text"
	AnthropicContentBlockTypeImage                              AnthropicContentBlockType = "image"
	AnthropicContentBlockTypeDocument                           AnthropicContentBlockType = "document"
	AnthropicContentBlockTypeSearchResult                       AnthropicContentBlockType = "search_result"
	AnthropicContentBlockTypeToolUse                            AnthropicContentBlockType = "tool_use"
	AnthropicContentBlockTypeServerToolUse                      AnthropicContentBlockType = "server_tool_use"
	AnthropicContentBlockTypeToolResult                         AnthropicContentBlockType = "tool_result"
	AnthropicContentBlockTypeWebSearchToolResult                AnthropicContentBlockType = "web_search_tool_result"
	AnthropicContentBlockTypeWebSearchToolResultError           AnthropicContentBlockType = "web_search_tool_result_error"
	AnthropicContentBlockTypeWebSearchResult                    AnthropicContentBlockType = "web_search_result"
	AnthropicContentBlockTypeWebFetchToolResult                 AnthropicContentBlockType = "web_fetch_tool_result"
	AnthropicContentBlockTypeCodeExecutionToolResult            AnthropicContentBlockType = "code_execution_tool_result"
	AnthropicContentBlockTypeBashCodeExecutionToolResult        AnthropicContentBlockType = "bash_code_execution_tool_result"
	AnthropicContentBlockTypeTextEditorCodeExecutionToolResult  AnthropicContentBlockType = "text_editor_code_execution_tool_result"
	AnthropicContentBlockTypeToolSearchToolResult               AnthropicContentBlockType = "tool_search_tool_result"
	AnthropicContentBlockTypeToolReference                      AnthropicContentBlockType = "tool_reference"
	AnthropicContentBlockTypeContainerUpload                    AnthropicContentBlockType = "container_upload"
	AnthropicContentBlockTypeAdvisorToolResult                  AnthropicContentBlockType = "advisor_tool_result"
	AnthropicContentBlockTypeMCPToolUse                         AnthropicContentBlockType = "mcp_tool_use"
	AnthropicContentBlockTypeMCPToolResult                      AnthropicContentBlockType = "mcp_tool_result"
	AnthropicContentBlockTypeThinking                           AnthropicContentBlockType = "thinking"
	AnthropicContentBlockTypeRedactedThinking                   AnthropicContentBlockType = "redacted_thinking"
	AnthropicContentBlockTypeCompaction                         AnthropicContentBlockType = "compaction"
)

// AnthropicToolCallerType identifies which agentic caller produced a tool
// invocation. Appears on tool_use, server_tool_use, and every *_tool_result
// block per Anthropic docs.
// Source: https://platform.claude.com/docs/en/api/beta/messages/create
type AnthropicToolCallerType string

const (
	AnthropicToolCallerTypeDirect                 AnthropicToolCallerType = "direct"
	AnthropicToolCallerTypeCodeExecution20250825  AnthropicToolCallerType = "code_execution_20250825"
	AnthropicToolCallerTypeCodeExecution20260120  AnthropicToolCallerType = "code_execution_20260120"
)

// AnthropicToolCaller represents the "caller" union on tool-use and
// tool-result blocks. For the two code-execution variants, ToolID is required
// and identifies the upstream server tool that invoked the nested tool.
type AnthropicToolCaller struct {
	Type   AnthropicToolCallerType `json:"type"`
	ToolID *string                 `json:"tool_id,omitempty"` // Required for code_execution_* caller types
}

// AnthropicContentBlock represents content in Anthropic message format.
// This is a fat struct: every optional field here is used by at least one
// block type. Consult Anthropic's content-block docs before adding a field
// so we reuse existing ones where semantics align.
type AnthropicContentBlock struct {
	Type             AnthropicContentBlockType `json:"type"`                        // Discriminator
	Text             *string                   `json:"text,omitempty"`              // text block; also "advisor_result" variant
	Thinking         *string                   `json:"thinking,omitempty"`          // thinking block
	Signature        *string                   `json:"signature,omitempty"`         // thinking block signature
	Data             *string                   `json:"data,omitempty"`              // redacted_thinking encrypted data (no signature)
	ToolUseID        *string                   `json:"tool_use_id,omitempty"`       // tool_result, *_tool_result blocks
	ID               *string                   `json:"id,omitempty"`                // tool_use, server_tool_use, mcp_tool_use
	Name             *string                   `json:"name,omitempty"`              // tool_use, server_tool_use; also reused for tool_reference's tool_name via ToolName
	Input            json.RawMessage           `json:"input,omitempty"`             // tool_use / server_tool_use (json.RawMessage preserves key ordering for prompt caching)
	ServerName       *string                   `json:"server_name,omitempty"`       // mcp_tool_use
	Content          *AnthropicContent         `json:"content,omitempty"`           // tool_result, *_tool_result; inner structured content or string
	IsError          *bool                     `json:"is_error,omitempty"`          // tool_result, *_tool_result
	Source           *AnthropicBlockSource     `json:"source,omitempty"`            // image, document (SourceObj) or search_result (SourceStr) — union type
	CacheControl     *schemas.CacheControl     `json:"cache_control,omitempty"`     // any block
	Citations        *AnthropicCitations       `json:"citations,omitempty"`         // text, document, search_result (request config) or response citations array
	Context          *string                   `json:"context,omitempty"`           // document
	Title            *string                   `json:"title,omitempty"`             // document, search_result, web_search_result
	URL              *string                   `json:"url,omitempty"`               // web_search_result, web_fetch_result
	EncryptedContent *string                   `json:"encrypted_content,omitempty"` // web_search_result, advisor_redacted_result, compaction
	PageAge          *string                   `json:"page_age,omitempty"`          // web_search_result
	ErrorCode        *string                   `json:"error_code,omitempty"`        // any *_tool_result_error variant
	Caller           *AnthropicToolCaller      `json:"caller,omitempty"`            // tool_use, server_tool_use, every *_tool_result block

	// search_result block: the API uses the literal key "source" with a plain
	// string value, which collides with the existing Source *AnthropicSource
	// field (object form, used by image/document). Supporting both requires
	// either (a) a string-or-object union type for Source, or (b) full custom
	// Marshal/Unmarshal on AnthropicContentBlock. Deferred until we decide the
	// representation — search_result block enum is present above but its
	// source string has no typed slot yet. Callers needing it can use
	// ExtraParams pass-through on the request side in the meantime.

	// code_execution_tool_result / bash_code_execution_tool_result result-variant fields
	Stdout          *string `json:"stdout,omitempty"`
	Stderr          *string `json:"stderr,omitempty"`
	ReturnCode      *int    `json:"return_code,omitempty"`
	EncryptedStdout *string `json:"encrypted_stdout,omitempty"`

	// text_editor_code_execution_tool_result variants
	FileType     *string  `json:"file_type,omitempty"`    // view_result: "text"|"image"|"pdf"
	StartLine    *int     `json:"start_line,omitempty"`   // view_result
	NumLines     *int     `json:"num_lines,omitempty"`    // view_result
	TotalLines   *int     `json:"total_lines,omitempty"`  // view_result
	IsFileUpdate *bool    `json:"is_file_update,omitempty"` // create_result
	OldStart     *int     `json:"old_start,omitempty"`    // str_replace_result
	OldLines     *int     `json:"old_lines,omitempty"`    // str_replace_result
	NewStart     *int     `json:"new_start,omitempty"`    // str_replace_result
	NewLines     *int     `json:"new_lines,omitempty"`    // str_replace_result
	Lines        []string `json:"lines,omitempty"`        // str_replace_result
	ErrorMessage *string  `json:"error_message,omitempty"` // text_editor error variant

	// tool_search_tool_result success variant
	ToolReferences []AnthropicContentBlock `json:"tool_references,omitempty"` // tool_search_tool_search_result (array of tool_reference blocks)

	// tool_reference block — tool_name field on the block itself
	ToolName *string `json:"tool_name,omitempty"`

	// container_upload block + web_fetch_result inner file_id reference
	FileID *string `json:"file_id,omitempty"`

	// web_fetch_tool_result / web_fetch_result inner retrieval timestamp
	RetrievedAt *string `json:"retrieved_at,omitempty"`
}

// AnthropicSource represents image or document source in Anthropic format.
//
// Per docs (https://platform.claude.com/docs/en/api/messages/create) the
// documented type values and their carrying fields are:
//   - "base64"         → MediaType + Data
//   - "url"            → URL
//   - "text"           → MediaType ("text/plain") + Data
//   - "content_block"  → Content (nested string OR array of inner blocks);
//                        recursive ContentBlockSource used inside DocumentBlockParam
//   - "file"           → FileID (requires files-api-2025-04-14 beta)
//
// The struct is a superset — only the fields relevant to Type should be set
// at a time.
type AnthropicSource struct {
	Type      string          `json:"type"`                 // "base64" | "url" | "text" | "content" | "content_block" (alias) | "file"
	MediaType *string         `json:"media_type,omitempty"` // "image/jpeg", "image/png", "application/pdf", etc.
	Data      *string         `json:"data,omitempty"`       // Base64-encoded data (base64 type) or text payload (text type)
	URL       *string         `json:"url,omitempty"`        // URL (url type)
	FileID    *string         `json:"file_id,omitempty"`    // File ID (file type; requires files-api-2025-04-14 beta)
	Content   json.RawMessage `json:"content,omitempty"`    // For content_block type: nested content — string OR array of inner blocks (TextBlockParam / ImageBlockParam). json.RawMessage preserves exact bytes for prompt caching.
}

// AnthropicBlockSource is the union "source" field on a content block.
//
// Anthropic's API uses the literal JSON key "source" for two incompatible
// shapes depending on which block the key appears on:
//
//   - On `image` / `document` blocks: an OBJECT describing the source
//     (type + media_type + data/url/file_id). Modeled by AnthropicSource.
//   - On `search_result` blocks: a plain STRING identifier (URL/path).
//
// This union wrapper lets AnthropicContentBlock carry either shape under
// the single "source" JSON key.
//
// Docs:
//   - https://platform.claude.com/docs/en/api/messages/create (ImageBlockParam, DocumentBlockParam)
//   - https://platform.claude.com/docs/en/api/beta/messages/create (SearchResultBlockParam)
type AnthropicBlockSource struct {
	SourceStr *string          // search_result: plain string (URL, path, identifier)
	SourceObj *AnthropicSource // image / document: object form
}

// MarshalJSON emits either the string or the object form directly (unwrapped).
// Matches the union-type idiom used by AnthropicCitations, AnthropicContainer,
// and CompactManagementEditTypeAndValue.
func (s AnthropicBlockSource) MarshalJSON() ([]byte, error) {
	if s.SourceStr != nil && s.SourceObj != nil {
		return nil, fmt.Errorf("both SourceStr and SourceObj are set; only one should be non-nil")
	}
	if s.SourceStr != nil {
		return providerUtils.MarshalSorted(*s.SourceStr)
	}
	if s.SourceObj != nil {
		return providerUtils.MarshalSorted(s.SourceObj)
	}
	return providerUtils.MarshalSorted(nil)
}

// UnmarshalJSON decodes either the string or the object form into the union.
// Matches AnthropicCitations.UnmarshalJSON: sonic-decode into each variant,
// first success wins.
// UnmarshalJSON decodes either the string form (search_result blocks) or the
// object form (image/document blocks) into the union. Clears the inactive
// arm on each success so a reused struct never ends up with both fields
// populated (which MarshalJSON rejects). Explicitly handles JSON null.
func (s *AnthropicBlockSource) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		s.SourceStr = nil
		s.SourceObj = nil
		return nil
	}
	var str string
	if err := sonic.Unmarshal(data, &str); err == nil {
		s.SourceStr = &str
		s.SourceObj = nil
		return nil
	}
	var obj AnthropicSource
	if err := sonic.Unmarshal(data, &obj); err == nil {
		s.SourceStr = nil
		s.SourceObj = &obj
		return nil
	}
	return fmt.Errorf("source field is neither a string nor an AnthropicSource object")
}

type AnthropicCitationType string

const (
	AnthropicCitationTypeCharLocation            AnthropicCitationType = "char_location"
	AnthropicCitationTypePageLocation            AnthropicCitationType = "page_location"
	AnthropicCitationTypeContentBlockLocation    AnthropicCitationType = "content_block_location"
	AnthropicCitationTypeWebSearchResultLocation AnthropicCitationType = "web_search_result_location"
	AnthropicCitationTypeSearchResultLocation    AnthropicCitationType = "search_result_location"
)

// AnthropicTextCitation represents a single citation in a response
// Supports multiple citation types: char_location, page_location, content_block_location,
// web_search_result_location, and search_result_location
type AnthropicTextCitation struct {
	Type      AnthropicCitationType `json:"type"` // "char_location", "page_location", "content_block_location", "web_search_result_location", "search_result_location"
	CitedText string                `json:"cited_text"`

	// File ID char_location, page_location, content_block_location
	FileID *string `json:"file_id,omitempty"`
	// Common fields for document-based citations
	DocumentIndex *int    `json:"document_index,omitempty"`
	DocumentTitle *string `json:"document_title,omitempty"`

	// Character location fields (type: "char_location")
	StartCharIndex *int `json:"start_char_index,omitempty"`
	EndCharIndex   *int `json:"end_char_index,omitempty"`

	// Page location fields (type: "page_location")
	StartPageNumber *int `json:"start_page_number,omitempty"`
	EndPageNumber   *int `json:"end_page_number,omitempty"`

	// Content block location fields (type: "content_block_location" or "search_result_location")
	StartBlockIndex *int `json:"start_block_index,omitempty"`
	EndBlockIndex   *int `json:"end_block_index,omitempty"`

	// Web search result fields (type: "web_search_result_location")
	EncryptedIndex *string `json:"encrypted_index,omitempty"`
	Title          *string `json:"title,omitempty"`
	URL            *string `json:"url,omitempty"`

	// Search result location fields (type: "search_result_location")
	SearchResultIndex *int    `json:"search_result_index,omitempty"`
	Source            *string `json:"source,omitempty"`
}

// AnthropicCitations can represent either:
// - Request: {enabled: true}
// - Response: [{type: "...", cited_text: "...", ...}]
type AnthropicCitations struct {
	// For requests (document configuration)
	Config *schemas.Citations
	// For responses (array of citations)
	TextCitations []AnthropicTextCitation
}

// MarshalJSON implements the json.Marshaler interface
func (ac *AnthropicCitations) MarshalJSON() ([]byte, error) {
	if len(ac.TextCitations) == 0 {
		ac.TextCitations = nil
	}
	if ac.Config != nil && ac.TextCitations != nil {
		return nil, fmt.Errorf("both Config and TextCitations are set; only one should be non-nil")
	}

	if ac.Config != nil {
		return providerUtils.MarshalSorted(ac.Config)
	}
	if ac.TextCitations != nil {
		return providerUtils.MarshalSorted(ac.TextCitations)
	}
	return providerUtils.MarshalSorted(nil)
}

// UnmarshalJSON implements the json.Unmarshaler interface
func (ac *AnthropicCitations) UnmarshalJSON(data []byte) error {
	// Try to unmarshal as array of citations
	var textCitations []AnthropicTextCitation
	if err := sonic.Unmarshal(data, &textCitations); err == nil {
		ac.Config = nil
		ac.TextCitations = textCitations
		return nil
	}

	// Try to unmarshal as config object first
	var config schemas.Citations
	if err := sonic.Unmarshal(data, &config); err == nil {
		ac.TextCitations = nil
		ac.Config = &config
		return nil
	}

	return fmt.Errorf("citations field is neither a config object nor an array of citations")
}

// AnthropicImageContent represents image content in Anthropic format
type AnthropicImageContent struct {
	Type      schemas.ImageContentType `json:"type"`
	URL       string                   `json:"url"`
	MediaType string                   `json:"media_type,omitempty"`
}

type AnthropicToolType string

const (
	AnthropicToolTypeCustom             AnthropicToolType = "custom"
	AnthropicToolTypeBash20241022       AnthropicToolType = "bash_20241022" // computer-use-2024-10-22 beta
	AnthropicToolTypeBash20250124       AnthropicToolType = "bash_20250124"
	AnthropicToolTypeComputer20241022   AnthropicToolType = "computer_20241022" // computer-use-2024-10-22 beta
	AnthropicToolTypeComputer20250124   AnthropicToolType = "computer_20250124"
	AnthropicToolTypeComputer20251124   AnthropicToolType = "computer_20251124" // for claude-opus-4.5, claude-opus-4.6, claude-sonnet-4.6
	AnthropicToolTypeTextEditor20250124 AnthropicToolType = "text_editor_20250124"
	AnthropicToolTypeTextEditor20250429 AnthropicToolType = "text_editor_20250429"
	AnthropicToolTypeTextEditor20250728 AnthropicToolType = "text_editor_20250728"

	// Code execution
	AnthropicToolTypeCodeExecution20250522 AnthropicToolType = "code_execution_20250522" // Legacy Python-only
	AnthropicToolTypeCodeExecution         AnthropicToolType = "code_execution_20250825"
	AnthropicToolTypeCodeExecution20260120 AnthropicToolType = "code_execution_20260120" // Programmatic tool calling

	// Web search
	AnthropicToolTypeWebSearch20250305 AnthropicToolType = "web_search_20250305"
	AnthropicToolTypeWebSearch20260209 AnthropicToolType = "web_search_20260209" // Dynamic filtering (Opus 4.6 / Sonnet 4.6)

	// Web fetch
	AnthropicToolTypeWebFetch20250910 AnthropicToolType = "web_fetch_20250910"
	AnthropicToolTypeWebFetch20260209 AnthropicToolType = "web_fetch_20260209" // Dynamic filtering
	AnthropicToolTypeWebFetch20260309 AnthropicToolType = "web_fetch_20260309"

	// Memory (client-side)
	AnthropicToolTypeMemory20250818 AnthropicToolType = "memory_20250818"

	// Tool search (client-side, for defer_loading)
	AnthropicToolTypeToolSearchBM25          AnthropicToolType = "tool_search_tool_bm25"
	AnthropicToolTypeToolSearchBM2520251119  AnthropicToolType = "tool_search_tool_bm25_20251119"
	AnthropicToolTypeToolSearchRegex         AnthropicToolType = "tool_search_tool_regex"
	AnthropicToolTypeToolSearchRegex20251119 AnthropicToolType = "tool_search_tool_regex_20251119"
)

type AnthropicToolName string

const (
	AnthropicToolNameComputer        AnthropicToolName = "computer"
	AnthropicToolNameWebSearch       AnthropicToolName = "web_search"
	AnthropicToolNameWebFetch        AnthropicToolName = "web_fetch"
	AnthropicToolNameBash            AnthropicToolName = "bash"
	AnthropicToolNameTextEditor      AnthropicToolName = "str_replace_based_edit_tool"
	AnthropicToolNameCodeExecution   AnthropicToolName = "code_execution"
	AnthropicToolNameMemory          AnthropicToolName = "memory"
	AnthropicToolNameToolSearchBM25  AnthropicToolName = "tool_search_tool_bm25"
	AnthropicToolNameToolSearchRegex AnthropicToolName = "tool_search_tool_regex"
)

type AnthropicToolComputerUse struct {
	DisplayWidthPx  *int  `json:"display_width_px,omitempty"`
	DisplayHeightPx *int  `json:"display_height_px,omitempty"`
	DisplayNumber   *int  `json:"display_number,omitempty"`
	EnableZoom      *bool `json:"enable_zoom,omitempty"` // for computer tool computer_20251124 only
}

type AnthropicToolWebSearchUserLocation struct {
	Type     *string `json:"type,omitempty"` // "approximate"
	City     *string `json:"city,omitempty"`
	Region   *string `json:"region,omitempty"`
	Country  *string `json:"country,omitempty"`
	Timezone *string `json:"timezone,omitempty"`
}

type AnthropicToolWebSearch struct {
	MaxUses        *int                                `json:"max_uses,omitempty"`
	AllowedDomains []string                            `json:"allowed_domains,omitempty"`
	BlockedDomains []string                            `json:"blocked_domains,omitempty"`
	UserLocation   *AnthropicToolWebSearchUserLocation `json:"user_location,omitempty"`
}

type AnthropicToolWebFetch struct {
	MaxUses          *int                `json:"max_uses,omitempty"`
	AllowedDomains   []string            `json:"allowed_domains,omitempty"`
	BlockedDomains   []string            `json:"blocked_domains,omitempty"`
	MaxContentTokens *int                `json:"max_content_tokens,omitempty"`
	Citations        *AnthropicCitations `json:"citations,omitempty"` // {enabled: bool} — toggles citation emission on fetched documents
	UseCache         *bool               `json:"use_cache,omitempty"` // web_fetch_20260309+ only — enables server-side page cache
}

// AnthropicToolTextEditor holds fields specific to the text_editor tool
// variants. Only text_editor_20250728 (and later) honours max_characters
// as a view-truncation cap.
type AnthropicToolTextEditor struct {
	MaxCharacters *int `json:"max_characters,omitempty"` // text_editor_20250728+ only
}

// AnthropicToolInputExample represents an input example for a tool (beta feature)
type AnthropicToolInputExample struct {
	Input       json.RawMessage `json:"input"`
	Description *string         `json:"description,omitempty"`
}

// AnthropicTool represents a tool in Anthropic format
type AnthropicTool struct {
	Name                string                          `json:"name"`
	Type                *AnthropicToolType              `json:"type,omitempty"`
	Description         *string                         `json:"description,omitempty"`
	InputSchema         *schemas.ToolFunctionParameters `json:"input_schema,omitempty"`
	CacheControl        *schemas.CacheControl           `json:"cache_control,omitempty"`
	DeferLoading        *bool                           `json:"defer_loading,omitempty"`         // Beta: defer loading of tool definition
	Strict              *bool                           `json:"strict,omitempty"`                // Whether to enforce strict parameter validation
	AllowedCallers      []string                        `json:"allowed_callers,omitempty"`       // Beta: which callers can use this tool
	InputExamples       []AnthropicToolInputExample     `json:"input_examples,omitempty"`        // Beta: example inputs for the tool
	EagerInputStreaming *bool                           `json:"eager_input_streaming,omitempty"` // Custom tools only; beta fine-grained-tool-streaming-2025-05-14

	*AnthropicToolComputerUse
	*AnthropicToolWebSearch
	*AnthropicToolWebFetch
	*AnthropicToolTextEditor

	// MCP toolset (mcp-client-2025-11-20 format) — embedded when Type is nil and MCPToolset is set
	MCPToolset *AnthropicMCPToolsetTool `json:"-"` // Serialized via custom MarshalJSON
}

// MarshalJSON implements custom JSON marshaling for AnthropicTool.
// When MCPToolset is set, serializes as an mcp_toolset tool instead of a regular tool.
func (t AnthropicTool) MarshalJSON() ([]byte, error) {
	if t.MCPToolset != nil {
		return providerUtils.MarshalSorted(t.MCPToolset)
	}
	// Use an alias to avoid infinite recursion
	type Alias AnthropicTool
	return providerUtils.MarshalSorted((Alias)(t))
}

// UnmarshalJSON implements custom JSON unmarshaling for AnthropicTool.
// Detects "type": "mcp_toolset" entries and populates the MCPToolset field,
// which would otherwise be skipped due to the json:"-" tag.
func (t *AnthropicTool) UnmarshalJSON(data []byte) error {
	// Peek at the type field to detect mcp_toolset entries
	var peek struct {
		Type string `json:"type"`
	}
	if err := sonic.Unmarshal(data, &peek); err == nil && peek.Type == "mcp_toolset" {
		var toolset AnthropicMCPToolsetTool
		if err := sonic.Unmarshal(data, &toolset); err != nil {
			return err
		}
		t.MCPToolset = &toolset
		return nil
	}
	// Default unmarshaling for all other tool types
	type Alias AnthropicTool
	return sonic.Unmarshal(data, (*Alias)(t))
}

// AnthropicToolChoice represents tool choice in Anthropic format
type AnthropicToolChoice struct {
	Type                   string `json:"type"`                                // "auto", "any", "tool", "none"
	Name                   string `json:"name,omitempty"`                      // For type "tool"
	DisableParallelToolUse *bool  `json:"disable_parallel_tool_use,omitempty"` // Whether to disable parallel tool use
}

// AnthropicToolContent represents content within tool result blocks
type AnthropicToolContent struct {
	Type             string  `json:"type"`
	Title            string  `json:"title,omitempty"`
	URL              string  `json:"url,omitempty"`
	EncryptedContent string  `json:"encrypted_content,omitempty"`
	PageAge          *string `json:"page_age,omitempty"`
}

// AnthropicMCPServer represents an MCP server definition (deprecated mcp-client-2025-04-04 format).
// Kept for backward-compatible response parsing.
type AnthropicMCPServer struct {
	Type               string                  `json:"type"`
	URL                string                  `json:"url"`
	Name               string                  `json:"name"`
	AuthorizationToken *string                 `json:"authorization_token,omitempty"`
	ToolConfiguration  *AnthropicMCPToolConfig `json:"tool_configuration,omitempty"` // Deprecated: use AnthropicMCPToolsetTool in tools[] instead
}

type AnthropicMCPToolConfig struct {
	Enabled      bool     `json:"enabled"`
	AllowedTools []string `json:"allowed_tools,omitempty"`
}

// AnthropicMCPServerV2 represents a simplified MCP server for mcp-client-2025-11-20 format.
// Tool configuration is now in AnthropicMCPToolsetTool in the tools[] array.
type AnthropicMCPServerV2 struct {
	Type               string  `json:"type"`                          // "url"
	URL                string  `json:"url"`                           // Server endpoint (must be https://)
	Name               string  `json:"name"`                          // Unique server name
	AuthorizationToken *string `json:"authorization_token,omitempty"` // OAuth token
}

// AnthropicMCPToolsetTool represents the new mcp_toolset tool type (mcp-client-2025-11-20).
// Lives in the tools[] array and references an MCP server by name.
type AnthropicMCPToolsetTool struct {
	Type          string                                `json:"type"`            // "mcp_toolset"
	MCPServerName string                                `json:"mcp_server_name"` // Must match a server in mcp_servers[]
	DefaultConfig *AnthropicMCPToolsetConfig            `json:"default_config,omitempty"`
	Configs       map[string]*AnthropicMCPToolsetConfig `json:"configs,omitempty"`
	CacheControl  *schemas.CacheControl                 `json:"cache_control,omitempty"`
}

// AnthropicMCPToolsetConfig configures individual MCP tools or provides defaults.
type AnthropicMCPToolsetConfig struct {
	Enabled      *bool `json:"enabled,omitempty"`
	DeferLoading *bool `json:"defer_loading,omitempty"`
}

// ==================== RESPONSE TYPES ====================

type AnthropicStopReason string

const (
	AnthropicStopReasonEndTurn                    AnthropicStopReason = "end_turn"
	AnthropicStopReasonMaxTokens                  AnthropicStopReason = "max_tokens"
	AnthropicStopReasonStopSequence               AnthropicStopReason = "stop_sequence"
	AnthropicStopReasonToolUse                    AnthropicStopReason = "tool_use"
	AnthropicStopReasonPauseTurn                  AnthropicStopReason = "pause_turn"
	AnthropicStopReasonRefusal                    AnthropicStopReason = "refusal"
	AnthropicStopReasonModelContextWindowExceeded AnthropicStopReason = "model_context_window_exceeded"
	AnthropicStopReasonCompaction                 AnthropicStopReason = "compaction"
)

// AnthropicMessageResponse represents an Anthropic messages API response
type AnthropicMessageResponse struct {
	ID           string                  `json:"id"`
	Type         string                  `json:"type"`
	Role         string                  `json:"role"`
	Content      []AnthropicContentBlock `json:"content"`
	Model        string                  `json:"model"`
	StopReason   AnthropicStopReason     `json:"stop_reason,omitempty"`
	StopSequence *string                 `json:"stop_sequence,omitempty"`
	Usage        *AnthropicUsage         `json:"usage,omitempty"`
}

// AnthropicTextResponse represents the response structure from Anthropic's text completion API
type AnthropicTextResponse struct {
	ID         string `json:"id"`         // Unique identifier for the completion
	Type       string `json:"type"`       // Type of completion
	Completion string `json:"completion"` // Generated completion text
	Model      string `json:"model"`      // Model used for the completion
	Usage      struct {
		InputTokens  int `json:"input_tokens"`  // Number of input tokens used
		OutputTokens int `json:"output_tokens"` // Number of output tokens generated
	} `json:"usage"` // Token usage statistics
}

// AnthropicUsage represents usage information in Anthropic format
type AnthropicUsage struct {
	Type *string `json:"type,omitempty"`
	// Unlike OpenAI models, Anthropic (claude) models separately track cache creation and cache read tokens, and its not included in the input_tokens field.
	InputTokens              int                          `json:"input_tokens"`
	CacheCreationInputTokens int                          `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int                          `json:"cache_read_input_tokens"`
	CacheCreation            AnthropicUsageCacheCreation  `json:"cache_creation"`
	OutputTokens             int                          `json:"output_tokens"`
	ServerToolUse            *AnthropicServerToolUseUsage `json:"server_tool_use,omitempty"` // Server tool use statistics (e.g., web search)
	ServiceTier              *string                      `json:"service_tier,omitempty"`    // "standard", "priority", or "batch"
	InferenceGeo             *string                      `json:"inference_geo,omitempty"`   // the geographic region for inference processing. If not specified, the workspace's default_inference_geo is used.
	Iterations               []AnthropicUsage             `json:"iterations,omitempty"`      // Iterations statistics
}

// AnthropicServerToolUseUsage represents server tool use statistics in usage
type AnthropicServerToolUseUsage struct {
	WebSearchRequests int `json:"web_search_requests"` // Number of web search requests made
}

type AnthropicUsageCacheCreation struct {
	Ephemeral5mInputTokens int `json:"ephemeral_5m_input_tokens"`
	Ephemeral1hInputTokens int `json:"ephemeral_1h_input_tokens"`
}

// ==================== STREAMING TYPES ====================

type AnthropicStreamEventType string

const (
	AnthropicStreamEventTypeMessageStart      AnthropicStreamEventType = "message_start"
	AnthropicStreamEventTypeMessageStop       AnthropicStreamEventType = "message_stop"
	AnthropicStreamEventTypeContentBlockStart AnthropicStreamEventType = "content_block_start"
	AnthropicStreamEventTypeContentBlockDelta AnthropicStreamEventType = "content_block_delta"
	AnthropicStreamEventTypeContentBlockStop  AnthropicStreamEventType = "content_block_stop"
	AnthropicStreamEventTypeMessageDelta      AnthropicStreamEventType = "message_delta"
	AnthropicStreamEventTypePing              AnthropicStreamEventType = "ping"
	AnthropicStreamEventTypeError             AnthropicStreamEventType = "error"
)

// AnthropicStreamEvent represents a single event in the Anthropic streaming response
type AnthropicStreamEvent struct {
	ID           *string                   `json:"id,omitempty"`
	Type         AnthropicStreamEventType  `json:"type"`
	Message      *AnthropicMessageResponse `json:"message,omitempty"`
	Index        *int                      `json:"index,omitempty"`
	ContentBlock *AnthropicContentBlock    `json:"content_block,omitempty"`
	Delta        *AnthropicStreamDelta     `json:"delta,omitempty"`
	Usage        *AnthropicUsage           `json:"usage,omitempty"`
	Error        *AnthropicStreamError     `json:"error,omitempty"`
}

type AnthropicStreamDeltaType string

const (
	AnthropicStreamDeltaTypeText       AnthropicStreamDeltaType = "text_delta"
	AnthropicStreamDeltaTypeInputJSON  AnthropicStreamDeltaType = "input_json_delta"
	AnthropicStreamDeltaTypeThinking   AnthropicStreamDeltaType = "thinking_delta"
	AnthropicStreamDeltaTypeSignature  AnthropicStreamDeltaType = "signature_delta"
	AnthropicStreamDeltaTypeCitations  AnthropicStreamDeltaType = "citations_delta"
	AnthropicStreamDeltaTypeCompaction AnthropicStreamDeltaType = "compaction_delta"
)

// AnthropicStreamDelta represents incremental updates to content blocks during streaming (legacy)
type AnthropicStreamDelta struct {
	Type         AnthropicStreamDeltaType `json:"type,omitempty"`
	Text         *string                  `json:"text,omitempty"`
	Content      *string                  `json:"content,omitempty"` // For compaction_delta
	PartialJSON  *string                  `json:"partial_json,omitempty"`
	Thinking     *string                  `json:"thinking,omitempty"`
	Signature    *string                  `json:"signature,omitempty"`
	Citation     *AnthropicTextCitation   `json:"citation,omitempty"`    // For citations_delta
	StopReason   *AnthropicStopReason     `json:"stop_reason,omitempty"` // only not present in "message_start" events
	StopSequence *string                  `json:"stop_sequence"`
}

// ==================== MODEL TYPES ====================

type AnthropicModel struct {
	ID             string          `json:"id"`
	Type           string          `json:"type"`
	DisplayName    string          `json:"display_name"`
	CreatedAt      time.Time       `json:"created_at"`
	MaxInputTokens *int            `json:"max_input_tokens,omitempty"`
	MaxTokens      *int            `json:"max_tokens,omitempty"`
	Capabilities   json.RawMessage `json:"capabilities,omitempty"`
}

type AnthropicListModelsResponse struct {
	Data    []AnthropicModel `json:"data"`
	FirstID *string          `json:"first_id,omitempty"`
	HasMore bool             `json:"has_more"`
	LastID  *string          `json:"last_id,omitempty"`
}

// ==================== ERROR TYPES ====================

// AnthropicMessageError represents an Anthropic messages API error response
type AnthropicMessageError struct {
	Type  string                      `json:"type"`  // always "error"
	Error AnthropicMessageErrorStruct `json:"error"` // Error details
}

// AnthropicMessageErrorStruct represents the error structure of an Anthropic messages API error response
type AnthropicMessageErrorStruct struct {
	Type    string `json:"type"`    // Error type
	Message string `json:"message"` // Error message
}

// AnthropicError represents the error response structure from Anthropic's API (legacy)
type AnthropicError struct {
	Type  string `json:"type"` // always "error"
	Error *struct {
		Type    string `json:"type"`    // Error type
		Message string `json:"message"` // Error message
	} `json:"error,omitempty"` // Error details
}

// AnthropicStreamError represents error events in the streaming response
type AnthropicStreamError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// ==================== FILE TYPES ====================

// AnthropicFileUploadRequest represents a request to upload a file.
type AnthropicFileUploadRequest struct {
	File     []byte `json:"-"`        // Raw file content (not serialized)
	Filename string `json:"filename"` // Original filename
	Purpose  string `json:"purpose"`  // Purpose of the file (e.g., "batch")
}

// AnthropicFileRetrieveRequest represents a request to retrieve a file.
type AnthropicFileRetrieveRequest struct {
	FileID string `json:"file_id"`
}

// AnthropicFileListRequest represents a request to list files.
type AnthropicFileListRequest struct {
	Limit int     `json:"limit"`
	After *string `json:"after"`
	Order *string `json:"order"`
}

// AnthropicFileDeleteRequest represents a request to delete a file.
type AnthropicFileDeleteRequest struct {
	FileID string `json:"file_id"`
}

// AnthropicFileContentRequest represents a request to get the content of a file.
type AnthropicFileContentRequest struct {
	FileID string `json:"file_id"`
}

// AnthropicFileResponse represents an Anthropic file response.
type AnthropicFileResponse struct {
	ID           string `json:"id"`
	Type         string `json:"type"`
	Filename     string `json:"filename"`
	MimeType     string `json:"mime_type"`
	SizeBytes    int64  `json:"size_bytes"`
	CreatedAt    string `json:"created_at"`
	Downloadable bool   `json:"downloadable"`
}

// AnthropicFileListResponse represents the response from listing files.
type AnthropicFileListResponse struct {
	Data    []AnthropicFileResponse `json:"data"`
	HasMore bool                    `json:"has_more"`
	FirstID *string                 `json:"first_id,omitempty"`
	LastID  *string                 `json:"last_id,omitempty"`
}

// AnthropicFileDeleteResponse represents the response from deleting a file.
type AnthropicFileDeleteResponse struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

// ToBifrostFileUploadResponse converts an Anthropic file response to Bifrost file upload response.
func (r *AnthropicFileResponse) ToBifrostFileUploadResponse(providerName schemas.ModelProvider, latency time.Duration, sendBackRawRequest bool, sendBackRawResponse bool, rawRequest interface{}, rawResponse interface{}) *schemas.BifrostFileUploadResponse {
	resp := &schemas.BifrostFileUploadResponse{
		ID:             r.ID,
		Object:         r.Type,
		Bytes:          r.SizeBytes,
		CreatedAt:      parseAnthropicFileTimestamp(r.CreatedAt),
		Filename:       r.Filename,
		Purpose:        schemas.FilePurposeBatch, // We hardcode as purpose is not supported by Anthropic
		Status:         schemas.FileStatusProcessed,
		StorageBackend: schemas.FileStorageAPI,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.FileUploadRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}

	if sendBackRawRequest {
		resp.ExtraFields.RawRequest = rawRequest
	}

	if sendBackRawResponse {
		resp.ExtraFields.RawResponse = rawResponse
	}

	return resp
}

// ToBifrostFileRetrieveResponse converts an Anthropic file response to Bifrost file retrieve response.
func (r *AnthropicFileResponse) ToBifrostFileRetrieveResponse(providerName schemas.ModelProvider, latency time.Duration, sendBackRawRequest bool, sendBackRawResponse bool, rawRequest interface{}, rawResponse interface{}) *schemas.BifrostFileRetrieveResponse {
	resp := &schemas.BifrostFileRetrieveResponse{
		ID:             r.ID,
		Object:         r.Type,
		Bytes:          r.SizeBytes,
		CreatedAt:      parseAnthropicFileTimestamp(r.CreatedAt),
		Filename:       r.Filename,
		Purpose:        schemas.FilePurposeBatch,
		Status:         schemas.FileStatusProcessed,
		StorageBackend: schemas.FileStorageAPI,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.FileRetrieveRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}

	if sendBackRawRequest {
		resp.ExtraFields.RawRequest = rawRequest
	}

	if sendBackRawResponse {
		resp.ExtraFields.RawResponse = rawResponse
	}

	return resp
}

// parseAnthropicFileTimestamp converts Anthropic ISO timestamp to Unix timestamp.
func parseAnthropicFileTimestamp(timestamp string) int64 {
	if timestamp == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		return 0
	}
	return t.Unix()
}

// AnthropicCountTokensResponse models the payload returned by Anthropic's count tokens endpoint.
type AnthropicCountTokensResponse struct {
	InputTokens int `json:"input_tokens"`
}

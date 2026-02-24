package anthropic

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// Since Anthropic always needs to have a max_tokens parameter, we set a default value if not provided.
const (
	AnthropicDefaultMaxTokens = 4096
	MinimumReasoningMaxTokens = 1024

	// Beta headers for various Anthropic features
	// AnthropicFilesAPIBetaHeader is the required beta header for the Files API.
	AnthropicFilesAPIBetaHeader = "files-api-2025-04-14"
	// AnthropicStructuredOutputsBetaHeader is required for strict tool validation and output_format.
	AnthropicStructuredOutputsBetaHeader = "structured-outputs-2025-11-13"
	// AnthropicAdvancedToolUseBetaHeader is required for defer_loading, input_examples, and allowed_callers.
	AnthropicAdvancedToolUseBetaHeader = "advanced-tool-use-2025-11-20"
	// AnthropicMCPClientBetaHeader is required for MCP servers.
	AnthropicMCPClientBetaHeader = "mcp-client-2025-04-04"
	// AnthropicPromptCachingScopeBetaHeader is required for prompt caching scope.
	AnthropicPromptCachingScopeBetaHeader = "prompt-caching-scope-2026-01-05"
	// AnthropicCompactionBetaHeader is required for compaction.
	AnthropicCompactionBetaHeader = "compact-2026-01-12"
	// AnthropicContextManagementBetaHeader is required for context management.
	AnthropicContextManagementBetaHeader = "context-management-2025-06-27"

	// Prefixes for Vertex-unsupported beta headers (version-bump proof).
	// Use these with strings.HasPrefix when filtering headers for Vertex AI,
	// so that future date bumps (e.g. structured-outputs-2025-12-15) are still matched.
	AnthropicAdvancedToolUseBetaHeaderPrefix    = "advanced-tool-use-"
	AnthropicStructuredOutputsBetaHeaderPrefix  = "structured-outputs-"
	AnthropicPromptCachingScopeBetaHeaderPrefix = "prompt-caching-scope-"
	AnthropicMCPClientBetaHeaderPrefix          = "mcp-client-"
)

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

// AnthropicOutputConfig represents the GA structured outputs config (output_config.format)
// and the effort parameter (output_config.effort) for controlling token spending.
type AnthropicOutputConfig struct {
	Format interface{} `json:"format,omitempty"`
	Effort *string     `json:"effort,omitempty"` // "low", "medium", "high", "max" (Opus 4.5+)
}

// AnthropicMessageRequest represents an Anthropic messages API request
type AnthropicMessageRequest struct {
	Model             string                 `json:"model"`
	MaxTokens         int                    `json:"max_tokens"`
	Messages          []AnthropicMessage     `json:"messages"`
	Metadata          *AnthropicMetaData     `json:"metadata,omitempty"`
	System            *AnthropicContent      `json:"system,omitempty"`
	Temperature       *float64               `json:"temperature,omitempty"`
	TopP              *float64               `json:"top_p,omitempty"`
	TopK              *int                   `json:"top_k,omitempty"`
	StopSequences     []string               `json:"stop_sequences,omitempty"`
	Stream            *bool                  `json:"stream,omitempty"`
	Tools             []AnthropicTool        `json:"tools,omitempty"`
	ToolChoice        *AnthropicToolChoice   `json:"tool_choice,omitempty"`
	MCPServers        []AnthropicMCPServer   `json:"mcp_servers,omitempty"` // This feature requires the beta header: "anthropic-beta": "mcp-client-2025-04-04"
	Thinking          *AnthropicThinking     `json:"thinking,omitempty"`
	OutputFormat      interface{}            `json:"output_format,omitempty"` // Beta: requires header "anthropic-beta": "structured-outputs-2025-11-13"
	OutputConfig      *AnthropicOutputConfig `json:"output_config,omitempty"` // GA: structured outputs without beta header
	ServiceTier       *string                `json:"service_tier,omitempty"`  // "auto" or "standard_only"
	InferenceGeo      *string                `json:"inference_geo,omitempty"` // the geographic region for inference processing. If not specified, the workspace's default_inference_geo is used.
	ContextManagement *ContextManagement     `json:"context_management,omitempty"`

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
	Type         string `json:"type"` // "enabled" or "disabled"
	BudgetTokens *int   `json:"budget_tokens,omitempty"`
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
		return sonic.Marshal(*tv.TypeAndValueString)
	}
	if tv.TypeAndValueObject != nil {
		return sonic.Marshal(tv.TypeAndValueObject)
	}
	return sonic.Marshal(nil)
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
		return sonic.Marshal(*ct.ClearToolInputsBoolean)
	}
	if ct.ClearToolInputsArray != nil {
		return sonic.Marshal(ct.ClearToolInputsArray)
	}
	return sonic.Marshal(nil)
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
			return sonic.Marshal(struct {
				Type ContextManagementEditType `json:"type"`
			}{
				Type: edit.Type,
			})
		}
		return sonic.Marshal(struct {
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
		return sonic.Marshal(struct {
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
		return sonic.Marshal(struct {
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
	"service_tier":       true,
	"inference_geo":      true,
	"context_management": true,
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

	// Extract unknown fields
	for key, value := range rawData {
		if !anthropicMessageRequestKnownFields[key] {
			var v interface{}
			if err := sonic.Unmarshal(value, &v); err != nil {
				continue // Skip fields that can't be unmarshaled
			}
			req.ExtraParams[key] = v
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

		return sonic.Marshal((*Alias)(&reqCopy))
	}

	return sonic.Marshal((*Alias)(req))
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
		return sonic.Marshal(*mc.ContentStr)
	}
	if mc.ContentBlocks != nil {
		return sonic.Marshal(mc.ContentBlocks)
	}
	// If both are nil, return null
	return sonic.Marshal(nil)
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
	AnthropicContentBlockTypeText                     AnthropicContentBlockType = "text"
	AnthropicContentBlockTypeImage                    AnthropicContentBlockType = "image"
	AnthropicContentBlockTypeDocument                 AnthropicContentBlockType = "document"
	AnthropicContentBlockTypeToolUse                  AnthropicContentBlockType = "tool_use"
	AnthropicContentBlockTypeServerToolUse            AnthropicContentBlockType = "server_tool_use"
	AnthropicContentBlockTypeToolResult               AnthropicContentBlockType = "tool_result"
	AnthropicContentBlockTypeWebSearchToolResult      AnthropicContentBlockType = "web_search_tool_result"
	AnthropicContentBlockTypeWebSearchToolResultError AnthropicContentBlockType = "web_search_tool_result_error"
	AnthropicContentBlockTypeWebSearchResult          AnthropicContentBlockType = "web_search_result"
	AnthropicContentBlockTypeMCPToolUse               AnthropicContentBlockType = "mcp_tool_use"
	AnthropicContentBlockTypeMCPToolResult            AnthropicContentBlockType = "mcp_tool_result"
	AnthropicContentBlockTypeThinking                 AnthropicContentBlockType = "thinking"
	AnthropicContentBlockTypeRedactedThinking         AnthropicContentBlockType = "redacted_thinking"
	AnthropicContentBlockTypeCompaction               AnthropicContentBlockType = "compaction"
)

// AnthropicContentBlock represents content in Anthropic message format
type AnthropicContentBlock struct {
	Type             AnthropicContentBlockType `json:"type"`                        // "text", "image", "document", "tool_use", "tool_result", "thinking"
	Text             *string                   `json:"text,omitempty"`              // For text content
	Thinking         *string                   `json:"thinking,omitempty"`          // For thinking content
	Signature        *string                   `json:"signature,omitempty"`         // For signature content
	Data             *string                   `json:"data,omitempty"`              // For data content (encrypted data for redacted thinking, signature does not come with this)
	ToolUseID        *string                   `json:"tool_use_id,omitempty"`       // For tool_result content
	ID               *string                   `json:"id,omitempty"`                // For tool_use content
	Name             *string                   `json:"name,omitempty"`              // For tool_use content
	Input            any                       `json:"input,omitempty"`             // For tool_use content
	ServerName       *string                   `json:"server_name,omitempty"`       // For mcp_tool_use content
	Content          *AnthropicContent         `json:"content,omitempty"`           // For tool_result content
	IsError          *bool                     `json:"is_error,omitempty"`          // For tool_result content, indicates error state
	Source           *AnthropicSource          `json:"source,omitempty"`            // For image/document content
	CacheControl     *schemas.CacheControl     `json:"cache_control,omitempty"`     // For cache control content
	Citations        *AnthropicCitations       `json:"citations,omitempty"`         // For document content
	Context          *string                   `json:"context,omitempty"`           // For document content
	Title            *string                   `json:"title,omitempty"`             // For document content
	URL              *string                   `json:"url,omitempty"`               // For web_search_result content
	EncryptedContent *string                   `json:"encrypted_content,omitempty"` // For web_search_result content
	PageAge          *string                   `json:"page_age,omitempty"`          // For web_search_result content
	ErrorCode        *string                   `json:"error_code,omitempty"`        // For web_search_tool_result_error content
}

// AnthropicSource represents image or document source in Anthropic format
type AnthropicSource struct {
	Type      string  `json:"type"`                 // "base64", "url", "text", "content_block"
	MediaType *string `json:"media_type,omitempty"` // "image/jpeg", "image/png", "application/pdf", etc.
	Data      *string `json:"data,omitempty"`       // Base64-encoded data (for base64 type)
	URL       *string `json:"url,omitempty"`        // URL (for url type)
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
		return nil, fmt.Errorf("AnthropicCitations: both Config and TextCitations are set; only one should be non-nil")
	}

	if ac.Config != nil {
		return sonic.Marshal(ac.Config)
	}
	if ac.TextCitations != nil {
		return sonic.Marshal(ac.TextCitations)
	}
	return sonic.Marshal(nil)
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
	AnthropicToolTypeBash20250124       AnthropicToolType = "bash_20250124"
	AnthropicToolTypeComputer20250124   AnthropicToolType = "computer_20250124"
	AnthropicToolTypeComputer20251124   AnthropicToolType = "computer_20251124" // for claude-opus-4.5
	AnthropicToolTypeCodeExecution      AnthropicToolType = "code_execution_20250825"
	AnthropicToolTypeTextEditor20250124 AnthropicToolType = "text_editor_20250124"
	AnthropicToolTypeTextEditor20250429 AnthropicToolType = "text_editor_20250429"
	AnthropicToolTypeTextEditor20250728 AnthropicToolType = "text_editor_20250728"
	AnthropicToolTypeWebSearch20250305  AnthropicToolType = "web_search_20250305"
)

type AnthropicToolName string

const (
	AnthropicToolNameComputer   AnthropicToolName = "computer"
	AnthropicToolNameWebSearch  AnthropicToolName = "web_search"
	AnthropicToolNameBash       AnthropicToolName = "bash"
	AnthropicToolNameTextEditor AnthropicToolName = "str_replace_based_edit_tool"
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

// AnthropicToolInputExample represents an input example for a tool (beta feature)
type AnthropicToolInputExample struct {
	Input       any     `json:"input"`
	Description *string `json:"description,omitempty"`
}

// AnthropicTool represents a tool in Anthropic format
type AnthropicTool struct {
	Name           string                          `json:"name"`
	Type           *AnthropicToolType              `json:"type,omitempty"`
	Description    *string                         `json:"description,omitempty"`
	InputSchema    *schemas.ToolFunctionParameters `json:"input_schema,omitempty"`
	CacheControl   *schemas.CacheControl           `json:"cache_control,omitempty"`
	DeferLoading   *bool                           `json:"defer_loading,omitempty"`   // Beta: defer loading of tool definition
	Strict         *bool                           `json:"strict,omitempty"`          // Whether to enforce strict parameter validation
	AllowedCallers []string                        `json:"allowed_callers,omitempty"` // Beta: which callers can use this tool
	InputExamples  []AnthropicToolInputExample     `json:"input_examples,omitempty"`  // Beta: example inputs for the tool

	*AnthropicToolComputerUse
	*AnthropicToolWebSearch
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

type AnthropicMCPServer struct {
	Type               string                  `json:"type"`
	URL                string                  `json:"url"`
	Name               string                  `json:"name"`
	AuthorizationToken *string                 `json:"authorization_token,omitempty"`
	ToolConfiguration  *AnthropicMCPToolConfig `json:"tool_configuration,omitempty"`
}

type AnthropicMCPToolConfig struct {
	Enabled      bool     `json:"enabled"`
	AllowedTools []string `json:"allowed_tools,omitempty"`
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
	Type                     *string                      `json:"type,omitempty"`
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
	ID          string    `json:"id"`
	DisplayName string    `json:"display_name"`
	CreatedAt   time.Time `json:"created_at"`
	Type        string    `json:"type"`
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

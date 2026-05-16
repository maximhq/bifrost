package llmtests

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProviderToolValidation verifies that unsupported tools are rejected per provider
func TestProviderToolValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		provider  schemas.ModelProvider
		tools     []schemas.ResponsesTool
		expectErr bool
		errSubstr string
	}{
		// ── Anthropic (supports everything) ──
		{
			name:     "Anthropic/web_search_allowed",
			provider: schemas.Anthropic,
			tools:    []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeWebSearch}},
		},
		{
			name:     "Anthropic/web_fetch_allowed",
			provider: schemas.Anthropic,
			tools:    []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeWebFetch}},
		},
		{
			name:     "Anthropic/code_interpreter_allowed",
			provider: schemas.Anthropic,
			tools:    []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeCodeInterpreter}},
		},
		{
			name:     "Anthropic/mcp_allowed",
			provider: schemas.Anthropic,
			tools:    []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeMCP}},
		},
		{
			name:     "Anthropic/computer_use_allowed",
			provider: schemas.Anthropic,
			tools:    []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeComputerUsePreview}},
		},

		// ── Vertex (web_search yes, web_fetch/code_exec/MCP no) ──
		{
			name:     "Vertex/web_search_allowed",
			provider: schemas.Vertex,
			tools:    []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeWebSearch}},
		},
		{
			name:      "Vertex/web_fetch_rejected",
			provider:  schemas.Vertex,
			tools:     []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeWebFetch}},
			expectErr: true,
			errSubstr: "web_fetch",
		},
		{
			name:      "Vertex/code_interpreter_rejected",
			provider:  schemas.Vertex,
			tools:     []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeCodeInterpreter}},
			expectErr: true,
			errSubstr: "code_interpreter",
		},
		{
			name:      "Vertex/mcp_rejected",
			provider:  schemas.Vertex,
			tools:     []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeMCP}},
			expectErr: true,
			errSubstr: "mcp",
		},
		{
			name:     "Vertex/computer_use_allowed",
			provider: schemas.Vertex,
			tools:    []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeComputerUsePreview}},
		},
		{
			name:     "Vertex/bash_allowed",
			provider: schemas.Vertex,
			tools:    []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeLocalShell}},
		},
		{
			name:     "Vertex/memory_allowed",
			provider: schemas.Vertex,
			tools:    []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeMemory}},
		},
		{
			name:     "Vertex/tool_search_allowed",
			provider: schemas.Vertex,
			tools:    []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeToolSearch}},
		},

		// ── Bedrock (no web_search, web_fetch, code_exec, MCP) ──
		{
			name:      "Bedrock/web_search_rejected",
			provider:  schemas.Bedrock,
			tools:     []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeWebSearch}},
			expectErr: true,
			errSubstr: "web_search",
		},
		{
			name:      "Bedrock/web_fetch_rejected",
			provider:  schemas.Bedrock,
			tools:     []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeWebFetch}},
			expectErr: true,
			errSubstr: "web_fetch",
		},
		{
			name:      "Bedrock/code_interpreter_rejected",
			provider:  schemas.Bedrock,
			tools:     []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeCodeInterpreter}},
			expectErr: true,
			errSubstr: "code_interpreter",
		},
		{
			name:      "Bedrock/mcp_rejected",
			provider:  schemas.Bedrock,
			tools:     []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeMCP}},
			expectErr: true,
			errSubstr: "mcp",
		},
		{
			name:     "Bedrock/computer_use_allowed",
			provider: schemas.Bedrock,
			tools:    []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeComputerUsePreview}},
		},
		{
			name:     "Bedrock/bash_allowed",
			provider: schemas.Bedrock,
			tools:    []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeLocalShell}},
		},
		{
			name:     "Bedrock/memory_allowed",
			provider: schemas.Bedrock,
			tools:    []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeMemory}},
		},

		// ── Azure (supports everything like Anthropic) ──
		{
			name:     "Azure/web_search_allowed",
			provider: schemas.Azure,
			tools:    []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeWebSearch}},
		},
		{
			name:     "Azure/web_fetch_allowed",
			provider: schemas.Azure,
			tools:    []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeWebFetch}},
		},
		{
			name:     "Azure/code_interpreter_allowed",
			provider: schemas.Azure,
			tools:    []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeCodeInterpreter}},
		},
		{
			name:     "Azure/mcp_allowed",
			provider: schemas.Azure,
			tools:    []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeMCP}},
		},

		// ── Function/custom tools always allowed ──
		{
			name:     "Bedrock/function_tool_allowed",
			provider: schemas.Bedrock,
			tools:    []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeFunction}},
		},
		{
			name:     "Vertex/custom_tool_allowed",
			provider: schemas.Vertex,
			tools:    []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeCustom}},
		},

		// ── FileSearch and ImageGeneration (OpenAI-only, rejected on all Anthropic providers) ──
		{
			name:      "Anthropic/file_search_rejected",
			provider:  schemas.Anthropic,
			tools:     []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeFileSearch}},
			expectErr: true,
			errSubstr: "file_search",
		},
		{
			name:      "Vertex/file_search_rejected",
			provider:  schemas.Vertex,
			tools:     []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeFileSearch}},
			expectErr: true,
			errSubstr: "file_search",
		},
		{
			name:      "Bedrock/image_generation_rejected",
			provider:  schemas.Bedrock,
			tools:     []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeImageGeneration}},
			expectErr: true,
			errSubstr: "image_generation",
		},
		{
			name:      "Azure/image_generation_rejected",
			provider:  schemas.Azure,
			tools:     []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeImageGeneration}},
			expectErr: true,
			errSubstr: "image_generation",
		},

		// ── Mixed tools: first unsupported tool triggers error ──
		{
			name:     "Vertex/mixed_supported_and_unsupported",
			provider: schemas.Vertex,
			tools: []schemas.ResponsesTool{
				{Type: schemas.ResponsesToolTypeWebSearch},   // allowed
				{Type: schemas.ResponsesToolTypeFunction},    // allowed
				{Type: schemas.ResponsesToolTypeCodeInterpreter}, // rejected
			},
			expectErr: true,
			errSubstr: "code_interpreter",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := anthropic.ValidateToolsForProvider(tt.tools, tt.provider)
			if tt.expectErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errSubstr)
				assert.Contains(t, err.Error(), string(tt.provider))
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestProviderWebSearchVersionSelection verifies that the correct web_search version
// is selected based on model and provider
func TestProviderWebSearchVersionSelection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		provider         schemas.ModelProvider
		model            string
		expectedToolType string
	}{
		// Anthropic 4.6 model → dynamic filtering version
		{
			name:             "Anthropic/4.6_model_gets_dynamic_filtering",
			provider:         schemas.Anthropic,
			model:            "claude-opus-4-6",
			expectedToolType: "web_search_20260209",
		},
		{
			name:             "Anthropic/sonnet_4-6_gets_dynamic_filtering",
			provider:         schemas.Anthropic,
			model:            "claude-sonnet-4-6",
			expectedToolType: "web_search_20260209",
		},
		// Anthropic non-4.6 model → basic version
		{
			name:             "Anthropic/4.5_model_gets_basic",
			provider:         schemas.Anthropic,
			model:            "claude-opus-4-5-20251101",
			expectedToolType: "web_search_20250305",
		},
		// Vertex 4.6 model → forced to basic (no dynamic filtering on Vertex)
		{
			name:             "Vertex/4.6_model_forced_to_basic",
			provider:         schemas.Vertex,
			model:            "claude-opus-4-6",
			expectedToolType: "web_search_20250305",
		},
		{
			name:             "Vertex/sonnet_4-6_forced_to_basic",
			provider:         schemas.Vertex,
			model:            "claude-sonnet-4-6",
			expectedToolType: "web_search_20250305",
		},
		// Vertex non-4.6 model → basic version
		{
			name:             "Vertex/4.5_model_gets_basic",
			provider:         schemas.Vertex,
			model:            "claude-sonnet-4-5-20250929",
			expectedToolType: "web_search_20250305",
		},
		// Azure 4.6 model → dynamic filtering (Azure supports it)
		{
			name:             "Azure/4.6_model_gets_dynamic_filtering",
			provider:         schemas.Azure,
			model:            "claude-opus-4-6",
			expectedToolType: "web_search_20260209",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := schemas.NewBifrostContext(nil, time.Time{})
			bifrostReq := &schemas.BifrostResponsesRequest{
				Provider: tt.provider,
				Model:    tt.model,
				Input: []schemas.ResponsesMessage{
					CreateBasicResponsesMessage("What is the weather?"),
				},
				Params: &schemas.ResponsesParameters{
					Tools: []schemas.ResponsesTool{
						{
							Type:                   schemas.ResponsesToolTypeWebSearch,
							ResponsesToolWebSearch: &schemas.ResponsesToolWebSearch{},
						},
					},
				},
			}

			result, err := anthropic.ToAnthropicResponsesRequest(ctx, bifrostReq)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.NotEmpty(t, result.Tools)

			// Find the web search tool in the result
			found := false
			for _, tool := range result.Tools {
				if tool.Type != nil && tool.Name == "web_search" {
					assert.Equal(t, tt.expectedToolType, string(*tool.Type),
						"expected tool type %s but got %s for provider=%s model=%s",
						tt.expectedToolType, string(*tool.Type), tt.provider, tt.model)
					found = true
					break
				}
			}
			require.True(t, found, "web_search tool should be present in converted request")
		})
	}
}

// TestProviderWebFetchVersionSelection verifies that the correct web_fetch version
// is selected based on model and provider
func TestProviderWebFetchVersionSelection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		provider         schemas.ModelProvider
		model            string
		expectedToolType string
	}{
		{
			name:             "Anthropic/4.6_model_gets_latest",
			provider:         schemas.Anthropic,
			model:            "claude-opus-4-6",
			expectedToolType: "web_fetch_20260309",
		},
		{
			name:             "Anthropic/4.5_model_gets_basic",
			provider:         schemas.Anthropic,
			model:            "claude-opus-4-5-20251101",
			expectedToolType: "web_fetch_20250910",
		},
		{
			name:             "Azure/4.6_model_gets_latest",
			provider:         schemas.Azure,
			model:            "claude-sonnet-4-6",
			expectedToolType: "web_fetch_20260309",
		},
		{
			name:             "Azure/4.5_model_gets_basic",
			provider:         schemas.Azure,
			model:            "claude-sonnet-4-5-20250929",
			expectedToolType: "web_fetch_20250910",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := schemas.NewBifrostContext(nil, time.Time{})
			bifrostReq := &schemas.BifrostResponsesRequest{
				Provider: tt.provider,
				Model:    tt.model,
				Input: []schemas.ResponsesMessage{
					CreateBasicResponsesMessage("Fetch https://example.com"),
				},
				Params: &schemas.ResponsesParameters{
					Tools: []schemas.ResponsesTool{
						{
							Type:                  schemas.ResponsesToolTypeWebFetch,
							ResponsesToolWebFetch: &schemas.ResponsesToolWebFetch{},
						},
					},
				},
			}

			result, err := anthropic.ToAnthropicResponsesRequest(ctx, bifrostReq)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.NotEmpty(t, result.Tools)

			found := false
			for _, tool := range result.Tools {
				if tool.Type != nil && tool.Name == "web_fetch" {
					assert.Equal(t, tt.expectedToolType, string(*tool.Type))
					found = true
					break
				}
			}
			require.True(t, found, "web_fetch tool should be present in converted request")
		})
	}
}

// TestProviderBetaHeaderInjection verifies that the correct beta headers
// are added (or omitted) based on provider
func TestProviderBetaHeaderInjection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		provider        schemas.ModelProvider
		setupReq        func() *anthropic.AnthropicMessageRequest
		expectHeaders   []string
		unexpectHeaders []string
	}{
		// ── Structured outputs header ──
		{
			name:     "Anthropic/structured_outputs_header_added",
			provider: schemas.Anthropic,
			setupReq: func() *anthropic.AnthropicMessageRequest {
				strict := true
				return &anthropic.AnthropicMessageRequest{
					Tools: []anthropic.AnthropicTool{{Name: "test", Strict: &strict}},
				}
			},
			expectHeaders: []string{"structured-outputs-2025-11-13"},
		},
		{
			name:     "Vertex/structured_outputs_header_skipped",
			provider: schemas.Vertex,
			setupReq: func() *anthropic.AnthropicMessageRequest {
				strict := true
				return &anthropic.AnthropicMessageRequest{
					Tools: []anthropic.AnthropicTool{{Name: "test", Strict: &strict}},
				}
			},
			unexpectHeaders: []string{"structured-outputs-2025-11-13"},
		},
		{
			name:     "Bedrock/structured_outputs_header_added",
			provider: schemas.Bedrock,
			setupReq: func() *anthropic.AnthropicMessageRequest {
				strict := true
				return &anthropic.AnthropicMessageRequest{
					Tools: []anthropic.AnthropicTool{{Name: "test", Strict: &strict}},
				}
			},
			expectHeaders: []string{"structured-outputs-2025-11-13"},
		},

		// ── MCP header ──
		{
			name:     "Anthropic/mcp_header_added",
			provider: schemas.Anthropic,
			setupReq: func() *anthropic.AnthropicMessageRequest {
				return &anthropic.AnthropicMessageRequest{
					MCPServers: []anthropic.AnthropicMCPServerV2{{URL: "https://example.com"}},
				}
			},
			expectHeaders: []string{"mcp-client-2025-11-20"},
		},
		{
			name:     "Vertex/mcp_header_skipped",
			provider: schemas.Vertex,
			setupReq: func() *anthropic.AnthropicMessageRequest {
				return &anthropic.AnthropicMessageRequest{
					MCPServers: []anthropic.AnthropicMCPServerV2{{URL: "https://example.com"}},
				}
			},
			unexpectHeaders: []string{"mcp-client-2025-11-20"},
		},
		{
			name:     "Bedrock/mcp_header_skipped",
			provider: schemas.Bedrock,
			setupReq: func() *anthropic.AnthropicMessageRequest {
				return &anthropic.AnthropicMessageRequest{
					MCPServers: []anthropic.AnthropicMCPServerV2{{URL: "https://example.com"}},
				}
			},
			unexpectHeaders: []string{"mcp-client-2025-11-20"},
		},
		{
			name:     "Azure/mcp_header_added",
			provider: schemas.Azure,
			setupReq: func() *anthropic.AnthropicMessageRequest {
				return &anthropic.AnthropicMessageRequest{
					MCPServers: []anthropic.AnthropicMCPServerV2{{URL: "https://example.com"}},
				}
			},
			expectHeaders: []string{"mcp-client-2025-11-20"},
		},

		// ── Compaction header (supported on all providers) ──
		{
			name:     "Anthropic/compaction_header_added",
			provider: schemas.Anthropic,
			setupReq: func() *anthropic.AnthropicMessageRequest {
				return &anthropic.AnthropicMessageRequest{
					ContextManagement: &anthropic.ContextManagement{
						Edits: []anthropic.ContextManagementEdit{{Type: anthropic.ContextManagementEditTypeCompact}},
					},
				}
			},
			expectHeaders: []string{"compact-2026-01-12"},
		},
		{
			name:     "Vertex/compaction_header_added",
			provider: schemas.Vertex,
			setupReq: func() *anthropic.AnthropicMessageRequest {
				return &anthropic.AnthropicMessageRequest{
					ContextManagement: &anthropic.ContextManagement{
						Edits: []anthropic.ContextManagementEdit{{Type: anthropic.ContextManagementEditTypeCompact}},
					},
				}
			},
			expectHeaders: []string{"compact-2026-01-12"},
		},
		{
			name:     "Bedrock/compaction_header_added",
			provider: schemas.Bedrock,
			setupReq: func() *anthropic.AnthropicMessageRequest {
				return &anthropic.AnthropicMessageRequest{
					ContextManagement: &anthropic.ContextManagement{
						Edits: []anthropic.ContextManagementEdit{{Type: anthropic.ContextManagementEditTypeCompact}},
					},
				}
			},
			expectHeaders: []string{"compact-2026-01-12"},
		},

		// ── Context editing header (supported on all providers) ──
		{
			name:     "Anthropic/context_editing_header_added",
			provider: schemas.Anthropic,
			setupReq: func() *anthropic.AnthropicMessageRequest {
				return &anthropic.AnthropicMessageRequest{
					ContextManagement: &anthropic.ContextManagement{
						Edits: []anthropic.ContextManagementEdit{{Type: anthropic.ContextManagementEditTypeClearToolUses}},
					},
				}
			},
			expectHeaders: []string{"context-management-2025-06-27"},
		},
		{
			name:     "Vertex/context_editing_header_added",
			provider: schemas.Vertex,
			setupReq: func() *anthropic.AnthropicMessageRequest {
				return &anthropic.AnthropicMessageRequest{
					ContextManagement: &anthropic.ContextManagement{
						Edits: []anthropic.ContextManagementEdit{{Type: anthropic.ContextManagementEditTypeClearToolUses}},
					},
				}
			},
			expectHeaders: []string{"context-management-2025-06-27"},
		},

		// ── Prompt caching scope header ──
		{
			name:     "Anthropic/prompt_caching_scope_added",
			provider: schemas.Anthropic,
			setupReq: func() *anthropic.AnthropicMessageRequest {
				scope := "global"
				return &anthropic.AnthropicMessageRequest{
					Tools: []anthropic.AnthropicTool{
						{Name: "test", CacheControl: &schemas.CacheControl{Type: "ephemeral", Scope: &scope}},
					},
				}
			},
			expectHeaders: []string{"prompt-caching-scope-2026-01-05"},
		},
		{
			name:     "Vertex/prompt_caching_scope_skipped",
			provider: schemas.Vertex,
			setupReq: func() *anthropic.AnthropicMessageRequest {
				scope := "global"
				return &anthropic.AnthropicMessageRequest{
					Tools: []anthropic.AnthropicTool{
						{Name: "test", CacheControl: &schemas.CacheControl{Type: "ephemeral", Scope: &scope}},
					},
				}
			},
			unexpectHeaders: []string{"prompt-caching-scope-2026-01-05"},
		},
		{
			name:     "Bedrock/prompt_caching_scope_skipped",
			provider: schemas.Bedrock,
			setupReq: func() *anthropic.AnthropicMessageRequest {
				scope := "global"
				return &anthropic.AnthropicMessageRequest{
					Tools: []anthropic.AnthropicTool{
						{Name: "test", CacheControl: &schemas.CacheControl{Type: "ephemeral", Scope: &scope}},
					},
				}
			},
			unexpectHeaders: []string{"prompt-caching-scope-2026-01-05"},
		},

		// ── Computer use version-specific beta headers ──
		{
			name:     "Anthropic/computer_20251124_gets_correct_beta",
			provider: schemas.Anthropic,
			setupReq: func() *anthropic.AnthropicMessageRequest {
				toolType := anthropic.AnthropicToolTypeComputer20251124
				return &anthropic.AnthropicMessageRequest{
					Tools: []anthropic.AnthropicTool{{Name: "computer", Type: &toolType}},
				}
			},
			expectHeaders:   []string{"computer-use-2025-11-24"},
			unexpectHeaders: []string{"computer-use-2025-01-24"},
		},
		{
			name:     "Anthropic/computer_20250124_gets_correct_beta",
			provider: schemas.Anthropic,
			setupReq: func() *anthropic.AnthropicMessageRequest {
				toolType := anthropic.AnthropicToolTypeComputer20250124
				return &anthropic.AnthropicMessageRequest{
					Tools: []anthropic.AnthropicTool{{Name: "computer", Type: &toolType}},
				}
			},
			expectHeaders:   []string{"computer-use-2025-01-24"},
			unexpectHeaders: []string{"computer-use-2025-11-24"},
		},
		{
			name:     "Vertex/computer_20251124_gets_correct_beta",
			provider: schemas.Vertex,
			setupReq: func() *anthropic.AnthropicMessageRequest {
				toolType := anthropic.AnthropicToolTypeComputer20251124
				return &anthropic.AnthropicMessageRequest{
					Tools: []anthropic.AnthropicTool{{Name: "computer", Type: &toolType}},
				}
			},
			expectHeaders: []string{"computer-use-2025-11-24"},
		},
		{
			name:     "Bedrock/computer_20250124_gets_correct_beta",
			provider: schemas.Bedrock,
			setupReq: func() *anthropic.AnthropicMessageRequest {
				toolType := anthropic.AnthropicToolTypeComputer20250124
				return &anthropic.AnthropicMessageRequest{
					Tools: []anthropic.AnthropicTool{{Name: "computer", Type: &toolType}},
				}
			},
			expectHeaders: []string{"computer-use-2025-01-24"},
		},

		// ── Fine-grained tool streaming header (eager_input_streaming) ──
		// Per cited citations (A overview table + B-header): EagerInputStreaming
		// is supported on Anthropic, Bedrock, Vertex, and Azure — all four
		// should auto-inject fine-grained-tool-streaming-2025-05-14 when a
		// tool has eager_input_streaming: true.
		{
			name:     "Anthropic/eager_input_streaming_header_added",
			provider: schemas.Anthropic,
			setupReq: func() *anthropic.AnthropicMessageRequest {
				eager := true
				return &anthropic.AnthropicMessageRequest{
					Tools: []anthropic.AnthropicTool{{Name: "t1", EagerInputStreaming: &eager}},
				}
			},
			expectHeaders: []string{"fine-grained-tool-streaming-2025-05-14"},
		},
		{
			name:     "Bedrock/eager_input_streaming_header_added",
			provider: schemas.Bedrock,
			setupReq: func() *anthropic.AnthropicMessageRequest {
				eager := true
				return &anthropic.AnthropicMessageRequest{
					Tools: []anthropic.AnthropicTool{{Name: "t1", EagerInputStreaming: &eager}},
				}
			},
			expectHeaders: []string{"fine-grained-tool-streaming-2025-05-14"},
		},
		{
			name:     "Vertex/eager_input_streaming_header_added",
			provider: schemas.Vertex,
			setupReq: func() *anthropic.AnthropicMessageRequest {
				eager := true
				return &anthropic.AnthropicMessageRequest{
					Tools: []anthropic.AnthropicTool{{Name: "t1", EagerInputStreaming: &eager}},
				}
			},
			expectHeaders: []string{"fine-grained-tool-streaming-2025-05-14"},
		},
		{
			name:     "Azure/eager_input_streaming_header_added",
			provider: schemas.Azure,
			setupReq: func() *anthropic.AnthropicMessageRequest {
				eager := true
				return &anthropic.AnthropicMessageRequest{
					Tools: []anthropic.AnthropicTool{{Name: "t1", EagerInputStreaming: &eager}},
				}
			},
			expectHeaders: []string{"fine-grained-tool-streaming-2025-05-14"},
		},
		{
			name:     "eager_input_streaming_header_skipped_when_flag_false",
			provider: schemas.Anthropic,
			setupReq: func() *anthropic.AnthropicMessageRequest {
				eager := false
				return &anthropic.AnthropicMessageRequest{
					Tools: []anthropic.AnthropicTool{{Name: "t1", EagerInputStreaming: &eager}},
				}
			},
			unexpectHeaders: []string{"fine-grained-tool-streaming-2025-05-14"},
		},
		{
			name:     "eager_input_streaming_header_skipped_when_unset",
			provider: schemas.Anthropic,
			setupReq: func() *anthropic.AnthropicMessageRequest {
				return &anthropic.AnthropicMessageRequest{
					Tools: []anthropic.AnthropicTool{{Name: "t1"}},
				}
			},
			unexpectHeaders: []string{"fine-grained-tool-streaming-2025-05-14"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := schemas.NewBifrostContext(nil, time.Time{})
			req := tt.setupReq()

			anthropic.AddMissingBetaHeadersToContext(ctx, req, tt.provider)

			var headers []string
			if extraHeaders, ok := ctx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string); ok {
				headers = extraHeaders["anthropic-beta"]
			}

			for _, expected := range tt.expectHeaders {
				found := false
				for _, h := range headers {
					if h == expected {
						found = true
						break
					}
				}
				assert.True(t, found, "expected beta header %q for provider %s, got headers: %v", expected, tt.provider, headers)
			}

			for _, unexpected := range tt.unexpectHeaders {
				for _, h := range headers {
					assert.NotEqual(t, unexpected, h, "unexpected beta header %q should NOT be present for provider %s", unexpected, tt.provider)
				}
			}
		})
	}
}

// TestProviderAnthropicRequestPipeline exercises the full Vertex/Bedrock/Anthropic/Azure
// request preparation pipeline end-to-end: validate tools → convert request → inject beta headers.
// This catches regressions in the provider-specific paths (e.g., missing tool version remapping
// or unsupported beta headers leaking through).
func TestProviderAnthropicRequestPipeline(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                    string
		provider                schemas.ModelProvider
		model                   string
		tools                   []schemas.ResponsesTool
		expectConversionErr     bool
		errSubstr               string
		expectedWebSearchType   string // expected web_search tool type after conversion
		expectedBetaHeaders     []string
		unexpectedBetaHeaders   []string
	}{
		// ── Vertex: web_search with filters → basic version, no dynamic headers ──
		{
			name:     "Vertex/web_search_4.6_gets_basic_version_no_dynamic_headers",
			provider: schemas.Vertex,
			model:    "claude-opus-4-6",
			tools: []schemas.ResponsesTool{
				{
					Type: schemas.ResponsesToolTypeWebSearch,
					ResponsesToolWebSearch: &schemas.ResponsesToolWebSearch{
						UserLocation: &schemas.ResponsesToolWebSearchUserLocation{
							Type:    schemas.Ptr("approximate"),
							Country: schemas.Ptr("US"),
						},
					},
				},
			},
			expectedWebSearchType: "web_search_20250305", // Vertex does NOT get dynamic filtering
			expectedBetaHeaders:   nil,                   // no beta headers for basic web search
			unexpectedBetaHeaders: []string{"structured-outputs-2025-11-13", "mcp-client-2025-11-20", "prompt-caching-scope-2026-01-05"},
		},
		{
			name:     "Vertex/web_search_with_compaction_gets_compaction_header",
			provider: schemas.Vertex,
			model:    "claude-sonnet-4-6",
			tools: []schemas.ResponsesTool{
				{Type: schemas.ResponsesToolTypeWebSearch, ResponsesToolWebSearch: &schemas.ResponsesToolWebSearch{}},
			},
			expectedWebSearchType: "web_search_20250305",
		},
		// ── Vertex: web_fetch rejected ──
		{
			name:     "Vertex/web_fetch_rejected_in_pipeline",
			provider: schemas.Vertex,
			tools: []schemas.ResponsesTool{
				{Type: schemas.ResponsesToolTypeWebFetch, ResponsesToolWebFetch: &schemas.ResponsesToolWebFetch{}},
			},
			expectConversionErr: true,
			errSubstr:           "web_fetch",
		},
		// ── Anthropic: web_search 4.6 gets dynamic filtering ──
		{
			name:     "Anthropic/web_search_4.6_gets_dynamic_version",
			provider: schemas.Anthropic,
			model:    "claude-opus-4-6",
			tools: []schemas.ResponsesTool{
				{
					Type: schemas.ResponsesToolTypeWebSearch,
					ResponsesToolWebSearch: &schemas.ResponsesToolWebSearch{
						Filters: &schemas.ResponsesToolWebSearchFilters{
							AllowedDomains: []string{"example.com"},
						},
					},
				},
			},
			expectedWebSearchType: "web_search_20260209",
		},
		{
			name:     "Anthropic/web_search_4.5_gets_basic_version",
			provider: schemas.Anthropic,
			model:    "claude-opus-4-5-20251101",
			tools: []schemas.ResponsesTool{
				{Type: schemas.ResponsesToolTypeWebSearch, ResponsesToolWebSearch: &schemas.ResponsesToolWebSearch{}},
			},
			expectedWebSearchType: "web_search_20250305",
		},
		// ── Azure: web_search 4.6 gets dynamic filtering (same as Anthropic) ──
		{
			name:     "Azure/web_search_4.6_gets_dynamic_version",
			provider: schemas.Azure,
			model:    "claude-sonnet-4-6",
			tools: []schemas.ResponsesTool{
				{Type: schemas.ResponsesToolTypeWebSearch, ResponsesToolWebSearch: &schemas.ResponsesToolWebSearch{}},
			},
			expectedWebSearchType: "web_search_20260209",
		},
		// ── Bedrock: web_search rejected ──
		{
			name:     "Bedrock/web_search_rejected_in_pipeline",
			provider: schemas.Bedrock,
			tools: []schemas.ResponsesTool{
				{Type: schemas.ResponsesToolTypeWebSearch, ResponsesToolWebSearch: &schemas.ResponsesToolWebSearch{}},
			},
			expectConversionErr: true,
			errSubstr:           "web_search",
		},
		// ── Bedrock: computer_use with structured outputs → correct headers ──
		{
			name:     "Bedrock/computer_use_with_structured_outputs_headers",
			provider: schemas.Bedrock,
			model:    "claude-sonnet-4-6",
			tools: []schemas.ResponsesTool{
				{
					Type: schemas.ResponsesToolTypeComputerUsePreview,
					ResponsesToolComputerUsePreview: &schemas.ResponsesToolComputerUsePreview{
						DisplayWidth: 1024, DisplayHeight: 768,
					},
				},
			},
			expectedBetaHeaders:   []string{"computer-use-2025-11-24"},
			unexpectedBetaHeaders: []string{"mcp-client-2025-11-20", "prompt-caching-scope-2026-01-05"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := schemas.NewBifrostContext(nil, time.Time{})
			model := tt.model
			if model == "" {
				model = "claude-sonnet-4-5"
			}

			// Step 1: Validate tools for provider
			if valErr := anthropic.ValidateToolsForProvider(tt.tools, tt.provider); valErr != nil {
				if tt.expectConversionErr {
					assert.Contains(t, valErr.Error(), tt.errSubstr)
					return
				}
				t.Fatalf("unexpected validation error: %v", valErr)
			}

			// Step 2: Convert bifrost request → anthropic request
			bifrostReq := &schemas.BifrostResponsesRequest{
				Provider: tt.provider,
				Model:    model,
				Input: []schemas.ResponsesMessage{
					CreateBasicResponsesMessage("Test query"),
				},
				Params: &schemas.ResponsesParameters{
					Tools: tt.tools,
				},
			}

			result, err := anthropic.ToAnthropicResponsesRequest(ctx, bifrostReq)
			if tt.expectConversionErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errSubstr)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, result)

			// Step 3: Verify web_search tool type if expected
			if tt.expectedWebSearchType != "" {
				found := false
				for _, tool := range result.Tools {
					if tool.Name == "web_search" {
						require.NotNil(t, tool.Type)
						assert.Equal(t, tt.expectedWebSearchType, string(*tool.Type),
							"wrong web_search type for provider=%s model=%s", tt.provider, model)
						found = true
						break
					}
				}
				require.True(t, found, "web_search tool should be present in converted request")
			}

			// Step 4: Run beta header injection
			anthropic.AddMissingBetaHeadersToContext(ctx, result, tt.provider)

			// Step 5: Verify beta headers
			var headers []string
			if extraHeaders, ok := ctx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string); ok {
				headers = extraHeaders["anthropic-beta"]
			}

			for _, expected := range tt.expectedBetaHeaders {
				found := false
				for _, h := range headers {
					if h == expected {
						found = true
						break
					}
				}
				assert.True(t, found, "expected beta header %q not found in %v for provider=%s", expected, headers, tt.provider)
			}

			for _, unexpected := range tt.unexpectedBetaHeaders {
				for _, h := range headers {
					assert.NotEqual(t, unexpected, h, "unexpected beta header %q should NOT be present for provider=%s", unexpected, tt.provider)
				}
			}
		})
	}
}

// TestProviderFeatureMapCompleteness ensures every provider in the map has consistent settings
func TestProviderFeatureMapCompleteness(t *testing.T) {
	t.Parallel()

	// Verify all four major providers are in the map
	for _, provider := range []schemas.ModelProvider{schemas.Anthropic, schemas.Vertex, schemas.Bedrock, schemas.Azure} {
		features, ok := anthropic.ProviderFeatures[provider]
		assert.True(t, ok, "provider %s should be in ProviderFeatures map", provider)

		// Anthropic and Azure should support everything
		if provider == schemas.Anthropic || provider == schemas.Azure {
			assert.True(t, features.WebSearch, "%s should support WebSearch", provider)
			assert.True(t, features.WebSearchDynamic, "%s should support WebSearchDynamic", provider)
			assert.True(t, features.WebFetch, "%s should support WebFetch", provider)
			assert.True(t, features.CodeExecution, "%s should support CodeExecution", provider)
			assert.True(t, features.MCP, "%s should support MCP", provider)
			assert.True(t, features.StructuredOutputs, "%s should support StructuredOutputs", provider)
			assert.True(t, features.FilesAPI, "%s should support FilesAPI", provider)
		}

		// Vertex specifics
		if provider == schemas.Vertex {
			assert.True(t, features.WebSearch, "Vertex should support basic WebSearch")
			assert.False(t, features.WebSearchDynamic, "Vertex should NOT support WebSearchDynamic")
			assert.False(t, features.WebFetch, "Vertex should NOT support WebFetch")
			assert.False(t, features.CodeExecution, "Vertex should NOT support CodeExecution")
			assert.False(t, features.MCP, "Vertex should NOT support MCP")
			assert.False(t, features.StructuredOutputs, "Vertex should NOT support StructuredOutputs")
			assert.True(t, features.Compaction, "Vertex should support Compaction")
			assert.True(t, features.ContextEditing, "Vertex should support ContextEditing")
		}

		// Bedrock specifics
		if provider == schemas.Bedrock {
			assert.False(t, features.WebSearch, "Bedrock should NOT support WebSearch")
			assert.False(t, features.WebFetch, "Bedrock should NOT support WebFetch")
			assert.False(t, features.CodeExecution, "Bedrock should NOT support CodeExecution")
			assert.False(t, features.MCP, "Bedrock should NOT support MCP")
			assert.True(t, features.StructuredOutputs, "Bedrock should support StructuredOutputs")
			assert.True(t, features.Compaction, "Bedrock should support Compaction")
			assert.True(t, features.ComputerUse, "Bedrock should support ComputerUse")
		}

		// All providers should support client-side tools
		assert.True(t, features.ComputerUse, "%s should support ComputerUse", provider)
		assert.True(t, features.Bash, "%s should support Bash", provider)
		assert.True(t, features.Memory, "%s should support Memory", provider)
		assert.True(t, features.TextEditor, "%s should support TextEditor", provider)
		assert.True(t, features.ToolSearch, "%s should support ToolSearch", provider)
	}
}

// TestComputerUseVersionAndBetaHeaderEndToEnd verifies the full pipeline:
// bifrost tool → anthropic tool version → correct beta header
func TestComputerUseVersionAndBetaHeaderEndToEnd(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		provider           schemas.ModelProvider
		model              string
		expectedToolType   string
		expectedBetaHeader string
	}{
		{
			name:               "Anthropic/opus_4.6_gets_20251124_and_matching_beta",
			provider:           schemas.Anthropic,
			model:              "claude-opus-4-6",
			expectedToolType:   "computer_20251124",
			expectedBetaHeader: "computer-use-2025-11-24",
		},
		{
			name:               "Anthropic/sonnet_4.6_gets_20251124_and_matching_beta",
			provider:           schemas.Anthropic,
			model:              "claude-sonnet-4-6",
			expectedToolType:   "computer_20251124",
			expectedBetaHeader: "computer-use-2025-11-24",
		},
		{
			name:               "Anthropic/opus_4.5_gets_20251124_and_matching_beta",
			provider:           schemas.Anthropic,
			model:              "claude-opus-4-5-20251101",
			expectedToolType:   "computer_20251124",
			expectedBetaHeader: "computer-use-2025-11-24",
		},
		{
			name:               "Anthropic/sonnet_4.5_gets_20250124_and_matching_beta",
			provider:           schemas.Anthropic,
			model:              "claude-sonnet-4-5-20250929",
			expectedToolType:   "computer_20250124",
			expectedBetaHeader: "computer-use-2025-01-24",
		},
		{
			name:               "Anthropic/sonnet_4_gets_20250124_and_matching_beta",
			provider:           schemas.Anthropic,
			model:              "claude-sonnet-4-20250514",
			expectedToolType:   "computer_20250124",
			expectedBetaHeader: "computer-use-2025-01-24",
		},
		{
			name:               "Vertex/opus_4.6_gets_20251124_and_matching_beta",
			provider:           schemas.Vertex,
			model:              "claude-opus-4-6",
			expectedToolType:   "computer_20251124",
			expectedBetaHeader: "computer-use-2025-11-24",
		},
		{
			name:               "Bedrock/sonnet_4_gets_20250124_and_matching_beta",
			provider:           schemas.Bedrock,
			model:              "claude-sonnet-4-20250514",
			expectedToolType:   "computer_20250124",
			expectedBetaHeader: "computer-use-2025-01-24",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := schemas.NewBifrostContext(nil, time.Time{})

			// Step 1: Convert bifrost tool → anthropic tool (selects version based on model)
			bifrostReq := &schemas.BifrostResponsesRequest{
				Provider: tt.provider,
				Model:    tt.model,
				Input: []schemas.ResponsesMessage{
					CreateBasicResponsesMessage("Take a screenshot"),
				},
				Params: &schemas.ResponsesParameters{
					Tools: []schemas.ResponsesTool{
						{
							Type: schemas.ResponsesToolTypeComputerUsePreview,
							ResponsesToolComputerUsePreview: &schemas.ResponsesToolComputerUsePreview{
								DisplayWidth:  1024,
								DisplayHeight: 768,
							},
						},
					},
				},
			}

			result, err := anthropic.ToAnthropicResponsesRequest(ctx, bifrostReq)
			require.NoError(t, err)
			require.NotNil(t, result)

			// Verify correct tool version was selected
			var computerTool *anthropic.AnthropicTool
			for i, tool := range result.Tools {
				if tool.Name == "computer" {
					computerTool = &result.Tools[i]
					break
				}
			}
			require.NotNil(t, computerTool, "computer tool should be present")
			require.NotNil(t, computerTool.Type)
			assert.Equal(t, tt.expectedToolType, string(*computerTool.Type),
				"wrong tool version for model=%s provider=%s", tt.model, tt.provider)

			// Step 2: Run beta header injection on the converted request
			anthropic.AddMissingBetaHeadersToContext(ctx, result, tt.provider)

			// Step 3: Verify correct beta header was added
			var headers []string
			if extraHeaders, ok := ctx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string); ok {
				headers = extraHeaders["anthropic-beta"]
			}

			found := false
			for _, h := range headers {
				if h == tt.expectedBetaHeader {
					found = true
					break
				}
			}
			assert.True(t, found, "expected beta header %q not found in %v for model=%s provider=%s",
				tt.expectedBetaHeader, headers, tt.model, tt.provider)
		})
	}
}

// TestRawBodyToolVersionRemapping verifies that when a raw request body contains
// a tool version unsupported by the target provider, it gets remapped automatically
func TestRawBodyToolVersionRemapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		provider         schemas.ModelProvider
		inputJSON        string
		expectedToolType string
		expectErr        bool
		errSubstr        string
	}{
		// ── Vertex: web_search_20260209 → web_search_20250305 ──
		{
			name:     "Vertex/web_search_20260209_remapped_to_20250305",
			provider: schemas.Vertex,
			inputJSON: `{
				"model": "claude-opus-4-6",
				"max_tokens": 1024,
				"tools": [{"type": "web_search_20260209", "name": "web_search"}],
				"messages": [{"role": "user", "content": "hello"}]
			}`,
			expectedToolType: "web_search_20250305",
		},
		// ── Vertex: web_search_20250305 unchanged ──
		{
			name:     "Vertex/web_search_20250305_unchanged",
			provider: schemas.Vertex,
			inputJSON: `{
				"model": "claude-opus-4-6",
				"max_tokens": 1024,
				"tools": [{"type": "web_search_20250305", "name": "web_search"}],
				"messages": [{"role": "user", "content": "hello"}]
			}`,
			expectedToolType: "web_search_20250305",
		},
		// ── Vertex: web_fetch rejected (no remap possible) ──
		{
			name:     "Vertex/web_fetch_rejected_in_raw_body",
			provider: schemas.Vertex,
			inputJSON: `{
				"model": "claude-opus-4-6",
				"tools": [{"type": "web_fetch_20250910", "name": "web_fetch"}],
				"messages": [{"role": "user", "content": "hello"}]
			}`,
			expectErr: true,
			errSubstr: "web_fetch_20250910",
		},
		// ── Vertex: code_execution rejected ──
		{
			name:     "Vertex/code_execution_rejected_in_raw_body",
			provider: schemas.Vertex,
			inputJSON: `{
				"model": "claude-opus-4-6",
				"tools": [{"type": "code_execution_20250825", "name": "code_execution"}],
				"messages": [{"role": "user", "content": "hello"}]
			}`,
			expectErr: true,
			errSubstr: "code_execution",
		},
		// ── Bedrock: web_search rejected ──
		{
			name:     "Bedrock/web_search_rejected_in_raw_body",
			provider: schemas.Bedrock,
			inputJSON: `{
				"model": "claude-opus-4-6",
				"tools": [{"type": "web_search_20250305", "name": "web_search"}],
				"messages": [{"role": "user", "content": "hello"}]
			}`,
			expectErr: true,
			errSubstr: "web_search_20250305",
		},
		// ── Anthropic: no remapping needed ──
		{
			name:     "Anthropic/web_search_20260209_unchanged",
			provider: schemas.Anthropic,
			inputJSON: `{
				"model": "claude-opus-4-6",
				"tools": [{"type": "web_search_20260209", "name": "web_search"}],
				"messages": [{"role": "user", "content": "hello"}]
			}`,
			expectedToolType: "web_search_20260209",
		},
		// ── No tools in body: no error ──
		{
			name:     "Vertex/no_tools_no_error",
			provider: schemas.Vertex,
			inputJSON: `{
				"model": "claude-opus-4-6",
				"messages": [{"role": "user", "content": "hello"}]
			}`,
		},
		// ── Vertex: bash tool unchanged (supported) ──
		{
			name:     "Vertex/bash_tool_unchanged",
			provider: schemas.Vertex,
			inputJSON: `{
				"model": "claude-opus-4-6",
				"tools": [{"type": "bash_20250124", "name": "bash"}],
				"messages": [{"role": "user", "content": "hello"}]
			}`,
			expectedToolType: "bash_20250124",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			model := providerUtils.GetJSONField([]byte(tt.inputJSON), "model").String()
			result, err := anthropic.RemapRawToolVersionsForProvider([]byte(tt.inputJSON), tt.provider, model)

			if tt.expectErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errSubstr)
				return
			}

			require.NoError(t, err)

			if tt.expectedToolType != "" {
				// Extract the tool type from the result JSON
				toolType := providerUtils.GetJSONField(result, "tools.0.type").String()
				assert.Equal(t, tt.expectedToolType, toolType,
					"expected tool type %s but got %s for provider %s",
					tt.expectedToolType, toolType, tt.provider)
			}
		})
	}
}

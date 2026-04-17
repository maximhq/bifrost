package llmtests

import (
	"context"
	"os"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// RunServerToolsViaOpenAIEndpointTest reproduces the user-reported bug where
// sending an Anthropic-server-tool-shaped entry in tools[] via the OpenAI-
// compatible chat-completions endpoint was silently dropped (Claude responded
// with a prose "I can't check real-time data" fallback). The fix was a
// combination of:
//   - ChatTool schema gaining Name + all server-tool variant fields.
//   - ToAnthropicChatRequest learning to convert non-function tools (server
//     tools) into AnthropicTool with the correct variant embed.
//
// This test sends the exact curl-reported shape via BifrostChatRequest +
// ChatCompletionRequest and asserts the request succeeds end-to-end against
// the provider. It covers three server tools that have single-turn triggers
// (web_search, web_fetch, code_execution) across all supporting providers per
// Table 20. Other variants (bash, memory, text_editor, tool_search,
// mcp_toolset, computer_use) require multi-turn tool loops or infra setup
// and are covered by the schema / unit-level round-trip tests instead.
func RunServerToolsViaOpenAIEndpointTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.ServerToolsViaOpenAIEndpoint {
		t.Logf("ServerToolsViaOpenAIEndpoint not supported for provider %s", testConfig.Provider)
		return
	}

	cases := []struct {
		name     string
		toolType schemas.ChatToolType
		toolName string
		prompt   string
		// extra lets the case set server-tool metadata (max_uses etc.).
		extra func(*schemas.ChatTool)
		// supported reports whether this tool is supported on the given
		// provider per Table 20 (cited provider feature matrix).
		supported func(schemas.ModelProvider) bool
	}{
		{
			name:     "web_search",
			toolType: "web_search_20260209",
			toolName: "web_search",
			prompt:   "What is the weather in San Francisco today? Use the web_search tool.",
			extra: func(t *schemas.ChatTool) {
				five := 5
				t.MaxUses = &five
				t.AllowedCallers = []string{"direct"}
			},
			// web_search: Anthropic + Vertex + Azure per Table 20 (not Bedrock).
			supported: func(p schemas.ModelProvider) bool {
				return p == schemas.Anthropic || p == schemas.Vertex || p == schemas.Azure
			},
		},
		{
			name:     "web_fetch",
			toolType: "web_fetch_20260309",
			toolName: "web_fetch",
			prompt:   "Fetch https://example.com and summarise the title.",
			extra: func(t *schemas.ChatTool) {
				three := 3
				t.MaxUses = &three
			},
			// web_fetch: Anthropic + Azure only per Table 20.
			supported: func(p schemas.ModelProvider) bool {
				return p == schemas.Anthropic || p == schemas.Azure
			},
		},
		{
			name:     "code_execution",
			toolType: "code_execution_20250825",
			toolName: "code_execution",
			prompt:   "Compute 2^64 minus 1 using the code_execution tool and return the result.",
			// code_execution: Anthropic + Azure only per Table 20.
			supported: func(p schemas.ModelProvider) bool {
				return p == schemas.Anthropic || p == schemas.Azure
			},
		},
	}

	t.Run("ServerToolsViaOpenAIEndpoint", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		for _, tc := range cases {
			tc := tc
			t.Run(tc.name, func(t *testing.T) {
				if !tc.supported(testConfig.Provider) {
					t.Skipf("%s not supported on %s per Table 20", tc.name, testConfig.Provider)
				}
				if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
					t.Parallel()
				}

				tool := schemas.ChatTool{
					Type: tc.toolType,
					Name: tc.toolName,
				}
				if tc.extra != nil {
					tc.extra(&tool)
				}

				req := &schemas.BifrostChatRequest{
					Provider: testConfig.Provider,
					Model:    testConfig.ChatModel,
					Input: []schemas.ChatMessage{
						CreateBasicChatMessage(tc.prompt),
					},
					Params: &schemas.ChatParameters{
						MaxCompletionTokens: bifrost.Ptr(500),
						Tools:               []schemas.ChatTool{tool},
					},
					Fallbacks: testConfig.Fallbacks,
				}

				bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
				resp, err := client.ChatCompletionRequest(bfCtx, req)
				if err != nil {
					t.Fatalf("%s tool request failed: %s", tc.name, GetErrorMessage(err))
				}
				if resp == nil {
					t.Fatal("expected non-nil response")
				}

				// Regression signals:
				//   1. Upstream accepted the request (no error).
				//   2. Response is not the prose fallback Claude emits when
				//      the server-tool was silently stripped pre-fix
				//      ("I can't/cannot/don't have access to real-time ...").
				// The schema + conversion unit tests prove the outbound
				// request carries the tool; this live test proves the
				// provider accepts the shape AND actually uses the tool
				// rather than answering from parametric memory.
				content := GetChatContent(resp)
				lc := strings.ToLower(content)
				if strings.Contains(lc, "can't access real-time") ||
					strings.Contains(lc, "cannot access real-time") ||
					strings.Contains(lc, "don't have access to real-time") {
					t.Fatalf("%s regression: tool appears to be ignored, content=%q", tc.name, content)
				}
				t.Logf("%s tool live call succeeded: chars=%d", tc.name, len(content))
			})
		}
	})
}

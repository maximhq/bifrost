package testutil

import (
	"context"
	"os"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// This test verifies that the web search tool is properly invoked and returns results
func RunWebSearchToolTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.WebSearchTool {
		t.Logf("Web search tool not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("WebSearchTool", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		// Create a simple query that should trigger web search
		responsesMessages := []schemas.ResponsesMessage{
			CreateBasicResponsesMessage("What is the current weather in New York City?"),
		}

		// Create web search tool for Responses API
		webSearchTool := &schemas.ResponsesTool{
			Type: schemas.ResponsesToolTypeWebSearch,
			ResponsesToolWebSearch: &schemas.ResponsesToolWebSearch{
				UserLocation: &schemas.ResponsesToolWebSearchUserLocation{
					Type:    bifrost.Ptr("approximate"),
					Country: bifrost.Ptr("US"),
					City:    bifrost.Ptr("New York"),
				},
			},
		}

		// Use specialized web search retry configuration
		retryConfig := WebSearchRetryConfig()
		retryContext := TestRetryContext{
			ScenarioName: "WebSearchTool",
			ExpectedBehavior: map[string]interface{}{
				"expected_tool_type": "web_search",
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
				"model":    testConfig.ChatModel,
			},
		}

		// Create expectations for web search
		expectations := WebSearchExpectations()

		// Create operation for Responses API
		responsesOperation := func() (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			responsesReq := &schemas.BifrostResponsesRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.ChatModel,
				Input:    responsesMessages,
				Params: &schemas.ResponsesParameters{
					Tools: []schemas.ResponsesTool{*webSearchTool},
				},
				Fallbacks: testConfig.Fallbacks,
			}

			return client.ResponsesRequest(bfCtx, responsesReq)
		}

		// Execute test with retry - Responses API only for web search
		response, err := WithResponsesTestRetry(t, retryConfig, retryContext, expectations, "WebSearchTool", responsesOperation)

		// Validate success
		if err != nil {
			t.Fatalf("❌ WebSearchTool test failed: %s", GetErrorMessage(err))
		}

		require.NotNil(t, response, "Response should not be nil")

		// Validate web search was invoked
		webSearchCallFound := false
		hasTextResponse := false

		if response.Output != nil {
			for _, output := range response.Output {
				// Check for web_search_call
				if output.Type != nil && *output.Type == schemas.ResponsesMessageTypeWebSearchCall {
					webSearchCallFound = true
					t.Logf("✅ Found web_search_call in output")

					// Validate the search action
					if output.ResponsesToolMessage != nil && output.ResponsesToolMessage.Action != nil {
						action := output.ResponsesToolMessage.Action
						if action.ResponsesWebSearchToolCallAction != nil {
							query := action.ResponsesWebSearchToolCallAction.Query
							if query != nil {
								t.Logf("✅ Web search query: %s", *query)
							}

							// Validate sources if present
							if len(action.ResponsesWebSearchToolCallAction.Sources) > 0 {
								t.Logf("✅ Found %d search result sources", len(action.ResponsesWebSearchToolCallAction.Sources))

								// Log first few sources
								for i, source := range action.ResponsesWebSearchToolCallAction.Sources {
									if i >= 3 {
										break
									}
									t.Logf("  Source %d: %s", i+1, source.URL)
								}
							}
						}
					}
				}

				// Check for text response (message with actual answer)
				if output.Type != nil && *output.Type == schemas.ResponsesMessageTypeMessage {
					if output.Content != nil && len(output.Content.ContentBlocks) > 0 {
						for _, block := range output.Content.ContentBlocks {
							if block.Text != nil && *block.Text != "" {
								hasTextResponse = true

								// Check for citations
								if block.ResponsesOutputMessageContentText != nil && len(block.ResponsesOutputMessageContentText.Annotations) > 0 {
									t.Logf("✅ Found %d citations in response", len(block.ResponsesOutputMessageContentText.Annotations))
								} else {
									t.Logf("✅ Found text response")
								}
							}
						}
					}
				}
			}
		}

		require.True(t, webSearchCallFound, "Web search call should be present in response output")
		require.True(t, hasTextResponse, "Response should contain text answer based on web search results")

		t.Logf("🎉 WebSearchTool test passed!")
	})
}

// WebSearchRetryConfig returns specialized retry configuration for web search tests
func WebSearchRetryConfig() ResponsesRetryConfig {
	return ResponsesRetryConfig{
		MaxAttempts: 5,
		BaseDelay:   2 * time.Second,
		MaxDelay:    10 * time.Second,
		Conditions: []ResponsesRetryCondition{
			&ResponsesEmptyCondition{},
			&ResponsesGenericResponseCondition{},
		},
		OnRetry: func(attempt int, reason string, t *testing.T) {
			t.Logf("🔄 Retrying web search test (attempt %d): %s", attempt, reason)
		},
	}
}

// WebSearchExpectations returns validation expectations for web search responses
func WebSearchExpectations() ResponseExpectations {
	return ResponseExpectations{
		ShouldHaveContent: true,
	}
}

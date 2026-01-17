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

// RunWebSearchToolStreamTest executes streaming web search test
func RunWebSearchToolStreamTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.WebSearchTool {
		t.Logf("Web search tool not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("WebSearchToolStream", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		responsesMessages := []schemas.ResponsesMessage{
			CreateBasicResponsesMessage("What are the latest advancements in renewable energy? Use web search."),
		}

		// Create web search tool with user location
		webSearchTool := &schemas.ResponsesTool{
			Type: schemas.ResponsesToolTypeWebSearch,
			ResponsesToolWebSearch: &schemas.ResponsesToolWebSearch{
				UserLocation: &schemas.ResponsesToolWebSearchUserLocation{
					Type:     bifrost.Ptr("approximate"),
					Country:  bifrost.Ptr("US"),
					City:     bifrost.Ptr("San Francisco"),
					Region:   bifrost.Ptr("California"),
					Timezone: bifrost.Ptr("America/Los_Angeles"),
				},
			},
		}

		request := &schemas.BifrostResponsesRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ChatModel,
			Input:    responsesMessages,
			Params: &schemas.ResponsesParameters{
				Tools:           []schemas.ResponsesTool{*webSearchTool},
				MaxOutputTokens: bifrost.Ptr(1500),
			},
			Fallbacks: testConfig.Fallbacks,
		}

		retryConfig := StreamingRetryConfig()
		retryContext := TestRetryContext{
			ScenarioName: "WebSearchToolStream",
			ExpectedBehavior: map[string]interface{}{
				"should_stream_content":        true,
				"should_have_web_search_call":  true,
				"should_have_streaming_events": true,
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
				"model":    testConfig.ChatModel,
			},
		}

		validationResult := WithResponsesStreamValidationRetry(t, retryConfig, retryContext,
			func() (chan *schemas.BifrostStream, *schemas.BifrostError) {
				bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
				return client.ResponsesStreamRequest(bfCtx, request)
			},
			func(responseChannel chan *schemas.BifrostStream) ResponsesStreamValidationResult {
				var hasWebSearchCall, hasMessageContent bool
				var webSearchQuery string
				var searchSources []schemas.ResponsesWebSearchToolCallActionSearchSource
				var chunkCount int
				var errors []string

				streamCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
				defer cancel()

				for {
					select {
					case stream, ok := <-responseChannel:
						if !ok {
							goto ValidationComplete
						}
						if stream == nil {
							continue
						}

						chunkCount++

						// Check streaming events for web_search_call and message content
						if stream.BifrostResponsesStreamResponse != nil {
							streamType := stream.BifrostResponsesStreamResponse.Type

							// Check for output_item.added with web_search_call
							if streamType == schemas.ResponsesStreamResponseTypeOutputItemAdded {
								if stream.BifrostResponsesStreamResponse.Item != nil {
									if stream.BifrostResponsesStreamResponse.Item.Type != nil &&
										*stream.BifrostResponsesStreamResponse.Item.Type == schemas.ResponsesMessageTypeWebSearchCall {
										hasWebSearchCall = true
										t.Logf("✅ Found web_search_call in streaming event: %s", streamType)

										// Extract query and sources if available
										if stream.BifrostResponsesStreamResponse.Item.ResponsesToolMessage != nil &&
											stream.BifrostResponsesStreamResponse.Item.ResponsesToolMessage.Action != nil {
											action := stream.BifrostResponsesStreamResponse.Item.ResponsesToolMessage.Action
											if action.ResponsesWebSearchToolCallAction != nil {
												if action.ResponsesWebSearchToolCallAction.Query != nil {
													webSearchQuery = *action.ResponsesWebSearchToolCallAction.Query
													t.Logf("✅ Web search query: %s", webSearchQuery)
												}
												searchSources = append(searchSources, action.ResponsesWebSearchToolCallAction.Sources...)
											}
										}
									}
								}
							}

							// Also check other web_search_call streaming events
							if streamType == schemas.ResponsesStreamResponseTypeWebSearchCallInProgress ||
								streamType == schemas.ResponsesStreamResponseTypeWebSearchCallSearching ||
								streamType == schemas.ResponsesStreamResponseTypeWebSearchCallCompleted {
								hasWebSearchCall = true
								t.Logf("✅ Found web_search_call streaming event: %s", streamType)
							}

							// Check for message text content in streaming deltas
							if streamType == schemas.ResponsesStreamResponseTypeOutputTextDelta {
								if stream.BifrostResponsesStreamResponse.Delta != nil && *stream.BifrostResponsesStreamResponse.Delta != "" {
									hasMessageContent = true
									t.Logf("✅ Found message text delta: %s", *stream.BifrostResponsesStreamResponse.Delta)
								}
							}
						}

					case <-streamCtx.Done():
						t.Logf("⚠️ Stream timeout after %d chunks", chunkCount)
						goto ValidationComplete
					}
				}

			ValidationComplete:
				if len(searchSources) > 0 {
					t.Logf("✅ Found %d search sources", len(searchSources))
				}

				// Validate streaming requirements
				if !hasWebSearchCall {
					errors = append(errors, "No web_search_call found in stream")
				}

				if !hasMessageContent {
					errors = append(errors, "No message content found in stream")
				}

				if chunkCount < 3 {
					errors = append(errors, "Too few streaming chunks received")
				}

				return ResponsesStreamValidationResult{
					Passed:       len(errors) == 0,
					Errors:       errors,
					ReceivedData: hasWebSearchCall || hasMessageContent,
				}
			},
		)

		require.True(t, validationResult.Passed, "Stream validation failed: %v", validationResult.Errors)
		t.Logf("🎉 WebSearchToolStream test passed!")
	})
}

// RunWebSearchToolWithDomainsTest tests web search with domain filtering
func RunWebSearchToolWithDomainsTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.WebSearchTool {
		t.Logf("Web search tool not supported for provider %s", testConfig.Provider)
		return
	}

	if testConfig.Provider == "gemini" {
		// skip because gemini google search tool does not support domain filtering
		t.Logf("Skipping WebSearchToolWithDomains test for provider %s because gemini google search tool does not support domain filtering", testConfig.Provider)
		return
	}

	t.Run("WebSearchToolWithDomains", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		responsesMessages := []schemas.ResponsesMessage{
			CreateBasicResponsesMessage("What is machine learning? Use web search tool."),
		}

		// Create web search tool with domain filters
		webSearchTool := &schemas.ResponsesTool{
			Type: schemas.ResponsesToolTypeWebSearch,
			ResponsesToolWebSearch: &schemas.ResponsesToolWebSearch{
				Filters: &schemas.ResponsesToolWebSearchFilters{
					AllowedDomains: []string{"wikipedia.org", "en.wikipedia.org"},
				},
			},
		}

		retryConfig := WebSearchRetryConfig()
		retryContext := TestRetryContext{
			ScenarioName: "WebSearchToolWithDomains",
			ExpectedBehavior: map[string]interface{}{
				"expected_tool_type": "web_search",
				"domain_filters":     true,
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
				"model":    testConfig.ChatModel,
			},
		}

		expectations := WebSearchExpectations()

		responsesOperation := func() (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			responsesReq := &schemas.BifrostResponsesRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.ChatModel,
				Input:    responsesMessages,
				Params: &schemas.ResponsesParameters{
					Tools:           []schemas.ResponsesTool{*webSearchTool},
					MaxOutputTokens: bifrost.Ptr(1200),
				},
				Fallbacks: testConfig.Fallbacks,
			}

			return client.ResponsesRequest(bfCtx, responsesReq)
		}

		response, err := WithResponsesTestRetry(t, retryConfig, retryContext, expectations, "WebSearchToolWithDomains", responsesOperation)

		if err != nil {
			t.Fatalf("❌ WebSearchToolWithDomains test failed: %s", GetErrorMessage(err))
		}

		require.NotNil(t, response, "Response should not be nil")

		// Validate web search was invoked and collect sources
		webSearchCallFound := false
		var sources []schemas.ResponsesWebSearchToolCallActionSearchSource

		if response.Output != nil {
			for _, output := range response.Output {
				if output.Type != nil && *output.Type == schemas.ResponsesMessageTypeWebSearchCall {
					webSearchCallFound = true
					if output.ResponsesToolMessage != nil && output.ResponsesToolMessage.Action != nil {
						action := output.ResponsesToolMessage.Action
						if action.ResponsesWebSearchToolCallAction != nil {
							sources = action.ResponsesWebSearchToolCallAction.Sources
							t.Logf("✅ Found %d search sources", len(sources))
						}
					}
				}
			}
		}

		require.True(t, webSearchCallFound, "Web search call should be present")

		// Validate sources respect domain filters
		if len(sources) > 0 {
			ValidateWebSearchSources(t, sources, []string{"wikipedia.org", "en.wikipedia.org"})
		}

		t.Logf("🎉 WebSearchToolWithDomains test passed!")
	})
}

// RunWebSearchToolContextSizesTest tests different search context sizes
func RunWebSearchToolContextSizesTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.WebSearchTool {
		t.Logf("Web search tool not supported for provider %s", testConfig.Provider)
		return
	}

	if testConfig.Provider == "gemini" {
		// skip because gemini google search tool does not support context size
		t.Logf("Skipping WebSearchToolContextSizes test for provider %s because gemini google search tool does not support context size", testConfig.Provider)
		return
	}

	t.Run("WebSearchToolContextSizes", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		contextSizes := []string{"low", "medium", "high"}

		for _, size := range contextSizes {
			size := size // Capture loop variable
			t.Run("ContextSize_"+size, func(t *testing.T) {
				responsesMessages := []schemas.ResponsesMessage{
					CreateBasicResponsesMessage("What is quantum computing? Use web search."),
				}

				webSearchTool := &schemas.ResponsesTool{
					Type: schemas.ResponsesToolTypeWebSearch,
					ResponsesToolWebSearch: &schemas.ResponsesToolWebSearch{
						SearchContextSize: &size,
					},
				}

				retryConfig := WebSearchRetryConfig()
				retryContext := TestRetryContext{
					ScenarioName: "WebSearchToolContextSize_" + size,
					ExpectedBehavior: map[string]interface{}{
						"expected_tool_type": "web_search",
						"context_size":       size,
					},
					TestMetadata: map[string]interface{}{
						"provider":     testConfig.Provider,
						"model":        testConfig.ChatModel,
						"context_size": size,
					},
				}

				expectations := WebSearchExpectations()

				responsesOperation := func() (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
					bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
					responsesReq := &schemas.BifrostResponsesRequest{
						Provider: testConfig.Provider,
						Model:    testConfig.ChatModel,
						Input:    responsesMessages,
						Params: &schemas.ResponsesParameters{
							Tools:           []schemas.ResponsesTool{*webSearchTool},
							MaxOutputTokens: bifrost.Ptr(1500),
						},
						Fallbacks: testConfig.Fallbacks,
					}

					return client.ResponsesRequest(bfCtx, responsesReq)
				}

				response, err := WithResponsesTestRetry(t, retryConfig, retryContext, expectations, "WebSearchToolContextSize", responsesOperation)

				if err != nil {
					t.Fatalf("❌ WebSearchToolContextSize (%s) test failed: %s", size, GetErrorMessage(err))
				}

				require.NotNil(t, response, "Response should not be nil")

				webSearchCallFound := false
				hasTextResponse := false

				if response.Output != nil {
					for _, output := range response.Output {
						if output.Type != nil && *output.Type == schemas.ResponsesMessageTypeWebSearchCall {
							webSearchCallFound = true
							t.Logf("✅ Web search call with context size: %s", size)
						}

						if output.Type != nil && *output.Type == schemas.ResponsesMessageTypeMessage {
							if output.Content != nil && len(output.Content.ContentBlocks) > 0 {
								for _, block := range output.Content.ContentBlocks {
									if block.Text != nil && *block.Text != "" {
										hasTextResponse = true
										t.Logf("✅ Response length for %s context: %d chars", size, len(*block.Text))
									}
								}
							}
						}
					}
				}

				require.True(t, webSearchCallFound, "Web search call should be present")
				require.True(t, hasTextResponse, "Response should contain text")

				t.Logf("🎉 WebSearchToolContextSize (%s) test passed!", size)
			})
		}
	})
}

// RunWebSearchToolMultiTurnTest tests multi-turn conversation with web search
func RunWebSearchToolMultiTurnTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.WebSearchTool {
		t.Logf("Web search tool not supported for provider %s", testConfig.Provider)
		return
	}

	t.Run("WebSearchToolMultiTurn", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		webSearchTool := &schemas.ResponsesTool{
			Type:                   schemas.ResponsesToolTypeWebSearch,
			ResponsesToolWebSearch: &schemas.ResponsesToolWebSearch{},
		}

		// First turn
		t.Log("🔄 Starting first turn...")
		firstMessages := []schemas.ResponsesMessage{
			CreateBasicResponsesMessage("What is renewable energy? Use web search tool."),
		}

		retryConfig := WebSearchRetryConfig()
		retryContext1 := TestRetryContext{
			ScenarioName: "WebSearchToolMultiTurn_Turn1",
			ExpectedBehavior: map[string]interface{}{
				"expected_tool_type": "web_search",
				"turn":               1,
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
				"model":    testConfig.ChatModel,
			},
		}

		expectations := WebSearchExpectations()

		firstOperation := func() (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			responsesReq := &schemas.BifrostResponsesRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.ChatModel,
				Input:    firstMessages,
				Params: &schemas.ResponsesParameters{
					Tools:           []schemas.ResponsesTool{*webSearchTool},
					MaxOutputTokens: bifrost.Ptr(1500),
				},
				Fallbacks: testConfig.Fallbacks,
			}

			return client.ResponsesRequest(bfCtx, responsesReq)
		}

		firstResponse, err := WithResponsesTestRetry(t, retryConfig, retryContext1, expectations, "WebSearchToolMultiTurn_Turn1", firstOperation)

		if err != nil {
			t.Fatalf("❌ First turn failed: %s", GetErrorMessage(err))
		}

		require.NotNil(t, firstResponse, "First response should not be nil")

		// Validate first turn has web search
		firstTurnHasWebSearch := false
		if firstResponse.Output != nil {
			for _, output := range firstResponse.Output {
				if output.Type != nil && *output.Type == schemas.ResponsesMessageTypeWebSearchCall {
					firstTurnHasWebSearch = true
					t.Logf("✅ First turn: Web search executed")
					break
				}
			}
		}

		require.True(t, firstTurnHasWebSearch, "First turn should have web search call")

		// Second turn - add first response to conversation history
		t.Log("🔄 Starting second turn...")
		secondMessages := append(firstMessages, firstResponse.Output...)
		secondMessages = append(secondMessages, CreateBasicResponsesMessage("What are the main types of renewable energy?"))

		retryContext2 := TestRetryContext{
			ScenarioName: "WebSearchToolMultiTurn_Turn2",
			ExpectedBehavior: map[string]interface{}{
				"expected_tool_type": "web_search",
				"turn":               2,
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
				"model":    testConfig.ChatModel,
			},
		}

		secondOperation := func() (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			responsesReq := &schemas.BifrostResponsesRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.ChatModel,
				Input:    secondMessages,
				Params: &schemas.ResponsesParameters{
					Tools:           []schemas.ResponsesTool{*webSearchTool},
					MaxOutputTokens: bifrost.Ptr(1500),
				},
				Fallbacks: testConfig.Fallbacks,
			}

			return client.ResponsesRequest(bfCtx, responsesReq)
		}

		secondResponse, err := WithResponsesTestRetry(t, retryConfig, retryContext2, expectations, "WebSearchToolMultiTurn_Turn2", secondOperation)

		if err != nil {
			t.Fatalf("❌ Second turn failed: %s", GetErrorMessage(err))
		}

		require.NotNil(t, secondResponse, "Second response should not be nil")

		// Validate second turn
		secondTurnHasMessage := false
		if secondResponse.Output != nil {
			for _, output := range secondResponse.Output {
				if output.Type != nil && *output.Type == schemas.ResponsesMessageTypeMessage {
					secondTurnHasMessage = true
					t.Logf("✅ Second turn: Got response message")
					break
				}
			}
		}

		require.True(t, secondTurnHasMessage, "Second turn should have message response")

		t.Logf("🎉 WebSearchToolMultiTurn test passed!")
	})
}

// RunWebSearchToolMaxUsesTest tests Anthropic-specific max uses parameter
func RunWebSearchToolMaxUsesTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.WebSearchTool {
		t.Logf("Web search tool not supported for provider %s", testConfig.Provider)
		return
	}

	// This is Anthropic-specific functionality
	if testConfig.Provider != "anthropic" {
		t.Logf("Max uses parameter is Anthropic-specific, skipping for provider %s", testConfig.Provider)
		return
	}

	t.Run("WebSearchToolMaxUses", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		responsesMessages := []schemas.ResponsesMessage{
			CreateBasicResponsesMessage("Compare the populations of Tokyo and New York City. Use web search."),
		}

		// Create web search tool with max uses limit
		maxUses := 3
		webSearchTool := &schemas.ResponsesTool{
			Type: schemas.ResponsesToolTypeWebSearch,
			ResponsesToolWebSearch: &schemas.ResponsesToolWebSearch{
				MaxUses: &maxUses,
			},
		}

		retryConfig := WebSearchRetryConfig()
		retryContext := TestRetryContext{
			ScenarioName: "WebSearchToolMaxUses",
			ExpectedBehavior: map[string]interface{}{
				"expected_tool_type": "web_search",
				"max_uses":           maxUses,
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
				"model":    testConfig.ChatModel,
			},
		}

		expectations := WebSearchExpectations()

		responsesOperation := func() (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			responsesReq := &schemas.BifrostResponsesRequest{
				Provider: testConfig.Provider,
				Model:    testConfig.ChatModel,
				Input:    responsesMessages,
				Params: &schemas.ResponsesParameters{
					Tools:           []schemas.ResponsesTool{*webSearchTool},
					MaxOutputTokens: bifrost.Ptr(2000),
				},
				Fallbacks: testConfig.Fallbacks,
			}

			return client.ResponsesRequest(bfCtx, responsesReq)
		}

		response, err := WithResponsesTestRetry(t, retryConfig, retryContext, expectations, "WebSearchToolMaxUses", responsesOperation)

		if err != nil {
			t.Fatalf("❌ WebSearchToolMaxUses test failed: %s", GetErrorMessage(err))
		}

		require.NotNil(t, response, "Response should not be nil")

		// Count web search calls
		webSearchCallCount := 0
		if response.Output != nil {
			for _, output := range response.Output {
				if output.Type != nil && *output.Type == schemas.ResponsesMessageTypeWebSearchCall {
					webSearchCallCount++
				}
			}
		}

		t.Logf("✅ Web search called %d times (max: %d)", webSearchCallCount, maxUses)
		require.True(t, webSearchCallCount <= maxUses, "Web search should not exceed max uses limit")
		require.True(t, webSearchCallCount > 0, "Web search should be called at least once")

		t.Logf("🎉 WebSearchToolMaxUses test passed!")
	})
}

// ValidateWebSearchSources validates web search sources structure and domain filtering
func ValidateWebSearchSources(t *testing.T, sources []schemas.ResponsesWebSearchToolCallActionSearchSource, allowedDomains []string) {
	require.NotEmpty(t, sources, "Sources should not be empty")

	for i, source := range sources {
		// Validate basic structure
		require.NotEmpty(t, source.URL, "Source %d should have a URL", i+1)

		t.Logf("  Source %d: %s", i+1, source.URL)

		// If domain filters specified, validate sources match patterns
		if len(allowedDomains) > 0 {
			matchesFilter := false
			for _, domain := range allowedDomains {
				// Simple pattern matching for wildcard domains
				// "wikipedia.org/*" matches any wikipedia.org URL
				// "*.edu" matches any .edu domain
				if matchesDomainPattern(source.URL, domain) {
					matchesFilter = true
					break
				}
			}

			if !matchesFilter {
				t.Logf("  ⚠️ Source %d (%s) doesn't match allowed domain filters", i+1, source.URL)
			}
		}
	}

	t.Logf("✅ Validated %d search sources", len(sources))
}

// matchesDomainPattern checks if a URL matches a domain pattern
func matchesDomainPattern(url, pattern string) bool {
	// Simple pattern matching implementation
	// "*.edu" matches URLs containing ".edu"
	// "wikipedia.org/*" matches URLs containing "wikipedia.org"

	if len(pattern) > 0 && pattern[0] == '*' {
		// Pattern like "*.edu"
		suffix := pattern[1:]
		return containsSubstring(url, suffix)
	}

	if len(pattern) > 0 && pattern[len(pattern)-1] == '*' {
		// Pattern like "wikipedia.org/*"
		prefix := pattern[:len(pattern)-2]
		return containsSubstring(url, prefix)
	}

	// Exact match
	return containsSubstring(url, pattern)
}

// containsSubstring checks if s contains substr (case-insensitive)
func containsSubstring(s, substr string) bool {
	s = toLower(s)
	substr = toLower(substr)
	return len(s) >= len(substr) && indexOfSubstring(s, substr) >= 0
}

// toLower converts string to lowercase
func toLower(s string) string {
	result := make([]rune, len(s))
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			result[i] = r + 32
		} else {
			result[i] = r
		}
	}
	return string(result)
}

// indexOfSubstring finds index of substr in s, or -1 if not found
func indexOfSubstring(s, substr string) int {
	if len(substr) == 0 {
		return 0
	}
	if len(substr) > len(s) {
		return -1
	}

	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

package testutil

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// StreamingToolCallAccumulator accumulates tool call fragments from streaming responses
type StreamingToolCallAccumulator struct {
	// For Chat Completions: map of tool call index -> accumulated tool call
	ChatToolCalls map[int]*schemas.ChatAssistantMessageToolCall
	// For Responses API: map of call ID or item ID -> accumulated tool call info
	ResponsesToolCalls map[string]*ResponsesToolCallInfo
	// Map itemID to the key used in ResponsesToolCalls for quick lookup
	ItemIDToKey map[string]string
}

// ResponsesToolCallInfo accumulates tool call information from Responses API streaming
type ResponsesToolCallInfo struct {
	ID        string
	Name      string
	Arguments string
}

// NewStreamingToolCallAccumulator creates a new accumulator
func NewStreamingToolCallAccumulator() *StreamingToolCallAccumulator {
	return &StreamingToolCallAccumulator{
		ChatToolCalls:      make(map[int]*schemas.ChatAssistantMessageToolCall),
		ResponsesToolCalls: make(map[string]*ResponsesToolCallInfo),
		ItemIDToKey:        make(map[string]string),
	}
}

// AccumulateChatToolCall accumulates a tool call from a Chat Completions streaming chunk
func (acc *StreamingToolCallAccumulator) AccumulateChatToolCall(choiceIndex int, toolCall schemas.ChatAssistantMessageToolCall) {
	// Prefer ID as key if available, otherwise use index
	key := -1
	var found bool
	if toolCall.ID != nil && *toolCall.ID != "" {
		// Try to find existing tool call by ID first
		for k, existing := range acc.ChatToolCalls {
			if existing.ID != nil && *existing.ID == *toolCall.ID {
				key = k
				found = true
				break
			}
		}
		// If not found by ID, use index
		if !found {
			key = int(toolCall.Index)
		}
	} else {
		// Use the tool call index as the key
		key = int(toolCall.Index)
	}

	existing, exists := acc.ChatToolCalls[key]
	if !exists {
		// First chunk for this tool call - initialize
		acc.ChatToolCalls[key] = &schemas.ChatAssistantMessageToolCall{
			Index:    toolCall.Index,
			Type:     toolCall.Type,
			ID:       toolCall.ID,
			Function: schemas.ChatAssistantMessageToolCallFunction{},
		}
		existing = acc.ChatToolCalls[key]
	}

	// Accumulate name if present
	if toolCall.Function.Name != nil && *toolCall.Function.Name != "" {
		existing.Function.Name = toolCall.Function.Name
	}

	// Accumulate ID if present (may come in later chunks)
	if toolCall.ID != nil && *toolCall.ID != "" {
		existing.ID = toolCall.ID
	}

	// Accumulate arguments (they come incrementally)
	if toolCall.Function.Arguments != "" {
		existing.Function.Arguments += toolCall.Function.Arguments
	}
}

// AccumulateResponsesToolCall accumulates a tool call from a Responses API streaming chunk
func (acc *StreamingToolCallAccumulator) AccumulateResponsesToolCall(callID *string, name *string, arguments *string, itemID *string) {
	// First, try to find existing tool call by itemID (most reliable for matching)
	key := "default"
	if itemID != nil && *itemID != "" {
		itemIDStr := *itemID
		// Check if we have a mapping for this itemID
		if mappedKey, exists := acc.ItemIDToKey[itemIDStr]; exists {
			key = mappedKey
		} else {
			// Try to find by itemID in keys (with or without prefix)
			for k := range acc.ResponsesToolCalls {
				if k == itemIDStr || k == "item:"+itemIDStr {
					key = k
					acc.ItemIDToKey[itemIDStr] = key
					break
				}
			}
			// If not found, use itemID as key
			if key == "default" {
				key = "item:" + itemIDStr
				acc.ItemIDToKey[itemIDStr] = key
			}
		}
	} else if callID != nil && *callID != "" {
		// Use callID as key if no itemID
		key = *callID
	} else if name != nil && *name != "" {
		// Try to find existing tool call by name if we don't have callID or itemID yet
		for k, existing := range acc.ResponsesToolCalls {
			if existing.Name == *name && existing.ID == "" {
				key = k
				break
			}
		}
		// If not found, use name as temporary key
		if key == "default" {
			key = "name:" + *name
		}
	}

	existing, exists := acc.ResponsesToolCalls[key]
	if !exists {
		existing = &ResponsesToolCallInfo{}
		acc.ResponsesToolCalls[key] = existing
	}

	// Track the final key that will be used for this entry
	finalKey := key

	// Update fields if present
	if callID != nil && *callID != "" {
		existing.ID = *callID
		// If we were using a temporary key, migrate to callID-based key
		if key != *callID {
			acc.ResponsesToolCalls[*callID] = existing
			finalKey = *callID
			// Update itemID mapping if we have one
			if itemID != nil && *itemID != "" {
				acc.ItemIDToKey[*itemID] = *callID
			}
			if key != "default" && key != *callID {
				delete(acc.ResponsesToolCalls, key)
			}
		}
	}
	if name != nil && *name != "" {
		existing.Name = *name
	}
	if arguments != nil && *arguments != "" {
		// If we're getting complete arguments (from done event), replace instead of append
		// Check if this looks like complete JSON (starts with { and ends with })
		argsStr := *arguments
		if len(argsStr) > 0 && argsStr[0] == '{' && argsStr[len(argsStr)-1] == '}' && existing.Arguments != "" {
			// This looks like complete arguments, but only replace if we already have partial args
			// Otherwise, this might be the first chunk which happens to be complete
			existing.Arguments = argsStr
		} else {
			// Incremental chunk, append
			existing.Arguments += argsStr
		}
	}

	// Update itemID mapping if we have itemID but haven't mapped it yet
	// Use finalKey which is the actual key where the entry is stored
	if itemID != nil && *itemID != "" {
		if _, exists := acc.ItemIDToKey[*itemID]; !exists {
			acc.ItemIDToKey[*itemID] = finalKey
		}
	}
}

// GetFinalChatToolCalls returns the final accumulated tool calls for Chat Completions
func (acc *StreamingToolCallAccumulator) GetFinalChatToolCalls() []ToolCallInfo {
	var result []ToolCallInfo
	for _, toolCall := range acc.ChatToolCalls {
		info := ToolCallInfo{}
		if toolCall.ID != nil {
			info.ID = *toolCall.ID
		}
		if toolCall.Function.Name != nil {
			info.Name = *toolCall.Function.Name
		}
		info.Arguments = toolCall.Function.Arguments
		result = append(result, info)
	}
	return result
}

// GetFinalResponsesToolCalls returns the final accumulated tool calls for Responses API
func (acc *StreamingToolCallAccumulator) GetFinalResponsesToolCalls() []ToolCallInfo {
	var result []ToolCallInfo
	for _, toolCall := range acc.ResponsesToolCalls {
		result = append(result, ToolCallInfo{
			ID:        toolCall.ID,
			Name:      toolCall.Name,
			Arguments: toolCall.Arguments,
		})
	}
	return result
}

// RunToolCallsStreamingTest executes the tool calls streaming test scenario
func RunToolCallsStreamingTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.ToolCallsStreaming {
		t.Logf("Tool calls streaming not supported for provider %s", testConfig.Provider)
		return
	}

	// Test Chat Completions streaming with tool calls
	t.Run("ToolCallsStreamingChatCompletions", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		chatMessages := []schemas.ChatMessage{
			CreateBasicChatMessage("What's the weather like in New York? answer in celsius"),
		}

		chatTool := GetSampleChatTool(SampleToolTypeWeather)

		request := &schemas.BifrostChatRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ChatModel,
			Input:    chatMessages,
			Params: &schemas.ChatParameters{
				MaxCompletionTokens: bifrost.Ptr(150),
				Tools:               []schemas.ChatTool{*chatTool},
			},
			Fallbacks: testConfig.Fallbacks,
		}

		// Use retry framework for stream requests with tools
		retryConfig := StreamingRetryConfig()
		retryContext := TestRetryContext{
			ScenarioName: "ToolCallsStreamingChatCompletions",
			ExpectedBehavior: map[string]interface{}{
				"should_stream_content":  true,
				"should_have_tool_calls": true,
				"tool_name":              "get_weather",
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
				"model":    testConfig.ChatModel,
				"tools":    true,
			},
		}

		responseChannel, err := WithStreamRetry(t, retryConfig, retryContext, func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
			bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			return client.ChatCompletionStreamRequest(bfCtx, request)
		})

		RequireNoError(t, err, "Chat completion stream with tools failed")
		if responseChannel == nil {
			t.Fatal("Response channel should not be nil")
		}

		accumulator := NewStreamingToolCallAccumulator()
		var responseCount int

		t.Logf("🔧 Testing Chat Completions streaming with tool calls...")

		for response := range responseChannel {
			if response == nil || response.BifrostChatResponse == nil {
				t.Fatal("Streaming response should not be nil")
			}
			responseCount++

			// Process tool calls from this chunk
			if response.BifrostChatResponse.Choices != nil {
				for _, choice := range response.BifrostChatResponse.Choices {
					if choice.ChatStreamResponseChoice != nil && choice.ChatStreamResponseChoice.Delta != nil {
						delta := choice.ChatStreamResponseChoice.Delta

						// Check for tool calls in delta
						if len(delta.ToolCalls) > 0 {
							for _, toolCall := range delta.ToolCalls {
								// Debug logging: what fields are present in this chunk
								chunkType := "ChatCompletions.Delta.ToolCalls"
								hasID := toolCall.ID != nil && *toolCall.ID != ""
								hasName := toolCall.Function.Name != nil && *toolCall.Function.Name != ""
								hasArgs := toolCall.Function.Arguments != ""

								t.Logf("📊 [%s] Chunk fields: ID=%v (field: toolCall.ID), Name=%v (field: toolCall.Function.Name), Args=%v (field: toolCall.Function.Arguments, len=%d)",
									chunkType, hasID, hasName, hasArgs, len(toolCall.Function.Arguments))

								if hasID {
									t.Logf("  ✅ ID found in %s: %s", chunkType, *toolCall.ID)
								}
								if hasName {
									t.Logf("  ✅ Name found in %s: %s", chunkType, *toolCall.Function.Name)
								}
								if hasArgs {
									t.Logf("  ✅ Arguments found in %s: %s", chunkType, toolCall.Function.Arguments)
								}

								accumulator.AccumulateChatToolCall(choice.Index, toolCall)
								t.Logf("🔧 Accumulated tool call chunk: index=%d, id=%v, name=%v, args_len=%d",
									choice.Index,
									toolCall.ID,
									toolCall.Function.Name,
									len(toolCall.Function.Arguments))
							}
						}
					}
				}
			}

			if responseCount > 500 {
				break
			}
		}

		if responseCount == 0 {
			t.Fatal("Should receive at least one streaming response")
		}

		// Validate final tool calls
		finalToolCalls := accumulator.GetFinalChatToolCalls()

		if len(finalToolCalls) == 0 {
			t.Fatal("❌ No tool calls found in streaming response")
		}

		for i, toolCall := range finalToolCalls {
			if toolCall.ID == "" || toolCall.Name == "" || toolCall.Arguments == "" {
				t.Fatalf("❌ Tool call %d missing required fields: ID=%v, Name=%v, Arguments=%v",
					i, toolCall.ID != "", toolCall.Name != "", toolCall.Arguments != "")
			}
		}

		validateStreamingToolCalls(t, finalToolCalls, "Chat Completions")
		t.Logf("✅ Chat Completions streaming with tools test completed successfully")
	})

	// Test Responses API streaming with tool calls
	t.Run("ToolCallsStreamingResponses", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		responsesMessages := []schemas.ResponsesMessage{
			CreateBasicResponsesMessage("What's the weather like in New York? answer in celsius"),
		}

		responsesTool := GetSampleResponsesTool(SampleToolTypeWeather)

		request := &schemas.BifrostResponsesRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.ChatModel,
			Input:    responsesMessages,
			Params: &schemas.ResponsesParameters{
				Tools: []schemas.ResponsesTool{*responsesTool},
			},
			Fallbacks: testConfig.Fallbacks,
		}

		// Use retry framework for stream requests with tools
		retryConfig := StreamingRetryConfig()
		retryContext := TestRetryContext{
			ScenarioName: "ToolCallsStreamingResponses",
			ExpectedBehavior: map[string]interface{}{
				"should_stream_content":  true,
				"should_have_tool_calls": true,
				"tool_name":              "get_weather",
			},
			TestMetadata: map[string]interface{}{
				"provider": testConfig.Provider,
				"model":    testConfig.ChatModel,
				"tools":    true,
			},
		}

		// Use validation retry wrapper that validates tool calls and retries on validation failures
		validationResult := WithResponsesStreamValidationRetry(t, retryConfig, retryContext,
			func() (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
				bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
				return client.ResponsesStreamRequest(bfCtx, request)
			},
			func(responseChannel chan *schemas.BifrostStreamChunk) ResponsesStreamValidationResult {
				accumulator := NewStreamingToolCallAccumulator()
				var responseCount int

				t.Logf("🔧 Testing Responses API streaming with tool calls...")

				// Create a timeout context for the stream reading
				streamCtx, cancel := context.WithTimeout(ctx, 200*time.Second)
				defer cancel()

				for {
					select {
					case response, ok := <-responseChannel:
						if !ok {
							// Channel closed, streaming completed
							t.Logf("✅ Responses streaming completed. Total chunks received: %d", responseCount)
							goto streamComplete
						}

						if response == nil {
							return ResponsesStreamValidationResult{
								Passed: false,
								Errors: []string{"❌ Streaming response should not be nil"},
							}
						}
						responseCount++

						if response.BifrostResponsesStreamResponse != nil {
							streamResp := response.BifrostResponsesStreamResponse

							// Check for function call events
							switch streamResp.Type {
							case schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta:
								// Arguments are being streamed - check both Delta and Arguments fields
								// Delta is used by most providers (Anthropic, Cohere, Bedrock, OpenAI)
								// Arguments is used by some providers (OpenAI-compatible via mux)
								chunkType := string(streamResp.Type)
								var arguments *string
								argsField := "<none>"
								if streamResp.Delta != nil {
									arguments = streamResp.Delta
									argsField = "streamResp.Delta"
								} else if streamResp.Arguments != nil {
									arguments = streamResp.Arguments
									argsField = "streamResp.Arguments"
								}

								if arguments != nil {
									// Try to get call ID, name, and item ID
									var callID *string
									var name *string
									var itemID *string
									callIDField := "<none>"
									nameField := "<none>"
									itemIDField := "<none>"

									// Item ID is often in the delta event itself (for OpenAI)
									if streamResp.ItemID != nil {
										itemID = streamResp.ItemID
										itemIDField = "streamResp.ItemID"
									}

									// Try to get call ID and name from item if available
									if streamResp.Item != nil && streamResp.Item.ResponsesToolMessage != nil {
										if streamResp.Item.ResponsesToolMessage.CallID != nil {
											callID = streamResp.Item.ResponsesToolMessage.CallID
											callIDField = "streamResp.Item.ResponsesToolMessage.CallID"
										}
										if streamResp.Item.ResponsesToolMessage.Name != nil {
											name = streamResp.Item.ResponsesToolMessage.Name
											nameField = "streamResp.Item.ResponsesToolMessage.Name"
										}
									}

									// Also check if item has an ID
									if streamResp.Item != nil && streamResp.Item.ID != nil {
										itemID = streamResp.Item.ID
										itemIDField = "streamResp.Item.ID"
									}

									// Debug logging: what fields are present in this chunk
									hasID := callID != nil && *callID != ""
									hasName := name != nil && *name != ""
									hasArgs := *arguments != ""
									hasItemID := itemID != nil && *itemID != ""

									t.Logf("📊 [%s] Chunk fields: ID=%v (%s), Name=%v (%s), Args=%v (%s, len=%d), ItemID=%v (%s)",
										chunkType, hasID, callIDField, hasName, nameField, hasArgs, argsField, len(*arguments), hasItemID, itemIDField)

									if hasID {
										t.Logf("  ✅ ID found in %s: %s", chunkType, *callID)
									}
									if hasName {
										t.Logf("  ✅ Name found in %s: %s", chunkType, *name)
									}
									if hasArgs {
										t.Logf("  ✅ Arguments found in %s: %s", chunkType, *arguments)
									}
									if hasItemID {
										t.Logf("  ✅ ItemID found in %s: %s", chunkType, *itemID)
									}

									accumulator.AccumulateResponsesToolCall(callID, name, arguments, itemID)
									callIDStr := "<nil>"
									if callID != nil {
										callIDStr = *callID
									}
									nameStr := "<nil>"
									if name != nil {
										nameStr = *name
									}
									itemIDStr := "<nil>"
									if itemID != nil {
										itemIDStr = *itemID
									}
									t.Logf("🔧 Accumulated function call arguments chunk: callID=%s, name=%s, itemID=%s, args_len=%d",
										callIDStr, nameStr, itemIDStr, len(*arguments))
								}

							case schemas.ResponsesStreamResponseTypeOutputItemAdded:
								// A new function call item was added
								if streamResp.Item != nil && streamResp.Item.Type != nil {
									if *streamResp.Item.Type == schemas.ResponsesMessageTypeFunctionCall {
										chunkType := string(streamResp.Type)
										var callID *string
										var name *string
										var itemID *string
										callIDField := "<none>"
										nameField := "<none>"
										itemIDField := "<none>"

										// Extract itemID first, before any accumulation calls
										if streamResp.Item.ID != nil {
											itemID = streamResp.Item.ID
											itemIDField = "streamResp.Item.ID"
										}

										if streamResp.Item.ResponsesToolMessage != nil {
											if streamResp.Item.ResponsesToolMessage.CallID != nil {
												callID = streamResp.Item.ResponsesToolMessage.CallID
												callIDField = "streamResp.Item.ResponsesToolMessage.CallID"
											}
											if streamResp.Item.ResponsesToolMessage.Name != nil {
												name = streamResp.Item.ResponsesToolMessage.Name
												nameField = "streamResp.Item.ResponsesToolMessage.Name"
											}
											if streamResp.Item.ResponsesToolMessage.Arguments != nil {
												argsField := "streamResp.Item.ResponsesToolMessage.Arguments"
												t.Logf("📊 [%s] Arguments also found in item: %s (len=%d)", chunkType, argsField, len(*streamResp.Item.ResponsesToolMessage.Arguments))
												// Accumulate arguments if found in item
												accumulator.AccumulateResponsesToolCall(callID, name, streamResp.Item.ResponsesToolMessage.Arguments, itemID)
											}
										}

										// Debug logging: what fields are present in this chunk
										hasID := callID != nil && *callID != ""
										hasName := name != nil && *name != ""
										hasItemID := itemID != nil && *itemID != ""

										t.Logf("📊 [%s] Chunk fields: ID=%v (%s), Name=%v (%s), ItemID=%v (%s)",
											chunkType, hasID, callIDField, hasName, nameField, hasItemID, itemIDField)

										if hasID {
											t.Logf("  ✅ ID found in %s: %s", chunkType, *callID)
										}
										if hasName {
											t.Logf("  ✅ Name found in %s: %s", chunkType, *name)
										}
										if hasItemID {
											t.Logf("  ✅ ItemID found in %s: %s", chunkType, *itemID)
										}

										// Initialize or update the tool call (only if Arguments not already accumulated)
										if streamResp.Item.ResponsesToolMessage == nil || streamResp.Item.ResponsesToolMessage.Arguments == nil {
											accumulator.AccumulateResponsesToolCall(callID, name, nil, itemID)
										}
										callIDStr := "<nil>"
										if callID != nil {
											callIDStr = *callID
										}
										nameStr := "<nil>"
										if name != nil {
											nameStr = *name
										}
										itemIDStr := "<nil>"
										if itemID != nil {
											itemIDStr = *itemID
										}
										t.Logf("🔧 Function call item added: callID=%s, name=%s, itemID=%s",
											callIDStr, nameStr, itemIDStr)
									}
								}

							case schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone:
								// Function call arguments are complete - use the complete arguments
								if streamResp.Arguments != nil {
									chunkType := string(streamResp.Type)
									var callID *string
									var name *string
									var itemID *string
									callIDField := "<none>"
									nameField := "<none>"
									itemIDField := "<none>"
									argsField := "streamResp.Arguments"

									if streamResp.ItemID != nil {
										itemID = streamResp.ItemID
										itemIDField = "streamResp.ItemID"
									}

									if streamResp.Item != nil && streamResp.Item.ResponsesToolMessage != nil {
										if streamResp.Item.ResponsesToolMessage.CallID != nil {
											callID = streamResp.Item.ResponsesToolMessage.CallID
											callIDField = "streamResp.Item.ResponsesToolMessage.CallID"
										}
										if streamResp.Item.ResponsesToolMessage.Name != nil {
											name = streamResp.Item.ResponsesToolMessage.Name
											nameField = "streamResp.Item.ResponsesToolMessage.Name"
										}
									}

									if streamResp.Item != nil && streamResp.Item.ID != nil {
										itemID = streamResp.Item.ID
										itemIDField = "streamResp.Item.ID"
									}

									// Debug logging: what fields are present in this chunk
									hasID := callID != nil && *callID != ""
									hasName := name != nil && *name != ""
									hasArgs := streamResp.Arguments != nil && *streamResp.Arguments != ""
									hasItemID := itemID != nil && *itemID != ""

									t.Logf("📊 [%s] Chunk fields: ID=%v (%s), Name=%v (%s), Args=%v (%s, len=%d), ItemID=%v (%s)",
										chunkType, hasID, callIDField, hasName, nameField, hasArgs, argsField, len(*streamResp.Arguments), hasItemID, itemIDField)

									if hasID {
										t.Logf("  ✅ ID found in %s: %s", chunkType, *callID)
									}
									if hasName {
										t.Logf("  ✅ Name found in %s: %s", chunkType, *name)
									}
									if hasArgs {
										t.Logf("  ✅ Complete Arguments found in %s: %s", chunkType, *streamResp.Arguments)
									}
									if hasItemID {
										t.Logf("  ✅ ItemID found in %s: %s", chunkType, *itemID)
									}

									// Use the complete arguments from the done event
									accumulator.AccumulateResponsesToolCall(callID, name, streamResp.Arguments, itemID)
									callIDStr := "<nil>"
									if callID != nil {
										callIDStr = *callID
									}
									nameStr := "<nil>"
									if name != nil {
										nameStr = *name
									}
									itemIDStr := "<nil>"
									if itemID != nil {
										itemIDStr = *itemID
									}
									t.Logf("🔧 Function call arguments done: callID=%s, name=%s, itemID=%s, complete_args=%s",
										callIDStr, nameStr, itemIDStr, *streamResp.Arguments)
								}
							}
						}

						// Safety check to prevent infinite loops
						if responseCount > 500 {
							return ResponsesStreamValidationResult{
								Passed: false,
								Errors: []string{"❌ Received too many streaming chunks, something might be wrong"},
							}
						}

					case <-streamCtx.Done():
						return ResponsesStreamValidationResult{
							Passed:       false,
							Errors:       []string{"❌ Timeout waiting for responses streaming response"},
							ReceivedData: responseCount > 0,
						}
					}
				}

			streamComplete:
				if responseCount == 0 {
					return ResponsesStreamValidationResult{
						Passed:       false,
						Errors:       []string{"❌ Stream closed without receiving any data"},
						ReceivedData: false,
					}
				}

				// Validate final tool calls
				finalToolCalls := accumulator.GetFinalResponsesToolCalls()

				if len(finalToolCalls) == 0 {
					return ResponsesStreamValidationResult{
						Passed:       false,
						Errors:       []string{"❌ No tool calls found in streaming response"},
						ReceivedData: responseCount > 0,
					}
				}

				// Check for missing required fields
				var validationErrors []string
				for i, toolCall := range finalToolCalls {
					if toolCall.ID == "" || toolCall.Name == "" || toolCall.Arguments == "" {
						validationErrors = append(validationErrors, fmt.Sprintf("Tool call %d missing required fields: ID=%v, Name=%v, Arguments=%v",
							i, toolCall.ID != "", toolCall.Name != "", toolCall.Arguments != ""))
					}
				}

				if len(validationErrors) > 0 {
					return ResponsesStreamValidationResult{
						Passed:       false,
						Errors:       validationErrors,
						ReceivedData: responseCount > 0,
					}
				}

				validateStreamingToolCalls(t, finalToolCalls, "Responses API")
				return ResponsesStreamValidationResult{
					Passed:       true,
					ReceivedData: responseCount > 0,
				}
			})

		// Check validation result and fail test if validation failed after all retries
		if !validationResult.Passed {
			allErrors := append(validationResult.Errors, validationResult.StreamErrors...)
			errorMsg := strings.Join(allErrors, "; ")
			if !strings.Contains(errorMsg, "❌") {
				errorMsg = fmt.Sprintf("❌ %s", errorMsg)
			}
			t.Fatalf("❌ Responses streaming tool calls validation failed after retries: %s", errorMsg)
		}

		t.Logf("✅ Responses API streaming with tools test completed successfully")
	})
}

// validateStreamingToolCalls validates that all tool calls have ID, name, and arguments
func validateStreamingToolCalls(t *testing.T, toolCalls []ToolCallInfo, apiName string) {
	if len(toolCalls) == 0 {
		t.Fatalf("❌ %s: No tool calls found in streaming response", apiName)
	}

	t.Logf("📊 %s: Found %d tool call(s) in streaming response", apiName, len(toolCalls))

	for i, toolCall := range toolCalls {
		// Validate ID
		if toolCall.ID == "" {
			t.Fatalf("❌ %s: Tool call %d missing ID", apiName, i)
		} else {
			t.Logf("✅ %s: Tool call %d has ID: %s", apiName, i, toolCall.ID)
		}

		// Validate name
		if toolCall.Name == "" {
			t.Fatalf("❌ %s: Tool call %d missing name", apiName, i)
		} else {
			t.Logf("✅ %s: Tool call %d has name: %s", apiName, i, toolCall.Name)
		}

		// Validate arguments
		if toolCall.Arguments == "" {
			t.Fatalf("❌ %s: Tool call %d missing arguments", apiName, i)
		} else {
			// Try to parse arguments as JSON to ensure they're valid
			var args map[string]interface{}
			if err := json.Unmarshal([]byte(toolCall.Arguments), &args); err != nil {
				t.Logf("⚠️ %s: Tool call %d arguments are not valid JSON: %v", apiName, i, err)
				// Don't fail on this - some providers might send partial JSON during streaming
				// But we should at least have some content
				if strings.TrimSpace(toolCall.Arguments) == "" {
					t.Fatalf("❌ %s: Tool call %d has empty arguments", apiName, i)
				}
			} else {
				t.Logf("✅ %s: Tool call %d has valid JSON arguments: %s", apiName, i, toolCall.Arguments)
			}
		}

		// All three must be present for the test to pass
		require.NotEmpty(t, toolCall.ID, "%s: Tool call %d must have an ID", apiName, i)
		require.NotEmpty(t, toolCall.Name, "%s: Tool call %d must have a name", apiName, i)
		require.NotEmpty(t, toolCall.Arguments, "%s: Tool call %d must have arguments", apiName, i)
	}

	t.Logf("✅ %s: All tool calls have ID, name, and arguments present", apiName)
}

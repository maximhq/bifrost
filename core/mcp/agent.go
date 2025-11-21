package mcp

import (
	"context"
	"fmt"
	"sync"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// ExecuteAgent handles the agent mode execution loop
func ExecuteAgent(
	ctx *context.Context,
	maxAgentDepth int,
	originalReq *schemas.BifrostChatRequest,
	initialResponse *schemas.BifrostChatResponse,
	llmCaller schemas.BifrostLLMCaller,
	fetchNewRequestIDFunc func(ctx context.Context) string,
	executeToolFunc func(ctx context.Context, toolCall schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, error),
	clientForToolFetcherFunc func(toolName string) *schemas.MCPClientState, // Function to get the client for a tool
) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	logger.Debug("Entering agent mode - detected tool calls in response")

	// Create conversation history starting with original messages
	conversationHistory := make([]schemas.ChatMessage, 0)
	if originalReq.Input != nil {
		conversationHistory = append(conversationHistory, originalReq.Input...)
	}

	currentResponse := initialResponse
	depth := 0

	// Track all executed tool results and tool calls across all iterations
	allExecutedToolResults := make([]*schemas.ChatMessage, 0)
	allExecutedToolCalls := make([]schemas.ChatAssistantMessageToolCall, 0)

	originalRequestID, ok := (*ctx).Value(schemas.BifrostContextKeyRequestID).(string)
	if ok {
		*ctx = context.WithValue(*ctx, schemas.BifrostMCPAgentOriginalRequestID, originalRequestID)
	}
	for depth < maxAgentDepth {
		toolCalls := extractToolCalls(currentResponse)
		if len(toolCalls) == 0 {
			logger.Debug("No more tool calls found, exiting agent mode")
			break
		}

		logger.Debug(fmt.Sprintf("Agent mode depth %d: executing %d tool calls", depth, len(toolCalls)))

		// Separate tools into auto-executable and non-auto-executable groups
		var autoExecutableTools []schemas.ChatAssistantMessageToolCall
		var nonAutoExecutableTools []schemas.ChatAssistantMessageToolCall

		for _, toolCall := range toolCalls {
			if toolCall.Function.Name == nil {
				// Skip tools without names
				nonAutoExecutableTools = append(nonAutoExecutableTools, toolCall)
				continue
			}

			toolName := *toolCall.Function.Name
			client := clientForToolFetcherFunc(toolName)
			if client == nil {
				// If client not found, treat as non-auto-executable
				logger.Warn(fmt.Sprintf("Client not found for tool %s, treating as non-auto-executable", toolName))
				nonAutoExecutableTools = append(nonAutoExecutableTools, toolCall)
				continue
			}

			// Check if tool can be auto-executed
			if canAutoExecuteTool(toolName, client.ExecutionConfig) {
				autoExecutableTools = append(autoExecutableTools, toolCall)
				logger.Debug(fmt.Sprintf("Tool %s can be auto-executed", toolName))
			} else {
				nonAutoExecutableTools = append(nonAutoExecutableTools, toolCall)
				logger.Debug(fmt.Sprintf("Tool %s cannot be auto-executed", toolName))
			}
		}

		// Execute auto-executable tools first
		var executedToolResults []*schemas.ChatMessage
		if len(autoExecutableTools) > 0 {
			// Add assistant message with auto-executable tool calls to conversation
			assistantMessage := &schemas.ChatMessage{
				Role: schemas.ChatMessageRoleAssistant,
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					ToolCalls: autoExecutableTools,
				},
			}

			// Add content if present
			if len(currentResponse.Choices) > 0 &&
				currentResponse.Choices[0].ChatNonStreamResponseChoice != nil &&
				currentResponse.Choices[0].ChatNonStreamResponseChoice.Message != nil &&
				currentResponse.Choices[0].ChatNonStreamResponseChoice.Message.Content != nil {
				assistantMessage.Content = currentResponse.Choices[0].ChatNonStreamResponseChoice.Message.Content
			}

			conversationHistory = append(conversationHistory, *assistantMessage)

			// Execute all auto-executable tool calls parallelly
			wg := sync.WaitGroup{}
			wg.Add(len(autoExecutableTools))
			channelToolResults := make(chan *schemas.ChatMessage, len(autoExecutableTools))
			for _, toolCall := range autoExecutableTools {
				go func(toolCall schemas.ChatAssistantMessageToolCall) {
					defer wg.Done()
					toolResult, toolErr := executeToolFunc(*ctx, toolCall)
					if toolErr != nil {
						logger.Warn(fmt.Sprintf("Tool execution failed: %v", toolErr))
						channelToolResults <- createToolResultMessage(toolCall, "", toolErr)
					} else {
						channelToolResults <- toolResult
					}
				}(toolCall)
			}
			wg.Wait()
			close(channelToolResults)

			// Collect tool results
			executedToolResults = make([]*schemas.ChatMessage, 0, len(autoExecutableTools))
			for toolResult := range channelToolResults {
				executedToolResults = append(executedToolResults, toolResult)
			}

			// Track executed tool results and calls across all iterations
			allExecutedToolResults = append(allExecutedToolResults, executedToolResults...)
			allExecutedToolCalls = append(allExecutedToolCalls, autoExecutableTools...)

			// Add tool results to conversation history
			for _, toolResult := range executedToolResults {
				conversationHistory = append(conversationHistory, *toolResult)
			}
		}

		// If there are non-auto-executable tools, return them immediately without continuing the loop
		if len(nonAutoExecutableTools) > 0 {
			logger.Debug(fmt.Sprintf("Found %d non-auto-executable tools, returning them immediately without continuing the loop", len(nonAutoExecutableTools)))
			// Create response with all executed tool results from all iterations, and non-auto-executable tool calls
			return createResponseWithExecutedToolsAndNonAutoExecutableCalls(currentResponse, allExecutedToolResults, allExecutedToolCalls, nonAutoExecutableTools), nil
		}

		// Create new request with updated conversation history
		newReq := &schemas.BifrostChatRequest{
			Provider:  originalReq.Provider,
			Model:     originalReq.Model,
			Fallbacks: originalReq.Fallbacks,
			Params:    originalReq.Params, // Preserve all original parameters including tools
			Input:     conversationHistory,
		}

		if fetchNewRequestIDFunc != nil {
			newID := fetchNewRequestIDFunc(*ctx)
			if newID != "" {
				*ctx = context.WithValue(*ctx, schemas.BifrostContextKeyRequestID, newID)
			}
		}

		// Make new LLM request
		response, err := llmCaller.ChatCompletionRequest(*ctx, newReq)
		if err != nil {
			logger.Error("Agent mode: LLM request failed: %v", err)
			return nil, err
		}

		currentResponse = response
		depth++
	}

	// Check if we hit max depth
	if depth >= maxAgentDepth {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: fmt.Sprintf("Agent mode exceeded maximum depth of %d", maxAgentDepth),
			},
		}
	}

	logger.Debug(fmt.Sprintf("Agent mode completed after %d iterations", depth))
	return currentResponse, nil
}

// hasToolCalls checks if a chat response contains tool calls that need to be executed
func hasToolCalls(response *schemas.BifrostChatResponse) bool {
	if response == nil || len(response.Choices) == 0 {
		return false
	}

	choice := response.Choices[0]

	// Check finish reason
	if choice.FinishReason != nil && *choice.FinishReason == "tool_calls" {
		return true
	}

	// Check if message has tool calls
	if choice.ChatNonStreamResponseChoice != nil &&
		choice.ChatNonStreamResponseChoice.Message != nil &&
		choice.ChatNonStreamResponseChoice.Message.ChatAssistantMessage != nil &&
		len(choice.ChatNonStreamResponseChoice.Message.ChatAssistantMessage.ToolCalls) > 0 {
		return true
	}

	return false
}

// extractToolCalls extracts tool calls from a chat response
func extractToolCalls(response *schemas.BifrostChatResponse) []schemas.ChatAssistantMessageToolCall {
	if !hasToolCalls(response) {
		return nil
	}

	var toolCalls []schemas.ChatAssistantMessageToolCall
	for _, choice := range response.Choices {
		if choice.ChatNonStreamResponseChoice != nil &&
			choice.ChatNonStreamResponseChoice.Message != nil &&
			choice.ChatNonStreamResponseChoice.Message.ChatAssistantMessage != nil {
			toolCalls = append(toolCalls, choice.ChatNonStreamResponseChoice.Message.ChatAssistantMessage.ToolCalls...)
		}
	}

	return toolCalls
}

// createToolResultMessage creates a tool result message from tool execution
func createToolResultMessage(toolCall schemas.ChatAssistantMessageToolCall, result string, err error) *schemas.ChatMessage {
	var content string
	if err != nil {
		content = fmt.Sprintf("Error executing tool %s: %s",
			func() string {
				if toolCall.Function.Name != nil {
					return *toolCall.Function.Name
				}
				return "unknown"
			}(), err.Error())
	} else {
		content = result
	}

	return &schemas.ChatMessage{
		Role: schemas.ChatMessageRoleTool,
		Content: &schemas.ChatMessageContent{
			ContentStr: &content,
		},
		ChatToolMessage: &schemas.ChatToolMessage{
			ToolCallID: toolCall.ID,
		},
	}
}

// createResponseWithExecutedToolsAndNonAutoExecutableCalls creates a response that includes:
// 1. A single choice with text content showing executed tool results
// 2. Non-auto-executable tool calls
func createResponseWithExecutedToolsAndNonAutoExecutableCalls(
	originalResponse *schemas.BifrostChatResponse,
	executedToolResults []*schemas.ChatMessage,
	executedToolCalls []schemas.ChatAssistantMessageToolCall,
	nonAutoExecutableToolCalls []schemas.ChatAssistantMessageToolCall,
) *schemas.BifrostChatResponse {
	// Start with a copy of the original response metadata
	response := &schemas.BifrostChatResponse{
		ID:      originalResponse.ID,
		Object:  originalResponse.Object,
		Created: originalResponse.Created,
		Model:   originalResponse.Model,
		Choices: make([]schemas.BifrostResponseChoice, 0),
	}

	// Build a map from tool call ID to tool name for easy lookup
	toolCallIDToName := make(map[string]string)
	for _, toolCall := range executedToolCalls {
		if toolCall.ID != nil && toolCall.Function.Name != nil {
			toolCallIDToName[*toolCall.ID] = *toolCall.Function.Name
		}
	}

	// Build content text showing executed tool results
	var contentText string
	if len(executedToolResults) > 0 {
		// Format tool results as JSON-like structure
		toolResultsMap := make(map[string]interface{})
		for _, toolResult := range executedToolResults {
			// Get tool name from tool call ID mapping
			var toolName string
			if toolResult.ChatToolMessage != nil && toolResult.ChatToolMessage.ToolCallID != nil {
				toolCallID := *toolResult.ChatToolMessage.ToolCallID
				if name, ok := toolCallIDToName[toolCallID]; ok {
					toolName = name
				} else {
					toolName = toolCallID // Fallback to tool call ID if name not found
				}
			} else {
				toolName = "unknown_tool"
			}

			// Extract output from tool result
			var output interface{}
			if toolResult.Content != nil {
				if toolResult.Content.ContentStr != nil {
					output = *toolResult.Content.ContentStr
				} else if toolResult.Content.ContentBlocks != nil {
					// Convert content blocks to a readable format
					blocks := make([]map[string]interface{}, 0)
					for _, block := range toolResult.Content.ContentBlocks {
						blockMap := make(map[string]interface{})
						blockMap["type"] = string(block.Type)
						if block.Text != nil {
							blockMap["text"] = *block.Text
						}
						blocks = append(blocks, blockMap)
					}
					output = blocks
				}
			}
			toolResultsMap[toolName] = output
		}

		// Convert to JSON string for display
		jsonBytes, err := sonic.Marshal(toolResultsMap)
		if err != nil {
			// Fallback to simple string representation
			contentText = fmt.Sprintf("The Output from allowed tools calls is - %v\n\nNow I shall call these tools next...", toolResultsMap)
		} else {
			contentText = fmt.Sprintf("The Output from allowed tools calls is - %s\n\nNow I shall call these tools next...", string(jsonBytes))
		}
	} else {
		contentText = "Now I shall call these tools next..."
	}

	// Create content with the formatted text
	content := &schemas.ChatMessageContent{
		ContentStr: &contentText,
	}

	// Determine finish reason
	var finishReason *string
	if len(nonAutoExecutableToolCalls) > 0 {
		reason := "tool_calls"
		finishReason = &reason
	}

	// Create a single choice with the formatted content and non-auto-executable tool calls
	response.Choices = append(response.Choices, schemas.BifrostResponseChoice{
		Index:        0,
		FinishReason: finishReason,
		ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
			Message: &schemas.ChatMessage{
				Role:    schemas.ChatMessageRoleAssistant,
				Content: content,
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					ToolCalls: nonAutoExecutableToolCalls,
				},
			},
		},
	})

	return response
}

package mcp

import (
	"context"
	"fmt"
	"sync"

	"github.com/maximhq/bifrost/core/schemas"
)

// ExecuteAgentMode handles the agent mode execution loop
func CheckAndExecuteAgentMode(
	ctx *context.Context,
	maxAgentDepth int,
	originalReq *schemas.BifrostChatRequest,
	initialResponse *schemas.BifrostChatResponse,
	llmCaller schemas.BifrostLLMCaller,
	fetchNewRequestIDFunc func(ctx context.Context) string,
	executeToolFunc func(ctx context.Context, toolCall schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, error),
) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	if llmCaller == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: "llmCaller is required to execute agent mode",
			},
		}
	}
	if executeToolFunc == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: "executeToolFunc is required to execute agent mode",
			},
		}
	}
	// Check if initial response has tool calls
	if !hasToolCalls(initialResponse) {
		logger.Debug("No tool calls detected, returning initial response")
		return initialResponse, nil
	}

	logger.Debug("Entering agent mode - detected tool calls in response")

	// Create conversation history starting with original messages
	conversationHistory := make([]schemas.ChatMessage, 0)
	if originalReq.Input != nil {
		conversationHistory = append(conversationHistory, originalReq.Input...)
	}

	currentResponse := initialResponse
	depth := 0

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

		// Add assistant message with tool calls to conversation
		assistantMessage := &schemas.ChatMessage{
			Role: schemas.ChatMessageRoleAssistant,
			ChatAssistantMessage: &schemas.ChatAssistantMessage{
				ToolCalls: toolCalls,
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

		// Execute all tool calls parallelly
		wg := sync.WaitGroup{}
		wg.Add(len(toolCalls))
		channelToolResults := make(chan *schemas.ChatMessage, len(toolCalls))
		for _, toolCall := range toolCalls {
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
		toolResults := make([]*schemas.ChatMessage, 0, len(toolCalls))
		for toolResult := range channelToolResults {
			toolResults = append(toolResults, toolResult)
		}

		// Add tool results to conversation history
		for _, toolResult := range toolResults {
			conversationHistory = append(conversationHistory, *toolResult)
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

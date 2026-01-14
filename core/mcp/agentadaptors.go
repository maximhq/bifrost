package mcp

import (
	"fmt"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// agentAPIAdapter defines the interface for API-specific operations in agent mode.
// This adapter pattern allows the agent execution logic to work with both Chat Completions
// and Responses APIs without requiring API-specific code in the agent loop.
//
// The adapter handles format conversions at the boundaries:
//   - Responses API requests/responses are converted to/from Chat API format
//   - Tool calls are extracted in Chat format for uniform processing
//   - Results are converted back to the original API format for the response
//
// This design ensures that:
//  1. Tool execution logic is format-agnostic
//  2. Both APIs have feature parity
//  3. Conversions are localized to adapters
//  4. The agent loop remains API-neutral
type agentAPIAdapter interface {
	// Extract conversation history from the original request
	getConversationHistory() []interface{}

	// Get original request
	getOriginalRequest() interface{}

	// Get initial response
	getInitialResponse() interface{}

	// Check if response has tool calls
	hasToolCalls(response interface{}) bool

	// Extract tool calls from response.
	// For Chat API: Returns tool calls directly from the response.
	// For Responses API: Converts ResponsesMessage tool calls to ChatAssistantMessageToolCall for processing.
	extractToolCalls(response interface{}) []schemas.ChatAssistantMessageToolCall

	// Add assistant message with tool calls to conversation
	addAssistantMessage(conversation []interface{}, response interface{}) []interface{}

	// Add tool results to conversation.
	// For Chat API: Adds ChatMessage results directly.
	// For Responses API: Converts ChatMessage results to ResponsesMessage via ToResponsesToolMessage().
	addToolResults(conversation []interface{}, toolResults []*schemas.ChatMessage) []interface{}

	// Create new request with updated conversation
	createNewRequest(conversation []interface{}) interface{}

	// Make LLM call
	makeLLMCall(ctx *schemas.BifrostContext, request interface{}) (interface{}, *schemas.BifrostError)

	// Create response with executed tools and non-auto-executable calls
	createResponseWithExecutedTools(
		response interface{},
		executedToolResults []*schemas.ChatMessage,
		executedToolCalls []schemas.ChatAssistantMessageToolCall,
		nonAutoExecutableToolCalls []schemas.ChatAssistantMessageToolCall,
	) interface{}
}

// chatAPIAdapter implements agentAPIAdapter for Chat API
type chatAPIAdapter struct {
	originalReq     *schemas.BifrostChatRequest
	initialResponse *schemas.BifrostChatResponse
	makeReq         func(ctx *schemas.BifrostContext, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError)
}

// responsesAPIAdapter implements agentAPIAdapter for Responses API.
// It enables the agent mode execution loop to work with Responses API requests and responses
// by handling format conversions transparently.
//
// Key conversions performed:
//   - extractToolCalls(): Converts ResponsesMessage tool calls to ChatAssistantMessageToolCall
//     via BifrostResponsesResponse.ToBifrostChatResponse() and existing extraction logic
//   - addToolResults(): Converts ChatMessage tool results back to ResponsesMessage
//     via ChatMessage.ToResponsesMessages() and ToResponsesToolMessage()
//   - createNewRequest(): Builds a new BifrostResponsesRequest from converted conversation
//   - createResponseWithExecutedTools(): Creates a Responses response with results and pending tools
//
// This adapter enables full feature parity between Chat Completions and Responses APIs
// for tool execution in agent mode.
type responsesAPIAdapter struct {
	originalReq     *schemas.BifrostResponsesRequest
	initialResponse *schemas.BifrostResponsesResponse
	makeReq         func(ctx *schemas.BifrostContext, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError)
}

// Chat API adapter implementations
func (c *chatAPIAdapter) getConversationHistory() []interface{} {
	history := make([]interface{}, 0)
	if c.originalReq.Input != nil {
		for _, msg := range c.originalReq.Input {
			history = append(history, msg)
		}
	}
	return history
}

func (c *chatAPIAdapter) getOriginalRequest() interface{} {
	return c.originalReq
}

func (c *chatAPIAdapter) getInitialResponse() interface{} {
	return c.initialResponse
}

func (c *chatAPIAdapter) hasToolCalls(response interface{}) bool {
	chatResponse := response.(*schemas.BifrostChatResponse)
	return hasToolCallsForChatResponse(chatResponse)
}

func (c *chatAPIAdapter) extractToolCalls(response interface{}) []schemas.ChatAssistantMessageToolCall {
	chatResponse := response.(*schemas.BifrostChatResponse)
	return extractToolCalls(chatResponse)
}

func (c *chatAPIAdapter) addAssistantMessage(conversation []interface{}, response interface{}) []interface{} {
	chatResponse := response.(*schemas.BifrostChatResponse)
	for _, choice := range chatResponse.Choices {
		if choice.ChatNonStreamResponseChoice != nil && choice.ChatNonStreamResponseChoice.Message != nil {
			conversation = append(conversation, *choice.ChatNonStreamResponseChoice.Message)
		}
	}
	return conversation
}

func (c *chatAPIAdapter) addToolResults(conversation []interface{}, toolResults []*schemas.ChatMessage) []interface{} {
	for _, toolResult := range toolResults {
		conversation = append(conversation, *toolResult)
	}
	return conversation
}

func (c *chatAPIAdapter) createNewRequest(conversation []interface{}) interface{} {
	// Convert conversation back to ChatMessage slice
	chatMessages := make([]schemas.ChatMessage, 0, len(conversation))
	for _, msg := range conversation {
		if msg == nil {
			continue
		}
		if chatMessage, ok := msg.(schemas.ChatMessage); ok {
			chatMessages = append(chatMessages, chatMessage)
		}
	}

	return &schemas.BifrostChatRequest{
		Provider:  c.originalReq.Provider,
		Model:     c.originalReq.Model,
		Fallbacks: c.originalReq.Fallbacks,
		Params:    c.originalReq.Params,
		Input:     chatMessages,
	}
}

func (c *chatAPIAdapter) makeLLMCall(ctx *schemas.BifrostContext, request interface{}) (interface{}, *schemas.BifrostError) {
	chatRequest := request.(*schemas.BifrostChatRequest)
	return c.makeReq(ctx, chatRequest)
}

func (c *chatAPIAdapter) createResponseWithExecutedTools(
	response interface{},
	executedToolResults []*schemas.ChatMessage,
	executedToolCalls []schemas.ChatAssistantMessageToolCall,
	nonAutoExecutableToolCalls []schemas.ChatAssistantMessageToolCall,
) interface{} {
	chatResponse := response.(*schemas.BifrostChatResponse)
	return createChatResponseWithExecutedToolsAndNonAutoExecutableCalls(
		chatResponse,
		executedToolResults,
		executedToolCalls,
		nonAutoExecutableToolCalls,
	)
}

// createChatResponseWithExecutedToolsAndNonAutoExecutableCalls creates a chat response
// that includes executed tool results and non-auto-executable tool calls. The response
// contains a formatted text summary of executed tool results and includes the non-auto-executable
// tool calls for the caller to handle. The finish reason is set to "stop" to prevent
// further agent loop iterations.
//
// Parameters:
//   - originalResponse: The original chat response to copy metadata from
//   - executedToolResults: List of tool execution results from auto-executable tools
//   - executedToolCalls: List of tool calls that were executed
//   - nonAutoExecutableToolCalls: List of tool calls that require manual execution
//
// Returns:
//   - *schemas.BifrostChatResponse: A new chat response with executed results and pending tool calls
func createChatResponseWithExecutedToolsAndNonAutoExecutableCalls(
	originalResponse *schemas.BifrostChatResponse,
	executedToolResults []*schemas.ChatMessage,
	executedToolCalls []schemas.ChatAssistantMessageToolCall,
	nonAutoExecutableToolCalls []schemas.ChatAssistantMessageToolCall,
) *schemas.BifrostChatResponse {
	// Start with a copy of the original response metadata
	response := &schemas.BifrostChatResponse{
		ID:                originalResponse.ID,
		Object:            originalResponse.Object,
		Created:           originalResponse.Created,
		Model:             originalResponse.Model,
		Choices:           make([]schemas.BifrostResponseChoice, 0),
		ServiceTier:       originalResponse.ServiceTier,
		SystemFingerprint: originalResponse.SystemFingerprint,
		Usage:             originalResponse.Usage,
		ExtraFields:       originalResponse.ExtraFields,
		SearchResults:     originalResponse.SearchResults,
		Videos:            originalResponse.Videos,
		Citations:         originalResponse.Citations,
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
	// Note: We set finish_reason to "stop" (not "tool_calls") for non-auto-executable tools
	// to prevent the agent loop from retrying. The tool calls are still included in the response
	// for the caller to handle, but setting finish_reason to "stop" ensures hasToolCalls returns false
	// and the agent loop exits properly.
	finishReason := "stop"

	// Create a single choice with the formatted content and non-auto-executable tool calls
	response.Choices = append(response.Choices, schemas.BifrostResponseChoice{
		Index:        0,
		FinishReason: &finishReason,
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

// Responses API adapter implementations
func (r *responsesAPIAdapter) getConversationHistory() []interface{} {
	history := make([]interface{}, 0)
	if r.originalReq.Input != nil {
		for _, msg := range r.originalReq.Input {
			history = append(history, msg)
		}
	}
	return history
}

func (r *responsesAPIAdapter) getOriginalRequest() interface{} {
	return r.originalReq
}

func (r *responsesAPIAdapter) getInitialResponse() interface{} {
	return r.initialResponse
}

func (r *responsesAPIAdapter) hasToolCalls(response interface{}) bool {
	responsesResponse := response.(*schemas.BifrostResponsesResponse)
	return hasToolCallsForResponsesResponse(responsesResponse)
}

func (r *responsesAPIAdapter) extractToolCalls(response interface{}) []schemas.ChatAssistantMessageToolCall {
	responsesResponse := response.(*schemas.BifrostResponsesResponse)
	// Convert to Chat format and extract tool calls using existing logic
	chatResponse := responsesResponse.ToBifrostChatResponse()
	return extractToolCalls(chatResponse)
}

func (r *responsesAPIAdapter) addAssistantMessage(conversation []interface{}, response interface{}) []interface{} {
	responsesResponse := response.(*schemas.BifrostResponsesResponse)
	for _, output := range responsesResponse.Output {
		conversation = append(conversation, output)
	}
	return conversation
}

func (r *responsesAPIAdapter) addToolResults(conversation []interface{}, toolResults []*schemas.ChatMessage) []interface{} {
	for _, toolResult := range toolResults {
		// Convert using existing converter
		responsesMessages := toolResult.ToResponsesMessages()
		for _, respMsg := range responsesMessages {
			conversation = append(conversation, respMsg)
		}
	}
	return conversation
}

func (r *responsesAPIAdapter) createNewRequest(conversation []interface{}) interface{} {
	// Convert conversation back to ResponsesMessage slice
	responsesMessages := make([]schemas.ResponsesMessage, 0, len(conversation))
	for _, msg := range conversation {
		responsesMessages = append(responsesMessages, msg.(schemas.ResponsesMessage))
	}

	return &schemas.BifrostResponsesRequest{
		Provider:  r.originalReq.Provider,
		Model:     r.originalReq.Model,
		Fallbacks: r.originalReq.Fallbacks,
		Params:    r.originalReq.Params,
		Input:     responsesMessages,
	}
}

func (r *responsesAPIAdapter) makeLLMCall(ctx *schemas.BifrostContext, request interface{}) (interface{}, *schemas.BifrostError) {
	responsesRequest := request.(*schemas.BifrostResponsesRequest)
	return r.makeReq(ctx, responsesRequest)
}

func (r *responsesAPIAdapter) createResponseWithExecutedTools(
	response interface{},
	executedToolResults []*schemas.ChatMessage,
	executedToolCalls []schemas.ChatAssistantMessageToolCall,
	nonAutoExecutableToolCalls []schemas.ChatAssistantMessageToolCall,
) interface{} {
	responsesResponse := response.(*schemas.BifrostResponsesResponse)

	// Create response with executed tools directly on Responses schema
	return createResponsesResponseWithExecutedToolsAndNonAutoExecutableCalls(
		responsesResponse,
		executedToolResults,
		executedToolCalls,
		nonAutoExecutableToolCalls,
	)
}

// createResponsesResponseWithExecutedToolsAndNonAutoExecutableCalls creates a responses response
// that includes executed tool results and non-auto-executable tool calls. The response
// contains a formatted text summary of executed tool results and includes the non-auto-executable
// tool calls for the caller to handle. All Response-specific fields are preserved.
//
// Parameters:
//   - originalResponse: The original responses response to copy metadata from
//   - executedToolResults: List of tool execution results from auto-executable tools
//   - executedToolCalls: List of tool calls that were executed
//   - nonAutoExecutableToolCalls: List of tool calls that require manual execution
//
// Returns:
//   - *schemas.BifrostResponsesResponse: A new responses response with executed results and pending tool calls
func createResponsesResponseWithExecutedToolsAndNonAutoExecutableCalls(
	originalResponse *schemas.BifrostResponsesResponse,
	executedToolResults []*schemas.ChatMessage,
	executedToolCalls []schemas.ChatAssistantMessageToolCall,
	nonAutoExecutableToolCalls []schemas.ChatAssistantMessageToolCall,
) *schemas.BifrostResponsesResponse {
	// Start with a copy of the original response, preserving all Response-specific fields
	response := &schemas.BifrostResponsesResponse{
		ID:                 originalResponse.ID,
		Background:         originalResponse.Background,
		Conversation:       originalResponse.Conversation,
		CreatedAt:          originalResponse.CreatedAt,
		Error:              originalResponse.Error,
		Include:            originalResponse.Include,
		IncompleteDetails:  originalResponse.IncompleteDetails,
		Instructions:       originalResponse.Instructions,
		MaxOutputTokens:    originalResponse.MaxOutputTokens,
		MaxToolCalls:       originalResponse.MaxToolCalls,
		Metadata:           originalResponse.Metadata,
		ParallelToolCalls:  originalResponse.ParallelToolCalls,
		PreviousResponseID: originalResponse.PreviousResponseID,
		Prompt:             originalResponse.Prompt,
		PromptCacheKey:     originalResponse.PromptCacheKey,
		Reasoning:          originalResponse.Reasoning,
		SafetyIdentifier:   originalResponse.SafetyIdentifier,
		ServiceTier:        originalResponse.ServiceTier,
		StreamOptions:      originalResponse.StreamOptions,
		Store:              originalResponse.Store,
		Temperature:        originalResponse.Temperature,
		Text:               originalResponse.Text,
		TopLogProbs:        originalResponse.TopLogProbs,
		TopP:               originalResponse.TopP,
		ToolChoice:         originalResponse.ToolChoice,
		Tools:              originalResponse.Tools,
		Truncation:         originalResponse.Truncation,
		Usage:              originalResponse.Usage,
		ExtraFields:        originalResponse.ExtraFields,
		// Perplexity-specific fields
		SearchResults: originalResponse.SearchResults,
		Videos:        originalResponse.Videos,
		Citations:     originalResponse.Citations,
		Output:        make([]schemas.ResponsesMessage, 0),
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

	// Create assistant message with the formatted text content
	messageType := schemas.ResponsesMessageTypeMessage
	role := schemas.ResponsesInputMessageRoleAssistant
	assistantMessage := schemas.ResponsesMessage{
		Type: &messageType,
		Role: &role,
		Content: &schemas.ResponsesMessageContent{
			ContentBlocks: []schemas.ResponsesMessageContentBlock{
				{
					Type: schemas.ResponsesOutputMessageContentTypeText,
					Text: &contentText,
				},
			},
		},
	}
	response.Output = append(response.Output, assistantMessage)

	// Add non-auto-executable tool calls as separate function_call messages
	for _, toolCall := range nonAutoExecutableToolCalls {
		functionCallType := schemas.ResponsesMessageTypeFunctionCall
		assistantRole := schemas.ResponsesInputMessageRoleAssistant

		var callID *string
		if toolCall.ID != nil && *toolCall.ID != "" {
			callID = toolCall.ID
		}

		var namePtr *string
		if toolCall.Function.Name != nil && *toolCall.Function.Name != "" {
			namePtr = toolCall.Function.Name
		}

		var argumentsPtr *string
		if toolCall.Function.Arguments != "" {
			argumentsPtr = &toolCall.Function.Arguments
		}

		toolCallMessage := schemas.ResponsesMessage{
			Type: &functionCallType,
			Role: &assistantRole,
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID:    callID,
				Name:      namePtr,
				Arguments: argumentsPtr,
			},
		}

		response.Output = append(response.Output, toolCallMessage)
	}

	return response
}

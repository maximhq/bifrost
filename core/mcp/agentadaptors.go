package mcp

import (
	"fmt"
	"slices"
	"strings"

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

	// extractUsage returns the token usage from a response as BifrostLLMUsage.
	extractUsage(response interface{}) *schemas.BifrostLLMUsage

	// applyUsage sets accumulated usage on the response in place.
	applyUsage(response interface{}, usage *schemas.BifrostLLMUsage)

	// carryForward records the assistant text and reasoning of a turn the loop is about to
	// supersede, leaving out the tool calls Bifrost executed for it.
	carryForward(response interface{}, executedToolCalls []schemas.ChatAssistantMessageToolCall)

	// applyCarriedContent prepends what carryForward recorded to the final response, in turn order.
	applyCarriedContent(response interface{})

	// routingInfo returns the route that served a response.
	routingInfo(response interface{}) schemas.RoutingInfo
}

// chatAPIAdapter implements agentAPIAdapter for Chat API
type chatAPIAdapter struct {
	originalReq     *schemas.BifrostChatRequest
	initialResponse *schemas.BifrostChatResponse
	makeReq         func(ctx *schemas.BifrostContext, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError)

	carriedText             []string                       // assistant text of superseded turns, in order
	carriedReasoning        []string                       // reasoning text of superseded turns, in order
	carriedReasoningDetails []schemas.ChatReasoningDetails // reasoning details (signatures intact) of superseded turns
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

	carriedOutput []schemas.ResponsesMessage // output items of superseded turns, executed tool calls removed
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

func (c *chatAPIAdapter) extractUsage(response interface{}) *schemas.BifrostLLMUsage {
	return response.(*schemas.BifrostChatResponse).Usage
}

func (c *chatAPIAdapter) applyUsage(response interface{}, usage *schemas.BifrostLLMUsage) {
	response.(*schemas.BifrostChatResponse).Usage = usage
}

func (c *chatAPIAdapter) routingInfo(response interface{}) schemas.RoutingInfo {
	return response.(*schemas.BifrostChatResponse).ExtraFields.RoutingInfo
}

// carryForward keeps the first choice's text and reasoning: the agent loop only ever
// continues the first choice's conversation, so that is the one the client is shown. Tool
// calls are left behind - this turn's were executed by Bifrost, and the final response lists
// any the client still has to run.
func (c *chatAPIAdapter) carryForward(response interface{}, _ []schemas.ChatAssistantMessageToolCall) {
	message := firstChatMessage(response.(*schemas.BifrostChatResponse))
	if message == nil {
		return
	}
	if text := chatMessageText(message.Content); text != "" {
		c.carriedText = append(c.carriedText, text)
	}
	if assistant := message.ChatAssistantMessage; assistant != nil {
		if assistant.Reasoning != nil && *assistant.Reasoning != "" {
			c.carriedReasoning = append(c.carriedReasoning, *assistant.Reasoning)
		}
		c.carriedReasoningDetails = append(c.carriedReasoningDetails, assistant.ReasoningDetails...)
	}
}

// applyCarriedContent puts earlier turns' text ahead of the final message's own, and their
// reasoning ahead of its reasoning. String content stays a string (parts joined by a blank
// line) so OpenAI-shaped clients keep the shape they expect; block content gains leading
// text blocks. Reasoning details keep their signatures and are re-indexed in order.
func (c *chatAPIAdapter) applyCarriedContent(response interface{}) {
	if len(c.carriedText) == 0 && len(c.carriedReasoning) == 0 && len(c.carriedReasoningDetails) == 0 {
		return
	}
	message := firstChatMessage(response.(*schemas.BifrostChatResponse))
	if message == nil {
		return
	}
	if len(c.carriedText) > 0 {
		if message.Content == nil || message.Content.ContentStr != nil || len(message.Content.ContentBlocks) == 0 {
			var last *string
			if message.Content != nil {
				last = message.Content.ContentStr
			}
			joined := joinNonEmpty(c.carriedText, last)
			message.Content = &schemas.ChatMessageContent{ContentStr: &joined}
		} else {
			blocks := make([]schemas.ChatContentBlock, 0, len(c.carriedText)+len(message.Content.ContentBlocks))
			for i := range c.carriedText {
				blocks = append(blocks, schemas.ChatContentBlock{Type: schemas.ChatContentBlockTypeText, Text: &c.carriedText[i]})
			}
			blocks = append(blocks, message.Content.ContentBlocks...)
			message.Content = &schemas.ChatMessageContent{ContentBlocks: blocks}
		}
	}
	if len(c.carriedReasoning) == 0 && len(c.carriedReasoningDetails) == 0 {
		return
	}
	if message.ChatAssistantMessage == nil {
		message.ChatAssistantMessage = &schemas.ChatAssistantMessage{}
	}
	assistant := message.ChatAssistantMessage
	if len(c.carriedReasoning) > 0 {
		joined := joinNonEmpty(c.carriedReasoning, assistant.Reasoning)
		assistant.Reasoning = &joined
	}
	if len(c.carriedReasoningDetails) > 0 {
		details := make([]schemas.ChatReasoningDetails, 0, len(c.carriedReasoningDetails)+len(assistant.ReasoningDetails))
		details = append(details, c.carriedReasoningDetails...)
		details = append(details, assistant.ReasoningDetails...)
		for i := range details {
			details[i].Index = i
		}
		assistant.ReasoningDetails = details
	}
}

// firstChatMessage returns the message of a chat response's first choice, or nil.
func firstChatMessage(response *schemas.BifrostChatResponse) *schemas.ChatMessage {
	if response == nil || len(response.Choices) == 0 || response.Choices[0].ChatNonStreamResponseChoice == nil {
		return nil
	}
	return response.Choices[0].ChatNonStreamResponseChoice.Message
}

// chatMessageText returns the text a chat message content carries, as a string or text blocks.
func chatMessageText(content *schemas.ChatMessageContent) string {
	if content == nil {
		return ""
	}
	if content.ContentStr != nil {
		return *content.ContentStr
	}
	var parts []string
	for _, block := range content.ContentBlocks {
		if block.Type == schemas.ChatContentBlockTypeText && block.Text != nil && *block.Text != "" {
			parts = append(parts, *block.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// joinNonEmpty joins earlier parts and a final part with a blank line, skipping an empty final part.
func joinNonEmpty(earlier []string, last *string) string {
	parts := earlier
	if last != nil && *last != "" {
		parts = append(slices.Clip(earlier), *last)
	}
	return strings.Join(parts, "\n\n")
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
		jsonBytes, err := schemas.MarshalSorted(toolResultsMap)
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

func (r *responsesAPIAdapter) extractUsage(response interface{}) *schemas.BifrostLLMUsage {
	return response.(*schemas.BifrostResponsesResponse).Usage.ToBifrostLLMUsage()
}

func (r *responsesAPIAdapter) applyUsage(response interface{}, usage *schemas.BifrostLLMUsage) {
	response.(*schemas.BifrostResponsesResponse).Usage = usage.ToResponsesResponseUsage()
}

func (r *responsesAPIAdapter) routingInfo(response interface{}) schemas.RoutingInfo {
	return response.(*schemas.BifrostResponsesResponse).ExtraFields.RoutingInfo
}

// carryForward keeps every output item of the turn except the tool calls Bifrost executed,
// and any function_call, which the final response re-lists when the client must run it.
// Items are kept whole, so a reasoning item's encrypted_content survives byte for byte.
func (r *responsesAPIAdapter) carryForward(response interface{}, executedToolCalls []schemas.ChatAssistantMessageToolCall) {
	for _, item := range response.(*schemas.BifrostResponsesResponse).Output {
		if item.Type != nil && *item.Type == schemas.ResponsesMessageTypeFunctionCall {
			continue
		}
		if item.ResponsesToolMessage != nil && item.ResponsesToolMessage.CallID != nil && executedToolCallID(executedToolCalls, *item.ResponsesToolMessage.CallID) {
			continue
		}
		r.carriedOutput = append(r.carriedOutput, item)
	}
}

// applyCarriedContent puts earlier turns' output items ahead of the final response's own.
func (r *responsesAPIAdapter) applyCarriedContent(response interface{}) {
	if len(r.carriedOutput) == 0 {
		return
	}
	responsesResponse := response.(*schemas.BifrostResponsesResponse)
	output := make([]schemas.ResponsesMessage, 0, len(r.carriedOutput)+len(responsesResponse.Output))
	output = append(output, r.carriedOutput...)
	responsesResponse.Output = append(output, responsesResponse.Output...)
}

// executedToolCallID reports whether callID names one of the executed tool calls.
func executedToolCallID(executed []schemas.ChatAssistantMessageToolCall, callID string) bool {
	for i := range executed {
		if executed[i].ID != nil && *executed[i].ID == callID {
			return true
		}
	}
	return false
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
		ID:                   originalResponse.ID,
		Background:           originalResponse.Background,
		Conversation:         originalResponse.Conversation,
		CreatedAt:            originalResponse.CreatedAt,
		Error:                originalResponse.Error,
		Include:              originalResponse.Include,
		IncompleteDetails:    originalResponse.IncompleteDetails,
		Instructions:         originalResponse.Instructions,
		MaxOutputTokens:      originalResponse.MaxOutputTokens,
		MaxToolCalls:         originalResponse.MaxToolCalls,
		Metadata:             originalResponse.Metadata,
		ParallelToolCalls:    originalResponse.ParallelToolCalls,
		PreviousResponseID:   originalResponse.PreviousResponseID,
		Prompt:               originalResponse.Prompt,
		PromptCacheKey:       originalResponse.PromptCacheKey,
		PromptCacheRetention: originalResponse.PromptCacheRetention,
		PromptCacheOptions:   originalResponse.PromptCacheOptions,
		Reasoning:            originalResponse.Reasoning,
		SafetyIdentifier:     originalResponse.SafetyIdentifier,
		ServiceTier:          originalResponse.ServiceTier,
		StreamOptions:        originalResponse.StreamOptions,
		Store:                originalResponse.Store,
		Temperature:          originalResponse.Temperature,
		Text:                 originalResponse.Text,
		TopLogProbs:          originalResponse.TopLogProbs,
		TopP:                 originalResponse.TopP,
		ToolChoice:           originalResponse.ToolChoice,
		Tools:                originalResponse.Tools,
		Truncation:           originalResponse.Truncation,
		Usage:                originalResponse.Usage,
		ExtraFields:          originalResponse.ExtraFields,
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
		jsonBytes, err := schemas.MarshalSorted(toolResultsMap)
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

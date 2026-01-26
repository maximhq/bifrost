package mcp

import (
	"fmt"
	"strings"
	"sync"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// ExecuteAgentForChatRequest handles the agent mode execution loop for Chat API.
// It orchestrates iterative tool execution up to the maximum depth, handling
// auto-executable and non-auto-executable tools appropriately.
//
// Parameters:
//   - ctx: Context for agent execution
//   - maxAgentDepth: Maximum number of agent iterations allowed
//   - originalReq: The original chat request
//   - initialResponse: The initial chat response containing tool calls
//   - makeReq: Function to make subsequent chat requests during agent execution
//   - fetchNewRequestIDFunc: Optional function to generate unique request IDs for each iteration
//   - executeToolFunc: Function to execute individual tool calls
//   - clientManager: Client manager for accessing MCP clients and tools
//
// Returns:
//   - *schemas.BifrostChatResponse: The final response after agent execution
//   - *schemas.BifrostError: Any error that occurred during agent execution
func ExecuteAgentForChatRequest(
	ctx *schemas.BifrostContext,
	maxAgentDepth int,
	originalReq *schemas.BifrostChatRequest,
	initialResponse *schemas.BifrostChatResponse,
	makeReq func(ctx *schemas.BifrostContext, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError),
	fetchNewRequestIDFunc func(ctx *schemas.BifrostContext) string,
	executeToolFunc func(ctx *schemas.BifrostContext, toolCall schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, error),
	clientManager ClientManager,
) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	// Create adapter for Chat API
	adapter := &chatAPIAdapter{
		originalReq:     originalReq,
		initialResponse: initialResponse,
		makeReq:         makeReq,
	}

	result, err := executeAgent(ctx, maxAgentDepth, adapter, fetchNewRequestIDFunc, executeToolFunc, clientManager)
	if err != nil {
		return nil, err
	}

	chatResponse, ok := result.(*schemas.BifrostChatResponse)
	// Should never happen, but just in case
	if !ok {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: "Failed to convert result to schemas.BifrostChatResponse",
			},
		}
	}

	return chatResponse, nil
}

// ExecuteAgentForResponsesRequest handles the agent mode execution loop for Responses API.
// It orchestrates iterative tool execution up to the maximum depth, handling
// auto-executable and non-auto-executable tools appropriately.
//
// Parameters:
//   - ctx: Context for agent execution
//   - maxAgentDepth: Maximum number of agent iterations allowed
//   - originalReq: The original responses request
//   - initialResponse: The initial responses response containing tool calls
//   - makeReq: Function to make subsequent responses requests during agent execution
//   - fetchNewRequestIDFunc: Optional function to generate unique request IDs for each iteration
//   - executeToolFunc: Function to execute individual tool calls
//   - clientManager: Client manager for accessing MCP clients and tools
//
// Returns:
//   - *schemas.BifrostResponsesResponse: The final response after agent execution
//   - *schemas.BifrostError: Any error that occurred during agent execution
func ExecuteAgentForResponsesRequest(
	ctx *schemas.BifrostContext,
	maxAgentDepth int,
	originalReq *schemas.BifrostResponsesRequest,
	initialResponse *schemas.BifrostResponsesResponse,
	makeReq func(ctx *schemas.BifrostContext, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError),
	fetchNewRequestIDFunc func(ctx *schemas.BifrostContext) string,
	executeToolFunc func(ctx *schemas.BifrostContext, toolCall schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, error),
	clientManager ClientManager,
) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	// Create adapter for Responses API
	adapter := &responsesAPIAdapter{
		originalReq:     originalReq,
		initialResponse: initialResponse,
		makeReq:         makeReq,
	}

	result, err := executeAgent(ctx, maxAgentDepth, adapter, fetchNewRequestIDFunc, executeToolFunc, clientManager)
	if err != nil {
		return nil, err
	}

	responsesResponse, ok := result.(*schemas.BifrostResponsesResponse)
	// Should never happen, but just in case
	if !ok {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: "Failed to convert result to schemas.BifrostResponsesResponse",
			},
		}
	}

	return responsesResponse, nil
}

// executeAgent handles the generic agent mode execution loop using an API adapter pattern.
// It iteratively executes tools, separates auto-executable from non-auto-executable tools,
// executes auto-executable tools in parallel, and continues the loop until no more tool
// calls are present or the maximum depth is reached.
//
// Parameters:
//   - ctx: Context for agent execution (may be modified to add request IDs)
//   - maxAgentDepth: Maximum number of agent iterations allowed
//   - adapter: API adapter that abstracts differences between Chat and Responses APIs
//   - fetchNewRequestIDFunc: Optional function to generate unique request IDs for each iteration
//   - executeToolFunc: Function to execute individual tool calls
//   - clientManager: Client manager for accessing MCP clients and tools
//
// Returns:
//   - interface{}: The final response after agent execution (type depends on adapter)
//   - *schemas.BifrostError: Any error that occurred during agent execution
func executeAgent(
	ctx *schemas.BifrostContext,
	maxAgentDepth int,
	adapter agentAPIAdapter,
	fetchNewRequestIDFunc func(ctx *schemas.BifrostContext) string,
	executeToolFunc func(ctx *schemas.BifrostContext, toolCall schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, error),
	clientManager ClientManager,
) (interface{}, *schemas.BifrostError) {
	logger.Debug("Entering agent mode - detected tool calls in response")

	// Get initial response from adapter
	currentResponse := adapter.getInitialResponse()

	// Create conversation history starting with original messages
	conversationHistory := adapter.getConversationHistory()

	depth := 0

	// Track all executed tool results and tool calls across all iterations
	allExecutedToolResults := make([]*schemas.ChatMessage, 0)
	allExecutedToolCalls := make([]schemas.ChatAssistantMessageToolCall, 0)

	originalRequestID, ok := ctx.Value(schemas.BifrostContextKeyRequestID).(string)
	if ok {
		ctx.SetValue(schemas.BifrostMCPAgentOriginalRequestID, originalRequestID)
	}

	for depth < maxAgentDepth {
		depth++
		toolCalls := adapter.extractToolCalls(currentResponse)
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
			client := clientManager.GetClientForTool(toolName)
			if client == nil {
				// Allow code mode list and read tool tools
				if toolName == ToolTypeListToolFiles || toolName == ToolTypeReadToolFile {
					autoExecutableTools = append(autoExecutableTools, toolCall)
					logger.Debug(fmt.Sprintf("Tool %s can be auto-executed", toolName))
					continue
				} else if toolName == ToolTypeExecuteToolCode {
					// Build allowed auto-execution tools map for code mode validation
					allClientNames, allowedAutoExecutionTools := buildAllowedAutoExecutionTools(ctx, clientManager)

					// Parse tool arguments
					var arguments map[string]interface{}
					if err := sonic.Unmarshal([]byte(toolCall.Function.Arguments), &arguments); err != nil {
						logger.Debug(fmt.Sprintf("%s Failed to parse tool arguments: %v", CodeModeLogPrefix, err))
						nonAutoExecutableTools = append(nonAutoExecutableTools, toolCall)
						continue
					}

					code, ok := arguments["code"].(string)
					if !ok || code == "" {
						logger.Debug(fmt.Sprintf("%s Code parameter missing or empty", CodeModeLogPrefix))
						nonAutoExecutableTools = append(nonAutoExecutableTools, toolCall)
						continue
					}

					// Step 1: Convert literal \n escape sequences to actual newlines for parsing
					codeWithNewlines := strings.ReplaceAll(code, "\\n", "\n")
					if len(codeWithNewlines) != len(code) {
						logger.Debug(fmt.Sprintf("%s Converted literal \\n escape sequences to actual newlines", CodeModeLogPrefix))
					}

					// Step 2: Extract tool calls from code during AST formation
					extractedToolCalls, err := extractToolCallsFromCode(codeWithNewlines)
					if err != nil {
						logger.Debug(fmt.Sprintf("%s Failed to parse code for tool calls: %v", CodeModeLogPrefix, err))
						nonAutoExecutableTools = append(nonAutoExecutableTools, toolCall)
						continue
					}

					logger.Debug(fmt.Sprintf("%s Extracted %d tool call(s) from code", CodeModeLogPrefix, len(extractedToolCalls)))

					// Step 3: Validate all tool calls against allowedAutoExecutionTools
					canAutoExecute := true
					if len(extractedToolCalls) > 0 {
						// If there are tool calls, we need allowedAutoExecutionTools to validate them
						if len(allowedAutoExecutionTools) == 0 {
							logger.Debug(fmt.Sprintf("%s Validation failed: no allowed auto-execution tools configured", CodeModeLogPrefix))
							canAutoExecute = false
						} else {
							logger.Debug(fmt.Sprintf("%s Validating %d tool call(s) against %d allowed server(s)", CodeModeLogPrefix, len(extractedToolCalls), len(allowedAutoExecutionTools)))

							// Validate each tool call
							for _, extractedToolCall := range extractedToolCalls {
								isAllowed := isToolCallAllowedForCodeMode(extractedToolCall.serverName, extractedToolCall.toolName, allClientNames, allowedAutoExecutionTools)
								if !isAllowed {
									logger.Debug(fmt.Sprintf("%s Tool call %s.%s: allowed=%v", CodeModeLogPrefix, extractedToolCall.serverName, extractedToolCall.toolName, isAllowed))
									logger.Debug(fmt.Sprintf("%s Validation failed: tool call %s.%s not in auto-execute list", CodeModeLogPrefix, extractedToolCall.serverName, extractedToolCall.toolName))
									canAutoExecute = false
									break
								}
							}
							if canAutoExecute {
								logger.Debug(fmt.Sprintf("%s All tool calls validated successfully", CodeModeLogPrefix))
							}
						}
					} else {
						logger.Debug(fmt.Sprintf("%s No tool calls found in code, skipping validation", CodeModeLogPrefix))
					}

					// Add to appropriate list based on validation result
					if canAutoExecute {
						autoExecutableTools = append(autoExecutableTools, toolCall)
						logger.Debug(fmt.Sprintf("Tool %s can be auto-executed (validation passed)", toolName))
					} else {
						nonAutoExecutableTools = append(nonAutoExecutableTools, toolCall)
						logger.Debug(fmt.Sprintf("Tool %s cannot be auto-executed (validation failed)", toolName))
					}
					continue
				}
				// Else, if client not found, treat as non-auto-executable (can be a manually passed tool)
				logger.Debug(fmt.Sprintf("Client not found for tool %s, treating as non-auto-executable", toolName))
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

		logger.Debug(fmt.Sprintf("Auto-executable tools: %d", len(autoExecutableTools)))
		logger.Debug(fmt.Sprintf("Non-auto-executable tools: %d", len(nonAutoExecutableTools)))

		// Execute auto-executable tools first
		var executedToolResults []*schemas.ChatMessage
		if len(autoExecutableTools) > 0 {
			// Add assistant message with auto-executable tool calls to conversation
			conversationHistory = adapter.addAssistantMessage(conversationHistory, currentResponse)

			// Execute all auto-executable tool calls parallelly
			wg := sync.WaitGroup{}
			wg.Add(len(autoExecutableTools))
			channelToolResults := make(chan *schemas.ChatMessage, len(autoExecutableTools))
			for _, toolCall := range autoExecutableTools {
				go func(toolCall schemas.ChatAssistantMessageToolCall) {
					defer wg.Done()
					toolResult, toolErr := executeToolFunc(ctx, toolCall)
					if toolErr != nil {
						logger.Warn("Tool execution failed: %v", toolErr)
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
			conversationHistory = adapter.addToolResults(conversationHistory, executedToolResults)
		}

		// If there are non-auto-executable tools, return them immediately without continuing the loop
		if len(nonAutoExecutableTools) > 0 {
			logger.Debug(fmt.Sprintf("Found %d non-auto-executable tools, returning them immediately without continuing the loop", len(nonAutoExecutableTools)))
			// Return as is if its the first iteration
			if depth == 1 && len(allExecutedToolResults) == 0 {
				return currentResponse, nil
			}
			// Create response with all executed tool results from all iterations, and non-auto-executable tool calls
			return adapter.createResponseWithExecutedTools(currentResponse, allExecutedToolResults, allExecutedToolCalls, nonAutoExecutableTools), nil
		}

		// Create new request with updated conversation history
		newReq := adapter.createNewRequest(conversationHistory)

		if fetchNewRequestIDFunc != nil {
			newID := fetchNewRequestIDFunc(ctx)
			if newID != "" {
				ctx.SetValue(schemas.BifrostContextKeyRequestID, newID)
			}
		}

		// Make new LLM request
		response, err := adapter.makeLLMCall(ctx, newReq)
		if err != nil {
			logger.Error("Agent mode: LLM request failed: %v", err)
			return nil, err
		}

		currentResponse = response
	}

	logger.Debug(fmt.Sprintf("Agent mode completed after %d iterations", depth))
	return currentResponse, nil
}

// extractToolCalls extracts all tool calls from a chat response.
// It iterates through all choices in the response and collects tool calls
// from assistant messages.
//
// Parameters:
//   - response: The chat response to extract tool calls from
//
// Returns:
//   - []schemas.ChatAssistantMessageToolCall: List of extracted tool calls, or nil if none found
func extractToolCalls(response *schemas.BifrostChatResponse) []schemas.ChatAssistantMessageToolCall {
	if !hasToolCallsForChatResponse(response) {
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

// createToolResultMessage creates a tool result message from tool execution.
// It formats the result or error into a chat message with the appropriate tool call ID.
//
// Parameters:
//   - toolCall: The original tool call that was executed
//   - result: The successful execution result (ignored if err is not nil)
//   - err: Any error that occurred during tool execution
//
// Returns:
//   - *schemas.ChatMessage: A tool message containing the execution result or error
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

// buildAllowedAutoExecutionTools builds a map of client names to their auto-executable tools.
// It processes code mode clients and parses their ToolsToAutoExecute configuration to create
// a map of allowed tools. Tool names are parsed to match their appearance in JavaScript code.
//
// Parameters:
//   - ctx: Context for accessing client tools
//   - clientManager: Client manager for accessing MCP clients
//
// Returns:
//   - []string: List of all client names
//   - map[string][]string: Map of client names to their auto-executable tool names (as they appear in code)
func buildAllowedAutoExecutionTools(ctx *schemas.BifrostContext, clientManager ClientManager) ([]string, map[string][]string) {
	allowedTools := make(map[string][]string)
	availableToolsPerClient := clientManager.GetToolPerClient(ctx)
	allClientNames := []string{}

	for clientName := range availableToolsPerClient {
		client := clientManager.GetClientByName(clientName)
		if client == nil {
			continue
		}
		allClientNames = append(allClientNames, clientName)

		// Only include code mode clients
		if !client.ExecutionConfig.IsCodeModeClient {
			continue
		}

		// Get auto-executable tools from config
		toolsToAutoExecute := client.ExecutionConfig.ToolsToAutoExecute
		if len(toolsToAutoExecute) == 0 {
			// No auto-executable tools configured for this client
			continue
		}

		// Parse tool names (as they appear in JavaScript code)
		autoExecutableTools := []string{}
		for _, originalToolName := range toolsToAutoExecute {
			// Handle wildcard "*" - means all tools are auto-executable
			if originalToolName == "*" {
				autoExecutableTools = append(autoExecutableTools, "*")
				continue
			}
			// Use parsed tool name (as it appears in code)
			parsedToolName := parseToolName(originalToolName)
			autoExecutableTools = append(autoExecutableTools, parsedToolName)
		}

		// Add to map if there are auto-executable tools
		if len(autoExecutableTools) > 0 {
			allowedTools[clientName] = autoExecutableTools
		}
	}

	return allClientNames, allowedTools
}

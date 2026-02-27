//go:build !tinygo && !wasm

package starlark

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/mark3labs/mcp-go/mcp"

	codemcp "github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
)

// ExecutionResult represents the result of code execution
type ExecutionResult struct {
	Result      interface{}          `json:"result"`
	Logs        []string             `json:"logs"`
	Errors      *ExecutionError      `json:"errors,omitempty"`
	Environment ExecutionEnvironment `json:"environment"`
}

// ExecutionErrorType represents the type of execution error
type ExecutionErrorType string

const (
	ExecutionErrorTypeCompile ExecutionErrorType = "compile"
	ExecutionErrorTypeSyntax  ExecutionErrorType = "syntax"
	ExecutionErrorTypeRuntime ExecutionErrorType = "runtime"
)

// ExecutionError represents an error during code execution
type ExecutionError struct {
	Kind    ExecutionErrorType `json:"kind"` // "compile", "syntax", or "runtime"
	Message string             `json:"message"`
	Hints   []string           `json:"hints"`
}

// ExecutionEnvironment contains information about the execution environment
type ExecutionEnvironment struct {
	ServerKeys []string `json:"serverKeys"`
}

// createExecuteToolCodeTool creates the executeToolCode tool definition for code mode.
// This tool allows executing Python (Starlark) code in a sandboxed interpreter with access to MCP server tools.
func (s *StarlarkCodeMode) createExecuteToolCodeTool() schemas.ChatTool {
	executeToolCodeProps := schemas.NewOrderedMapFromPairs(
		schemas.KV("code", map[string]interface{}{
			"type":        "string",
			"description": "Python code to execute. The code runs in a Starlark interpreter (Python subset). Tool calls are synchronous - no async/await needed. For loops/conditionals, wrap in a function. Use print() for logging. ALWAYS retry if code fails. Example: def main():\n  items = server.list_items()\n  for item in items:\n    print(item)\nresult = main()",
		}),
	)
	return schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name: codemcp.ToolTypeExecuteToolCode,
			Description: schemas.Ptr(
				"Executes Python code inside a sandboxed Starlark interpreter with access to all connected MCP servers' tools. " +
					"All connected servers are exposed as global objects named after their configuration keys, and each server " +
					"provides functions for every tool available on that server. The canonical usage pattern is: " +
					"result = <serverName>.<toolName>(param=\"value\"). Both <serverName> and <toolName> should be discovered " +
					"using listToolFiles and readToolFile. " +

					"IMPORTANT WORKFLOW: Always follow this order — first use listToolFiles to see available servers and tools, " +
					"then use readToolFile to understand the tool definitions and their parameters, and finally use executeToolCode " +
					"to execute your code. " +

					"SYNTAX NOTES: " +
					"• Tool calls are synchronous - NO async/await needed, just call directly: result = server.tool(arg=\"value\") " +
					"• Use keyword arguments: server.tool(param=\"value\") NOT server.tool({\"param\": \"value\"}) " +
					"• Access dict values with brackets: result[\"key\"] NOT result.key " +
					"• Use print() for logging (not console.log) " +
					"• List comprehensions work: [x for x in items if x[\"active\"]] " +
					"• To return a value, assign to 'result' variable: result = computed_value " +
					"• CRITICAL: for/if/while at top level MUST be inside a function - def main(): ... then result = main() " +

					"RETRY POLICY: ALWAYS retry if a code block fails. Analyze the error, adjust your code, and retry. " +

					"The environment is intentionally minimal: " +
					"• No imports needed or supported " +
					"• No network APIs (use MCP tools for external interactions) " +
					"• No file system access (use MCP tools) " +
					"• No classes (use dicts and functions) " +
					"• Deterministic execution (no random, no time) " +

					"Long-running operations are interrupted via execution timeout. " +
					"This tool is designed specifically for orchestrating MCP tool calls and lightweight computation.",
			),

			Parameters: &schemas.ToolFunctionParameters{
				Type:       "object",
				Properties: executeToolCodeProps,
				Required:   []string{"code"},
			},
		},
	}
}

// handleExecuteToolCode handles the executeToolCode tool call.
func (s *StarlarkCodeMode) handleExecuteToolCode(ctx context.Context, toolCall schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, error) {
	toolName := "unknown"
	if toolCall.Function.Name != nil {
		toolName = *toolCall.Function.Name
	}
	s.logger.Debug("%s Handling executeToolCode tool call: %s", codemcp.CodeModeLogPrefix, toolName)

	// Parse tool arguments
	var arguments map[string]interface{}
	if err := sonic.Unmarshal([]byte(toolCall.Function.Arguments), &arguments); err != nil {
		s.logger.Debug("%s Failed to parse tool arguments: %v", codemcp.CodeModeLogPrefix, err)
		return nil, fmt.Errorf("failed to parse tool arguments: %v", err)
	}

	code, ok := arguments["code"].(string)
	if !ok || code == "" {
		s.logger.Debug("%s Code parameter missing or empty", codemcp.CodeModeLogPrefix)
		return nil, fmt.Errorf("code parameter is required and must be a non-empty string")
	}

	s.logger.Debug("%s Starting code execution", codemcp.CodeModeLogPrefix)
	result := s.executeCode(ctx, code)
	s.logger.Debug("%s Code execution completed. Success: %v, Has errors: %v, Log count: %d", codemcp.CodeModeLogPrefix, result.Errors == nil, result.Errors != nil, len(result.Logs))

	// Format response text
	var responseText string
	var executionSuccess bool = true
	if result.Errors != nil {
		s.logger.Debug("%s Formatting error response. Error kind: %s, Message length: %d, Hints count: %d", codemcp.CodeModeLogPrefix, result.Errors.Kind, len(result.Errors.Message), len(result.Errors.Hints))
		logsText := ""
		if len(result.Logs) > 0 {
			logsText = fmt.Sprintf("\n\nPrint Output:\n%s\n", strings.Join(result.Logs, "\n"))
		}

		responseText = fmt.Sprintf(
			"Execution %s error:\n\n%s\n\nHints:\n%s%s\n\nEnvironment:\n  Available server keys: %s",
			result.Errors.Kind,
			result.Errors.Message,
			strings.Join(result.Errors.Hints, "\n"),
			logsText,
			strings.Join(result.Environment.ServerKeys, ", "),
		)
		s.logger.Debug("%s Error response formatted. Response length: %d chars", codemcp.CodeModeLogPrefix, len(responseText))
	} else {
		hasLogs := len(result.Logs) > 0
		hasResult := result.Result != nil
		s.logger.Debug("%s Formatting success response. Has logs: %v, Has result: %v", codemcp.CodeModeLogPrefix, hasLogs, hasResult)

		if !hasLogs && !hasResult {
			executionSuccess = false
			s.logger.Debug("%s Execution completed with no data (no logs, no result), marking as failure", codemcp.CodeModeLogPrefix)
			hints := []string{
				"Add print() statements throughout your code to debug and see what's happening at each step",
				"Assign the final value to 'result' variable if you want to return it: result = computed_value",
				"Check that your tool calls are actually executing and returning data",
			}
			responseText = fmt.Sprintf(
				"Execution completed but produced no data:\n\n"+
					"The code executed without errors but returned no output (no print output and no result variable).\n\n"+
					"Hints:\n%s\n\n"+
					"Environment:\n  Available server keys: %s",
				strings.Join(hints, "\n"),
				strings.Join(result.Environment.ServerKeys, ", "),
			)
			s.logger.Debug("%s No-data failure response formatted. Response length: %d chars", codemcp.CodeModeLogPrefix, len(responseText))
		} else {
			if hasLogs {
				responseText = fmt.Sprintf("Print output:\n%s\n\nExecution completed successfully.",
					strings.Join(result.Logs, "\n"))
			} else {
				responseText = "Execution completed successfully."
			}
			if hasResult {
				resultJSON, err := sonic.MarshalIndent(result.Result, "", "  ")
				if err == nil {
					responseText += fmt.Sprintf("\nReturn value: %s", string(resultJSON))
					s.logger.Debug("%s Added return value to response (JSON length: %d chars)", codemcp.CodeModeLogPrefix, len(resultJSON))
				} else {
					s.logger.Debug("%s Failed to marshal result to JSON: %v", codemcp.CodeModeLogPrefix, err)
				}
			}

			responseText += fmt.Sprintf("\n\nEnvironment:\n  Available server keys: %s",
				strings.Join(result.Environment.ServerKeys, ", "))
			responseText += "\nNote: This is a Starlark (Python subset) environment. Use MCP tools for external interactions."
			s.logger.Debug("%s Success response formatted. Response length: %d chars, Server keys: %v", codemcp.CodeModeLogPrefix, len(responseText), result.Environment.ServerKeys)
		}
	}

	s.logger.Debug("%s Returning tool response message. Execution success: %v", codemcp.CodeModeLogPrefix, executionSuccess)
	return createToolResponseMessage(toolCall, responseText), nil
}

// executeCode executes Python (Starlark) code in a sandboxed interpreter with MCP tool bindings.
func (s *StarlarkCodeMode) executeCode(ctx context.Context, code string) ExecutionResult {
	logs := []string{}

	s.logger.Debug("%s Starting Starlark code execution", codemcp.CodeModeLogPrefix)

	// Step 1: Convert literal \n escape sequences to actual newlines
	codeWithNewlines := strings.ReplaceAll(code, "\\n", "\n")

	// Step 2: Handle empty code
	trimmedCode := strings.TrimSpace(codeWithNewlines)
	if trimmedCode == "" {
		return ExecutionResult{
			Result: nil,
			Logs:   logs,
			Errors: nil,
			Environment: ExecutionEnvironment{
				ServerKeys: []string{},
			},
		}
	}

	// Step 3: Build tool bindings for all connected servers
	availableToolsPerClient := s.clientManager.GetToolPerClient(ctx)
	serverKeys := make([]string, 0, len(availableToolsPerClient))
	predeclared := starlark.StringDict{}

	// Thread-safe log appender
	appendLog := func(msg string) {
		s.logMu.Lock()
		defer s.logMu.Unlock()
		logs = append(logs, msg)
	}

	s.logger.Debug("%s GetToolPerClient returned %d clients", codemcp.CodeModeLogPrefix, len(availableToolsPerClient))

	for clientName, tools := range availableToolsPerClient {
		client := s.clientManager.GetClientByName(clientName)
		if client == nil {
			s.logger.Warn("%s Client %s not found, skipping", codemcp.CodeModeLogPrefix, clientName)
			continue
		}
		s.logger.Debug("%s [%s] Client found. IsCodeModeClient: %v, ToolCount: %d", codemcp.CodeModeLogPrefix, clientName, client.ExecutionConfig.IsCodeModeClient, len(tools))
		if !client.ExecutionConfig.IsCodeModeClient || len(tools) == 0 {
			s.logger.Debug("%s [%s] Skipped: IsCodeModeClient=%v, HasTools=%v", codemcp.CodeModeLogPrefix, clientName, client.ExecutionConfig.IsCodeModeClient, len(tools) > 0)
			continue
		}
		serverKeys = append(serverKeys, clientName)

		// Build struct with tool methods
		structMembers := starlark.StringDict{}

		for _, tool := range tools {
			if tool.Function == nil || tool.Function.Name == "" {
				continue
			}

			originalToolName := tool.Function.Name
			unprefixedToolName := stripClientPrefix(originalToolName, clientName)
			unprefixedToolName = strings.ReplaceAll(unprefixedToolName, "-", "_")
			parsedToolName := parseToolName(unprefixedToolName)

			s.logger.Debug("%s [%s] Binding tool: %s -> %s", codemcp.CodeModeLogPrefix, clientName, originalToolName, parsedToolName)

			// Capture variables for closure
			capturedToolName := originalToolName
			capturedClientName := clientName

			// Create a Starlark builtin function for this tool
			toolFunc := starlark.NewBuiltin(parsedToolName, func(thread *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
				// Convert kwargs to Go map
				goArgs := make(map[string]interface{})
				for _, kwarg := range kwargs {
					if len(kwarg) == 2 {
						key := string(kwarg[0].(starlark.String))
						value := starlarkToGo(kwarg[1])
						goArgs[key] = value
					}
				}

				// Also handle positional args if there's exactly one dict argument
				if len(args) == 1 && len(kwargs) == 0 {
					if dict, ok := args[0].(*starlark.Dict); ok {
						for _, item := range dict.Items() {
							if keyStr, ok := item[0].(starlark.String); ok {
								goArgs[string(keyStr)] = starlarkToGo(item[1])
							}
						}
					}
				}

				// Call the MCP tool
				result, err := s.callMCPTool(ctx, capturedClientName, capturedToolName, goArgs, appendLog)
				if err != nil {
					return starlark.None, fmt.Errorf("tool call failed: %v", err)
				}

				// Convert result back to Starlark
				return goToStarlark(result), nil
			})

			structMembers[parsedToolName] = toolFunc
		}

		// Create a struct for this server
		serverStruct := starlarkstruct.FromStringDict(starlark.String(clientName), structMembers)
		predeclared[clientName] = serverStruct
		s.logger.Debug("%s [%s] Added server struct with %d tools", codemcp.CodeModeLogPrefix, clientName, len(structMembers))
	}

	if len(serverKeys) > 0 {
		s.logger.Debug("%s Bound %d servers with tools: %v", codemcp.CodeModeLogPrefix, len(serverKeys), serverKeys)
	} else {
		s.logger.Debug("%s No servers available for code mode execution", codemcp.CodeModeLogPrefix)
	}

	// Step 4: Create Starlark thread with print function and timeout
	toolExecutionTimeout := s.getToolExecutionTimeout()
	timeoutCtx, cancel := context.WithTimeout(ctx, toolExecutionTimeout)
	defer cancel()

	thread := &starlark.Thread{
		Name: "codemode",
		Print: func(_ *starlark.Thread, msg string) {
			appendLog(msg)
		},
	}

	// Set up cancellation check
	thread.SetLocal("context", timeoutCtx)

	// Step 5: Execute the code
	globals, err := starlark.ExecFile(thread, "code.star", trimmedCode, predeclared)

	if err != nil {
		errorMessage := err.Error()
		hints := generatePythonErrorHints(errorMessage, serverKeys)
		s.logger.Debug("%s Execution failed: %s", codemcp.CodeModeLogPrefix, errorMessage)

		errorKind := ExecutionErrorTypeRuntime
		if strings.Contains(errorMessage, "syntax error") {
			errorKind = ExecutionErrorTypeSyntax
		}

		return ExecutionResult{
			Result: nil,
			Logs:   logs,
			Errors: &ExecutionError{
				Kind:    errorKind,
				Message: errorMessage,
				Hints:   hints,
			},
			Environment: ExecutionEnvironment{
				ServerKeys: serverKeys,
			},
		}
	}

	// Step 6: Extract result from globals
	var result interface{}
	if resultVal, ok := globals["result"]; ok && resultVal != starlark.None {
		result = starlarkToGo(resultVal)
	}

	s.logger.Debug("%s Execution completed successfully", codemcp.CodeModeLogPrefix)
	return ExecutionResult{
		Result: result,
		Logs:   logs,
		Errors: nil,
		Environment: ExecutionEnvironment{
			ServerKeys: serverKeys,
		},
	}
}

// callMCPTool calls an MCP tool and returns the result.
func (s *StarlarkCodeMode) callMCPTool(ctx context.Context, clientName, toolName string, args map[string]interface{}, appendLog func(string)) (interface{}, error) {
	// Get available tools per client
	availableToolsPerClient := s.clientManager.GetToolPerClient(ctx)

	// Find the client by name
	tools, exists := availableToolsPerClient[clientName]
	if !exists || len(tools) == 0 {
		return nil, fmt.Errorf("client not found for server name: %s", clientName)
	}

	// Get client using a tool from this client
	var client *schemas.MCPClientState
	for _, tool := range tools {
		if tool.Function != nil && tool.Function.Name != "" {
			client = s.clientManager.GetClientForTool(tool.Function.Name)
			if client != nil {
				break
			}
		}
	}

	if client == nil {
		return nil, fmt.Errorf("client not found for server name: %s", clientName)
	}

	// Strip the client name prefix from tool name before calling MCP server
	originalToolName := stripClientPrefix(toolName, clientName)

	// Get BifrostContext for plugin pipeline
	var bifrostCtx *schemas.BifrostContext
	var ok bool
	if bifrostCtx, ok = ctx.(*schemas.BifrostContext); !ok {
		return s.callMCPToolDirect(ctx, client, originalToolName, clientName, toolName, args, appendLog)
	}

	originalRequestID, _ := bifrostCtx.Value(schemas.BifrostContextKeyRequestID).(string)

	// Generate new request ID for this nested tool call
	var newRequestID string
	if s.fetchNewRequestIDFunc != nil {
		newRequestID = s.fetchNewRequestIDFunc(bifrostCtx)
	} else {
		newRequestID = fmt.Sprintf("exec_%d_%s", time.Now().UnixNano(), toolName)
	}

	// Create new child context
	deadline, hasDeadline := bifrostCtx.Deadline()
	if !hasDeadline {
		deadline = schemas.NoDeadline
	}
	nestedCtx := schemas.NewBifrostContext(bifrostCtx, deadline)
	nestedCtx.SetValue(schemas.BifrostContextKeyRequestID, newRequestID)
	if originalRequestID != "" {
		nestedCtx.SetValue(schemas.BifrostContextKeyParentMCPRequestID, originalRequestID)
	}

	// Marshal arguments to JSON for the tool call
	argsJSON, err := sonic.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal tool arguments: %v", err)
	}

	// Build tool call for MCP request
	toolCallReq := schemas.ChatAssistantMessageToolCall{
		ID: schemas.Ptr(newRequestID),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr(toolName),
			Arguments: string(argsJSON),
		},
	}

	// Create BifrostMCPRequest
	mcpRequest := &schemas.BifrostMCPRequest{
		RequestType:                  schemas.MCPRequestTypeChatToolCall,
		ChatAssistantMessageToolCall: &toolCallReq,
	}

	// Check if plugin pipeline is available
	if s.pluginPipelineProvider == nil {
		return s.callMCPToolDirect(ctx, client, originalToolName, clientName, toolName, args, appendLog)
	}

	// Get plugin pipeline and run hooks
	pipeline := s.pluginPipelineProvider()
	if pipeline == nil {
		return s.callMCPToolDirect(ctx, client, originalToolName, clientName, toolName, args, appendLog)
	}
	defer s.releasePluginPipeline(pipeline)

	// Run PreMCPHooks
	preReq, shortCircuit, preCount := pipeline.RunMCPPreHooks(nestedCtx, mcpRequest)

	// Handle short-circuit cases
	if shortCircuit != nil {
		if shortCircuit.Response != nil {
			finalResp, _ := pipeline.RunMCPPostHooks(nestedCtx, shortCircuit.Response, nil, preCount)
			if finalResp != nil {
				if finalResp.ChatMessage != nil {
					return extractResultFromChatMessage(finalResp.ChatMessage), nil
				}
				if finalResp.ResponsesMessage != nil {
					result, err := extractResultFromResponsesMessage(finalResp.ResponsesMessage)
					if err != nil {
						return nil, err
					}
					if result != nil {
						return result, nil
					}
				}
			}
			return nil, fmt.Errorf("plugin short-circuit returned invalid response")
		}
		if shortCircuit.Error != nil {
			pipeline.RunMCPPostHooks(nestedCtx, nil, shortCircuit.Error, preCount)
			if shortCircuit.Error.Error != nil {
				return nil, fmt.Errorf("%s", shortCircuit.Error.Error.Message)
			}
			return nil, fmt.Errorf("plugin short-circuit error")
		}
	}

	// If pre-hooks modified the request, extract updated args
	if preReq != nil && preReq.ChatAssistantMessageToolCall != nil {
		toolCallReq = *preReq.ChatAssistantMessageToolCall
		if toolCallReq.Function.Arguments != "" {
			if err := sonic.Unmarshal([]byte(toolCallReq.Function.Arguments), &args); err != nil {
				s.logger.Warn("%s Failed to parse modified tool arguments, using original: %v", codemcp.CodeModeLogPrefix, err)
			}
		}
	}

	// Execute tool
	startTime := time.Now()
	toolNameToCall := originalToolName

	callRequest := mcp.CallToolRequest{
		Request: mcp.Request{
			Method: string(mcp.MethodToolsCall),
		},
		Params: mcp.CallToolParams{
			Name:      toolNameToCall,
			Arguments: args,
		},
	}

	toolExecutionTimeout := s.getToolExecutionTimeout()
	toolCtx, cancel := context.WithTimeout(nestedCtx, toolExecutionTimeout)
	defer cancel()

	toolResponse, callErr := client.Conn.CallTool(toolCtx, callRequest)
	latency := time.Since(startTime).Milliseconds()

	var mcpResp *schemas.BifrostMCPResponse
	var bifrostErr *schemas.BifrostError

	if callErr != nil {
		s.logger.Debug("%s Tool call failed: %s.%s - %v", codemcp.CodeModeLogPrefix, clientName, toolName, callErr)
		appendLog(fmt.Sprintf("[TOOL] %s.%s error: %v", clientName, toolName, callErr))
		bifrostErr = &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: fmt.Sprintf("tool call failed for %s.%s: %v", clientName, toolName, callErr),
			},
		}
	} else {
		rawResult := extractTextFromMCPResponse(toolResponse, toolName)

		if after, ok := strings.CutPrefix(rawResult, "Error: "); ok {
			errorMsg := after
			s.logger.Debug("%s Tool returned error result: %s.%s - %s", codemcp.CodeModeLogPrefix, clientName, toolName, errorMsg)
			appendLog(fmt.Sprintf("[TOOL] %s.%s error result: %s", clientName, toolName, errorMsg))
			bifrostErr = &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Message: errorMsg,
				},
			}
		} else {
			mcpResp = &schemas.BifrostMCPResponse{
				ChatMessage: createToolResponseMessage(toolCallReq, rawResult),
				ExtraFields: schemas.BifrostMCPResponseExtraFields{
					ClientName: clientName,
					ToolName:   originalToolName,
					Latency:    latency,
				},
			}

			resultStr := formatResultForLog(rawResult)
			logToolName := stripClientPrefix(toolName, clientName)
			logToolName = strings.ReplaceAll(logToolName, "-", "_")
			appendLog(fmt.Sprintf("[TOOL] %s.%s raw response: %s", clientName, logToolName, resultStr))
		}
	}

	// Run post-hooks
	finalResp, finalErr := pipeline.RunMCPPostHooks(nestedCtx, mcpResp, bifrostErr, preCount)

	if finalErr != nil {
		if finalErr.Error != nil {
			return nil, fmt.Errorf("%s", finalErr.Error.Message)
		}
		return nil, fmt.Errorf("tool execution failed")
	}

	if finalResp == nil {
		return nil, fmt.Errorf("plugin post-hooks returned invalid response")
	}

	if finalResp.ChatMessage != nil {
		return extractResultFromChatMessage(finalResp.ChatMessage), nil
	}

	if finalResp.ResponsesMessage != nil {
		result, err := extractResultFromResponsesMessage(finalResp.ResponsesMessage)
		if err != nil {
			return nil, err
		}
		if result != nil {
			return result, nil
		}
	}

	return nil, fmt.Errorf("plugin post-hooks returned invalid response")
}

// callMCPToolDirect executes an MCP tool call directly without plugin hooks.
func (s *StarlarkCodeMode) callMCPToolDirect(ctx context.Context, client *schemas.MCPClientState, originalToolName, clientName, toolName string, args map[string]interface{}, appendLog func(string)) (interface{}, error) {
	callRequest := mcp.CallToolRequest{
		Request: mcp.Request{
			Method: string(mcp.MethodToolsCall),
		},
		Params: mcp.CallToolParams{
			Name:      originalToolName,
			Arguments: args,
		},
	}

	toolExecutionTimeout := s.getToolExecutionTimeout()
	toolCtx, cancel := context.WithTimeout(ctx, toolExecutionTimeout)
	defer cancel()

	logToolName := stripClientPrefix(toolName, clientName)
	logToolName = strings.ReplaceAll(logToolName, "-", "_")

	toolResponse, callErr := client.Conn.CallTool(toolCtx, callRequest)
	if callErr != nil {
		s.logger.Debug("%s Tool call failed: %s.%s - %v", codemcp.CodeModeLogPrefix, clientName, logToolName, callErr)
		appendLog(fmt.Sprintf("[TOOL] %s.%s error: %v", clientName, logToolName, callErr))
		return nil, fmt.Errorf("tool call failed for %s.%s: %v", clientName, logToolName, callErr)
	}

	rawResult := extractTextFromMCPResponse(toolResponse, toolName)

	if after, ok := strings.CutPrefix(rawResult, "Error: "); ok {
		errorMsg := after
		s.logger.Debug("%s Tool returned error result: %s.%s - %s", codemcp.CodeModeLogPrefix, clientName, logToolName, errorMsg)
		appendLog(fmt.Sprintf("[TOOL] %s.%s error result: %s", clientName, logToolName, errorMsg))
		return nil, fmt.Errorf("%s", errorMsg)
	}

	var finalResult interface{}
	if err := sonic.Unmarshal([]byte(rawResult), &finalResult); err != nil {
		finalResult = rawResult
	}

	resultStr := formatResultForLog(finalResult)
	appendLog(fmt.Sprintf("[TOOL] %s.%s raw response: %s", clientName, logToolName, resultStr))

	return finalResult, nil
}

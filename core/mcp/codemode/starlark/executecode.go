//go:build !tinygo && !wasm

package starlark

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/canonical/starlark/starlark"
	"github.com/mark3labs/mcp-go/mcp"

	codemcp "github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/schemas"
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
			"type": "string",
			"description": "Python (Starlark) code to execute. Tool calls are synchronous: result = server.tool(param=\"value\"). " +
				"Use print() for logging. Assign to 'result' variable to return a value. " +
				"Retry after fixing syntax or logic errors, especially for read-only flows. Before rerunning code that already made tool calls, inspect prior outputs and avoid replaying stateful operations. " +
				"Example: items = server.list_items()\nfor item in items:\n    print(item[\"name\"])\nresult = items",
		}),
	)
	return schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name: codemcp.ToolTypeExecuteToolCode,
			Description: schemas.Ptr(
				"Executes Python code in a sandboxed Starlark interpreter with MCP server tool access. " +
					"Servers are exposed as global objects: result = serverName.toolName(param=\"value\"). " +
					"This is the final step of the four-tool code mode workflow: listToolFiles -> readToolFile -> (optional) getToolDocs -> executeToolCode. " +
					"If you have not already read a tool's .pyi stub in this conversation, do that before writing code. " +
					"Do NOT guess callable tool names from natural language or stale assumptions; use the exact identifier returned by listToolFiles/readToolFile. " +

					"STARLARK DIFFERENCES FROM PYTHON — READ BEFORE WRITING CODE: " +
					"1. NO try/except/finally/raise — error handling is not supported, and tool failures cannot be caught inside Starlark. " +
					"2. NO classes — use dicts and functions. " +
					"3. NO imports, direct network access, or direct filesystem access — use MCP tools instead. " +
					"4. NO is operator — use == for comparison. " +
					"5. NO f-strings — use % formatting: \"Hello %s, count=%d\" % (name, n). " +
					"6. Each executeToolCode call runs in a FRESH ISOLATED SCOPE — no variables, functions, or state persist between calls. Re-fetch data or store it via MCP tools (e.g., SQLite, FileSystem) if needed across calls. " +

					"SYNTAX NOTES: " +
					"• Synchronous calls — NO async/await: result = server.tool(arg=\"value\") " +
					"• Use keyword arguments: server.tool(param=\"value\") NOT server.tool({\"param\": \"value\"}) " +
					"• Access dict values with brackets: result[\"key\"] NOT result.key " +
					"• Use print() for logging/debugging " +
					"• List comprehensions: [x for x in items if x[\"active\"]] " +
					"• String escapes work normally: \"line1\\nline2\" produces a newline " +
					"• Triple-quoted strings for multiline: \"\"\"multi\\nline\"\"\" " +
					"• chr(10) for newline character, chr(9) for tab " +
					"• To return a value, assign to 'result': result = computed_value " +
					"• MCP tool calls are timeout-limited; avoid long or infinite loops " +

					"AVAILABLE BUILTINS: print, len, range, enumerate, zip, sorted, reversed, min, max, " +
					"int, float, str, bool, list, dict, tuple, set, hasattr, getattr, type, chr, ord, any, all, hash, repr. " +

					"RETRY POLICY: Retry after fixing syntax or logic errors, especially for read-only flows. Before rerunning code that already made tool calls, inspect prior outputs and avoid replaying stateful operations.",
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
func (s *StarlarkCodeMode) handleExecuteToolCode(ctx *schemas.BifrostContext, toolCall schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, error) {
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
		// A sandbox run that raised is a tool failure, not a result. Without the
		// marker the traceback below reaches the provider as an ordinary tool
		// result and the model reads it as a genuine answer.
		executionSuccess = false
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
				resultJSON, err := schemas.MarshalSortedIndent(result.Result, "", "  ")
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
	// A failed sandbox run already reports the failure in responseText, but without
	// the marker the model reads that text as an ordinary result.
	return createToolResponseMessage(toolCall, responseText, !executionSuccess), nil
}

// executeCode executes Python (Starlark) code in a sandboxed interpreter with MCP tool bindings.
func (s *StarlarkCodeMode) executeCode(ctx *schemas.BifrostContext, code string) ExecutionResult {
	limits := s.getLimits()
	if len(code) > limits.MaxSourceBytes {
		return sandboxFailure("code exceeds source limit")
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return ExecutionResult{Logs: []string{}, Environment: ExecutionEnvironment{ServerKeys: []string{}}}
	}
	bindings := map[string][]string{}
	for clientName, tools := range s.clientManager.GetToolPerClient(ctx) {
		client := s.clientManager.GetClientByName(clientName)
		if client == nil || !client.ExecutionConfig.IsCodeModeClient {
			continue
		}
		for _, tool := range tools {
			if tool.Function != nil && tool.Function.Name != "" {
				bindings[clientName] = append(bindings[clientName], tool.Function.Name)
			}
		}
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, s.getToolExecutionTimeout())
	defer cancel()
	return runSandbox(timeoutCtx, limits, code, bindings, func(callCtx context.Context, client, tool string, args map[string]interface{}, log func(string)) (interface{}, error) {
		return s.callMCPTool(schemas.NewBifrostContext(callCtx, schemas.NoDeadline), client, tool, args, log)
	})
}

// callMCPTool calls an MCP tool and returns the result.
func (s *StarlarkCodeMode) callMCPTool(ctx *schemas.BifrostContext, clientName, toolName string, args map[string]interface{}, appendLog func(string)) (interface{}, error) {
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

	// Enforce the allow-lists at the actual invocation chokepoint, not just via the
	// pre-flight source-text scan in agent.go: generated code can reach any bound tool
	// through indirection (getattr, a dispatch table, etc.) that scan doesn't recognize,
	// so this is the only point that sees the real tool being called regardless of how
	// the Starlark code referenced it. Every Starlark tool invocation passes through this
	// function (it's the sole callee of the builtin closures the sandbox is populated
	// with), so this one check covers every syntax shape at once. ToolsToAutoExecute
	// applies only to unattended (agent loop) runs; approved runs are bound by
	// ToolsToExecute. A pre-hook rename is re-checked inside the op closure below.
	if err := codemcp.AuthorizeCodeModeToolCall(ctx, toolName, client.ExecutionConfig); err != nil {
		return nil, err
	}

	// Strip the client name prefix from tool name before calling MCP server
	originalToolName := stripClientPrefix(toolName, clientName)

	originalRequestID, ok := ctx.Value(schemas.BifrostContextKeyRequestID).(string)
	if !ok {
		originalRequestID = ""
	}

	// Generate new request ID for this nested tool call
	var newRequestID string
	if s.fetchNewRequestIDFunc != nil {
		newRequestID = s.fetchNewRequestIDFunc(ctx)
	} else {
		newRequestID = fmt.Sprintf("exec_%d_%s", time.Now().UnixNano(), toolName)
	}

	// Create new child context
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		deadline = schemas.NoDeadline
	}
	nestedCtx := schemas.NewBifrostContext(ctx, deadline)
	nestedCtx.SetValue(schemas.BifrostContextKeyRequestID, newRequestID)
	if originalRequestID != "" {
		nestedCtx.SetValue(schemas.BifrostContextKeyParentMCPRequestID, originalRequestID)
	}

	// Marshal arguments to JSON for the tool call
	argsJSON, err := schemas.MarshalSorted(args)
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

	// Create BifrostMCPRequest. ClientName is set explicitly so the plugin gate
	// can attribute short-circuit responses without re-parsing the prefixed name.
	mcpRequest := &schemas.BifrostMCPRequest{
		RequestType:                  schemas.MCPRequestTypeChatToolCall,
		ClientName:                   clientName,
		ChatAssistantMessageToolCall: &toolCallReq,
	}

	// Acquire a connection through the shared ClientManager abstraction outside
	// the gate, mirroring the gateway's exec.go:prepareToolExecution → gate
	// ordering. Connection lifecycle is the caller's concern; the gate's op
	// closure only performs the wire CallTool.
	conn, release, err := s.clientManager.AcquireClientConn(nestedCtx, client)
	if err != nil {
		return nil, err
	}
	defer release()

	toolExecutionTimeout := s.getToolExecutionTimeout()

	// Delegate to the canonical plugin gate. RunWithPluginPipeline owns the
	// tracing span, MCPRequestType/ClientName/ToolName stamping (via
	// PopulateExtraFields), plugin log draining, and short-circuit semantics —
	// the op closure below only handles the wire CallTool. Keeps Starlark
	// nested calls observationally identical to gateway-routed calls.
	//
	// Declared out here so the post-pipeline diagnostics can name the tool.
	logToolName := strings.ReplaceAll(originalToolName, "-", "_")
	finalResp, finalErr := s.clientManager.RunWithPluginPipeline(nestedCtx, mcpRequest, func(preReq *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
		// Honor any pre-hook mutation of tool name + arguments before the wire
		// call. Mirrors ToolsManager.executeToolInternal (toolmanager.go:648–670):
		// mutated name has its client prefix stripped using the ORIGINAL client
		// (we already hold a connection to it; redirecting is out of scope),
		// empty mutated args become an empty map, and a parse failure is a hard
		// error rather than a silent fallback.
		effectiveToolName := originalToolName
		effectiveArgs := args
		if preReq != nil && preReq.ChatAssistantMessageToolCall != nil {
			toolCallReq = *preReq.ChatAssistantMessageToolCall
			if toolCallReq.Function.Name != nil && *toolCallReq.Function.Name != "" {
				effectiveToolName = stripClientPrefix(*toolCallReq.Function.Name, clientName)
				// The pre-hook may have rewritten the name: authorize the tool that
				// CallTool will actually invoke, not only the one the code asked for.
				if err := codemcp.AuthorizeCodeModeToolCall(nestedCtx, *toolCallReq.Function.Name, client.ExecutionConfig); err != nil {
					return nil, err
				}
			}
			if strings.TrimSpace(toolCallReq.Function.Arguments) == "" {
				effectiveArgs = map[string]interface{}{}
			} else {
				var mutatedArgs map[string]interface{}
				if err := sonic.Unmarshal([]byte(toolCallReq.Function.Arguments), &mutatedArgs); err != nil {
					return nil, fmt.Errorf("failed to parse modified tool arguments for '%s': %v", effectiveToolName, err)
				}
				effectiveArgs = mutatedArgs
			}
		}

		startTime := time.Now()
		toolCtx, cancel := context.WithTimeout(nestedCtx, toolExecutionTimeout)
		defer cancel()

		// Per-request extra headers (BifrostContextKeyMCPExtraHeaders) are injected
		// uniformly by the transport headerFunc (see createHTTPConnection /
		// createSSEConnection / AcquireClientConn), so no per-call Header is set here.
		// Keeps nested codemode calls on the same single header path as the gateway.
		callRequest := mcp.CallToolRequest{
			Request: mcp.Request{
				Method: string(mcp.MethodToolsCall),
			},
			Params: mcp.CallToolParams{
				Name:      effectiveToolName,
				Arguments: effectiveArgs,
			},
		}

		toolResponse, callErr := conn.CallTool(toolCtx, callRequest)
		if callErr != nil && toolCtx.Err() == context.DeadlineExceeded {
			callErr = fmt.Errorf("MCP tool call timed out after %v: %s", toolExecutionTimeout, effectiveToolName)
		}
		latency := time.Since(startTime).Milliseconds()

		if callErr != nil {
			// Server-supplied text no post-hook will see: keep it out of the execution
			// log, which is rendered into the tool message the model receives.
			s.logger.Debug("%s Tool call failed: %s.%s - %v", codemcp.CodeModeLogPrefix, clientName, effectiveToolName, callErr)
			appendLog(fmt.Sprintf("[TOOL] %s.%s call failed", clientName, effectiveToolName))
			return nil, fmt.Errorf("tool call failed for %s.%s: %v", clientName, effectiveToolName, callErr)
		}

		rawResult, err := extractTextFromMCPResponse(toolResponse, effectiveToolName, contextSandboxLimits(ctx).MaxValueBytes)
		if err != nil {
			return nil, err
		}
		// An "Error: ..." text result is still a result, so it has to reach the output
		// guardrails: mark it here and raise after the pipeline, not before it.
		isErrorResult := strings.HasPrefix(rawResult, "Error: ") || (toolResponse != nil && toolResponse.IsError)
		if isErrorResult {
			s.logger.Debug("%s Tool returned error result: %s.%s", codemcp.CodeModeLogPrefix, clientName, effectiveToolName)
		}
		logToolName = strings.ReplaceAll(effectiveToolName, "-", "_")

		return &schemas.BifrostMCPResponse{
			ChatMessage: createToolResponseMessage(toolCallReq, rawResult, isErrorResult),
			ExtraFields: schemas.BifrostMCPResponseExtraFields{
				ClientName: clientName,
				ToolName:   effectiveToolName,
				Latency:    latency,
			},
		}, nil
	})

	if finalErr != nil {
		// Rejected by the pipeline: its content must not reach the caller via the log.
		if finalErr.Error != nil {
			return nil, fmt.Errorf("%s", finalErr.Error.Message)
		}
		return nil, fmt.Errorf("tool execution failed")
	}

	if finalResp == nil {
		return nil, fmt.Errorf("plugin post-hooks returned invalid response")
	}

	if finalResp.ChatMessage != nil {
		if content := finalResp.ChatMessage.Content; content != nil && content.ContentStr != nil {
			if err := chargeToolResult(ctx, len(*content.ContentStr)); err != nil {
				return nil, err
			}
			// Raised below, so logging it too would duplicate it in the model's context.
			if after, ok := strings.CutPrefix(*content.ContentStr, "Error: "); ok {
				return nil, fmt.Errorf("%s", truncateDiagnostic(after))
			}
			// Post-pipeline text only: the wire result would put blocked and redacted
			// content into this log, which the model receives verbatim.
			appendLog(fmt.Sprintf("[TOOL] %s.%s response: %s", clientName, logToolName, formatResultForLog(*content.ContentStr)))
		}
		return extractResultFromChatMessage(finalResp.ChatMessage), nil
	}

	if finalResp.ResponsesMessage != nil {
		if message := finalResp.ResponsesMessage.ResponsesToolMessage; message != nil && message.Output != nil {
			size := 0
			if text := message.Output.ResponsesToolCallOutputStr; text != nil {
				size = len(*text)
			}
			for _, block := range message.Output.ResponsesFunctionToolCallOutputBlocks {
				if block.Text != nil {
					if len(*block.Text) >= contextSandboxLimits(ctx).MaxValueBytes-size {
						return nil, fmt.Errorf("code mode tool result exceeds size limit")
					}
					size += len(*block.Text) + 1
				}
			}
			if err := chargeToolResult(ctx, size); err != nil {
				return nil, err
			}
		}
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

// Bound response decoding before constructing generic JSON containers. The
// conservative charge includes parsing overhead; the subsequent conversion
// accounts for the Starlark values. MCP transports retain their own wire limits.
func chargeToolResult(ctx context.Context, size int) error {
	if size < 0 || size > contextSandboxLimits(ctx).MaxValueBytes {
		return fmt.Errorf("code mode tool result exceeds size limit")
	}
	if thread := starlark.ContextThread(ctx); thread != nil {
		return thread.AddAllocs(starlark.SafeInt(32 * size))
	}
	return nil
}

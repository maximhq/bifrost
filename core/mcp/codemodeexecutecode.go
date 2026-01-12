package mcp

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/clarkmcc/go-typescript"
	"github.com/dop251/goja"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/schemas"
)

// toolBinding represents a tool binding for the VM
type toolBinding struct {
	toolName   string
	clientName string
}

// toolCallInfo represents a tool call extracted from code
type toolCallInfo struct {
	serverName string
	toolName   string
}

// ExecutionResult represents the result of code execution
type ExecutionResult struct {
	Result      interface{}          `json:"result"`
	Logs        []string             `json:"logs"`
	Errors      *ExecutionError      `json:"errors,omitempty"`
	Environment ExecutionEnvironment `json:"environment"`
}

type ExecutionErrorType string

const (
	ExecutionErrorTypeCompile    ExecutionErrorType = "compile"
	ExecutionErrorTypeTypescript ExecutionErrorType = "typescript"
	ExecutionErrorTypeRuntime    ExecutionErrorType = "runtime"
)

// ExecutionError represents an error during code execution
type ExecutionError struct {
	Kind    ExecutionErrorType `json:"kind"` // "compile", "typescript", or "runtime"
	Message string             `json:"message"`
	Hints   []string           `json:"hints"`
}

// ExecutionEnvironment contains information about the execution environment
type ExecutionEnvironment struct {
	ServerKeys      []string `json:"serverKeys"`
	ImportsStripped bool     `json:"importsStripped"`
	StrippedLines   []int    `json:"strippedLines"`
	TypeScriptUsed  bool     `json:"typescriptUsed"`
}

const (
	CodeModeLogPrefix = "[CODE MODE]"
)

// createExecuteToolCodeTool creates the executeToolCode tool definition for code mode.
// This tool allows executing TypeScript code in a sandboxed VM with access to MCP server tools.
//
// Returns:
//   - schemas.ChatTool: The tool definition for executing tool code
func (m *ToolsManager) createExecuteToolCodeTool() schemas.ChatTool {
	executeToolCodeProps := schemas.OrderedMap{
		"code": map[string]interface{}{
			"type":        "string",
			"description": "TypeScript code to execute. The code will be transpiled to JavaScript and validated before execution. Import/export statements will be stripped. You can use async/await syntax for async operations. For simple use cases, directly return results. Check keys and value types only for debugging. Do not print entire outputs in console logs - only print structure (keys, types) when debugging. ALWAYS retry if code fails. Example (simple): const result = await serverName.toolName({arg: 'value'}); return result; Example (debugging): const result = await serverName.toolName({arg: 'value'}); const getStruct = (o, d=0) => d>2 ? '...' : o===null ? 'null' : Array.isArray(o) ? `Array[${o.length}]` : typeof o !== 'object' ? typeof o : Object.keys(o).reduce((a,k) => (a[k]=getStruct(o[k],d+1), a), {}); console.log('Structure:', getStruct(result)); return result;",
		},
	}
	return schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name: ToolTypeExecuteToolCode,
			Description: schemas.Ptr(
				"Executes TypeScript code inside a sandboxed goja-based VM with access to all connected MCP servers' tools. " +
					"TypeScript code is automatically transpiled to JavaScript and validated before execution, providing type checking and validation. " +
					"All connected servers are exposed as global objects named after their configuration keys, and each server " +
					"provides async (Promise-returning) functions for every tool available on that server. The canonical usage " +
					"pattern is: const result = await <serverName>.<toolName>({ ...args }); Both <serverName> and <toolName> " +
					"should be discovered using listToolFiles and readToolFile. " +

					"IMPORTANT WORKFLOW: Always follow this order — first use listToolFiles to see available servers and tools, " +
					"then use readToolFile to understand the tool definitions and their parameters, and finally use executeToolCode " +
					"to execute your code. Check listToolFiles whenever you are unsure about what tools you have available or if you want to verify available servers and their tools. " +

					"LOGGING GUIDELINES: For simple use cases, you can directly return results without logging. Check for keys and value types only " +
					"for debugging purposes when you need to understand the response structure. Do not print the entire output in console logs. " +
					"When debugging, use console logs to print just the output structure to understand its type. For nested objects, use a recursive helper to show types at all levels. " +
					"For example: const getStruct = (o, d=0) => d>2 ? '...' : o===null ? 'null' : Array.isArray(o) ? `Array[${o.length}]` : typeof o !== 'object' ? typeof o : Object.keys(o).reduce((a,k) => (a[k]=getStruct(o[k],d+1), a), {}); " +
					"console.log('Structure:', getStruct(result)); Only print the entire data if absolutely necessary for debugging. " +
					"This helps understand the response structure without cluttering the output with full object contents. " +

					"RETRY POLICY: ALWAYS retry if a code block fails. If execution produces an error or unexpected result, analyze the error, " +
					"adjust your code accordingly for better results or debugging, and retry the execution. Do not give up after a single failure — iterate and improve your code until it succeeds. " +

					"The environment is intentionally minimal and has several constraints: " +
					"• ES modules are not supported — any leading import/export statements are automatically stripped and imported symbols will not exist. " +
					"• Browser and Node APIs such as fetch, XMLHttpRequest, axios, require, setTimeout, setInterval, window, and document do not exist. " +
					"• async/await syntax is supported and automatically transpiled to Promise chains compatible with goja. " +
					"• Using undefined server names or tool names will result in reference or function errors. " +
					"• The VM does not emulate a browser or Node.js environment — no DOM, timers, modules, or network APIs are available. " +
					"• Only ES5.1+ features supported by goja are guaranteed to work. " +
					"• TypeScript type checking occurs during transpilation — type errors will prevent execution. " +

					"If you want a value returned from the code, write a top-level 'return <value>'; otherwise the return value will be null. " +
					"Console output (log, error, warn, info) is captured and returned. " +
					"Long-running or blocked operations are interrupted via execution timeout. " +
					"This tool is designed specifically for orchestrating MCP tool calls and lightweight TypeScript computation.",
			),

			Parameters: &schemas.ToolFunctionParameters{
				Type:       "object",
				Properties: &executeToolCodeProps,
				Required:   []string{"code"},
			},
		},
	}
}

// handleExecuteToolCode handles the executeToolCode tool call.
// It parses the code argument, executes it in a sandboxed VM, and formats the response
// with execution results, logs, errors, and environment information.
//
// Parameters:
//   - ctx: Context for code execution
//   - toolCall: The tool call request containing the TypeScript code to execute
//
// Returns:
//   - *schemas.ChatMessage: A tool response message containing execution results
//   - error: Any error that occurred during processing
func (m *ToolsManager) handleExecuteToolCode(ctx context.Context, toolCall schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, error) {
	toolName := "unknown"
	if toolCall.Function.Name != nil {
		toolName = *toolCall.Function.Name
	}
	logger.Debug(fmt.Sprintf("%s Handling executeToolCode tool call: %s", CodeModeLogPrefix, toolName))

	// Parse tool arguments
	var arguments map[string]interface{}
	if err := sonic.Unmarshal([]byte(toolCall.Function.Arguments), &arguments); err != nil {
		logger.Debug(fmt.Sprintf("%s Failed to parse tool arguments: %v", CodeModeLogPrefix, err))
		return nil, fmt.Errorf("failed to parse tool arguments: %v", err)
	}

	code, ok := arguments["code"].(string)
	if !ok || code == "" {
		logger.Debug(fmt.Sprintf("%s Code parameter missing or empty", CodeModeLogPrefix))
		return nil, fmt.Errorf("code parameter is required and must be a non-empty string")
	}

	logger.Debug(fmt.Sprintf("%s Starting code execution", CodeModeLogPrefix))
	result := m.executeCode(ctx, code)
	logger.Debug(fmt.Sprintf("%s Code execution completed. Success: %v, Has errors: %v, Log count: %d", CodeModeLogPrefix, result.Errors == nil, result.Errors != nil, len(result.Logs)))

	// Format response text
	var responseText string
	var executionSuccess bool = true // Track if execution was successful (has data)
	if result.Errors != nil {
		logger.Debug(fmt.Sprintf("%s Formatting error response. Error kind: %s, Message length: %d, Hints count: %d", CodeModeLogPrefix, result.Errors.Kind, len(result.Errors.Message), len(result.Errors.Hints)))
		logsText := ""
		if len(result.Logs) > 0 {
			logsText = fmt.Sprintf("\n\nConsole/Log Output:\n%s\n",
				strings.Join(result.Logs, "\n"))
		}
		errorKindLabel := result.Errors.Kind

		responseText = fmt.Sprintf(
			"Execution %s error:\n\n%s\n\nHints:\n%s%s\n\nEnvironment:\n  Available server keys: %s\n  TypeScript used: %s\n  Imports stripped: %s",
			errorKindLabel,
			result.Errors.Message,
			strings.Join(result.Errors.Hints, "\n"),
			logsText,
			strings.Join(result.Environment.ServerKeys, ", "),
			map[bool]string{true: "Yes", false: "No"}[result.Environment.TypeScriptUsed],
			map[bool]string{true: "Yes", false: "No"}[result.Environment.ImportsStripped],
		)
		if len(result.Environment.StrippedLines) > 0 {
			strippedStr := make([]string, len(result.Environment.StrippedLines))
			for i, line := range result.Environment.StrippedLines {
				strippedStr[i] = fmt.Sprintf("%d", line)
			}
			responseText += fmt.Sprintf("\n  Stripped lines: %s", strings.Join(strippedStr, ", "))
		}
		logger.Debug(fmt.Sprintf("%s Error response formatted. Response length: %d chars", CodeModeLogPrefix, len(responseText)))
	} else {
		// Success case - check if execution produced any data
		hasLogs := len(result.Logs) > 0
		hasResult := result.Result != nil
		logger.Debug(fmt.Sprintf("%s Formatting success response. Has logs: %v, Has result: %v", CodeModeLogPrefix, hasLogs, hasResult))

		// If execution completed but produced no data (no logs, no return value), treat as failure
		if !hasLogs && !hasResult {
			executionSuccess = false
			logger.Debug(fmt.Sprintf("%s Execution completed with no data (no logs, no result), marking as failure", CodeModeLogPrefix))
			hints := []string{
				"Add console.log() statements throughout your code to debug and see what's happening at each step",
				"Ensure your code has a top-level return statement if you want to return a value",
				"Check that your tool calls are actually executing and returning data",
				"Verify that async operations (like await) are properly handled",
			}
			responseText = fmt.Sprintf(
				"Execution completed but produced no data:\n\n"+
					"The code executed without errors but returned no output (no console logs and no return value).\n\n"+
					"Hints:\n%s\n\n"+
					"Environment:\n  Available server keys: %s\n  TypeScript used: %s\n  Imports stripped: %s",
				strings.Join(hints, "\n"),
				strings.Join(result.Environment.ServerKeys, ", "),
				map[bool]string{true: "Yes", false: "No"}[result.Environment.TypeScriptUsed],
				map[bool]string{true: "Yes", false: "No"}[result.Environment.ImportsStripped],
			)
			if len(result.Environment.StrippedLines) > 0 {
				strippedStr := make([]string, len(result.Environment.StrippedLines))
				for i, line := range result.Environment.StrippedLines {
					strippedStr[i] = fmt.Sprintf("%d", line)
				}
				responseText += fmt.Sprintf("\n  Stripped lines: %s", strings.Join(strippedStr, ", "))
			}
			logger.Debug(fmt.Sprintf("%s No-data failure response formatted. Response length: %d chars", CodeModeLogPrefix, len(responseText)))
		} else {
			// Normal success case with data
			if hasLogs {
				responseText = fmt.Sprintf("Console output:\n%s\n\nExecution completed successfully.",
					strings.Join(result.Logs, "\n"))
			} else {
				responseText = "Execution completed successfully."
			}
			if hasResult {
				resultJSON, err := sonic.MarshalIndent(result.Result, "", "  ")
				if err == nil {
					responseText += fmt.Sprintf("\nReturn value: %s", string(resultJSON))
					logger.Debug(fmt.Sprintf("%s Added return value to response (JSON length: %d chars)", CodeModeLogPrefix, len(resultJSON)))
				} else {
					logger.Debug(fmt.Sprintf("%s Failed to marshal result to JSON: %v", CodeModeLogPrefix, err))
				}
			}

			// Add environment information for successful executions
			responseText += fmt.Sprintf("\n\nEnvironment:\n  Available server keys: %s\n  TypeScript used: %s\n  Imports stripped: %s",
				strings.Join(result.Environment.ServerKeys, ", "),
				map[bool]string{true: "Yes", false: "No"}[result.Environment.TypeScriptUsed],
				map[bool]string{true: "Yes", false: "No"}[result.Environment.ImportsStripped])
			if len(result.Environment.StrippedLines) > 0 {
				strippedStr := make([]string, len(result.Environment.StrippedLines))
				for i, line := range result.Environment.StrippedLines {
					strippedStr[i] = fmt.Sprintf("%d", line)
				}
				responseText += fmt.Sprintf("\n  Stripped lines: %s", strings.Join(strippedStr, ", "))
			}
			responseText += "\nNote: Browser APIs like fetch, setTimeout are not available. Use MCP tools for external interactions."
			logger.Debug(fmt.Sprintf("%s Success response formatted. Response length: %d chars, Server keys: %v", CodeModeLogPrefix, len(responseText), result.Environment.ServerKeys))
		}
	}

	logger.Debug(fmt.Sprintf("%s Returning tool response message. Execution success: %v", CodeModeLogPrefix, executionSuccess))
	return createToolResponseMessage(toolCall, responseText), nil
}

// executeCode executes TypeScript code in a sandboxed VM with MCP tool bindings.
// It handles code preprocessing (stripping imports/exports), TypeScript transpilation,
// VM setup with tool bindings, and promise-based async execution with timeout handling.
//
// Parameters:
//   - ctx: Context for code execution (used for timeout and tool access)
//   - code: TypeScript code string to execute
//
// Returns:
//   - ExecutionResult: Result containing execution output, logs, errors, and environment info
func (m *ToolsManager) executeCode(ctx context.Context, code string) ExecutionResult {
	logs := []string{}
	strippedLines := []int{}

	logger.Debug(fmt.Sprintf("%s Starting TypeScript code execution", CodeModeLogPrefix))

	// Step 1: Convert literal \n escape sequences to actual newlines first
	// This ensures multiline code and import/export stripping work correctly
	codeWithNewlines := strings.ReplaceAll(code, "\\n", "\n")

	// Step 2: Strip import/export statements
	cleanedCode, strippedLineNumbers := stripImportsAndExports(codeWithNewlines)
	strippedLines = append(strippedLines, strippedLineNumbers...)
	if len(strippedLineNumbers) > 0 {
		logger.Debug(fmt.Sprintf("%s Stripped %d import/export lines", CodeModeLogPrefix, len(strippedLineNumbers)))
	}

	// Step 3: Handle empty code after stripping (in case stripping made it empty)
	trimmedCode := strings.TrimSpace(cleanedCode)
	if trimmedCode == "" {
		// Empty code should return null - return early without VM execution
		return ExecutionResult{
			Result: nil,
			Logs:   logs,
			Errors: nil,
			Environment: ExecutionEnvironment{
				ServerKeys:      []string{}, // Will be populated below if needed, but empty code doesn't need tools
				ImportsStripped: len(strippedLines) > 0,
				StrippedLines:   strippedLines,
				TypeScriptUsed:  true,
			},
		}
	}

	// Step 4: Wrap code in async function for proper await transpilation
	// TypeScript needs an async function context to properly transpile await expressions
	// Check if code is already an async IIFE - if so, await it
	trimmedLower := strings.ToLower(strings.TrimSpace(trimmedCode))
	isAsyncIIFE := strings.HasPrefix(trimmedLower, "(async") && strings.Contains(trimmedCode, ")()")

	var codeToTranspile string
	if isAsyncIIFE {
		// Code is already an async IIFE - await it to get the result
		codeToTranspile = fmt.Sprintf("async function __execute__() {\nreturn await %s\n}", trimmedCode)
	} else {
		// Regular code - wrap in async function
		codeToTranspile = fmt.Sprintf("async function __execute__() {\n%s\n}", trimmedCode)
	}

	// Step 5: Transpile TypeScript to JavaScript with validation
	// Configure TypeScript compiler to transpile async/await to Promise chains (ES5 compatible)
	logger.Debug(fmt.Sprintf("%s Transpiling TypeScript code", CodeModeLogPrefix))
	compileOptions := map[string]interface{}{
		"target":             "ES5",      // Target ES5 for goja compatibility
		"module":             "None",     // No module system
		"lib":                []string{}, // No lib (minimal environment)
		"downlevelIteration": true,       // Support async/await transpilation
	}
	jsCode, transpileErr := typescript.TranspileString(codeToTranspile, typescript.WithCompileOptions(compileOptions))
	if transpileErr != nil {
		logger.Debug(fmt.Sprintf("%s TypeScript transpilation failed: %v", CodeModeLogPrefix, transpileErr))
		// Build bindings to get server keys for error hints
		availableToolsPerClient := m.clientManager.GetToolPerClient(ctx)
		serverKeys := make([]string, 0, len(availableToolsPerClient))
		for clientName := range availableToolsPerClient {
			client := m.clientManager.GetClientByName(clientName)
			if client == nil {
				logger.Warn("%s Client %s not found, skipping", MCPLogPrefix, clientName)
				continue
			}
			if !client.ExecutionConfig.IsCodeModeClient {
				continue
			}
			serverKeys = append(serverKeys, clientName)
		}

		errorMessage := transpileErr.Error()
		hints := generateTypeScriptErrorHints(errorMessage, serverKeys)

		return ExecutionResult{
			Result: nil,
			Logs:   logs,
			Errors: &ExecutionError{
				Kind:    ExecutionErrorTypeTypescript,
				Message: fmt.Sprintf("TypeScript compilation error: %s", errorMessage),
				Hints:   hints,
			},
			Environment: ExecutionEnvironment{
				ServerKeys:      serverKeys,
				ImportsStripped: len(strippedLines) > 0,
				StrippedLines:   strippedLines,
				TypeScriptUsed:  true,
			},
		}
	}

	logger.Debug(fmt.Sprintf("%s TypeScript transpiled successfully", CodeModeLogPrefix))

	// Step 5: Create timeout context early so goroutines can use it
	toolExecutionTimeout := m.toolExecutionTimeout.Load().(time.Duration)
	timeoutCtx, cancel := context.WithTimeout(ctx, toolExecutionTimeout)
	defer cancel()

	// Step 6: Build bindings for all connected servers
	availableToolsPerClient := m.clientManager.GetToolPerClient(ctx)
	bindings := make(map[string]map[string]toolBinding)
	serverKeys := make([]string, 0, len(availableToolsPerClient))

	logger.Debug(fmt.Sprintf("%s GetToolPerClient returned %d clients", CodeModeLogPrefix, len(availableToolsPerClient)))

	for clientName, tools := range availableToolsPerClient {
		client := m.clientManager.GetClientByName(clientName)
		if client == nil {
			logger.Warn("%s Client %s not found, skipping", MCPLogPrefix, clientName)
			continue
		}
		logger.Debug(fmt.Sprintf("%s [%s] Client found. IsCodeModeClient: %v, ToolCount: %d", CodeModeLogPrefix, clientName, client.ExecutionConfig.IsCodeModeClient, len(tools)))
		if !client.ExecutionConfig.IsCodeModeClient || len(tools) == 0 {
			logger.Debug(fmt.Sprintf("%s [%s] Skipped: IsCodeModeClient=%v, HasTools=%v", CodeModeLogPrefix, clientName, client.ExecutionConfig.IsCodeModeClient, len(tools) > 0))
			continue
		}
		serverKeys = append(serverKeys, clientName)

		toolFunctions := make(map[string]toolBinding)

		// Create a function for each tool
		for _, tool := range tools {
			if tool.Function == nil || tool.Function.Name == "" {
				continue
			}

			originalToolName := tool.Function.Name
			// Strip client prefix and replace - with _ for code mode compatibility
			unprefixedToolName := stripClientPrefix(originalToolName, clientName)
			unprefixedToolName = strings.ReplaceAll(unprefixedToolName, "-", "_")
			// Parse tool name for property name compatibility (used as property name in the runtime)
			parsedToolName := parseToolName(unprefixedToolName)

			logger.Debug(fmt.Sprintf("%s [%s] Bound tool: %s -> %s -> %s", CodeModeLogPrefix, clientName, originalToolName, unprefixedToolName, parsedToolName))

			// Store tool binding
			toolFunctions[parsedToolName] = toolBinding{
				toolName:   originalToolName,
				clientName: clientName,
			}
		}

		bindings[clientName] = toolFunctions
		logger.Debug(fmt.Sprintf("%s [%s] Added to bindings with %d functions", CodeModeLogPrefix, clientName, len(toolFunctions)))
	}

	if len(serverKeys) > 0 {
		logger.Debug(fmt.Sprintf("%s Bound %d servers with tools: %v", CodeModeLogPrefix, len(serverKeys), serverKeys))
	} else {
		logger.Debug(fmt.Sprintf("%s No servers available for code mode execution", CodeModeLogPrefix))
	}

	// Step 7: Wrap transpiled code to execute the async function and return its result
	// The transpiled code contains an async function __execute__() that we need to call
	// Trim trailing newlines to avoid issues when wrapping
	codeToWrap := strings.TrimRight(jsCode, "\n\r")
	// Wrap in IIFE that calls the transpiled async function and returns the promise
	wrappedCode := fmt.Sprintf("(function() {\n%s\nreturn __execute__();\n})()", codeToWrap)

	// Step 8: Create goja runtime
	vm := goja.New()

	// Step 9: Set up thread-safe logging
	appendLog := func(msg string) {
		m.logMu.Lock()
		defer m.logMu.Unlock()
		logs = append(logs, msg)
	}

	// Step 10: Set up console
	consoleObj := vm.NewObject()
	consoleObj.Set("log", func(args ...interface{}) {
		message := formatConsoleArgs(args)
		appendLog(message)
	})
	consoleObj.Set("error", func(args ...interface{}) {
		message := formatConsoleArgs(args)
		appendLog(fmt.Sprintf("[ERROR] %s", message))
	})
	consoleObj.Set("warn", func(args ...interface{}) {
		message := formatConsoleArgs(args)
		appendLog(fmt.Sprintf("[WARN] %s", message))
	})
	consoleObj.Set("info", func(args ...interface{}) {
		message := formatConsoleArgs(args)
		appendLog(fmt.Sprintf("[INFO] %s", message))
	})
	vm.Set("console", consoleObj)

	// Step 11: Set up server bindings
	logger.Debug(fmt.Sprintf("%s Setting up %d server bindings in VM", CodeModeLogPrefix, len(bindings)))
	for serverKey, tools := range bindings {
		logger.Debug(fmt.Sprintf("%s [%s] Setting up server object with %d tools", CodeModeLogPrefix, serverKey, len(tools)))
		serverObj := vm.NewObject()
		for toolName, binding := range tools {
			// Capture variables for closure
			toolNameFinal := binding.toolName
			clientNameFinal := binding.clientName

			logger.Debug(fmt.Sprintf("%s [%s] Binding tool function: %s -> %s", CodeModeLogPrefix, serverKey, toolName, toolNameFinal))

			serverObj.Set(toolName, func(call goja.FunctionCall) goja.Value {
				args := call.Argument(0).Export()

				// Convert args to map[string]interface{}
				argsMap, ok := args.(map[string]interface{})
				if !ok {
					logger.Debug(fmt.Sprintf("%s Invalid args type for %s.%s: expected object, got %T",
						CodeModeLogPrefix, clientNameFinal, toolNameFinal, args))
					// Return rejected promise for invalid args
					promise, _, reject := vm.NewPromise()
					err := fmt.Errorf("expected object argument, got %T", args)
					reject(vm.ToValue(err))
					return vm.ToValue(promise)
				}

				// Create promise on VM goroutine (thread-safe)
				promise, resolve, reject := vm.NewPromise()

				// Define result struct for channel communication
				type toolResult struct {
					result interface{}
					err    error
				}

				// Create buffered channel for worker communication
				resultChan := make(chan toolResult, 1)

				// Call tool asynchronously with timeout context and panic recovery
				// Worker goroutine - NO VM calls allowed here
				go func() {
					defer func() {
						if r := recover(); r != nil {
							logger.Debug(fmt.Sprintf("%s Panic in tool call goroutine for %s.%s: %v",
								CodeModeLogPrefix, clientNameFinal, toolNameFinal, r))
							// Send panic as error through channel (no VM calls in worker)
							select {
							case resultChan <- toolResult{nil, fmt.Errorf("tool call panic: %v", r)}:
							case <-timeoutCtx.Done():
								// Context cancelled, ignore
							}
						}
					}()

					// Check if context is already cancelled before starting
					select {
					case <-timeoutCtx.Done():
						// Send timeout error through channel (no VM calls in worker)
						select {
						case resultChan <- toolResult{nil, fmt.Errorf("execution timeout")}:
						case <-timeoutCtx.Done():
							// Already cancelled, ignore
						}
						return
					default:
					}

					// Pass the original ctx (BifrostContext) to callMCPTool, not timeoutCtx
					// callMCPTool will handle timeout internally
					result, err := m.callMCPTool(ctx, clientNameFinal, toolNameFinal, argsMap, appendLog)

					// Check if context was cancelled during execution
					select {
					case <-timeoutCtx.Done():
						// Send timeout error through channel (no VM calls in worker)
						select {
						case resultChan <- toolResult{nil, fmt.Errorf("execution timeout")}:
						case <-timeoutCtx.Done():
							// Already cancelled, ignore
						}
						return
					default:
					}

					// Send result through channel (no VM calls in worker)
					select {
					case resultChan <- toolResult{result, err}:
					case <-timeoutCtx.Done():
						// Context cancelled, ignore
					}
				}()

				// Process result synchronously on VM goroutine to ensure thread safety
				// This blocks the VM goroutine until the tool call completes, but ensures
				// all VM operations (vm.ToValue, resolve, reject) happen on the correct thread
				select {
				case res := <-resultChan:
					if res.err != nil {
						logger.Debug(fmt.Sprintf("%s Tool call failed: %s.%s - %v",
							CodeModeLogPrefix, clientNameFinal, toolNameFinal, res.err))
						reject(vm.ToValue(res.err))
					} else {
						resolve(vm.ToValue(res.result))
					}
				case <-timeoutCtx.Done():
					reject(vm.ToValue(fmt.Errorf("execution timeout")))
				}

				return vm.ToValue(promise)
			})
		}
		vm.Set(serverKey, serverObj)
		logger.Debug(fmt.Sprintf("%s [%s] Server object set in VM", CodeModeLogPrefix, serverKey))
	}

	// Step 12: Set up environment info
	envObj := vm.NewObject()
	envObj.Set("serverKeys", serverKeys)
	envObj.Set("version", "1.0.0")
	vm.Set("__MCP_ENV__", envObj)

	// Step 13: Execute code with timeout

	// Set up interrupt handler
	interruptDone := make(chan struct{})
	go func() {
		select {
		case <-timeoutCtx.Done():
			logger.Debug(fmt.Sprintf("%s Execution timeout reached", CodeModeLogPrefix))
			vm.Interrupt("execution timeout")
		case <-interruptDone:
		}
	}()

	var result interface{}
	var executionErr error

	func() {
		defer close(interruptDone)
		val, err := vm.RunString(wrappedCode)
		if err != nil {
			logger.Debug(fmt.Sprintf("%s VM execution error: %v", CodeModeLogPrefix, err))
			executionErr = err
			return
		}

		// Check if the result is a promise by checking its type
		// First check if val is nil or undefined (these can't be converted to objects)
		if val == nil || val == goja.Undefined() {
			result = nil
			return
		}

		// Try to convert to object to check if it's a promise
		// Use recover to safely handle null values that can't be converted to objects
		var valObj *goja.Object
		func() {
			defer func() {
				if r := recover(); r != nil {
					// Value is null or can't be converted to object, just export it
					valObj = nil
				}
			}()
			valObj = val.ToObject(vm)
		}()

		if valObj != nil {
			// Check if it has a 'then' method (Promise-like)
			if then := valObj.Get("then"); then != nil && then != goja.Undefined() {
				// It's a promise, we need to await it
				// Use buffered channels to prevent blocking if handlers are called after timeout
				resultChan := make(chan interface{}, 1)
				errChan := make(chan error, 1)

				// Set up promise handlers
				thenFunc, ok := goja.AssertFunction(then)
				if ok {
					// Call then with resolve and reject handlers
					_, err := thenFunc(val,
						vm.ToValue(func(res goja.Value) {
							select {
							case resultChan <- res.Export():
							case <-timeoutCtx.Done():
								// Timeout already occurred, ignore result
							}
						}),
						vm.ToValue(func(err goja.Value) {
							var errMsg string
							if err == nil || err == goja.Undefined() {
								errMsg = "unknown error"
							} else {
								// Try to get error message from Error object
								if errObj := err.ToObject(vm); errObj != nil {
									if msg := errObj.Get("message"); msg != nil && msg != goja.Undefined() {
										errMsg = msg.String()
									} else if name := errObj.Get("name"); name != nil && name != goja.Undefined() {
										errMsg = name.String()
									} else {
										errMsg = err.String()
									}
								} else {
									// Fallback to string conversion
									errMsg = err.String()
								}
							}
							select {
							case errChan <- fmt.Errorf("%s", errMsg):
							case <-timeoutCtx.Done():
								// Timeout already occurred, ignore error
							}
						}),
					)
					if err != nil {
						executionErr = err
						return
					}

					// Wait for result or error with timeout
					select {
					case res := <-resultChan:
						result = res
					case err := <-errChan:
						logger.Debug(fmt.Sprintf("%s Promise rejected: %v", CodeModeLogPrefix, err))
						executionErr = err
					case <-timeoutCtx.Done():
						logger.Debug(fmt.Sprintf("%s Promise timeout while waiting for result", CodeModeLogPrefix))
						executionErr = fmt.Errorf("execution timeout")
					}
				} else {
					result = val.Export()
				}
			} else {
				result = val.Export()
			}
		} else {
			// Not an object (or null/undefined), just export the value
			result = val.Export()
		}
	}()

	if executionErr != nil {
		errorMessage := executionErr.Error()
		hints := generateErrorHints(errorMessage, serverKeys)
		logger.Debug(fmt.Sprintf("%s Execution failed: %s", CodeModeLogPrefix, errorMessage))

		return ExecutionResult{
			Result: nil,
			Logs:   logs,
			Errors: &ExecutionError{
				Kind:    ExecutionErrorTypeRuntime,
				Message: errorMessage,
				Hints:   hints,
			},
			Environment: ExecutionEnvironment{
				ServerKeys:      serverKeys,
				ImportsStripped: len(strippedLines) > 0,
				StrippedLines:   strippedLines,
				TypeScriptUsed:  true,
			},
		}
	}

	logger.Debug(fmt.Sprintf("%s Execution completed successfully", CodeModeLogPrefix))
	return ExecutionResult{
		Result: result,
		Logs:   logs,
		Errors: nil,
		Environment: ExecutionEnvironment{
			ServerKeys:      serverKeys,
			ImportsStripped: len(strippedLines) > 0,
			StrippedLines:   strippedLines,
			TypeScriptUsed:  true,
		},
	}
}

// callMCPTool calls an MCP tool and returns the result.
// It locates the client by name, constructs the MCP tool call request, executes it
// with timeout handling, and parses the response as JSON or returns it as a string.
// This function now runs MCP plugin hooks (PreMCPHook/PostMCPHook) for nested tool calls.
//
// Parameters:
//   - ctx: Context for tool execution (used for timeout and plugin hooks)
//   - clientName: Name of the MCP client/server to call
//   - toolName: Name of the tool to execute
//   - args: Tool arguments as a map
//   - appendLog: Function to append log messages during execution
//
// Returns:
//   - interface{}: Parsed tool result (JSON object or string)
//   - error: Any error that occurred during tool execution
func (m *ToolsManager) callMCPTool(ctx context.Context, clientName, toolName string, args map[string]interface{}, appendLog func(string)) (interface{}, error) {
	// Get available tools per client
	availableToolsPerClient := m.clientManager.GetToolPerClient(ctx)

	// Find the client by name
	tools, exists := availableToolsPerClient[clientName]
	if !exists || len(tools) == 0 {
		return nil, fmt.Errorf("client not found for server name: %s", clientName)
	}

	// Get client using a tool from this client
	// Find the first tool with a valid Function to use for client lookup
	var client *schemas.MCPClientState
	for _, tool := range tools {
		if tool.Function != nil && tool.Function.Name != "" {
			client = m.clientManager.GetClientForTool(tool.Function.Name)
			if client != nil {
				break
			}
		}
	}

	if client == nil {
		return nil, fmt.Errorf("client not found for server name: %s", clientName)
	}

	// Strip the client name prefix from tool name before calling MCP server
	// The MCP server expects the original tool name, not the prefixed version
	originalToolName := stripClientPrefix(toolName, clientName)

	// ==================== PLUGIN PIPELINE INTEGRATION ====================
	// Set up parent-child request ID tracking and run plugin hooks

	// Get original executeCode request ID from context (will become parent)
	var bifrostCtx *schemas.BifrostContext
	var ok bool
	if bifrostCtx, ok = ctx.(*schemas.BifrostContext); !ok {
		// Fallback: if not a BifrostContext, execute directly without plugins
		return m.callMCPToolDirect(ctx, client, originalToolName, clientName, toolName, args, appendLog)
	}

	originalRequestID, _ := bifrostCtx.Value(schemas.BifrostContextKeyRequestID).(string)

	// Generate new request ID for this nested tool call
	var newRequestID string
	if m.fetchNewRequestIDFunc != nil {
		newRequestID = m.fetchNewRequestIDFunc(bifrostCtx)
	} else {
		// Fallback: generate a simple UUID-like ID
		newRequestID = fmt.Sprintf("exec_%d_%s", time.Now().UnixNano(), toolName)
	}

	// Create new CHILD context with parent-child relationship
	// IMPORTANT: We must use NewBifrostContext() to create a proper child context with its own
	// userValues map. Using WithValue() would modify the parent context in-place, which would
	// cause the parent executeToolCode's request ID to be overwritten with the last nested tool's
	// request ID, leading to the parent's response overwriting the last nested tool's log entry.
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
	toolCall := schemas.ChatAssistantMessageToolCall{
		ID: schemas.Ptr(newRequestID),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr(toolName),
			Arguments: string(argsJSON),
		},
	}

	// Create BifrostMCPRequest
	mcpRequest := &schemas.BifrostMCPRequest{
		RequestType:                  schemas.MCPRequestTypeChatToolCall,
		ChatAssistantMessageToolCall: &toolCall,
	}

	// Check if plugin pipeline is available
	if m.pluginPipelineProvider == nil {
		// Fallback: execute directly without plugins
		return m.callMCPToolDirect(ctx, client, originalToolName, clientName, toolName, args, appendLog)
	}

	// Get plugin pipeline and run hooks
	pipeline := m.pluginPipelineProvider()
	if pipeline == nil {
		// Fallback: execute directly if pipeline is nil
		return m.callMCPToolDirect(ctx, client, originalToolName, clientName, toolName, args, appendLog)
	}
	defer m.releasePluginPipeline(pipeline)

	// Run PreMCPHooks
	preReq, shortCircuit, preCount := pipeline.RunMCPPreHooks(nestedCtx, mcpRequest)

	// Handle short-circuit cases
	if shortCircuit != nil {
		if shortCircuit.Response != nil {
			finalResp, _ := pipeline.RunMCPPostHooks(nestedCtx, shortCircuit.Response, nil, preCount)
			if finalResp != nil {
				// Try ChatMessage first
				if finalResp.ChatMessage != nil {
					return extractResultFromChatMessage(finalResp.ChatMessage), nil
				}
				// Try ResponsesMessage
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

	// If pre-hooks modified the request, extract updated tool name and args
	if preReq != nil && preReq.ChatAssistantMessageToolCall != nil {
		toolCall = *preReq.ChatAssistantMessageToolCall
		if toolCall.Function.Arguments != "" {
			// Re-parse arguments if they were modified
			if err := sonic.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
				logger.Warn(fmt.Sprintf("%s Failed to parse modified tool arguments, using original: %v", CodeModeLogPrefix, err))
			}
		}
	}

	// ==================== EXECUTE TOOL ====================

	// Capture start time for latency calculation
	startTime := time.Now()

	// Derive tool name from originalToolName (ignore pre-hook modifications to tool name)
	// Pre-hooks should not modify which tool gets called, only arguments
	toolNameToCall := originalToolName

	// Call the tool via MCP client
	callRequest := mcp.CallToolRequest{
		Request: mcp.Request{
			Method: string(mcp.MethodToolsCall),
		},
		Params: mcp.CallToolParams{
			Name:      toolNameToCall,
			Arguments: args,
		},
	}

	// Create timeout context
	toolExecutionTimeout := m.toolExecutionTimeout.Load().(time.Duration)
	toolCtx, cancel := context.WithTimeout(nestedCtx, toolExecutionTimeout)
	defer cancel()

	toolResponse, callErr := client.Conn.CallTool(toolCtx, callRequest)

	// Calculate latency
	latency := time.Since(startTime).Milliseconds()

	// ==================== PREPARE RESPONSE FOR POST-HOOKS ====================

	var mcpResp *schemas.BifrostMCPResponse
	var bifrostErr *schemas.BifrostError

	if callErr != nil {
		logger.Debug(fmt.Sprintf("%s Tool call failed: %s.%s - %v", CodeModeLogPrefix, clientName, toolName, callErr))
		appendLog(fmt.Sprintf("[TOOL] %s.%s error: %v", clientName, toolName, callErr))
		bifrostErr = &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: fmt.Sprintf("tool call failed for %s.%s: %v", clientName, toolName, callErr),
			},
		}
	} else {
		// Extract result
		rawResult := extractTextFromMCPResponse(toolResponse, toolName)

		// Check if this is an error result (from NewToolResultError)
		// Error results start with "Error: " prefix
		if after, ok := strings.CutPrefix(rawResult, "Error: "); ok {
			errorMsg := after
			logger.Debug(fmt.Sprintf("%s Tool returned error result: %s.%s - %s", CodeModeLogPrefix, clientName, toolName, errorMsg))
			appendLog(fmt.Sprintf("[TOOL] %s.%s error result: %s", clientName, toolName, errorMsg))
			bifrostErr = &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Message: errorMsg,
				},
			}
		} else {
			// Success case - create response
			mcpResp = &schemas.BifrostMCPResponse{
				ChatMessage: createToolResponseMessage(toolCall, rawResult),
				ExtraFields: schemas.BifrostMCPResponseExtraFields{
					ClientName: clientName,
					ToolName:   originalToolName,
					Latency:    latency,
				},
			}

			// Log the result
			resultStr := formatResultForLog(rawResult)
			// Strip prefix and replace - with _ for code mode display
			logToolName := stripClientPrefix(toolName, clientName)
			logToolName = strings.ReplaceAll(logToolName, "-", "_")
			appendLog(fmt.Sprintf("[TOOL] %s.%s raw response: %s", clientName, logToolName, resultStr))
		}
	}

	// ==================== RUN POST-HOOKS ====================

	finalResp, finalErr := pipeline.RunMCPPostHooks(nestedCtx, mcpResp, bifrostErr, preCount)

	// Return result
	if finalErr != nil {
		if finalErr.Error != nil {
			return nil, fmt.Errorf("%s", finalErr.Error.Message)
		}
		return nil, fmt.Errorf("tool execution failed")
	}

	if finalResp == nil {
		return nil, fmt.Errorf("plugin post-hooks returned invalid response")
	}

	// Extract and parse the final result from the chat message or responses message
	if finalResp.ChatMessage != nil {
		return extractResultFromChatMessage(finalResp.ChatMessage), nil
	}

	// Try ResponsesMessage if ChatMessage is not present
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
// This is used as a fallback when the plugin pipeline is not available or context is not BifrostContext.
func (m *ToolsManager) callMCPToolDirect(ctx context.Context, client *schemas.MCPClientState, originalToolName, clientName, toolName string, args map[string]interface{}, appendLog func(string)) (interface{}, error) {
	// Call the tool via MCP client
	callRequest := mcp.CallToolRequest{
		Request: mcp.Request{
			Method: string(mcp.MethodToolsCall),
		},
		Params: mcp.CallToolParams{
			Name:      originalToolName,
			Arguments: args,
		},
	}

	// Create timeout context
	toolExecutionTimeout := m.toolExecutionTimeout.Load().(time.Duration)
	toolCtx, cancel := context.WithTimeout(ctx, toolExecutionTimeout)
	defer cancel()

	// Strip prefix and replace - with _ for code mode display
	logToolName := stripClientPrefix(toolName, clientName)
	logToolName = strings.ReplaceAll(logToolName, "-", "_")

	toolResponse, callErr := client.Conn.CallTool(toolCtx, callRequest)
	if callErr != nil {
		logger.Debug(fmt.Sprintf("%s Tool call failed: %s.%s - %v", CodeModeLogPrefix, clientName, logToolName, callErr))
		appendLog(fmt.Sprintf("[TOOL] %s.%s error: %v", clientName, logToolName, callErr))
		return nil, fmt.Errorf("tool call failed for %s.%s: %v", clientName, logToolName, callErr)
	}

	// Extract result
	rawResult := extractTextFromMCPResponse(toolResponse, toolName)

	// Check if this is an error result (from NewToolResultError)
	// Error results start with "Error: " prefix
	if after, ok := strings.CutPrefix(rawResult, "Error: "); ok {
		errorMsg := after
		logger.Debug(fmt.Sprintf("%s Tool returned error result: %s.%s - %s", CodeModeLogPrefix, clientName, logToolName, errorMsg))
		appendLog(fmt.Sprintf("[TOOL] %s.%s error result: %s", clientName, logToolName, errorMsg))
		return nil, fmt.Errorf("%s", errorMsg)
	}

	// Try to parse as JSON, otherwise use as string
	var finalResult interface{}
	if err := sonic.Unmarshal([]byte(rawResult), &finalResult); err != nil {
		// Not JSON, use as string
		finalResult = rawResult
	}

	// Log the result
	resultStr := formatResultForLog(finalResult)
	appendLog(fmt.Sprintf("[TOOL] %s.%s raw response: %s", clientName, logToolName, resultStr))

	return finalResult, nil
}

// extractResultFromChatMessage extracts the result from a chat message and parses it as JSON if possible.
func extractResultFromChatMessage(msg *schemas.ChatMessage) interface{} {
	if msg == nil || msg.Content == nil || msg.Content.ContentStr == nil {
		return nil
	}

	rawResult := *msg.Content.ContentStr

	// Try to parse as JSON, otherwise use as string
	var finalResult interface{}
	if err := sonic.Unmarshal([]byte(rawResult), &finalResult); err != nil {
		// Not JSON, use as string
		return rawResult
	}

	return finalResult
}

// extractResultFromResponsesMessage extracts the result or error from a ResponsesMessage.
// It checks for tool errors first, then extracts output from the ResponsesToolMessage.
// Returns the extracted result/error, and a boolean indicating if it was an error.
func extractResultFromResponsesMessage(msg *schemas.ResponsesMessage) (interface{}, error) {
	if msg == nil {
		return nil, nil
	}

	// Check if this is a tool message
	if msg.ResponsesToolMessage != nil {
		// Check for tool error first
		if msg.ResponsesToolMessage.Error != nil && *msg.ResponsesToolMessage.Error != "" {
			return nil, fmt.Errorf("%s", *msg.ResponsesToolMessage.Error)
		}

		// Extract output if present
		if msg.ResponsesToolMessage.Output != nil {
			// Try ResponsesToolCallOutputStr first
			if msg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr != nil {
				rawResult := *msg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr

				// Try to parse as JSON, otherwise use as string
				var finalResult interface{}
				if err := sonic.Unmarshal([]byte(rawResult), &finalResult); err != nil {
					// Not JSON, use as string
					return rawResult, nil
				}
				return finalResult, nil
			}

			// Try ResponsesFunctionToolCallOutputBlocks if OutputStr is not present
			if len(msg.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks) > 0 {
				// For now, extract text from blocks and concatenate
				var textParts []string
				for _, block := range msg.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks {
					if block.Text != nil {
						textParts = append(textParts, *block.Text)
					}
				}
				if len(textParts) > 0 {
					result := strings.Join(textParts, "\n")
					// Try to parse as JSON
					var finalResult interface{}
					if err := sonic.Unmarshal([]byte(result), &finalResult); err != nil {
						return result, nil
					}
					return finalResult, nil
				}
			}
		}
	}

	// If no tool message or output, return nil
	return nil, nil
}

// HELPER FUNCTIONS

// formatResultForLog formats a result value for logging purposes.
// It attempts to marshal to JSON for structured output, falling back to string representation.
//
// Parameters:
//   - result: The result value to format
//
// Returns:
//   - string: Formatted string representation of the result
func formatResultForLog(result interface{}) string {
	var resultStr string
	if result == nil {
		resultStr = "null"
	} else if resultBytes, err := sonic.Marshal(result); err == nil {
		resultStr = string(resultBytes)
	} else {
		resultStr = fmt.Sprintf("%v", result)
	}
	return resultStr
}

// formatConsoleArgs formats console arguments for logging.
// It formats each argument as JSON if possible, otherwise uses string representation.
//
// Parameters:
//   - args: Array of console arguments to format
//
// Returns:
//   - string: Formatted string with all arguments joined by spaces
func formatConsoleArgs(args []interface{}) string {
	parts := make([]string, len(args))
	for i, arg := range args {
		if argBytes, err := sonic.MarshalIndent(arg, "", "  "); err == nil {
			parts[i] = string(argBytes)
		} else {
			parts[i] = fmt.Sprintf("%v", arg)
		}
	}
	return strings.Join(parts, " ")
}

// stripImportsAndExports strips import and export statements from code.
// It removes lines that start with import or export keywords and returns
// the cleaned code along with 1-based line numbers of stripped lines.
//
// Parameters:
//   - code: Source code string to process
//
// Returns:
//   - string: Code with import/export statements removed
//   - []int: 1-based line numbers of stripped lines
func stripImportsAndExports(code string) (string, []int) {
	lines := strings.Split(code, "\n")
	keptLines := []string{}
	strippedLineNumbers := []int{}

	importExportRegex := regexp.MustCompile(`^\s*(import|export)\b`)

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Skip empty lines
		if trimmed == "" {
			keptLines = append(keptLines, line)
			continue
		}

		// Check if this is an import or export statement
		isImportOrExport := importExportRegex.MatchString(line)

		if isImportOrExport {
			strippedLineNumbers = append(strippedLineNumbers, i+1) // 1-based line numbers
			continue                                               // Skip import/export lines
		}

		// Keep comment lines and all other non-import/export lines
		keptLines = append(keptLines, line)
	}

	return strings.Join(keptLines, "\n"), strippedLineNumbers
}

// generateTypeScriptErrorHints generates helpful hints for TypeScript compilation errors.
// It analyzes the error message and provides context-specific guidance based on error patterns.
//
// Parameters:
//   - errorMessage: The TypeScript compilation error message
//   - serverKeys: List of available MCP server keys for context
//
// Returns:
//   - []string: Array of helpful hint messages
func generateTypeScriptErrorHints(errorMessage string, serverKeys []string) []string {
	hints := []string{}

	// TypeScript-specific error patterns
	if strings.Contains(errorMessage, "Cannot find name") || strings.Contains(errorMessage, "is not defined") {
		hints = append(hints, "TypeScript compilation error: undefined variable or identifier.")
		hints = append(hints, "Check that all variables are properly declared and typed.")
		if len(serverKeys) > 0 {
			hints = append(hints, fmt.Sprintf("Available server keys: %s", strings.Join(serverKeys, ", ")))
			hints = append(hints, "Use server keys to access MCP tools: <serverKey>.<toolName>(args)")
		}
	} else if strings.Contains(errorMessage, "Type") && (strings.Contains(errorMessage, "is not assignable") || strings.Contains(errorMessage, "does not exist")) {
		hints = append(hints, "TypeScript type error detected.")
		hints = append(hints, "Check that variable types match their usage.")
		hints = append(hints, "Ensure function arguments match the expected types.")
	} else if strings.Contains(errorMessage, "Expected") {
		hints = append(hints, "TypeScript syntax error detected.")
		hints = append(hints, "Check for missing parentheses, brackets, or semicolons.")
		hints = append(hints, "Ensure all code blocks are properly closed.")
	} else if strings.Contains(errorMessage, "async") || strings.Contains(errorMessage, "await") {
		hints = append(hints, "async/await syntax should be supported. If you see this error, it may be a TypeScript compilation issue.")
		hints = append(hints, "Ensure async functions are properly declared: async function myFunction() { ... }")
		hints = append(hints, "Example: const result = await serverName.toolName({...});")
	} else {
		hints = append(hints, "TypeScript compilation error detected.")
		hints = append(hints, "Review the error message above for specific details.")
		hints = append(hints, "Ensure your TypeScript code follows valid syntax and type rules.")
		if len(serverKeys) > 0 {
			hints = append(hints, fmt.Sprintf("Available server keys: %s", strings.Join(serverKeys, ", ")))
		}
	}

	return hints
}

// generateErrorHints generates helpful hints based on runtime error messages.
// It analyzes common runtime error patterns (undefined variables, missing functions, etc.)
// and provides context-specific guidance including available server keys and usage examples.
//
// Parameters:
//   - errorMessage: The runtime error message
//   - serverKeys: List of available MCP server keys for context
//
// Returns:
//   - []string: Array of helpful hint messages
func generateErrorHints(errorMessage string, serverKeys []string) []string {
	hints := []string{}

	if strings.Contains(errorMessage, "is not defined") {
		re := regexp.MustCompile(`(\w+)\s+is not defined`)
		if match := re.FindStringSubmatch(errorMessage); len(match) > 1 {
			undefinedVar := match[1]

			// Special handling for common browser/Node.js APIs
			if undefinedVar == "fetch" {
				hints = append(hints, "The 'fetch' API is not available in this runtime environment.")
				hints = append(hints, "Instead of using fetch for HTTP requests, use the available MCP tools.")
				if len(serverKeys) > 0 {
					hints = append(hints, fmt.Sprintf("Available server keys: %s", strings.Join(serverKeys, ", ")))
					hints = append(hints, fmt.Sprintf("Example: const result = await %s.<toolName>({ url: 'https://example.com' });", serverKeys[0]))
				}
				hints = append(hints, "MCP tools handle HTTP requests, file operations, and other external interactions.")
				return hints
			} else if undefinedVar == "XMLHttpRequest" || undefinedVar == "axios" {
				hints = append(hints, fmt.Sprintf("The '%s' API is not available in this runtime environment.", undefinedVar))
				hints = append(hints, "Use MCP tools instead for HTTP requests and external API calls.")
				if len(serverKeys) > 0 {
					hints = append(hints, fmt.Sprintf("Available server keys: %s", strings.Join(serverKeys, ", ")))
				}
				return hints
			} else if undefinedVar == "setTimeout" || undefinedVar == "setInterval" {
				hints = append(hints, fmt.Sprintf("The '%s' API is not available in this runtime environment.", undefinedVar))
				hints = append(hints, "This is a sandboxed environment focused on MCP tool interactions.")
				hints = append(hints, "Use Promise chains with MCP tools instead of timing functions.")
				return hints
			} else if undefinedVar == "require" || undefinedVar == "import" {
				hints = append(hints, "Module imports are not supported in this runtime environment.")
				hints = append(hints, "Use the available MCP tools for external functionality.")
				if len(serverKeys) > 0 {
					hints = append(hints, fmt.Sprintf("Available server keys: %s", strings.Join(serverKeys, ", ")))
				}
				return hints
			}

			// Generic undefined variable handling
			hints = append(hints, fmt.Sprintf("Variable or identifier '%s' is not defined.", undefinedVar))
			if len(serverKeys) > 0 {
				hints = append(hints, fmt.Sprintf("Use one of the available server keys as the object name: %s", strings.Join(serverKeys, ", ")))
				hints = append(hints, "Then access tools using: <serverKey>.<toolName>(args)")
				hints = append(hints, fmt.Sprintf("For example: const result = await %s.<toolName>({ ... });", serverKeys[0]))
			}
		}
	} else if strings.Contains(errorMessage, "is not a function") {
		re := regexp.MustCompile(`(\w+(?:\.\w+)?)\s+is not a function`)
		if match := re.FindStringSubmatch(errorMessage); len(match) > 1 {
			notFunction := match[1]
			hints = append(hints, fmt.Sprintf("'%s' is not a function.", notFunction))
			hints = append(hints, "Ensure you're using the correct server key and tool name.")
			if len(serverKeys) > 0 {
				hints = append(hints, fmt.Sprintf("Available server keys: %s", strings.Join(serverKeys, ", ")))
			}
			hints = append(hints, "To see available tools for a server, use listToolFiles and readToolFile.")
		}
	} else if strings.Contains(errorMessage, "Cannot read property") ||
		strings.Contains(errorMessage, "Cannot read properties") ||
		strings.Contains(errorMessage, "is not an object") {
		hints = append(hints, "You're trying to access a property that doesn't exist or is undefined.")
		hints = append(hints, "The tool response structure might be different than expected.")
		hints = append(hints, "Check the console logs above to see the actual response structure from the tool.")
		hints = append(hints, "Add console.log() statements to inspect the response before accessing properties.")
		hints = append(hints, "Example: console.log('searchResults:', searchResults);")
		if len(serverKeys) > 0 {
			hints = append(hints, fmt.Sprintf("Available server keys: %s", strings.Join(serverKeys, ", ")))
		}
	} else {
		hints = append(hints, "Check the error message above for details.")
		hints = append(hints, "Check the console logs above to see tool responses and debug the issue.")
		if len(serverKeys) > 0 {
			hints = append(hints, fmt.Sprintf("Available server keys: %s", strings.Join(serverKeys, ", ")))
		}
		hints = append(hints, "Ensure you're using the correct syntax: const result = await <serverKey>.<toolName>({ ...args });")
	}

	return hints
}

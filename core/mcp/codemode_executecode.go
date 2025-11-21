package mcp

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/dop251/goja"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/schemas"
)

// ExecutionResult represents the result of code execution
type ExecutionResult struct {
	Result      interface{}          `json:"result"`
	Logs        []string             `json:"logs"`
	Errors      *ExecutionError      `json:"errors,omitempty"`
	Environment ExecutionEnvironment `json:"environment"`
}

// ExecutionError represents an error during code execution
type ExecutionError struct {
	Kind    string   `json:"kind"` // "compile" or "runtime"
	Message string   `json:"message"`
	Hints   []string `json:"hints"`
}

// ExecutionEnvironment contains information about the execution environment
type ExecutionEnvironment struct {
	ServerKeys      []string `json:"serverKeys"`
	ImportsStripped bool     `json:"importsStripped"`
	StrippedLines   []int    `json:"strippedLines"`
}

// ExpressionInfo contains information about a detected expression
type ExpressionInfo struct {
	IsExpression   bool   `json:"isExpression"`
	StartIndex     int    `json:"startIndex"`
	EndIndex       int    `json:"endIndex"`
	ExpressionType string `json:"expressionType"` // "function_call", "promise_chain", "assignment", etc.
}

const (
	CodeModeLogPrefix = "[CODE MODE]"
)

// createExecuteToolCodeTool creates the executeToolCode tool definition
func (m *CodeModeToolsHandler) createExecuteToolCodeTool() schemas.ChatTool {
	executeToolCodeProps := map[string]interface{}{
		"code": map[string]interface{}{
			"type":        "string",
			"description": "JavaScript code to execute. Import/export statements will be stripped. Use Promises with .then() for async operations. Example: serverName.toolName({arg: 'value'}).then(result => { console.log(result); return result; })",
		},
	}
	return schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name: "executeToolCode",
			Description: schemas.Ptr(
				"Executes JavaScript code inside a sandboxed goja-based VM with access to all connected MCP servers' tools. " +
					"All connected servers are exposed as global objects named after their configuration keys, and each server " +
					"provides async (Promise-returning) functions for every tool available on that server. The canonical usage " +
					"pattern is: <serverName>.<toolName>({ ...args }).then(result => { ... }). Both <serverName> and <toolName> " +
					"should be discovered using listToolFiles and readToolFile. " +

					"The environment is intentionally minimal and has several constraints: " +
					"• ES modules are not supported — any leading import/export statements are automatically stripped and imported symbols will not exist. " +
					"• Browser and Node APIs such as fetch, XMLHttpRequest, axios, require, setTimeout, setInterval, window, and document do not exist. " +
					"• async/await syntax is not supported by goja; use Promises and .then()/.catch() for async flows. " +
					"• Using undefined server names or tool names will result in reference or function errors. " +
					"• The VM does not emulate a browser or Node.js environment — no DOM, timers, modules, or network APIs are available. " +
					"• Only ES5.1+ features supported by goja are guaranteed to work. " +

					"The runtime will automatically attempt to 'auto-return' the last expression in your code unless a top-level " +
					"return is present, enabling REPL-like behavior. Console output (log, error, warn, info) is captured and returned. " +
					"Long-running or blocked operations are interrupted via execution timeout. " +
					"This tool is designed specifically for orchestrating MCP tool calls and lightweight JavaScript computation.",
			),

			Parameters: &schemas.ToolFunctionParameters{
				Type:       "object",
				Properties: &executeToolCodeProps,
				Required:   []string{"code"},
			},
		},
	}
}

// handleExecuteToolCode handles the executeToolCode tool call
func (m *CodeModeToolsHandler) handleExecuteToolCode(ctx context.Context, toolCall schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, error) {
	// Parse tool arguments
	var arguments map[string]interface{}
	if err := sonic.Unmarshal([]byte(toolCall.Function.Arguments), &arguments); err != nil {
		return nil, fmt.Errorf("failed to parse tool arguments: %v", err)
	}

	code, ok := arguments["code"].(string)
	if !ok || code == "" {
		return nil, fmt.Errorf("code parameter is required and must be a non-empty string")
	}

	// Execute the code
	result := m.executeCode(ctx, code)

	// Format response text
	var responseText string
	if result.Errors != nil {
		logsText := ""
		if len(result.Logs) > 0 {
			logsText = fmt.Sprintf("\n\nConsole/Log Output:\n%s\n",
				strings.Join(result.Logs, "\n"))
		}
		responseText = fmt.Sprintf(
			"Execution %s error:\n\n%s\n\nHints:\n%s%s\n\nEnvironment:\n  Available server keys: %s\n  Imports stripped: %s",
			result.Errors.Kind,
			result.Errors.Message,
			strings.Join(result.Errors.Hints, "\n"),
			logsText,
			strings.Join(result.Environment.ServerKeys, ", "),
			map[bool]string{true: "Yes", false: "No"}[result.Environment.ImportsStripped],
		)
		if len(result.Environment.StrippedLines) > 0 {
			strippedStr := make([]string, len(result.Environment.StrippedLines))
			for i, line := range result.Environment.StrippedLines {
				strippedStr[i] = fmt.Sprintf("%d", line)
			}
			responseText += fmt.Sprintf("\n  Stripped lines: %s", strings.Join(strippedStr, ", "))
		}
	} else {
		// Success case
		if len(result.Logs) > 0 {
			responseText = fmt.Sprintf("Console output:\n%s\n\nExecution completed successfully.",
				strings.Join(result.Logs, "\n"))
		} else {
			responseText = "Execution completed successfully."
		}
		if result.Result != nil {
			resultJSON, err := sonic.MarshalIndent(result.Result, "", "  ")
			if err == nil {
				responseText += fmt.Sprintf("\nReturn value: %s", string(resultJSON))
			}
		}

		// Add environment information for successful executions
		responseText += fmt.Sprintf("\n\nEnvironment:\n  Available server keys: %s\n  Imports stripped: %s",
			strings.Join(result.Environment.ServerKeys, ", "),
			map[bool]string{true: "Yes", false: "No"}[result.Environment.ImportsStripped])
		if len(result.Environment.StrippedLines) > 0 {
			strippedStr := make([]string, len(result.Environment.StrippedLines))
			for i, line := range result.Environment.StrippedLines {
				strippedStr[i] = fmt.Sprintf("%d", line)
			}
			responseText += fmt.Sprintf("\n  Stripped lines: %s", strings.Join(strippedStr, ", "))
		}
		responseText += "\nNote: Browser APIs like fetch, setTimeout are not available. Use MCP tools for external interactions."
	}

	return createToolResponseMessage(toolCall, responseText), nil
}

// executeCode executes JavaScript code in a sandboxed VM with MCP tool bindings
func (m *CodeModeToolsHandler) executeCode(ctx context.Context, code string) ExecutionResult {
	logs := []string{}
	strippedLines := []int{}

	logger.Debug(fmt.Sprintf("%s Starting code execution", CodeModeLogPrefix))

	// Step 1: Convert literal \n escape sequences to actual newlines first
	// This ensures multiline code and import/export stripping work correctly
	codeWithNewlines := strings.ReplaceAll(code, "\\n", "\n")

	// Step 2: Strip import/export statements
	cleanedCode, strippedLineNumbers := m.stripImportsAndExports(codeWithNewlines)
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
			},
		}
	}

	// Step 4: Create timeout context early so goroutines can use it
	timeoutCtx, cancel := context.WithTimeout(ctx, m.toolExecutionTimeout)
	defer cancel()

	// Step 4: Build bindings for all connected servers
	availableToolsPerClient := m.toolsFetcherFunc(ctx)
	bindings := make(map[string]map[string]interface{})
	serverKeys := make([]string, 0, len(availableToolsPerClient))

	for clientName, tools := range availableToolsPerClient {
		if len(tools) == 0 {
			continue
		}
		serverKeys = append(serverKeys, clientName)

		toolFunctions := make(map[string]interface{})

		// Create a function for each tool
		for _, tool := range tools {
			if tool.Function == nil || tool.Function.Name == "" {
				continue
			}

			originalToolName := tool.Function.Name
			// Parse tool name for JavaScript compatibility (used as property name in virtual files)
			parsedToolName := parseToolName(originalToolName)

			// Capture variables for closure
			toolNameCopy := originalToolName // Keep original for MCP call
			clientNameCopy := clientName

			// Store tool name and client name for later function creation
			// Use parsed name as key (this will be the property name in JavaScript)
			toolFunctions[parsedToolName] = map[string]string{
				"toolName":   toolNameCopy,
				"clientName": clientNameCopy,
			}
		}

		bindings[clientName] = toolFunctions
	}

	if len(serverKeys) > 0 {
		logger.Debug(fmt.Sprintf("%s Bound %d servers with tools", CodeModeLogPrefix, len(serverKeys)))
	}

	var wrappedCode string
	// Check for async/await syntax (goja doesn't support it)
	// Use regex to catch various patterns: async function, async () =>, await expression, etc.
	asyncPattern := regexp.MustCompile(`\basync\b`)
	awaitPattern := regexp.MustCompile(`\bawait\b`)
	if asyncPattern.MatchString(trimmedCode) || awaitPattern.MatchString(trimmedCode) {
		// Return a compile error for async/await usage
		return ExecutionResult{
			Result: nil,
			Logs:   logs,
			Errors: &ExecutionError{
				Kind:    "runtime",
				Message: "async/await syntax is not supported. Use Promises with .then() and .catch() instead.",
				Hints: []string{
					"Use Promises: serverName.toolName({...}).then(result => { ... })",
					"Use .catch() for error handling: promise.catch(err => { ... })",
				},
			},
			Environment: ExecutionEnvironment{
				ServerKeys:      serverKeys,
				ImportsStripped: len(strippedLines) > 0,
				StrippedLines:   strippedLines,
			},
		}
	}

	// Step 5: Auto-return the last expression if needed
	codeWithAutoReturn := m.addAutoReturnIfNeeded(trimmedCode)
	if codeWithAutoReturn != trimmedCode {
		logger.Debug(fmt.Sprintf("%s Auto-return added", CodeModeLogPrefix))
	}

	// Step 6: Wrap code in an IIFE to allow top-level return statements
	// Note: goja doesn't support async/await syntax, so users should use Promises directly
	// Example: serverName.toolName({...}).then(result => { ... })
	// Convert literal \n escape sequences to actual newlines (for multiline code)
	// This handles cases where code contains literal \n characters that should be newlines
	codeToWrap := strings.ReplaceAll(codeWithAutoReturn, "\\n", "\n")
	// Trim trailing newlines to avoid issues when wrapping
	codeToWrap = strings.TrimRight(codeToWrap, "\n\r")
	wrappedCode = fmt.Sprintf("(function() {\n%s\n})()", codeToWrap)

	// Step 7: Create goja runtime
	vm := goja.New()

	// Step 8: Set up console
	consoleObj := vm.NewObject()
	consoleObj.Set("log", func(args ...interface{}) {
		message := m.formatConsoleArgs(args)
		logs = append(logs, message)
	})
	consoleObj.Set("error", func(args ...interface{}) {
		message := m.formatConsoleArgs(args)
		logs = append(logs, fmt.Sprintf("[ERROR] %s", message))
	})
	consoleObj.Set("warn", func(args ...interface{}) {
		message := m.formatConsoleArgs(args)
		logs = append(logs, fmt.Sprintf("[WARN] %s", message))
	})
	consoleObj.Set("info", func(args ...interface{}) {
		message := m.formatConsoleArgs(args)
		logs = append(logs, fmt.Sprintf("[INFO] %s", message))
	})
	vm.Set("console", consoleObj)

	// Step 9: Set up server bindings
	for serverKey, tools := range bindings {
		serverObj := vm.NewObject()
		for toolName, toolInfo := range tools {
			// Get the actual tool name and client name
			toolInfoMap := toolInfo.(map[string]string)
			toolNameCopy := toolInfoMap["toolName"]
			clientNameCopy := toolInfoMap["clientName"]

			// Create the async function that returns a Promise
			// Capture variables for closure
			toolNameFinal := toolNameCopy
			clientNameFinal := clientNameCopy

			serverObj.Set(toolName, func(call goja.FunctionCall) goja.Value {
				args := call.Argument(0).Export()

				// Create promise first so we can reject it on error
				promise, resolve, reject := vm.NewPromise()

				// Convert args to map[string]interface{}
				argsMap, ok := args.(map[string]interface{})
				if !ok {
					logger.Debug(fmt.Sprintf("%s Invalid args type for %s.%s: expected object, got %T",
						CodeModeLogPrefix, clientNameFinal, toolNameFinal, args))
					err := fmt.Errorf("expected object argument, got %T", args)
					reject(vm.ToValue(err))
					return vm.ToValue(promise)
				}

				// Call tool asynchronously with timeout context and panic recovery
				go func() {
					defer func() {
						if r := recover(); r != nil {
							logger.Debug(fmt.Sprintf("%s Panic in tool call goroutine for %s.%s: %v",
								CodeModeLogPrefix, clientNameFinal, toolNameFinal, r))
							reject(vm.ToValue(fmt.Errorf("tool call panic: %v", r)))
						}
					}()

					// Check if context is already cancelled before starting
					select {
					case <-timeoutCtx.Done():
						reject(vm.ToValue(fmt.Errorf("execution timeout")))
						return
					default:
					}

					result, err := m.callMCPTool(timeoutCtx, clientNameFinal, toolNameFinal, argsMap, logs)

					// Check if context was cancelled during execution
					select {
					case <-timeoutCtx.Done():
						reject(vm.ToValue(fmt.Errorf("execution timeout")))
						return
					default:
					}

					if err != nil {
						logger.Debug(fmt.Sprintf("%s Tool call failed: %s.%s - %v",
							CodeModeLogPrefix, clientNameFinal, toolNameFinal, err))
						reject(vm.ToValue(err))
						return
					}
					resolve(vm.ToValue(result))
				}()

				return vm.ToValue(promise)
			})
		}
		vm.Set(serverKey, serverObj)
	}

	// Step 10: Set up environment info
	envObj := vm.NewObject()
	envObj.Set("serverKeys", serverKeys)
	envObj.Set("version", "1.0.0")
	vm.Set("__MCP_ENV__", envObj)

	// Step 11: Execute code with timeout

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
							select {
							case errChan <- fmt.Errorf("%v", err.Export()):
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
		hints := m.generateErrorHints(errorMessage, serverKeys)
		logger.Debug(fmt.Sprintf("%s Execution failed: %s", CodeModeLogPrefix, errorMessage))

		return ExecutionResult{
			Result: nil,
			Logs:   logs,
			Errors: &ExecutionError{
				Kind:    "runtime",
				Message: errorMessage,
				Hints:   hints,
			},
			Environment: ExecutionEnvironment{
				ServerKeys:      serverKeys,
				ImportsStripped: len(strippedLines) > 0,
				StrippedLines:   strippedLines,
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
		},
	}
}

// callMCPTool calls an MCP tool and returns the result
func (m *CodeModeToolsHandler) callMCPTool(ctx context.Context, clientName, toolName string, args map[string]interface{}, logs []string) (interface{}, error) {
	// Get available tools per client
	availableToolsPerClient := m.toolsFetcherFunc(ctx)

	// Find the client by name
	tools, exists := availableToolsPerClient[clientName]
	if !exists || len(tools) == 0 {
		return nil, fmt.Errorf("client not found for server name: %s", clientName)
	}

	// Get client using a tool from this client
	var client *schemas.MCPClientState
	if tools[0].Function != nil {
		client = m.clientForToolFetcher(tools[0].Function.Name)
	}

	if client == nil {
		return nil, fmt.Errorf("client not found for server name: %s", clientName)
	}

	// Call the tool via MCP client
	callRequest := mcp.CallToolRequest{
		Request: mcp.Request{
			Method: string(mcp.MethodToolsCall),
		},
		Params: mcp.CallToolParams{
			Name:      toolName,
			Arguments: args,
		},
	}

	// Create timeout context
	toolCtx, cancel := context.WithTimeout(ctx, m.toolExecutionTimeout)
	defer cancel()

	toolResponse, callErr := client.Conn.CallTool(toolCtx, callRequest)
	if callErr != nil {
		logger.Debug(fmt.Sprintf("%s Tool call failed: %s.%s - %v", CodeModeLogPrefix, clientName, toolName, callErr))
		logs = append(logs, fmt.Sprintf("[TOOL] %s.%s error: %v", clientName, toolName, callErr))
		return nil, fmt.Errorf("tool call failed for %s.%s: %v", clientName, toolName, callErr)
	}

	// Extract result
	rawResult := extractTextFromMCPResponse(toolResponse, toolName)

	// Check if this is an error result (from NewToolResultError)
	// Error results start with "Error: " prefix
	if strings.HasPrefix(rawResult, "Error: ") {
		errorMsg := strings.TrimPrefix(rawResult, "Error: ")
		logger.Debug(fmt.Sprintf("%s Tool returned error result: %s.%s - %s", CodeModeLogPrefix, clientName, toolName, errorMsg))
		logs = append(logs, fmt.Sprintf("[TOOL] %s.%s error result: %s", clientName, toolName, errorMsg))
		return nil, fmt.Errorf("%s", errorMsg)
	}

	// Try to parse as JSON, otherwise use as string
	var finalResult interface{}
	if err := sonic.Unmarshal([]byte(rawResult), &finalResult); err != nil {
		// Not JSON, use as string
		finalResult = rawResult
	}

	// Log the result
	resultStr := m.formatResultForLog(finalResult)
	logs = append(logs, fmt.Sprintf("[TOOL] %s.%s raw response: %s", clientName, toolName, resultStr))

	return finalResult, nil
}

// formatResultForLog formats a result for logging
func (m *CodeModeToolsHandler) formatResultForLog(result interface{}) string {
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

// formatConsoleArgs formats console arguments for logging
func (m *CodeModeToolsHandler) formatConsoleArgs(args []interface{}) string {
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

// stripImportsAndExports strips import and export statements from code
func (m *CodeModeToolsHandler) stripImportsAndExports(code string) (string, []int) {
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

// addAutoReturnIfNeeded adds a return statement if the last statement is an expression
func (m *CodeModeToolsHandler) addAutoReturnIfNeeded(code string) string {
	lines := strings.Split(code, "\n")
	if len(lines) == 0 {
		return code
	}

	// Check if code already has a top-level return statement
	// We need to be more careful here - return statements inside callbacks don't count
	hasTopLevelReturn := false
	for i, line := range lines {
		trimmedLine := strings.TrimSpace(line)
		if strings.HasPrefix(trimmedLine, "return ") {
			// Check if this return is at the top level (not inside a function/callback)
			// Simple heuristic: if there are unmatched opening braces before this line, it's likely inside a callback
			precedingCode := strings.Join(lines[:i], "\n")
			openBraces := strings.Count(precedingCode, "{") - strings.Count(precedingCode, "}")
			if openBraces == 0 {
				hasTopLevelReturn = true
				break
			}
		}
	}

	if hasTopLevelReturn {
		return code
	}

	// Find the last non-empty, non-comment line
	lastStatementIndex := -1
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") {
			continue
		}
		lastStatementIndex = i
		break
	}

	if lastStatementIndex == -1 {
		return code
	}

	lastLine := lines[lastStatementIndex]
	trimmed := strings.TrimSpace(lastLine)

	// Check if it's a declaration (should not add return)
	declarationRegex := regexp.MustCompile(`^\s*(function|const|let|var|class|interface|type|enum|async\s+function)\b`)
	if declarationRegex.MatchString(trimmed) {
		return code
	}

	// Check if it's a control flow statement (should not add return)
	controlFlowRegex := regexp.MustCompile(`^\s*(if|for|while|switch|try|catch|finally)\b`)
	if controlFlowRegex.MatchString(trimmed) {
		return code
	}

	// Check if it ends with a semicolon (likely a statement, not expression)
	// BUT make an exception for promise chains that end with semicolon
	if strings.HasSuffix(trimmed, ";") {
		// Check if this is a promise chain ending with semicolon
		if !strings.Contains(strings.Join(lines, "\n"), ".then(") && !strings.Contains(strings.Join(lines, "\n"), ".catch(") {
			return code
		}
	}

	// First, check for MCP tool calls at the beginning - these should always have return added
	// We need to get the server keys to validate against
	availableToolsPerClient := m.toolsFetcherFunc(context.Background())
	serverKeys := make([]string, 0, len(availableToolsPerClient))
	for clientName := range availableToolsPerClient {
		serverKeys = append(serverKeys, clientName)
	}

	mcpToolCallStart := m.findMCPToolCallStart(lines, serverKeys)
	if mcpToolCallStart != -1 {
		// Add return to the MCP tool call
		indent := ""
		if mcpToolCallStart < len(lines) {
			startLine := lines[mcpToolCallStart]
			indent = startLine[:len(startLine)-len(strings.TrimLeft(startLine, " \t"))]
		}
		lines[mcpToolCallStart] = indent + "return " + strings.TrimLeft(lines[mcpToolCallStart], " \t")
		return strings.Join(lines, "\n")
	}

	// Use sophisticated expression analysis for other cases
	expressionInfo := m.analyzeExpression(lines, lastStatementIndex)

	if expressionInfo.IsExpression {
		// Add return to the start of the expression
		indent := ""
		if expressionInfo.StartIndex < len(lines) {
			startLine := lines[expressionInfo.StartIndex]
			indent = startLine[:len(startLine)-len(strings.TrimLeft(startLine, " \t"))]
		}
		lines[expressionInfo.StartIndex] = indent + "return " + strings.TrimLeft(lines[expressionInfo.StartIndex], " \t")
		return strings.Join(lines, "\n")
	}

	return code
}

// findMCPToolCallStart finds the first line that starts an MCP tool call
func (m *CodeModeToolsHandler) findMCPToolCallStart(lines []string, serverKeys []string) int {
	// Pattern to match MCP tool calls: serverName.TOOL_NAME(
	mcpToolCallRegex := regexp.MustCompile(`^\s*([a-zA-Z_$][\w$]*)\.([\w_]+)\s*\(`)

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		// Check if this line starts an MCP tool call
		if matches := mcpToolCallRegex.FindStringSubmatch(line); matches != nil {
			serverName := matches[1]
			toolName := matches[2]

			// Check if the server name matches one of our known MCP servers
			for _, knownServer := range serverKeys {
				if serverName == knownServer {
					return i
				}
			}

			// Fallback: check if it matches MCP tool naming pattern (ALL_CAPS with underscores)
			if strings.ToUpper(toolName) == toolName && strings.Contains(toolName, "_") {
				return i
			}
		}
	}

	return -1
}

// analyzeExpression performs sophisticated analysis to detect if the code ends with an expression
func (m *CodeModeToolsHandler) analyzeExpression(lines []string, lastIndex int) ExpressionInfo {
	if lastIndex < 0 || lastIndex >= len(lines) {
		return ExpressionInfo{IsExpression: false}
	}

	// Start from the last line and work backwards
	parenDepth := 0
	braceDepth := 0
	bracketDepth := 0

	// Analyze the structure by looking at brackets and parentheses
	for i := lastIndex; i >= 0; i-- {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		// Skip empty lines
		if trimmed == "" {
			continue
		}

		// Analyze character by character for the current line
		inString := false
		stringChar := byte(0)

		for j := len(line) - 1; j >= 0; j-- {
			char := line[j]

			// Handle string literals (simple approach - doesn't handle escaped quotes perfectly)
			if (char == '"' || char == '\'' || char == '`') && (j == 0 || line[j-1] != '\\') {
				if inString && char == stringChar {
					inString = false
					stringChar = 0
				} else if !inString {
					inString = true
					stringChar = char
				}
				continue
			}

			if inString {
				continue
			}

			// Count brackets and parentheses
			switch char {
			case ')':
				parenDepth++
			case '(':
				parenDepth--
			case '}':
				braceDepth++
			case '{':
				braceDepth--
			case ']':
				bracketDepth++
			case '[':
				bracketDepth--
			}
		}

		// Check if this line could be the start of the expression
		if parenDepth == 0 && braceDepth == 0 && bracketDepth == 0 {
			// This might be the start of the expression
			expressionType := m.classifyExpression(lines, i, lastIndex)

			if expressionType != "" {
				return ExpressionInfo{
					IsExpression:   true,
					StartIndex:     i,
					EndIndex:       lastIndex,
					ExpressionType: expressionType,
				}
			}
		}

		// Don't look too far back (reasonable limit)
		if lastIndex-i > 20 {
			break
		}
	}

	// Fallback: check if the last line looks like a simple expression
	lastLine := strings.TrimSpace(lines[lastIndex])

	if m.isSimpleExpression(lastLine) {
		return ExpressionInfo{
			IsExpression:   true,
			StartIndex:     lastIndex,
			EndIndex:       lastIndex,
			ExpressionType: "simple",
		}
	}

	return ExpressionInfo{IsExpression: false}
}

// classifyExpression determines what type of expression this is
func (m *CodeModeToolsHandler) classifyExpression(lines []string, startIndex, endIndex int) string {
	if startIndex < 0 || endIndex >= len(lines) || startIndex > endIndex {
		return ""
	}

	// Get the full expression text
	expressionLines := lines[startIndex : endIndex+1]
	fullExpression := strings.Join(expressionLines, "\n")

	// Check for different expression types
	// Promise chains - look for .then(), .catch(), .finally() patterns
	promiseChainRegex := regexp.MustCompile(`\.(then|catch|finally)\s*\(`)
	if promiseChainRegex.MatchString(fullExpression) {
		return "promise_chain"
	}

	// Check for function calls (including MCP tool calls)
	functionCallRegex := regexp.MustCompile(`[a-zA-Z_$][\w$]*(?:\.[a-zA-Z_$][\w$]*)*\s*\(`)
	if functionCallRegex.MatchString(fullExpression) {
		return "function_call"
	}

	// Check for await expressions
	if strings.Contains(fullExpression, "await ") {
		return "await_expression"
	}

	// Check for object/array literals that aren't assignments
	firstLine := strings.TrimSpace(lines[startIndex])
	if (strings.HasPrefix(firstLine, "{") || strings.HasPrefix(firstLine, "[")) &&
		!strings.Contains(firstLine, "=") {
		return "literal"
	}

	return ""
}

// isSimpleExpression checks if a single line looks like a simple expression
func (m *CodeModeToolsHandler) isSimpleExpression(line string) bool {
	trimmed := strings.TrimSpace(line)

	// Empty line is not an expression
	if trimmed == "" {
		return false
	}

	// Lines ending with semicolon are statements
	if strings.HasSuffix(trimmed, ";") {
		return false
	}

	// Lines with assignments are usually statements (but not comparisons)
	if strings.Contains(trimmed, "=") && !strings.Contains(trimmed, "==") && !strings.Contains(trimmed, "!=") && !strings.Contains(trimmed, "<=") && !strings.Contains(trimmed, ">=") {
		return false
	}

	// Check for promise chain patterns (lines ending with method calls)
	promiseChainEndRegex := regexp.MustCompile(`\.(then|catch|finally)\s*\([^)]*\)\s*;?\s*$`)
	if promiseChainEndRegex.MatchString(trimmed) {
		return true
	}

	// Function calls without assignment
	functionCallRegex := regexp.MustCompile(`^[a-zA-Z_$][\w$]*(?:\.[a-zA-Z_$][\w$]*)*\s*\(.*\)$`)
	if functionCallRegex.MatchString(trimmed) {
		return true
	}

	// Simple identifiers or property access
	identifierRegex := regexp.MustCompile(`^[a-zA-Z_$][\w$]*(?:\.[a-zA-Z_$][\w$]*)*$`)
	if identifierRegex.MatchString(trimmed) {
		return true
	}

	return false
}

// generateErrorHints generates helpful hints based on error messages
func (m *CodeModeToolsHandler) generateErrorHints(errorMessage string, serverKeys []string) []string {
	hints := []string{}

	if strings.Contains(errorMessage, "is not defined") {
		re := regexp.MustCompile(`(\w+)\s+is not defined`)
		if match := re.FindStringSubmatch(errorMessage); len(match) > 1 {
			undefinedVar := match[1]

			// Special handling for common browser/Node.js APIs
			if undefinedVar == "fetch" {
				hints = append(hints, "The 'fetch' API is not available in this JavaScript environment.")
				hints = append(hints, "Instead of using fetch for HTTP requests, use the available MCP tools.")
				if len(serverKeys) > 0 {
					hints = append(hints, fmt.Sprintf("Available server keys: %s", strings.Join(serverKeys, ", ")))
					hints = append(hints, fmt.Sprintf("Example: await %s.<toolName>({ url: 'https://example.com' })", serverKeys[0]))
				}
				hints = append(hints, "MCP tools handle HTTP requests, file operations, and other external interactions.")
				return hints
			} else if undefinedVar == "XMLHttpRequest" || undefinedVar == "axios" {
				hints = append(hints, fmt.Sprintf("The '%s' API is not available in this JavaScript environment.", undefinedVar))
				hints = append(hints, "Use MCP tools instead for HTTP requests and external API calls.")
				if len(serverKeys) > 0 {
					hints = append(hints, fmt.Sprintf("Available server keys: %s", strings.Join(serverKeys, ", ")))
				}
				return hints
			} else if undefinedVar == "setTimeout" || undefinedVar == "setInterval" {
				hints = append(hints, fmt.Sprintf("The '%s' API is not available in this JavaScript environment.", undefinedVar))
				hints = append(hints, "This is a sandboxed environment focused on MCP tool interactions.")
				hints = append(hints, "Use Promise chains with MCP tools instead of timing functions.")
				return hints
			} else if undefinedVar == "require" || undefinedVar == "import" {
				hints = append(hints, "Module imports are not supported in this JavaScript environment.")
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
				hints = append(hints, fmt.Sprintf("For example: await %s.<toolName>({ ... })", serverKeys[0]))
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
		hints = append(hints, "Ensure you're using the correct syntax: await <serverKey>.<toolName>({ ...args })")
	}

	return hints
}

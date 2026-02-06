package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/schemas"
)

// RetryConfig defines the retry behavior with exponential backoff
type RetryConfig struct {
	MaxRetries     int           // Maximum number of retry attempts (not including the initial attempt)
	InitialBackoff time.Duration // Initial backoff duration
	MaxBackoff     time.Duration // Maximum backoff duration
}

var DefaultRetryConfig = RetryConfig{
	MaxRetries:     5,
	InitialBackoff: 1 * time.Second,
	MaxBackoff:     30 * time.Second,
}

// GetClientForTool safely finds a client that has the specified tool.
// Returns a copy of the client state to avoid data races. Callers should be aware
// that fields like Conn and ToolMap are still shared references and may be modified
// by other goroutines, but the struct itself is safe from concurrent modification.
func (m *MCPManager) GetClientForTool(toolName string) *schemas.MCPClientState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, client := range m.clientMap {
		// All tools (both internal and external) are now stored with prefix "clientName-toolName"
		// This ensures consistent behavior across all MCP clients
		if _, exists := client.ToolMap[toolName]; exists {
			// Return a copy to prevent TOCTOU race conditions
			clientCopy := *client
			return &clientCopy
		}
	}
	return nil
}

// GetToolPerClient returns all tools from connected MCP clients.
// Applies client filtering if specified in the context.
// Returns a map of client name to its available tools.
// Parameters:
//   - ctx: Execution context
//
// Returns:
//   - map[string][]schemas.ChatTool: Map of client name to its available tools
func (m *MCPManager) GetToolPerClient(ctx context.Context) map[string][]schemas.ChatTool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var includeClients []string

	// Extract client filtering from request context
	if existingIncludeClients, ok := ctx.Value(MCPContextKeyIncludeClients).([]string); ok && existingIncludeClients != nil {
		includeClients = existingIncludeClients
	}

	m.logger.Debug("%s GetToolPerClient: Total clients in manager: %d, Filter: %v", MCPLogPrefix, len(m.clientMap), includeClients)

	tools := make(map[string][]schemas.ChatTool)
	for _, client := range m.clientMap {
		// Use client name as the key (not ID)
		clientName := client.ExecutionConfig.Name
		clientID := client.ExecutionConfig.ID

		m.logger.Debug("%s Evaluating client %s (ID: %s) for tools", MCPLogPrefix, clientName, clientID)

		// Apply client filtering logic - check both ID and Name for compatibility
		if !shouldIncludeClient(clientName, includeClients, m.logger) {
			m.logger.Debug("%s Skipping MCP client %s: not in include clients list", MCPLogPrefix, clientName)
			continue
		}

		// Add all tools from this client
		// FILTERING HIERARCHY (restrictive, not permissive):
		// 1. Client-level configuration (ToolsToExecute) - Global allow-list, most restrictive
		// 2. Request context (MCPContextKeyIncludeTools) - Can only further narrow, not expand
		// Context filtering CANNOT override client configuration - it can only be more restrictive.
		for toolName, tool := range client.ToolMap {
			// First check: Client configuration is the global allow-list
			// If client config blocks a tool, it CANNOT be overridden by context
			if shouldSkipToolForConfig(toolName, client.ExecutionConfig) {
				continue
			}

			// Second check: Request context can further narrow the allowed tools
			// Context can only restrict, not expand beyond client configuration
			if shouldSkipToolForRequest(ctx, clientName, toolName) {
				continue
			}

			tools[clientName] = append(tools[clientName], tool)
		}
		if len(tools[clientName]) > 0 {
			m.logger.Debug("%s Added %d tools for MCP client %s", MCPLogPrefix, len(tools[clientName]), clientName)
		}
	}
	return tools
}

// GetClientByName returns a client by name.
//
// Parameters:
//   - clientName: Name of the client to get
//
// Returns:
//   - *schemas.MCPClientState: Client state if found, nil otherwise
func (m *MCPManager) GetClientByName(clientName string) *schemas.MCPClientState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	m.logger.Debug("%s GetClientByName: Looking for client '%s' among %d clients", MCPLogPrefix, clientName, len(m.clientMap))
	for _, client := range m.clientMap {
		m.logger.Debug("%s Checking client with Name: %s, ID: %s", MCPLogPrefix, client.ExecutionConfig.Name, client.ExecutionConfig.ID)
		if client.ExecutionConfig.Name == clientName {
			// Return a copy to prevent TOCTOU race conditions
			// The caller receives a snapshot of the client state at this point in time
			m.logger.Debug("%s Found client '%s' with IsCodeModeClient=%v", MCPLogPrefix, clientName, client.ExecutionConfig.IsCodeModeClient)
			clientCopy := *client
			return &clientCopy
		}
	}
	m.logger.Debug("%s Client '%s' not found", MCPLogPrefix, clientName)
	return nil
}

// isTransientError determines if an error is transient and should be retried.
// Permanent errors (auth failures, config errors, context deadline, etc.) return false.
// Transient errors (network issues, temporary timeouts, etc.) return true.
func isTransientError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()

	// Context errors are NEVER retryable - they indicate the operation exceeded its deadline
	// If context is cancelled or deadline exceeded, the issue is permanent (not transient)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if strings.Contains(errStr, "context canceled") || strings.Contains(errStr, "context deadline exceeded") {
		return false
	}

	// Permanent errors that should NOT be retried
	permanentErrors := []string{
		// Authentication/authorization errors
		"401", "403", "unauthorized", "forbidden", "invalid auth", "invalid credential",
		// HTTP client errors
		"400", "405", "422", "bad request", "method not allowed",
		// Configuration errors
		"command not found", "no such file", "not found", "permission denied",
		"invalid config",
		// Command execution errors
		"executable file not found", "permission denied", "command failed",
		// Timeout errors - if something times out, retrying won't help
		"timeout", "deadline exceeded", "waiting for endpoint",
	}

	for _, permanentErr := range permanentErrors {
		if strings.Contains(strings.ToLower(errStr), permanentErr) {
			return false
		}
	}

	// Transient errors that SHOULD be retried
	transientErrors := []string{
		// Network errors
		"connection refused", "connection reset", "broken pipe",
		"network is unreachable", "no route to host",
		// Timeout errors
		"timeout", "deadline exceeded", "i/o timeout",
		// DNS errors
		"no such host", "name resolution failed",
		// HTTP errors
		"503", "502", "504", "429", "500", // Service Unavailable, Bad Gateway, Gateway Timeout, Too Many Requests, Internal Server Error
		// Connection errors
		"connection error", "connection lost", "connection failed",
		// I/O errors
		"i/o error", "read error", "write error",
		// Temporary errors
		"temporary failure", "try again",
	}

	for _, transientErr := range transientErrors {
		if strings.Contains(strings.ToLower(errStr), transientErr) {
			return true
		}
	}

	// Check for net.Error types (timeout-related errors)
	var netErr net.Error
	if errors.As(err, &netErr) {
		// Timeout errors are transient and should be retried
		if netErr.Timeout() {
			return true
		}
	}

	// Default: treat as transient to be safe (connection-related errors)
	// This ensures we retry unknown errors that are likely transient
	return true
}

// ExecuteWithRetry executes a function with exponential backoff retry logic.
// Only retries on transient errors; permanent errors (auth, config) fail immediately.
// It returns the error from the last attempt if all retries fail.
//
// Parameters:
//   - ctx: Context for cancellation
//   - fn: Function to execute with retry logic
//   - config: Retry configuration
//   - logger: Logger for logging retries
//
// Returns:
//   - error: The last error if all retries failed, nil if successful
func ExecuteWithRetry(
	ctx context.Context,
	fn func() error,
	config RetryConfig,
	logger schemas.Logger,
) error {
	var lastErr error
	backoff := config.InitialBackoff

	for attempt := 0; attempt <= config.MaxRetries; attempt++ {
		// Check context before attempting
		select {
		case <-ctx.Done():
			return fmt.Errorf("retry context cancelled: %w", ctx.Err())
		default:
		}

		// Execute the function
		lastErr = fn()
		if lastErr == nil {
			return nil // Success on this attempt
		}

		// Check if error is transient - if not, fail immediately without retrying
		if !isTransientError(lastErr) {
			logger.Debug("%s permanent error (not retrying): %v", MCPLogPrefix, lastErr)
			return lastErr
		}

		// If this was the last attempt, return the error
		if attempt == config.MaxRetries {
			return lastErr
		}

		logger.Debug("%s retrying after %s for attempt %d/%d (transient error): %v", MCPLogPrefix, backoff, attempt+1, config.MaxRetries, lastErr)

		// Wait before next attempt (with context cancellation support)
		select {
		case <-ctx.Done():
			return fmt.Errorf("retry context cancelled: %w", ctx.Err())
		case <-time.After(backoff):
			// Continue to next attempt
		}

		// Update backoff for next iteration
		backoff = time.Duration(float64(backoff) * 2)
		if backoff > config.MaxBackoff {
			backoff = config.MaxBackoff
		}
	}

	return lastErr
}

// retrieveExternalTools retrieves and filters tools from an external MCP server without holding locks.
// Uses exponential backoff retry logic (5 retries, 1-30 seconds) for tool retrieval.
// Returns both the tools map and a name mapping (sanitized_name -> original_mcp_name) for tool execution.
func retrieveExternalTools(ctx context.Context, client *client.Client, clientName string, logger schemas.Logger) (map[string]schemas.ChatTool, map[string]string, error) {
	// Get available tools from external server with retry logic
	listRequest := mcp.ListToolsRequest{
		PaginatedRequest: mcp.PaginatedRequest{
			Request: mcp.Request{
				Method: string(mcp.MethodToolsList),
			},
		},
	}

	var toolsResponse *mcp.ListToolsResult
	retryConfig := DefaultRetryConfig
	err := ExecuteWithRetry(
		ctx,
		func() error {
			var retrieveErr error
			toolsResponse, retrieveErr = client.ListTools(ctx, listRequest)
			return retrieveErr
		},
		retryConfig,
		logger,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list tools after %d retries: %v", retryConfig.MaxRetries, err)
	}

	if toolsResponse == nil {
		return make(map[string]schemas.ChatTool), make(map[string]string), nil // No tools available
	}

	tools := make(map[string]schemas.ChatTool)
	toolNameMapping := make(map[string]string) // Maps sanitized_name -> original_mcp_name

	// toolsResponse is already a ListToolsResult
	for _, mcpTool := range toolsResponse.Tools {
		// Validate the original tool name (with hyphens replaced by underscores for validation only)
		validationName := strings.ReplaceAll(mcpTool.Name, "-", "_")
		if err := validateNormalizedToolName(validationName); err != nil {
			logger.Warn("%s Skipping MCP tool %q: %v", MCPLogPrefix, mcpTool.Name, err)
			continue
		}

		// Convert MCP tool schema to Bifrost format
		bifrostTool := convertMCPToolToBifrostSchema(&mcpTool, logger)
		// Prefix tool name with client name to make it permanent (using '-' as separator)
		// Keep the original tool name (don't sanitize) so we can call the MCP server correctly
		prefixedToolName := fmt.Sprintf("%s-%s", clientName, mcpTool.Name)
		// Update the tool's function name to match the prefixed name
		if bifrostTool.Function != nil {
			bifrostTool.Function.Name = prefixedToolName
		}
		// Store the tool with the prefixed name
		tools[prefixedToolName] = bifrostTool
		// Store the mapping from sanitized name to original MCP name for later lookup during execution
		sanitizedToolName := strings.ReplaceAll(mcpTool.Name, "-", "_")
		toolNameMapping[sanitizedToolName] = mcpTool.Name
	}

	return tools, toolNameMapping, nil
}

// shouldIncludeClient determines if a client should be included based on filtering rules.
func shouldIncludeClient(clientName string, includeClients []string, logger schemas.Logger) bool {
	// If includeClients is specified (not nil), apply whitelist filtering
	if includeClients != nil {
		// Handle empty array [] - means no clients are included
		if len(includeClients) == 0 {
			logger.Debug("%s shouldIncludeClient: %s - BLOCKED (empty include list)", MCPLogPrefix, clientName)
			return false // No clients allowed
		}

		// Handle wildcard "*" - if present, all clients are included
		if slices.Contains(includeClients, "*") {
			logger.Debug("%s shouldIncludeClient: %s - ALLOWED (wildcard filter)", MCPLogPrefix, clientName)
			return true // All clients allowed
		}

		// Check if specific client is in the list
		included := slices.Contains(includeClients, clientName)
		logger.Debug("%s shouldIncludeClient: %s - %s (filter: %v)", MCPLogPrefix, clientName, map[bool]string{true: "ALLOWED", false: "BLOCKED"}[included], includeClients)
		return included
	}

	// Default: include all clients when no filtering specified (nil case)
	logger.Debug("%s shouldIncludeClient: %s - ALLOWED (no filter)", MCPLogPrefix, clientName)
	return true
}

// shouldSkipToolForConfig checks if a tool should be skipped based on client configuration (without accessing clientMap).
func shouldSkipToolForConfig(toolName string, config *schemas.MCPClientConfig) bool {
	if config == nil {
		return true // No tools allowed
	}
	// If ToolsToExecute is specified (not nil), apply filtering
	if config.ToolsToExecute != nil {
		// Handle empty array [] - means no tools are allowed
		if len(config.ToolsToExecute) == 0 {
			return true // No tools allowed
		}

		// Handle wildcard "*" - if present, all tools are allowed
		if slices.Contains(config.ToolsToExecute, "*") {
			return false // All tools allowed
		}

		// Strip client prefix from tool name before checking
		// Tool names in config are stored without prefix (e.g., "add")
		// but tool names in ToolMap are stored with prefix (e.g., "calculator/add")
		unprefixedToolName := stripClientPrefix(toolName, config.Name)

		// Check if specific tool is in the allowed list
		return !slices.Contains(config.ToolsToExecute, unprefixedToolName) // Tool not in allowed list
	}

	return true // Tool is skipped (nil is treated as [] - no tools)
}

// canAutoExecuteTool checks if a tool can be auto-executed based on client configuration.
// Returns true if the tool can be auto-executed, false otherwise.
func canAutoExecuteTool(toolName string, config *schemas.MCPClientConfig) bool {
	// First check if tool is in ToolsToExecute (must be executable first)
	if shouldSkipToolForConfig(toolName, config) {
		return false // Tool is not in ToolsToExecute, so it cannot be auto-executed
	}

	// If ToolsToAutoExecute is specified (not nil), apply filtering
	if config.ToolsToAutoExecute != nil {
		// Handle empty array [] - means no tools are auto-executed
		if len(config.ToolsToAutoExecute) == 0 {
			return false // No tools auto-executed
		}

		// Handle wildcard "*" - if present, all tools are auto-executed
		if slices.Contains(config.ToolsToAutoExecute, "*") {
			return true // All tools auto-executed
		}

		// Strip client prefix from tool name before checking
		// Tool names in config are stored without prefix (e.g., "add")
		// but tool names in ToolMap are stored with prefix (e.g., "calculator/add")
		unprefixedToolName := stripClientPrefix(toolName, config.Name)

		// Check if specific tool is in the auto-execute list
		return slices.Contains(config.ToolsToAutoExecute, unprefixedToolName)
	}

	return false // Tool is not auto-executed (nil is treated as [] - no tools)
}

// shouldSkipToolForRequest checks if a tool should be skipped based on the request context.
// shouldSkipToolForRequest determines if a tool should be skipped based on request context filtering.
// Context filtering can only NARROW the tools available, NOT expand beyond client configuration.
// This is checked AFTER client-level filtering (shouldSkipToolForConfig).
func shouldSkipToolForRequest(ctx context.Context, clientName, toolName string) bool {
	includeTools := ctx.Value(MCPContextKeyIncludeTools)

	if includeTools != nil {
		// Try []string first (preferred type)
		if includeToolsList, ok := includeTools.([]string); ok {
			// Handle empty array [] - means no tools are included
			if len(includeToolsList) == 0 {
				return true // No tools allowed
			}

			// Handle wildcard "clientName-*" - if present, all tools are included for this client
			if slices.Contains(includeToolsList, fmt.Sprintf("%s-*", clientName)) {
				return false // All tools allowed
			}

			// Check if specific tool is in the list (format: clientName-toolName)
			// Note: toolName is already prefixed when coming from ToolMap, so use it directly
			if slices.Contains(includeToolsList, toolName) {
				return false // Tool is explicitly allowed
			}

			// If includeTools is specified but this tool is not in it, skip it
			return true
		}
	}

	return false // Tool is allowed (default when no filtering specified)
}

// convertMCPToolToBifrostSchema converts an MCP tool definition to Bifrost format.
func convertMCPToolToBifrostSchema(mcpTool *mcp.Tool, logger schemas.Logger) schemas.ChatTool {
	var properties *schemas.OrderedMap
	if len(mcpTool.InputSchema.Properties) > 0 {
		orderedProps := make(schemas.OrderedMap, len(mcpTool.InputSchema.Properties))
		maps.Copy(orderedProps, mcpTool.InputSchema.Properties)

		// Fix array schemas: ensure all array properties have an 'items' field
		FixArraySchemas(orderedProps, logger)

		properties = &orderedProps
	} else {
		// For tools with no parameters, initialize an empty properties map
		// This is required by some providers (e.g., OpenAI) which expect
		// object schemas to always have a properties field, even if empty
		emptyProps := make(schemas.OrderedMap)
		properties = &emptyProps
	}
	return schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        mcpTool.Name,
			Description: schemas.Ptr(mcpTool.Description),
			Parameters: &schemas.ToolFunctionParameters{
				Type:       mcpTool.InputSchema.Type,
				Properties: properties,
				Required:   mcpTool.InputSchema.Required,
			},
		},
	}
}

// extractTextFromMCPResponse extracts text content from an MCP tool response.
func extractTextFromMCPResponse(toolResponse *mcp.CallToolResult, toolName string) string {
	if toolResponse == nil {
		return fmt.Sprintf("MCP tool '%s' executed successfully", toolName)
	}

	var result strings.Builder
	for _, contentBlock := range toolResponse.Content {
		// Handle typed content
		switch content := contentBlock.(type) {
		case mcp.TextContent:
			result.WriteString(content.Text)
		case mcp.ImageContent:
			result.WriteString(fmt.Sprintf("[Image Response: %s, MIME: %s]\n", content.Data, content.MIMEType))
		case mcp.AudioContent:
			result.WriteString(fmt.Sprintf("[Audio Response: %s, MIME: %s]\n", content.Data, content.MIMEType))
		case mcp.EmbeddedResource:
			result.WriteString(fmt.Sprintf("[Embedded Resource Response: %s]\n", content.Type))
		default:
			// Fallback: try to extract from map structure
			if jsonBytes, err := json.Marshal(contentBlock); err == nil {
				var contentMap map[string]interface{}
				if json.Unmarshal(jsonBytes, &contentMap) == nil {
					if text, ok := contentMap["text"].(string); ok {
						result.WriteString(fmt.Sprintf("[Text Response: %s]\n", text))
						continue
					}
				}
				// Final fallback: serialize as JSON
				result.WriteString(string(jsonBytes))
			}
		}
	}

	if result.Len() > 0 {
		return strings.TrimSpace(result.String())
	}
	return fmt.Sprintf("MCP tool '%s' executed successfully", toolName)
}

// createToolResponseMessage creates a tool response message with the execution result.
func createToolResponseMessage(toolCall schemas.ChatAssistantMessageToolCall, responseText string) *schemas.ChatMessage {
	return &schemas.ChatMessage{
		Role: schemas.ChatMessageRoleTool,
		Content: &schemas.ChatMessageContent{
			ContentStr: &responseText,
		},
		ChatToolMessage: &schemas.ChatToolMessage{
			ToolCallID: toolCall.ID,
		},
	}
}

// validateMCPClientConfig validates an MCP client configuration.
func validateMCPClientConfig(config *schemas.MCPClientConfig) error {
	if strings.TrimSpace(config.ID) == "" {
		return fmt.Errorf("id is required for MCP client config")
	}
	if err := validateMCPClientName(config.Name); err != nil {
		return fmt.Errorf("invalid name for MCP client: %w", err)
	}
	if config.ConnectionType == "" {
		return fmt.Errorf("connection type is required for MCP client config")
	}
	switch config.ConnectionType {
	case schemas.MCPConnectionTypeHTTP:
		if config.ConnectionString == nil {
			return fmt.Errorf("ConnectionString is required for HTTP connection type in client '%s'", config.Name)
		}
	case schemas.MCPConnectionTypeSSE:
		if config.ConnectionString == nil {
			return fmt.Errorf("ConnectionString is required for SSE connection type in client '%s'", config.Name)
		}
	case schemas.MCPConnectionTypeSTDIO:
		if config.StdioConfig == nil {
			return fmt.Errorf("StdioConfig is required for STDIO connection type in client '%s'", config.Name)
		}
	case schemas.MCPConnectionTypeInProcess:
		// InProcess can be provided programmatically or created automatically.
	default:
		return fmt.Errorf("unknown connection type '%s' in client '%s'", config.ConnectionType, config.Name)
	}
	return nil
}

func validateMCPClientName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("name is required for MCP client")
	}
	for _, r := range name {
		if r > 127 { // non-ASCII
			return fmt.Errorf("name must contain only ASCII characters")
		}
	}
	if strings.Contains(name, "-") {
		return fmt.Errorf("name cannot contain hyphens")
	}
	if strings.Contains(name, " ") {
		return fmt.Errorf("name cannot contain spaces")
	}
	if len(name) > 0 && name[0] >= '0' && name[0] <= '9' {
		return fmt.Errorf("name cannot start with a number")
	}
	return nil
}

// parseToolName parses the tool name to be JavaScript-compatible.
// It converts spaces and hyphens to underscores, removes invalid characters, and ensures
// the name starts with a valid JavaScript identifier character.
func parseToolName(toolName string) string {
	if toolName == "" {
		return ""
	}

	var result strings.Builder
	runes := []rune(toolName)

	// Process first character - must be letter, underscore, or dollar sign
	if len(runes) > 0 {
		first := runes[0]
		if unicode.IsLetter(first) || first == '_' || first == '$' {
			result.WriteRune(unicode.ToLower(first))
		} else {
			// If first char is invalid, prefix with underscore
			result.WriteRune('_')
			if unicode.IsDigit(first) {
				result.WriteRune(first)
			}
		}
	}

	// Process remaining characters
	for i := 1; i < len(runes); i++ {
		r := runes[i]
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '$' {
			result.WriteRune(unicode.ToLower(r))
		} else if unicode.IsSpace(r) || r == '-' {
			// Replace spaces and hyphens with single underscore
			// Avoid consecutive underscores
			if result.Len() > 0 && result.String()[result.Len()-1] != '_' {
				result.WriteRune('_')
			}
		}
		// Skip other invalid characters
	}

	parsed := result.String()

	// Remove trailing underscores
	parsed = strings.TrimRight(parsed, "_")

	// Ensure we have at least one character
	// Should never happen, but just in case
	if parsed == "" {
		return "tool"
	}

	return parsed
}

// validateNormalizedToolName validates a normalized tool name to prevent path traversal.
// It rejects tool names that are empty, contain '/', or contain '..' after normalization.
// This prevents issues when tool names are used in VFS file paths.
//
// Parameters:
//   - normalizedName: The tool name after normalization (e.g., after replacing '-' with '_')
//
// Returns:
//   - error: An error if the tool name is invalid, nil otherwise
func validateNormalizedToolName(normalizedName string) error {
	if normalizedName == "" {
		return fmt.Errorf("tool name cannot be empty after normalization")
	}
	if strings.Contains(normalizedName, "/") {
		return fmt.Errorf("tool name cannot contain '/' (path separator) after normalization: %s", normalizedName)
	}
	if strings.Contains(normalizedName, "..") {
		return fmt.Errorf("tool name cannot contain '..' (path traversal) after normalization: %s", normalizedName)
	}
	return nil
}

// extractToolCallsFromCode extracts tool calls from Python/Starlark code
// Tool calls are in the format: server_name.tool_name(...)
func extractToolCallsFromCode(code string) ([]toolCallInfo, error) {
	toolCalls := []toolCallInfo{}

	// Regex pattern to match tool calls:
	// - Optional "await" keyword
	// - Server name (identifier)
	// - Dot
	// - Tool name (identifier)
	// - Opening parenthesis
	// This pattern matches: await serverName.toolName( or serverName.toolName(
	toolCallPattern := regexp.MustCompile(`(?:await\s+)?([a-zA-Z_$][a-zA-Z0-9_$]*)\s*\.\s*([a-zA-Z_$][a-zA-Z0-9_$]*)\s*\(`)

	// Find all matches
	matches := toolCallPattern.FindAllStringSubmatch(code, -1)
	for _, match := range matches {
		if len(match) >= 3 {
			serverName := match[1]
			toolName := match[2]
			toolCalls = append(toolCalls, toolCallInfo{
				serverName: serverName,
				toolName:   toolName,
			})
		}
	}

	return toolCalls, nil
}

// isToolCallAllowedForCodeMode checks if a tool call is allowed based on allowedAutoExecutionTools map
func isToolCallAllowedForCodeMode(serverName, toolName string, allClientNames []string, allowedAutoExecutionTools map[string][]string) bool {
	// Check if the server name is in the list of all client names
	if !slices.Contains(allClientNames, serverName) {
		// It can be a built-in Python/Starlark object, if not then downstream execution will fail with a runtime error.
		return true
	}

	// Get allowed tools for this server
	allowedTools, exists := allowedAutoExecutionTools[serverName]
	if !exists {
		// Server not in allowed list, return false to prevent downstream execution.
		return false
	}

	// Check if wildcard "*" is present (all tools allowed)
	if slices.Contains(allowedTools, "*") {
		return true
	}

	// Check if specific tool is in the allowed list
	if slices.Contains(allowedTools, toolName) {
		return true
	}

	return false // Tool not in allowed list
}

// hasToolCalls checks if a chat response contains tool calls that need to be executed
func hasToolCallsForChatResponse(response *schemas.BifrostChatResponse) bool {
	if response == nil || len(response.Choices) == 0 {
		return false
	}

	choice := response.Choices[0]

	// If finish_reason is "stop", this indicates non-auto-executable tools that require user approval.
	// Don't return true even if tool calls are present, as the agent loop should not process them.
	if choice.FinishReason != nil && *choice.FinishReason == "stop" {
		return false
	}

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

func hasToolCallsForResponsesResponse(response *schemas.BifrostResponsesResponse) bool {
	if response == nil || len(response.Output) == 0 {
		return false
	}

	// Check if any output message is a tool call
	for _, output := range response.Output {
		if output.Type == nil {
			continue
		}

		// Check for tool call types
		switch *output.Type {
		case schemas.ResponsesMessageTypeFunctionCall, schemas.ResponsesMessageTypeCustomToolCall:
			// Verify that ResponsesToolMessage is actually set
			if output.ResponsesToolMessage != nil {
				return true
			}
		}
	}

	return false
}

// stripClientPrefix removes the client name prefix from a tool name.
// Tool names are stored with format "{clientName}-{toolName}", but when calling
// the MCP server, we need the original tool name without the prefix.
//
// Parameters:
//   - prefixedToolName: Tool name with client prefix (e.g., "calculator-add")
//   - clientName: Client name to strip (e.g., "calculator")
//
// Returns:
//   - string: Sanitized tool name without prefix (e.g., "add")
func stripClientPrefix(prefixedToolName, clientName string) string {
	prefix := clientName + "-"
	if strings.HasPrefix(prefixedToolName, prefix) {
		return strings.TrimPrefix(prefixedToolName, prefix)
	}
	// If prefix doesn't match, return as-is (shouldn't happen, but be safe)
	return prefixedToolName
}

// getOriginalToolName retrieves the original MCP tool name from the sanitized name using the mapping.
// This function is used to restore the original tool name (with hyphens) that the MCP server expects.
//
// Parameters:
//   - sanitizedToolName: Sanitized tool name (e.g., "notion_search")
//   - client: The MCP client state containing the name mapping
//
// Returns:
//   - string: Original MCP tool name (e.g., "notion-search"), or sanitizedToolName if not found in mapping
func getOriginalToolName(sanitizedToolName string, client *schemas.MCPClientState) string {
	if client == nil || client.ToolNameMapping == nil {
		return sanitizedToolName
	}

	// Look up the original MCP name in the mapping
	if originalName, exists := client.ToolNameMapping[sanitizedToolName]; exists {
		return originalName
	}

	// If not in mapping, return as-is (might not need mapping if names are the same)
	return sanitizedToolName
}

// FixArraySchemas recursively fixes array schemas by ensuring they have an 'items' field.
// This prevents validation errors like "array schema missing items" when tools are registered.
// It handles nested arrays (array-of-array) and recurses into items regardless of type.
//
// Parameters:
//   - properties: The properties map to fix
func FixArraySchemas(properties map[string]interface{}, logger schemas.Logger) {
	for key, value := range properties {
		// Check if the value is a map (representing a schema object)
		if schemaMap, ok := value.(map[string]interface{}); ok {
			// Check if this is an array type
			if schemaType, ok := schemaMap["type"].(string); ok && schemaType == "array" {
				// Check if 'items' is missing
				if _, hasItems := schemaMap["items"]; !hasItems {
					// Add a default 'items' schema (unconstrained)
					schemaMap["items"] = map[string]interface{}{}
					logger.Debug("%s Fixed array schema for property '%s': added missing 'items' field", MCPLogPrefix, key)
				}
				// Recurse into items regardless of type (object or array)
				if itemsMap, ok := schemaMap["items"].(map[string]interface{}); ok {
					itemsType, _ := itemsMap["type"].(string)
					switch itemsType {
					case "array":
						// Handle nested arrays (array-of-array)
						FixArraySchemas(map[string]interface{}{"": itemsMap}, logger)
					case "object":
						// Recurse into object properties
						if itemsProps, ok := itemsMap["properties"].(map[string]interface{}); ok {
							FixArraySchemas(itemsProps, logger)
						}
					}
				}
			}

			// Recursively fix nested object properties
			if schemaType, ok := schemaMap["type"].(string); ok && schemaType == "object" {
				if nestedProps, ok := schemaMap["properties"].(map[string]interface{}); ok {
					FixArraySchemas(nestedProps, logger)
				}
			}

			// Handle anyOf, oneOf, allOf
			for _, unionKey := range []string{"anyOf", "oneOf", "allOf"} {
				if unionArray, ok := schemaMap[unionKey].([]interface{}); ok {
					for _, unionItem := range unionArray {
						if unionMap, ok := unionItem.(map[string]interface{}); ok {
							if unionType, ok := unionMap["type"].(string); ok && unionType == "array" {
								if _, hasItems := unionMap["items"]; !hasItems {
									unionMap["items"] = map[string]interface{}{}
									logger.Debug("%s Fixed array schema in %s for property '%s': added missing 'items' field", MCPLogPrefix, unionKey, key)
								}
								// Recurse into items regardless of type
								if itemsMap, ok := unionMap["items"].(map[string]interface{}); ok {
									itemsType, _ := itemsMap["type"].(string)
									switch itemsType {
									case "array":
										// Handle nested arrays
										FixArraySchemas(map[string]interface{}{"": itemsMap}, logger)
									case "object":
										if itemsProps, ok := itemsMap["properties"].(map[string]interface{}); ok {
											FixArraySchemas(itemsProps, logger)
										}
									}
								}
							}
							if nestedProps, ok := unionMap["properties"].(map[string]interface{}); ok {
								FixArraySchemas(nestedProps, logger)
							}
						}
					}
				}
			}
		}
	}
}

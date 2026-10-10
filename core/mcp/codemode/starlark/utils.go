//go:build !tinygo && !wasm

package starlark

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/bytedance/sonic"
	"github.com/canonical/starlark/syntax"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/schemas"
)

// extractResultFromChatMessage extracts the result from a chat message and parses it as JSON if possible.
func extractResultFromChatMessage(msg *schemas.ChatMessage) interface{} {
	if msg == nil || msg.Content == nil || msg.Content.ContentStr == nil {
		return nil
	}

	rawResult := *msg.Content.ContentStr

	var finalResult interface{}
	if err := sonic.Unmarshal([]byte(rawResult), &finalResult); err != nil {
		return rawResult
	}

	return finalResult
}

// extractResultFromResponsesMessage extracts the result or error from a ResponsesMessage.
func extractResultFromResponsesMessage(msg *schemas.ResponsesMessage) (interface{}, error) {
	if msg == nil {
		return nil, nil
	}

	if msg.ResponsesToolMessage != nil {
		if toolError := msg.ResponsesToolMessage.Error; toolError.IsError() {
			errText := toolError.Text()
			if errText == "" {
				errText = "tool call returned an error"
			}
			return nil, fmt.Errorf("%s", errText)
		}

		if msg.ResponsesToolMessage.Output != nil {
			if msg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr != nil {
				rawResult := *msg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr

				var finalResult interface{}
				if err := sonic.Unmarshal([]byte(rawResult), &finalResult); err != nil {
					return rawResult, nil
				}
				return finalResult, nil
			}

			if len(msg.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks) > 0 {
				var textParts []string
				for _, block := range msg.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks {
					if block.Text != nil {
						textParts = append(textParts, *block.Text)
					}
				}
				if len(textParts) > 0 {
					result := strings.Join(textParts, "\n")
					var finalResult interface{}
					if err := sonic.Unmarshal([]byte(result), &finalResult); err != nil {
						return result, nil
					}
					return finalResult, nil
				}
			}
		}
	}

	return nil, nil
}

// formatResultForLog formats a result value for logging purposes.
func formatResultForLog(result interface{}) string {
	// Tool responses can be large even when code returns a small projection.
	// Bound automatic diagnostic previews before JSON escaping/copying them.
	if text, ok := result.(string); ok && len(text) > 2048 {
		result = text[:2048] + "... (truncated)"
	}
	var resultStr string
	if result == nil {
		resultStr = "null"
	} else if resultBytes, err := schemas.MarshalSorted(result); err == nil {
		resultStr = string(resultBytes)
	} else {
		resultStr = fmt.Sprintf("%v", result)
	}
	return resultStr
}

// exceptionHandlingHints explains that Starlark has no exception handling.
func exceptionHandlingHints() []string {
	return []string{
		"Starlark does NOT support try/except/finally/raise — there is no exception handling.",
		"Instead, check return values for errors:",
		"  result = server.tool(param=\"value\")",
		"  if result == None or (type(result) == \"dict\" and \"error\" in result):",
		"    print(\"Error:\", result)",
	}
}

// unsupportedStatement returns the try/except/finally/raise keyword a parse error
// stopped at. Canonical's scanner reports these keywords as "illegal token", so
// the error text alone does not name them.
func unsupportedStatement(code string, err error) string {
	var syntaxErr syntax.Error
	if !errors.As(err, &syntaxErr) || syntaxErr.Pos.Line < 1 || syntaxErr.Pos.Col < 1 {
		return ""
	}
	lines := strings.Split(code, "\n")
	if int(syntaxErr.Pos.Line) > len(lines) {
		return ""
	}
	line := []rune(lines[syntaxErr.Pos.Line-1])
	if int(syntaxErr.Pos.Col) > len(line) {
		return ""
	}
	word := string(line[syntaxErr.Pos.Col-1:])
	if end := strings.IndexFunc(word, func(r rune) bool { return !unicode.IsLetter(r) }); end >= 0 {
		word = word[:end]
	}
	switch word {
	case "try", "except", "finally", "raise":
		return word
	}
	return ""
}

// generatePythonErrorHints generates helpful hints for Python/Starlark errors.
func generatePythonErrorHints(errorMessage string, serverKeys []string) []string {
	hints := []string{}

	if strings.Contains(errorMessage, "got try") || strings.Contains(errorMessage, "got except") ||
		strings.Contains(errorMessage, "got finally") || strings.Contains(errorMessage, "got raise") {
		hints = append(hints, exceptionHandlingHints()...)
	} else if strings.Contains(errorMessage, "undefined") || strings.Contains(errorMessage, "not defined") {
		var undefinedVar string
		if match := regexp.MustCompile(`name ['"]([^'"]+)['"] is not defined`).FindStringSubmatch(errorMessage); len(match) > 1 {
			undefinedVar = match[1]
		} else if match := regexp.MustCompile(`undefined:\s*([A-Za-z_][A-Za-z0-9_]*)`).FindStringSubmatch(errorMessage); len(match) > 1 {
			undefinedVar = match[1]
		} else if match := regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)[^A-Za-z0-9_]+(?:undefined|not defined)`).FindStringSubmatch(errorMessage); len(match) > 1 {
			undefinedVar = match[1]
		}
		if undefinedVar != "" {
			hints = append(hints, fmt.Sprintf("Variable '%s' is not defined.", undefinedVar))
			hints = append(hints, "Note: Each executeToolCode call runs in a fresh scope — no variables persist between calls.")
			if len(serverKeys) > 0 {
				hints = append(hints, fmt.Sprintf("Available server keys: %s", strings.Join(serverKeys, ", ")))
				hints = append(hints, "Access tools using: server_name.tool_name(param=\"value\")")
			}
		}
	} else if strings.Contains(errorMessage, "not within a function") {
		hints = append(hints, "Starlark requires for/if/while statements to be inside functions at the top level.")
		hints = append(hints, "Wrap your code in a function, then call it:")
		hints = append(hints, "  def fetch_all():")
		hints = append(hints, "    results = []")
		hints = append(hints, "    for id in ids:")
		hints = append(hints, "      results.append(server.get(id=id))")
		hints = append(hints, "    return results")
		hints = append(hints, "  result = fetch_all()")
	} else if strings.Contains(errorMessage, "syntax error") {
		hints = append(hints, "Python syntax error detected.")
		hints = append(hints, "Check for proper indentation (use spaces, not tabs).")
		hints = append(hints, "Ensure colons after if/for/def statements.")
		hints = append(hints, "Check for matching parentheses and brackets.")
	} else if strings.Contains(errorMessage, "has no") && strings.Contains(errorMessage, "attribute") {
		hints = append(hints, "You're trying to access an attribute that doesn't exist.")
		hints = append(hints, "Use dict access syntax: result[\"key\"] instead of result.key")
		hints = append(hints, "Use print(result) to see the actual structure.")
		if len(serverKeys) > 0 {
			hints = append(hints, fmt.Sprintf("Available server keys: %s", strings.Join(serverKeys, ", ")))
		}
	} else if strings.Contains(errorMessage, "not callable") {
		hints = append(hints, "You're trying to call something that is not a function.")
		hints = append(hints, "Ensure you're using the correct tool name.")
		if len(serverKeys) > 0 {
			hints = append(hints, fmt.Sprintf("Available server keys: %s", strings.Join(serverKeys, ", ")))
		}
		hints = append(hints, "Use readToolFile to see available tools for a server.")
	} else if strings.Contains(errorMessage, "key") && strings.Contains(errorMessage, "not found") {
		hints = append(hints, "Dictionary key not found.")
		hints = append(hints, "Use print() to inspect the dict structure before accessing keys.")
		hints = append(hints, "Use .get(\"key\", default) for safe access.")
	} else {
		hints = append(hints, "Check the error message above for details.")
		if len(serverKeys) > 0 {
			hints = append(hints, fmt.Sprintf("Available server keys: %s", strings.Join(serverKeys, ", ")))
		}
		hints = append(hints, "Use: result = server_name.tool_name(param=\"value\")")
		hints = append(hints, "Access dict values with brackets: result[\"key\"]")
	}

	return hints
}

// extractTextFromMCPResponse extracts text content from an MCP tool response,
// bounded by valueLimit bytes.
func extractTextFromMCPResponse(toolResponse *mcp.CallToolResult, toolName string, valueLimit int) (string, error) {
	if toolResponse == nil {
		return fmt.Sprintf("MCP tool '%s' executed successfully", toolName), nil
	}
	if len(toolResponse.Content) > valueLimit/128 {
		return "", fmt.Errorf("code mode tool result exceeds size limit")
	}
	var result strings.Builder
	for _, contentBlock := range toolResponse.Content {
		// Both values and pointers implement MCP's sealed Content interface.
		switch content := contentBlock.(type) {
		case *mcp.TextContent:
			if content == nil {
				continue
			}
			contentBlock = *content
		case *mcp.ImageContent:
			if content == nil {
				continue
			}
			contentBlock = *content
		case *mcp.AudioContent:
			if content == nil {
				continue
			}
			contentBlock = *content
		case *mcp.EmbeddedResource:
			if content == nil {
				continue
			}
			contentBlock = *content
		case *mcp.ResourceLink:
			if content == nil {
				continue
			}
			contentBlock = *content
		}
		var parts []string
		switch content := contentBlock.(type) {
		case mcp.TextContent:
			parts = []string{content.Text}
		case mcp.ImageContent:
			parts = []string{"[Image Response: ", content.Data, ", MIME: ", content.MIMEType, "]\n"}
		case mcp.AudioContent:
			parts = []string{"[Audio Response: ", content.Data, ", MIME: ", content.MIMEType, "]\n"}
		case mcp.EmbeddedResource:
			parts = []string{"[Embedded Resource Response: ", content.Type, "]\n"}
		case mcp.ResourceLink:
			remaining := valueLimit - 256
			check := func(value string) bool {
				if len(value) > remaining/6 {
					return false
				}
				remaining -= 6 * len(value)
				return true
			}
			for _, value := range []string{content.Type, content.URI, content.Name, content.Description, content.MIMEType} {
				if !check(value) {
					return "", fmt.Errorf("code mode tool result exceeds size limit")
				}
			}
			if content.Annotations != nil {
				if len(content.Annotations.Audience) > remaining/32 {
					return "", fmt.Errorf("code mode tool result exceeds size limit")
				}
				remaining -= len(content.Annotations.Audience) * 32
				for _, role := range content.Annotations.Audience {
					if !check(string(role)) {
						return "", fmt.Errorf("code mode tool result exceeds size limit")
					}
				}
			}
			encoded, err := schemas.MarshalSorted(content)
			if err != nil {
				return "", err
			}
			parts = []string{string(encoded)}
		default:
			return "", fmt.Errorf("unsupported code mode tool content type %T", contentBlock)
		}
		for _, part := range parts {
			if len(part) > valueLimit-result.Len() {
				return "", fmt.Errorf("code mode tool result exceeds size limit")
			}
			result.WriteString(part)
		}
	}
	if result.Len() > 0 {
		return strings.TrimSpace(result.String()), nil
	}
	return fmt.Sprintf("MCP tool '%s' executed successfully", toolName), nil
}

// createToolResponseMessage creates a tool response message with the execution result.
// isError marks the result failed so a CodeMode lookup or sandbox failure is not
// replayed to the model as a successful answer.
func createToolResponseMessage(toolCall schemas.ChatAssistantMessageToolCall, responseText string, isError bool) *schemas.ChatMessage {
	toolMsg := &schemas.ChatToolMessage{
		ToolCallID: toolCall.ID,
	}
	if isError {
		toolMsg.IsError = schemas.Ptr(true)
	}

	return &schemas.ChatMessage{
		Role: schemas.ChatMessageRoleTool,
		Content: &schemas.ChatMessageContent{
			ContentStr: &responseText,
		},
		ChatToolMessage: toolMsg,
	}
}

// parseToolName normalizes a raw tool name into a Starlark-compatible identifier.
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
	if parsed == "" {
		return "tool"
	}

	return parsed
}

// getCanonicalToolName returns the exact callable tool identifier exposed in Starlark.
func getCanonicalToolName(clientName, originalToolName string) string {
	return parseToolName(stripClientPrefix(originalToolName, clientName))
}

// getCompatibilityToolAlias returns the case-preserving alias derived from the raw tool name.
// This is used as a compatibility alias when the raw name is still a valid Starlark identifier.
func getCompatibilityToolAlias(clientName, originalToolName string) string {
	return strings.ReplaceAll(stripClientPrefix(originalToolName, clientName), "-", "_")
}

// matchesToolReference reports whether the requested tool name matches any supported identifier form.
// We accept the canonical callable name plus legacy display forms for backward compatibility.
func matchesToolReference(requestedToolName, clientName, originalToolName string) bool {
	requested := strings.ToLower(requestedToolName)
	if requested == "" {
		return false
	}

	candidates := []string{
		getCanonicalToolName(clientName, originalToolName),
		getCompatibilityToolAlias(clientName, originalToolName),
		stripClientPrefix(originalToolName, clientName),
	}

	for _, candidate := range candidates {
		if candidate != "" && requested == strings.ToLower(candidate) {
			return true
		}
	}

	return false
}

// isValidStarlarkIdentifier reports whether name can be used directly in Starlark code.
func isValidStarlarkIdentifier(name string) bool {
	if name == "" {
		return false
	}

	runes := []rune(name)
	first := runes[0]
	if !unicode.IsLetter(first) && first != '_' && first != '$' {
		return false
	}

	for _, r := range runes[1:] {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '$' {
			return false
		}
	}

	return true
}

// validateNormalizedToolName validates a normalized tool name to prevent path traversal.
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

// stripClientPrefix removes the client name prefix from a tool name.
func stripClientPrefix(prefixedToolName, clientName string) string {
	prefix := clientName + "-"
	if strings.HasPrefix(prefixedToolName, prefix) {
		return strings.TrimPrefix(prefixedToolName, prefix)
	}
	// If prefix doesn't match, return as-is (shouldn't happen, but be safe)
	return prefixedToolName
}

//go:build !tinygo && !wasm

package starlark

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/bytedance/sonic"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
)

// starlarkToGo converts a Starlark value to a Go value
func starlarkToGo(v starlark.Value) interface{} {
	switch val := v.(type) {
	case starlark.NoneType:
		return nil
	case starlark.Bool:
		return bool(val)
	case starlark.Int:
		if i, ok := val.Int64(); ok {
			return i
		}
		if i, ok := val.Uint64(); ok {
			return i
		}
		return val.String()
	case starlark.Float:
		return float64(val)
	case starlark.String:
		return string(val)
	case *starlark.List:
		result := make([]interface{}, val.Len())
		for i := 0; i < val.Len(); i++ {
			result[i] = starlarkToGo(val.Index(i))
		}
		return result
	case starlark.Tuple:
		result := make([]interface{}, len(val))
		for i, item := range val {
			result[i] = starlarkToGo(item)
		}
		return result
	case *starlark.Dict:
		result := make(map[string]interface{})
		for _, item := range val.Items() {
			if keyStr, ok := item[0].(starlark.String); ok {
				result[string(keyStr)] = starlarkToGo(item[1])
			} else {
				// Use string representation for non-string keys
				result[item[0].String()] = starlarkToGo(item[1])
			}
		}
		return result
	case *starlarkstruct.Struct:
		result := make(map[string]interface{})
		for _, name := range val.AttrNames() {
			if attrVal, err := val.Attr(name); err == nil {
				result[name] = starlarkToGo(attrVal)
			}
		}
		return result
	default:
		return val.String()
	}
}

// goToStarlark converts a Go value to a Starlark value
func goToStarlark(v interface{}) starlark.Value {
	if v == nil {
		return starlark.None
	}

	switch val := v.(type) {
	case bool:
		return starlark.Bool(val)
	case int:
		return starlark.MakeInt(val)
	case int64:
		return starlark.MakeInt64(val)
	case uint64:
		return starlark.MakeUint64(val)
	case float64:
		return starlark.Float(val)
	case string:
		return starlark.String(val)
	case []interface{}:
		items := make([]starlark.Value, len(val))
		for i, item := range val {
			items[i] = goToStarlark(item)
		}
		return starlark.NewList(items)
	case map[string]interface{}:
		dict := starlark.NewDict(len(val))
		for k, v := range val {
			dict.SetKey(starlark.String(k), goToStarlark(v))
		}
		return dict
	default:
		// Try to marshal to JSON and parse as a generic structure
		if jsonBytes, err := sonic.Marshal(val); err == nil {
			var generic interface{}
			if sonic.Unmarshal(jsonBytes, &generic) == nil {
				return goToStarlark(generic)
			}
		}
		return starlark.String(fmt.Sprintf("%v", val))
	}
}

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
		if msg.ResponsesToolMessage.Error != nil && *msg.ResponsesToolMessage.Error != "" {
			return nil, fmt.Errorf("%s", *msg.ResponsesToolMessage.Error)
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

// generatePythonErrorHints generates helpful hints for Python/Starlark errors.
func generatePythonErrorHints(errorMessage string, serverKeys []string) []string {
	hints := []string{}

	if strings.Contains(errorMessage, "undefined") || strings.Contains(errorMessage, "not defined") {
		re := regexp.MustCompile(`(\w+).*(?:undefined|not defined)`)
		if match := re.FindStringSubmatch(errorMessage); len(match) > 1 {
			undefinedVar := match[1]
			hints = append(hints, fmt.Sprintf("Variable '%s' is not defined.", undefinedVar))
			if len(serverKeys) > 0 {
				hints = append(hints, fmt.Sprintf("Available server keys: %s", strings.Join(serverKeys, ", ")))
				hints = append(hints, "Access tools using: server_name.tool_name(param=\"value\")")
			}
		}
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

// parseToolName parses the tool name to be JavaScript-compatible.
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

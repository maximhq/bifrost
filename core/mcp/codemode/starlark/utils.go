//go:build !tinygo && !wasm

package starlark

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bytedance/sonic"
	"github.com/canonical/starlark/starlarkstruct"
	"github.com/canonical/starlark/syntax"
	"github.com/mark3labs/mcp-go/mcp"
	codemcp "github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

// serverInstructionsComment renders the client's upstream instructions, or "" if it advertises
// none. Shared by readToolFile and getToolDocs: every stub documenting a server carries them.
func (s *StarlarkCodeMode) serverInstructionsComment(clientName string) string {
	client := s.clientManager.GetClientByName(clientName)
	if client == nil || client.ExecutionConfig == nil {
		return ""
	}
	// Client override first, then the configured global. The aggregate total stays out of it:
	// a stub carries one server, so being clamped by the whole-set budget would cut it early.
	limit := client.ExecutionConfig.MaxInstructionsLength
	if limit <= 0 {
		limit = s.clientManager.InstructionCaps().PerClient
	}
	return codemcp.InstructionsCommentBlock(client.ServerInstructions, limit)
}

// jsonModuleShadowed reports whether a code mode server is bound as "json", which keeps the
// sandbox's json module out of a script's reach (see runSandbox).
func (s *StarlarkCodeMode) jsonModuleShadowed(toolsPerClient map[string][]schemas.ChatTool) bool {
	if len(toolsPerClient["json"]) == 0 {
		return false
	}
	client := s.clientManager.GetClientByName("json")
	return client != nil && client.ExecutionConfig != nil && client.ExecutionConfig.IsCodeModeClient
}

// extractResultFromChatMessage extracts the result from a chat message and parses it as JSON if possible.
func extractResultFromChatMessage(msg *schemas.ChatMessage) interface{} {
	if msg == nil || msg.Content == nil || msg.Content.ContentStr == nil {
		return nil
	}
	return decodeToolResult(*msg.Content.ContentStr)
}

// decodeToolResult turns a tool's text result into the value a script receives: the decoded
// JSON when the text is JSON, the text itself otherwise. Some servers wrap their JSON payload
// in a JSON string, so a decode that yields a string holding an object or array is decoded
// once more. A quoted scalar such as "123" stays the string the server sent.
func decodeToolResult(raw string) interface{} {
	var value interface{}
	if err := sonic.Unmarshal([]byte(raw), &value); err != nil {
		return raw
	}
	if wrapped, ok := value.(string); ok {
		trimmed := strings.TrimSpace(wrapped)
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			var inner interface{}
			if err := sonic.Unmarshal([]byte(trimmed), &inner); err == nil {
				return inner
			}
		}
	}
	return value
}

// joinTextBlocks joins a result's text blocks. Several blocks that are each valid JSON become
// one JSON array, so a server that sends one result per block still hands the script a list;
// anything else is joined with newlines so adjacent blocks never run together.
func joinTextBlocks(texts []string) string {
	if len(texts) > 1 {
		allJSON := true
		for _, text := range texts {
			if !gjson.Valid(text) {
				allJSON = false
				break
			}
		}
		if allJSON {
			return "[" + strings.Join(texts, ",") + "]"
		}
	}
	return strings.Join(texts, "\n")
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
				return decodeToolResult(*msg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr), nil
			}

			if len(msg.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks) > 0 {
				var textParts []string
				for _, block := range msg.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks {
					if block.Text != nil {
						textParts = append(textParts, *block.Text)
					}
				}
				if len(textParts) > 0 {
					return decodeToolResult(joinTextBlocks(textParts)), nil
				}
			}
		}
	}

	return nil, nil
}

// codeModeDiagnosticBytes bounds any tool text surfaced to the caller.
const codeModeDiagnosticBytes = 2048

// truncateDiagnostic bounds caller-visible text without JSON-encoding it. Cuts on
// a rune boundary: it is raised as an error, not only escaped into a log line.
func truncateDiagnostic(text string) string {
	if len(text) <= codeModeDiagnosticBytes {
		return text
	}
	// The marker counts against the cap, so reserve it before picking the prefix.
	const marker = "... (truncated)"
	cut := text[:codeModeDiagnosticBytes-len(marker)]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + marker
}

// exceptionHandlingHints explains that Starlark has no exception handling.
func exceptionHandlingHints() []string {
	return []string{
		"Starlark does NOT support try/except/finally/raise — there is no exception handling.",
		"A failed tool call returns an error value instead of raising. Check it:",
		"  r = server.tool(param=\"value\")",
		"  if hasattr(r, \"error\"):",
		"    fail(r.error)  # or handle it and carry on",
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

// The patterns below capture at most 80 characters of a name or key, which the hints repeat. A
// script can take a name from tool data (getattr(d, name)), and the error line already carries it
// once, so a longer one is not matched and its error gets the generic hints instead.
var (
	// serverMemberPattern matches a missing tool on a server object: besides a failed call's
	// tool_error, a server is the only struct a script can hold.
	serverMemberPattern = regexp.MustCompile(`"([^"]{1,80})" struct has no \.([A-Za-z_][A-Za-z0-9_]{0,79}) attribute`)
	// missingMemberPattern matches Starlark's message for a method or field a built-in type lacks.
	missingMemberPattern = regexp.MustCompile(`\b(string|dict|module) has no \.([A-Za-z_][A-Za-z0-9_]{0,79}) field or method`)
	// missingKeyPattern matches Starlark's message for a dict lookup that misses.
	missingKeyPattern = regexp.MustCompile(`key (.{1,80}) not in dict`)
)

// dictMethods are the methods a script calls on a value it took for a dict.
var dictMethods = []string{"get", "items", "keys", "values"}

// moduleMemberHints names what each sandbox module offers, read from the modules themselves so
// the hint cannot drift from them.
var moduleMemberHints = func() map[string]string {
	hints := map[string]string{}
	for name, value := range sandboxModules {
		if module, ok := value.(*starlarkstruct.Module); ok {
			hints[name] = fmt.Sprintf("The %s module has %s.", name, strings.Join(module.Members.Keys(), ", "))
		}
	}
	return hints
}()

// generatePythonErrorHints generates helpful hints for Python/Starlark errors.
func generatePythonErrorHints(errorMessage string, serverKeys []string) []string {
	hints := []string{}
	missingMember := missingMemberPattern.FindStringSubmatch(errorMessage)
	serverMember := serverMemberPattern.FindStringSubmatch(errorMessage)

	if strings.HasPrefix(errorMessage, "fail: ") {
		// The script chose to stop; generic advice about the code would only mislead.
		hints = append(hints, "The script stopped itself with fail(); the message above is the reason it gave.")
	} else if strings.Contains(errorMessage, "unhandled index operation struct[") || strings.Contains(errorMessage, `"tool_error" struct has no`) ||
		strings.Contains(errorMessage, "value of type struct has no len") || strings.Contains(errorMessage, "struct value is not iterable") {
		hints = append(hints, "If this value came from a tool call, the call failed: its result is an error value, not a dict or list.")
		hints = append(hints, "Check it first: if hasattr(r, \"error\"): ... (r.error holds the message)")
	} else if serverMember != nil && slices.Contains(serverKeys, serverMember[1]) {
		hints = append(hints, fmt.Sprintf("%s has no tool %s. readToolFile lists the exact tool names of %s.", serverMember[1], serverMember[2], serverMember[1]))
	} else if strings.Contains(errorMessage, " not in dict") {
		key := "key"
		if match := missingKeyPattern.FindStringSubmatch(errorMessage); match != nil {
			key = match[1]
			hints = append(hints, fmt.Sprintf("The dict has no key %s.", key))
		}
		hints = append(hints, fmt.Sprintf("List its keys with sorted(r.keys()), or read it with r.get(%s, default).", key))
	} else if strings.Contains(errorMessage, "string index") ||
		(missingMember != nil && missingMember[1] == "string" && slices.Contains(dictMethods, missingMember[2])) {
		hints = append(hints, "The value is a str, not a dict: the server's text was not JSON, or was JSON wrapped in a string.")
		if !slices.Contains(serverKeys, "json") {
			hints = append(hints, "If it holds JSON, parse it first: data = json.decode(result)")
		}
		hints = append(hints, "Check the shape with type(result) before indexing a tool result.")
	} else if missingMember != nil {
		switch name := missingMember[2]; missingMember[1] {
		case "string":
			hints = append(hints, fmt.Sprintf("Starlark's str has no .%s method; it lacks some Python string methods.", name))
		case "dict":
			hints = append(hints, fmt.Sprintf("A dict is read with brackets: r[%q] or r.get(%q, default), never r.%s.", name, name, name))
		case "module":
			// The message does not name the module, so list each one a server does not shadow.
			for _, module := range sandboxModules.Keys() {
				if !slices.Contains(serverKeys, module) {
					hints = append(hints, moduleMemberHints[module])
				}
			}
		}
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
	} else if strings.Contains(errorMessage, "syntax error") {
		hints = append(hints, "Python syntax error detected.")
		hints = append(hints, "Check for proper indentation (use spaces, not tabs).")
		hints = append(hints, "Ensure colons after if/for/def statements.")
		hints = append(hints, "Check for matching parentheses and brackets.")
	} else if strings.Contains(errorMessage, "not callable") {
		hints = append(hints, "You're trying to call something that is not a function.")
		hints = append(hints, "Ensure you're using the correct tool name.")
		if len(serverKeys) > 0 {
			hints = append(hints, fmt.Sprintf("Available server keys: %s", strings.Join(serverKeys, ", ")))
		}
		hints = append(hints, "Use readToolFile to see available tools for a server.")
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
	// Structured content is the machine-readable result the MCP spec defines; the text
	// blocks beside it are a copy kept for older clients. An error result keeps its text.
	if !toolResponse.IsError && toolResponse.StructuredContent != nil {
		encoded, err := schemas.MarshalSorted(toolResponse.StructuredContent)
		if err != nil {
			return "", err
		}
		if len(encoded) > valueLimit {
			return "", fmt.Errorf("code mode tool result exceeds size limit")
		}
		return string(encoded), nil
	}
	if len(toolResponse.Content) > valueLimit/128 {
		return "", fmt.Errorf("code mode tool result exceeds size limit")
	}
	blocks := make([]string, 0, len(toolResponse.Content))
	size := 0
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
			parts = []string{"[Image Response: ", content.Data, ", MIME: ", content.MIMEType, "]"}
		case mcp.AudioContent:
			parts = []string{"[Audio Response: ", content.Data, ", MIME: ", content.MIMEType, "]"}
		case mcp.EmbeddedResource:
			parts = []string{"[Embedded Resource Response: ", content.Type, "]"}
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
		if len(blocks) > 0 {
			size++ // the separator joinTextBlocks puts between blocks
		}
		for _, part := range parts {
			if len(part) > valueLimit-size {
				return "", fmt.Errorf("code mode tool result exceeds size limit")
			}
			size += len(part)
		}
		blocks = append(blocks, strings.Join(parts, ""))
	}
	if text := strings.TrimSpace(joinTextBlocks(blocks)); text != "" {
		return text, nil
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

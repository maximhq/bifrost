//go:build !tinygo && !wasm

package starlark

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	codemcp "github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/schemas"
)

// createReadToolFileTool creates the readToolFile tool definition for code mode.
// This tool allows reading virtual .pyi stub files for specific MCP servers/tools,
// generating Python type stubs from the server's tool schemas.
func (s *StarlarkCodeMode) createReadToolFileTool() schemas.ChatTool {
	bindingLevel := s.GetBindingLevel()

	var fileNameDescription, toolDescription string

	if bindingLevel == schemas.CodeModeBindingLevelServer {
		fileNameDescription = "The virtual filename from listToolFiles in format: servers/<serverName>.pyi (e.g., 'servers/calculator.pyi')"
		toolDescription = "Reads a virtual .pyi stub file for a specific MCP server, returning compact Python function signatures " +
			"for all tools available on that server. The fileName should be in format servers/<serverName>.pyi as listed by listToolFiles. " +
			"The function performs case-insensitive matching and removes the .pyi extension. " +
			"This is the authoritative source for the exact callable tool names and parameters to use in executeToolCode. " +
			"Each tool can be accessed in code via: serverName.tool_name(param=value). " +
			"If the compact signature is not enough to understand a tool, use getToolDocs for detailed documentation. " +
			"Workflow: listToolFiles -> readToolFile -> (optional) getToolDocs -> executeToolCode. " +
			"IMPORTANT: If the response header shows 'Total lines: X (this is the complete file)', " +
			"do NOT call this tool again with startLine/endLine - you already have the complete file."
	} else {
		fileNameDescription = "The virtual filename from listToolFiles in format: servers/<serverName>/<toolName>.pyi (e.g., 'servers/calculator/add.pyi')"
		toolDescription = "Reads a virtual .pyi stub file for a specific tool, returning its compact Python function signature. " +
			"The fileName should be in format servers/<serverName>/<toolName>.pyi as listed by listToolFiles. " +
			"The function performs case-insensitive matching and removes the .pyi extension. " +
			"This is the authoritative source for the exact callable tool name and arguments to use in executeToolCode. " +
			"The tool can be accessed in code via: serverName.tool_name(param=value) using the def name shown in the file. " +
			"If the compact signature is not enough to understand the tool, use getToolDocs for detailed documentation. " +
			"Workflow: listToolFiles -> readToolFile -> (optional) getToolDocs -> executeToolCode. " +
			"IMPORTANT: If the response header shows 'Total lines: X (this is the complete file)', " +
			"do NOT call this tool again with startLine/endLine - you already have the complete file."
	}

	readToolFileProps := schemas.NewOrderedMapFromPairs(
		schemas.KV("fileName", map[string]interface{}{
			"type":        "string",
			"description": fileNameDescription,
		}),
		schemas.KV("startLine", map[string]interface{}{
			"type":        "number",
			"description": "Optional 1-based starting line number for partial file read. Usually not needed - omit to read the entire file. Files are typically small (under 50 lines).",
		}),
		schemas.KV("endLine", map[string]interface{}{
			"type":        "number",
			"description": "Optional 1-based ending line number for partial file read. Usually not needed - omit to read the entire file. Will be clamped to actual file size if too large.",
		}),
	)
	return schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        codemcp.ToolTypeReadToolFile,
			Description: schemas.Ptr(toolDescription),
			Parameters: &schemas.ToolFunctionParameters{
				Type:       "object",
				Properties: readToolFileProps,
				Required:   []string{"fileName"},
			},
		},
	}
}

// handleReadToolFile handles the readToolFile tool call.
func (s *StarlarkCodeMode) handleReadToolFile(ctx context.Context, toolCall schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, error) {
	// Parse tool arguments
	var arguments map[string]interface{}
	if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &arguments); err != nil {
		return nil, fmt.Errorf("failed to parse tool arguments: %v", err)
	}

	fileName, ok := arguments["fileName"].(string)
	if !ok || fileName == "" {
		return nil, fmt.Errorf("fileName parameter is required and must be a string")
	}

	// Get available tools per client
	availableToolsPerClient := s.clientManager.GetToolPerClient(codemcp.WithModelToolsOnly(ctx))
	servers := s.codeModeServers(availableToolsPerClient)

	// Parse the file path to extract server name and optional tool name
	serverName, toolName, isToolLevel := parseVFSFilePath(fileName, servers)

	// Find matching client
	var matchedClientName string
	var matchedTools []schemas.ChatTool
	matchCount := 0

	for clientName, tools := range availableToolsPerClient {
		client := s.clientManager.GetClientByName(clientName)
		if client == nil {
			s.logger.Warn("%s Client %s not found, skipping", codemcp.CodeModeLogPrefix, clientName)
			continue
		}
		if !client.ExecutionConfig.IsCodeModeClient || len(tools) == 0 {
			continue
		}

		clientNameLower := strings.ToLower(clientName)
		serverNameLower := strings.ToLower(serverName)

		if clientNameLower == serverNameLower {
			matchCount++
			if matchCount > 1 {
				// Multiple matches found
				errorMsg := fmt.Sprintf("Multiple servers match filename '%s':\n", fileName)
				for name := range availableToolsPerClient {
					if strings.ToLower(name) == serverNameLower {
						errorMsg += fmt.Sprintf("  - %s\n", name)
					}
				}
				errorMsg += "\nPlease use a more specific filename. Use the exact display name from listToolFiles to avoid ambiguity."
				return createToolResponseMessage(toolCall, errorMsg, true), nil
			}

			matchedClientName = clientName

			if isToolLevel {
				// Tool-level: filter to specific tool
				var foundTool *schemas.ChatTool
				for i, tool := range tools {
					if tool.Function != nil {
						if matchesToolReference(toolName, clientName, tool.Function.Name) {
							foundTool = &tools[i]
							break
						}
					}
				}

				if foundTool == nil {
					availableTools := make([]string, 0)
					for _, tool := range tools {
						if tool.Function != nil {
							availableTools = append(availableTools, getCanonicalToolName(clientName, tool.Function.Name))
						}
					}
					errorMsg := fmt.Sprintf("Tool '%s' not found in server '%s'. Available tools in this server are:\n", toolName, clientName)
					for _, t := range availableTools {
						errorMsg += fmt.Sprintf("  - servers/%s/%s.pyi\n", clientName, t)
					}
					return createToolResponseMessage(toolCall, errorMsg, true), nil
				}

				matchedTools = []schemas.ChatTool{*foundTool}
			} else {
				// Server-level: use all tools
				matchedTools = tools
			}
		}
	}

	if matchedClientName == "" {
		// List server names only: listing every tool file of every server on a
		// mistyped path floods the caller's context.
		errorMsg := fmt.Sprintf("No server found matching '%s'. Available servers: %s\n", serverName, strings.Join(servers, ", "))
		if s.GetBindingLevel() == schemas.CodeModeBindingLevelServer {
			errorMsg += "Use servers/<server>.pyi as listed by listToolFiles.\n"
		} else {
			errorMsg += "Use servers/<server>/<tool>.pyi as listed by listToolFiles.\n"
		}
		return createToolResponseMessage(toolCall, errorMsg, true), nil
	}

	// Generate compact Python signatures
	fileContent := generateCompactSignatures(matchedClientName, matchedTools, isToolLevel, s.serverInstructionsComment(ctx, matchedClientName), !s.jsonModuleShadowed(availableToolsPerClient))
	lines := strings.Split(fileContent, "\n")
	totalLines := len(lines)

	// Prepend total lines info so LLM knows the file size upfront
	fileContent = fmt.Sprintf("# Total lines: %d (this is the complete file, no need to paginate)\n%s", totalLines+1, fileContent)
	// Recalculate lines after prepending
	lines = strings.Split(fileContent, "\n")
	totalLines = len(lines)

	// Handle line slicing if provided
	var startLine, endLine *int
	if sl, ok := arguments["startLine"].(float64); ok {
		slInt := int(sl)
		startLine = &slInt
	}
	if el, ok := arguments["endLine"].(float64); ok {
		elInt := int(el)
		endLine = &elInt
	}

	if startLine != nil || endLine != nil {
		start := 1
		if startLine != nil {
			start = *startLine
		}
		end := totalLines
		if endLine != nil {
			end = *endLine
		}

		// Clamp values to valid range instead of erroring
		// This handles cases where LLM requests more lines than exist
		if start < 1 {
			start = 1
		}
		if start > totalLines {
			start = totalLines
		}
		if end < 1 {
			end = 1
		}
		if end > totalLines {
			end = totalLines
		}
		if start > end {
			// If start > end after clamping, just return the start line
			end = start
		}

		// Slice lines (convert to 0-based indexing)
		selectedLines := lines[start-1 : end]
		fileContent = strings.Join(selectedLines, "\n")
	}

	return createToolResponseMessage(toolCall, fileContent, false), nil
}

// codeModeServers returns the sorted names of the connected code mode servers that expose tools,
// i.e. the names readToolFile and getToolDocs can resolve.
func (s *StarlarkCodeMode) codeModeServers(availableToolsPerClient map[string][]schemas.ChatTool) []string {
	servers := make([]string, 0, len(availableToolsPerClient))
	for name, tools := range availableToolsPerClient {
		client := s.clientManager.GetClientByName(name)
		if client == nil || client.ExecutionConfig == nil || !client.ExecutionConfig.IsCodeModeClient || len(tools) == 0 {
			continue
		}
		servers = append(servers, name)
	}
	slices.Sort(servers)
	return servers
}

// isKnownServer reports whether name case-insensitively equals one of servers.
func isKnownServer(name string, servers []string) bool {
	for _, server := range servers {
		if strings.EqualFold(name, server) {
			return true
		}
	}
	return false
}

// normalizeVFSPath strips the decorations callers commonly add to a VFS path:
// surrounding whitespace, a trailing "()", a leading "./" or "/", a "servers/"
// prefix in any case, a trailing "/" and a ".pyi" or ".py" extension.
func normalizeVFSPath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimSuffix(p, "()")
	p = strings.TrimPrefix(p, "./")
	p = strings.TrimLeft(p, "/")
	if len(p) >= len("servers/") && strings.EqualFold(p[:len("servers/")], "servers/") {
		p = p[len("servers/"):]
	}
	p = strings.TrimRight(p, "/")
	if trimmed, ok := strings.CutSuffix(p, ".pyi"); ok {
		return trimmed
	}
	return strings.TrimSuffix(p, ".py")
}

// splitDottedToolReference splits the callable form "server.tool" used in executeToolCode.
// Client names may contain dots, so a path that names a connected server is never split, and
// the split point is the longest dotted prefix that names a connected server. Without such a
// prefix it splits at the first dot, so the miss reports the server part. ok is false when
// path has no dot to split at.
func splitDottedToolReference(path string, servers []string) (serverName, toolName string, ok bool) {
	if isKnownServer(path, servers) {
		return "", "", false
	}
	for i := len(path) - 1; i > 0; i-- {
		if path[i] == '.' && i < len(path)-1 && isKnownServer(path[:i], servers) {
			return path[:i], path[i+1:], true
		}
	}
	if i := strings.IndexByte(path, '.'); i > 0 && i < len(path)-1 {
		return path[:i], path[i+1:], true
	}
	return "", "", false
}

// parseVFSFilePath parses a VFS file path and extracts the server name and optional tool name.
// Besides servers/<server>.pyi and servers/<server>/<tool>.pyi it accepts the callable form
// <server>.<tool> and the spellings normalizeVFSPath strips. servers lists the connected server
// names, used to keep dotted server names whole.
func parseVFSFilePath(fileName string, servers []string) (serverName, toolName string, isToolLevel bool) {
	basePath := normalizeVFSPath(fileName)

	// Defensive validation: reject paths with path traversal attempts
	if strings.Contains(basePath, "..") {
		// Return empty to indicate invalid path
		return "", "", false
	}

	// Check for path separator
	parts := strings.Split(basePath, "/")
	if len(parts) == 2 {
		// Tool-level: "serverName/toolName"
		// Validate that tool name doesn't contain additional path separators or traversal
		if parts[1] == "" || strings.Contains(parts[1], "/") || strings.Contains(parts[1], "..") {
			// Invalid tool name, treat as server-level
			return parts[0], "", false
		}
		return parts[0], parts[1], true
	}
	// Server-level: "serverName"
	// Validate server name doesn't contain path separators or traversal
	if strings.Contains(basePath, "/") || strings.Contains(basePath, "..") {
		// Invalid path
		return "", "", false
	}
	if server, tool, ok := splitDottedToolReference(basePath, servers); ok {
		return server, tool, true
	}
	return basePath, "", false
}

// generateCompactSignatures generates compact Python function signatures for tools.
// jsonModuleAvailable says whether the sandbox's json module is reachable (see generateTypeDefinitions).
func generateCompactSignatures(clientName string, tools []schemas.ChatTool, isToolLevel bool, instructions string, jsonModuleAvailable bool) string {
	var sb strings.Builder

	// Minimal header
	if isToolLevel && len(tools) == 1 && tools[0].Function != nil {
		toolName := getCanonicalToolName(clientName, tools[0].Function.Name)
		sb.WriteString(fmt.Sprintf("# %s.%s tool\n", clientName, toolName))
	} else {
		sb.WriteString(fmt.Sprintf("# %s server tools\n", clientName))
	}
	sb.WriteString(fmt.Sprintf("# Usage: %s.tool_name(param=value)\n", clientName))
	sb.WriteString("# The def names below are the exact callable names to use in executeToolCode.\n")
	sb.WriteString("# Read this file before executeToolCode to confirm parameters and return shape.\n")
	sb.WriteString("# Results: the decoded JSON when the server sends JSON (a dict or list, or a number, bool or None), otherwise a str.\n")
	if jsonModuleAvailable {
		sb.WriteString("# json.decode(s) parses a str that holds JSON; plain text raises.\n")
	}
	sb.WriteString("# A call that fails returns an error value: check hasattr(r, \"error\"); r.error holds the message, and fail(r.error) stops the run.\n")
	sb.WriteString(fmt.Sprintf("# For detailed docs: use getToolDocs(server=\"%s\", tool=\"tool_name\")\n", clientName))
	sb.WriteString("# Note: Descriptions may be truncated. Use getToolDocs for full details.\n")
	if instructions != "" {
		sb.WriteString("#\n")
		sb.WriteString(instructions)
	}
	sb.WriteString("\n")

	for _, tool := range tools {
		if tool.Function == nil || tool.Function.Name == "" {
			continue
		}

		toolName := getCanonicalToolName(clientName, tool.Function.Name)

		// Format inline parameters in Python style
		params := formatPythonParams(tool.Function.Parameters)

		// Get description (truncate if too long)
		desc := ""
		if tool.Function.Description != nil && *tool.Function.Description != "" {
			desc = *tool.Function.Description
			// Truncate long descriptions to first sentence or 80 chars
			if idx := strings.Index(desc, ". "); idx > 0 && idx < 80 {
				desc = desc[:idx+1]
			} else if len(desc) > 80 {
				desc = desc[:77] + "..."
			}
		}

		// Write Python signature: def tool_name(param: type, param: type = None) -> dict:  # description
		if desc != "" {
			sb.WriteString(fmt.Sprintf("def %s(%s) -> dict:  # %s\n", toolName, params, desc))
		} else {
			sb.WriteString(fmt.Sprintf("def %s(%s) -> dict\n", toolName, params))
		}
	}

	return sb.String()
}

// formatPythonParams formats tool parameters as Python function parameters.
func formatPythonParams(params *schemas.ToolFunctionParameters) string {
	if params == nil || params.Properties == nil || params.Properties.Len() == 0 {
		return ""
	}

	props := params.Properties
	required := make(map[string]bool)
	if params.Required != nil {
		for _, req := range params.Required {
			required[req] = true
		}
	}

	// Sort properties: required first, then optional, alphabetically within each group
	requiredNames := make([]string, 0)
	optionalNames := make([]string, 0)
	props.Range(func(name string, _ interface{}) bool {
		if required[name] {
			requiredNames = append(requiredNames, name)
		} else {
			optionalNames = append(optionalNames, name)
		}
		return true
	})
	// Simple alphabetical sort for each group
	for i := 0; i < len(requiredNames)-1; i++ {
		for j := i + 1; j < len(requiredNames); j++ {
			if requiredNames[i] > requiredNames[j] {
				requiredNames[i], requiredNames[j] = requiredNames[j], requiredNames[i]
			}
		}
	}
	for i := 0; i < len(optionalNames)-1; i++ {
		for j := i + 1; j < len(optionalNames); j++ {
			if optionalNames[i] > optionalNames[j] {
				optionalNames[i], optionalNames[j] = optionalNames[j], optionalNames[i]
			}
		}
	}

	parts := make([]string, 0, props.Len())

	// Add required params first
	for _, propName := range requiredNames {
		prop, _ := props.Get(propName)
		propSchema, ok := asJSONSchema(prop)
		if !ok {
			continue
		}
		pyType := jsonSchemaToPython(propSchema)
		parts = append(parts, fmt.Sprintf("%s: %s", propName, pyType))
	}

	// Add optional params with default None
	for _, propName := range optionalNames {
		prop, _ := props.Get(propName)
		propSchema, ok := asJSONSchema(prop)
		if !ok {
			continue
		}
		pyType := jsonSchemaToPython(propSchema)
		parts = append(parts, fmt.Sprintf("%s: %s = None", propName, pyType))
	}

	return strings.Join(parts, ", ")
}

// jsonSchema is a read-only view of one JSON Schema object.
type jsonSchema interface {
	Get(key string) (interface{}, bool)
}

// plainSchema adapts a decoded map[string]interface{} to jsonSchema.
type plainSchema map[string]interface{}

func (s plainSchema) Get(key string) (interface{}, bool) {
	v, ok := s[key]
	return v, ok
}

// asJSONSchema returns v as a jsonSchema without copying it. Live discovery keeps property
// schemas as map[string]interface{}, but a per-call client restores its tools from the config
// store after a restart, and that decode turns every nested object into *OrderedMap. Both
// shapes have to render the same stub.
func asJSONSchema(v interface{}) (jsonSchema, bool) {
	switch node := v.(type) {
	case map[string]interface{}:
		return plainSchema(node), true
	case *schemas.OrderedMap:
		if node == nil {
			return nil, false
		}
		return node, true
	}
	return nil, false
}

// jsonSchemaToPython converts a JSON Schema type definition to a Python type string.
func jsonSchemaToPython(prop jsonSchema) string {
	// Check for enum first - takes precedence over type to show allowed values
	enumVal, _ := prop.Get("enum")
	if enum, ok := enumVal.([]interface{}); ok && len(enum) > 0 {
		enumStrs := make([]string, 0, len(enum))
		for _, e := range enum {
			enumStrs = append(enumStrs, fmt.Sprintf("%q", e))
		}
		return "Literal[" + strings.Join(enumStrs, ", ") + "]"
	}

	// Check for const (single fixed value)
	if constVal, ok := prop.Get("const"); ok {
		return fmt.Sprintf("Literal[%q]", constVal)
	}

	// Fall back to type-based conversion
	typeField, _ := prop.Get("type")
	if typeVal, ok := typeField.(string); ok {
		switch typeVal {
		case "string":
			return "str"
		case "number":
			return "float"
		case "integer":
			return "int"
		case "boolean":
			return "bool"
		case "array":
			itemsType := "Any"
			itemsVal, _ := prop.Get("items")
			if items, ok := asJSONSchema(itemsVal); ok {
				itemsType = jsonSchemaToPython(items)
			}
			return fmt.Sprintf("list[%s]", itemsType)
		case "object":
			return "dict"
		case "null":
			return "None"
		}
	}

	return "Any"
}

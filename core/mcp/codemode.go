//go:build !tinygo && !wasm

package mcp

import (
	"context"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// CodeMode tool type constants
const (
	ToolTypeListToolFiles   string = "listToolFiles"
	ToolTypeReadToolFile    string = "readToolFile"
	ToolTypeGetToolDocs     string = "getToolDocs"
	ToolTypeExecuteToolCode string = "executeToolCode"
)

// CodeModeLogPrefix is the log prefix for code mode operations
const CodeModeLogPrefix = "[CODE MODE]"

// CodeMode defines the interface for code execution environments.
// Implementations can provide different interpreters (Starlark, Lua, JavaScript, etc.)
// while maintaining the same tool interface for the ToolsManager.
type CodeMode interface {
	// GetTools returns the code mode meta-tools (listToolFiles, readToolFile, getToolDocs, executeToolCode)
	// These tools are added to the available tools when a code mode client is connected.
	GetTools() []schemas.ChatTool

	// ExecuteTool handles a code mode tool call by name.
	// Returns the response message and any error that occurred.
	ExecuteTool(ctx context.Context, toolCall schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, error)

	// IsCodeModeTool returns true if the given tool name is a code mode tool.
	IsCodeModeTool(toolName string) bool

	// GetBindingLevel returns the current code mode binding level (server or tool).
	GetBindingLevel() schemas.CodeModeBindingLevel

	// UpdateConfig updates the code mode configuration atomically.
	UpdateConfig(config *CodeModeConfig)

	// SetDependencies sets the dependencies required for code execution.
	// This is called by MCPManager after construction to inject the dependencies
	// (ClientManager, plugin pipeline, etc.) that weren't available at CodeMode creation time.
	SetDependencies(deps *CodeModeDependencies)
}

// CodeModeConfig holds the configuration for a CodeMode implementation.
type CodeModeConfig struct {
	// BindingLevel controls how tools are exposed in the VFS: "server" or "tool"
	BindingLevel schemas.CodeModeBindingLevel

	// ToolExecutionTimeout is the maximum time allowed for tool execution
	ToolExecutionTimeout time.Duration
}

// CodeModeDependencies holds the dependencies required by CodeMode implementations.
type CodeModeDependencies struct {
	// ClientManager provides access to MCP clients and their tools
	ClientManager ClientManager

	// PluginPipelineProvider returns a plugin pipeline for running MCP hooks
	PluginPipelineProvider func() PluginPipeline

	// ReleasePluginPipeline releases a plugin pipeline back to the pool
	ReleasePluginPipeline func(pipeline PluginPipeline)

	// FetchNewRequestIDFunc generates unique request IDs for nested tool calls
	FetchNewRequestIDFunc func(ctx *schemas.BifrostContext) string

	// LogMutex protects concurrent access to logs during code execution
	LogMutex *sync.Mutex
}

// DefaultCodeModeConfig returns the default configuration for CodeMode.
func DefaultCodeModeConfig() *CodeModeConfig {
	return &CodeModeConfig{
		BindingLevel:         schemas.CodeModeBindingLevelServer,
		ToolExecutionTimeout: schemas.DefaultToolExecutionTimeout,
	}
}

// codeModeToolNames is a set of all code mode tool names for fast lookup
var codeModeToolNames = map[string]bool{
	ToolTypeListToolFiles:   true,
	ToolTypeReadToolFile:    true,
	ToolTypeGetToolDocs:     true,
	ToolTypeExecuteToolCode: true,
}

// IsCodeModeTool returns true if the given tool name is a code mode tool.
// This is a package-level helper function.
func IsCodeModeTool(toolName string) bool {
	return codeModeToolNames[toolName]
}

// toolCallInfo represents a tool call extracted from code.
// Used for validating tool calls before auto-execution in agent mode.
type toolCallInfo struct {
	serverName string
	toolName   string
}

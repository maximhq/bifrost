//go:build !tinygo && !wasm

// Package starlark provides a Starlark-based implementation of the CodeMode interface.
// Starlark is a Python-like language designed for configuration and embedded scripting.
// See https://github.com/google/starlark-go for more information.
package starlark

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/schemas"
)

// StarlarkCodeMode implements the CodeMode interface using a Starlark interpreter.
// It provides a sandboxed Python-like execution environment with access to MCP tools.
type StarlarkCodeMode struct {
	// Configuration (atomic for thread-safe updates)
	bindingLevel         atomic.Value // schemas.CodeModeBindingLevel
	toolExecutionTimeout atomic.Value // time.Duration

	// Dependencies
	clientManager          mcp.ClientManager
	pluginPipelineProvider func() mcp.PluginPipeline
	releasePluginPipeline  func(pipeline mcp.PluginPipeline)
	fetchNewRequestIDFunc  func(ctx *schemas.BifrostContext) string

	// Logger for this instance
	logger schemas.Logger

	// Mutex for protecting logs during concurrent execution
	logMu sync.Mutex
}

// NewStarlarkCodeMode creates a new Starlark-based CodeMode implementation.
//
// Parameters:
//   - config: Configuration for the code mode (binding level, timeouts). Can be nil for defaults.
//   - logger: Logger instance for this code mode. Can be nil.
//
// Returns:
//   - *StarlarkCodeMode: A new Starlark code mode instance
//
// Note: Dependencies must be set via SetDependencies before the CodeMode can execute tools.
// This allows the CodeMode to be created before the MCPManager, avoiding circular dependencies.
func NewStarlarkCodeMode(config *mcp.CodeModeConfig, logger schemas.Logger) *StarlarkCodeMode {
	if config == nil {
		config = mcp.DefaultCodeModeConfig()
	}

	if config.BindingLevel == "" {
		config.BindingLevel = schemas.CodeModeBindingLevelServer
	}

	if config.ToolExecutionTimeout <= 0 {
		config.ToolExecutionTimeout = schemas.DefaultToolExecutionTimeout
	}

	if logger == nil {
		logger = defaultLogger
	}

	s := &StarlarkCodeMode{
		logger: logger,
	}

	// Initialize atomic values
	s.bindingLevel.Store(config.BindingLevel)
	s.toolExecutionTimeout.Store(config.ToolExecutionTimeout)

	s.logger.Info("%s Starlark code mode initialized with binding level: %s, timeout: %v",
		mcp.CodeModeLogPrefix, config.BindingLevel, config.ToolExecutionTimeout)

	return s
}

// SetDependencies sets the dependencies required for code execution.
// This must be called after the MCPManager is created, as the dependencies
// include the ClientManager (which is the MCPManager itself).
func (s *StarlarkCodeMode) SetDependencies(deps *mcp.CodeModeDependencies) {
	if deps != nil {
		s.clientManager = deps.ClientManager
		s.pluginPipelineProvider = deps.PluginPipelineProvider
		s.releasePluginPipeline = deps.ReleasePluginPipeline
		s.fetchNewRequestIDFunc = deps.FetchNewRequestIDFunc
	}
}

// GetTools returns the code mode meta-tools for Starlark execution.
// These tools allow LLMs to discover, read, and execute code against MCP servers.
func (s *StarlarkCodeMode) GetTools() []schemas.ChatTool {
	return []schemas.ChatTool{
		s.createListToolFilesTool(),
		s.createReadToolFileTool(),
		s.createGetToolDocsTool(),
		s.createExecuteToolCodeTool(),
	}
}

// ExecuteTool handles a code mode tool call.
// It dispatches to the appropriate handler based on the tool name.
//
// Parameters:
//   - ctx: Context for tool execution
//   - toolCall: The tool call to execute
//
// Returns:
//   - *schemas.ChatMessage: The tool response message
//   - error: Any error that occurred during execution
func (s *StarlarkCodeMode) ExecuteTool(ctx context.Context, toolCall schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, error) {
	if toolCall.Function.Name == nil {
		return nil, fmt.Errorf("tool call missing function name")
	}

	toolName := *toolCall.Function.Name

	switch toolName {
	case mcp.ToolTypeListToolFiles:
		return s.handleListToolFiles(ctx, toolCall)
	case mcp.ToolTypeReadToolFile:
		return s.handleReadToolFile(ctx, toolCall)
	case mcp.ToolTypeGetToolDocs:
		return s.handleGetToolDocs(ctx, toolCall)
	case mcp.ToolTypeExecuteToolCode:
		return s.handleExecuteToolCode(ctx, toolCall)
	default:
		return nil, fmt.Errorf("unknown code mode tool: %s", toolName)
	}
}

// IsCodeModeTool returns true if the given tool name is a code mode tool.
func (s *StarlarkCodeMode) IsCodeModeTool(toolName string) bool {
	return mcp.IsCodeModeTool(toolName)
}

// GetBindingLevel returns the current code mode binding level.
func (s *StarlarkCodeMode) GetBindingLevel() schemas.CodeModeBindingLevel {
	val := s.bindingLevel.Load()
	if val == nil {
		return schemas.CodeModeBindingLevelServer
	}
	return val.(schemas.CodeModeBindingLevel)
}

// UpdateConfig updates the code mode configuration atomically.
func (s *StarlarkCodeMode) UpdateConfig(config *mcp.CodeModeConfig) {
	if config == nil {
		return
	}

	if config.BindingLevel != "" {
		s.bindingLevel.Store(config.BindingLevel)
	}

	if config.ToolExecutionTimeout > 0 {
		s.toolExecutionTimeout.Store(config.ToolExecutionTimeout)
	}

	s.logger.Info("%s Starlark code mode configuration updated: binding level=%s, timeout=%v",
		mcp.CodeModeLogPrefix, config.BindingLevel, config.ToolExecutionTimeout)
}

// getToolExecutionTimeout returns the current tool execution timeout.
func (s *StarlarkCodeMode) getToolExecutionTimeout() time.Duration {
	val := s.toolExecutionTimeout.Load()
	if val == nil {
		return schemas.DefaultToolExecutionTimeout
	}
	return val.(time.Duration)
}

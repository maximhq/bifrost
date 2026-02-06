package mcptests

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// MOCK STDIO MCP SERVER
// =============================================================================

// STDIOServerManager manages a STDIO MCP server for testing
type STDIOServerManager struct {
	server     *server.MCPServer
	cmd        *exec.Cmd
	stdinPipe  *os.File
	stdoutPipe *os.File
	isRunning  bool
	mu         sync.RWMutex
	serverPath string // Path to the compiled server executable
	t          *testing.T
}

// NewSTDIOServerManager creates a new STDIO server manager for testing
func NewSTDIOServerManager(t *testing.T) *STDIOServerManager {
	t.Helper()

	return &STDIOServerManager{
		t: t,
	}
}

// Start starts the STDIO server
func (m *STDIOServerManager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.isRunning {
		return fmt.Errorf("server already running")
	}

	// Create a new MCP server
	m.server = server.NewMCPServer(
		"test-stdio-server",
		"1.0.0",
		server.WithToolCapabilities(true),
	)

	// Register test tools
	if err := m.registerTestTools(); err != nil {
		return fmt.Errorf("failed to register tools: %w", err)
	}

	m.isRunning = true
	return nil
}

// Stop stops the STDIO server
func (m *STDIOServerManager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.isRunning {
		return nil
	}

	if m.cmd != nil && m.cmd.Process != nil {
		if err := m.cmd.Process.Kill(); err != nil {
			return fmt.Errorf("failed to kill process: %w", err)
		}
	}

	m.isRunning = false
	return nil
}

// IsRunning returns whether the server is running
func (m *STDIOServerManager) IsRunning() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.isRunning
}

// registerTestTools registers test tools on the server
func (m *STDIOServerManager) registerTestTools() error {
	// Calculator tool
	calculatorTool := mcp.NewTool("calculator",
		mcp.WithDescription("Performs basic arithmetic operations"),
		mcp.WithString("operation",
			mcp.Required(),
			mcp.Description("The operation to perform: add, subtract, multiply, divide"),
			mcp.Enum("add", "subtract", "multiply", "divide"),
		),
		mcp.WithNumber("x",
			mcp.Required(),
			mcp.Description("First number"),
		),
		mcp.WithNumber("y",
			mcp.Required(),
			mcp.Description("Second number"),
		),
	)

	m.server.AddTool(calculatorTool, m.handleCalculator)

	// Echo tool
	echoTool := mcp.NewTool("echo",
		mcp.WithDescription("Echoes back the input message"),
		mcp.WithString("message",
			mcp.Required(),
			mcp.Description("The message to echo"),
		),
	)

	m.server.AddTool(echoTool, m.handleEcho)

	// Weather tool (for testing external data)
	weatherTool := mcp.NewTool("get_weather",
		mcp.WithDescription("Gets the weather for a location"),
		mcp.WithString("location",
			mcp.Required(),
			mcp.Description("The location to get weather for"),
		),
		mcp.WithString("units",
			mcp.Description("Temperature units: celsius or fahrenheit"),
			mcp.Enum("celsius", "fahrenheit"),
		),
	)

	m.server.AddTool(weatherTool, m.handleWeather)

	// Delay tool (for timeout testing)
	delayTool := mcp.NewTool("delay",
		mcp.WithDescription("Delays for a specified duration"),
		mcp.WithNumber("seconds",
			mcp.Required(),
			mcp.Description("Number of seconds to delay"),
		),
	)

	m.server.AddTool(delayTool, m.handleDelay)

	// Error tool (for error testing)
	errorTool := mcp.NewTool("throw_error",
		mcp.WithDescription("Throws an error for testing"),
		mcp.WithString("error_message",
			mcp.Required(),
			mcp.Description("The error message to throw"),
		),
	)

	m.server.AddTool(errorTool, m.handleError)

	return nil
}

// Tool handlers

func (m *STDIOServerManager) handleCalculator(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Operation string  `json:"operation"`
		X         float64 `json:"x"`
		Y         float64 `json:"y"`
	}

	argsBytes, ok := request.Params.Arguments.(string)
	if !ok {
		return mcp.NewToolResultError("invalid arguments type"), nil
	}
	if err := json.Unmarshal([]byte(argsBytes), &args); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid arguments: %v", err)), nil
	}

	var result float64
	switch args.Operation {
	case "add":
		result = args.X + args.Y
	case "subtract":
		result = args.X - args.Y
	case "multiply":
		result = args.X * args.Y
	case "divide":
		if args.Y == 0 {
			return mcp.NewToolResultError("division by zero"), nil
		}
		result = args.X / args.Y
	default:
		return mcp.NewToolResultError(fmt.Sprintf("unknown operation: %s", args.Operation)), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf("%.2f", result)), nil
}

func (m *STDIOServerManager) handleEcho(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Message string `json:"message"`
	}

	argsBytes, ok := request.Params.Arguments.(string)
	if !ok {
		return mcp.NewToolResultError("invalid arguments type"), nil
	}
	if err := json.Unmarshal([]byte(argsBytes), &args); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid arguments: %v", err)), nil
	}

	return mcp.NewToolResultText(args.Message), nil
}

func (m *STDIOServerManager) handleWeather(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Location string `json:"location"`
		Units    string `json:"units"`
	}

	argsBytes, ok := request.Params.Arguments.(string)
	if !ok {
		return mcp.NewToolResultError("invalid arguments type"), nil
	}
	if err := json.Unmarshal([]byte(argsBytes), &args); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid arguments: %v", err)), nil
	}

	if args.Units == "" {
		args.Units = "celsius"
	}

	// Mock weather response
	temp := "22"
	if args.Units == "fahrenheit" {
		temp = "72"
	}

	response := fmt.Sprintf("The weather in %s is sunny with a temperature of %s°%s",
		args.Location, temp, args.Units)

	return mcp.NewToolResultText(response), nil
}

func (m *STDIOServerManager) handleDelay(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Seconds float64 `json:"seconds"`
	}

	argsBytes, ok := request.Params.Arguments.(string)
	if !ok {
		return mcp.NewToolResultError("invalid arguments type"), nil
	}
	if err := json.Unmarshal([]byte(argsBytes), &args); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid arguments: %v", err)), nil
	}

	duration := time.Duration(args.Seconds * float64(time.Second))
	select {
	case <-time.After(duration):
		return mcp.NewToolResultText(fmt.Sprintf("Delayed for %.2f seconds", args.Seconds)), nil
	case <-ctx.Done():
		return mcp.NewToolResultError("delay cancelled"), nil
	}
}

func (m *STDIOServerManager) handleError(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		ErrorMessage string `json:"error_message"`
	}

	argsBytes, ok := request.Params.Arguments.(string)
	if !ok {
		return mcp.NewToolResultError("invalid arguments type"), nil
	}
	if err := json.Unmarshal([]byte(argsBytes), &args); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("invalid arguments: %v", err)), nil
	}

	return mcp.NewToolResultError(args.ErrorMessage), nil
}

// GetServerExecutablePath returns the path where the STDIO server will be compiled
func (m *STDIOServerManager) GetServerExecutablePath() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.serverPath
}

// =============================================================================
// HELPER FUNCTIONS
// =============================================================================

// createTestContext creates a BifrostContext for testing
func createTestContext() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
}

// createTestContextWithTimeout creates a BifrostContext with timeout
func createTestContextWithTimeout(timeout time.Duration) (*schemas.BifrostContext, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	return schemas.NewBifrostContext(ctx, schemas.NoDeadline), cancel
}

// assertNoError asserts that error is nil
func assertNoError(t *testing.T, err error, msgAndArgs ...interface{}) {
	t.Helper()
	require.NoError(t, err, msgAndArgs...)
}

// assertError asserts that error is not nil
func assertError(t *testing.T, err error, msgAndArgs ...interface{}) {
	t.Helper()
	require.Error(t, err, msgAndArgs...)
}

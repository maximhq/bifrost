package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func main() {
	// Seed random number generator
	rand.Seed(time.Now().UnixNano())

	// Create MCP server
	s := server.NewMCPServer(
		"error-test-server",
		"1.0.0",
		server.WithToolCapabilities(true),
	)

	// Register all tools
	registerTimeoutAfterTool(s)
	registerReturnMalformedJSONTool(s)
	registerReturnErrorTool(s)
	registerIntermittentFailTool(s)
	registerMemoryIntensiveTool(s)

	// Start STDIO server
	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}

// ============================================================================
// TOOL 1: timeout_after
// ============================================================================

func registerTimeoutAfterTool(s *server.MCPServer) {
	tool := mcp.NewTool("timeout_after",
		mcp.WithDescription("Simulates a timeout by delaying for specified seconds"),
		mcp.WithNumber("seconds",
			mcp.Required(),
			mcp.Description("Number of seconds to wait before responding"),
		),
	)

	s.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			Seconds float64 `json:"seconds"`
		}

		// Get arguments using the proper method
		argsInterface := request.GetArguments()

		// Marshal and unmarshal to convert to our struct
		argsBytes, err := json.Marshal(argsInterface)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Failed to marshal arguments: %v", err)), nil
		}

		if err := json.Unmarshal(argsBytes, &args); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Invalid arguments: %v", err)), nil
		}

		duration := time.Duration(args.Seconds * float64(time.Second))

		// Use context-aware sleep
		select {
		case <-time.After(duration):
			response := map[string]interface{}{
				"delayed_seconds": args.Seconds,
				"message":         fmt.Sprintf("Delayed for %.2f seconds", args.Seconds),
			}
			jsonResult, _ := json.Marshal(response)
			return mcp.NewToolResultText(string(jsonResult)), nil
		case <-ctx.Done():
			return mcp.NewToolResultError("Operation cancelled or timed out"), nil
		}
	})
}

// ============================================================================
// TOOL 2: return_malformed_json
// ============================================================================

func registerReturnMalformedJSONTool(s *server.MCPServer) {
	tool := mcp.NewTool("return_malformed_json",
		mcp.WithDescription("Returns intentionally malformed JSON to test error handling"),
	)

	s.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Return deliberately broken JSON
		// Note: This will be wrapped in the MCP protocol, so the MCP layer should handle it
		// But the content itself is invalid JSON
		malformedJSON := `{"key": "value", "broken": }`
		return mcp.NewToolResultText(malformedJSON), nil
	})
}

// ============================================================================
// TOOL 3: return_error
// ============================================================================

func registerReturnErrorTool(s *server.MCPServer) {
	tool := mcp.NewTool("return_error",
		mcp.WithDescription("Returns an error with specified type"),
		mcp.WithString("error_type",
			mcp.Required(),
			mcp.Description("Type of error to return"),
			mcp.Enum("validation", "runtime", "network", "timeout", "permission"),
		),
	)

	s.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			ErrorType string `json:"error_type"`
		}

		// Get arguments using the proper method
		argsInterface := request.GetArguments()

		// Marshal and unmarshal to convert to our struct
		argsBytes, err := json.Marshal(argsInterface)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Failed to marshal arguments: %v", err)), nil
		}

		if err := json.Unmarshal(argsBytes, &args); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Invalid arguments: %v", err)), nil
		}

		var errorMessage string
		switch args.ErrorType {
		case "validation":
			errorMessage = "Validation Error: Invalid input parameters provided"
		case "runtime":
			errorMessage = "Runtime Error: Unexpected condition occurred during execution"
		case "network":
			errorMessage = "Network Error: Failed to connect to remote service"
		case "timeout":
			errorMessage = "Timeout Error: Operation exceeded maximum allowed time"
		case "permission":
			errorMessage = "Permission Error: Insufficient privileges to perform operation"
		default:
			errorMessage = fmt.Sprintf("Unknown error type: %s", args.ErrorType)
		}

		return mcp.NewToolResultError(errorMessage), nil
	})
}

// ============================================================================
// TOOL 4: intermittent_fail
// ============================================================================

func registerIntermittentFailTool(s *server.MCPServer) {
	tool := mcp.NewTool("intermittent_fail",
		mcp.WithDescription("Fails randomly based on specified fail rate percentage (0-100)"),
		mcp.WithNumber("fail_rate",
			mcp.Required(),
			mcp.Description("Percentage chance of failure (0-100)"),
		),
	)

	s.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			FailRate float64 `json:"fail_rate"`
		}

		// Get arguments using the proper method
		argsInterface := request.GetArguments()

		// Marshal and unmarshal to convert to our struct
		argsBytes, err := json.Marshal(argsInterface)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Failed to marshal arguments: %v", err)), nil
		}

		if err := json.Unmarshal(argsBytes, &args); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Invalid arguments: %v", err)), nil
		}

		// Validate fail rate
		if args.FailRate < 0 || args.FailRate > 100 {
			return mcp.NewToolResultError("Fail rate must be between 0 and 100"), nil
		}

		// Generate random number between 0-100
		randomValue := rand.Float64() * 100

		if randomValue < args.FailRate {
			// Fail
			return mcp.NewToolResultError(fmt.Sprintf("Intermittent failure (fail_rate: %.1f%%, random: %.2f)", args.FailRate, randomValue)), nil
		}

		// Success
		response := map[string]interface{}{
			"success":   true,
			"fail_rate": args.FailRate,
			"random":    randomValue,
			"message":   "Operation succeeded",
		}

		jsonResult, _ := json.Marshal(response)
		return mcp.NewToolResultText(string(jsonResult)), nil
	})
}

// ============================================================================
// TOOL 5: memory_intensive
// ============================================================================

func registerMemoryIntensiveTool(s *server.MCPServer) {
	tool := mcp.NewTool("memory_intensive",
		mcp.WithDescription("Allocates specified amount of memory to test resource limits"),
		mcp.WithNumber("size_mb",
			mcp.Required(),
			mcp.Description("Amount of memory to allocate in megabytes"),
		),
	)

	s.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			SizeMB int `json:"size_mb"`
		}

		// Get arguments using the proper method
		argsInterface := request.GetArguments()

		// Marshal and unmarshal to convert to our struct
		argsBytes, err := json.Marshal(argsInterface)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Failed to marshal arguments: %v", err)), nil
		}

		if err := json.Unmarshal(argsBytes, &args); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Invalid arguments: %v", err)), nil
		}

		// Limit to reasonable size to prevent crashes
		if args.SizeMB > 100 {
			return mcp.NewToolResultError("Size limited to 100MB for safety"), nil
		}

		// Allocate memory (use int64 to prevent overflow)
		sizeBytes := int64(args.SizeMB) * 1024 * 1024
		data := make([]byte, sizeBytes)

		// Fill with pattern to ensure allocation
		for i := range data {
			data[i] = byte(i % 256)
		}

		// Calculate checksum to verify allocation
		var checksum uint64
		for _, b := range data {
			checksum += uint64(b)
		}

		response := map[string]interface{}{
			"allocated_mb": args.SizeMB,
			"allocated_bytes": sizeBytes,
			"checksum":     checksum,
			"message":      fmt.Sprintf("Successfully allocated %dMB", args.SizeMB),
		}

		// Clear memory before returning
		data = nil

		jsonResult, _ := json.Marshal(response)
		return mcp.NewToolResultText(string(jsonResult)), nil
	})
}

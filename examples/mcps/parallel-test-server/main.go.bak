package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func main() {
	// Create MCP server
	s := server.NewMCPServer(
		"parallel-test-server",
		"1.0.0",
		server.WithToolCapabilities(true),
	)

	// Register all tools
	registerFastOperationTool(s)
	registerMediumOperationTool(s)
	registerSlowOperationTool(s)
	registerVerySlowOperationTool(s)
	registerReturnTimestampTool(s)

	// Start STDIO server
	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}

// ============================================================================
// TOOL 1: fast_operation
// ============================================================================

func registerFastOperationTool(s *server.MCPServer) {
	tool := mcp.NewTool("fast_operation",
		mcp.WithDescription("Returns immediately (< 10ms)"),
	)

	s.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		start := time.Now()

		response := map[string]interface{}{
			"operation": "fast",
			"timestamp": start.UnixNano(),
			"message":   "Fast operation completed",
		}

		elapsed := time.Since(start)
		response["elapsed_ms"] = float64(elapsed.Nanoseconds()) / 1e6

		jsonResult, _ := json.Marshal(response)
		return mcp.NewToolResultText(string(jsonResult)), nil
	})
}

// ============================================================================
// TOOL 2: medium_operation
// ============================================================================

func registerMediumOperationTool(s *server.MCPServer) {
	tool := mcp.NewTool("medium_operation",
		mcp.WithDescription("Takes 100-200ms to complete"),
	)

	s.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		start := time.Now()

		// Sleep for 150ms
		time.Sleep(150 * time.Millisecond)

		response := map[string]interface{}{
			"operation": "medium",
			"timestamp": start.UnixNano(),
			"message":   "Medium operation completed",
		}

		elapsed := time.Since(start)
		response["elapsed_ms"] = float64(elapsed.Nanoseconds()) / 1e6

		jsonResult, _ := json.Marshal(response)
		return mcp.NewToolResultText(string(jsonResult)), nil
	})
}

// ============================================================================
// TOOL 3: slow_operation
// ============================================================================

func registerSlowOperationTool(s *server.MCPServer) {
	tool := mcp.NewTool("slow_operation",
		mcp.WithDescription("Takes 500-1000ms to complete"),
	)

	s.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		start := time.Now()

		// Sleep for 750ms
		time.Sleep(750 * time.Millisecond)

		response := map[string]interface{}{
			"operation": "slow",
			"timestamp": start.UnixNano(),
			"message":   "Slow operation completed",
		}

		elapsed := time.Since(start)
		response["elapsed_ms"] = float64(elapsed.Nanoseconds()) / 1e6

		jsonResult, _ := json.Marshal(response)
		return mcp.NewToolResultText(string(jsonResult)), nil
	})
}

// ============================================================================
// TOOL 4: very_slow_operation
// ============================================================================

func registerVerySlowOperationTool(s *server.MCPServer) {
	tool := mcp.NewTool("very_slow_operation",
		mcp.WithDescription("Takes 2-3 seconds to complete"),
	)

	s.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		start := time.Now()

		// Sleep for 2.5 seconds
		time.Sleep(2500 * time.Millisecond)

		response := map[string]interface{}{
			"operation": "very_slow",
			"timestamp": start.UnixNano(),
			"message":   "Very slow operation completed",
		}

		elapsed := time.Since(start)
		response["elapsed_ms"] = float64(elapsed.Nanoseconds()) / 1e6

		jsonResult, _ := json.Marshal(response)
		return mcp.NewToolResultText(string(jsonResult)), nil
	})
}

// ============================================================================
// TOOL 5: return_timestamp
// ============================================================================

func registerReturnTimestampTool(s *server.MCPServer) {
	tool := mcp.NewTool("return_timestamp",
		mcp.WithDescription("Returns high-precision timestamp immediately"),
	)

	s.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		now := time.Now()

		response := map[string]interface{}{
			"timestamp_unix":       now.Unix(),
			"timestamp_unix_nano":  now.UnixNano(),
			"timestamp_unix_micro": now.UnixMicro(),
			"timestamp_iso8601":    now.Format(time.RFC3339Nano),
			"message":              "Timestamp captured",
		}

		jsonResult, _ := json.Marshal(response)
		return mcp.NewToolResultText(string(jsonResult)), nil
	})
}

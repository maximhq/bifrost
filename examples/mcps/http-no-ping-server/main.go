package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// NoPingMCPServer is an HTTP MCP server that intentionally does not support ping.
// This demonstrates how to configure servers with is_ping_available=false in Bifrost
// when your MCP server implementation doesn't support the optional ping method.
func main() {
	// Create MCP server
	mcpServer := server.NewMCPServer(
		"http-no-ping-server",
		"1.0.0",
	)

	// Define tools using the proper NewTool API
	echoTool := mcp.NewTool(
		"echo",
		mcp.WithDescription("Echo back the input message"),
		mcp.WithString("message", mcp.Required(), mcp.Description("Message to echo")),
	)

	addTool := mcp.NewTool(
		"add",
		mcp.WithDescription("Add two numbers"),
		mcp.WithNumber("a", mcp.Required(), mcp.Description("First number")),
		mcp.WithNumber("b", mcp.Required(), mcp.Description("Second number")),
	)

	greetTool := mcp.NewTool(
		"greet",
		mcp.WithDescription("Greet someone by name"),
		mcp.WithString("name", mcp.Required(), mcp.Description("Name to greet")),
	)

	// Register tool handlers
	mcpServer.AddTool(echoTool, echoHandler)
	mcpServer.AddTool(addTool, addHandler)
	mcpServer.AddTool(greetTool, greetHandler)

	// Create HTTP server using StreamableHTTP transport
	httpServer := server.NewStreamableHTTPServer(mcpServer)

	port := 3001
	addr := fmt.Sprintf("localhost:%d", port)

	log.Printf("MCP server listening on http://%s/", addr)
	log.Printf("Note: This server does NOT support ping. Use is_ping_available=false in Bifrost config.")
	log.Printf("\nExample Bifrost config:")
	log.Printf(`
{
  "name": "http_no_ping_server",
  "connection_type": "http",
  "connection_string": "http://%s/",
  "is_ping_available": false,
  "tools_to_execute": ["*"]
}
`, addr)

	// Wrap the HTTP server with middleware that rejects ping requests
	wrappedHandler := noPingMiddleware(httpServer)

	if err := http.ListenAndServe(addr, wrappedHandler); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

// echoHandler handles the echo tool
func echoHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Extract arguments as JSON
	var args struct {
		Message string `json:"message"`
	}

	// Parse the arguments
	argBytes, err := json.Marshal(request.Params.Arguments)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to parse arguments: %v", err)), nil
	}

	if err := json.Unmarshal(argBytes, &args); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Invalid arguments: %v", err)), nil
	}

	result := fmt.Sprintf("Echo: %s", args.Message)
	return mcp.NewToolResultText(result), nil
}

// addHandler handles the add tool
func addHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Extract arguments as JSON
	var args struct {
		A float64 `json:"a"`
		B float64 `json:"b"`
	}

	// Parse the arguments
	argBytes, err := json.Marshal(request.Params.Arguments)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to parse arguments: %v", err)), nil
	}

	if err := json.Unmarshal(argBytes, &args); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Invalid arguments: %v", err)), nil
	}

	result := args.A + args.B
	return mcp.NewToolResultText(fmt.Sprintf("%v + %v = %v", args.A, args.B, result)), nil
}

// greetHandler handles the greet tool
func greetHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Extract arguments as JSON
	var args struct {
		Name string `json:"name"`
	}

	// Parse the arguments
	argBytes, err := json.Marshal(request.Params.Arguments)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to parse arguments: %v", err)), nil
	}

	if err := json.Unmarshal(argBytes, &args); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Invalid arguments: %v", err)), nil
	}

	result := fmt.Sprintf("Hello, %s! Welcome to the MCP server.", args.Name)
	return mcp.NewToolResultText(result), nil
}

// noPingMiddleware is HTTP middleware that rejects ping requests
// This allows us to demonstrate a server that doesn't support ping
func noPingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only intercept POST requests (MCP messages)
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}

		// Read the request body
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Failed to read request body", http.StatusBadRequest)
			return
		}

		// Parse the JSON-RPC request to check if it's a ping request
		var jsonRequest map[string]interface{}
		if err := json.Unmarshal(body, &jsonRequest); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}

		// Check if this is a ping request
		if method, ok := jsonRequest["method"].(string); ok && method == "ping" {
			// Reject ping requests with a method not found error
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)

			var id interface{}
			if idVal, ok := jsonRequest["id"]; ok {
				id = idVal
			}

			errorResponse := map[string]interface{}{
				"jsonrpc": "2.0",
				"error": map[string]interface{}{
					"code":    -32601,
					"message": "Method not found: ping is not supported by this server",
				},
				"id": id,
			}

			json.NewEncoder(w).Encode(errorResponse)
			return
		}

		// For non-ping requests, restore the body and pass through to the next handler
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		next.ServeHTTP(w, r)
	})
}

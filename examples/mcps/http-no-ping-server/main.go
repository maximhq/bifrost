package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"

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

	// Port defaults to 3001 but can be overridden via MCP_SERVER_PORT so multiple
	// instances (or a test harness facing a port conflict) can bind elsewhere.
	port := "3001"
	if p := os.Getenv("MCP_SERVER_PORT"); p != "" {
		port = p
	}
	addr := fmt.Sprintf("localhost:%s", port)

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
	// Enable the stateful handshake regression fixture only for harness runs.
	if os.Getenv("MCP_HANDSHAKE_RETRY_FIXTURE") == "1" {
		wrappedHandler = handshakeRetryMiddleware(wrappedHandler)
	}

	if err := http.ListenAndServe(addr, wrappedHandler); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

type handshakeRetryRequest struct {
	Method    string `json:"method"`
	SessionID string `json:"session_id"`
	Status    int    `json:"status"`
}

type handshakeRetryState struct {
	Sessions       int                     `json:"sessions"`
	Requests       []handshakeRetryRequest `json:"requests"`
	ClosedSessions []string                `json:"closed_sessions"`
	initialized    map[string]bool
}

// handshakeRetryMiddleware provides isolated, per-run stateful upstreams. The
// first initialized notification fails after allocating a session; initializing
// that session again is rejected. Only a fresh handshake can recover.
func handshakeRetryMiddleware(next http.Handler) http.Handler {
	var mu sync.Mutex
	states := make(map[string]*handshakeRetryState)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		control, isControl := strings.CutPrefix(r.URL.Path, "/api/handshake-retry/")
		scenario, isFixture := strings.CutPrefix(r.URL.Path, "/handshake-retry/")
		if !isControl && !isFixture {
			next.ServeHTTP(w, r)
			return
		}
		if isControl {
			scenario = control
		} else {
			var ok bool
			scenario, ok = strings.CutSuffix(scenario, "/mcp")
			if !ok {
				http.NotFound(w, r)
				return
			}
		}
		if scenario == "" || strings.Contains(scenario, "/") {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		state := states[scenario]
		if isControl {
			if state == nil {
				http.NotFound(w, r)
			} else if r.Method == http.MethodGet {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(state)
			} else if r.Method == http.MethodDelete {
				delete(states, scenario)
				w.WriteHeader(http.StatusOK)
			} else {
				w.WriteHeader(http.StatusMethodNotAllowed)
			}
			return
		}
		if state == nil {
			state = &handshakeRetryState{Requests: []handshakeRetryRequest{}, ClosedSessions: []string{}, initialized: make(map[string]bool)}
			states[scenario] = state
		}
		sid := r.Header.Get("Mcp-Session-Id")
		if r.Method == http.MethodDelete {
			state.ClosedSessions = append(state.ClosedSessions, sid)
			delete(state.initialized, sid)
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var request struct {
			Method string          `json:"method"`
			ID     json.RawMessage `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		record := len(state.Requests)
		state.Requests = append(state.Requests, handshakeRetryRequest{request.Method, sid, http.StatusOK})
		switch request.Method {
		case "initialize":
			if sid != "" {
				state.Requests[record].Status = http.StatusBadRequest
				http.Error(w, "session already initialized; create a fresh session", http.StatusBadRequest)
				return
			}
			state.Sessions++
			sessionID := fmt.Sprintf("session-%d", state.Sessions)
			state.initialized[sessionID] = false
			w.Header().Set("Mcp-Session-Id", sessionID)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":%q,"capabilities":{"tools":{}},"serverInfo":{"name":"handshake-retry-fixture","version":"1.0"}}}`, request.ID, mcp.LATEST_PROTOCOL_VERSION)
		case "notifications/initialized":
			if _, exists := state.initialized[sid]; !exists {
				state.Requests[record].Status = http.StatusNotFound
				http.Error(w, "unknown session", http.StatusNotFound)
				return
			}
			if sid == "session-1" {
				state.Requests[record].Status = http.StatusServiceUnavailable
				http.Error(w, "temporary upstream failure", http.StatusServiceUnavailable)
				return
			}
			state.initialized[sid] = true
			state.Requests[record].Status = http.StatusAccepted
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			if !state.initialized[sid] {
				state.Requests[record].Status = http.StatusBadRequest
				http.Error(w, "session not initialized", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"echo","description":"Echo","inputSchema":{"type":"object"}}]}}`, request.ID)
		default:
			state.Requests[record].Status = http.StatusBadRequest
			http.Error(w, "unsupported fixture method", http.StatusBadRequest)
		}
	})
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

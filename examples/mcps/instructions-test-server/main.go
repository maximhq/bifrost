// instructions-test-server serves a small tool set over streamable HTTP and
// returns a configurable `instructions` string from the MCP initialize
// handshake.
//
// It exists because the instructions-forwarding path in Bifrost's MCP gateway
// cannot be exercised by a single fixture. The gateway's job there is to
// AGGREGATE the instructions of every upstream a caller may see - labeling each
// block with its source server, ordering them deterministically, applying the
// size caps, and withholding the ones the caller was not granted. All of that
// needs at least two upstreams advertising DIFFERENT text at the same time,
// which is what the configurable name/instructions pair below is for:
//
//	MCP_SERVER_NAME=alpha MCP_INSTRUCTIONS="Alpha rule." MCP_HTTP_PORT=3021 ./instructions-test-server
//	MCP_SERVER_NAME=beta  MCP_INSTRUCTIONS="Beta rule."  MCP_HTTP_PORT=3022 ./instructions-test-server
//
// Leaving MCP_INSTRUCTIONS empty is itself a case worth running: an upstream
// that advertises nothing must contribute no heading at all to the aggregate,
// rather than an empty section.
//
// POST /set-instructions changes what the next handshake returns, without
// restarting. That is what lets a test cover an upstream editing its
// instructions in place — the case tools/list cannot observe.
//
// Deliberately plain otherwise - no auth, no OAuth, no rejected methods - so
// nothing but the instructions field distinguishes it from a boring server.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const (
	defaultHTTPPort = "3021"
	defaultName     = "instructions-test-server"

	httpEndpointPath    = "/mcp"
	controlEndpointPath = "/set-instructions"

	// The default text is long enough to be recognizable in an aggregate but
	// short enough not to trip the gateway's per-client cap.
	defaultInstructions = "Always call describe_policy before calling echo, and quote its result verbatim."
)

func main() {
	httpPort := envOr("MCP_HTTP_PORT", defaultHTTPPort)
	name := envOr("MCP_SERVER_NAME", defaultName)
	// Not envOr: an explicitly empty MCP_INSTRUCTIONS is the "advertises none"
	// case and must not fall back to the default.
	instructions, hasInstructions := os.LookupEnv("MCP_INSTRUCTIONS")
	if !hasInstructions {
		instructions = defaultInstructions
	}

	live := &liveInstructions{}
	live.set(instructions)

	srv := server.NewStreamableHTTPServer(
		newMCPServer(name, live),
		server.WithEndpointPath(httpEndpointPath),
	)

	// The MCP endpoint and the control endpoint share one listener so a caller
	// needs only one port.
	mux := http.NewServeMux()
	mux.Handle(httpEndpointPath, srv)
	mux.HandleFunc(controlEndpointPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		live.set(string(body))
		log.Printf("instructions changed to %q", string(body))
		w.WriteHeader(http.StatusNoContent)
	})

	addr := "localhost:" + httpPort
	log.Printf("MCP server %q listening on http://%s%s (control: %s)", name, addr, httpEndpointPath, controlEndpointPath)
	if instructions == "" {
		log.Printf("advertising no instructions")
	} else {
		log.Printf("advertising instructions: %q", instructions)
	}
	log.Fatal(http.ListenAndServe(addr, mux))
}

// liveInstructions holds the text the next handshake returns. Guarded because the
// control endpoint and the MCP handler run on different goroutines.
type liveInstructions struct {
	mu   sync.RWMutex
	text string
}

func (l *liveInstructions) set(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.text = s
}

func (l *liveInstructions) get() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.text
}

// newMCPServer builds the tool set. describe_policy exists so a caller can see
// through the tool layer whether the instructions actually reached it: the
// text it returns is the same text the handshake advertises.
func newMCPServer(name string, live *liveInstructions) *server.MCPServer {
	// An AfterInitialize hook rather than WithInstructions: that option is fixed at
	// construction, and this fixture has to be able to change the text at runtime.
	hooks := &server.Hooks{}
	hooks.AddAfterInitialize(func(_ context.Context, _ any, _ *mcp.InitializeRequest, result *mcp.InitializeResult) {
		if result != nil {
			result.Instructions = live.get()
		}
	})

	s := server.NewMCPServer(name, "1.0.0",
		server.WithToolCapabilities(true),
		server.WithHooks(hooks),
	)

	s.AddTool(mcp.NewTool(
		"echo",
		mcp.WithDescription("Echo back the input message"),
		mcp.WithString("message", mcp.Required(), mcp.Description("Message to echo")),
	), echoHandler)

	s.AddTool(mcp.NewTool(
		"describe_policy",
		mcp.WithDescription("Return this server's own usage policy"),
	), policyHandler(name, live))

	return s
}

func echoHandler(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	message, err := request.RequireString("message")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return jsonResult(map[string]any{"echo": message})
}

func policyHandler(name string, live *liveInstructions) server.ToolHandlerFunc {
	return func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return jsonResult(map[string]any{"server": name, "policy": live.get()})
	}
}

// jsonResult returns a JSON object rather than prose, matching the other
// fixtures here: code mode parses a tool's text content as JSON when it can.
func jsonResult(payload map[string]any) (*mcp.CallToolResult, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to encode result: %v", err)), nil
	}
	return mcp.NewToolResultText(string(encoded)), nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

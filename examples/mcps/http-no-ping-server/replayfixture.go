package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const replayScenarioLimit = 256

var replayScenarioID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,96}$`)

type replayScenario struct {
	ID          string `json:"id"`
	Tool        string `json:"tool"`
	Fault       string `json:"fault"`
	WireCalls   int    `json:"wire_calls"`
	Invocations int    `json:"invocations"`
	Commits     int    `json:"commits"`
}

type replayFixture struct {
	next      http.Handler
	mu        sync.Mutex
	scenarios map[string]*replayScenario
}

// withReplaySafetyFixture is opt-in and runs once at example-server startup.
// Its bounded ledger models disposable local records, never external mutations.
func withReplaySafetyFixture(s *server.MCPServer, next http.Handler) http.Handler {
	if os.Getenv("MCP_REPLAY_SAFETY_FIXTURE") != "1" {
		return next
	}
	f := &replayFixture{next: next, scenarios: make(map[string]*replayScenario)}
	tools := map[string]mcp.ToolAnnotation{
		"replay_append":      {},
		"replay_additive":    {DestructiveHint: mcp.ToBoolPtr(false), IdempotentHint: mcp.ToBoolPtr(false)},
		"replay_destructive": {DestructiveHint: mcp.ToBoolPtr(true), IdempotentHint: mcp.ToBoolPtr(false)},
		"replay_read":        {ReadOnlyHint: mcp.ToBoolPtr(true)},
		"replay_upsert":      {IdempotentHint: mcp.ToBoolPtr(true)},
	}
	for name, annotations := range tools {
		s.AddTool(mcp.NewTool(name, mcp.WithToolAnnotation(annotations), mcp.WithDescription("Local replay-safety fixture; uses disposable records only"), mcp.WithString("scenario", mcp.Required())), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id, _ := req.GetArguments()["scenario"].(string)
			f.mu.Lock()
			state := f.scenarios[id]
			if state == nil || state.Tool != req.Params.Name {
				f.mu.Unlock()
				return mcp.NewToolResultError("unknown or mismatched replay scenario"), nil
			}
			state.Invocations++
			if state.Tool != "replay_read" && (state.Tool != "replay_upsert" || state.Commits == 0) {
				state.Commits++
			}
			snapshot := *state
			f.mu.Unlock()
			body, err := json.Marshal(snapshot)
			if err != nil {
				return nil, err
			}
			return mcp.NewToolResultText(string(body)), nil
		})
	}
	return f
}

func (f *replayFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/_replay/scenarios") {
		f.manage(w, r)
		return
	}
	if r.Method != http.MethodPost {
		f.next.ServeHTTP(w, r)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		http.Error(w, "fixture request exceeds limit", http.StatusRequestEntityTooLarge)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	var request struct {
		Method string `json:"method"`
		Params struct {
			Name      string `json:"name"`
			Arguments struct {
				Scenario string `json:"scenario"`
			} `json:"arguments"`
		} `json:"params"`
	}
	if json.Unmarshal(body, &request) != nil || request.Method != "tools/call" {
		f.next.ServeHTTP(w, r)
		return
	}
	f.mu.Lock()
	state := f.scenarios[request.Params.Arguments.Scenario]
	if state == nil || state.Tool != request.Params.Name {
		f.mu.Unlock()
		f.next.ServeHTTP(w, r)
		return
	}
	state.WireCalls++
	attempt, fault := state.WireCalls, state.Fault
	f.mu.Unlock()
	response := httptest.NewRecorder()
	f.next.ServeHTTP(response, r) // Commit in the actual MCP handler before failing the response.
	if attempt == 1 {
		switch fault {
		case "503":
			http.Error(w, "response lost after fixture commit", http.StatusServiceUnavailable)
			return
		case "401":
			http.Error(w, "unauthorized after fixture commit", http.StatusUnauthorized)
			return
		case "reset":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Length", "9999")
			_, _ = io.WriteString(w, "{")
			w.(http.Flusher).Flush()
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			if tcp, ok := conn.(*net.TCPConn); ok {
				_ = tcp.SetLinger(0)
			}
			_ = conn.Close()
			return
		}
	}
	for key, values := range response.Header() {
		w.Header()[key] = values
	}
	w.WriteHeader(response.Code)
	_, _ = w.Write(response.Body.Bytes())
}

func (f *replayFixture) manage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodPost && r.URL.Path == "/_replay/scenarios" {
		var input struct {
			ID    string `json:"id"`
			Tool  string `json:"tool"`
			Fault string `json:"fault"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || !replayScenarioID.MatchString(input.ID) {
			http.Error(w, "invalid replay scenario", http.StatusBadRequest)
			return
		}
		switch input.Tool {
		case "replay_append", "replay_additive", "replay_destructive", "replay_read", "replay_upsert":
		default:
			http.Error(w, "unknown fixture tool", http.StatusBadRequest)
			return
		}
		switch input.Fault {
		case "503", "401", "reset":
		default:
			http.Error(w, "unknown fixture fault", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if _, exists := f.scenarios[input.ID]; exists {
			http.Error(w, "scenario exists", http.StatusConflict)
			return
		}
		if len(f.scenarios) >= replayScenarioLimit {
			http.Error(w, "fixture scenario limit reached", http.StatusTooManyRequests)
			return
		}
		state := &replayScenario{ID: input.ID, Tool: input.Tool, Fault: input.Fault}
		f.scenarios[input.ID] = state
		_ = json.NewEncoder(w).Encode(state)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/_replay/scenarios/")
	if !replayScenarioID.MatchString(id) {
		http.Error(w, "invalid replay scenario path", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	state := f.scenarios[id]
	if state == nil {
		http.Error(w, "scenario not found", http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		_ = json.NewEncoder(w).Encode(state)
	case http.MethodDelete:
		delete(f.scenarios, id)
		_, _ = fmt.Fprintln(w, `{"deleted":true}`)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

//go:build !tinygo && !wasm

package starlark

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	codemcp "github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/schemas"
)

// newVFSPathCodeMode connects "codey", plus "acme" and "acme.v2": client names may contain dots,
// and "acme" has a tool named "v2", so "acme.v2" reads as either a server or a server.tool pair.
func newVFSPathCodeMode(level schemas.CodeModeBindingLevel) *StarlarkCodeMode {
	mode := NewStarlarkCodeMode(&codemcp.CodeModeConfig{
		BindingLevel:         level,
		ToolExecutionTimeout: time.Second,
	}, nil)
	client := func(name string) *schemas.MCPClientState {
		return &schemas.MCPClientState{Name: name, ExecutionConfig: &schemas.MCPClientConfig{ID: name + "-id", Name: name, IsCodeModeClient: true}}
	}
	tool := func(name string) schemas.ChatTool {
		return schemas.ChatTool{Function: &schemas.ChatToolFunction{Name: name, Description: schemas.Ptr("Does " + name)}}
	}
	mode.clientManager = &testClientManager{
		clients: map[string]*schemas.MCPClientState{
			"codey":   client("codey"),
			"acme":    client("acme"),
			"acme.v2": client("acme.v2"),
		},
		tools: map[string][]schemas.ChatTool{
			"codey":   {tool("codey-add"), tool("codey-multiply")},
			"acme":    {tool("v2"), tool("status")},
			"acme.v2": {tool("ping")},
		},
	}
	return mode
}

func vfsToolResponse(t *testing.T, msg *schemas.ChatMessage, err error) (string, bool) {
	t.Helper()
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if msg == nil || msg.Content == nil || msg.Content.ContentStr == nil {
		t.Fatal("expected tool response content")
	}
	isError := msg.ChatToolMessage != nil && msg.ChatToolMessage.IsError != nil && *msg.ChatToolMessage.IsError
	return *msg.Content.ContentStr, isError
}

func toolCallArgs(t *testing.T, args map[string]string) schemas.ChatAssistantMessageToolCall {
	t.Helper()
	data, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return schemas.ChatAssistantMessageToolCall{
		ID:       schemas.Ptr("tool-call-1"),
		Function: schemas.ChatAssistantMessageToolCallFunction{Arguments: string(data)},
	}
}

func TestReadToolFileAcceptsCommonPathSpellings(t *testing.T) {
	const (
		codeyServer = "# codey server tools"
		codeyAdd    = "# codey.add tool"
		acmeServer  = "# acme server tools"
		acmeV2Tool  = "# acme.v2 tool"
		v2Server    = "# acme.v2 server tools"
		v2Ping      = "# acme.v2.ping tool"
	)
	cases := []struct {
		fileName string
		header   string
	}{
		{"servers/codey.pyi", codeyServer},
		{"servers/codey/add.pyi", codeyAdd},
		{"codey/add.pyi", codeyAdd},
		{"/servers/codey/add", codeyAdd},
		{"./servers/codey/add.pyi", codeyAdd},
		{"Servers/codey/add.py", codeyAdd},
		{"SERVERS/codey.pyi", codeyServer},
		{"codey.add", codeyAdd},
		{"codey.add()", codeyAdd},
		{"servers/codey.add.pyi", codeyAdd},
		{" ./servers/codey/ ", codeyServer},
		{"codey.py", codeyServer},
		// "acme.v2" is a connected server, so it is never split into acme + v2.
		{"acme.v2", v2Server},
		{"servers/acme.v2.pyi", v2Server},
		{"servers/acme.v2/ping.pyi", v2Ping},
		{"acme.v2.ping", v2Ping},
		{"acme.v2.ping()", v2Ping},
		{"acme.status", "# acme.status tool"},
		{"servers/acme/v2.pyi", acmeV2Tool},
		{"servers/acme.pyi", acmeServer},
	}
	for _, level := range []schemas.CodeModeBindingLevel{schemas.CodeModeBindingLevelServer, schemas.CodeModeBindingLevelTool} {
		mode := newVFSPathCodeMode(level)
		for _, tc := range cases {
			t.Run(string(level)+"/"+tc.fileName, func(t *testing.T) {
				msg, err := mode.handleReadToolFile(context.Background(), toolCallArgs(t, map[string]string{"fileName": tc.fileName}))
				content, isError := vfsToolResponse(t, msg, err)
				if isError || !strings.Contains(content, tc.header+"\n") {
					t.Fatalf("readToolFile(%q): want a stub headed %q, got (isError=%v):\n%s", tc.fileName, tc.header, isError, content)
				}
			})
		}
	}
}

func TestReadToolFileStillRejectsTraversalAndUnknownServers(t *testing.T) {
	mode := newVFSPathCodeMode(schemas.CodeModeBindingLevelTool)
	cases := []string{
		"servers/../codey.pyi",
		"servers/codey/../acme.pyi",
		"../../../etc/passwd.pyi",
		"/etc/passwd.pyi",
		"servers/nope.pyi",
		"nope.add",
	}
	for _, fileName := range cases {
		t.Run(fileName, func(t *testing.T) {
			msg, err := mode.handleReadToolFile(context.Background(), toolCallArgs(t, map[string]string{"fileName": fileName}))
			content, isError := vfsToolResponse(t, msg, err)
			if !isError || !strings.Contains(content, "No server found matching") {
				t.Fatalf("readToolFile(%q): want a server-not-found error, got (isError=%v):\n%s", fileName, isError, content)
			}
		})
	}
}

// A mistyped server must not dump every tool file of every server into the caller's context.
func TestReadToolFileServerMissListsServerNamesOnly(t *testing.T) {
	mode := newVFSPathCodeMode(schemas.CodeModeBindingLevelTool)
	msg, err := mode.handleReadToolFile(context.Background(), toolCallArgs(t, map[string]string{"fileName": "servers/nope.pyi"}))
	content, isError := vfsToolResponse(t, msg, err)
	if !isError {
		t.Fatalf("want an error, got:\n%s", content)
	}
	if !strings.Contains(content, "Available servers: acme, acme.v2, codey\n") {
		t.Fatalf("want the sorted server names, got:\n%s", content)
	}
	if strings.Contains(content, "servers/codey/add.pyi") {
		t.Fatalf("miss listed per-tool files, got:\n%s", content)
	}
	if lines := strings.Count(content, "\n"); lines > 2 {
		t.Fatalf("want a compact miss message, got %d lines:\n%s", lines, content)
	}
}

func TestGetToolDocsAcceptsCommonPathSpellings(t *testing.T) {
	cases := []struct {
		server, tool string
		header       string
	}{
		{"codey", "add", "codey.add"},
		{"servers/codey", "add.pyi", "codey.add"},
		{"codey.pyi", "servers/codey/add.pyi", "codey.add"},
		{"Servers/codey/", "codey.add", "codey.add"},
		{"codey", "CODEY.add()", "codey.add"},
		{"servers/codey/add.pyi", "add", "codey.add"},
		{"acme.v2", "ping", "acme.v2.ping"},
		{"servers/acme.v2.pyi", "acme.v2.ping", "acme.v2.ping"},
		{"acme", "v2", "acme.v2"},
		{"acme", "acme.v2", "acme.v2"},
	}
	mode := newVFSPathCodeMode(schemas.CodeModeBindingLevelServer)
	for _, tc := range cases {
		t.Run(tc.server+"|"+tc.tool, func(t *testing.T) {
			msg, err := mode.handleGetToolDocs(context.Background(), toolCallArgs(t, map[string]string{"server": tc.server, "tool": tc.tool}))
			content, isError := vfsToolResponse(t, msg, err)
			want := "Documentation for " + tc.header + " tool"
			if isError || !strings.Contains(content, want) {
				t.Fatalf("getToolDocs(%q, %q): want %q, got (isError=%v):\n%s", tc.server, tc.tool, want, isError, content)
			}
		})
	}
}

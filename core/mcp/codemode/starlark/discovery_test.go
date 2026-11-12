//go:build !tinygo && !wasm

package starlark

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	codemcp "github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/schemas"
)

func discoveryTestTool(name, description string, params ...string) schemas.ChatTool {
	properties := schemas.NewOrderedMap()
	for _, p := range params {
		properties.Set(p, map[string]interface{}{"type": "string"})
	}
	return schemas.ChatTool{Function: &schemas.ChatToolFunction{
		Name:        name,
		Description: schemas.Ptr(description),
		Parameters:  &schemas.ToolFunctionParameters{Type: "object", Properties: properties},
	}}
}

// newDiscoveryCodeMode builds a code mode with three code mode servers and one
// server that is connected but not in code mode.
func newDiscoveryCodeMode(level schemas.CodeModeBindingLevel) *StarlarkCodeMode {
	mode := NewStarlarkCodeMode(&codemcp.CodeModeConfig{BindingLevel: level, ToolExecutionTimeout: time.Second}, nil)
	client := func(name string, codeMode bool) *schemas.MCPClientState {
		return &schemas.MCPClientState{Name: name, ExecutionConfig: &schemas.MCPClientConfig{ID: name + "-id", Name: name, IsCodeModeClient: codeMode}}
	}
	mode.clientManager = &testClientManager{
		clients: map[string]*schemas.MCPClientState{
			"github":  client("github", true),
			"slack":   client("slack", true),
			"jira":    client("jira", true),
			"billing": client("billing", false),
		},
		tools: map[string][]schemas.ChatTool{
			"github": {
				discoveryTestTool("search_repos", "Search repositories by keyword", "keyword"),
				discoveryTestTool("create_issue", "Open a new issue in a repository", "repo", "title", "body"),
				discoveryTestTool("list_pull_requests", "List pull requests of a repository", "repo"),
			},
			"slack": {
				discoveryTestTool("send_message", "Post a message to a channel", "channel", "text"),
				discoveryTestTool("search_messages", "Search messages across channels", "query"),
			},
			"jira": {
				discoveryTestTool("create_issue", "Create a ticket in a project", "project", "summary"),
			},
			"billing": {
				discoveryTestTool("create_invoice", "Create an invoice for an issue", "customer"),
			},
		},
		instructions: map[string]string{"slack-id": "Prefer search_messages before send_message."},
	}
	return mode
}

func listToolFiles(t *testing.T, mode *StarlarkCodeMode, args string) string {
	t.Helper()
	msg, err := mode.handleListToolFiles(context.Background(), schemas.ChatAssistantMessageToolCall{
		ID:       schemas.Ptr("call-1"),
		Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr(codemcp.ToolTypeListToolFiles), Arguments: args},
	})
	if err != nil {
		t.Fatalf("listToolFiles(%s) returned error: %v", args, err)
	}
	if msg == nil || msg.Content == nil || msg.Content.ContentStr == nil {
		t.Fatalf("listToolFiles(%s) returned no content", args)
	}
	return *msg.Content.ContentStr
}

// shortlistPaths returns the file lines of a shortlist, in order, without annotations.
func shortlistPaths(text string) []string {
	var paths []string
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "servers/") {
			paths = append(paths, strings.SplitN(line, "  #", 2)[0])
		}
	}
	return paths
}

func TestListToolFilesWithoutQueryKeepsCompleteListing(t *testing.T) {
	for _, level := range []schemas.CodeModeBindingLevel{schemas.CodeModeBindingLevelServer, schemas.CodeModeBindingLevelTool} {
		mode := newDiscoveryCodeMode(level)
		complete, err := mode.handleCompleteToolFiles(context.Background(), schemas.ChatAssistantMessageToolCall{ID: schemas.Ptr("call-1")})
		if err != nil {
			t.Fatal(err)
		}
		want := *complete.Content.ContentStr
		for _, args := range []string{"", "{}", "null", `{"query":""}`, `{"query":"   "}`, `{"limit":3}`, `{"query":null}`} {
			if got := listToolFiles(t, mode, args); got != want {
				t.Fatalf("%s binding, args %q: listing changed\nwant:\n%s\ngot:\n%s", level, args, want, got)
			}
		}
		if strings.Contains(want, "billing") {
			t.Fatalf("complete listing shows a non code mode server:\n%s", want)
		}
	}
}

func TestListToolFilesToolDeclaresQueryAndLimit(t *testing.T) {
	tool := newDiscoveryCodeMode(schemas.CodeModeBindingLevelTool).createListToolFilesTool()
	params := tool.Function.Parameters
	if len(params.Required) != 0 {
		t.Fatalf("listToolFiles must not require arguments, got %v", params.Required)
	}
	if keys := params.Properties.Keys(); len(keys) != 2 || keys[0] != "query" || keys[1] != "limit" {
		t.Fatalf("expected query and limit properties, got %v", keys)
	}
	limit, _ := params.Properties.Get("limit")
	spec := limit.(map[string]interface{})
	if spec["minimum"] != 1 || spec["maximum"] != discoveryMaxLimit {
		t.Fatalf("unexpected limit bounds %v", spec)
	}
}

func TestListToolFilesLexicalRanking(t *testing.T) {
	mode := newDiscoveryCodeMode(schemas.CodeModeBindingLevelTool)
	out := listToolFiles(t, mode, `{"query":"create an issue"}`)
	paths := shortlistPaths(out)
	want := []string{"servers/github/create_issue.pyi", "servers/jira/create_issue.pyi"}
	if len(paths) < 2 || paths[0] != want[0] || paths[1] != want[1] {
		t.Fatalf("expected %v first, got %v\n%s", want, paths, out)
	}
	if !strings.Contains(out, "lexical match") || !strings.Contains(out, "without query for the complete listing") {
		t.Fatalf("shortlist header missing:\n%s", out)
	}
	if strings.Contains(out, "billing") {
		t.Fatalf("shortlist shows a tool the caller cannot list:\n%s", out)
	}

	// Description and parameter words count, below name words.
	out = listToolFiles(t, mode, `{"query":"post to a channel"}`)
	if paths := shortlistPaths(out); len(paths) == 0 || paths[0] != "servers/slack/send_message.pyi" {
		t.Fatalf("expected send_message first, got %v\n%s", paths, out)
	}
	if !strings.Contains(out, "Servers with their own usage instructions: slack") {
		t.Fatalf("expected instructions pointer for slack:\n%s", out)
	}

	// Naming a tool outright returns just that tool.
	out = listToolFiles(t, mode, `{"query":"jira.create_issue"}`)
	if paths := shortlistPaths(out); len(paths) != 1 || paths[0] != "servers/jira/create_issue.pyi" {
		t.Fatalf("expected exact match, got %v\n%s", paths, out)
	}

	out = listToolFiles(t, mode, `{"query":"zzz qqq"}`)
	if len(shortlistPaths(out)) != 0 || !strings.Contains(out, "No tool names or descriptions matched") {
		t.Fatalf("expected no match message:\n%s", out)
	}
}

func TestListToolFilesServerBindingGroupsTools(t *testing.T) {
	mode := newDiscoveryCodeMode(schemas.CodeModeBindingLevelServer)
	out := listToolFiles(t, mode, `{"query":"search"}`)
	paths := shortlistPaths(out)
	if len(paths) != 2 {
		t.Fatalf("expected one file per server, got %v\n%s", paths, out)
	}
	if !strings.Contains(out, "servers/github.pyi  # tools: search_repos") || !strings.Contains(out, "servers/slack.pyi  # tools: search_messages") {
		t.Fatalf("expected server files annotated with matching tools:\n%s", out)
	}
}

func TestListToolFilesLimitBounds(t *testing.T) {
	mode := newDiscoveryCodeMode(schemas.CodeModeBindingLevelTool)
	call := func(args string) error {
		_, err := mode.handleListToolFiles(context.Background(), schemas.ChatAssistantMessageToolCall{
			ID:       schemas.Ptr("call-1"),
			Function: schemas.ChatAssistantMessageToolCallFunction{Arguments: args},
		})
		return err
	}
	for _, args := range []string{
		`{"query":"issue","limit":0}`,
		`{"query":"issue","limit":21}`,
		`{"query":"issue","limit":2.5}`,
		`{"query":"issue","limit":"3"}`,
		`{"query":"issue","limit":-1}`,
		`{"query":7}`,
		`{"query":"` + strings.Repeat("a", discoveryMaxQueryRunes+1) + `"}`,
		`{not json`,
	} {
		if err := call(args); err == nil {
			t.Fatalf("expected %s to be rejected", args)
		}
	}
	if paths := shortlistPaths(listToolFiles(t, mode, `{"query":"issue repository","limit":1}`)); len(paths) != 1 {
		t.Fatalf("limit 1 returned %v", paths)
	}
	if paths := shortlistPaths(listToolFiles(t, mode, `{"query":"issue repository","limit":20}`)); len(paths) < 2 {
		t.Fatalf("limit 20 returned %v", paths)
	}

	// Default limit is 8, over a fleet bigger than that.
	tools := make([]schemas.ChatTool, 0, 30)
	for i := range 30 {
		tools = append(tools, discoveryTestTool(fmt.Sprintf("report_%02d", i), "Build a report"))
	}
	mode.clientManager.(*testClientManager).tools["reports"] = tools
	mode.clientManager.(*testClientManager).clients["reports"] = &schemas.MCPClientState{Name: "reports", ExecutionConfig: &schemas.MCPClientConfig{ID: "reports-id", Name: "reports", IsCodeModeClient: true}}
	if paths := shortlistPaths(listToolFiles(t, mode, `{"query":"report"}`)); len(paths) != discoveryDefaultLimit {
		t.Fatalf("default limit returned %d files: %v", len(paths), paths)
	}
}

// rankerServer serves the ranking contract with answer, recording each request.
type rankerServer struct {
	*httptest.Server
	calls    atomic.Int32
	requests chan discoveryRankRequest
}

func newRankerServer(t *testing.T, answer func(w http.ResponseWriter, r *http.Request, req discoveryRankRequest)) *rankerServer {
	t.Helper()
	rs := &rankerServer{requests: make(chan discoveryRankRequest, 16)}
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.calls.Add(1)
		var req discoveryRankRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		rs.requests <- req
		answer(w, r, req)
	}))
	t.Cleanup(rs.Close)
	return rs
}

func writeRankResponse(w http.ResponseWriter, resp interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func useRanker(t *testing.T, mode *StarlarkCodeMode, ranker schemas.MCPCodeModeDiscoveryRanker) {
	t.Helper()
	mode.UpdateConfig(&codemcp.CodeModeConfig{DiscoveryRanker: &ranker})
	if mode.discoveryRanker.Load() == nil {
		t.Fatalf("ranker %+v was not installed", ranker)
	}
}

func TestListToolFilesExternalRankerSuccess(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("secret-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var authorization atomic.Value
	rs := newRankerServer(t, func(w http.ResponseWriter, r *http.Request, req discoveryRankRequest) {
		authorization.Store(r.Header.Get("Authorization"))
		scores := []map[string]interface{}{}
		for _, c := range req.Candidates {
			relevance := 0.0
			switch c.ID {
			case "servers/slack/send_message.pyi":
				relevance = 0.9
			case "servers/slack/search_messages.pyi":
				relevance = 0.4
			}
			scores = append(scores, map[string]interface{}{"id": c.ID, "relevance": relevance})
		}
		writeRankResponse(w, map[string]interface{}{"version": 1, "revision": req.Revision, "candidates": scores, "sufficient": 0.95})
	})
	mode := newDiscoveryCodeMode(schemas.CodeModeBindingLevelTool)
	useRanker(t, mode, schemas.MCPCodeModeDiscoveryRanker{URL: rs.URL, TokenFile: tokenFile})

	out := listToolFiles(t, mode, `{"query":"tell the team the deploy finished"}`)
	paths := shortlistPaths(out)
	want := []string{"servers/slack/send_message.pyi", "servers/slack/search_messages.pyi"}
	if len(paths) != 2 || paths[0] != want[0] || paths[1] != want[1] {
		t.Fatalf("expected ranker order %v, got %v\n%s", want, paths, out)
	}
	if !strings.Contains(out, "(discovery ranker)") || !strings.Contains(out, "relevance=0.90") || strings.Contains(out, "may not cover") {
		t.Fatalf("unexpected ranked output:\n%s", out)
	}
	if got := authorization.Load(); got != "Bearer secret-token" {
		t.Fatalf("expected bearer token from file, got %v", got)
	}

	req := <-rs.requests
	if req.Version != 1 || req.Query != "tell the team the deploy finished" || req.Revision == "" {
		t.Fatalf("unexpected request %+v", req)
	}
	if len(req.Candidates) != 6 {
		t.Fatalf("expected the 6 visible tools as candidates, got %d", len(req.Candidates))
	}
	for _, c := range req.Candidates {
		if c.Server == "billing" {
			t.Fatalf("ranker was sent a tool the caller cannot list: %+v", c)
		}
		if c.ID == "servers/github/create_issue.pyi" {
			if c.Name != "create_issue" || c.Description != "Open a new issue in a repository" || strings.Join(c.Parameters, ",") != "repo,title,body" {
				t.Fatalf("unexpected candidate %+v", c)
			}
		}
	}

	// Same catalogue, same revision; a changed tool changes it.
	listToolFiles(t, mode, `{"query":"tell the team"}`)
	again := <-rs.requests
	if again.Revision != req.Revision {
		t.Fatalf("revision changed without a catalogue change")
	}
	mode.clientManager.(*testClientManager).tools["jira"][0] = discoveryTestTool("create_issue", "Create a ticket", "project")
	listToolFiles(t, mode, `{"query":"tell the team"}`)
	if changed := <-rs.requests; changed.Revision == req.Revision {
		t.Fatalf("revision did not change with the catalogue")
	}

	// A query that names a tool needs no ranking call.
	before := rs.calls.Load()
	listToolFiles(t, mode, `{"query":"servers/github/search_repos.pyi"}`)
	if rs.calls.Load() != before {
		t.Fatalf("exact reference called the ranker")
	}
	// Nor does the complete listing.
	listToolFiles(t, mode, `{}`)
	if rs.calls.Load() != before {
		t.Fatalf("complete listing called the ranker")
	}
}

func TestListToolFilesExternalRankerInsufficientHint(t *testing.T) {
	rs := newRankerServer(t, func(w http.ResponseWriter, _ *http.Request, req discoveryRankRequest) {
		writeRankResponse(w, map[string]interface{}{
			"version": 1, "revision": req.Revision, "sufficient": 0.2,
			"candidates": []map[string]interface{}{{"id": "servers/github/search_repos.pyi", "relevance": 0.6}},
		})
	})
	mode := newDiscoveryCodeMode(schemas.CodeModeBindingLevelTool)
	useRanker(t, mode, schemas.MCPCodeModeDiscoveryRanker{URL: rs.URL})
	out := listToolFiles(t, mode, `{"query":"find code"}`)
	if paths := shortlistPaths(out); len(paths) != 1 || paths[0] != "servers/github/search_repos.pyi" {
		t.Fatalf("unexpected shortlist %v\n%s", paths, out)
	}
	if !strings.Contains(out, "may not cover the request") {
		t.Fatalf("expected a hint to widen the search:\n%s", out)
	}
}

func assertLexicalFallback(t *testing.T, out string) {
	t.Helper()
	if !strings.Contains(out, "(lexical match)") || !strings.Contains(out, "discovery ranker was not used") {
		t.Fatalf("expected lexical fallback:\n%s", out)
	}
	if paths := shortlistPaths(out); len(paths) == 0 || paths[0] != "servers/github/create_issue.pyi" {
		t.Fatalf("expected the lexical shortlist, got %v\n%s", paths, out)
	}
}

func TestListToolFilesExternalRankerTimeoutFallsBack(t *testing.T) {
	release := make(chan struct{})
	rs := newRankerServer(t, func(w http.ResponseWriter, r *http.Request, req discoveryRankRequest) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	defer close(release)
	mode := newDiscoveryCodeMode(schemas.CodeModeBindingLevelTool)
	useRanker(t, mode, schemas.MCPCodeModeDiscoveryRanker{URL: rs.URL})
	mode.discoveryRanker.Load().timeout = 50 * time.Millisecond

	start := time.Now()
	out := listToolFiles(t, mode, `{"query":"create an issue"}`)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("ranking was not bounded by its timeout: %v", elapsed)
	}
	assertLexicalFallback(t, out)
}

func TestListToolFilesExternalRankerErrorsFallBack(t *testing.T) {
	cases := map[string]func(w http.ResponseWriter, r *http.Request, req discoveryRankRequest){
		"http 500": func(w http.ResponseWriter, _ *http.Request, _ discoveryRankRequest) {
			http.Error(w, "boom", http.StatusInternalServerError)
		},
		"not json": func(w http.ResponseWriter, _ *http.Request, _ discoveryRankRequest) {
			_, _ = w.Write([]byte("<html>"))
		},
		"redirect": func(w http.ResponseWriter, r *http.Request, _ discoveryRankRequest) {
			http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
		},
		"unknown id": func(w http.ResponseWriter, _ *http.Request, req discoveryRankRequest) {
			writeRankResponse(w, map[string]interface{}{"version": 1, "revision": req.Revision, "candidates": []map[string]interface{}{
				{"id": "servers/billing/create_invoice.pyi", "relevance": 0.9},
			}})
		},
		"relevance above 1": func(w http.ResponseWriter, _ *http.Request, req discoveryRankRequest) {
			writeRankResponse(w, map[string]interface{}{"version": 1, "revision": req.Revision, "candidates": []map[string]interface{}{
				{"id": req.Candidates[0].ID, "relevance": 1.5},
			}})
		},
		"negative relevance": func(w http.ResponseWriter, _ *http.Request, req discoveryRankRequest) {
			writeRankResponse(w, map[string]interface{}{"version": 1, "revision": req.Revision, "candidates": []map[string]interface{}{
				{"id": req.Candidates[0].ID, "relevance": -0.1},
			}})
		},
		"missing relevance": func(w http.ResponseWriter, _ *http.Request, req discoveryRankRequest) {
			writeRankResponse(w, map[string]interface{}{"version": 1, "revision": req.Revision, "candidates": []map[string]interface{}{
				{"id": req.Candidates[0].ID},
			}})
		},
		"stale revision": func(w http.ResponseWriter, _ *http.Request, req discoveryRankRequest) {
			writeRankResponse(w, map[string]interface{}{"version": 1, "revision": "other", "candidates": []map[string]interface{}{
				{"id": req.Candidates[0].ID, "relevance": 0.9},
			}})
		},
		"nothing relevant": func(w http.ResponseWriter, _ *http.Request, req discoveryRankRequest) {
			writeRankResponse(w, map[string]interface{}{"version": 1, "revision": req.Revision, "candidates": []map[string]interface{}{}})
		},
	}
	for name, answer := range cases {
		t.Run(name, func(t *testing.T) {
			rs := newRankerServer(t, answer)
			mode := newDiscoveryCodeMode(schemas.CodeModeBindingLevelTool)
			useRanker(t, mode, schemas.MCPCodeModeDiscoveryRanker{URL: rs.URL})
			assertLexicalFallback(t, listToolFiles(t, mode, `{"query":"create an issue"}`))
		})
	}

	t.Run("unreadable token file", func(t *testing.T) {
		rs := newRankerServer(t, func(w http.ResponseWriter, _ *http.Request, _ discoveryRankRequest) {
			t.Error("ranker must not be called without its token")
		})
		mode := newDiscoveryCodeMode(schemas.CodeModeBindingLevelTool)
		useRanker(t, mode, schemas.MCPCodeModeDiscoveryRanker{URL: rs.URL, TokenFile: filepath.Join(t.TempDir(), "missing")})
		assertLexicalFallback(t, listToolFiles(t, mode, `{"query":"create an issue"}`))
	})
}

func TestValidateDiscoveryRankResponse(t *testing.T) {
	req := discoveryRankRequest{Version: 1, Revision: "r1", Candidates: []discoveryCandidate{{ID: "a"}, {ID: "b"}}}
	f := func(v float64) *float64 { return &v }
	ok := discoveryRankResponse{Version: 1, Revision: "r1", Sufficient: f(1), Candidates: []discoveryRankScore{{ID: "a", Relevance: f(0)}, {ID: "b", Relevance: f(1)}}}
	scores, err := validateDiscoveryRankResponse(req, ok)
	if err != nil || scores["a"] != 0 || scores["b"] != 1 {
		t.Fatalf("valid response rejected: %v %v", scores, err)
	}
	partial := discoveryRankResponse{Version: 1, Revision: "r1", Candidates: []discoveryRankScore{{ID: "b", Relevance: f(0.5)}}}
	if _, err := validateDiscoveryRankResponse(req, partial); err != nil {
		t.Fatalf("a response may leave candidates out: %v", err)
	}
	for name, resp := range map[string]discoveryRankResponse{
		"version":           {Version: 2, Revision: "r1"},
		"revision":          {Version: 1, Revision: "r0"},
		"unknown id":        {Version: 1, Revision: "r1", Candidates: []discoveryRankScore{{ID: "c", Relevance: f(0.5)}}},
		"duplicate id":      {Version: 1, Revision: "r1", Candidates: []discoveryRankScore{{ID: "a", Relevance: f(0.5)}, {ID: "a", Relevance: f(0.4)}}},
		"missing relevance": {Version: 1, Revision: "r1", Candidates: []discoveryRankScore{{ID: "a"}}},
		"relevance > 1":     {Version: 1, Revision: "r1", Candidates: []discoveryRankScore{{ID: "a", Relevance: f(1.01)}}},
		"relevance < 0":     {Version: 1, Revision: "r1", Candidates: []discoveryRankScore{{ID: "a", Relevance: f(-0.01)}}},
		"sufficient > 1":    {Version: 1, Revision: "r1", Sufficient: f(2)},
	} {
		if _, err := validateDiscoveryRankResponse(req, resp); err == nil {
			t.Fatalf("%s: invalid response accepted", name)
		}
	}
}

func TestRankCandidatesKeepsEveryServer(t *testing.T) {
	items := make([]discoveryTool, 0, 60)
	for i := range 50 {
		items = append(items, discoveryTool{candidate: discoveryCandidate{ID: fmt.Sprintf("servers/big/t%02d.pyi", i), Server: "big"}, lexical: float64(100 - i)})
	}
	for i := range 10 {
		server := fmt.Sprintf("small%d", i)
		items = append(items, discoveryTool{candidate: discoveryCandidate{ID: "servers/" + server + "/t.pyi", Server: server}})
	}
	selected := rankCandidates(items)
	if len(selected) != discoveryMaxRankCandidates {
		t.Fatalf("expected %d candidates, got %d", discoveryMaxRankCandidates, len(selected))
	}
	servers := map[string]bool{}
	ids := map[string]bool{}
	for _, item := range selected {
		servers[item.candidate.Server] = true
		if ids[item.candidate.ID] {
			t.Fatalf("duplicate candidate %s", item.candidate.ID)
		}
		ids[item.candidate.ID] = true
	}
	if len(servers) != 9 {
		t.Fatalf("expected the big server plus 8 small servers in the remaining slots, got %d servers", len(servers))
	}
	if selected[0].candidate.ID != "servers/big/t00.pyi" {
		t.Fatalf("best lexical match must come first, got %s", selected[0].candidate.ID)
	}
}

func TestDiscoveryRankerConfigUpdates(t *testing.T) {
	mode := newDiscoveryCodeMode(schemas.CodeModeBindingLevelTool)
	if mode.discoveryRanker.Load() != nil {
		t.Fatal("no ranker is configured by default")
	}
	useRanker(t, mode, schemas.MCPCodeModeDiscoveryRanker{URL: "http://127.0.0.1:1/rank", TimeoutSeconds: 3})
	if got := mode.discoveryRanker.Load().timeout; got != 3*time.Second {
		t.Fatalf("expected 3s timeout, got %v", got)
	}
	// An invalid update keeps the current ranker; nil leaves it; an empty URL removes it.
	mode.UpdateConfig(&codemcp.CodeModeConfig{DiscoveryRanker: &schemas.MCPCodeModeDiscoveryRanker{URL: "ftp://x"}})
	mode.UpdateConfig(&codemcp.CodeModeConfig{BindingLevel: schemas.CodeModeBindingLevelTool})
	if got := mode.discoveryRanker.Load(); got == nil || got.endpoint != "http://127.0.0.1:1/rank" {
		t.Fatalf("ranker changed unexpectedly: %+v", got)
	}
	mode.UpdateConfig(&codemcp.CodeModeConfig{DiscoveryRanker: &schemas.MCPCodeModeDiscoveryRanker{}})
	if mode.discoveryRanker.Load() != nil {
		t.Fatal("an empty URL must remove the ranker")
	}
}

package heimdall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
)

// ---- message and row builders ----

func user(text string) schemas.ChatMessage {
	return schemas.ChatMessage{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &text}}
}

func assistantText(text string) schemas.ChatMessage {
	return schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: &text}, ChatAssistantMessage: &schemas.ChatAssistantMessage{}}
}

func call(id, name, args string) schemas.ChatAssistantMessageToolCall {
	return schemas.ChatAssistantMessageToolCall{
		ID:       &id,
		Type:     schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{Name: &name, Arguments: args},
	}
}

func assistantCalls(calls ...schemas.ChatAssistantMessageToolCall) schemas.ChatMessage {
	return schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, ChatAssistantMessage: &schemas.ChatAssistantMessage{ToolCalls: calls}}
}

func toolResult(id, text string) schemas.ChatMessage {
	return schemas.ChatMessage{Role: schemas.ChatMessageRoleTool, Content: &schemas.ChatMessageContent{ContentStr: &text}, ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: &id}}
}

func functionTools(names ...string) []schemas.ChatTool {
	var tools []schemas.ChatTool
	for _, n := range names {
		tools = append(tools, schemas.ChatTool{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: n}})
	}
	return tools
}

var t0 = time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)

// row is one logged model call.
func row(id, session string, at time.Time, history []schemas.ChatMessage, output schemas.ChatMessage) *logstore.Log {
	return &logstore.Log{
		ID:                  id,
		SessionID:           &session,
		Timestamp:           at,
		Object:              objectChat,
		Provider:            "openai",
		Model:               "m",
		Status:              statusSuccess,
		InputHistoryParsed:  history,
		OutputMessageParsed: &output,
		ToolsParsed:         functionTools("check", "refund"),
		ParamsParsed:        map[string]any{"temperature": 0.2},
	}
}

// goodSession is a two-turn session: the model checks an order, then answers.
func goodSession(sid string) []*logstore.Log {
	check := call("c1", "check", `{"id":"o1"}`)
	h1 := []schemas.ChatMessage{user("refund o1")}
	o1 := assistantCalls(check)
	h2 := append(slices.Clone(h1), o1, toolResult("c1", "eligible"))
	return []*logstore.Log{
		row(sid+"-1", sid, t0, h1, o1),
		row(sid+"-2", sid, t0.Add(time.Second), h2, assistantText("refunded")),
	}
}

// ---- fake log store ----

// fakeLogs answers like the log store does: list queries return rows without their payloads, and
// FindByID returns the whole row.
type fakeLogs struct {
	rows         []*logstore.Log
	mcpRuns      map[string]int
	findByID     map[string]int
	reverseTies  bool
	searchErr    error
	findErr      error
	mcpErr       error
	missingByID  map[string]bool
	searchCalled int
}

func newFakeLogs(rows ...*logstore.Log) *fakeLogs {
	return &fakeLogs{rows: rows, mcpRuns: map[string]int{}, findByID: map[string]int{}, missingByID: map[string]bool{}}
}

func listCopy(r *logstore.Log) logstore.Log {
	c := *r
	c.InputHistoryParsed, c.OutputMessageParsed, c.ToolsParsed, c.ParamsParsed = nil, nil, nil, nil
	return c
}

func (f *fakeLogs) SearchLogs(_ context.Context, fl logstore.SearchFilters, p logstore.PaginationOptions) (*logstore.SearchResult, error) {
	f.searchCalled++
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	var matched []logstore.Log
	for _, r := range f.rows {
		if fl.SessionID != "" && (r.SessionID == nil || *r.SessionID != fl.SessionID) {
			continue
		}
		if len(fl.Providers) > 0 && !slices.Contains(fl.Providers, r.Provider) {
			continue
		}
		if len(fl.Models) > 0 && !slices.Contains(fl.Models, r.Model) {
			continue
		}
		if fl.RootsOnly && r.ParentRequestID != nil {
			continue
		}
		matched = append(matched, listCopy(r))
	}
	if fl.SessionGroupingActive() {
		matched = groupSessions(matched)
	}
	order := 1
	if p.Order == string(logstore.SortDesc) {
		order = -1
	}
	slices.SortStableFunc(matched, func(a, b logstore.Log) int { return order * a.Timestamp.Compare(b.Timestamp) })
	if f.reverseTies {
		// Rows that share a timestamp come back in the opposite order from how they were logged.
		for i := 0; i < len(matched); {
			j := i
			for j < len(matched) && matched[j].Timestamp.Equal(matched[i].Timestamp) {
				j++
			}
			slices.Reverse(matched[i:j])
			i = j
		}
	}
	start := min(p.Offset, len(matched))
	end := len(matched)
	if p.Limit > 0 {
		end = min(start+p.Limit, end)
	}
	return &logstore.SearchResult{Logs: matched[start:end]}, nil
}

// groupSessions keeps each session's earliest row, with its other rows counted, and every sessionless row.
func groupSessions(rows []logstore.Log) []logstore.Log {
	slices.SortStableFunc(rows, func(a, b logstore.Log) int { return a.Timestamp.Compare(b.Timestamp) })
	first := map[string]int{}
	var out []logstore.Log
	for _, r := range rows {
		if r.SessionID == nil || *r.SessionID == "" {
			out = append(out, r)
			continue
		}
		if i, ok := first[*r.SessionID]; ok {
			out[i].SessionChildCount++
			if r.Cost != nil {
				out[i].SessionTotalCost += *r.Cost
			}
			continue
		}
		first[*r.SessionID] = len(out)
		if r.Cost != nil {
			r.SessionTotalCost = *r.Cost
		}
		out = append(out, r)
	}
	return out
}

func (f *fakeLogs) FindByID(_ context.Context, id string) (*logstore.Log, error) {
	f.findByID[id]++
	if f.findErr != nil {
		return nil, f.findErr
	}
	if f.missingByID[id] {
		return nil, nil
	}
	for _, r := range f.rows {
		if r.ID == id {
			c := *r
			return &c, nil
		}
	}
	return nil, fmt.Errorf("no row %s", id)
}

func (f *fakeLogs) SearchMCPToolLogs(_ context.Context, fl logstore.MCPToolLogSearchFilters, _ logstore.PaginationOptions) (*logstore.MCPToolLogSearchResult, error) {
	if f.mcpErr != nil {
		return nil, f.mcpErr
	}
	return &logstore.MCPToolLogSearchResult{Pagination: logstore.PaginationOptions{TotalCount: int64(f.mcpRuns[fl.SessionID])}}, nil
}

func (f *fakeLogs) totalFindByID() int {
	n := 0
	for _, c := range f.findByID {
		n += c
	}
	return n
}

// ---- fixtures recorded in M1 ----

func loadFixture(t *testing.T, name string) *logstore.Log {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var r logstore.Log
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if len(r.InputHistoryParsed) == 0 || r.OutputMessageParsed == nil {
		t.Fatalf("%s: the row lost its payload on decoding", name)
	}
	return &r
}

// fixtureLogs holds the six rows of the three sessions from Spike C. The gateway-run session's tool
// ran once on the MCP server.
func fixtureLogs(t *testing.T) *fakeLogs {
	t.Helper()
	var rows []*logstore.Log
	for _, n := range []string{"gw_row1", "gw_row2", "cr_row1", "cr_row2", "crs_row1", "crs_row2"} {
		rows = append(rows, loadFixture(t, n+".json"))
	}
	f := newFakeLogs(rows...)
	f.mcpRuns["spike-gw"] = 1
	return f
}

// traceJSON prints a trace the way a person reads it, with the cassette as a summary.
func traceJSON(t *testing.T, tr *SessionTrace) string {
	t.Helper()
	b, err := json.MarshalIndent(tr, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func mustBuild(t *testing.T, f *fakeLogs, sid string, opts ReconOptions) *SessionTrace {
	t.Helper()
	tr, elig, err := NewReconstructor(f, opts).Build(context.Background(), sid)
	if err != nil {
		t.Fatalf("Build(%s): %v", sid, err)
	}
	if !elig.Eligible() || tr == nil {
		t.Fatalf("Build(%s) = %+v; want an eligible trace", sid, elig)
	}
	return tr
}

func wantIneligible(t *testing.T, f *fakeLogs, sid string, opts ReconOptions, code string) {
	t.Helper()
	tr, elig, err := NewReconstructor(f, opts).Build(context.Background(), sid)
	if err != nil {
		t.Fatalf("Build(%s): %v", sid, err)
	}
	if elig.Code != code || tr != nil {
		t.Errorf("Build(%s) = %+v, trace %v; want ineligible with %s", sid, elig, tr != nil, code)
	}
}

// ---- tests ----

func TestBuild_ClientRunSession(t *testing.T) {
	tr := mustBuild(t, fixtureLogs(t), "spike-cr", ReconOptions{})

	if tr.Route != (Route{Provider: "openai", Model: "mock-model"}) {
		t.Errorf("route = %+v", tr.Route)
	}
	if len(tr.Seed) != 1 || tr.Seed[0].Role != schemas.ChatMessageRoleUser {
		t.Fatalf("seed = %+v", tr.Seed)
	}
	if text, _ := messageText(tr.Seed[0].Content); text != "[scenario:cr] look up order o1" {
		t.Errorf("seed text = %q", text)
	}
	wantActions := []Action{
		{Kind: ActionTools, Calls: []ToolCall{{Name: "client_lookup", Args: `{"order_id":"o1"}`}}},
		{Kind: ActionFinal, Text: "Order o1 is shipped."},
	}
	if !actionsEqual(tr.Actions, wantActions) {
		t.Errorf("actions = %+v, want %+v", tr.Actions, wantActions)
	}
	if len(tr.RowIDs) != 2 {
		t.Errorf("row ids = %v", tr.RowIDs)
	}
	// The logged tool list holds the client's tool and the MCP tools injected at the gateway.
	var names []string
	for _, tool := range tr.Tools {
		names = append(names, tool.Function.Name)
	}
	if !slices.Equal(names, []string{"client_lookup", "shop-issue_refund", "shop-lookup_order"}) {
		t.Errorf("tools = %v", names)
	}

	result, isError, outcome := tr.Cassette.Clone().Lookup("client_lookup", `{"order_id": "o1"}`)
	if result != `{"status":"shipped"}` || isError || outcome != OutcomeHit {
		t.Errorf("cassette lookup = %q, %v, %s", result, isError, outcome)
	}
	if _, _, outcome := tr.Cassette.Clone().Lookup("shop-lookup_order", `{"order_id":"o1"}`); outcome != OutcomeMissUnrecorded {
		t.Errorf("a declared tool that was never called gave %s, want %s", outcome, OutcomeMissUnrecorded)
	}
}

func TestBuild_GatewayAgentSession(t *testing.T) {
	tr := mustBuild(t, fixtureLogs(t), "spike-gw", ReconOptions{})

	wantActions := []Action{
		{Kind: ActionTools, Calls: []ToolCall{{Name: "shop-lookup_order", Args: `{"order_id":"o1"}`}}},
		{Kind: ActionFinal, Text: "Order o1 is shipped."},
	}
	if !actionsEqual(tr.Actions, wantActions) {
		t.Errorf("actions = %+v, want %+v", tr.Actions, wantActions)
	}
	if len(tr.RowIDs) != 2 {
		t.Errorf("row ids = %v; want one per agent turn", tr.RowIDs)
	}
	// The result the gateway got from the MCP server is the recording.
	result, _, outcome := tr.Cassette.Clone().Lookup("shop-lookup_order", `{"order_id":"o1"}`)
	if result != `lookup_order:{"order_id":"o1"}` || outcome != OutcomeHit {
		t.Errorf("cassette lookup = %q, %s", result, outcome)
	}
}

func TestBuild_StreamedSession(t *testing.T) {
	f := fixtureLogs(t)
	streamed := mustBuild(t, f, "spike-crs", ReconOptions{})
	plain := mustBuild(t, f, "spike-cr", ReconOptions{})

	if !actionsEqual(streamed.Actions, plain.Actions) {
		t.Errorf("streamed actions %+v differ from non-streamed %+v", streamed.Actions, plain.Actions)
	}
	if len(streamed.Seed) != 1 || streamed.Tools[0].Function.Name != "client_lookup" {
		t.Errorf("streamed seed or tools differ: %+v / %+v", streamed.Seed, streamed.Tools)
	}
	result, _, outcome := streamed.Cassette.Clone().Lookup("client_lookup", `{"order_id":"o1"}`)
	if result != `{"status":"shipped"}` || outcome != OutcomeHit {
		t.Errorf("cassette lookup = %q, %s", result, outcome)
	}
}

func TestBuild_PrintTrace(t *testing.T) {
	tr := mustBuild(t, fixtureLogs(t), "spike-cr", ReconOptions{})
	out := traceJSON(t, tr)
	t.Log("\n" + out)
	for _, want := range []string{`"session_id": "spike-cr"`, `"client_lookup"`, `Order o1 is shipped.`, `"calls": 1`} {
		if !strings.Contains(out, want) {
			t.Errorf("printed trace lacks %s", want)
		}
	}
	if strings.Contains(out, "status") {
		t.Error("a tool result leaked into the printed trace")
	}
}

func TestBuild_ParallelToolCalls(t *testing.T) {
	a, b := call("ca", "refund", `{"id":"o2"}`), call("cb", "check", `{"id":"o1"}`)
	h1 := []schemas.ChatMessage{user("do both")}
	o1 := assistantCalls(a, b)
	// Results arrive in the opposite order from the calls.
	h2 := []schemas.ChatMessage{h1[0], o1, toolResult("cb", "result b"), toolResult("ca", "result a")}
	f := newFakeLogs(
		row("r1", "par", t0, h1, o1),
		row("r2", "par", t0.Add(time.Second), h2, assistantText("done")),
	)
	tr := mustBuild(t, f, "par", ReconOptions{})

	if len(tr.Actions) != 2 || tr.Actions[0].Kind != ActionTools {
		t.Fatalf("actions = %+v", tr.Actions)
	}
	got := tr.Actions[0].Calls
	if len(got) != 2 || got[0].Name != "check" || got[1].Name != "refund" {
		t.Errorf("one parallel group sorted by name expected, got %+v", got)
	}
	c := tr.Cassette.Clone()
	if r, _, o := c.Lookup("refund", `{"id":"o2"}`); r != "result a" || o != OutcomeHit {
		t.Errorf("refund = %q, %s; want its own result", r, o)
	}
	if r, _, o := c.Lookup("check", `{"id":"o1"}`); r != "result b" || o != OutcomeHit {
		t.Errorf("check = %q, %s; want its own result", r, o)
	}
}

func TestBuild_MessageOrderBeatsTimestamps(t *testing.T) {
	rows := goodSession("tie")
	rows[1].Timestamp = rows[0].Timestamp // the clock cannot tell the turns apart
	f := newFakeLogs(rows...)
	f.reverseTies = true
	tr := mustBuild(t, f, "tie", ReconOptions{})

	want := mustBuild(t, newFakeLogs(goodSession("tie")...), "tie", ReconOptions{})
	if !actionsEqual(tr.Actions, want.Actions) {
		t.Errorf("actions with tied timestamps = %+v, want %+v", tr.Actions, want.Actions)
	}
	if !slices.Equal(tr.RowIDs, []string{"tie-1", "tie-2"}) {
		t.Errorf("rows are in chain order, got %v", tr.RowIDs)
	}
}

func TestBuild_FallbackChildRowsIgnored(t *testing.T) {
	rows := goodSession("fb")
	failedAttempt := row("fb-1-attempt", "fb", t0.Add(time.Hour), nil, assistantText("x"))
	failedAttempt.Status = "error"
	failedAttempt.ParentRequestID = schemas.Ptr("fb-1")
	succeededAttempt := row("fb-2-attempt", "fb", t0.Add(2*time.Hour), []schemas.ChatMessage{user("other")}, assistantText("y"))
	succeededAttempt.ParentRequestID = schemas.Ptr("fb-2")
	succeededAttempt.Model = "fallback-model"
	f := newFakeLogs(append(rows, failedAttempt, succeededAttempt)...)

	tr := mustBuild(t, f, "fb", ReconOptions{})
	if !slices.Equal(tr.RowIDs, []string{"fb-1", "fb-2"}) {
		t.Errorf("row ids = %v; child rows must not count", tr.RowIDs)
	}
	if f.findByID["fb-1-attempt"] != 0 || f.findByID["fb-2-attempt"] != 0 {
		t.Error("a child row was loaded")
	}
}

func TestBuild_FailedTurnThatWasRetried(t *testing.T) {
	rows := goodSession("retry")
	failed := row("retry-failed", "retry", t0.Add(500*time.Millisecond), rows[1].InputHistoryParsed, assistantText("x"))
	failed.Status = "error"
	f := newFakeLogs(append(rows, failed)...)
	// The failed row sits between the two good ones, so the session still ends well.
	tr := mustBuild(t, f, "retry", ReconOptions{})
	if len(tr.RowIDs) != 2 {
		t.Errorf("row ids = %v", tr.RowIDs)
	}
}

func TestBuild_LastRowFailed(t *testing.T) {
	for _, status := range []string{"error", "processing"} {
		rows := goodSession("late")
		third := row("late-3", "late", t0.Add(time.Minute), rows[1].InputHistoryParsed, assistantText("x"))
		third.Status = status
		wantIneligible(t, newFakeLogs(append(rows, third)...), "late", ReconOptions{}, CodeIncomplete)
	}
	noOutput := goodSession("noout")
	noOutput[1].OutputMessageParsed = nil
	wantIneligible(t, newFakeLogs(noOutput...), "noout", ReconOptions{}, CodeIncomplete)
}

func TestBuild_HistoryRewritten(t *testing.T) {
	// The client compacted the history between turns.
	rows := goodSession("rw")
	rows[1].InputHistoryParsed = []schemas.ChatMessage{user("summary of the chat so far"), rows[1].InputHistoryParsed[2]}
	wantIneligible(t, newFakeLogs(rows...), "rw", ReconOptions{}, CodeHistoryRewritten)

	// An earlier message was edited.
	edited := goodSession("ed")
	edited[1].InputHistoryParsed[0] = user("refund o9")
	wantIneligible(t, newFakeLogs(edited...), "ed", ReconOptions{}, CodeHistoryRewritten)

	// A reply was swapped for another.
	swapped := goodSession("sw")
	swapped[1].InputHistoryParsed[1] = assistantCalls(call("c1", "check", `{"id":"o2"}`))
	wantIneligible(t, newFakeLogs(swapped...), "sw", ReconOptions{}, CodeHistoryRewritten)
}

func TestBuild_HistoryDifferingOnlyInLayoutIsAccepted(t *testing.T) {
	rows := goodSession("lay")
	reasoning := "thinking"
	rows[1].InputHistoryParsed[1] = assistantCalls(call("c1", "check", `{ "id" : "o1" }`))
	rows[1].InputHistoryParsed[1].ChatAssistantMessage.Reasoning = &reasoning
	mustBuild(t, newFakeLogs(rows...), "lay", ReconOptions{})
}

func TestBuild_UnpairedToolCall(t *testing.T) {
	// A call with no result.
	noResult := goodSession("u1")
	noResult[1].InputHistoryParsed = noResult[1].InputHistoryParsed[:2]
	wantIneligible(t, newFakeLogs(noResult...), "u1", ReconOptions{}, CodeUnpairedToolCall)

	// A result with no call.
	noCall := goodSession("u2")
	noCall[1].InputHistoryParsed = append(noCall[1].InputHistoryParsed, toolResult("ghost", "x"))
	wantIneligible(t, newFakeLogs(noCall...), "u2", ReconOptions{}, CodeUnpairedToolCall)

	// The session ends on a tool call that was never answered.
	dangling := goodSession("u3")
	dangling[1].OutputMessageParsed = &schemas.ChatMessage{}
	*dangling[1].OutputMessageParsed = assistantCalls(call("c2", "refund", `{}`))
	wantIneligible(t, newFakeLogs(dangling...), "u3", ReconOptions{}, CodeUnpairedToolCall)

	// A result before any reply, a stray result after the final answer, a missing id, a repeated id.
	cases := map[string][]schemas.ChatMessage{
		"leading result": {toolResult("c1", "x"), assistantText("hi")},
		"stray result":   {user("hi"), assistantText("done"), toolResult("c1", "x")},
		"no result id":   {user("hi"), assistantCalls(call("c1", "check", `{}`)), {Role: schemas.ChatMessageRoleTool, ChatToolMessage: &schemas.ChatToolMessage{}}},
		"repeated id":    {user("hi"), assistantCalls(call("c1", "check", `{}`)), toolResult("c1", "a"), toolResult("c1", "b")},
		"call without id": {user("hi"), assistantCalls(schemas.ChatAssistantMessageToolCall{Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("check")}}),
			toolResult("c1", "a")},
	}
	for name, transcript := range cases {
		_, elig := splitTranscript(append(transcript, assistantText("end")))
		if elig.Code != CodeUnpairedToolCall {
			t.Errorf("%s: code = %s, want %s", name, elig.Code, CodeUnpairedToolCall)
		}
	}
}

func TestBuild_ToolResultFlaggedAsError(t *testing.T) {
	rows := goodSession("err")
	rows[1].InputHistoryParsed[2].IsError = schemas.Ptr(true)
	tr := mustBuild(t, newFakeLogs(rows...), "err", ReconOptions{})
	if _, isError, outcome := tr.Cassette.Clone().Lookup("check", `{"id":"o1"}`); !isError || outcome != OutcomeHit {
		t.Errorf("isError = %v, %s; the recorded failure must be kept", isError, outcome)
	}
}

func TestBuild_StoreErrors(t *testing.T) {
	boom := errors.New("boom")
	cases := map[string]func(*fakeLogs){
		"search":    func(f *fakeLogs) { f.searchErr = boom },
		"find":      func(f *fakeLogs) { f.findErr = boom },
		"mcp count": func(f *fakeLogs) { f.mcpErr = boom },
		"vanished":  func(f *fakeLogs) { f.missingByID["e-1"] = true },
	}
	for name, mutate := range cases {
		f := newFakeLogs(goodSession("e")...)
		mutate(f)
		tr, _, err := NewReconstructor(f, ReconOptions{}).Build(context.Background(), "e")
		if err == nil || tr != nil {
			t.Errorf("%s: err = %v, trace %v; want an error and no trace", name, err, tr != nil)
		}
	}
}

func TestBuild_TraceDoesNotAliasStoreRows(t *testing.T) {
	f := newFakeLogs(goodSession("al")...)
	tr := mustBuild(t, f, "al", ReconOptions{})
	text := "changed"
	f.rows[0].InputHistoryParsed[0].Content.ContentStr = &text
	if got, _ := messageText(tr.Seed[0].Content); got != "refund o1" {
		t.Errorf("the seed changed with the store row: %q", got)
	}
}

func TestSessionTrace_CassetteForRules(t *testing.T) {
	rows := goodSession("rules")
	rows[0].OutputMessageParsed.ToolCalls[0].Function.Arguments = `{"id":"o1","trace_id":"a"}`
	rows[1].InputHistoryParsed[1].ToolCalls[0].Function.Arguments = `{"id":"o1","trace_id":"a"}`
	tr := mustBuild(t, newFakeLogs(rows...), "rules", ReconOptions{})

	if _, _, o := tr.Cassette.Clone().Lookup("check", `{"id":"o1","trace_id":"b"}`); o != OutcomeMissArgs {
		t.Errorf("without a rule a changed trace_id is %s, want %s", o, OutcomeMissArgs)
	}
	withRule := tr.NewCassette(ToolRules{Rules: []ToolRule{{Tool: "check", IgnoreArgs: []string{"trace_id"}}}})
	if _, _, o := withRule.Lookup("check", `{"id":"o1","trace_id":"b"}`); o != OutcomeHit {
		t.Errorf("with ignore_args a changed trace_id is %s, want %s", o, OutcomeHit)
	}
}

func TestListSessions(t *testing.T) {
	var rows []*logstore.Log
	for i, sid := range []string{"s1", "s2", "s3"} {
		sess := goodSession(sid)
		for _, r := range sess {
			r.Timestamp = r.Timestamp.Add(time.Duration(i) * time.Hour)
			r.Cost = schemas.Ptr(0.5)
		}
		rows = append(rows, sess...)
	}
	rows = append(rows, row("none-1", "", t0.Add(10*time.Hour), nil, assistantText("x")))
	rows[len(rows)-1].SessionID = nil
	f := newFakeLogs(rows...)
	r := NewReconstructor(f, ReconOptions{})

	page, err := r.ListSessions(context.Background(), SessionQuery{})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range page.Sessions {
		ids = append(ids, s.SessionID)
	}
	if !slices.Equal(ids, []string{"s3", "s2", "s1"}) || page.Count != 3 || page.Limit != 50 {
		t.Errorf("sessions = %v, count %d, limit %d; want newest first, sessionless row skipped", ids, page.Count, page.Limit)
	}
	if s := page.Sessions[2]; s.Turns != 2 || s.TotalCost != 1.0 || s.Provider != "openai" || s.Model != "m" || !s.StartedAt.Equal(t0) {
		t.Errorf("summary = %+v", s)
	}
	if f.totalFindByID() != 0 {
		t.Error("listing must not load full rows")
	}

	page, err = r.ListSessions(context.Background(), SessionQuery{Limit: 1, Offset: 1})
	if err != nil || len(page.Sessions) != 1 || page.Sessions[0].SessionID != "s2" || page.Offset != 1 {
		t.Errorf("second page = %+v, %v", page, err)
	}
	page, _ = r.ListSessions(context.Background(), SessionQuery{Limit: 5, Offset: 10})
	if len(page.Sessions) != 0 || page.Sessions == nil {
		t.Errorf("a page past the end = %+v; want an empty list", page)
	}
	page, _ = r.ListSessions(context.Background(), SessionQuery{Model: "other"})
	if len(page.Sessions) != 0 {
		t.Errorf("model filter = %+v", page.Sessions)
	}
	page, _ = r.ListSessions(context.Background(), SessionQuery{Provider: "openai", Model: "m"})
	if len(page.Sessions) != 3 {
		t.Errorf("provider and model filter = %d sessions", len(page.Sessions))
	}
}

func TestListSessions_ReadsPastSessionlessRowsUpToACap(t *testing.T) {
	mk := func(sessionless int) *fakeLogs {
		var rows []*logstore.Log
		for i := 0; i < sessionless; i++ {
			r := row(fmt.Sprintf("n%d", i), "", t0.Add(time.Hour+time.Duration(i)*time.Second), nil, assistantText("x"))
			r.SessionID = nil
			rows = append(rows, r)
		}
		return newFakeLogs(append(rows, goodSession("late")...)...)
	}

	// Seventy newer sessionless rows: the session is on the second page.
	page, err := NewReconstructor(mk(70), ReconOptions{}).ListSessions(context.Background(), SessionQuery{Limit: 1})
	if err != nil || len(page.Sessions) != 1 || page.Sessions[0].SessionID != "late" {
		t.Errorf("page = %+v, %v; want the session found on a later page", page, err)
	}
	// Three hundred: past the cap of five pages.
	f := mk(300)
	page, err = NewReconstructor(f, ReconOptions{}).ListSessions(context.Background(), SessionQuery{Limit: 1})
	if err != nil || len(page.Sessions) != 0 || f.searchCalled != listMaxPages {
		t.Errorf("page = %+v, %v after %d searches; want an empty page after %d", page, err, f.searchCalled, listMaxPages)
	}

	f = newFakeLogs()
	f.searchErr = errors.New("boom")
	if _, err := NewReconstructor(f, ReconOptions{}).ListSessions(context.Background(), SessionQuery{}); err == nil {
		t.Error("expected the store error")
	}
}

func TestSplitTranscript_Seed(t *testing.T) {
	system := schemas.ChatMessage{Role: schemas.ChatMessageRoleSystem, Name: schemas.Ptr("sys"), Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("be brief")}}
	got, elig := splitTranscript([]schemas.ChatMessage{system, user("hi"), assistantCalls(call("c1", "check", `{}`)), toolResult("c1", "ok"), assistantText("done")})
	if !elig.Eligible() || len(got.seed) != 2 {
		t.Fatalf("seed = %+v, %+v", got.seed, elig)
	}
	if got.seed[0].Role != schemas.ChatMessageRoleSystem || got.seed[0].Name == nil || *got.seed[0].Name != "sys" {
		t.Errorf("the system message lost its role or name: %+v", got.seed[0])
	}
	if _, elig := splitTranscript([]schemas.ChatMessage{user("hi")}); elig.Code != CodeIncomplete {
		t.Errorf("a transcript with no assistant reply = %s, want %s", elig.Code, CodeIncomplete)
	}
}

func actionsEqual(a, b []Action) bool {
	return slices.EqualFunc(a, b, func(x, y Action) bool {
		return x.Kind == y.Kind && x.Text == y.Text && slices.Equal(x.Calls, y.Calls)
	})
}

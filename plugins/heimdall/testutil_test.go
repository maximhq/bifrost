package heimdall

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/logstore"
	"gorm.io/gorm"
)

// fakeCall is one tool call in a scripted reply.
type fakeCall struct {
	ID    string // empty leaves the id out of the reply
	Name  string
	Args  string
	Extra string // raw JSON for extra_content, such as a provider's thought signature
}

// fakeReply is what the fake provider answers with.
type fakeReply struct {
	Status int // 0 is 200
	Calls  []fakeCall
	Text   string
	Raw    string        // sent as the body, whatever else is set
	Delay  time.Duration // held back before answering
	Hang   bool          // never answers; waits for the client to leave
}

func textReply(text string) fakeReply { return fakeReply{Text: text} }

func callsReply(calls ...fakeCall) fakeReply { return fakeReply{Calls: calls} }

func errorReply(status int, message string) fakeReply {
	return fakeReply{Status: status, Raw: fmt.Sprintf(`{"error":{"message":%q,"type":"fake_error"}}`, message)}
}

// fakeRequest is one request the fake provider received.
type fakeRequest struct {
	Model    string
	Body     map[string]any
	Raw      []byte
	Messages []map[string]any
}

// assistantTurns counts the assistant messages the request carries, which is how many turns the
// conversation has already had.
func (r fakeRequest) assistantTurns() int {
	n := 0
	for _, m := range r.Messages {
		if m["role"] == "assistant" {
			n++
		}
	}
	return n
}

// fakeProvider speaks the OpenAI chat protocol. It records every request and answers from a script.
type fakeProvider struct {
	URL string

	mu        sync.Mutex
	reqs      []fakeRequest
	script    func(fakeRequest) fakeReply
	onRequest func(n int, r fakeRequest) // runs before the reply, with the 1-based request count
}

func newFakeProvider(t *testing.T, script func(fakeRequest) fakeReply) *fakeProvider {
	t.Helper()
	f := &fakeProvider{script: script}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	f.URL = srv.URL
	return f
}

// byModel answers each model from its list of replies, one per assistant turn already taken. The last
// reply repeats. A model with no list gets a 404.
func byModel(script map[string][]fakeReply) func(fakeRequest) fakeReply {
	return func(r fakeRequest) fakeReply {
		steps := script[r.Model]
		if len(steps) == 0 {
			return errorReply(404, "model "+r.Model+" is not scripted")
		}
		return steps[min(r.assistantTurns(), len(steps)-1)]
	}
}

func (f *fakeProvider) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	req := fakeRequest{Raw: raw}
	_ = json.Unmarshal(raw, &req.Body)
	req.Model, _ = req.Body["model"].(string)
	if msgs, ok := req.Body["messages"].([]any); ok {
		for _, m := range msgs {
			if mm, ok := m.(map[string]any); ok {
				req.Messages = append(req.Messages, mm)
			}
		}
	}

	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	n := len(f.reqs)
	hook := f.onRequest
	f.mu.Unlock()
	if hook != nil {
		hook(n, req)
	}

	reply := f.script(req)
	if reply.Hang {
		<-r.Context().Done()
		return
	}
	if reply.Delay > 0 {
		select {
		case <-time.After(reply.Delay):
		case <-r.Context().Done():
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if reply.Status != 0 {
		w.WriteHeader(reply.Status)
	}
	if reply.Raw != "" {
		fmt.Fprint(w, reply.Raw)
		return
	}
	fmt.Fprint(w, renderReply(req.Model, reply))
}

func renderReply(model string, reply fakeReply) string {
	message := map[string]any{"role": "assistant", "content": reply.Text}
	finish := "stop"
	if len(reply.Calls) > 0 {
		var calls []map[string]any
		for _, c := range reply.Calls {
			call := map[string]any{
				"type":     "function",
				"function": map[string]any{"name": c.Name, "arguments": c.Args},
			}
			if c.ID != "" {
				call["id"] = c.ID
			}
			if c.Extra != "" {
				call["extra_content"] = json.RawMessage(c.Extra)
			}
			calls = append(calls, call)
		}
		message["content"] = nil
		message["tool_calls"] = calls
		finish = "tool_calls"
	}
	body, _ := json.Marshal(map[string]any{
		"id": "chatcmpl-fake", "object": "chat.completion", "created": 1, "model": model,
		"choices": []map[string]any{{"index": 0, "finish_reason": finish, "message": message}},
		"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
	})
	return string(body)
}

func (f *fakeProvider) requests() []fakeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeRequest(nil), f.reqs...)
}

func (f *fakeProvider) count() int { return len(f.requests()) }

// account points the openai provider at this fake.
func (f *fakeProvider) account() *testAccount {
	a := newTestAccount()
	a.add(schemas.OpenAI, f.URL)
	return a
}

// engineFixture is an engine over a replay client wired to a fake provider.
type engineFixture struct {
	*Engine
	client *bifrost.Bifrost
	fake   *fakeProvider
}

func newEngineFixture(t *testing.T, fake *fakeProvider, opts EngineOptions) *engineFixture {
	t.Helper()
	return newEngineFixtureRoutes(t, fake, opts, baseRoute, candRoute)
}

func newEngineFixtureRoutes(t *testing.T, fake *fakeProvider, opts EngineOptions, routes ...Route) *engineFixture {
	t.Helper()
	client, err := newReplayClient(context.Background(), fake.account(), routes, 2, nil, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Shutdown)
	return &engineFixture{Engine: NewEngine(client, opts), client: client, fake: fake}
}

var (
	candRoute = Route{Provider: schemas.OpenAI, Model: "cand"}
	baseRoute = Route{Provider: schemas.OpenAI, Model: "base"}
)

// checkOnce is the scripted candidate that follows the recording of goodSession: it calls check, then answers.
func checkOnce() []fakeReply {
	return []fakeReply{callsReply(fakeCall{ID: "k1", Name: "check", Args: `{"id":"o1"}`}), textReply("refunded")}
}

// engineTrace is the trace of goodSession: check({"id":"o1"}) answered "eligible", then a final answer.
func engineTrace(t *testing.T) *SessionTrace {
	t.Helper()
	return mustBuild(t, newFakeLogs(goodSession("s")...), "s", ReconOptions{})
}

// fixedPricing prices every reply the same.
type fixedPricing struct {
	schemas.ModelInfoProvider
	cost    float64
	onPrice func()
}

func (p fixedPricing) CalculateRequestCost(*schemas.BifrostContext, *schemas.BifrostResponse) float64 {
	if p.onPrice != nil {
		p.onPrice()
	}
	return p.cost
}

// fixedParams is a model catalog's list of supported parameters.
type fixedParams []string

func (p fixedParams) GetSupportedParameters(string) []string { return p }

// newTestConfigStore opens a real config store on a SQLite file, so the tests run the real migrations.
func newTestConfigStore(t *testing.T) configstore.ConfigStore {
	t.Helper()
	cs, err := configstore.NewConfigStore(context.Background(), &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "config.db")},
	}, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close(context.Background()) })
	return cs
}

// countingStore counts how often the connection is asked for.
type countingStore struct {
	configstore.ConfigStore
	calls atomic.Int64
}

func (c *countingStore) DB() *gorm.DB {
	c.calls.Add(1)
	return c.ConfigStore.DB()
}

// pluginFixture is a whole plugin: a real config store, a fake log store holding demo sessions, and a fake provider.
type pluginFixture struct {
	*Plugin
	cs   configstore.ConfigStore
	logs *fakeLogs
	fake *fakeProvider
}

// newPluginFixture starts a plugin over the demo sessions of the given orders. The provider answers from script.
func newPluginFixture(t *testing.T, orders []int, script func(fakeRequest) fakeReply, mutate func(*Config)) *pluginFixture {
	t.Helper()
	var rows []*logstore.Log
	for _, n := range orders {
		rows = append(rows, demoRows(n)...)
	}
	logs := newFakeLogs(rows...)
	fake := newFakeProvider(t, script)
	cs := newTestConfigStore(t)
	cfg := &Config{MaxConcurrency: 2, CallTimeout: "5s"}
	if mutate != nil {
		mutate(cfg)
	}
	p, err := Init(context.Background(), cfg, testLogger(), Deps{Account: fake.account(), LogStore: logs, ConfigStore: cs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Cleanup() })
	return &pluginFixture{Plugin: p, cs: cs, logs: logs, fake: fake}
}

// demoScript is the demo models' script with a fresh noise counter.
func demoScript() func(fakeRequest) fakeReply {
	models := &demoModels{firstTurns: map[string]int{}}
	return models.script
}

// demoRequest is a valid request to run the demo sessions of the given orders.
func demoRequest(candidate string, orders []int) CreateRunRequest {
	ids := make([]string, len(orders))
	for i, n := range orders {
		ids[i] = demoSessionID(n)
	}
	return CreateRunRequest{
		Name:           "try " + candidate,
		Baseline:       baseRoute,
		Candidate:      Route{Provider: schemas.OpenAI, Model: candidate},
		Selection:      Selection{SessionIDs: ids},
		ToolRules:      demoRules().Rules,
		ForbiddenTools: demoRules().ForbiddenTools,
		MaxUSD:         5,
	}
}

// waitForRun waits until a run is no longer queued or running.
func waitForRun(t *testing.T, p *Plugin, id string) *RunDetail {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		run, err := p.GetRun(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != StatusQueued && run.Status != StatusRunning {
			return run
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("run %s did not finish", id)
	return nil
}

// startAndWait starts a run and waits for it to end.
func startAndWait(t *testing.T, p *Plugin, req CreateRunRequest) *RunDetail {
	t.Helper()
	started, err := p.StartRun(context.Background(), req)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	return waitForRun(t, p, started.ID)
}

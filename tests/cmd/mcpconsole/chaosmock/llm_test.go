package chaosmock_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/tests/cmd/mcpconsole/chaosmock"
)

// The LLM surface is only as useful as a real client's parsing of it, so these
// tests read every reply with the strictness the formats' own clients apply:
// arguments as strings where OpenAI wants them, every Anthropic event typed,
// Responses sequence numbers increasing, a stream's last frame where a client
// looks for it.

// llmScript scripts one scenario on alpha and fails the test on a refusal.
func llmScript(t *testing.T, fleet *chaosmock.Fleet, body map[string]any) {
	t.Helper()
	if resp, _ := post(t, fleet.URL()+"/control/alpha/llm/script", body); resp.StatusCode != http.StatusOK {
		t.Fatalf("llm/script refused %v: %d", body, resp.StatusCode)
	}
}

// llmDo sends one request to alpha's LLM surface and returns the status,
// headers and body.
func llmDo(t *testing.T, fleet *chaosmock.Fleet, method, path, body string) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequest(method, fleet.URL()+"/s/alpha/llm"+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s failed: %v", method, path, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(out)
}

// sseEvent is one server-sent event.
type sseEvent struct {
	event string
	data  string
}

// readSSE splits an event stream into its events.
func readSSE(t *testing.T, body string) []sseEvent {
	t.Helper()
	var events []sseEvent
	var cur sseEvent
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if cur.data != "" {
				events = append(events, cur)
			}
			cur = sseEvent{}
		case strings.HasPrefix(line, "event: "):
			cur.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			cur.data = strings.TrimPrefix(line, "data: ")
		default:
			t.Fatalf("unexpected stream line %q", line)
		}
	}
	return events
}

// decode parses JSON into a map, failing the test otherwise.
func decode(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("not a JSON object: %v\n%s", err, raw)
	}
	return m
}

// at walks a decoded JSON value by keys and indexes.
func at(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, _ := v.(map[string]any)
			v = m[k]
		case int:
			a, _ := v.([]any)
			if k >= len(a) {
				return nil
			}
			v = a[k]
		}
	}
	return v
}

// turnWithCall is a reply with text and one call, used by every format test.
var turnWithCall = map[string]any{"text": "Hello there", "tool_calls": []any{map[string]any{"id": "t1", "name": "alpha-echo", "arguments": map[string]any{"text": "hi"}}}, "usage": map[string]any{"input": 7, "output": 3}}

func TestLLMChatRepliesAsOpenAIChatCompletions(t *testing.T) {
	fleet, _ := newFleetWithTools(t)
	llmScript(t, fleet, map[string]any{"scenario": "chat", "then": turnWithCall})
	req := `{"model":"m1","messages":[{"role":"user","content":"[scenario:chat] go"}]}`

	status, _, body := llmDo(t, fleet, "POST", "/openai/v1/chat/completions", req)
	b := decode(t, body)
	if status != 200 || at(b, "choices", 0, "message", "content") != "Hello there" || at(b, "choices", 0, "finish_reason") != "tool_calls" || at(b, "model") != "m1" {
		t.Fatalf("unexpected reply %d %s", status, body)
	}
	// OpenAI carries arguments as a string; an object fails a real client.
	if args, ok := at(b, "choices", 0, "message", "tool_calls", 0, "function", "arguments").(string); !ok || args != `{"text":"hi"}` {
		t.Errorf("arguments must be the JSON text, got %#v", at(b, "choices", 0, "message", "tool_calls", 0, "function", "arguments"))
	}
	if at(b, "usage", "total_tokens") != 10.0 {
		t.Errorf("unexpected usage %v", at(b, "usage"))
	}

	stream := `{"model":"m1","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"[scenario:chat] go"}]}`
	_, header, body := llmDo(t, fleet, "POST", "/openai/v1/chat/completions", stream)
	if header.Get("Content-Type") != "text/event-stream" {
		t.Errorf("expected an event stream, got %q", header.Get("Content-Type"))
	}
	events := readSSE(t, body)
	if events[len(events)-1].data != "[DONE]" {
		t.Fatalf("the stream must end with [DONE], got %+v", events[len(events)-1])
	}
	usage := decode(t, events[len(events)-2].data)
	if len(at(usage, "choices").([]any)) != 0 || at(usage, "usage", "total_tokens") != 10.0 {
		t.Errorf("expected a usage frame before [DONE], got %s", events[len(events)-2].data)
	}
	var text, args, finish string
	for _, e := range events[:len(events)-2] {
		c := decode(t, e.data)
		if s, ok := at(c, "choices", 0, "delta", "content").(string); ok {
			text += s
		}
		if s, ok := at(c, "choices", 0, "delta", "tool_calls", 0, "function", "arguments").(string); ok {
			args += s
		}
		if s, ok := at(c, "choices", 0, "finish_reason").(string); ok {
			finish = s
		}
	}
	if text != "Hello there" || args != `{"text":"hi"}` || finish != "tool_calls" {
		t.Errorf("joined stream gave %q, %q, %q", text, args, finish)
	}
}

func TestLLMResponsesRepliesAsOpenAIResponses(t *testing.T) {
	fleet, _ := newFleetWithTools(t)
	llmScript(t, fleet, map[string]any{"scenario": "resp", "then": turnWithCall})

	_, _, body := llmDo(t, fleet, "POST", "/openai/v1/responses", `{"model":"m1","input":"[scenario:resp]"}`)
	b := decode(t, body)
	if at(b, "status") != "completed" || at(b, "output", 0, "content", 0, "text") != "Hello there" ||
		at(b, "output", 1, "type") != "function_call" || at(b, "output", 1, "call_id") != "t1" || at(b, "output", 1, "arguments") != `{"text":"hi"}` ||
		at(b, "usage", "input_tokens") != 7.0 {
		t.Fatalf("unexpected reply %s", body)
	}

	_, _, body = llmDo(t, fleet, "POST", "/openai/v1/responses", `{"model":"m1","stream":true,"input":"[scenario:resp]"}`)
	events := readSSE(t, body)
	var text, args string
	for i, e := range events {
		d := decode(t, e.data)
		// A client indexes chunks by sequence_number and drops repeats.
		if at(d, "sequence_number") != float64(i) || e.event != at(d, "type") {
			t.Fatalf("event %d: sequence_number %v, event %q, type %v", i, at(d, "sequence_number"), e.event, at(d, "type"))
		}
		switch e.event {
		case "response.output_text.delta":
			text += at(d, "delta").(string)
		case "response.function_call_arguments.delta":
			args += at(d, "delta").(string)
		}
	}
	last := decode(t, events[len(events)-1].data)
	if events[0].event != "response.created" || events[len(events)-1].event != "response.completed" || at(last, "response", "usage", "total_tokens") != 10.0 {
		t.Errorf("expected created first and completed with usage last, got %s ... %s", events[0].event, events[len(events)-1].data)
	}
	if text != "Hello there" || args != `{"text":"hi"}` {
		t.Errorf("joined stream gave %q, %q", text, args)
	}
}

func TestLLMMessagesRepliesAsAnthropicMessages(t *testing.T) {
	fleet, _ := newFleetWithTools(t)
	llmScript(t, fleet, map[string]any{"scenario": "anth", "then": turnWithCall})

	_, _, body := llmDo(t, fleet, "POST", "/anthropic/v1/messages", `{"model":"m1","max_tokens":9,"messages":[{"role":"user","content":"[scenario:anth]"}]}`)
	b := decode(t, body)
	if at(b, "type") != "message" || at(b, "stop_reason") != "tool_use" || at(b, "content", 0, "text") != "Hello there" ||
		at(b, "content", 1, "type") != "tool_use" || at(b, "content", 1, "input", "text") != "hi" || at(b, "usage", "output_tokens") != 3.0 {
		t.Fatalf("unexpected reply %s", body)
	}

	_, _, body = llmDo(t, fleet, "POST", "/anthropic/v1/messages", `{"model":"m1","stream":true,"messages":[{"role":"user","content":"[scenario:anth]"}]}`)
	events := readSSE(t, body)
	var text, partial string
	for _, e := range events {
		d := decode(t, e.data)
		// Every event must be typed by its event line, or a client skips it.
		if e.event == "" || e.event != at(d, "type") {
			t.Fatalf("event line %q does not match type %v", e.event, at(d, "type"))
		}
		switch e.event {
		case "content_block_start":
			if _, ok := at(d, "index").(float64); !ok {
				t.Errorf("content_block_start needs an index: %s", e.data)
			}
		case "content_block_delta":
			if s, ok := at(d, "delta", "text").(string); ok {
				text += s
			}
			if s, ok := at(d, "delta", "partial_json").(string); ok {
				partial += s
			}
		case "message_delta":
			if at(d, "delta", "stop_reason") != "tool_use" || at(d, "usage", "output_tokens") != 3.0 {
				t.Errorf("unexpected message_delta %s", e.data)
			}
		}
	}
	if events[0].event != "message_start" || events[len(events)-1].event != "message_stop" {
		t.Errorf("expected message_start first and message_stop last")
	}
	if text != "Hello there" || partial != `{"text":"hi"}` {
		t.Errorf("joined stream gave %q, %q", text, partial)
	}
}

func TestLLMGenerateContentRepliesAsGemini(t *testing.T) {
	fleet, _ := newFleetWithTools(t)
	llmScript(t, fleet, map[string]any{"scenario": "gen", "then": turnWithCall})
	req := `{"contents":[{"role":"user","parts":[{"text":"[scenario:gen]"}]}]}`

	_, _, body := llmDo(t, fleet, "POST", "/genai/v1beta/models/m1:generateContent", req)
	b := decode(t, body)
	if at(b, "candidates", 0, "content", "parts", 0, "text") != "Hello there" || at(b, "candidates", 0, "content", "parts", 1, "functionCall", "args", "text") != "hi" ||
		at(b, "candidates", 0, "finishReason") != "STOP" || at(b, "usageMetadata", "totalTokenCount") != 10.0 || at(b, "modelVersion") != "m1" {
		t.Fatalf("unexpected reply %s", body)
	}

	_, _, body = llmDo(t, fleet, "POST", "/genai/v1beta/models/m1:streamGenerateContent?alt=sse", req)
	events := readSSE(t, body)
	var text string
	for i, e := range events {
		d := decode(t, e.data)
		for _, p := range at(d, "candidates", 0, "content", "parts").([]any) {
			if s, ok := at(p, "text").(string); ok {
				text += s
			}
		}
		// Usage and the finish reason ride the last chunk only.
		last := i == len(events)-1
		if (at(d, "usageMetadata") != nil) != last || (at(d, "candidates", 0, "finishReason") != nil) != last {
			t.Errorf("chunk %d: usage or finishReason in the wrong chunk: %s", i, e.data)
		}
	}
	if text != "Hello there" || at(decode(t, events[len(events)-1].data), "candidates", 0, "content", "parts", 1, "functionCall", "name") != "alpha-echo" {
		t.Errorf("joined stream gave %q and no call", text)
	}
}

func TestLLMScenarioQueueThenAndRefusals(t *testing.T) {
	fleet, srv := newFleetWithTools(t)
	llmScript(t, fleet, map[string]any{"scenario": "q", "turns": []any{map[string]any{"text": "one"}, map[string]any{"text": "two"}}, "then": map[string]any{"text": "again"}})
	chat := func(content string) (int, string) {
		status, _, body := llmDo(t, fleet, "POST", "/openai/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"`+content+`"}]}`)
		if status != 200 {
			return status, body
		}
		return status, at(decode(t, body), "choices", 0, "message", "content").(string)
	}
	// Turns in order, then the then turn again and again. The first marker
	// wins, so a later one quoted in the conversation does not reroute it.
	for i, want := range []string{"one", "two", "again", "again"} {
		if _, got := chat("[scenario:q] and [scenario:other]"); got != want {
			t.Fatalf("request %d: want %q, got %q", i, want, got)
		}
	}
	if status, body := chat("no marker"); status != 400 || !strings.Contains(body, "chaosmock_unscripted") {
		t.Errorf("expected 400 chaosmock_unscripted without a marker, got %d %s", status, body)
	}
	if status, _ := chat("[scenario:nobody]"); status != 400 {
		t.Errorf("expected 400 for an unscripted scenario, got %d", status)
	}
	llmScript(t, fleet, map[string]any{"scenario": "once", "turns": []any{map[string]any{"text": "only"}}})
	chat("[scenario:once]")
	if status, body := chat("[scenario:once]"); status != 400 || !strings.Contains(body, "no turn left") {
		t.Errorf("expected 400 once the turns are used up, got %d %s", status, body)
	}

	state := srv.State().LLM
	if state == nil || state.Scenarios["q"].Served != 4 || !state.Scenarios["q"].Then || state.Unmarked != 1 {
		t.Fatalf("unexpected llm state %+v", state)
	}
	turns := []any{}
	for _, c := range llmRequests(t, fleet, "q") {
		turns = append(turns, *c.Turn)
	}
	if b, _ := json.Marshal(turns); string(b) != "[0,1,-1,-1]" {
		t.Errorf("expected turns 0, 1, -1, -1 in the log, got %s", b)
	}

	// append keeps the log; script replaces the queue and clears only that
	// scenario's log.
	if resp, _ := post(t, fleet.URL()+"/control/alpha/llm/append", map[string]any{"scenario": "once", "turns": []any{map[string]any{"text": "more"}}}); resp.StatusCode != 200 {
		t.Fatalf("append refused: %d", resp.StatusCode)
	}
	if _, got := chat("[scenario:once]"); got != "more" || len(llmRequests(t, fleet, "once")) != 3 {
		t.Errorf("expected the appended turn with the log kept, got %q and %d entries", got, len(llmRequests(t, fleet, "once")))
	}
	llmScript(t, fleet, map[string]any{"scenario": "once", "turns": []any{map[string]any{"text": "fresh"}}})
	if len(llmRequests(t, fleet, "once")) != 0 || len(llmRequests(t, fleet, "q")) != 4 {
		t.Errorf("script must clear its own scenario's log and no other")
	}
}

// llmRequests reads a scenario's logged requests.
func llmRequests(t *testing.T, fleet *chaosmock.Fleet, scenario string) []chaosmock.LLMCall {
	t.Helper()
	resp, err := http.Get(fleet.URL() + "/control/alpha/llm/requests?scenario=" + scenario)
	if err != nil {
		t.Fatalf("llm/requests failed: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Requests []chaosmock.LLMCall `json:"requests"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("failed to decode llm/requests: %v", err)
	}
	return out.Requests
}

func TestLLMLogShowsWhatTheClientSent(t *testing.T) {
	fleet, srv := newFleetWithTools(t)
	llmScript(t, fleet, map[string]any{"scenario": "log", "then": map[string]any{"text": "ok"}})
	cases := []struct{ path, body, want string }{
		{"/openai/v1/chat/completions", `{"model":"a","messages":[{"role":"user","content":"[scenario:log]"}],"tools":[{"type":"function","function":{"name":"alpha-echo"}},{"type":"web_search"}]}`, `["alpha-echo","type:web_search"]`},
		{"/openai/v1/responses", `{"model":"b","input":"[scenario:log]","tools":[{"type":"function","name":"alpha-add"},{"type":"web_search"}]}`, `["alpha-add","type:web_search"]`},
		{"/anthropic/v1/messages", `{"model":"c","messages":[{"role":"user","content":"[scenario:log]"}],"tools":[{"name":"alpha-greet","input_schema":{}}]}`, `["alpha-greet"]`},
		{"/genai/v1beta/models/d:generateContent", `{"contents":[{"parts":[{"text":"[scenario:log]"}]}],"tools":[{"functionDeclarations":[{"name":"x"},{"name":"y"}]},{"googleSearch":{}}]}`, `["x","y","type:googleSearch"]`},
	}
	for _, c := range cases {
		if status, _, body := llmDo(t, fleet, "POST", c.path, c.body); status != 200 {
			t.Fatalf("%s: %d %s", c.path, status, body)
		}
	}
	calls := llmRequests(t, fleet, "log")
	if len(calls) != len(cases) {
		t.Fatalf("expected %d logged requests, got %d", len(cases), len(calls))
	}
	for i, c := range cases {
		tools, _ := json.Marshal(calls[i].Tools)
		if string(tools) != c.want || calls[i].Body != c.body || calls[i].Model != string(rune('a'+i)) || calls[i].Status != 200 {
			t.Errorf("%s: logged tools %s, model %q, status %d, body kept %v", c.path, tools, calls[i].Model, calls[i].Status, calls[i].Body == c.body)
		}
	}
	if calls[0].Format != "openai.chat" || calls[3].Format != "genai" || calls[0].Headers["User-Agent"] == "" {
		t.Errorf("unexpected format or headers: %+v", calls[0])
	}
	if since := llmRequestsSince(t, fleet, calls[1].Seq); len(since) != 2 {
		t.Errorf("expected two requests after seq %d, got %d", calls[1].Seq, len(since))
	}
	post(t, fleet.URL()+"/control/alpha/counters/reset", nil)
	if len(llmRequests(t, fleet, "log")) != 0 || srv.State().LLM.Scenarios["log"].Served != len(cases) {
		t.Errorf("counters/reset must clear the log and keep the scripts")
	}
}

// llmRequestsSince reads every logged request after seq.
func llmRequestsSince(t *testing.T, fleet *chaosmock.Fleet, seq int) []chaosmock.LLMCall {
	t.Helper()
	resp, err := http.Get(fleet.URL() + "/control/alpha/llm/requests?since=" + strconv.Itoa(seq))
	if err != nil {
		t.Fatalf("llm/requests failed: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Requests []chaosmock.LLMCall `json:"requests"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out.Requests
}

func TestLLMErrorRawAndArgumentTurns(t *testing.T) {
	fleet, _ := newFleetWithTools(t)
	llmScript(t, fleet, map[string]any{"scenario": "err", "then": map[string]any{"error": map[string]any{"status": 429, "message": "slow down", "type": "rate_limit_error", "retry_after": "7"}}})
	shapes := map[string][]any{
		"/openai/v1/chat/completions":            {"error", "message"},
		"/anthropic/v1/messages":                 {"error", "message"},
		"/genai/v1beta/models/m:generateContent": {"error", "message"},
	}
	for path, msg := range shapes {
		status, header, body := llmDo(t, fleet, "POST", path, `{"model":"m","messages":[{"content":"[scenario:err]"}],"contents":[{"parts":[{"text":"[scenario:err]"}]}]}`)
		if status != 429 || header.Get("Retry-After") != "7" || at(decode(t, body), msg...) != "slow down" {
			t.Errorf("%s: expected the 429 in the format's shape, got %d %q %s", path, status, header.Get("Retry-After"), body)
		}
	}

	llmScript(t, fleet, map[string]any{"scenario": "raw", "then": map[string]any{"raw": map[string]any{"status": 200, "content_type": "text/event-stream", "body": "data: {not json\n\n"}}})
	if status, header, body := llmDo(t, fleet, "POST", "/openai/v1/chat/completions", `{"messages":[{"content":"[scenario:raw]"}]}`); status != 200 || header.Get("Content-Type") != "text/event-stream" || body != "data: {not json\n\n" {
		t.Errorf("raw turn: got %d %q %q", status, header.Get("Content-Type"), body)
	}

	// Arguments given as text go to OpenAI verbatim, but a format that carries
	// an object needs them to be one.
	llmScript(t, fleet, map[string]any{"scenario": "args", "then": map[string]any{"tool_calls": []any{map[string]any{"name": "f", "arguments": "not json"}}}})
	_, _, body := llmDo(t, fleet, "POST", "/openai/v1/chat/completions", `{"messages":[{"content":"[scenario:args]"}]}`)
	if at(decode(t, body), "choices", 0, "message", "tool_calls", 0, "function", "arguments") != "not json" {
		t.Errorf("expected the arguments text verbatim, got %s", body)
	}
	if status, _, body := llmDo(t, fleet, "POST", "/anthropic/v1/messages", `{"messages":[{"content":"[scenario:args]"}]}`); status != 400 || !strings.Contains(body, "not a JSON object") {
		t.Errorf("expected 400 for text arguments on Anthropic, got %d %s", status, body)
	}
}

func TestLLMFaultsDelaysAndModelLists(t *testing.T) {
	fleet, srv := newFleetWithTools(t)
	llmScript(t, fleet, map[string]any{"scenario": "slow", "then": map[string]any{"text": "late", "delay": "5s"}})

	// A delayed turn ends as soon as the client hangs up, and is logged.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", fleet.URL()+"/s/alpha/llm/openai/v1/chat/completions", strings.NewReader(`{"messages":[{"content":"[scenario:slow]"}]}`))
	start := time.Now()
	if _, err := http.DefaultClient.Do(req); err == nil {
		t.Fatalf("expected the client to give up")
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(llmRequests(t, fleet, "slow")) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if calls := llmRequests(t, fleet, "slow"); len(calls) != 1 || calls[0].Status != 0 || time.Since(start) > 3*time.Second {
		t.Errorf("expected one logged request with status 0, got %+v", calls)
	}

	srv.SetFaults(chaosmock.Faults{Status: 503})
	if status, _, _ := llmDo(t, fleet, "POST", "/openai/v1/chat/completions", `{"messages":[{"content":"[scenario:slow]"}]}`); status != 503 {
		t.Errorf("expected the server's 503, got %d", status)
	}
	srv.SetFaults(chaosmock.Faults{})

	for path, want := range map[string][]any{
		"/openai/v1/models":    {"data", 0, "id"},
		"/anthropic/v1/models": {"data", 0, "id"},
		"/genai/v1beta/models": {"models", 0, "name"},
	} {
		_, _, body := llmDo(t, fleet, "GET", path, "")
		got := at(decode(t, body), want...)
		if got != "mock-model" && got != "models/mock-model" {
			t.Errorf("%s: expected the default model, got %s", path, body)
		}
	}
	post(t, fleet.URL()+"/control/alpha/llm/models", map[string]any{"models": []string{"m-a", "m-b"}})
	if _, _, body := llmDo(t, fleet, "GET", "/openai/v1/models", ""); at(decode(t, body), "data", 1, "id") != "m-b" {
		t.Errorf("expected the set models, got %s", body)
	}
	if status, _, body := llmDo(t, fleet, "GET", "/anthropic/v1/nope", ""); status != 404 || at(decode(t, body), "type") != "error" {
		t.Errorf("expected a 404 in Anthropic's shape, got %d %s", status, body)
	}
}

func TestLLMModelsFromTheSeedSurviveReset(t *testing.T) {
	fleet := chaosmock.NewFleet()
	fleet.AddSeeded("llm", chaosmock.Seed{LLMModels: []string{"seeded"}})
	fleet.Start()
	t.Cleanup(fleet.Stop)
	post(t, fleet.URL()+"/control/llm/llm/models", map[string]any{"models": []string{"changed"}})
	post(t, fleet.URL()+"/control", map[string]any{"reset": "llm"})
	resp, err := http.Get(fleet.URL() + "/s/llm/llm/openai/v1/models")
	if err != nil {
		t.Fatalf("models request failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if at(decode(t, string(body)), "data", 0, "id") != "seeded" {
		t.Errorf("expected reset to restore the seeded models, got %s", body)
	}
}

func TestControlRejectsLLMScriptsThatCannotBeServed(t *testing.T) {
	fleet, _ := newFleetWithTools(t)
	for name, c := range map[string]struct {
		verb string
		body map[string]any
	}{
		"bad finish":        {"llm/script", map[string]any{"scenario": "s", "turns": []any{map[string]any{"finish": "done"}}}},
		"call without name": {"llm/script", map[string]any{"scenario": "s", "turns": []any{map[string]any{"tool_calls": []any{map[string]any{"id": "x"}}}}}},
		"bad scenario key":  {"llm/script", map[string]any{"scenario": "a b", "turns": []any{map[string]any{"text": "x"}}}},
		"no turns":          {"llm/script", map[string]any{"scenario": "s"}},
		"then on append":    {"llm/append", map[string]any{"scenario": "s", "then": map[string]any{"text": "x"}}},
		"2xx error":         {"llm/script", map[string]any{"scenario": "s", "turns": []any{map[string]any{"error": map[string]any{"status": 200, "message": "x"}}}}},
		"misspelled field":  {"llm/script", map[string]any{"scenario": "s", "turns": []any{map[string]any{"txt": "x"}}}},
		"no models":         {"llm/models", map[string]any{"models": []string{}}},
	} {
		if resp, _ := post(t, fleet.URL()+"/control/alpha/"+c.verb, c.body); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d", name, resp.StatusCode)
		}
	}
}

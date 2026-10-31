package chaosmock

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Every virtual server also answers as an LLM provider under /s/<name>/llm,
// for a client under test that sends model requests as well as MCP traffic:
// what it adds to a model request (tools, instructions) is only provable on
// the wire, and what it does with a model's reply (call a tool, loop, give up)
// only testable when the reply is scripted. Four wire formats are served, each
// streaming or not, at the paths their public APIs define below a per-format
// prefix:
//
//	/openai/v1/chat/completions                  OpenAI Chat Completions
//	/openai/v1/responses                         OpenAI Responses
//	/anthropic/v1/messages                       Anthropic Messages
//	/genai/v1beta/models/<m>:generateContent     Gemini, and :streamGenerateContent
//
// plus each format's model listing. A test scripts replies per scenario: a
// "[scenario:<key>]" marker anywhere in the request body selects that
// scenario's queue of turns, and each turn is written in the format and stream
// mode the request arrived in. Every request is logged with its raw body, which
// is how a test reads what the client sent.

// LLMTurn is one scripted model reply, independent of wire format.
type LLMTurn struct {
	// Text is the assistant's text, empty for none.
	Text string `json:"text,omitempty"`
	// ToolCalls are the calls the model makes, in order.
	ToolCalls []LLMToolCall `json:"tool_calls,omitempty"`
	// Finish is stop, tool_calls or length. It defaults to tool_calls when
	// there are calls and stop otherwise.
	Finish string `json:"finish,omitempty"`
	// Usage defaults to 10 input and 5 output tokens.
	Usage *LLMUsage `json:"usage,omitempty"`
	// Error, when set, answers with an HTTP error in the format's shape
	// instead of a reply.
	Error *LLMError `json:"error,omitempty"`
	// Delay holds the answer back before its status line; ChunkDelay spaces a
	// stream's events. Both end early when the client hangs up.
	Delay      Duration `json:"delay,omitempty"`
	ChunkDelay Duration `json:"chunk_delay,omitempty"`
	// Raw, when set, is sent verbatim instead of a rendered reply: an unknown
	// event, a malformed body, anything a format renderer would not produce.
	Raw *LLMRaw `json:"raw,omitempty"`
}

// LLMToolCall is one tool call in a turn. Arguments is a JSON object, or a
// JSON string holding the arguments text exactly as it should be sent.
type LLMToolCall struct {
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// LLMUsage is a turn's token usage.
type LLMUsage struct {
	Input  int `json:"input"`
	Output int `json:"output"`
}

// LLMError is a scripted HTTP error. Type and Code fill the format's error
// fields where it has them; RetryAfter is sent as the Retry-After header.
type LLMError struct {
	Status     int    `json:"status"`
	Message    string `json:"message"`
	Type       string `json:"type,omitempty"`
	Code       string `json:"code,omitempty"`
	RetryAfter string `json:"retry_after,omitempty"`
}

// LLMRaw is a reply sent byte for byte.
type LLMRaw struct {
	Status      int    `json:"status,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Body        string `json:"body"`
}

// Validate rejects a turn that cannot be rendered as written.
func (t LLMTurn) Validate() error {
	switch t.Finish {
	case "", "stop", "tool_calls", "length":
	default:
		return fmt.Errorf("finish must be stop, tool_calls or length, got %q", t.Finish)
	}
	for _, c := range t.ToolCalls {
		if c.Name == "" {
			return fmt.Errorf("a tool call needs a name")
		}
		if len(c.Arguments) > 0 && !json.Valid(c.Arguments) {
			return fmt.Errorf("tool call %s: arguments are not JSON", c.Name)
		}
	}
	if t.Error != nil && (t.Error.Status < 400 || t.Error.Status > 599) {
		return fmt.Errorf("error status must be 4xx or 5xx, got %d", t.Error.Status)
	}
	if t.Raw != nil && t.Raw.Status != 0 && (t.Raw.Status < 100 || t.Raw.Status > 999) {
		return fmt.Errorf("raw status %d is not an HTTP status", t.Raw.Status)
	}
	return nil
}

// finish resolves the turn's finish reason.
func (t LLMTurn) finish() string {
	if t.Finish != "" {
		return t.Finish
	}
	if len(t.ToolCalls) > 0 {
		return "tool_calls"
	}
	return "stop"
}

// usage resolves the turn's token usage.
func (t LLMTurn) usage() LLMUsage {
	if t.Usage != nil {
		return *t.Usage
	}
	return LLMUsage{Input: 10, Output: 5}
}

// callID resolves a call's id, numbering unnamed calls within the turn.
func (c LLMToolCall) callID(turn, i int) string {
	if c.ID != "" {
		return c.ID
	}
	return fmt.Sprintf("call_%d_%d", turn, i)
}

// argsText is the arguments as the text OpenAI carries: a JSON string's
// content verbatim, anything else as compact JSON, {} when absent.
func (c LLMToolCall) argsText() string {
	if len(c.Arguments) == 0 {
		return "{}"
	}
	var s string
	if json.Unmarshal(c.Arguments, &s) == nil {
		return s
	}
	var buf bytes.Buffer
	if json.Compact(&buf, c.Arguments) != nil {
		return string(c.Arguments)
	}
	return buf.String()
}

// argsObject is the arguments as the object the other formats carry. It
// fails when a string's content is not a JSON object.
func (c LLMToolCall) argsObject() (json.RawMessage, error) {
	text := c.argsText()
	var obj map[string]any
	if err := json.Unmarshal([]byte(text), &obj); err != nil {
		return nil, fmt.Errorf("tool call %s: arguments are not a JSON object: %q", c.Name, text)
	}
	return json.RawMessage(text), nil
}

// LLMCall is one request the LLM surface received.
type LLMCall struct {
	Seq      int    `json:"seq"`
	Scenario string `json:"scenario"`
	// Format is openai.chat, openai.responses, anthropic.messages or genai,
	// or "" for a path no format serves.
	Format string `json:"format"`
	Stream bool   `json:"stream"`
	Method string `json:"method"`
	Path   string `json:"path"`
	Query  string `json:"query"`
	// Headers maps each canonical header name to its values joined with ", ".
	Headers map[string]string `json:"headers"`
	// Body is the raw body; JSON is the same body parsed, null when it is not
	// JSON.
	Body string          `json:"body"`
	JSON json.RawMessage `json:"json"`
	// Model is the body's model, or the model in a Gemini path.
	Model string `json:"model"`
	// Tools are the declared tool names in order; a tool with no name, such
	// as a provider's native one, appears as type:<type>.
	Tools []string `json:"tools"`
	// Turn is the index of the scripted turn served, -1 for the then turn,
	// and null when none was.
	Turn *int `json:"turn"`
	// Status is what the mock answered, 0 when it refused the connection.
	Status int `json:"status"`
}

// LLMScenarioState is one scenario's progress.
type LLMScenarioState struct {
	Pending int  `json:"pending"`
	Served  int  `json:"served"`
	Then    bool `json:"then"`
}

// LLMState is the LLM surface's readable state. It leaves out the request log,
// which carries whole tool lists and would make every control answer huge; the
// llm/requests verb reads it.
type LLMState struct {
	Models    []string                    `json:"models"`
	Scenarios map[string]LLMScenarioState `json:"scenarios"`
	// Unmarked counts model requests that carried no scenario marker.
	Unmarked int `json:"unmarked"`
	// Requests is the size of the request log.
	Requests int `json:"requests"`
}

// defaultLLMModels is what the model listings answer until set.
var defaultLLMModels = []string{"mock-model"}

// llmLogCap bounds the request log; a long run keeps the most recent requests.
const llmLogCap = 1000

// scenarioMarker finds a request's scenario key in its raw body.
var scenarioMarker = regexp.MustCompile(`\[scenario:([A-Za-z0-9_.-]{1,64})\]`)

// llmScenario is one scenario's script.
type llmScenario struct {
	turns  []LLMTurn
	then   *LLMTurn
	served int
}

// llmSurface holds one server's scripts, model list and request log.
type llmSurface struct {
	mu        sync.Mutex
	models    []string
	modelsSet bool
	scenarios map[string]*llmScenario
	calls     []LLMCall
	seq       int
	unmarked  int
}

// newLLMSurface returns an empty surface listing the default models.
func newLLMSurface() *llmSurface {
	return &llmSurface{models: append([]string{}, defaultLLMModels...), scenarios: map[string]*llmScenario{}}
}

// script replaces a scenario's turns and then turn and clears its log entries,
// so a rerun starts clean; append keeps both.
func (ls *llmSurface) script(key string, turns []LLMTurn, then *LLMTurn, replace bool) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	sc := ls.scenarios[key]
	if sc == nil || replace {
		sc = &llmScenario{}
		ls.scenarios[key] = sc
		ls.dropCalls(key)
	}
	sc.turns = append(sc.turns, turns...)
	if replace {
		sc.then = then
	}
}

// clear drops one scenario with its log entries, or every scenario and the
// whole log when key is empty.
func (ls *llmSurface) clear(key string) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if key == "" {
		ls.scenarios = map[string]*llmScenario{}
		ls.calls = nil
		ls.unmarked = 0
		return
	}
	delete(ls.scenarios, key)
	ls.dropCalls(key)
}

// dropCalls removes a scenario's log entries. Callers hold mu.
func (ls *llmSurface) dropCalls(key string) {
	kept := ls.calls[:0]
	for _, c := range ls.calls {
		if c.Scenario != key {
			kept = append(kept, c)
		}
	}
	ls.calls = kept
}

// setModels replaces the model listing.
func (ls *llmSurface) setModels(models []string) {
	ls.mu.Lock()
	ls.models = append([]string{}, models...)
	ls.modelsSet = true
	ls.mu.Unlock()
}

// modelList returns the model listing.
func (ls *llmSurface) modelList() []string {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	return append([]string{}, ls.models...)
}

// next takes the scenario's next turn, its index (-1 for the then turn) and
// its ordinal among the turns served, which numbers unnamed call ids so a
// repeated then turn still gives each call a distinct id. It reports why there
// is no turn when the scenario is unknown or used up.
func (ls *llmSurface) next(key string) (LLMTurn, int, int, string) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if key == "" {
		ls.unmarked++
		return LLMTurn{}, 0, 0, "the request has no [scenario:<key>] marker"
	}
	sc := ls.scenarios[key]
	if sc == nil {
		return LLMTurn{}, 0, 0, "scenario " + key + " is not scripted"
	}
	ordinal := sc.served
	if sc.served < len(sc.turns) {
		sc.served++
		return sc.turns[ordinal], ordinal, ordinal, ""
	}
	if sc.then != nil {
		sc.served++
		return *sc.then, -1, ordinal, ""
	}
	return LLMTurn{}, 0, 0, fmt.Sprintf("scenario %s has no turn left after %d", key, len(sc.turns))
}

// record appends a request to the log and returns the sequence number it
// gave it, which also numbers the reply's ids.
func (ls *llmSurface) record(c LLMCall) int {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	ls.seq++
	c.Seq = ls.seq
	ls.calls = append(ls.calls, c)
	if len(ls.calls) > llmLogCap {
		ls.calls = ls.calls[len(ls.calls)-llmLogCap:]
	}
	return ls.seq
}

// requests returns a scenario's log entries after seq since, oldest first;
// every entry when key is empty.
func (ls *llmSurface) requests(key string, since int) []LLMCall {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	out := []LLMCall{}
	for _, c := range ls.calls {
		if (key == "" || c.Scenario == key) && c.Seq > since {
			out = append(out, c)
		}
	}
	return out
}

// clearLog empties the request log, keeping scripts and models.
func (ls *llmSurface) clearLog() {
	ls.mu.Lock()
	ls.calls = nil
	ls.unmarked = 0
	ls.mu.Unlock()
}

// snapshot returns the readable state, or nil when the surface was never
// scripted, called or given models, so a server that never used it reads back
// without it.
func (ls *llmSurface) snapshot() *LLMState {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if len(ls.scenarios) == 0 && len(ls.calls) == 0 && !ls.modelsSet && ls.unmarked == 0 {
		return nil
	}
	st := &LLMState{Models: append([]string{}, ls.models...), Scenarios: map[string]LLMScenarioState{}, Unmarked: ls.unmarked, Requests: len(ls.calls)}
	for key, sc := range ls.scenarios {
		pending := len(sc.turns) - sc.served
		if pending < 0 {
			pending = 0
		}
		st.Scenarios[key] = LLMScenarioState{Pending: pending, Served: sc.served, Then: sc.then != nil}
	}
	return st
}

// The format keys a request is logged under.
const (
	formatChat      = "openai.chat"
	formatResponses = "openai.responses"
	formatMessages  = "anthropic.messages"
	formatGenAI     = "genai"
)

// llmRoute is what a path asks of the surface.
type llmRoute struct {
	format string
	list   bool // a model listing rather than a model request
	model  string
	stream bool // fixed by the path (Gemini); otherwise read from the body
}

// routeLLM resolves a method and path relative to /s/<name>/llm. The format
// is set even for an unknown path under a format's prefix, so the 404 can use
// that format's error shape.
func routeLLM(method, path string) (llmRoute, bool) {
	switch {
	case path == "/openai/v1/chat/completions" && method == http.MethodPost:
		return llmRoute{format: formatChat}, true
	case path == "/openai/v1/responses" && method == http.MethodPost:
		return llmRoute{format: formatResponses}, true
	case path == "/openai/v1/models" && method == http.MethodGet:
		return llmRoute{format: formatChat, list: true}, true
	case path == "/anthropic/v1/messages" && method == http.MethodPost:
		return llmRoute{format: formatMessages}, true
	case path == "/anthropic/v1/models" && method == http.MethodGet:
		return llmRoute{format: formatMessages, list: true}, true
	case path == "/genai/v1beta/models" && method == http.MethodGet:
		return llmRoute{format: formatGenAI, list: true}, true
	case strings.HasPrefix(path, "/genai/v1beta/models/") && method == http.MethodPost:
		model, action, ok := strings.Cut(strings.TrimPrefix(path, "/genai/v1beta/models/"), ":")
		if ok && model != "" && (action == "generateContent" || action == "streamGenerateContent") {
			return llmRoute{format: formatGenAI, model: model, stream: action == "streamGenerateContent"}, true
		}
	}
	for prefix, format := range map[string]string{"/openai/": formatChat, "/anthropic/": formatMessages, "/genai/": formatGenAI} {
		if strings.HasPrefix(path, prefix) {
			return llmRoute{format: format}, false
		}
	}
	return llmRoute{}, false
}

// ServeLLM answers one request on the LLM surface; path is relative to
// /s/<name>/llm. The server-wide faults apply as they do to the REST surface:
// only an unscoped set reaches it, and of that set the transport, HTTP and
// timing fields.
func (s *Server) ServeLLM(w http.ResponseWriter, r *http.Request, path string) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	route, known := routeLLM(r.Method, path)
	call := LLMCall{Format: route.format, Method: r.Method, Path: path, Query: r.URL.RawQuery, Headers: map[string]string{}, Body: string(body), Model: route.model}
	for name, values := range r.Header {
		call.Headers[name] = strings.Join(values, ", ")
	}
	var parsed map[string]any
	if json.Unmarshal(body, &parsed) == nil {
		call.JSON = json.RawMessage(body)
		if m, ok := parsed["model"].(string); ok && call.Model == "" {
			call.Model = m
		}
		call.Tools = declaredTools(route.format, parsed)
	} else {
		call.JSON = json.RawMessage("null")
	}
	if call.Tools == nil {
		call.Tools = []string{}
	}
	call.Stream = route.stream || parsed["stream"] == true
	if m := scenarioMarker.FindSubmatch(body); m != nil {
		call.Scenario = string(m[1])
	}

	f := s.faults.forRequest("")
	if f.Refuse || f.Status != 0 {
		if !f.Refuse {
			call.Status = f.Status
		}
		s.llm.record(call)
		s.failRequest(w, f)
		return
	}
	if d := time.Duration(f.Hang); d > 0 && !sleepOrDone(r, d) {
		return
	}
	if d := s.faults.delay(f); d > 0 && !sleepOrDone(r, d) {
		return
	}

	switch {
	case !known:
		call.Status = http.StatusNotFound
		s.llm.record(call)
		writeLLMError(w, route.format, LLMError{Status: http.StatusNotFound, Message: "chaosmock llm: no route for " + r.Method + " " + path, Type: "not_found_error"})
		return
	case route.list:
		call.Status = http.StatusOK
		s.llm.record(call)
		writeModelList(w, route.format, s.llm.modelList())
		return
	}

	turn, index, ordinal, why := s.llm.next(call.Scenario)
	if why != "" {
		call.Status = http.StatusBadRequest
		s.llm.record(call)
		writeLLMError(w, route.format, LLMError{Status: http.StatusBadRequest, Message: "chaosmock llm: " + why, Type: "chaosmock_unscripted"})
		return
	}
	call.Turn = &index
	if d := time.Duration(turn.Delay); d > 0 && !sleepOrDone(r, d) {
		call.Status = 0
		s.llm.record(call)
		return
	}
	switch {
	case turn.Raw != nil:
		call.Status = turn.Raw.Status
		if call.Status == 0 {
			call.Status = http.StatusOK
		}
		s.llm.record(call)
		if turn.Raw.ContentType != "" {
			w.Header().Set("Content-Type", turn.Raw.ContentType)
		}
		w.WriteHeader(call.Status)
		_, _ = io.WriteString(w, turn.Raw.Body)
		return
	case turn.Error != nil:
		call.Status = turn.Error.Status
		s.llm.record(call)
		writeLLMError(w, route.format, *turn.Error)
		return
	}
	if err := turn.renderable(route.format); err != nil {
		call.Status = http.StatusBadRequest
		s.llm.record(call)
		writeLLMError(w, route.format, LLMError{Status: http.StatusBadRequest, Message: "chaosmock llm: " + err.Error(), Type: "chaosmock_unscripted"})
		return
	}
	call.Status = http.StatusOK
	seq := s.llm.record(call)
	reply := llmReply{turn: turn, ordinal: ordinal, model: call.Model, id: seq, includeUsage: includeUsage(parsed)}
	if call.Stream {
		reply.stream(w, r, route.format)
		return
	}
	writeJSON(w, http.StatusOK, reply.body(route.format))
}

// renderable reports whether every call's arguments fit the format: the
// formats other than OpenAI's carry arguments as an object.
func (t LLMTurn) renderable(format string) error {
	if format == formatChat || format == formatResponses {
		return nil
	}
	for _, c := range t.ToolCalls {
		if _, err := c.argsObject(); err != nil {
			return err
		}
	}
	return nil
}

// sleepOrDone waits d and reports true, or reports false as soon as the client
// hangs up.
func sleepOrDone(r *http.Request, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-r.Context().Done():
		return false
	}
}

// includeUsage reports whether an OpenAI chat stream asked for a usage frame.
func includeUsage(body map[string]any) bool {
	opts, _ := body["stream_options"].(map[string]any)
	return opts["include_usage"] == true
}

// declaredTools lists the tool names a model request declares, in order. A
// tool without a name is listed as type:<type>, or type:<key> for a Gemini
// tool that is not a function declaration.
func declaredTools(format string, body map[string]any) []string {
	tools, _ := body["tools"].([]any)
	out := []string{}
	for _, t := range tools {
		tool, _ := t.(map[string]any)
		switch format {
		case formatChat:
			fn, _ := tool["function"].(map[string]any)
			if name, ok := fn["name"].(string); ok {
				out = append(out, name)
				continue
			}
		case formatGenAI:
			decls, ok := tool["functionDeclarations"].([]any)
			if ok {
				for _, d := range decls {
					decl, _ := d.(map[string]any)
					name, _ := decl["name"].(string)
					out = append(out, name)
				}
				continue
			}
			for key := range tool {
				out = append(out, "type:"+key)
			}
			continue
		default:
			if name, ok := tool["name"].(string); ok {
				out = append(out, name)
				continue
			}
		}
		typ, _ := tool["type"].(string)
		out = append(out, "type:"+typ)
	}
	return out
}

// writeModelList answers a format's model listing.
func writeModelList(w http.ResponseWriter, format string, models []string) {
	switch format {
	case formatMessages:
		data := []map[string]any{}
		for _, m := range models {
			data = append(data, map[string]any{"id": m, "type": "model", "display_name": m, "created_at": "2025-01-01T00:00:00Z"})
		}
		resp := map[string]any{"data": data, "has_more": false}
		if len(models) > 0 {
			resp["first_id"], resp["last_id"] = models[0], models[len(models)-1]
		}
		writeJSON(w, http.StatusOK, resp)
	case formatGenAI:
		list := []map[string]any{}
		for _, m := range models {
			list = append(list, map[string]any{"name": "models/" + m, "displayName": m, "supportedGenerationMethods": []string{"generateContent", "streamGenerateContent"}})
		}
		writeJSON(w, http.StatusOK, map[string]any{"models": list})
	default:
		data := []map[string]any{}
		for _, m := range models {
			data = append(data, map[string]any{"id": m, "object": "model", "created": 0, "owned_by": "chaosmock"})
		}
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
	}
}

// writeLLMError answers an error in the format's shape.
func writeLLMError(w http.ResponseWriter, format string, e LLMError) {
	if e.RetryAfter != "" {
		w.Header().Set("Retry-After", e.RetryAfter)
	}
	typ := e.Type
	if typ == "" {
		typ = "api_error"
	}
	switch format {
	case formatMessages:
		writeJSON(w, e.Status, map[string]any{"type": "error", "error": map[string]any{"type": typ, "message": e.Message}})
	case formatGenAI:
		status := e.Code
		if status == "" {
			status = strings.ToUpper(strings.ReplaceAll(http.StatusText(e.Status), " ", "_"))
		}
		writeJSON(w, e.Status, map[string]any{"error": map[string]any{"code": e.Status, "message": e.Message, "status": status}})
	default:
		var code any
		if e.Code != "" {
			code = e.Code
		}
		writeJSON(w, e.Status, map[string]any{"error": map[string]any{"message": e.Message, "type": typ, "code": code, "param": nil}})
	}
}

// llmReply renders one turn in a format.
type llmReply struct {
	turn         LLMTurn
	ordinal      int
	model        string
	id           int
	includeUsage bool
}

// halves splits text in two so a stream proves the client joins deltas.
func halves(text string) []string {
	if len(text) < 2 {
		return []string{text}
	}
	mid := len(text) / 2
	return []string{text[:mid], text[mid:]}
}

// body renders the non-streaming reply.
func (rp llmReply) body(format string) any {
	switch format {
	case formatResponses:
		return rp.responsesBody("completed")
	case formatMessages:
		return rp.messagesBody()
	case formatGenAI:
		return rp.genaiChunk(rp.turn.Text, true, true)
	default:
		return rp.chatBody()
	}
}

// chatBody is an OpenAI chat completion.
func (rp llmReply) chatBody() map[string]any {
	u := rp.turn.usage()
	msg := map[string]any{"role": "assistant", "content": nil}
	if rp.turn.Text != "" || len(rp.turn.ToolCalls) == 0 {
		msg["content"] = rp.turn.Text
	}
	if calls := rp.chatToolCalls(); len(calls) > 0 {
		msg["tool_calls"] = calls
	}
	return map[string]any{
		"id": fmt.Sprintf("chatcmpl-mock-%d", rp.id), "object": "chat.completion", "created": time.Now().Unix(), "model": rp.model,
		"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": rp.turn.finish()}},
		"usage":   map[string]any{"prompt_tokens": u.Input, "completion_tokens": u.Output, "total_tokens": u.Input + u.Output},
	}
}

// chatToolCalls are the turn's calls in OpenAI chat form.
func (rp llmReply) chatToolCalls() []any {
	out := []any{}
	for i, c := range rp.turn.ToolCalls {
		out = append(out, map[string]any{"id": c.callID(rp.ordinal, i), "type": "function", "function": map[string]any{"name": c.Name, "arguments": c.argsText()}})
	}
	return out
}

// responsesItems are the turn's output items in Responses form.
func (rp llmReply) responsesItems() []map[string]any {
	items := []map[string]any{}
	if rp.turn.Text != "" {
		items = append(items, map[string]any{
			"type": "message", "id": fmt.Sprintf("msg_mock_%d", rp.id), "status": "completed", "role": "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": rp.turn.Text, "annotations": []any{}}},
		})
	}
	for i, c := range rp.turn.ToolCalls {
		id := c.callID(rp.ordinal, i)
		items = append(items, map[string]any{"type": "function_call", "id": "fc_" + id, "call_id": id, "name": c.Name, "arguments": c.argsText(), "status": "completed"})
	}
	return items
}

// responsesBody is a Responses response with the given status.
func (rp llmReply) responsesBody(status string) map[string]any {
	u := rp.turn.usage()
	if rp.turn.finish() == "length" {
		status = "incomplete"
	}
	resp := map[string]any{
		"id": fmt.Sprintf("resp_mock_%d", rp.id), "object": "response", "created_at": time.Now().Unix(), "status": status, "model": rp.model,
		"output": rp.responsesItems(),
		"usage":  map[string]any{"input_tokens": u.Input, "output_tokens": u.Output, "total_tokens": u.Input + u.Output},
	}
	if status == "incomplete" {
		resp["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
	}
	return resp
}

// anthropicStop maps a finish reason to Anthropic's stop_reason.
func anthropicStop(finish string) string {
	switch finish {
	case "tool_calls":
		return "tool_use"
	case "length":
		return "max_tokens"
	}
	return "end_turn"
}

// messagesBlocks are the turn's content blocks in Anthropic form. Arguments
// were checked by renderable.
func (rp llmReply) messagesBlocks() []any {
	blocks := []any{}
	if rp.turn.Text != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": rp.turn.Text})
	}
	for i, c := range rp.turn.ToolCalls {
		args, _ := c.argsObject()
		blocks = append(blocks, map[string]any{"type": "tool_use", "id": c.callID(rp.ordinal, i), "name": c.Name, "input": args})
	}
	return blocks
}

// messagesBody is an Anthropic message.
func (rp llmReply) messagesBody() map[string]any {
	u := rp.turn.usage()
	return map[string]any{
		"id": fmt.Sprintf("msg_mock_%d", rp.id), "type": "message", "role": "assistant", "model": rp.model,
		"content": rp.messagesBlocks(), "stop_reason": anthropicStop(rp.turn.finish()), "stop_sequence": nil,
		"usage": map[string]any{"input_tokens": u.Input, "output_tokens": u.Output},
	}
}

// genaiChunk is a Gemini response holding text, plus the calls, finish reason
// and usage when last is set.
func (rp llmReply) genaiChunk(text string, withCalls, last bool) map[string]any {
	parts := []any{}
	if text != "" {
		parts = append(parts, map[string]any{"text": text})
	}
	if withCalls {
		for i, c := range rp.turn.ToolCalls {
			args, _ := c.argsObject()
			parts = append(parts, map[string]any{"functionCall": map[string]any{"id": c.callID(rp.ordinal, i), "name": c.Name, "args": args}})
		}
	}
	candidate := map[string]any{"content": map[string]any{"role": "model", "parts": parts}, "index": 0}
	chunk := map[string]any{"candidates": []any{candidate}, "modelVersion": rp.model, "responseId": fmt.Sprintf("resp_mock_%d", rp.id)}
	if last {
		u := rp.turn.usage()
		reason := "STOP"
		if rp.turn.finish() == "length" {
			reason = "MAX_TOKENS"
		}
		candidate["finishReason"] = reason
		chunk["usageMetadata"] = map[string]any{"promptTokenCount": u.Input, "candidatesTokenCount": u.Output, "totalTokenCount": u.Input + u.Output}
	}
	return chunk
}

// sseWriter writes server-sent events, flushing each and pacing them.
type sseWriter struct {
	w     http.ResponseWriter
	r     *http.Request
	gap   time.Duration
	first bool
	ok    bool
}

// newSSE starts an event stream.
func newSSE(w http.ResponseWriter, r *http.Request, gap time.Duration) *sseWriter {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	return &sseWriter{w: w, r: r, gap: gap, first: true, ok: true}
}

// send writes one event, with an event: line when event is set. It stops
// writing once the client has hung up.
func (sw *sseWriter) send(event string, data any) {
	if !sw.ok {
		return
	}
	if !sw.first && sw.gap > 0 && !sleepOrDone(sw.r, sw.gap) {
		sw.ok = false
		return
	}
	sw.first = false
	var payload string
	if s, isText := data.(string); isText {
		payload = s
	} else {
		raw, _ := json.Marshal(data)
		payload = string(raw)
	}
	var b strings.Builder
	if event != "" {
		b.WriteString("event: " + event + "\n")
	}
	b.WriteString("data: " + payload + "\n\n")
	if _, err := io.WriteString(sw.w, b.String()); err != nil {
		sw.ok = false
		return
	}
	if fl, isFlusher := sw.w.(http.Flusher); isFlusher {
		fl.Flush()
	}
}

// stream writes the reply as the format's event stream. Text and each
// arguments string go out as two deltas, so the stream tests that the client
// joins them.
func (rp llmReply) stream(w http.ResponseWriter, r *http.Request, format string) {
	sw := newSSE(w, r, time.Duration(rp.turn.ChunkDelay))
	switch format {
	case formatResponses:
		rp.streamResponses(sw)
	case formatMessages:
		rp.streamMessages(sw)
	case formatGenAI:
		rp.streamGenAI(sw)
	default:
		rp.streamChat(sw)
	}
}

// streamChat writes an OpenAI chat completion stream: a role delta, content or
// tool call deltas, the finish reason, the usage frame when asked for, and
// [DONE].
func (rp llmReply) streamChat(sw *sseWriter) {
	id := fmt.Sprintf("chatcmpl-mock-%d", rp.id)
	created := time.Now().Unix()
	chunk := func(delta map[string]any, finish any) map[string]any {
		return map[string]any{"id": id, "object": "chat.completion.chunk", "created": created, "model": rp.model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
	}
	sw.send("", chunk(map[string]any{"role": "assistant", "content": ""}, nil))
	if rp.turn.Text != "" {
		for _, part := range halves(rp.turn.Text) {
			sw.send("", chunk(map[string]any{"content": part}, nil))
		}
	}
	for i, c := range rp.turn.ToolCalls {
		sw.send("", chunk(map[string]any{"tool_calls": []any{map[string]any{"index": i, "id": c.callID(rp.ordinal, i), "type": "function",
			"function": map[string]any{"name": c.Name, "arguments": ""}}}}, nil))
		for _, part := range halves(c.argsText()) {
			sw.send("", chunk(map[string]any{"tool_calls": []any{map[string]any{"index": i, "function": map[string]any{"arguments": part}}}}, nil))
		}
	}
	sw.send("", chunk(map[string]any{}, rp.turn.finish()))
	if rp.includeUsage {
		u := rp.turn.usage()
		sw.send("", map[string]any{"id": id, "object": "chat.completion.chunk", "created": created, "model": rp.model, "choices": []any{},
			"usage": map[string]any{"prompt_tokens": u.Input, "completion_tokens": u.Output, "total_tokens": u.Input + u.Output}})
	}
	sw.send("", "[DONE]")
}

// streamResponses writes a Responses event stream. Every event carries an
// increasing sequence_number from 0.
func (rp llmReply) streamResponses(sw *sseWriter) {
	seq := 0
	emit := func(typ string, fields map[string]any) {
		fields["type"] = typ
		fields["sequence_number"] = seq
		seq++
		sw.send(typ, fields)
	}
	final := rp.responsesBody("completed")
	created := map[string]any{}
	for k, v := range final {
		created[k] = v
	}
	created["status"], created["output"] = "in_progress", []any{}
	delete(created, "usage")
	delete(created, "incomplete_details")
	emit("response.created", map[string]any{"response": created})
	for out, item := range rp.responsesItems() {
		itemID, _ := item["id"].(string)
		if item["type"] == "message" {
			emit("response.output_item.added", map[string]any{"output_index": out, "item": map[string]any{"type": "message", "id": itemID, "status": "in_progress", "role": "assistant", "content": []any{}}})
			emit("response.content_part.added", map[string]any{"item_id": itemID, "output_index": out, "content_index": 0, "part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}}})
			for _, part := range halves(rp.turn.Text) {
				emit("response.output_text.delta", map[string]any{"item_id": itemID, "output_index": out, "content_index": 0, "delta": part})
			}
			emit("response.output_text.done", map[string]any{"item_id": itemID, "output_index": out, "content_index": 0, "text": rp.turn.Text})
			emit("response.content_part.done", map[string]any{"item_id": itemID, "output_index": out, "content_index": 0, "part": map[string]any{"type": "output_text", "text": rp.turn.Text, "annotations": []any{}}})
			emit("response.output_item.done", map[string]any{"output_index": out, "item": item})
			continue
		}
		args, _ := item["arguments"].(string)
		added := map[string]any{}
		for k, v := range item {
			added[k] = v
		}
		added["arguments"], added["status"] = "", "in_progress"
		emit("response.output_item.added", map[string]any{"output_index": out, "item": added})
		for _, part := range halves(args) {
			emit("response.function_call_arguments.delta", map[string]any{"item_id": itemID, "output_index": out, "delta": part})
		}
		emit("response.function_call_arguments.done", map[string]any{"item_id": itemID, "output_index": out, "arguments": args})
		emit("response.output_item.done", map[string]any{"output_index": out, "item": item})
	}
	if final["status"] == "incomplete" {
		emit("response.incomplete", map[string]any{"response": final})
		return
	}
	emit("response.completed", map[string]any{"response": final})
}

// streamMessages writes an Anthropic event stream: message_start, each
// content block with its deltas, message_delta with the stop reason and
// output usage, and message_stop.
func (rp llmReply) streamMessages(sw *sseWriter) {
	u := rp.turn.usage()
	sw.send("message_start", map[string]any{"type": "message_start", "message": map[string]any{
		"id": fmt.Sprintf("msg_mock_%d", rp.id), "type": "message", "role": "assistant", "model": rp.model, "content": []any{},
		"stop_reason": nil, "stop_sequence": nil, "usage": map[string]any{"input_tokens": u.Input, "output_tokens": 0},
	}})
	index := 0
	if rp.turn.Text != "" {
		sw.send("content_block_start", map[string]any{"type": "content_block_start", "index": index, "content_block": map[string]any{"type": "text", "text": ""}})
		for _, part := range halves(rp.turn.Text) {
			sw.send("content_block_delta", map[string]any{"type": "content_block_delta", "index": index, "delta": map[string]any{"type": "text_delta", "text": part}})
		}
		sw.send("content_block_stop", map[string]any{"type": "content_block_stop", "index": index})
		index++
	}
	for i, c := range rp.turn.ToolCalls {
		args, _ := c.argsObject()
		sw.send("content_block_start", map[string]any{"type": "content_block_start", "index": index, "content_block": map[string]any{"type": "tool_use", "id": c.callID(rp.ordinal, i), "name": c.Name, "input": map[string]any{}}})
		for _, part := range halves(string(args)) {
			sw.send("content_block_delta", map[string]any{"type": "content_block_delta", "index": index, "delta": map[string]any{"type": "input_json_delta", "partial_json": part}})
		}
		sw.send("content_block_stop", map[string]any{"type": "content_block_stop", "index": index})
		index++
	}
	sw.send("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": anthropicStop(rp.turn.finish()), "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": u.Output}})
	sw.send("message_stop", map[string]any{"type": "message_stop"})
}

// streamGenAI writes a Gemini stream: the first half of the text, then a last
// chunk with the rest of the text, the calls, the finish reason and usage.
func (rp llmReply) streamGenAI(sw *sseWriter) {
	parts := halves(rp.turn.Text)
	if rp.turn.Text != "" && len(parts) == 2 {
		sw.send("", rp.genaiChunk(parts[0], false, false))
		sw.send("", rp.genaiChunk(parts[1], true, true))
		return
	}
	sw.send("", rp.genaiChunk(rp.turn.Text, true, true))
}

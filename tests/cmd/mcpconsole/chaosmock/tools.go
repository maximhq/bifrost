package chaosmock

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

// Tool is one tool a virtual server advertises. The zero InputSchema is filled
// with a permissive object schema at registration, so scenarios that only care
// about a tool's presence do not have to spell one out.
//
// Name, Description, InputSchema and Annotations go on the wire in tools/list.
// The rest are instructions to the mock itself: they show in the control API's
// readback but never reach a client, which would otherwise see fields no real
// server sends.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`

	// Annotations is sent verbatim, so a scenario can mark a tool read-only or
	// destructive, or send a hint a client does not know yet.
	Annotations json.RawMessage `json:"annotations,omitempty"`

	// Reply is the CallToolResult tools/call returns for this tool when
	// nothing is faulted, sent verbatim so a scenario can return several
	// content blocks, structuredContent or isError. Empty means an echo of
	// the arguments, which is enough to prove which tool actually ran.
	Reply json.RawMessage `json:"reply,omitempty"`

	// ReplyEnv answers with the value of this variable in the environment
	// the stdio bridge registered, which is how a scenario proves what
	// environment the system under test spawned its subprocess with.
	ReplyEnv string `json:"reply_env,omitempty"`

	// Fault, when non-nil, replaces the server-wide fault set for calls to
	// this tool. It is how a scenario breaks one tool while leaving the rest
	// of the server healthy, which no server-level knob can express.
	Fault *Faults `json:"fault,omitempty"`
}

// wireTool is the part of a Tool that tools/list sends.
type wireTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations json.RawMessage `json:"annotations,omitempty"`
}

// wire returns the client-visible view of t.
func (t Tool) wire() wireTool {
	return wireTool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema, Annotations: t.Annotations}
}

// Validate rejects a definition that would otherwise misbehave quietly:
// annotations or a reply that is not a JSON object, two reply sources, or a
// per-tool fault field that cannot apply to one tool's call. In a chaos harness
// a silently ignored field looks exactly like the client coping.
func (t Tool) Validate() error {
	if !isObjectOrEmpty(t.Annotations) {
		return fmt.Errorf("tool %q: annotations must be a JSON object", t.Name)
	}
	if !isObjectOrEmpty(t.Reply) {
		return fmt.Errorf("tool %q: reply must be a JSON object (a CallToolResult)", t.Name)
	}
	if len(t.Reply) > 0 && t.ReplyEnv != "" {
		return fmt.Errorf("tool %q: reply and reply_env are exclusive", t.Name)
	}
	if t.Fault != nil {
		if err := t.Fault.validateForTool(); err != nil {
			return fmt.Errorf("tool %q: %w", t.Name, err)
		}
	}
	return nil
}

// isObjectOrEmpty reports whether raw is absent, null or a JSON object. Callers
// only pass bytes the JSON decoder already accepted, so the first byte decides.
func isObjectOrEmpty(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || trimmed[0] == '{' || string(trimmed) == "null"
}

// defaultInputSchema accepts anything. Tools that need to reject input define
// their own; most scenarios care about dispatch, not validation.
var defaultInputSchema = json.RawMessage(`{"type":"object","properties":{},"additionalProperties":true}`)

// toolSet is the mutable tool list of one virtual server, plus the per-identity
// overlays that let the same server show different tools to different callers.
// That is what makes per-user auth testable for real rather than simulated: two
// identities hitting one server legitimately see two tool lists.
type toolSet struct {
	mu sync.RWMutex

	base map[string]*Tool

	// overlays[identity] replaces the base set wholesale for that identity.
	// Wholesale rather than merged because "this user sees a narrower list"
	// and "this user sees a different list" are both real upstream behaviours,
	// and a merge semantics could only express the first.
	overlays map[string]map[string]*Tool

	// revision increments on every mutation. It is the mock's own answer to
	// "has the tool set changed since you last looked", independent of any
	// hashing Bifrost does, so a test can tell a genuine change from a
	// re-listing that happened to produce equal content.
	revision uint64
}

func newToolSet() *toolSet {
	return &toolSet{
		base:     map[string]*Tool{},
		overlays: map[string]map[string]*Tool{},
	}
}

// normalize fills defaults so callers can register a bare name.
func normalize(t Tool) *Tool {
	// An explicit null is treated as absent, so it neither fails validation
	// nor reaches the wire as a null a client has to cope with.
	if string(bytes.TrimSpace(t.Annotations)) == "null" {
		t.Annotations = nil
	}
	if string(bytes.TrimSpace(t.Reply)) == "null" {
		t.Reply = nil
	}
	if len(t.InputSchema) == 0 {
		t.InputSchema = defaultInputSchema
	}
	if t.Description == "" {
		t.Description = fmt.Sprintf("chaosmock tool %s", t.Name)
	}
	return &t
}

// Set replaces the whole base tool set.
func (ts *toolSet) Set(tools []Tool) uint64 {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.base = make(map[string]*Tool, len(tools))
	for _, t := range tools {
		ts.base[t.Name] = normalize(t)
	}
	ts.revision++
	return ts.revision
}

// Add registers or replaces one tool in the base set.
func (ts *toolSet) Add(t Tool) uint64 {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.base[t.Name] = normalize(t)
	ts.revision++
	return ts.revision
}

// Remove drops a tool from the base set. Removing an absent tool still bumps
// the revision: a scenario that removes the same tool twice should not silently
// look like it did nothing the second time.
func (ts *toolSet) Remove(name string) uint64 {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	delete(ts.base, name)
	ts.revision++
	return ts.revision
}

// Rename moves a tool to a new name, keeping its schema, reply and fault. This
// is deliberately not remove-plus-add: a rename is the case where a client's
// name mapping can go stale while the underlying tool is unchanged, and
// preserving identity is what makes that observable.
func (ts *toolSet) Rename(from, to string) (uint64, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	t, ok := ts.base[from]
	if !ok {
		return ts.revision, fmt.Errorf("tool %q not found", from)
	}
	renamed := *t
	renamed.Name = to
	delete(ts.base, from)
	ts.base[to] = &renamed
	ts.revision++
	return ts.revision, nil
}

// Describe changes only a tool's description, leaving its schema alone. A
// description-only change is the cheapest possible tool-set mutation and the
// one most likely to be missed by a content hash that ignores it.
func (ts *toolSet) Describe(name, description string) (uint64, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	t, ok := ts.base[name]
	if !ok {
		return ts.revision, fmt.Errorf("tool %q not found", name)
	}
	t.Description = description
	ts.revision++
	return ts.revision, nil
}

// Schema replaces a tool's input schema, leaving its description alone.
func (ts *toolSet) Schema(name string, schema json.RawMessage) (uint64, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	t, ok := ts.base[name]
	if !ok {
		return ts.revision, fmt.Errorf("tool %q not found", name)
	}
	if len(schema) == 0 {
		schema = defaultInputSchema
	}
	t.InputSchema = schema
	ts.revision++
	return ts.revision, nil
}

// SetOverlay gives one identity its own tool list. A nil tools slice clears the
// overlay and returns that identity to the base set.
func (ts *toolSet) SetOverlay(identity string, tools []Tool) uint64 {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if tools == nil {
		delete(ts.overlays, identity)
	} else {
		m := make(map[string]*Tool, len(tools))
		for _, t := range tools {
			m[t.Name] = normalize(t)
		}
		ts.overlays[identity] = m
	}
	ts.revision++
	return ts.revision
}

// view returns the tool map identity sees. Callers hold ts.mu.
func (ts *toolSet) view(identity string) map[string]*Tool {
	if identity != "" {
		if o, ok := ts.overlays[identity]; ok {
			return o
		}
	}
	return ts.base
}

// List returns the tools visible to identity, sorted by name so a client that
// diffs successive listings sees changes rather than map ordering.
func (ts *toolSet) List(identity string) []Tool {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	src := ts.view(identity)
	out := make([]Tool, 0, len(src))
	for _, t := range src {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Lookup resolves a tool by the name a caller asked for, in that identity's
// view. The bool distinguishes "no such tool" from a tool that exists but is
// faulted, which are different assertions.
func (ts *toolSet) Lookup(identity, name string) (Tool, bool) {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	t, ok := ts.view(identity)[name]
	if !ok {
		return Tool{}, false
	}
	return *t, true
}

// callFault returns the fault a call to name in identity's view is answered
// with, consuming its one-shot status the way the server-wide set does. The
// revision does not move: a fault firing is not a change to the tool set. The
// consumed fault is replaced rather than edited, since readers outside the lock
// may still hold the old pointer.
func (ts *toolSet) callFault(identity, name string) (Faults, bool) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	t, ok := ts.view(identity)[name]
	if !ok || t.Fault == nil {
		return Faults{}, false
	}
	f := *t.Fault
	if f.StatusOnce && f.Status != 0 {
		consumed := f
		consumed.Status, consumed.StatusOnce = 0, false
		t.Fault = &consumed
	}
	return f, true
}

// Revision reports the current tool-set revision.
func (ts *toolSet) Revision() uint64 {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return ts.revision
}

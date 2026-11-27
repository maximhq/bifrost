// Package toolsearch runs a tool search inside the gateway for requests that defer
// tools (defer_loading) on a provider with no tool search of its own.
//
// OpenAI gpt-5.4+ and Claude search deferred tools natively, and their converters
// provision that search. Every other provider either rejects defer_loading or ignores
// it and loads every tool up front. For those, core injects a bifrost_tool_search
// function through its provider-injected tool loop and holds the deferred tools back.
// When the model calls the search, the Session finds the matching deferred tools and
// loads them into the next turn's request, so the caller sees one response.
//
// State is rebuilt from each request: a deferred tool the conversation already called
// is loaded from the start, so a follow-up turn can call it again.
package toolsearch

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

const (
	// ToolName is the function the model calls to search the deferred tools.
	ToolName = "bifrost_tool_search"
	// MaxTurns bounds the model calls one attempt can make while searching.
	MaxTurns = 3
	// MaxHistoryLoaded bounds the tools the client's own earlier searches start out loaded,
	// the same bound as one gateway search.
	MaxHistoryLoaded = maxLimit

	defaultLimit    = 5
	maxLimit        = 20
	maxListedTools  = 200
	maxListedLength = 120

	// namespaceSeparator matches the separator providerUtils.FlattenResponsesNamespaceTools joins names with.
	namespaceSeparator = "__"
)

// Candidate reports whether req defers function tools, at the top level or inside a
// namespace, without a client search function of its own. A tool_search tool does not
// count as a search here: withGatewayToolSearch only runs this on providers with no
// tool search, where nothing can serve it (Codex declares one on every model). Requests
// backed by provider-side state are never candidates, and their deferred tools go out
// eagerly instead: a previous_response_id continuation sends only new input, so the
// session could not see which deferred tools were already loaded, and a conversation
// would store each internal search turn and replay it on the next one. It does not
// allocate, so it is cheap enough for every request.
func Candidate(req *schemas.BifrostResponsesRequest) bool {
	candidate, _ := Inspect(req)
	return candidate
}

// Inspect returns Candidate and whether req declares a tool_search tool, in the same
// single pass over the tools. A request that declares one may also defer tools in
// additional_tools input items, which the caller hoists before deciding.
func Inspect(req *schemas.BifrostResponsesRequest) (candidate, declaresToolSearch bool) {
	if req == nil || req.Params == nil || len(req.Params.Tools) == 0 ||
		req.Params.PreviousResponseID != nil || req.Params.Conversation != nil {
		return false, false
	}
	hasDeferred := false
	for i := range req.Params.Tools {
		tool := &req.Params.Tools[i]
		switch tool.Type {
		case schemas.ResponsesToolTypeFunction:
			if isDeferred(tool) {
				hasDeferred = true
			}
		case schemas.ResponsesToolTypeToolSearch:
			declaresToolSearch = true
		case schemas.ResponsesToolTypeNamespace:
			if schemas.DefersLoading(tool) {
				hasDeferred = true
			}
		}
	}
	return hasDeferred && schemas.ClientToolSearchFunctionName(req.Params.Tools, req.Input) == "", declaresToolSearch
}

// NativeSupport reports whether provider serves model with a tool search of its own,
// matching the gates the provider converters apply: Claude where the host supports
// Anthropic's tool search, OpenAI-family wires on gpt-5.4+.
func NativeSupport(provider schemas.ModelProvider, model string) bool {
	if schemas.IsAnthropicModel(model) {
		return anthropic.SupportsToolSearch(provider, model)
	}
	return openai.SupportsToolSearch(provider, model)
}

// Session is the gateway-run search for one attempt. Its methods are safe for
// concurrent use: the injected tool loop runs a turn's calls in parallel.
type Session struct {
	deferred []schemas.ResponsesTool // searchable copies, named by toolKey

	mu     sync.RWMutex
	loaded map[string]bool
}

// NewSession holds back req's deferred function tools, including namespace members.
// A deferred tool the conversation already called, or that tool_choice names, starts
// out loaded. Namespace members are searched and loaded under toolKey, the name the
// model sees once the namespace is flattened for the wire.
func NewSession(req *schemas.BifrostResponsesRequest) *Session {
	s := &Session{loaded: make(map[string]bool)}
	for _, tool := range req.Params.Tools {
		switch tool.Type {
		case schemas.ResponsesToolTypeFunction:
			if isDeferred(&tool) && tool.Name != nil {
				s.deferred = append(s.deferred, tool)
			}
		case schemas.ResponsesToolTypeNamespace:
			if tool.Name == nil || tool.ResponsesToolNamespace == nil {
				continue
			}
			for _, member := range tool.ResponsesToolNamespace.Tools {
				if member.Type != schemas.ResponsesToolTypeFunction || member.Name == nil || !schemas.IsDeferredNamespaceMember(&tool, &member) {
					continue
				}
				member.Name = schemas.Ptr(toolKey(*tool.Name, *member.Name))
				member.Description = searchDescription(member.Description, tool.Description)
				s.deferred = append(s.deferred, member)
			}
		}
	}
	var names []string // bare names, resolved as a tool_choice name is
	if choice := req.Params.ToolChoice; choice != nil && choice.ResponsesToolChoiceStruct != nil {
		names = chosenNames(choice.ResponsesToolChoiceStruct)
	}
	for i := range req.Input {
		msg := &req.Input[i]
		if msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeFunctionCall &&
			msg.ResponsesToolMessage != nil && msg.Name != nil {
			if msg.Namespace != nil && *msg.Namespace != "" {
				s.loaded[toolKey(*msg.Namespace, *msg.Name)] = true
			} else {
				names = append(names, *msg.Name)
			}
		}
	}
	if len(names) > 0 {
		s.loadNames(req.Params.Tools, names)
	}
	s.loadClientSearchOutputs(req.Input)
	return s
}

// searchDescription is the text a deferred namespace member is searched by: its own
// description, then the namespace's, as the flattened declaration carries both. The
// member's comes first so the listing in Tool, which shows a first line, stays the
// member's own. It is the searchable copy only; declarations go out as the caller sent
// them.
func searchDescription(member, namespace *string) *string {
	switch {
	case namespace == nil || *namespace == "":
		return member
	case member == nil || *member == "":
		return namespace
	default:
		return schemas.Ptr(*member + "\n\n" + *namespace)
	}
}

// chosenNames returns the function names a tool_choice pins, which need declaring
// from the first turn.
func chosenNames(pinned *schemas.ResponsesToolChoiceStruct) []string {
	var names []string
	if pinned.Type == schemas.ResponsesToolChoiceTypeFunction && pinned.Name != nil {
		names = append(names, *pinned.Name)
	}
	for _, allowed := range pinned.Tools {
		if allowed.Type == string(schemas.ResponsesToolTypeFunction) && allowed.Name != nil {
			names = append(names, *allowed.Name)
		}
	}
	return names
}

// loadNames loads the deferred functions that bare names refer to: a tool_choice's
// names, and history calls that carry no namespace. It resolves them as the namespace
// flattening resolves the choice: a name is loaded as given, and a bare name that no
// top-level function carries also loads the deferred namespace members of that name.
// A name several namespaces share loads them all, so the flattening step rejects a
// choice as ambiguous rather than the upstream rejecting a function it was never sent.
func (s *Session) loadNames(tools []schemas.ResponsesTool, names []string) {
	topLevel := make(map[string]bool, len(tools))
	members := make(map[string][]string) // bare member name -> toolKeys of deferred members
	for i := range tools {
		tool := &tools[i]
		switch {
		case tool.Type == schemas.ResponsesToolTypeFunction && tool.Name != nil:
			topLevel[*tool.Name] = true
		case tool.Type == schemas.ResponsesToolTypeNamespace && tool.Name != nil && tool.ResponsesToolNamespace != nil:
			for j := range tool.ResponsesToolNamespace.Tools {
				member := &tool.ResponsesToolNamespace.Tools[j]
				if member.Type != schemas.ResponsesToolTypeFunction || member.Name == nil {
					continue
				}
				key := toolKey(*tool.Name, *member.Name)
				if key == *member.Name {
					topLevel[key] = true // the default namespace is unwrapped to the top level
				} else if schemas.IsDeferredNamespaceMember(tool, member) {
					members[*member.Name] = append(members[*member.Name], key)
				}
			}
		}
	}
	for _, name := range names {
		s.loaded[name] = true
		if !topLevel[name] {
			for _, key := range members[name] {
				s.loaded[key] = true
			}
		}
	}
}

// loadClientSearchOutputs loads the tools the caller's own earlier searches loaded
// (client tool_search_output items), newest search first, until MaxHistoryLoaded are
// loaded. The client treats them as available, so the model sees them without searching
// again; the cap keeps a long history of searches from loading every tool, and tools
// past it stay searchable. Only function entries load, since the session holds back
// only functions (a custom tool of the same name is another tool). Only tools the
// session holds back count against the cap: a
// tool the conversation called is loaded already (NewSession marks those first), and
// an eager or undeclared tool a search output lists was never held back.
func (s *Session) loadClientSearchOutputs(input []schemas.ResponsesMessage) {
	var catalog map[string]bool // toolKeys of the deferred tools, built on the first output
	count := 0
	for i := len(input) - 1; i >= 0 && count < MaxHistoryLoaded; i-- {
		msg := &input[i]
		if msg.Type == nil || *msg.Type != schemas.ResponsesMessageTypeToolSearchOutput ||
			!msg.IsClientToolSearchItem() || len(msg.ToolSearchOutputTools) == 0 {
			continue
		}
		var tools []schemas.ResponsesTool
		if schemas.Unmarshal(msg.ToolSearchOutputTools, &tools) != nil {
			continue
		}
		if catalog == nil {
			catalog = make(map[string]bool, len(s.deferred))
			for j := range s.deferred {
				catalog[*s.deferred[j].Name] = true
			}
		}
		for _, tool := range tools {
			if tool.Name == nil {
				continue
			}
			if tool.Type == schemas.ResponsesToolTypeFunction {
				count += s.loadOnce(catalog, *tool.Name, count)
				continue
			}
			if tool.Type != schemas.ResponsesToolTypeNamespace || tool.ResponsesToolNamespace == nil {
				continue
			}
			for _, member := range tool.ResponsesToolNamespace.Tools {
				if member.Type == schemas.ResponsesToolTypeFunction && member.Name != nil {
					count += s.loadOnce(catalog, toolKey(*tool.Name, *member.Name), count)
				}
			}
		}
	}
}

// loadOnce loads key when it is a deferred tool (in catalog) not loaded yet and count
// has not reached the cap, and returns how many tools it loaded.
func (s *Session) loadOnce(catalog map[string]bool, key string, count int) int {
	if count >= MaxHistoryLoaded || !catalog[key] || s.loaded[key] {
		return 0
	}
	s.loaded[key] = true
	return 1
}

// FilterTool returns the form of a caller's tool the next turn sends, and false for a
// tool the next turn leaves out. A loaded deferred tool goes out eagerly; one not
// loaded yet stays out. A namespace keeps its eager and loaded members and is left out
// when none remain. A tool_search tool is left out too: the gateway serves the search.
func (s *Session) FilterTool(tool schemas.ResponsesTool) (schemas.ResponsesTool, bool) {
	switch tool.Type {
	case schemas.ResponsesToolTypeToolSearch:
		return tool, false
	case schemas.ResponsesToolTypeNamespace:
		return s.filterNamespace(tool)
	}
	if tool.Type != schemas.ResponsesToolTypeFunction || !isDeferred(&tool) {
		return tool, true
	}
	if tool.Name == nil {
		return tool, false
	}
	loaded := s.isLoaded(*tool.Name)
	tool.DeferLoading = nil
	return tool, loaded
}

// filterNamespace is FilterTool for a namespace. A namespace with nothing deferred is
// returned as is, without allocating.
func (s *Session) filterNamespace(tool schemas.ResponsesTool) (schemas.ResponsesTool, bool) {
	if tool.Name == nil || tool.ResponsesToolNamespace == nil {
		return tool, true
	}
	members := tool.ResponsesToolNamespace.Tools
	if !isDeferred(&tool) && !slices.ContainsFunc(members, func(m schemas.ResponsesTool) bool { return isDeferred(&m) }) {
		return tool, true
	}
	kept := make([]schemas.ResponsesTool, 0, len(members))
	for _, member := range members {
		if member.Type == schemas.ResponsesToolTypeFunction && schemas.IsDeferredNamespaceMember(&tool, &member) {
			if member.Name == nil || !s.isLoaded(toolKey(*tool.Name, *member.Name)) {
				continue
			}
		}
		// The namespace goes out eagerly below, so a member left undeferred here stays eager.
		member.DeferLoading = nil
		kept = append(kept, member)
	}
	if len(kept) == 0 {
		return tool, false
	}
	namespace := *tool.ResponsesToolNamespace
	namespace.Tools = kept
	tool.ResponsesToolNamespace = &namespace
	tool.DeferLoading = nil
	return tool, true
}

func (s *Session) isLoaded(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loaded[name]
}

// Tool is the bifrost_tool_search function. Its description names the deferred
// tools, as OpenAI's own tool search shows a deferred tool's name and description
// while holding back its parameters.
func (s *Session) Tool() schemas.ChatTool {
	var b strings.Builder
	b.WriteString("Search the tools that are available but not loaded yet, and load the matches so they can be called. ")
	b.WriteString(`Pass keywords describing the capability you need, or "select:<name>[,<name>...]" to load tools by exact name. `)
	b.WriteString("Tools not loaded yet:")
	for i, tool := range s.deferred {
		if i == maxListedTools {
			fmt.Fprintf(&b, "\n- ... and %d more", len(s.deferred)-maxListedTools)
			break
		}
		b.WriteString("\n- ")
		b.WriteString(*tool.Name)
		if tool.Description != nil && *tool.Description != "" {
			b.WriteString(": ")
			b.WriteString(firstLine(*tool.Description, maxListedLength))
		}
	}
	return schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        ToolName,
			Description: schemas.Ptr(b.String()),
			Parameters: &schemas.ToolFunctionParameters{
				Type: "object",
				Properties: schemas.NewOrderedMapFromPairs(
					schemas.KV("query", map[string]any{"type": "string", "description": "Keywords for the capability needed, or select:<name>[,<name>...]"}),
					schemas.KV("limit", map[string]any{"type": "integer", "description": fmt.Sprintf("Most tools to load (default %d)", defaultLimit)}),
				),
				Required: []string{"query"},
			},
		},
	}
}

// Execute runs one bifrost_tool_search call, loads the matching tools for the next
// turn and returns the tool result that reports them.
func (s *Session) Execute(call schemas.ChatAssistantMessageToolCall) *schemas.ChatMessage {
	query, limit := parseArguments(call.Function.Arguments)
	matches := search(s.deferred, query, limit)
	var text string
	if len(matches) == 0 {
		text = fmt.Sprintf("No tool matched %q. Search again with other keywords, or select a tool by exact name.", query)
	} else {
		names := make([]string, 0, len(matches))
		s.mu.Lock()
		for _, tool := range matches {
			s.loaded[*tool.Name] = true
			names = append(names, *tool.Name)
		}
		s.mu.Unlock()
		text = fmt.Sprintf("Loaded %d tool(s): %s. They can be called now.", len(names), strings.Join(names, ", "))
	}
	return &schemas.ChatMessage{
		Role:            schemas.ChatMessageRoleTool,
		Content:         &schemas.ChatMessageContent{ContentStr: &text},
		ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: call.ID},
	}
}

func isDeferred(tool *schemas.ResponsesTool) bool {
	return tool.DeferLoading != nil && *tool.DeferLoading
}

// toolKey is the name a tool is searched and loaded under: a namespace member's
// "<namespace>__<function>", matching the flat name the wire carries, and a top-level
// or default-namespace ("functions") tool's bare name.
func toolKey(namespace, name string) string {
	if namespace == "" || namespace == providerUtils.DefaultToolNamespace {
		return name
	}
	return namespace + namespaceSeparator + name
}

func firstLine(s string, limit int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if len(s) > limit {
		s = strings.TrimSpace(s[:limit]) + "..."
	}
	return s
}

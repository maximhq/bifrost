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
	"strings"
	"sync"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
)

const (
	// ToolName is the function the model calls to search the deferred tools.
	ToolName = "bifrost_tool_search"
	// MaxTurns bounds the model calls one attempt can make while searching.
	MaxTurns = 3

	defaultLimit    = 5
	maxLimit        = 20
	maxListedTools  = 200
	maxListedLength = 120
)

// Candidate reports whether req defers function tools without bringing any search of
// its own: no tool_search tool, no namespace, and no client search function. Requests
// backed by provider-side state are never candidates, and their deferred tools go out
// eagerly instead: a previous_response_id continuation sends only new input, so the
// session could not see which deferred tools were already loaded, and a conversation
// would store each internal search turn and replay it on the next one. It does not
// allocate, so it is cheap enough for every request.
func Candidate(req *schemas.BifrostResponsesRequest) bool {
	if req == nil || req.Params == nil || len(req.Params.Tools) == 0 ||
		req.Params.PreviousResponseID != nil || req.Params.Conversation != nil {
		return false
	}
	hasDeferred := false
	for i := range req.Params.Tools {
		tool := &req.Params.Tools[i]
		switch tool.Type {
		case schemas.ResponsesToolTypeToolSearch, schemas.ResponsesToolTypeNamespace:
			return false
		case schemas.ResponsesToolTypeFunction:
			if isDeferred(tool) {
				hasDeferred = true
			}
		}
	}
	return hasDeferred && schemas.ClientToolSearchFunctionName(req.Params.Tools, req.Input) == ""
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
	deferred []schemas.ResponsesTool

	mu     sync.RWMutex
	loaded map[string]bool
}

// NewSession holds back req's deferred function tools. A deferred tool the
// conversation already called, or that tool_choice names, starts out loaded.
func NewSession(req *schemas.BifrostResponsesRequest) *Session {
	s := &Session{loaded: make(map[string]bool)}
	for _, tool := range req.Params.Tools {
		if tool.Type == schemas.ResponsesToolTypeFunction && isDeferred(&tool) && tool.Name != nil {
			s.deferred = append(s.deferred, tool)
		}
	}
	for i := range req.Input {
		msg := &req.Input[i]
		if msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeFunctionCall &&
			msg.ResponsesToolMessage != nil && msg.Name != nil {
			s.loaded[*msg.Name] = true
		}
	}
	// A choice that pins a deferred function needs it declared from the first turn.
	if choice := req.Params.ToolChoice; choice != nil && choice.ResponsesToolChoiceStruct != nil {
		pinned := choice.ResponsesToolChoiceStruct
		if pinned.Type == schemas.ResponsesToolChoiceTypeFunction && pinned.Name != nil {
			s.loaded[*pinned.Name] = true
		}
		for _, allowed := range pinned.Tools {
			if allowed.Type == string(schemas.ResponsesToolTypeFunction) && allowed.Name != nil {
				s.loaded[*allowed.Name] = true
			}
		}
	}
	return s
}

// FilterTool returns the form of a caller's tool the next turn sends, and false for a
// deferred tool that is not loaded yet. A loaded tool goes out eagerly.
func (s *Session) FilterTool(tool schemas.ResponsesTool) (schemas.ResponsesTool, bool) {
	if tool.Type != schemas.ResponsesToolTypeFunction || !isDeferred(&tool) {
		return tool, true
	}
	if tool.Name == nil {
		return tool, false
	}
	s.mu.RLock()
	loaded := s.loaded[*tool.Name]
	s.mu.RUnlock()
	tool.DeferLoading = nil
	return tool, loaded
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

package warp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// Warp's agent loop: ask the model, run whatever tools it asks for, feed the
// results back, repeat until it answers or runs out of iterations.
//
// The loop emits Event values onto a channel rather than writing SSE
// directly. That is what lets the streaming and non-streaming endpoints share
// one implementation: the SSE path formats each event into a frame, the JSON
// path drains the same channel and assembles a single body. It also means the
// loop can be tested without a socket.

// eventType names the frames the client can receive.
type eventType string

const (
	EventStart         eventType = "start"
	EventDelta         eventType = "delta"
	EventToolCallStart eventType = "tool_call_start"
	EventToolCallEnd   eventType = "tool_call_end"
	EventError         eventType = "error"
	EventDone          eventType = "done"
)

// Error codes carried on EventError. The client branches on these, so they
// are part of the contract and must not be reworded casually.
const (
	ErrNotConfigured = "not_configured"
	ErrUpstream      = "upstream_error"
	ErrToolFailed    = "tool_error"
	ErrMaxIterations = "max_iterations"
	ErrTimeout       = "timeout"
	ErrCancelled     = "cancelled"
)

type Event struct {
	Type eventType `json:"type"`
	// Delta carries an assistant text fragment.
	Delta string `json:"delta,omitempty"`
	// Tool call fields.
	ToolID    string `json:"tool_id,omitempty"`
	ToolName  string `json:"tool_name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Iteration int    `json:"iteration,omitempty"`
	// DurationMs and Failed describe a finished tool call. The result payload is
	// deliberately absent: the model consumed it, and the UI only shows a chip.
	// Echoing it would double the transcript's size for no reader benefit.
	DurationMs int64  `json:"duration_ms,omitempty"`
	Failed     bool   `json:"failed,omitempty"`
	ResultNote string `json:"result_note,omitempty"`
	// Error fields.
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	// Completion fields.
	ConversationID string                   `json:"conversation_id,omitempty"`
	FinishReason   string                   `json:"finish_reason,omitempty"`
	Iterations     int                      `json:"iterations,omitempty"`
	Usage          *schemas.BifrostLLMUsage `json:"usage,omitempty"`
	Model          string                   `json:"model,omitempty"`
	Provider       string                   `json:"provider,omitempty"`
}

// ChatFunc is the loop's dependency on inference. It is a function rather
// than a *bifrost.Bifrost so tests can drive the loop with a scripted model and
// never need a live provider.
type ChatFunc func(ctx context.Context, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError)

// Agent is one question's worth of loop state: the model to ask, the tools it
// may run and the deployment slice those tools read.
type Agent struct {
	chat          ChatFunc
	tools         []Tool
	deps          *ToolDeps
	config        *schemas.WarpConfig
	maxIterations int
}

// NewAgent assembles an agent for one request. Config is read per request rather
// than captured once, so a settings change takes effect without a restart.
func NewAgent(chat ChatFunc, logs LogReader, config *schemas.WarpConfig) *Agent {
	return &Agent{
		chat:          chat,
		tools:         buildTools(),
		deps:          &ToolDeps{logManager: logs},
		config:        config,
		maxIterations: config.EffectiveMaxIterations(),
	}
}

const (
	// MaxToolCallsPerTurn bounds a single model turn. A model that asks for
	// twenty tools at once is thrashing, not researching.
	MaxToolCallsPerTurn = 4
	// MaxHistoryMessages and MaxHistoryBytes bound the client-sent
	// conversation. History is stateless by design - the dashboard holds the
	// thread - which means the request body is attacker-influenced and needs a
	// ceiling.
	MaxHistoryMessages = 40
	MaxHistoryBytes    = 256 * 1024
)

// Run drives the loop, emitting events onto out. It always closes out.
//
// The caller must pass a context that already carries the request's query scope
// (the transport snapshots it off the request). Every tool executes against this context, and
// queryscope treats a missing scope as "no restriction" - so a context that lost
// it returns the whole deployment to whoever asked.
func (a *Agent) Run(ctx context.Context, messages []schemas.ChatMessage, out chan<- Event) {
	defer close(out)

	emit := func(event Event) bool {
		select {
		case out <- event:
			return true
		case <-ctx.Done():
			return false
		}
	}

	declared, err := chatTools(a.tools)
	if err != nil {
		emit(Event{Type: EventError, Code: ErrUpstream, Message: err.Error()})
		return
	}

	emit(Event{
		Type:     EventStart,
		Model:    a.config.Model,
		Provider: string(a.config.Provider),
	})

	conversation := append([]schemas.ChatMessage{systemMessage(a.config)}, messages...)
	var usage *schemas.BifrostLLMUsage

	for iteration := 1; iteration <= a.maxIterations; iteration++ {
		if ctx.Err() != nil {
			emit(Event{Type: EventError, Code: ErrCancelled, Message: "request cancelled"})
			return
		}

		response, bifrostErr := a.chat(ctx, &schemas.BifrostChatRequest{
			Provider: a.config.Provider,
			Model:    a.config.Model,
			Input:    conversation,
			Params:   &schemas.ChatParameters{Tools: declared},
		})
		if bifrostErr != nil {
			code := ErrUpstream
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				code = ErrTimeout
			} else if ctx.Err() != nil {
				code = ErrCancelled
			}
			// An error frame is terminal. Never emit done after it, or a client
			// keyed on done reads a failed request as a successful one.
			emit(Event{Type: EventError, Code: code, Message: errorMessage(bifrostErr)})
			return
		}
		if response == nil || len(response.Choices) == 0 {
			emit(Event{Type: EventError, Code: ErrUpstream, Message: "the model returned no choices"})
			return
		}
		if response.Usage != nil {
			usage = response.Usage
		}

		message := assistantMessage(response)
		if message == nil {
			emit(Event{Type: EventError, Code: ErrUpstream, Message: "the model returned no message"})
			return
		}
		conversation = append(conversation, *message)

		text := messageText(message)
		toolCalls := messageToolCalls(message)

		if len(toolCalls) == 0 {
			if text != "" && !emit(Event{Type: EventDelta, Delta: text}) {
				return
			}
			emit(Event{
				Type:         EventDone,
				FinishReason: "stop",
				Iterations:   iteration,
				Usage:        usage,
			})
			return
		}

		// Narration the model produced alongside its tool calls ("let me check
		// last week's spend") is worth showing: it is what makes the wait legible
		// rather than a spinner.
		if text != "" && !emit(Event{Type: EventDelta, Delta: text}) {
			return
		}

		if len(toolCalls) > MaxToolCallsPerTurn {
			toolCalls = toolCalls[:MaxToolCallsPerTurn]
		}

		for _, call := range toolCalls {
			name, arguments, id := toolCallParts(call)
			if !emit(Event{
				Type: EventToolCallStart, ToolID: id, ToolName: name,
				Arguments: arguments, Iteration: iteration,
			}) {
				return
			}

			started := time.Now()
			result, failed := a.executeTool(ctx, name, arguments)
			if !emit(Event{
				Type: EventToolCallEnd, ToolID: id, ToolName: name,
				DurationMs: time.Since(started).Milliseconds(), Failed: failed,
			}) {
				return
			}

			conversation = append(conversation, schemas.ChatMessage{
				Role: schemas.ChatMessageRoleTool,
				Content: &schemas.ChatMessageContent{
					ContentStr: &result,
				},
				ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: &id},
			})
		}
	}

	// Out of iterations. Terminal error, no done frame.
	emit(Event{
		Type:    EventError,
		Code:    ErrMaxIterations,
		Message: fmt.Sprintf("Warp reached its limit of %d research steps without settling on an answer. Try a narrower question.", a.maxIterations),
	})
}

// executeTool runs one tool and returns the string handed back to the model.
//
// A tool failure is reported to the model as a tool result rather than aborting
// the request. The model can then correct itself - fix a filter name, widen a
// range - which is usually what a failed call means. Aborting would turn a
// recoverable mistake into a dead end.
func (a *Agent) executeTool(ctx context.Context, name, arguments string) (string, bool) {
	tool, ok := toolByName(a.tools, name)
	if !ok {
		return fmt.Sprintf(`{"error":"no tool named %q is available"}`, name), true
	}

	args := map[string]any{}
	if strings.TrimSpace(arguments) != "" {
		if err := sonic.UnmarshalString(arguments, &args); err != nil {
			return fmt.Sprintf(`{"error":"arguments were not valid JSON: %s"}`, err.Error()), true
		}
	}

	result, err := tool.execute(ctx, a.deps, args)
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error()), true
	}
	return boundToolResult(result), false
}

// errorMessage extracts a human-readable message from an upstream error,
// falling back to a generic one rather than surfacing an empty string.
func errorMessage(err *schemas.BifrostError) string {
	if err == nil {
		return "unknown upstream error"
	}
	if err.Error != nil && err.Error.Message != "" {
		return err.Error.Message
	}
	return "the model provider returned an error"
}

// assistantMessage pulls the assistant turn out of a completion. Warp uses
// the non-streaming response shape, so only that branch is populated.
func assistantMessage(response *schemas.BifrostChatResponse) *schemas.ChatMessage {
	choice := response.Choices[0]
	if choice.ChatNonStreamResponseChoice != nil {
		return choice.ChatNonStreamResponseChoice.Message
	}
	return nil
}

// messageText returns the message text, or empty when the turn carried none.
//
// Content is a pointer and providers routinely leave it nil on a turn that is
// purely tool calls - which is the single most common shape in this loop, since
// Warp's first move is almost always a tool call. Dereferencing it without this
// check panics, and because the loop runs in a goroutine the panic takes the
// whole server down rather than failing one request.
func messageText(message *schemas.ChatMessage) string {
	if message == nil || message.Content == nil || message.Content.ContentStr == nil {
		return ""
	}
	return *message.Content.ContentStr
}

// messageToolCalls returns the tool calls on an assistant turn, if any.
func messageToolCalls(message *schemas.ChatMessage) []schemas.ChatAssistantMessageToolCall {
	if message == nil || message.ChatAssistantMessage == nil {
		return nil
	}
	return message.ChatAssistantMessage.ToolCalls
}

// toolCallParts flattens a tool call into the three values the loop needs.
// Name, arguments and id are all optional on the wire, so each is defaulted to
// empty rather than dereferenced blindly.
func toolCallParts(call schemas.ChatAssistantMessageToolCall) (name, arguments, id string) {
	if call.Function.Name != nil {
		name = *call.Function.Name
	}
	arguments = call.Function.Arguments
	if call.ID != nil {
		id = *call.ID
	}
	return name, arguments, id
}

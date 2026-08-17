package handlers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// Odin's agent loop: ask the model, run whatever tools it asks for, feed the
// results back, repeat until it answers or runs out of iterations.
//
// The loop emits odinEvent values onto a channel rather than writing SSE
// directly. That is what lets the streaming and non-streaming endpoints share
// one implementation: the SSE path formats each event into a frame, the JSON
// path drains the same channel and assembles a single body. It also means the
// loop can be tested without a socket.

// odinEventType names the frames the client can receive.
type odinEventType string

const (
	odinEventStart         odinEventType = "start"
	odinEventDelta         odinEventType = "delta"
	odinEventToolCallStart odinEventType = "tool_call_start"
	odinEventToolCallEnd   odinEventType = "tool_call_end"
	odinEventError         odinEventType = "error"
	odinEventDone          odinEventType = "done"
)

// Error codes carried on odinEventError. The client branches on these, so they
// are part of the contract and must not be reworded casually.
const (
	odinErrNotConfigured = "not_configured"
	odinErrUpstream      = "upstream_error"
	odinErrToolFailed    = "tool_error"
	odinErrMaxIterations = "max_iterations"
	odinErrTimeout       = "timeout"
	odinErrCancelled     = "cancelled"
)

type odinEvent struct {
	Type odinEventType `json:"type"`
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

// odinChatFunc is the loop's dependency on inference. It is a function rather
// than a *bifrost.Bifrost so tests can drive the loop with a scripted model and
// never need a live provider.
type odinChatFunc func(ctx context.Context, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError)

type odinAgent struct {
	chat          odinChatFunc
	tools         []odinTool
	deps          *odinToolDeps
	config        *schemas.OdinConfig
	maxIterations int
}

const (
	// odinMaxToolCallsPerTurn bounds a single model turn. A model that asks for
	// twenty tools at once is thrashing, not researching.
	odinMaxToolCallsPerTurn = 4
	// odinMaxHistoryMessages and odinMaxHistoryBytes bound the client-sent
	// conversation. History is stateless by design - the dashboard holds the
	// thread - which means the request body is attacker-influenced and needs a
	// ceiling.
	odinMaxHistoryMessages = 40
	odinMaxHistoryBytes    = 256 * 1024
)

// run drives the loop, emitting events onto out. It always closes out.
//
// The caller must pass a context that already carries the request's query scope
// (see snapshotOdinContext). Every tool executes against this context, and
// queryscope treats a missing scope as "no restriction" - so a context that lost
// it returns the whole deployment to whoever asked.
func (a *odinAgent) run(ctx context.Context, messages []schemas.ChatMessage, out chan<- odinEvent) {
	defer close(out)

	emit := func(event odinEvent) bool {
		select {
		case out <- event:
			return true
		case <-ctx.Done():
			return false
		}
	}

	declared, err := odinChatTools(a.tools)
	if err != nil {
		emit(odinEvent{Type: odinEventError, Code: odinErrUpstream, Message: err.Error()})
		return
	}

	emit(odinEvent{
		Type:     odinEventStart,
		Model:    a.config.Model,
		Provider: string(a.config.Provider),
	})

	conversation := append([]schemas.ChatMessage{odinSystemMessage(a.config)}, messages...)
	var usage *schemas.BifrostLLMUsage

	for iteration := 1; iteration <= a.maxIterations; iteration++ {
		if ctx.Err() != nil {
			emit(odinEvent{Type: odinEventError, Code: odinErrCancelled, Message: "request cancelled"})
			return
		}

		response, bifrostErr := a.chat(ctx, &schemas.BifrostChatRequest{
			Provider: a.config.Provider,
			Model:    a.config.Model,
			Input:    conversation,
			Params:   &schemas.ChatParameters{Tools: declared},
		})
		if bifrostErr != nil {
			code := odinErrUpstream
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				code = odinErrTimeout
			} else if ctx.Err() != nil {
				code = odinErrCancelled
			}
			// An error frame is terminal. Never emit done after it, or a client
			// keyed on done reads a failed request as a successful one.
			emit(odinEvent{Type: odinEventError, Code: code, Message: odinErrorMessage(bifrostErr)})
			return
		}
		if response == nil || len(response.Choices) == 0 {
			emit(odinEvent{Type: odinEventError, Code: odinErrUpstream, Message: "the model returned no choices"})
			return
		}
		if response.Usage != nil {
			usage = response.Usage
		}

		message := odinAssistantMessage(response)
		if message == nil {
			emit(odinEvent{Type: odinEventError, Code: odinErrUpstream, Message: "the model returned no message"})
			return
		}
		conversation = append(conversation, *message)

		text := odinMessageText(message)
		toolCalls := odinMessageToolCalls(message)

		if len(toolCalls) == 0 {
			if text != "" && !emit(odinEvent{Type: odinEventDelta, Delta: text}) {
				return
			}
			emit(odinEvent{
				Type:         odinEventDone,
				FinishReason: "stop",
				Iterations:   iteration,
				Usage:        usage,
			})
			return
		}

		// Narration the model produced alongside its tool calls ("let me check
		// last week's spend") is worth showing: it is what makes the wait legible
		// rather than a spinner.
		if text != "" && !emit(odinEvent{Type: odinEventDelta, Delta: text}) {
			return
		}

		if len(toolCalls) > odinMaxToolCallsPerTurn {
			toolCalls = toolCalls[:odinMaxToolCallsPerTurn]
		}

		for _, call := range toolCalls {
			name, arguments, id := odinToolCallParts(call)
			if !emit(odinEvent{
				Type: odinEventToolCallStart, ToolID: id, ToolName: name,
				Arguments: arguments, Iteration: iteration,
			}) {
				return
			}

			started := time.Now()
			result, failed := a.executeTool(ctx, name, arguments)
			if !emit(odinEvent{
				Type: odinEventToolCallEnd, ToolID: id, ToolName: name,
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
	emit(odinEvent{
		Type:    odinEventError,
		Code:    odinErrMaxIterations,
		Message: fmt.Sprintf("Odin reached its limit of %d research steps without settling on an answer. Try a narrower question.", a.maxIterations),
	})
}

// executeTool runs one tool and returns the string handed back to the model.
//
// A tool failure is reported to the model as a tool result rather than aborting
// the request. The model can then correct itself - fix a filter name, widen a
// range - which is usually what a failed call means. Aborting would turn a
// recoverable mistake into a dead end.
func (a *odinAgent) executeTool(ctx context.Context, name, arguments string) (string, bool) {
	tool, ok := odinToolByName(a.tools, name)
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
	return boundOdinToolResult(result), false
}

// odinErrorMessage extracts a human-readable message from an upstream error,
// falling back to a generic one rather than surfacing an empty string.
func odinErrorMessage(err *schemas.BifrostError) string {
	if err == nil {
		return "unknown upstream error"
	}
	if err.Error != nil && err.Error.Message != "" {
		return err.Error.Message
	}
	return "the model provider returned an error"
}

// odinAssistantMessage pulls the assistant turn out of a completion. Odin uses
// the non-streaming response shape, so only that branch is populated.
func odinAssistantMessage(response *schemas.BifrostChatResponse) *schemas.ChatMessage {
	choice := response.Choices[0]
	if choice.ChatNonStreamResponseChoice != nil {
		return choice.ChatNonStreamResponseChoice.Message
	}
	return nil
}

// odinMessageText returns the message text, or empty when the turn carried none.
//
// Content is a pointer and providers routinely leave it nil on a turn that is
// purely tool calls - which is the single most common shape in this loop, since
// Odin's first move is almost always a tool call. Dereferencing it without this
// check panics, and because the loop runs in a goroutine the panic takes the
// whole server down rather than failing one request.
func odinMessageText(message *schemas.ChatMessage) string {
	if message == nil || message.Content == nil || message.Content.ContentStr == nil {
		return ""
	}
	return *message.Content.ContentStr
}

// odinMessageToolCalls returns the tool calls on an assistant turn, if any.
func odinMessageToolCalls(message *schemas.ChatMessage) []schemas.ChatAssistantMessageToolCall {
	if message == nil || message.ChatAssistantMessage == nil {
		return nil
	}
	return message.ChatAssistantMessage.ToolCalls
}

// odinToolCallParts flattens a tool call into the three values the loop needs.
// Name, arguments and id are all optional on the wire, so each is defaulted to
// empty rather than dereferenced blindly.
func odinToolCallParts(call schemas.ChatAssistantMessageToolCall) (name, arguments, id string) {
	if call.Function.Name != nil {
		name = *call.Function.Name
	}
	arguments = call.Function.Arguments
	if call.ID != nil {
		id = *call.ID
	}
	return name, arguments, id
}

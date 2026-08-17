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
	// EventQuestion carries a structured question for the person to answer.
	// It is followed by done: the turn ends there, and the reply arrives as an
	// ordinary next message.
	EventQuestion eventType = "question"
	EventError    eventType = "error"
	EventDone     eventType = "done"
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
	DurationMs int64 `json:"duration_ms,omitempty"`
	Failed     bool  `json:"failed,omitempty"`
	// ToolError is the executor's own message, carried so the panel can show why
	// a step failed. Without it a failed step is a red tick with no account of
	// itself, and a retry loop looks like the same query running four times for
	// no reason.
	ToolError  string `json:"tool_error,omitempty"`
	ResultNote string `json:"result_note,omitempty"`
	// Error fields.
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	// Question carries the structured question posed by ask_user.
	Question *Question `json:"question,omitempty"`
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
type ChatFunc func(ctx context.Context, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError)

// CostFunc prices one turn's usage.
//
// Most providers return no cost of their own, and Warp's client is plugin-free
// by design - so without this the panel could only ever show a token count, and
// "5,313 tokens" answers a question nobody asked. Nil on a deployment with no
// model catalog, where tokens really are all there is.
type CostFunc func(usage *schemas.BifrostLLMUsage) float64

type Agent struct {
	chat          ChatFunc
	cost          CostFunc
	tools         []Tool
	deps          *ToolDeps
	config        *schemas.WarpConfig
	maxIterations int
}

// NewAgent assembles a loop for one request.
//
// The fields stay unexported and the tool set is fixed here rather than passed
// in: a caller that could swap the tools could also widen what Warp is able to
// read, and the whole read surface is meant to be reviewable from LogReader
// alone. What a caller does supply is the inference function, the pricing
// function, and the scope - the three things that genuinely vary per request.
//
// scope comes from the caller because it must be lifted off the request context
// before the agent's goroutine starts. queryscope treats a missing scope as no
// restriction, so reading it late returns the whole deployment to whoever asked.
func NewAgent(chat ChatFunc, cost CostFunc, logs LogReader, scope Scope, config *schemas.WarpConfig) *Agent {
	return &Agent{
		chat:          chat,
		cost:          cost,
		tools:         buildTools(),
		deps:          &ToolDeps{logManager: logs, scope: scope},
		config:        config,
		maxIterations: config.EffectiveMaxIterations(),
	}
}

// accumulateUsage folds one iteration's usage into the running total.
//
// A question that takes four research steps costs four model calls, and
// reporting only the last one understates the answer by however many steps it
// took - worst for exactly the expensive questions where the number matters.
// Each turn is priced as it arrives, because a later iteration can be served by
// a different model after a fallback and pricing the sum would use the wrong
// rate card.
func accumulateUsage(total, next *schemas.BifrostLLMUsage, price CostFunc) *schemas.BifrostLLMUsage {
	if next == nil {
		return total
	}
	if total == nil {
		total = &schemas.BifrostLLMUsage{}
	}
	total.PromptTokens += next.PromptTokens
	total.CompletionTokens += next.CompletionTokens
	if next.TotalTokens > 0 {
		total.TotalTokens += next.TotalTokens
	} else {
		// Some providers report the parts but not the sum. Deriving it here keeps
		// the total honest instead of leaving it at zero beside non-zero parts.
		total.TotalTokens += next.PromptTokens + next.CompletionTokens
	}

	turnCost := 0.0
	if next.Cost != nil {
		turnCost = next.Cost.TotalCost
	}
	// Only price what the provider did not. A provider-reported cost is what was
	// actually billed; the catalog is an estimate, and an estimate must never
	// overwrite a fact.
	if turnCost == 0 && price != nil {
		turnCost = price(next)
	}
	if turnCost > 0 {
		if total.Cost == nil {
			total.Cost = &schemas.BifrostCost{}
		}
		total.Cost.TotalCost += turnCost
	}
	return total
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

// run drives the loop, emitting events onto out. It always closes out.
//
// The caller must pass a context that already carries the request's query scope
// (see snapshotWarpContext). Every tool executes against this context, and
// queryscope treats a missing scope as "no restriction" - so a context that lost
// it returns the whole deployment to whoever asked.
func (a *Agent) Run(ctx context.Context, messages []schemas.ResponsesMessage, out chan<- Event) {
	defer close(out)

	emit := func(event Event) bool {
		select {
		case out <- event:
			return true
		case <-ctx.Done():
			return false
		}
	}

	declared, err := responsesTools(a.tools)
	if err != nil {
		emit(Event{Type: EventError, Code: ErrUpstream, Message: err.Error()})
		return
	}

	emit(Event{
		Type:     EventStart,
		Model:    a.config.Model,
		Provider: string(a.config.Provider),
	})

	// The system prompt rides on Params.Instructions rather than as a leading
	// system item. The Responses API models instructions as a property of the
	// request, not a turn in the transcript, and keeping it out of Input means the
	// history bound below counts only real turns.
	instructions := systemInstructions(a.config)
	conversation := append([]schemas.ResponsesMessage{}, messages...)
	var usage *schemas.BifrostLLMUsage

	for iteration := 1; iteration <= a.maxIterations; iteration++ {
		if ctx.Err() != nil {
			emit(Event{Type: EventError, Code: ErrCancelled, Message: "request cancelled"})
			return
		}

		response, bifrostErr := a.chat(ctx, &schemas.BifrostResponsesRequest{
			// The wire protocol, not the provider that serves the request: the
			// configured provider rides in the model string below. See
			// transportProvider.
			Provider: transportProvider(),
			// Qualified as provider/model so the routing on the other end cannot
			// substitute a different provider for the same model name.
			Model: modelForRequest(a.config),
			Input: conversation,
			Params: &schemas.ResponsesParameters{
				Instructions: &instructions,
				Tools:        declared,
			},
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
		if response == nil || len(response.Output) == 0 {
			emit(Event{Type: EventError, Code: ErrUpstream, Message: "the model returned no output"})
			return
		}
		usage = accumulateUsage(usage, usageFromResponses(response.Usage), a.cost)

		// Every output item goes back verbatim, reasoning items included. Replaying
		// a reasoning model's own items is what lets it continue the thought it
		// started; dropping them makes each iteration start over.
		conversation = append(conversation, response.Output...)

		text := responsesText(response.Output)
		toolCalls := responsesToolCalls(response.Output)

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

		for index, call := range toolCalls {
			name, arguments, id := call.Name, call.Arguments, call.ID

			// Past the cap the call is refused, not dropped. The whole output list
			// - every function_call in it - was appended to the conversation above,
			// and providers require each one to be answered: Anthropic rejects the
			// next request outright with "tool_use ids were found without
			// tool_result blocks immediately after". Truncating the slice left
			// exactly those orphans behind, so a model that asked for too much at
			// once turned into Warp being unreachable, with nothing in the message
			// to suggest tool limits had anything to do with it.
			if index >= MaxToolCallsPerTurn {
				conversation = append(conversation, toolResultMessage(id,
					fmt.Sprintf(`{"error":"not run: no more than %d tools may be called in one step. Ask for the ones you need most, then continue."}`, MaxToolCallsPerTurn)))
				continue
			}

			// ask_user is not a query, it is the end of the turn. Emitting the
			// question and stopping is what makes the exchange turn-based: the reply
			// arrives as an ordinary next message, so there is no second channel and
			// no request held open while somebody reads.
			if question := questionFromToolCall(name, arguments); question != nil {
				if !emit(Event{Type: EventQuestion, Question: question}) {
					return
				}
				emit(Event{
					Type:         EventDone,
					FinishReason: "question",
					Iterations:   iteration,
					Usage:        usage,
				})
				return
			}

			if !emit(Event{
				Type: EventToolCallStart, ToolID: id, ToolName: name,
				Arguments: arguments, Iteration: iteration,
			}) {
				return
			}

			started := time.Now()
			result, failed := a.executeTool(ctx, name, arguments)
			end := Event{
				Type: EventToolCallEnd, ToolID: id, ToolName: name,
				DurationMs: time.Since(started).Milliseconds(), Failed: failed,
			}
			if failed {
				// The result *is* the error message on a failed call, and it is
				// already bounded, so it can be surfaced as-is.
				end.ToolError = result
			}
			if !emit(end) {
				return
			}

			conversation = append(conversation, toolResultMessage(id, result))
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

// responsesText concatenates the assistant prose in an output list.
//
// A Responses turn is a list of items, not one message: prose, reasoning and
// tool calls arrive as siblings, and a turn that is purely tool calls carries no
// text item at all - the single most common shape in this loop, since Warp's
// first move is almost always a query. Everything here is therefore a lookup
// that tolerates absence rather than a dereference.
func responsesText(output []schemas.ResponsesMessage) string {
	var builder strings.Builder
	for _, item := range output {
		if item.Type != nil && *item.Type != schemas.ResponsesMessageTypeMessage {
			continue
		}
		if item.Content == nil {
			continue
		}
		if item.Content.ContentStr != nil {
			builder.WriteString(*item.Content.ContentStr)
			continue
		}
		for _, block := range item.Content.ContentBlocks {
			if block.Text != nil {
				builder.WriteString(*block.Text)
			}
		}
	}
	return builder.String()
}

// responsesToolCalls returns the function calls in an output list, flattened
// to the three values the loop needs. Name, arguments and call id are all
// optional on the wire, so each is defaulted rather than dereferenced blindly.
func responsesToolCalls(output []schemas.ResponsesMessage) []ToolCall {
	var calls []ToolCall
	for _, item := range output {
		if item.Type == nil || *item.Type != schemas.ResponsesMessageTypeFunctionCall {
			continue
		}
		if item.ResponsesToolMessage == nil {
			continue
		}
		call := ToolCall{}
		if item.Name != nil {
			call.Name = *item.Name
		}
		if item.Arguments != nil {
			call.Arguments = *item.Arguments
		}
		if item.CallID != nil {
			call.ID = *item.CallID
		}
		calls = append(calls, call)
	}
	return calls
}

// ToolCall is one function call the model asked for.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// toolResultMessage builds the function_call_output item that answers a
// call. The call id is what pairs it with its request, so a lost id turns a
// perfectly good result into an orphan the model cannot attribute.
func toolResultMessage(callID, result string) schemas.ResponsesMessage {
	itemType := schemas.ResponsesMessageTypeFunctionCallOutput
	return schemas.ResponsesMessage{
		Type: &itemType,
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID: &callID,
			Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: &result},
		},
	}
}

// usageFromResponses converts Responses usage into the chat-shaped usage the
// rest of Warp reports.
//
// The two APIs count the same thing under different names (input/output versus
// prompt/completion). Converting at this one boundary keeps the pricing helper,
// the SSE event and the panel on a single shape, rather than teaching each of
// them about both.
func usageFromResponses(usage *schemas.ResponsesResponseUsage) *schemas.BifrostLLMUsage {
	if usage == nil {
		return nil
	}
	return &schemas.BifrostLLMUsage{
		PromptTokens:     usage.InputTokens,
		CompletionTokens: usage.OutputTokens,
		TotalTokens:      usage.TotalTokens,
		Cost:             usage.Cost,
	}
}

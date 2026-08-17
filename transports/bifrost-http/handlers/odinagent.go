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
	// odinEventQuestion carries a structured question for the person to answer.
	// It is followed by done: the turn ends there, and the reply arrives as an
	// ordinary next message.
	odinEventQuestion odinEventType = "question"
	odinEventError    odinEventType = "error"
	odinEventDone     odinEventType = "done"
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
	Question *odinQuestion `json:"question,omitempty"`
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
type odinChatFunc func(ctx context.Context, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError)

// odinCostFunc prices one turn's usage.
//
// Most providers return no cost of their own, and Odin's client is plugin-free
// by design - so without this the panel could only ever show a token count, and
// "5,313 tokens" answers a question nobody asked. Nil on a deployment with no
// model catalog, where tokens really are all there is.
type odinCostFunc func(usage *schemas.BifrostLLMUsage) float64

type odinAgent struct {
	chat          odinChatFunc
	cost          odinCostFunc
	tools         []odinTool
	deps          *odinToolDeps
	config        *schemas.OdinConfig
	maxIterations int
}

// accumulateOdinUsage folds one iteration's usage into the running total.
//
// A question that takes four research steps costs four model calls, and
// reporting only the last one understates the answer by however many steps it
// took - worst for exactly the expensive questions where the number matters.
// Each turn is priced as it arrives, because a later iteration can be served by
// a different model after a fallback and pricing the sum would use the wrong
// rate card.
func accumulateOdinUsage(total, next *schemas.BifrostLLMUsage, price odinCostFunc) *schemas.BifrostLLMUsage {
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
func (a *odinAgent) run(ctx context.Context, messages []schemas.ResponsesMessage, out chan<- odinEvent) {
	defer close(out)

	emit := func(event odinEvent) bool {
		select {
		case out <- event:
			return true
		case <-ctx.Done():
			return false
		}
	}

	declared, err := odinResponsesTools(a.tools)
	if err != nil {
		emit(odinEvent{Type: odinEventError, Code: odinErrUpstream, Message: err.Error()})
		return
	}

	emit(odinEvent{
		Type:     odinEventStart,
		Model:    a.config.Model,
		Provider: string(a.config.Provider),
	})

	// The system prompt rides on Params.Instructions rather than as a leading
	// system item. The Responses API models instructions as a property of the
	// request, not a turn in the transcript, and keeping it out of Input means the
	// history bound below counts only real turns.
	instructions := odinSystemInstructions(a.config)
	conversation := append([]schemas.ResponsesMessage{}, messages...)
	var usage *schemas.BifrostLLMUsage

	for iteration := 1; iteration <= a.maxIterations; iteration++ {
		if ctx.Err() != nil {
			emit(odinEvent{Type: odinEventError, Code: odinErrCancelled, Message: "request cancelled"})
			return
		}

		response, bifrostErr := a.chat(ctx, &schemas.BifrostResponsesRequest{
			// The wire protocol, not the provider that serves the request: the
			// configured provider rides in the model string below. See
			// odinTransportProvider.
			Provider: odinTransportProvider(),
			// Qualified as provider/model so the routing on the other end cannot
			// substitute a different provider for the same model name.
			Model: odinModelForRequest(a.config),
			Input: conversation,
			Params: &schemas.ResponsesParameters{
				Instructions: &instructions,
				Tools:        declared,
			},
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
		if response == nil || len(response.Output) == 0 {
			emit(odinEvent{Type: odinEventError, Code: odinErrUpstream, Message: "the model returned no output"})
			return
		}
		usage = accumulateOdinUsage(usage, odinUsageFromResponses(response.Usage), a.cost)

		// Every output item goes back verbatim, reasoning items included. Replaying
		// a reasoning model's own items is what lets it continue the thought it
		// started; dropping them makes each iteration start over.
		conversation = append(conversation, response.Output...)

		text := odinResponsesText(response.Output)
		toolCalls := odinResponsesToolCalls(response.Output)

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

		for index, call := range toolCalls {
			name, arguments, id := call.Name, call.Arguments, call.ID

			// Past the cap the call is refused, not dropped. The whole output list
			// - every function_call in it - was appended to the conversation above,
			// and providers require each one to be answered: Anthropic rejects the
			// next request outright with "tool_use ids were found without
			// tool_result blocks immediately after". Truncating the slice left
			// exactly those orphans behind, so a model that asked for too much at
			// once turned into Odin being unreachable, with nothing in the message
			// to suggest tool limits had anything to do with it.
			if index >= odinMaxToolCallsPerTurn {
				conversation = append(conversation, odinToolResultMessage(id,
					fmt.Sprintf(`{"error":"not run: no more than %d tools may be called in one step. Ask for the ones you need most, then continue."}`, odinMaxToolCallsPerTurn)))
				continue
			}

			// ask_user is not a query, it is the end of the turn. Emitting the
			// question and stopping is what makes the exchange turn-based: the reply
			// arrives as an ordinary next message, so there is no second channel and
			// no request held open while somebody reads.
			if question := odinQuestionFromToolCall(name, arguments); question != nil {
				if !emit(odinEvent{Type: odinEventQuestion, Question: question}) {
					return
				}
				emit(odinEvent{
					Type:         odinEventDone,
					FinishReason: "question",
					Iterations:   iteration,
					Usage:        usage,
				})
				return
			}

			if !emit(odinEvent{
				Type: odinEventToolCallStart, ToolID: id, ToolName: name,
				Arguments: arguments, Iteration: iteration,
			}) {
				return
			}

			started := time.Now()
			result, failed := a.executeTool(ctx, name, arguments)
			end := odinEvent{
				Type: odinEventToolCallEnd, ToolID: id, ToolName: name,
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

			conversation = append(conversation, odinToolResultMessage(id, result))
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

// odinResponsesText concatenates the assistant prose in an output list.
//
// A Responses turn is a list of items, not one message: prose, reasoning and
// tool calls arrive as siblings, and a turn that is purely tool calls carries no
// text item at all - the single most common shape in this loop, since Odin's
// first move is almost always a query. Everything here is therefore a lookup
// that tolerates absence rather than a dereference.
func odinResponsesText(output []schemas.ResponsesMessage) string {
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

// odinResponsesToolCalls returns the function calls in an output list, flattened
// to the three values the loop needs. Name, arguments and call id are all
// optional on the wire, so each is defaulted rather than dereferenced blindly.
func odinResponsesToolCalls(output []schemas.ResponsesMessage) []odinToolCall {
	var calls []odinToolCall
	for _, item := range output {
		if item.Type == nil || *item.Type != schemas.ResponsesMessageTypeFunctionCall {
			continue
		}
		if item.ResponsesToolMessage == nil {
			continue
		}
		call := odinToolCall{}
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

// odinToolCall is one function call the model asked for.
type odinToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// odinToolResultMessage builds the function_call_output item that answers a
// call. The call id is what pairs it with its request, so a lost id turns a
// perfectly good result into an orphan the model cannot attribute.
func odinToolResultMessage(callID, result string) schemas.ResponsesMessage {
	itemType := schemas.ResponsesMessageTypeFunctionCallOutput
	return schemas.ResponsesMessage{
		Type: &itemType,
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID: &callID,
			Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: &result},
		},
	}
}

// odinUsageFromResponses converts Responses usage into the chat-shaped usage the
// rest of Odin reports.
//
// The two APIs count the same thing under different names (input/output versus
// prompt/completion). Converting at this one boundary keeps the pricing helper,
// the SSE event and the panel on a single shape, rather than teaching each of
// them about both.
func odinUsageFromResponses(usage *schemas.ResponsesResponseUsage) *schemas.BifrostLLMUsage {
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

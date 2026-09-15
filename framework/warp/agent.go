package warp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

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

// FinishReasonPartial marks an answer given on the last research step, after
// the model was told to stop querying and say what it had. The figure may be
// incomplete, and the UI labels it so.
const FinishReasonPartial = "partial"

// finalStepInstructions is appended to the system prompt on the last research
// step, which is sent without tools. The model cannot spend that step on one
// more query, so whatever it knows becomes the answer instead of an error.
const finalStepInstructions = "\n\nThis is your final step. You have used every research step you have, and no tools are available now. " +
	"Answer from the results you already have. Lead with what you found, then say plainly what you could not check. " +
	"Do not present a gap as a settled figure."

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
	//
	// DurationMs is deliberately not omitempty: a call that fails validation
	// before doing any real work can finish in under a millisecond, and
	// time.Duration.Milliseconds() truncates that to exactly 0. omitempty would
	// drop the field from the wire entirely in that case, and the client reads
	// its absence as "still running" (see ChatToolCall.DurationMs below, whose
	// tag already omits omitempty for the same reason) - so a fast failure's
	// row would spin forever even though failed and tool_error arrived fine.
	DurationMs int64 `json:"duration_ms"`
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
	chat             ChatFunc
	cost             CostFunc
	tools            []Tool
	deps             *ToolDeps
	config           *schemas.WarpConfig
	maxIterations    int
	utcOffsetMinutes int
	timezone         string
	// questionsAsked is how many clarifying questions this thread has already
	// had in a row. Run stops after each one, but the next request builds a new
	// agent - so the count has to arrive with the conversation or nothing caps
	// the loop.
	questionsAsked int
}

// NewAgent assembles a loop for one request.
//
// The fields stay unexported and the tool set is fixed here rather than passed
// in: a caller that could swap the tools could also widen what Warp is able to
// read, and the whole read surface is meant to be reviewable from LogReader
// (and, for describe_virtual_key, GovernanceReader) alone. What a caller does
// supply is the inference function, the pricing function, the scope, and the
// asker's UTC offset - the things that genuinely vary per request.
//
// scope comes from the caller because it must be lifted off the request context
// before the agent's goroutine starts. queryscope treats a missing scope as no
// restriction, so reading it late returns the whole deployment to whoever asked.
//
// utcOffsetMinutes and timezone are already sanitized by the caller (see
// sanitizeUTCOffsetMinutes and sanitizeTimezone); this constructor trusts them
// rather than re-validating, since Turn is the one place a raw client value
// exists.
func NewAgent(chat ChatFunc, cost CostFunc, logs LogReader, governance GovernanceReader, scope Scope, config *schemas.WarpConfig, utcOffsetMinutes int, timezone string, semantic ...*SemanticSearcher) *Agent {
	var searcher *SemanticSearcher
	if len(semantic) > 0 {
		searcher = semantic[0]
	}
	return &Agent{
		chat:             chat,
		cost:             cost,
		tools:            buildToolsFor(searcher),
		deps:             &ToolDeps{logManager: logs, semantic: searcher, scope: scope, governance: governance},
		config:           config,
		maxIterations:    config.EffectiveMaxIterations(),
		utcOffsetMinutes: utcOffsetMinutes,
		timezone:         timezone,
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

	// The nested detail structs have to be summed too. Carrying only the three
	// scalars left the reported total disagreeing with its own breakdown -
	// cached reads and reasoning tokens stuck at whatever the first turn
	// reported - which reads as a real accounting of the request rather than a
	// partial one. Cost is handled below rather than here, because this loop
	// prices each turn as it arrives.
	total.PromptTokensDetails = mergePromptTokenDetails(total.PromptTokensDetails, next.PromptTokensDetails)
	total.CompletionTokensDetails = mergeCompletionTokenDetails(total.CompletionTokensDetails, next.CompletionTokensDetails)

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
	// The halves are summed alongside the total, so the reported breakdown adds
	// up to the figure beside it. A total with a zeroed breakdown reads as a
	// real accounting of the request rather than a partial one - and this
	// terminal event is the only place Warp's own spend is ever reported.
	if next.Cost != nil && (next.Cost.InputCost != 0 || next.Cost.OutputCost != 0 || next.Cost.AdditionalCost != 0) {
		if total.Cost == nil {
			total.Cost = &schemas.BifrostCost{}
		}
		total.Cost.InputCost += next.Cost.InputCost
		total.Cost.OutputCost += next.Cost.OutputCost
		total.Cost.AdditionalCost += next.Cost.AdditionalCost
	}
	return total
}

// mergePromptTokenDetails and mergeCompletionTokenDetails sum the nested
// per-turn breakdowns.
//
// Written here rather than reusing schemas.MergeBifrostLLMUsage: that merger
// also combines costs, and this loop deliberately prices each turn as it
// arrives, because a later iteration can be served by a different model after a
// fallback and pricing the sum would apply the wrong rate card.
func mergePromptTokenDetails(total, next *schemas.ChatPromptTokensDetails) *schemas.ChatPromptTokensDetails {
	if next == nil {
		return total
	}
	if total == nil {
		// Deep on the nested pointer, not just the struct. A shallow copy leaves
		// CachedWriteTokenDetails aliasing the provider response's own struct, and
		// the next merge then adds into it in place - mutating a response this
		// package does not own, and double-counting if that response is reused.
		copied := *next
		if next.CachedWriteTokenDetails != nil {
			nested := *next.CachedWriteTokenDetails
			copied.CachedWriteTokenDetails = &nested
		}
		return &copied
	}
	// Every field the converter populates, not a chosen few. usageFromResponses
	// fills TextTokens, ImageTokens and CachedWriteTokens as well, and on the
	// first turn the struct is copied wholesale - so anything left out here
	// stayed frozen at the first turn's value while TotalTokens kept climbing,
	// and a four-step research turn reported a breakdown that did not add up to
	// the number beside it. That inconsistency is what these helpers exist to
	// remove.
	total.TextTokens += next.TextTokens
	total.AudioTokens += next.AudioTokens
	total.ImageTokens += next.ImageTokens
	total.CachedReadTokens += next.CachedReadTokens
	total.CachedWriteTokens += next.CachedWriteTokens
	total.CachedWriteTokenDetails = mergeCachedWriteTokenDetails(total.CachedWriteTokenDetails, next.CachedWriteTokenDetails)
	return total
}

func mergeCachedWriteTokenDetails(total, next *schemas.ChatCachedWriteTokenDetails) *schemas.ChatCachedWriteTokenDetails {
	if next == nil {
		return total
	}
	if total == nil {
		copied := *next
		return &copied
	}
	total.CachedWriteTokens5m += next.CachedWriteTokens5m
	total.CachedWriteTokens1h += next.CachedWriteTokens1h
	return total
}

// addOptionalTokens sums two optional counters.
//
// nil and zero are different answers: nil is "the provider said nothing about
// this", zero is "it said none". Summing into a fresh pointer also matters -
// the first turn's struct is shallow-copied, so its pointer fields are shared
// with the response it came from, and adding in place would edit that response.
func addOptionalTokens(total, next *int) *int {
	if next == nil {
		return total
	}
	if total == nil {
		copied := *next
		return &copied
	}
	sum := *total + *next
	return &sum
}

func mergeCompletionTokenDetails(total, next *schemas.ChatCompletionTokensDetails) *schemas.ChatCompletionTokensDetails {
	if next == nil {
		return total
	}
	if total == nil {
		copied := *next
		return &copied
	}
	total.TextTokens += next.TextTokens
	total.ReasoningTokens += next.ReasoningTokens
	total.AudioTokens += next.AudioTokens
	total.AcceptedPredictionTokens += next.AcceptedPredictionTokens
	total.RejectedPredictionTokens += next.RejectedPredictionTokens
	total.CitationTokens = addOptionalTokens(total.CitationTokens, next.CitationTokens)
	total.NumSearchQueries = addOptionalTokens(total.NumSearchQueries, next.NumSearchQueries)
	total.ImageTokens = addOptionalTokens(total.ImageTokens, next.ImageTokens)
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
	// maxConcurrentToolCalls bounds how many tool calls may be executing
	// against the store at once, across every active Warp turn in this
	// process - not just the MaxToolCallsPerTurn=4 that bounds one turn's own
	// batch. Without this, several sessions each mid-batch multiply
	// unboundedly: the store shares its connection pool with the gateway's
	// own request-logging writes (see LogReader's doc comment), and on the
	// SQLite deployment default in particular that pool has no tuning of its
	// own to fall back on. Sized for two turns' worth of concurrency before a
	// third has to wait its turn - the same scale client.go's own
	// InitialPoolSize already assumes for Warp ("one dashboard user asking
	// one question at a time"). A constant, not a config field: this is a
	// safety net against a shared resource, not a knob most deployments will
	// ever need to touch.
	maxConcurrentToolCalls = 8

	// MaxConversationEstimatedTokens caps how large this turn's own
	// accumulated conversation - the model's own output plus every tool
	// result, replayed in full on every iteration - may grow before Warp is
	// forced to answer from what it already has rather than keep researching.
	// Each tool result is individually bounded (MaxToolResultBytes), and each
	// step's own tool calls are bounded (MaxToolCallsPerTurn), but nothing
	// previously bounded their sum across iterations: a model that filled every
	// iteration's budget could still assemble a request the underlying
	// provider rejects outright as too large, failing the whole turn with an
	// opaque upstream error instead of a partial answer. Sized well under
	// every provider's context window, including the smallest common one
	// (128k tokens), leaving room for the system prompt, the client-sent
	// history and the model's own next response.
	MaxConversationEstimatedTokens = 100_000
	// bytesPerTokenEstimate approximates how many UTF-8 bytes one ASCII-range
	// token occupies, for the running budget above. Real text averages closer
	// to 4; this is deliberately smaller so the estimate errs toward
	// overcounting tokens, which trips the budget earlier rather than later.
	// It only applies to ASCII bytes - see estimateTokensForBytes for why
	// non-ASCII content is not scaled down the same way.
	bytesPerTokenEstimate = 3

	// maxStepOutputTokens bounds a non-final step's own generation (narration
	// plus tool call arguments) - never the answer-only final step, which is
	// deliberately left uncapped. Without a real cap here, maxToolRoundGrowthTokens
	// below would only be an assumption: a reasoning model can otherwise emit far
	// more than that, and the request built right after - possibly the
	// answer-only one - would inherit however much it actually wrote.
	maxStepOutputTokens = 8192
	// maxToolRoundGrowthTokens is the most one non-final step can add to
	// conversationTokens: every queued tool call maxed out at
	// MaxToolResultBytes, plus the model's own output capped at
	// maxStepOutputTokens. Reserved as headroom before another tool round is
	// allowed (see finalStep below) - otherwise a step that was safely under
	// MaxConversationEstimatedTokens when it started could still leave the
	// conversation well past it once its results land, and the very next
	// request, tool round or answer-only, would be built from that
	// already-oversized total with nothing left to shrink it.
	//
	// MaxToolResultBytes is not divided by bytesPerTokenEstimate here: that
	// ratio only holds for ASCII (see estimateTokensForBytes), and a tool
	// result maxed out on dense non-ASCII content estimates at close to one
	// token per byte, not one per three. Using the discounted figure would
	// undersize this reservation for exactly the content it exists to guard
	// against.
	maxToolRoundGrowthTokens = MaxToolCallsPerTurn*MaxToolResultBytes + maxStepOutputTokens
)

// estimateMessageTokens roughly sizes messages for the running conversation
// budget. It does not need to be exact - a byte-based estimate is cheap enough
// to run every iteration, and only needs to trip the budget meaningfully
// before the real request would overflow the model's context window.
func estimateMessageTokens(messages ...schemas.ResponsesMessage) int {
	total := 0
	for _, message := range messages {
		encoded, err := sonic.Marshal(message)
		if err != nil {
			continue
		}
		total += estimateTokensForBytes(encoded)
	}
	return total
}

// estimateTokensForBytes bounds the token estimate for one already-encoded
// payload without a real tokenizer for whichever provider/model is
// configured - Warp has no local tokenizer for most of them, and even a
// borrowed one (e.g. tiktoken for an OpenAI model) would misestimate for
// every other provider, so this stays a byte-based heuristic rather than
// pretending to be exact for one provider and silently wrong for the rest.
//
// The bytesPerTokenEstimate ratio only holds for ASCII: a trained BPE
// vocabulary is dominated by merged multi-byte ASCII tokens (common words,
// punctuation, JSON structure), so real text rarely falls back anywhere near
// one token per byte there. Non-ASCII bytes get no such discount and are
// counted close to 1:1 - a tokenizer's byte-fallback path, taken for a
// multi-byte UTF-8 sequence it has no merged token for, can turn each of
// those bytes into its own token, and no tokenizer can produce more tokens
// than input bytes. Without this split, a message that is mostly dense or
// unusual Unicode (heavy CJK/emoji, or content shaped to defeat the estimate)
// could undercount by up to bytesPerTokenEstimate, letting a request past the
// budget check that the provider would still reject as too large. A single
// pass with no allocation keeps this as cheap as the flat division it
// replaces.
func estimateTokensForBytes(encoded []byte) int {
	ascii := 0
	for _, b := range encoded {
		if b < utf8.RuneSelf {
			ascii++
		}
	}
	nonASCII := len(encoded) - ascii
	return ascii/bytesPerTokenEstimate + nonASCII
}

// toolCallSem is the semaphore maxConcurrentToolCalls describes. Package-level
// rather than a field on Agent, since a fresh Agent is built for every turn
// (see NewAgent) - a per-Agent semaphore would protect nothing across the
// concurrent sessions this exists to bound in the first place.
var toolCallSem = make(chan struct{}, maxConcurrentToolCalls)

// run drives the loop, emitting events onto out. It always closes out.
//
// The caller must pass a context that already carries the request's query scope
// (see snapshotWarpContext). Every tool executes against this context, and
// queryscope treats a missing scope as "no restriction" - so a context that lost
// it returns the whole deployment to whoever asked.
func (a *Agent) Run(ctx context.Context, messages []schemas.ResponsesMessage, out chan<- Event) {
	defer close(out)

	emit := func(event Event) bool {
		// Try to deliver before considering the context. When the context is
		// already done and the buffer has room, both cases of a two-way select are
		// ready and Go picks at random - which drops the terminal error frame on a
		// cancelled or timed-out request about half the time, leaving the client
		// with neither an error nor a done frame. Cancellation still wins when the
		// reader is genuinely gone, because then the buffer fills and the send
		// below blocks.
		select {
		case out <- event:
			return true
		default:
		}
		select {
		case out <- event:
			return true
		case <-ctx.Done():
			// One more non-blocking attempt. The pre-check above only covers the
			// case where the buffer already had room; if it was full there and the
			// consumer drained it while this select was waiting, cancellation can
			// still win the race and drop a terminal frame on a client that is
			// very much still connected - a server-side timeout then reaches the
			// reader as neither an error nor a done.
			//
			// Non-blocking on purpose: a client that really is gone leaves the
			// buffer full, and blocking here would hold the stream open on it.
			select {
			case out <- event:
				return true
			default:
				return false
			}
		}
	}

	// a.tools is buildToolsFor's fixed output for this deployment's semantic
	// search availability (see NewAgent), so this is the matching memoized
	// declaration set - parsed once for the process, not once per turn.
	declared, err := declaredTools(a.deps != nil && a.deps.semantic != nil)
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
	instructions := systemInstructions(a.config, a.deps != nil && a.deps.semantic != nil, timeContext{timezone: a.timezone, utcOffsetMinutes: a.utcOffsetMinutes})
	finalInstructions := instructions + finalStepInstructions
	conversation := append([]schemas.ResponsesMessage{}, messages...)
	// conversationTokens tracks the running estimate behind
	// MaxConversationEstimatedTokens. Updated incrementally as conversation
	// grows rather than re-estimated from scratch each iteration, since only
	// what was just appended is new.
	conversationTokens := estimateMessageTokens(conversation...)
	var usage *schemas.BifrostLLMUsage
	// executed remembers every tool call that ran in an earlier step, keyed on
	// name and raw arguments, so an identical repeat is refused instead of
	// re-run. The value is the step it ran in, which is what the refusal points
	// the model at. Calls within one step are not checked against each other:
	// the per-turn cap already bounds those, and "step N" would name the step
	// in progress.
	executed := map[string]int{}
	// calledTool records whether any data tool ran this turn, and redirected bounds
	// the redirect below to once per turn, so a model that holds its ground
	// ends the turn rather than burning the step budget.
	// describedFilterSpace is whether describe_filter_space has answered this
	// turn, so the redirect does not send the model back to a call it would have
	// refused as a repeat.
	calledTool, describedFilterSpace, redirected := false, false, false
	// finalNudged and emptyRetried each bound a one-time message below.
	finalNudged, emptyRetried := false, false
	// issued is every Logs link a tool has returned, which is what the links in
	// an answer are checked against (see sanitizeAnswerLinks). Seeded from the
	// thread so far: tool results are not replayed across turns, only answers,
	// and a follow-up may fairly repeat a link an earlier answer carried.
	issued := issuedLinks{}
	issued.collect(responsesText(messages))
	// emitText streams one step's text as its own paragraph. Steps used to go
	// out back to back, so a turn that narrated between lookups read "...what
	// the root cause was.These are all overloaded_error" - live, in the saved
	// answer, and in the history replayed to the model. Only what the break is
	// missing is added, so text that already ends its paragraph is left alone.
	streamedTail := ""
	emitText := func(text string) bool {
		if text == "" {
			return true
		}
		if streamedTail != "" && !strings.HasPrefix(text, "\n\n") {
			switch {
			case strings.HasSuffix(streamedTail, "\n\n"):
			case strings.HasSuffix(streamedTail, "\n") || strings.HasPrefix(text, "\n"):
				text = "\n" + text
			default:
				text = "\n\n" + text
			}
		}
		streamedTail = text[max(0, len(text)-2):]
		return emit(Event{Type: EventDelta, Delta: text})
	}

	for iteration := 1; iteration <= a.maxIterations; iteration++ {
		if ctx.Err() != nil {
			// An expired budget and a client hang-up need different codes: the
			// first is ours, the second is the user's. Reporting both as
			// cancelled hides a Warp timeout as a user action.
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				emit(Event{Type: EventError, Code: ErrTimeout, Message: "request timed out", Usage: usage})
				return
			}
			emit(Event{Type: EventError, Code: ErrCancelled, Message: "request cancelled", Usage: usage})
			return
		}

		// The last step is answer-only. With tools still on offer the model
		// spends it on one more query and the whole run ends as an error that
		// discards everything the earlier steps learned. A budget of one step
		// is exempt: taking its tools away would leave Warp unable to look at
		// anything at all. The token check trips before conversationTokens
		// itself reaches the budget, once what's left is no longer enough to
		// cover one more tool round's worst case (maxToolRoundGrowthTokens) -
		// so whatever request comes next, tool round or answer-only, is built
		// from a conversation already known to fit, rather than discovering
		// after the fact that the just-finished round pushed it past what the
		// provider will accept.
		finalStep := (iteration == a.maxIterations || conversationTokens >= MaxConversationEstimatedTokens-maxToolRoundGrowthTokens) && a.maxIterations > 1
		params := &schemas.ResponsesParameters{Instructions: &instructions, Tools: declared, MaxOutputTokens: new(maxStepOutputTokens)}
		if finalStep {
			params = &schemas.ResponsesParameters{Instructions: &finalInstructions}
			// Said in the conversation as well as in the instructions. A final
			// request that ended on a tool result addressed nothing to the model
			// and offered no tool to call, and came back as an empty end_turn -
			// which discarded every step before it.
			if !finalNudged {
				finalNudged = true
				nudge := userNudge("This is your final step and no tools are available now. Write your answer from the results above: lead with what you found, then say plainly what you could not check.")
				conversation = append(conversation, nudge)
				conversationTokens += estimateMessageTokens(nudge)
			}
		}
		// Both unset by default, same as before either existed: an operator who
		// has not configured one gets the provider's own default, not a value
		// Warp picked for them. Applied to every step, including the answer-only
		// final one - a reasoning model changing mode mid-loop, or a deployment
		// running warmer for the finding step than the summarizing one, is not
		// something either field is configured per-step to express here.
		if a.config.Temperature != nil {
			params.Temperature = a.config.Temperature
		}
		if a.config.ReasoningEffort != "" {
			params.Reasoning = &schemas.ResponsesParametersReasoning{Effort: new(a.config.ReasoningEffort)}
		}

		response, bifrostErr := a.chat(ctx, &schemas.BifrostResponsesRequest{
			// The wire protocol, not the provider that serves the request: the
			// configured provider rides in the model string below. See
			// transportProvider.
			Provider: transportProvider(),
			// Qualified as provider/model so the routing on the other end cannot
			// substitute a different provider for the same model name.
			Model:  modelForRequest(a.config),
			Input:  conversation,
			Params: params,
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
			emit(Event{Type: EventError, Code: code, Message: errorMessage(bifrostErr), Usage: usage})
			return
		}
		// Counted before the reply is judged, so an empty one is still paid for
		// in what the turn reports.
		if response != nil {
			usage = accumulateUsage(usage, usageFromResponses(response.Usage), a.cost)
		}
		// A reply with no text and no tool call says nothing, whether it arrives
		// as an empty output list or as a message with empty text. It used to end
		// the turn outright - and, as a blank message, to end it as an empty
		// answer. It is asked again once, on the same step and with the same
		// tools, since nothing was learned that a step should be charged for. The
		// blank reply is not kept: providers reject an empty assistant message.
		if response == nil || (responsesText(response.Output) == "" && len(responsesToolCalls(response.Output)) == 0) {
			if emptyRetried {
				emit(Event{Type: EventError, Code: ErrUpstream, Message: "the model returned no output", Usage: usage})
				return
			}
			emptyRetried = true
			nudge := userNudge("Your last reply was empty. Continue: call a tool if you still need data and tools are available, otherwise write your answer from the results above.")
			conversation = append(conversation, nudge)
			conversationTokens += estimateMessageTokens(nudge)
			iteration--
			continue
		}

		// Every output item goes back verbatim, reasoning items included. Replaying
		// a reasoning model's own items is what lets it continue the thought it
		// started; dropping them makes each iteration start over.
		conversation = append(conversation, response.Output...)
		conversationTokens += estimateMessageTokens(response.Output...)

		// Repaired here, before anything streams, not only when the answer is
		// folded for saving: the model dropped the leading slash from its Logs
		// links, the raw delta reached the dashboard, and its markdown renderer
		// showed every such link as "[blocked]" until a reload served the saved,
		// repaired copy. What streams must be what is saved.
		text := sanitizeAnswerLinks(responsesText(response.Output), issued)
		toolCalls := responsesToolCalls(response.Output)

		// On the final step any text is the answer, tool calls or not: nothing
		// else can run, and an answer alongside an unrunnable call is still an
		// answer. It is marked partial because the model was cut off, and the
		// reader should weigh it accordingly.
		if len(toolCalls) == 0 || (finalStep && text != "") {
			// A reply that called no tool rests on nothing looked at. In practice
			// it was a question in prose with nothing to click ("I need to know
			// whose traffic you mean: ...", ending in a full stop, so no check on
			// its wording caught it) or a refusal to investigate what the tools
			// could have ("I'd need to inspect the failed requests"). Wording
			// checks kept missing variants; this one does not depend on wording.
			// After a tool has run, a question in prose is still caught by shape.
			// Held back and sent back once, while a later step can still offer
			// tools; the text has not been emitted, so the client never sees it.
			if !finalStep && !redirected && iteration+1 < a.maxIterations && (!calledTool || endsWithProseQuestion(text)) {
				redirected = true
				redirect := unsupportedReplyRedirect(calledTool, describedFilterSpace)
				conversation = append(conversation, redirect)
				conversationTokens += estimateMessageTokens(redirect)
				continue
			}
			if !emitText(text) {
				return
			}
			finishReason := "stop"
			if finalStep {
				finishReason = FinishReasonPartial
			}
			emit(Event{
				Type:         EventDone,
				FinishReason: finishReason,
				Iterations:   iteration,
				Usage:        usage,
			})
			return
		}
		if finalStep {
			// Asked for tools with none on offer and said nothing. Nothing is
			// left to run, so fall through to the budget error below.
			break
		}
		// Narration the model produced alongside its tool calls ("let me check
		// last week's spend") is worth showing: it is what makes the wait legible
		// rather than a spinner.
		if !emitText(text) {
			return
		}

		// This pass only decides, in order, which calls run - refusing what's
		// over the per-step cap or a repeat of an earlier step, stopping
		// outright at the first ask_user, and stopping outright the moment the
		// client is already known gone, exactly as a single sequential pass
		// over the whole batch would. The calls it queues instead of running
		// inline are what the prompt's "call them together" instruction (see
		// SystemPrompt) is actually asking the model to spend on one round
		// trip rather than several - see the concurrent phase below. results
		// is index-addressed so a queued call and one resolved immediately
		// right next to it still land back in conversation in the model's own
		// order, regardless of which finishes first - the one guarantee every
		// provider on the other end of the next request depends on.
		results := make([]*schemas.ResponsesMessage, len(toolCalls))
		type queuedCall struct {
			index          int
			id, name, args string
		}
		queued := make([]queuedCall, 0, min(len(toolCalls), MaxToolCallsPerTurn))
		// Written by each goroutine for its own index only, read after wg.Wait().
		succeeded := make([]bool, len(toolCalls))
		// Keys queued in this step, so a duplicate later in the same batch is
		// refused during the sequential queueing pass rather than run twice
		// concurrently below. executed[] is only written after wg.Wait().
		ranThisStep := make([]string, 0, len(toolCalls))
		var posedQuestion *Question

		for index, call := range toolCalls {
			// A cancelled ctx here means the client left sometime after an
			// earlier call in this same batch was announced (emit's own
			// ctx-awareness is not a reliable enough signal for this: with a
			// buffered channel a send can still succeed even after cancellation
			// races it). Stopping here is what keeps a slow first call from
			// quietly paying for three more nobody will read, the same
			// checkpoint a purely sequential pass gets for free between calls.
			// Whatever already queued in earlier iterations still runs below.
			if ctx.Err() != nil {
				break
			}

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
				msg := toolResultMessage(id,
					fmt.Sprintf(`{"error":"not run: no more than %d tools may be called in one step. Ask for the ones you need most, then continue."}`, MaxToolCallsPerTurn))
				results[index] = &msg
				continue
			}

			// ask_user is not a query, it is the end of the turn. Whatever was
			// already queued ahead of it below still runs - the same calls a
			// purely sequential pass would already have finished by the time it
			// reached this index - but nothing after it does, so the loop stops
			// here rather than continuing.
			if question := questionFromToolCall(name, arguments); question != nil {
				// Past the limit the model is stalling rather than narrowing, and
				// the person is being interrogated. Refusing as a tool result
				// rather than erroring the turn puts it back on the answering path
				// with what it already has, which is what they asked for. Refused
				// here rather than at the pose site below so the rest of the batch
				// still runs, the same way every other refusal in this loop works.
				if a.questionsAsked >= MaxConsecutiveQuestions {
					msg := toolResultMessage(id,
						fmt.Sprintf(`{"error":"not run: you have already asked %d questions in a row. Answer with the data you have, stating what you assumed."}`, a.questionsAsked))
					results[index] = &msg
					continue
				}
				posedQuestion = question
				break
			}

			// An identical call returns an identical result. Running it again
			// only burns a step, and a model that repeats itself is the shape
			// every runaway loop takes, so the repeat is refused with a pointer
			// to the step whose result it already has.
			key := name + "\x00" + arguments
			// ranThisStep is consulted as well as executed, because executed is
			// only written at the end of the step. A model that asks for the same
			// call twice in one batch - the shape a runaway loop takes - would
			// otherwise have both copies queued and run concurrently below,
			// spending two provider calls to produce the same bytes.
			if prior, repeated := stepOrPriorExecution(executed, ranThisStep, key, iteration); repeated {
				if !emit(Event{
					Type: EventToolCallStart, ToolID: id, ToolName: name,
					Arguments: arguments, Iteration: iteration,
				}) {
					return
				}
				refusal := fmt.Sprintf("not run: identical to your call in step %d, whose result you already have. Change the arguments, use a different tool, or answer from what you have.", prior)
				if !emit(Event{Type: EventToolCallEnd, ToolID: id, ToolName: name, Failed: true, ToolError: refusal}) {
					return
				}
				msg := toolResultMessage(id, `{"error":`+strconv.Quote(refusal)+`}`)
				results[index] = &msg
				continue
			}
			// Recorded before the call runs, not after: the guard above reads this
			// during the same sequential queueing pass, so a duplicate later in
			// this batch has to see the key already claimed. executed[] still gets
			// the authoritative write after wg.Wait() below.
			ranThisStep = append(ranThisStep, key)
			queued = append(queued, queuedCall{index: index, id: id, name: name, args: arguments})
		}

		// Run the queued calls together rather than one at a time. Each
		// goroutine only ever writes its own results[i] and calls emit, and a
		// channel send is safe from multiple goroutines at once, so nothing
		// here needs a lock; wg.Wait() below is what makes reading results
		// back afterward safe (the happens-before edge every prior Done gives
		// the next Wait).
		var wg sync.WaitGroup
		for _, q := range queued {
			wg.Add(1)
			go func(q queuedCall) {
				defer wg.Done()

				// Acquire a slot before doing any real work. Waiting here,
				// not skipping the call, is required: every function_call
				// the model sent needs a function_call_output back, or the
				// next request to the provider 400s (see the cap-refusal
				// comment above) - so this can only make a call wait its
				// turn, never drop it, the way toolCallSem's own doc comment
				// says. If the client leaves while queued for a slot, there
				// is nothing left to acquire it for.
				select {
				case toolCallSem <- struct{}{}:
				case <-ctx.Done():
					return
				}
				defer func() { <-toolCallSem }()

				// The wait for a slot can outlast the client's own patience -
				// select above can still pick the semaphore case in the same
				// instant ctx is cancelled, since it's not required to prefer
				// a ready Done(). Recheck here so a call that only just got
				// its turn does not run against a context that's already
				// gone, and so tool_call_start (emitted right below, right
				// before the call it describes) never goes out without the
				// tool_call_end that must follow it.
				if ctx.Err() != nil {
					return
				}

				if !emit(Event{
					Type: EventToolCallStart, ToolID: q.id, ToolName: q.name,
					Arguments: q.args, Iteration: iteration,
				}) {
					return
				}

				started := time.Now()
				result, failed := a.executeTool(ctx, q.name, q.args)
				end := Event{
					Type: EventToolCallEnd, ToolID: q.id, ToolName: q.name,
					DurationMs: time.Since(started).Milliseconds(), Failed: failed,
				}
				if failed {
					// The result *is* the error message on a failed call, and it is
					// already bounded, so it can be surfaced as-is.
					end.ToolError = result
				}
				// Best-effort: a false return means the client is gone, and ctx
				// cancellation - shared by every call in this batch - is what
				// the next iteration's own check acts on, not this return value.
				emit(end)
				msg := toolResultMessage(q.id, result)
				results[q.index] = &msg
				succeeded[q.index] = !failed
			}(q)
		}
		wg.Wait()

		for _, result := range results {
			if result != nil {
				conversation = append(conversation, *result)
				conversationTokens += estimateMessageTokens(*result)
				// Collected from the bounded result, so what counts as issued is
				// what the model was actually shown.
				issued.collect(*result.ResponsesToolMessage.Output.ResponsesToolCallOutputStr)
			}
		}
		// describe_filter_space only lists what can be filtered on. A turn that
		// looked at nothing else and then replied had fetched no data - in
		// practice it was a scope question in prose ("I need you to pick a
		// scope first: team, customer, or business unit."), so it is held to the
		// same redirect as a turn that called no tool. So is a turn whose calls
		// all failed or were refused: asking for data is not having it.
		for _, q := range queued {
			if !succeeded[q.index] {
				continue
			}
			if q.name == "describe_filter_space" {
				describedFilterSpace = true
			} else {
				calledTool = true
			}
		}
		for _, q := range queued {
			// Only a call that worked is worth refusing to repeat. The guard's
			// premise is that an identical call returns an identical result,
			// which holds for a success and not for a transient store or
			// provider failure - recording those blocked the model from ever
			// retrying the one query it actually needed.
			if succeeded[q.index] {
				executed[q.name+"\x00"+q.args] = iteration
			}
		}

		if posedQuestion != nil {
			if !emit(Event{Type: EventQuestion, Question: posedQuestion}) {
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
	}

	// Out of iterations. Terminal error, no done frame. Usage rides along: this
	// is the run that spent the most, and it is the only report of that spend.
	emit(Event{
		Type:    EventError,
		Code:    ErrMaxIterations,
		Message: fmt.Sprintf("Warp reached its limit of %d research steps without settling on an answer. Try a narrower question.", a.maxIterations),
		Usage:   usage,
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

	// An argument the tool does not take is refused rather than dropped. Dropped,
	// the call runs as if it had been honoured, and the model reads the result
	// as shaped by an argument that never existed - then guesses another.
	if accepted := tool.argumentNames(); len(accepted) > 0 {
		unknown := []string{}
		for name := range args {
			if !slices.Contains(accepted, name) {
				unknown = append(unknown, name)
			}
		}
		if len(unknown) > 0 {
			slices.Sort(unknown)
			return fmt.Sprintf(`{"error":%q}`, fmt.Sprintf("%s does not take %s. Its arguments are: %s. Filters such as time range, provider, model and status go inside filters.",
				name, strings.Join(unknown, ", "), strings.Join(accepted, ", "))), true
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
// endsWithProseQuestion reports whether an answer ends by asking the person
// something. An answer carrying a provenance block is a finished answer even
// if a sentence in it ends on a question mark.
func endsWithProseQuestion(text string) bool {
	text = strings.TrimSpace(text)
	return strings.HasSuffix(text, "?") && !strings.Contains(text, "```warp-scope")
}

// unsupportedReplyRedirect is the one-time nudge sent back for a reply that
// called no tool, or that asked in prose after one did. It rides as a user
// message because it is feedback on the last reply, not a standing
// instruction.
// userNudge builds a user-role message from the loop itself: feedback on the
// conversation so far rather than a standing instruction, which is why it rides
// in the transcript and not in the system prompt.
func userNudge(content string) schemas.ResponsesMessage {
	itemType := schemas.ResponsesMessageTypeMessage
	role := schemas.ResponsesInputMessageRoleUser
	return schemas.ResponsesMessage{Type: &itemType, Role: &role, Content: &schemas.ResponsesMessageContent{ContentStr: &content}}
}

func unsupportedReplyRedirect(calledTool, describedFilterSpace bool) schemas.ResponsesMessage {
	itemType := schemas.ResponsesMessageTypeMessage
	role := schemas.ResponsesInputMessageRoleUser
	content := "That reply asked a question in prose, which reaches the person with nothing to click. " +
		"If you need to ask, call " + AskUserTool + " with the options instead. If you did not need to ask, answer without the question."
	if !calledTool {
		options := "call describe_filter_space first so the options are ones that have traffic, and offer the person's own traffic only if describe_filter_space says they are identified. "
		if describedFilterSpace {
			options = "describe_filter_space has already run this turn, so build the options from the result you have rather than calling it again, and offer the person's own traffic only if it says they are identified. "
		}
		content = "That reply rests on no data: no tool has returned any this turn. " +
			"If it asks the person something, call " + AskUserTool + " with options instead - " + options +
			"If it says the question cannot be answered, investigate with your tools first - query_usage_by with dimension error_type and status error counts every failure by kind, query_logs returns failed rows with error_type, provider and model, and get_request_trace explains one request. " +
			"If it answers from results already in this conversation, or declines a question outside what your tools cover, give the same reply again."
	}
	return schemas.ResponsesMessage{Type: &itemType, Role: &role, Content: &schemas.ResponsesMessageContent{ContentStr: &content}}
}

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

// stepOrPriorExecution reports whether this exact call already ran, either in
// an earlier step or earlier in this one.
func stepOrPriorExecution(executed map[string]int, ranThisStep []string, key string, iteration int) (int, bool) {
	if prior, ok := executed[key]; ok {
		return prior, true
	}
	for _, ran := range ranThisStep {
		if ran == key {
			return iteration, true
		}
	}
	return 0, false
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
	converted := &schemas.BifrostLLMUsage{
		PromptTokens:     usage.InputTokens,
		CompletionTokens: usage.OutputTokens,
		TotalTokens:      usage.TotalTokens,
		Cost:             usage.Cost,
	}
	// The breakdowns travel too. Copying only the scalars left
	// PromptTokensDetails and CompletionTokensDetails nil on every real turn, so
	// the merge in accumulateUsage had nothing to accumulate. It also loses the
	// cached-read count, which CalculateCostForUsage prices lower than fresh
	// input - so a cached turn was reported as costing full price.
	if details := usage.InputTokensDetails; details != nil {
		converted.PromptTokensDetails = &schemas.ChatPromptTokensDetails{
			TextTokens:              details.TextTokens,
			AudioTokens:             details.AudioTokens,
			ImageTokens:             details.ImageTokens,
			CachedReadTokens:        details.CachedReadTokens,
			CachedWriteTokens:       details.CachedWriteTokens,
			CachedWriteTokenDetails: details.CachedWriteTokenDetails,
		}
	}
	if details := usage.OutputTokensDetails; details != nil {
		converted.CompletionTokensDetails = &schemas.ChatCompletionTokensDetails{
			TextTokens:               details.TextTokens,
			AudioTokens:              details.AudioTokens,
			ReasoningTokens:          details.ReasoningTokens,
			AcceptedPredictionTokens: details.AcceptedPredictionTokens,
			RejectedPredictionTokens: details.RejectedPredictionTokens,
			ImageTokens:              details.ImageTokens,
			CitationTokens:           details.CitationTokens,
			NumSearchQueries:         details.NumSearchQueries,
		}
	}
	return converted
}

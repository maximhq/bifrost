package heimdall

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// The engine replays a session against one model. It answers every tool call from a cassette and has
// nothing else to answer with: its only handle to Bifrost is the replay client, which has no MCP
// manager, and nothing in this file executes a tool. A call that is not in the recording stops the
// replay.

// extraTurns is how many model turns beyond the recording's own a replay may take.
const extraTurns = 5

// paramSupport is the part of the model catalog that lists the parameters a model accepts. It is not
// on schemas.ModelInfoProvider; *modelcatalog.ModelCatalog implements it.
type paramSupport interface {
	GetSupportedParameters(model string) []string
}

// EngineOptions are the settings of one run. The zero value of each is a sane default or "off".
type EngineOptions struct {
	CallTimeout time.Duration // per model call; 0 takes the default
	MaxSteps    int           // caps the turns of one replay; 0 is no cap
	Rules       ToolRules
	Budget      *Budget // nil is unlimited
	// Pricing prices each reply for the budget. Leave it nil, not a nil pointer in the interface, when
	// there is no catalog; replies then cost nothing and only the call cap applies.
	Pricing schemas.ModelInfoProvider
	// Params lists the parameters each model accepts. Nil sends every kept parameter.
	Params paramSupport
}

// Engine replays sessions. Build one per run, over a client from newReplayClient.
type Engine struct {
	client *bifrost.Bifrost
	opts   EngineOptions
}

// NewEngine returns an engine that calls models through client.
func NewEngine(client *bifrost.Bifrost, opts EngineOptions) *Engine {
	if opts.CallTimeout <= 0 {
		opts.CallTimeout = defaultCallTimeout
	}
	return &Engine{client: client, opts: opts}
}

// turnLimit is how many model turns a replay of t may take: the recording's actions plus a few, capped.
func (e *Engine) turnLimit(t *SessionTrace) int {
	limit := len(t.Actions) + extraTurns
	if e.opts.MaxSteps > 0 {
		limit = min(limit, e.opts.MaxSteps)
	}
	return limit
}

// ReplayOnce replays one session against route. It always returns a result; the outcome says how it ended.
// A result with outcome not_hermetic means the run must stop.
func (e *Engine) ReplayOnce(ctx context.Context, t *SessionTrace, route Route, arm Arm, attempt int) *ReplayResult {
	res := &ReplayResult{SessionID: t.SessionID, Arm: arm, Attempt: attempt}
	if err := CheckCandidate(route); err != nil {
		return res.end(ReplayInfraError, err)
	}
	cassette := t.NewCassette(e.opts.Rules)
	messages := slices.Clone(t.Seed)
	var supported []string
	if e.opts.Params != nil {
		supported = e.opts.Params.GetSupportedParameters(route.Model)
	}

	for turn := 1; turn <= e.turnLimit(t); turn++ {
		if err := assertHermetic(e.client); err != nil {
			return res.end(ReplayNotHermetic, err)
		}
		if err := ctx.Err(); err != nil {
			return res.end(ReplayCancelled, err)
		}
		if e.opts.Budget != nil && !e.opts.Budget.Reserve() {
			return res.end(ReplayBudgetStopped, nil)
		}

		reply, outcome, err := e.call(ctx, buildRequest(route, messages, t, supported), res)
		if err != nil {
			return res.end(outcome, err)
		}

		msg := reply.Choices[0].Message
		var calls []schemas.ChatAssistantMessageToolCall
		if msg.ChatAssistantMessage != nil {
			calls = msg.ToolCalls
		}
		if reason := reply.Choices[0].FinishReason; reason != nil {
			res.FinishReason = *reason
		}
		if len(calls) == 0 {
			text, _ := messageText(msg.Content)
			res.Actions = append(res.Actions, Action{Kind: ActionFinal, Text: text})
			return res.end(ReplayFinished, nil)
		}

		assistant, calls := withCallIDs(*msg, turn)
		results := make([]recordedCall, len(calls))
		replayed := make([]ToolCall, len(calls))
		missed := false
		for i, c := range calls {
			name := ""
			if c.Function.Name != nil {
				name = *c.Function.Name
			}
			result, isError, outcome := cassette.Lookup(name, c.Function.Arguments)
			results[i] = recordedCall{Name: name, Result: result, IsError: isError}
			replayed[i] = ToolCall{Name: name, Args: CanonicalArgs(c.Function.Arguments, nil), Outcome: outcome}
			if outcome != OutcomeHit {
				missed = true
			}
		}
		res.Actions = append(res.Actions, sortedGroup(replayed))
		if missed {
			return res.end(ReplayStoppedMiss, nil)
		}

		messages = append(messages, assistant)
		for i, c := range calls {
			messages = append(messages, toolMessage(*c.ID, results[i]))
		}
	}
	return res.end(ReplayTurnLimit, nil)
}

// call makes one model call and books its usage. On failure it returns the outcome to end the replay with.
func (e *Engine) call(ctx context.Context, req *schemas.BifrostChatRequest, res *ReplayResult) (*schemas.BifrostChatResponse, ReplayOutcome, error) {
	bctx, cancel := schemas.NewBifrostContextWithTimeout(ctx, e.opts.CallTimeout)
	defer cancel()

	started := time.Now()
	reply, bErr := e.client.ChatCompletionRequest(bctx, req)
	res.Usage.Calls++
	res.Usage.LatencyMS += float64(time.Since(started)) / float64(time.Millisecond)

	if bErr != nil {
		if err := ctx.Err(); err != nil {
			return nil, ReplayCancelled, err
		}
		return nil, classify(bErr), errors.New(errorText(bErr))
	}
	if reply == nil || len(reply.Choices) == 0 || reply.Choices[0].ChatNonStreamResponseChoice == nil || reply.Choices[0].Message == nil {
		return nil, ReplayInfraError, errors.New("the provider's reply has no message")
	}
	if u := reply.Usage; u != nil {
		res.Usage.PromptTokens += u.PromptTokens
		res.Usage.CompletionTokens += u.CompletionTokens
		res.Usage.TotalTokens += u.TotalTokens
	}
	var cost float64
	if e.opts.Pricing != nil {
		cost = e.opts.Pricing.CalculateRequestCost(bctx, &schemas.BifrostResponse{ChatResponse: reply})
	}
	res.Usage.CostUSD += cost
	if e.opts.Budget != nil {
		e.opts.Budget.Add(cost)
	}
	return reply, "", nil
}

// end sets how the replay ended and returns it.
func (r *ReplayResult) end(outcome ReplayOutcome, err error) *ReplayResult {
	r.Outcome = outcome
	if err != nil {
		r.Error = err.Error()
	}
	return r
}

// classify tells a model's behaviour from a fault that says nothing about it. A request the provider
// refuses is the model's doing; everything else is not.
func classify(bErr *schemas.BifrostError) ReplayOutcome {
	if bErr.StatusCode != nil && (*bErr.StatusCode == 400 || *bErr.StatusCode == 422) {
		return ReplayProviderRejected
	}
	return ReplayInfraError
}

func errorText(bErr *schemas.BifrostError) string {
	status := ""
	if bErr.StatusCode != nil {
		status = fmt.Sprintf("status %d: ", *bErr.StatusCode)
	}
	if bErr.Error != nil && bErr.Error.Message != "" {
		return status + bErr.Error.Message
	}
	return status + "the provider call failed"
}

// buildRequest makes the request for one turn from the allow-list and nothing else. Parameters the model
// does not accept are left out when the catalog lists what it accepts (nil lists everything). No
// fallbacks are set, so a failing model is never rescued by another.
func buildRequest(route Route, messages []schemas.ChatMessage, t *SessionTrace, supported []string) *schemas.BifrostChatRequest {
	keep := func(names ...string) bool {
		return supported == nil || slices.ContainsFunc(names, func(n string) bool { return slices.Contains(supported, n) })
	}
	rp := t.Params
	p := &schemas.ChatParameters{}
	if keep("temperature") {
		p.Temperature = rp.Temperature
	}
	if keep("top_p") {
		p.TopP = rp.TopP
	}
	p.TopK = rp.TopK // the catalog does not list it, and compat leaves it alone too
	if keep("frequency_penalty") {
		p.FrequencyPenalty = rp.FrequencyPenalty
	}
	if keep("presence_penalty") {
		p.PresencePenalty = rp.PresencePenalty
	}
	if keep("max_completion_tokens", "max_tokens") {
		p.MaxCompletionTokens = rp.MaxCompletionTokens
	}
	if keep("stop") {
		p.Stop = rp.Stop
	}
	if keep("seed") {
		p.Seed = rp.Seed
	}
	if keep("response_format") {
		p.ResponseFormat = rp.ResponseFormat
	}
	if keep("tool_choice") {
		p.ToolChoice = rp.ToolChoice
	}
	if keep("parallel_tool_calls") {
		p.ParallelToolCalls = rp.ParallelToolCalls
	}
	if keep("reasoning") {
		p.Reasoning = rp.Reasoning
	}
	if keep("verbosity") {
		p.Verbosity = rp.Verbosity
	}
	if keep("tools") {
		for _, tool := range t.Tools {
			if tool.Type != schemas.ChatToolTypeFunction || tool.Function == nil {
				continue
			}
			fn := *tool.Function
			p.Tools = append(p.Tools, schemas.ChatTool{Type: tool.Type, Function: &fn})
		}
	}
	return &schemas.BifrostChatRequest{Provider: route.Provider, Model: route.Model, Input: slices.Clone(messages), Params: p}
}

// withCallIDs returns the model's own message and calls, adding an ID to any call that came back
// without one so each can be answered. A message whose calls all have IDs is returned as it came.
func withCallIDs(msg schemas.ChatMessage, turn int) (schemas.ChatMessage, []schemas.ChatAssistantMessageToolCall) {
	if msg.Role == "" {
		msg.Role = schemas.ChatMessageRoleAssistant
	}
	calls := msg.ToolCalls
	if !slices.ContainsFunc(calls, func(c schemas.ChatAssistantMessageToolCall) bool { return c.ID == nil || *c.ID == "" }) {
		return msg, calls
	}
	calls = slices.Clone(calls)
	for i := range calls {
		if calls[i].ID == nil || *calls[i].ID == "" {
			calls[i].ID = schemas.Ptr(fmt.Sprintf("heimdall_%d_%d", turn, i))
		}
	}
	assistant := *msg.ChatAssistantMessage
	assistant.ToolCalls = calls
	msg.ChatAssistantMessage = &assistant
	return msg, calls
}

// toolMessage answers one call with its recorded result.
func toolMessage(callID string, rc recordedCall) schemas.ChatMessage {
	result := rc.Result
	tm := &schemas.ChatToolMessage{ToolCallID: &callID}
	if rc.IsError {
		tm.IsError = schemas.Ptr(true)
	}
	return schemas.ChatMessage{
		Role:            schemas.ChatMessageRoleTool,
		Content:         &schemas.ChatMessageContent{ContentStr: &result},
		ChatToolMessage: tm,
	}
}

// sortedGroup puts a turn's parallel calls in the order a recording's are in.
func sortedGroup(calls []ToolCall) Action {
	slices.SortStableFunc(calls, func(a, b ToolCall) int {
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.Args, b.Args)
	})
	return Action{Kind: ActionTools, Calls: calls}
}

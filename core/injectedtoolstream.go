package bifrost

import (
	"context"
	"sort"
	"strings"
	"sync/atomic"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// Streaming for provider-injected tools. One client stream spans several upstream
// streams: each turn's text streams to the client live, injected tool calls are held
// back and executed between turns, and only the last turn ends the client stream.
//
// The state below lives on the session, owned by the goroutines of one client stream,
// never on the BifrostContext (stream-sized data stays out of ctx).

// streamEmit is one chunk to send to the client. terminal marks the chunk that ends the
// client stream; it is the only one post-hooks see as the final chunk.
type streamEmit struct {
	resp     *schemas.BifrostChatResponse
	terminal bool
}

// streamedCall accumulates one tool call's deltas within a turn.
type streamedCall struct {
	index        uint16
	id           *string
	callType     *string
	name         *string
	arguments    strings.Builder
	extraContent []byte
}

func (c *streamedCall) toolCall() schemas.ChatAssistantMessageToolCall {
	call := schemas.ChatAssistantMessageToolCall{
		Index:    c.index,
		ID:       c.id,
		Type:     c.callType,
		Function: schemas.ChatAssistantMessageToolCallFunction{Name: c.name, Arguments: c.arguments.String()},
	}
	if len(c.extraContent) > 0 {
		call.ExtraContent = c.extraContent
	}
	return call
}

// injectedChatStream turns a sequence of upstream chat streams into one client stream.
type injectedChatStream struct {
	set   *injectedToolSet
	depth int // turns started, including the current one

	// Client stream identity, fixed by the first chunk of the first turn.
	id         string
	created    int
	roleSent   bool
	chunkIndex int
	usage      *schemas.BifrostLLMUsage // summed over finished turns

	// Current turn.
	text      strings.Builder
	reasoning strings.Builder
	details   map[int]*schemas.ChatReasoningDetails
	calls     map[uint16]*streamedCall
	finish    *string
	turnUsage *schemas.BifrostLLMUsage
	more      bool // set on the turn's final chunk: injected calls are pending
}

func newInjectedChatStream(set *injectedToolSet) *injectedChatStream {
	return &injectedChatStream{set: set, depth: 1}
}

// onChunk takes one upstream chunk and returns what the client should see. final is
// true for the last chunk of the upstream stream.
func (s *injectedChatStream) onChunk(resp *schemas.BifrostChatResponse, final bool) []streamEmit {
	if s.id == "" {
		s.id, s.created = resp.ID, resp.Created
	}
	if resp.Usage != nil {
		s.turnUsage = resp.Usage
	}
	var visible *schemas.ChatStreamResponseChoiceDelta
	if len(resp.Choices) > 0 {
		choice := resp.Choices[0]
		if choice.FinishReason != nil {
			s.finish = choice.FinishReason
		}
		if choice.ChatStreamResponseChoice != nil && choice.Delta != nil {
			visible = s.absorbDelta(choice.Delta)
		}
	}

	var emits []streamEmit
	if visible != nil {
		emits = append(emits, streamEmit{resp: s.clientChunk(resp, visible, nil)})
	}
	if !final {
		return emits
	}

	var injected, client []schemas.ChatAssistantMessageToolCall
	for _, call := range s.orderedCalls() {
		if call.name != nil && s.set.isInjected(*call.name) {
			injected = append(injected, call.toolCall())
		} else {
			client = append(client, call.toolCall())
		}
	}
	if len(injected) > 0 && s.depth < s.set.maxDepth {
		s.more = true
		s.usage = schemas.MergeBifrostLLMUsage(s.usage, s.turnUsage)
		s.turnUsage = nil
		return emits
	}
	s.more = false

	finish := s.finish
	if len(injected) > 0 && len(client) == 0 {
		finish = new(string(schemas.BifrostFinishReasonStop))
	}
	terminal := s.clientChunk(resp, &schemas.ChatStreamResponseChoiceDelta{ToolCalls: client}, finish)
	terminal.Usage = schemas.MergeBifrostLLMUsage(s.usage, s.turnUsage)
	return append(emits, streamEmit{resp: terminal, terminal: true})
}

// absorbDelta records the delta for the turn and returns its client-visible part, or
// nil when it carries nothing the client should see now. Every tool call is held:
// whether a call is injected is only known from its name, and a client call can stream
// before an injected one in the same turn, which must then be dropped.
func (s *injectedChatStream) absorbDelta(delta *schemas.ChatStreamResponseChoiceDelta) *schemas.ChatStreamResponseChoiceDelta {
	for _, call := range delta.ToolCalls {
		s.absorbCall(call)
	}
	if delta.Content != nil {
		s.text.WriteString(*delta.Content)
	}
	if delta.Reasoning != nil {
		s.reasoning.WriteString(*delta.Reasoning)
	}
	for _, detail := range delta.ReasoningDetails {
		s.absorbReasoningDetail(detail)
	}

	visible := *delta
	visible.ToolCalls = nil
	if visible.Role != nil {
		if s.roleSent {
			visible.Role = nil
		}
		s.roleSent = true
	}
	if visible.Role == nil && visible.Content == nil && visible.Refusal == nil && visible.Audio == nil &&
		visible.Reasoning == nil && len(visible.ReasoningDetails) == 0 && len(visible.Annotations) == 0 && len(visible.ExtraContent) == 0 {
		return nil
	}
	return &visible
}

func (s *injectedChatStream) absorbCall(delta schemas.ChatAssistantMessageToolCall) {
	if s.calls == nil {
		s.calls = make(map[uint16]*streamedCall)
	}
	call, ok := s.calls[delta.Index]
	if !ok {
		call = &streamedCall{index: delta.Index}
		s.calls[delta.Index] = call
	}
	if delta.ID != nil && *delta.ID != "" {
		call.id = delta.ID
	}
	if delta.Type != nil {
		call.callType = delta.Type
	}
	if delta.Function.Name != nil && *delta.Function.Name != "" {
		call.name = delta.Function.Name
	}
	call.arguments.WriteString(delta.Function.Arguments)
	if len(delta.ExtraContent) > 0 {
		call.extraContent = delta.ExtraContent
	}
}

// absorbReasoningDetail merges reasoning detail fragments by index, so a thinking
// block and the signature that arrives after it replay as one block.
func (s *injectedChatStream) absorbReasoningDetail(fragment schemas.ChatReasoningDetails) {
	if s.details == nil {
		s.details = make(map[int]*schemas.ChatReasoningDetails)
	}
	detail, ok := s.details[fragment.Index]
	if !ok {
		cp := fragment
		s.details[fragment.Index] = &cp
		return
	}
	appendStr := func(dst **string, add *string) {
		if add == nil {
			return
		}
		if *dst == nil {
			*dst = new(*add)
			return
		}
		*dst = new(**dst + *add)
	}
	appendStr(&detail.Text, fragment.Text)
	appendStr(&detail.Summary, fragment.Summary)
	appendStr(&detail.Data, fragment.Data)
	if fragment.Signature != nil {
		detail.Signature = fragment.Signature
	}
	if fragment.ID != nil {
		detail.ID = fragment.ID
	}
	if fragment.Type != "" {
		detail.Type = fragment.Type
	}
}

func (s *injectedChatStream) orderedCalls() []*streamedCall {
	calls := make([]*streamedCall, 0, len(s.calls))
	for _, call := range s.calls {
		calls = append(calls, call)
	}
	sort.Slice(calls, func(i, j int) bool { return calls[i].index < calls[j].index })
	return calls
}

// clientChunk builds a client chunk from an upstream one: the first turn's id and
// creation time, the running chunk index, and the given delta and finish reason.
func (s *injectedChatStream) clientChunk(upstream *schemas.BifrostChatResponse, delta *schemas.ChatStreamResponseChoiceDelta, finish *string) *schemas.BifrostChatResponse {
	out := *upstream
	out.ID = s.id
	out.Created = s.created
	out.Usage = nil
	out.ExtraFields.ChunkIndex = s.chunkIndex
	out.ExtraFields.RawResponse = nil
	s.chunkIndex++
	out.Choices = []schemas.BifrostResponseChoice{{
		FinishReason:             finish,
		ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{Delta: delta},
	}}
	return &out
}

// continues reports whether the turn that just ended left injected calls to execute.
func (s *injectedChatStream) continues() bool {
	return s.more
}

// endTurn closes the turn that just ended with injected calls pending. It returns the
// assistant message to replay, carrying only the injected calls, and those calls.
func (s *injectedChatStream) endTurn() (schemas.ChatMessage, []schemas.ChatAssistantMessageToolCall) {
	var injected []schemas.ChatAssistantMessageToolCall
	for _, call := range s.orderedCalls() {
		if call.name != nil && s.set.isInjected(*call.name) {
			injected = append(injected, call.toolCall())
		}
	}
	assistant := schemas.ChatMessage{
		Role:                 schemas.ChatMessageRoleAssistant,
		ChatAssistantMessage: &schemas.ChatAssistantMessage{ToolCalls: injected},
	}
	if s.text.Len() > 0 {
		assistant.Content = &schemas.ChatMessageContent{ContentStr: new(s.text.String())}
	}
	if s.reasoning.Len() > 0 {
		assistant.Reasoning = new(s.reasoning.String())
	}
	if len(s.details) > 0 {
		indexes := make([]int, 0, len(s.details))
		for index := range s.details {
			indexes = append(indexes, index)
		}
		sort.Ints(indexes)
		for _, index := range indexes {
			assistant.ReasoningDetails = append(assistant.ReasoningDetails, *s.details[index])
		}
	}

	s.depth++
	s.text.Reset()
	s.reasoning.Reset()
	s.details = nil
	s.calls = nil
	s.finish = nil
	s.turnUsage = nil
	s.more = false
	return assistant, injected
}

// injectedStreamSkip tells the provider not to send a chunk itself: the session has
// already sent whatever the client should see for it.
var injectedStreamSkip = &schemas.BifrostError{StreamControl: &schemas.StreamControl{SkipStream: new(true)}}

// forwardUpstreamChunks drains one turn's provider channel into out. The session sends
// everything itself; anything a provider sent directly (outside its post-hook runner) is
// forwarded unchanged. The provider already passed those chunks through the pause/resume
// gate, so this is a plain send: gating again would buffer or replay them twice. After a
// cancel it keeps draining so the provider never blocks on a full channel.
func forwardUpstreamChunks(ctx *schemas.BifrostContext, upstream chan *schemas.BifrostStreamChunk, out chan *schemas.BifrostStreamChunk) {
	for chunk := range upstream {
		if chunk == nil {
			continue
		}
		select {
		case out <- chunk:
		case <-ctx.Done():
		}
	}
}

// sendThroughPostHooks runs a client chunk through the real post-hook runner and sends
// it, the way providerUtils.ProcessAndSendResponse does for an ordinary stream.
func sendThroughPostHooks(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError, out chan *schemas.BifrostStreamChunk) {
	processed, processedErr := postHookRunner(ctx, result, bifrostErr)
	if providerUtils.HandleStreamControlSkip(processedErr) {
		return
	}
	providerUtils.GateSendChunk(ctx, providerUtils.BuildClientStreamChunk(ctx, processed, processedErr), out)
}

// injectedTurnsCancelledError ends a client stream cancelled between turns, while an
// injected tool ran and no upstream stream was open to report it.
func injectedTurnsCancelledError(usage *schemas.BifrostLLMUsage) *schemas.BifrostError {
	bifrostErr := &schemas.BifrostError{
		StatusCode: new(499), // Client Closed Request
		Error: &schemas.ErrorField{
			Message: "Request cancelled: client disconnected",
			Type:    new(schemas.RequestCancelled),
		},
	}
	billEarlierTurns(bifrostErr, usage)
	return bifrostErr
}

// resetStreamTurnState clears the per-stream flags the previous upstream stream left on
// the shared context, as the retry path does between attempts. The provider releases
// its response (claiming BifrostContextKeyConnectionClosed) before it closes its
// channel, so once the channel has drained these flags describe a dead stream; left in
// place, the next turn's reader would see its own fresh stream as already closed.
func resetStreamTurnState(ctx *schemas.BifrostContext) {
	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, false)
	ctx.ClearValue(schemas.BifrostContextKeyConnectionClosed)
	ctx.ClearValue(schemas.BifrostContextKeyStreamBodyExhausted)
	ctx.ClearValue(schemas.BifrostContextKeyStreamParkedAfterFinish)
}

// clientEmit is one chunk the session sends to the client.
type clientEmit struct {
	result   *schemas.BifrostResponse
	terminal bool
}

// injectedStreamTurns adapts one API's stream session to chainInjectedStream.
type injectedStreamTurns struct {
	// process returns what the client sees for one upstream chunk; ok is false for a
	// chunk this API does not carry, which is passed through unchanged.
	process func(result *schemas.BifrostResponse, final bool) (emits []clientEmit, ok bool)
	// continues reports whether the turn that just ended has injected calls pending.
	continues func() bool
	// usage is the usage of every turn that has finished, billed onto an error that ends
	// the client stream.
	usage func() *schemas.BifrostLLMUsage
	// advance executes the pending injected calls and prepares the next turn's request.
	advance func()
	// dispatch opens the current turn's upstream stream.
	dispatch func(runner schemas.PostHookRunner, finalizer func(context.Context)) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError)
}

// chainInjectedStream serves one streaming attempt whose provider injects tools. It
// returns the client channel at once; a goroutine chains upstream turns into it until
// a turn ends without injected calls.
//
// The session runs inside the provider's post-hook runner, which the provider calls in
// order with its own StreamEndIndicator writes. For every upstream chunk it sends what
// the client should see (zero or more chunks) through the real post hooks, then tells
// the provider to skip the chunk. Post hooks therefore see exactly the client stream:
// one final chunk, carrying usage for every turn.
//
// Providers call the finalizer they are given when their stream ends. Each turn gets
// one that does nothing until the client stream has ended, because the real finalizer
// releases the plugin pipeline that later turns still run chunks through.
func chainInjectedStream(ctx *schemas.BifrostContext, turns injectedStreamTurns, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context)) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	out := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)
	var ended atomic.Bool

	runner := func(c *schemas.BifrostContext, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		if bifrostErr != nil || result == nil {
			if bifrostErr != nil {
				ended.Store(true)
				billEarlierTurns(bifrostErr, turns.usage())
				c.SetValue(schemas.BifrostContextKeyStreamTurnPending, false)
			}
			sendThroughPostHooks(c, postHookRunner, result, bifrostErr, out)
			return nil, injectedStreamSkip
		}
		final := IsFinalChunk(c)
		emits, ok := turns.process(result, final)
		if !ok {
			sendThroughPostHooks(c, postHookRunner, result, nil, out)
			return nil, injectedStreamSkip
		}
		for _, emit := range emits {
			c.SetValue(schemas.BifrostContextKeyStreamEndIndicator, emit.terminal)
			if emit.terminal {
				ended.Store(true)
				c.SetValue(schemas.BifrostContextKeyStreamTurnPending, false)
				// The client's last chunk goes back to the provider, which sends it and
				// completes the request's LLM span with it, as for any stream. Skipping it
				// would hand the provider the skip signal as the span's error.
				return postHookRunner(c, emit.result, nil)
			}
			sendThroughPostHooks(c, postHookRunner, emit.result, nil, out)
		}
		if final && turns.continues() {
			// Another turn follows: keep the LLM span open past this stream's end.
			c.SetValue(schemas.BifrostContextKeyStreamTurnPending, true)
		}
		c.SetValue(schemas.BifrostContextKeyStreamEndIndicator, final)
		return nil, injectedStreamSkip
	}
	turnFinalizer := func(c context.Context) {
		if ended.Load() {
			postHookSpanFinalizer(c)
		}
	}

	upstream, bifrostErr := turns.dispatch(runner, turnFinalizer)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	go func() {
		failed := false
		defer func() {
			providerUtils.CloseStream(ctx, out)
			// A stream ending between turns (cancel, a turn that failed to open, an upstream
			// that died without a final chunk) never reached a provider's completion path,
			// so complete the LLM span here; a failure marks it failed. A normal end has
			// already completed it, and this only runs the finalizer, which is idempotent.
			ctx.SetValue(schemas.BifrostContextKeyStreamTurnPending, false)
			if failed || !ended.Load() {
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, false)
			}
			providerUtils.EnsureStreamFinalizerCalled(ctx, postHookSpanFinalizer)
		}()
		for {
			forwardUpstreamChunks(ctx, upstream, out)
			if ended.Load() || !turns.continues() {
				return
			}
			resetStreamTurnState(ctx)
			// A cancel while no upstream stream is open (before or during the injected
			// calls) has no provider to report it, so the stream ends here.
			cancelled := func() bool {
				if ctx.Err() == nil {
					return false
				}
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				ended.Store(true)
				sendThroughPostHooks(ctx, postHookRunner, nil, injectedTurnsCancelledError(turns.usage()), out)
				failed = true
				return true
			}
			if cancelled() {
				return
			}
			turns.advance()
			if cancelled() {
				return
			}
			upstream, bifrostErr = turns.dispatch(runner, turnFinalizer)
			if bifrostErr != nil {
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
				ended.Store(true)
				billEarlierTurns(bifrostErr, turns.usage())
				sendThroughPostHooks(ctx, postHookRunner, nil, bifrostErr, out)
				failed = true
				return
			}
		}
	}()
	return out, nil
}

// startInjectedChatStream serves a streaming chat attempt whose provider injects tools.
func (bifrost *Bifrost) startInjectedChatStream(ctx *schemas.BifrostContext, provider schemas.Provider, config *schemas.ProviderConfig, key schemas.Key, original *schemas.BifrostChatRequest, set *injectedToolSet, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context)) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	session := newInjectedChatStream(set)
	turn := applyInjectedToolsChat(original, set)
	return chainInjectedStream(ctx, injectedStreamTurns{
		process: func(result *schemas.BifrostResponse, final bool) ([]clientEmit, bool) {
			if result.ChatResponse == nil {
				return nil, false
			}
			var emits []clientEmit
			for _, emit := range session.onChunk(result.ChatResponse, final) {
				emits = append(emits, clientEmit{result: &schemas.BifrostResponse{ChatResponse: emit.resp}, terminal: emit.terminal})
			}
			return emits, true
		},
		continues: session.continues,
		usage:     func() *schemas.BifrostLLMUsage { return session.usage },
		advance: func() {
			assistant, injected := session.endTurn()
			results := executeInjectedCalls(injected, func(call schemas.ChatAssistantMessageToolCall) *schemas.ChatMessage {
				return bifrost.executeInjectedCall(ctx, call)
			})
			next := *turn
			next.Params = relaxForcedChatChoice(turn.Params)
			next.Input = make([]schemas.ChatMessage, 0, len(turn.Input)+1+len(results))
			next.Input = append(next.Input, turn.Input...)
			next.Input = append(next.Input, assistant)
			for _, result := range results {
				next.Input = append(next.Input, *result)
			}
			turn = &next
		},
		dispatch: func(runner schemas.PostHookRunner, finalizer func(context.Context)) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
			return bifrost.dispatchChatStream(ctx, provider, config, key, turn, runner, finalizer)
		},
	}, postHookRunner, postHookSpanFinalizer)
}

// startInjectedResponsesStream serves a streaming Responses attempt whose provider
// injects tools.
func (bifrost *Bifrost) startInjectedResponsesStream(ctx *schemas.BifrostContext, provider schemas.Provider, config *schemas.ProviderConfig, key schemas.Key, original *schemas.BifrostResponsesRequest, set *injectedToolSet, postHookRunner schemas.PostHookRunner, postHookSpanFinalizer func(context.Context)) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	session := newInjectedResponsesStream(set)
	turn := applyInjectedToolsResponses(original, set)
	return chainInjectedStream(ctx, injectedStreamTurns{
		process: func(result *schemas.BifrostResponse, final bool) ([]clientEmit, bool) {
			if result.ResponsesStreamResponse == nil {
				return nil, false
			}
			var emits []clientEmit
			for _, emit := range session.onEvent(result.ResponsesStreamResponse, final) {
				emits = append(emits, clientEmit{result: &schemas.BifrostResponse{ResponsesStreamResponse: emit.event}, terminal: emit.terminal})
			}
			return emits, true
		},
		continues: session.continues,
		usage:     func() *schemas.BifrostLLMUsage { return session.usage },
		advance: func() {
			items, injected := session.endTurn()
			results := executeInjectedCalls(injected, func(call schemas.ChatAssistantMessageToolCall) *schemas.ChatMessage {
				return bifrost.executeInjectedCall(ctx, call)
			})
			next := *turn
			next.Params = relaxForcedResponsesChoice(turn.Params)
			next.Input = make([]schemas.ResponsesMessage, 0, len(turn.Input)+len(items)+len(results))
			next.Input = append(next.Input, turn.Input...)
			next.Input = append(next.Input, items...)
			for _, result := range results {
				next.Input = append(next.Input, result.ToResponsesMessages()...)
			}
			turn = &next
		},
		dispatch: func(runner schemas.PostHookRunner, finalizer func(context.Context)) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
			return bifrost.dispatchResponsesStream(ctx, provider, config, key, turn, runner, finalizer)
		},
	}, postHookRunner, postHookSpanFinalizer)
}

// heldTextLimit bounds how much text a turn holds behind a client call. Past it, every
// held item is released in order, client calls first where the model put them, and the
// rest of the turn streams live. Once a client call has reached the client, the turn
// ends the loop as maxDepth does: an injected call later in it stays hidden and is not
// run, since re-asking the model would leave out the call the client is answering.
var heldTextLimit = 64 * 1024

// responsesEmit is one Responses stream event to send to the client.
type responsesEmit struct {
	event    *schemas.BifrostResponsesStreamResponse
	terminal bool
}

// itemRoute is what happens to one upstream output item's events.
type itemRoute int

const (
	routeVisible  itemRoute = iota // forwarded live with a client output index
	routeClient                    // a client function call: held until the turn shows no injected call
	routeInjected                  // an injected function call: never reaches the client
	routeDropped                   // a client call in a turn that also calls an injected tool
)

// injectedResponsesStream is the Responses API parallel of injectedChatStream. The
// client sees one response: the first turn's response.created, items from every turn
// under consecutive output indexes, sequence numbers without gaps, and one
// response.completed whose output and usage cover all turns.
type injectedResponsesStream struct {
	set   *injectedToolSet
	depth int

	responseID  *string
	createdAt   int
	createdSent bool
	seq         int
	nextIndex   int
	usage       *schemas.BifrostLLMUsage
	output      []schemas.ResponsesMessage // completed client-visible items, all turns

	// Current turn.
	routes      map[int]itemRoute
	indexes     map[int]int // upstream output index -> client output index, assigned on first release
	itemRoutes  map[string]int
	holding     bool                                      // a client call was seen: later items wait so the turn keeps the model's order
	heldText    int                                       // bytes of text deltas held behind a client call
	live        bool                                      // held text passed heldTextLimit: the rest of the turn streams unheld
	sentCall    bool                                      // a client call reached the client mid-turn: this turn ends the loop
	held        []*schemas.BifrostResponsesStreamResponse // events waiting for the turn to show whether it calls an injected tool
	replay      []schemas.ResponsesMessage                // completed items to send back as next turn's input
	injected    []schemas.ChatAssistantMessageToolCall
	hasInjected bool
	more        bool
}

func newInjectedResponsesStream(set *injectedToolSet) *injectedResponsesStream {
	return &injectedResponsesStream{set: set, depth: 1}
}

// onEvent takes one upstream event and returns the events the client should see.
func (s *injectedResponsesStream) onEvent(ev *schemas.BifrostResponsesStreamResponse, final bool) []responsesEmit {
	switch ev.Type {
	case schemas.ResponsesStreamResponseTypeCreated, schemas.ResponsesStreamResponseTypeInProgress, schemas.ResponsesStreamResponseTypeQueued:
		if ev.Type == schemas.ResponsesStreamResponseTypeCreated && s.responseID == nil && ev.Response != nil {
			s.responseID = ev.Response.ID
			s.createdAt = ev.Response.CreatedAt
		}
		if s.createdSent && ev.Type != schemas.ResponsesStreamResponseTypeQueued {
			return nil
		}
		if ev.Type == schemas.ResponsesStreamResponseTypeCreated {
			s.createdSent = true
		}
		return []responsesEmit{{event: s.renumber(ev, nil)}}
	case schemas.ResponsesStreamResponseTypeCompleted, schemas.ResponsesStreamResponseTypeIncomplete:
		return s.onTurnEnd(ev)
	case schemas.ResponsesStreamResponseTypeFailed, schemas.ResponsesStreamResponseTypeError:
		failed := s.renumber(ev, nil)
		if failed.Response != nil {
			// The client stream ends here: bill the turns that finished before it too.
			response := *failed.Response
			response.Usage = schemas.MergeBifrostLLMUsage(s.usage, response.Usage.ToBifrostLLMUsage()).ToResponsesResponseUsage()
			failed.Response = &response
		}
		return []responsesEmit{{event: failed, terminal: true}}
	}

	var emits []responsesEmit
	if ev.Type == schemas.ResponsesStreamResponseTypeOutputItemAdded && ev.Item != nil && isFunctionCall(*ev.Item) &&
		ev.Item.Name != nil && s.set.isInjected(*ev.Item.Name) {
		// The turn calls an injected tool, so it is re-asked and its client calls are never
		// answered: drop them, and release whatever was held behind them. On the last
		// permitted turn nothing is re-asked, so the client calls stay, as in chat.
		if s.depth < s.set.maxDepth && !s.sentCall {
			s.dropClientCalls()
			emits = s.releaseHeld()
		}
	}
	upstreamIndex, route, known := s.routeOf(ev)
	if !known {
		return append(emits, responsesEmit{event: s.renumber(ev, nil), terminal: final})
	}
	if ev.Type == schemas.ResponsesStreamResponseTypeOutputItemDone && ev.Item != nil {
		s.recordDone(route, *ev.Item)
	}
	switch {
	case route == routeInjected || route == routeDropped:
		return emits
	case (route == routeClient || s.holding) && !s.live:
		// Every tool call is held until the turn shows whether it also calls an injected
		// tool. Items after a held call wait behind it, so the turn keeps its order.
		s.holding = true
		s.held = append(s.held, ev)
		if route == routeVisible && ev.Delta != nil {
			s.heldText += len(*ev.Delta)
			if s.heldText > heldTextLimit {
				for _, held := range s.held {
					if _, heldRoute, ok := s.routeFor(held); ok && heldRoute == routeClient {
						s.sentCall = true
					}
				}
				emits = append(emits, s.releaseHeld()...)
				s.live = true
			}
		}
		return emits
	default:
		if route == routeClient {
			s.sentCall = true
		}
		index := s.clientIndex(upstreamIndex)
		if ev.Type == schemas.ResponsesStreamResponseTypeOutputItemDone && ev.Item != nil {
			s.output = append(s.output, *ev.Item)
		}
		return append(emits, responsesEmit{event: s.renumber(ev, &index)})
	}
}

// routeOf classifies the item an event belongs to, registering it on output_item.added.
func (s *injectedResponsesStream) routeOf(ev *schemas.BifrostResponsesStreamResponse) (int, itemRoute, bool) {
	if s.routes == nil {
		s.routes, s.indexes, s.itemRoutes = map[int]itemRoute{}, map[int]int{}, map[string]int{}
	}
	if ev.Type == schemas.ResponsesStreamResponseTypeOutputItemAdded && ev.OutputIndex != nil && ev.Item != nil {
		index := *ev.OutputIndex
		route := routeVisible
		if isFunctionCall(*ev.Item) {
			switch {
			case ev.Item.Name != nil && s.set.isInjected(*ev.Item.Name):
				route = routeInjected
			case s.hasInjected:
				route = routeDropped
			default:
				route = routeClient
			}
		}
		s.routes[index] = route
		if ev.Item.ID != nil {
			s.itemRoutes[*ev.Item.ID] = index
		}
		return index, route, true
	}
	return s.routeFor(ev)
}

// routeFor looks up the route of an event whose item is already registered.
func (s *injectedResponsesStream) routeFor(ev *schemas.BifrostResponsesStreamResponse) (int, itemRoute, bool) {
	if ev.OutputIndex != nil {
		route, ok := s.routes[*ev.OutputIndex]
		return *ev.OutputIndex, route, ok
	}
	if ev.ItemID != nil {
		if index, ok := s.itemRoutes[*ev.ItemID]; ok {
			return index, s.routes[index], true
		}
	}
	return 0, 0, false
}

// clientIndex returns the client output index of an upstream item, assigning the next
// one on its first release. Assigning on release rather than on arrival keeps indexes
// contiguous when a held client call is later dropped.
func (s *injectedResponsesStream) clientIndex(upstream int) int {
	index, ok := s.indexes[upstream]
	if !ok {
		index = s.nextIndex
		s.indexes[upstream] = index
		s.nextIndex++
	}
	return index
}

// dropClientCalls marks the client calls of this turn as dropped: the turn calls an
// injected tool, so it is re-asked and the client calls in it are never answered.
func (s *injectedResponsesStream) dropClientCalls() {
	s.hasInjected = true
	for index, route := range s.routes {
		if route == routeClient {
			s.routes[index] = routeDropped
		}
	}
}

// releaseHeld sends the held events that still belong to the client, in arrival order.
func (s *injectedResponsesStream) releaseHeld() []responsesEmit {
	var emits []responsesEmit
	for _, ev := range s.held {
		upstream, route, known := s.routeFor(ev)
		if !known || route == routeDropped || route == routeInjected {
			continue
		}
		index := s.clientIndex(upstream)
		if ev.Type == schemas.ResponsesStreamResponseTypeOutputItemDone && ev.Item != nil {
			s.output = append(s.output, *ev.Item)
		}
		emits = append(emits, responsesEmit{event: s.renumber(ev, &index)})
	}
	s.held = nil
	s.holding = false
	s.heldText = 0
	return emits
}

func (s *injectedResponsesStream) recordDone(route itemRoute, item schemas.ResponsesMessage) {
	switch route {
	case routeVisible:
		s.replay = append(s.replay, item)
	case routeInjected:
		s.replay = append(s.replay, item)
		if call, ok := injectedResponsesCall(item, s.set); ok {
			s.injected = append(s.injected, call)
		}
	}
}

// onTurnEnd handles response.completed (or incomplete) for one upstream turn.
func (s *injectedResponsesStream) onTurnEnd(ev *schemas.BifrostResponsesStreamResponse) []responsesEmit {
	var turnUsage *schemas.BifrostLLMUsage
	if ev.Response != nil {
		turnUsage = ev.Response.Usage.ToBifrostLLMUsage()
	}
	s.usage = schemas.MergeBifrostLLMUsage(s.usage, turnUsage)
	if len(s.injected) > 0 && s.depth < s.set.maxDepth && !s.sentCall {
		s.more = true
		return nil
	}
	s.more = false

	emits := s.releaseHeld()
	terminal := s.renumber(ev, nil)
	response := schemas.BifrostResponsesResponse{}
	if ev.Response != nil {
		response = *ev.Response
	}
	if s.responseID != nil {
		// One response to the client: completed describes the response created announced.
		response.ID = s.responseID
		response.CreatedAt = s.createdAt
	}
	response.Output = s.output
	response.Usage = s.usage.ToResponsesResponseUsage()
	terminal.Response = &response
	return append(emits, responsesEmit{event: terminal, terminal: true})
}

// renumber copies an event with the client's next sequence number and, when given, the
// client output index.
func (s *injectedResponsesStream) renumber(ev *schemas.BifrostResponsesStreamResponse, outputIndex *int) *schemas.BifrostResponsesStreamResponse {
	out := *ev
	out.SequenceNumber = s.seq
	out.ExtraFields.ChunkIndex = s.seq
	out.ExtraFields.RawResponse = nil
	s.seq++
	if outputIndex != nil {
		out.OutputIndex = outputIndex
	}
	return &out
}

// continues reports whether the turn that just ended left injected calls to execute.
func (s *injectedResponsesStream) continues() bool {
	return s.more
}

// endTurn closes a turn that ended with injected calls pending. It returns the items to
// replay as input (the turn's output minus client calls) and the calls to execute.
func (s *injectedResponsesStream) endTurn() ([]schemas.ResponsesMessage, []schemas.ChatAssistantMessageToolCall) {
	replay, injected := s.replay, s.injected
	s.depth++
	s.routes, s.indexes, s.itemRoutes = nil, nil, nil
	s.held, s.replay, s.injected = nil, nil, nil
	s.holding, s.live, s.sentCall, s.hasInjected, s.more = false, false, false, false, false
	s.heldText = 0
	return replay, injected
}

package mcp

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// ResponsesStreamStarter opens one streaming iteration of the agent loop on the Responses API.
type ResponsesStreamStarter func(ctx *schemas.BifrostContext, req *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError)

// responsesRelayState is the client-facing view the relay maintains across iterations. Each
// iteration numbers its own events and items from zero, so the relay renumbers everything it
// forwards and remembers what it sent in order to rebuild the terminal response.
type responsesRelayState struct {
	nextSequence   int
	nextItemIndex  int
	forwardedItems []schemas.ResponsesMessage
	responseID     *string
	carriedUsage   *schemas.BifrostLLMUsage
}

// responsesIteration is the outcome of relaying one iteration's events.
type responsesIteration struct {
	// held carries events withheld from the client: the function-call items, if any, and always
	// the terminal event, which the caller either rebuilds and forwards or drops.
	held []*schemas.BifrostStreamChunk
	// itemIndexes maps this iteration's output indexes onto client-facing ones.
	itemIndexes map[int]int
	aborted     bool
	failed      bool
}

// CheckAndExecuteAgentForResponsesStream is the Responses twin of CheckAndExecuteAgentForChatStream.
func (m *MCPManager) CheckAndExecuteAgentForResponsesStream(
	ctx *schemas.BifrostContext,
	req *schemas.BifrostResponsesRequest,
	stream chan *schemas.BifrostStreamChunk,
	startStream ResponsesStreamStarter,
) chan *schemas.BifrostStreamChunk {
	if m.toolsManager == nil {
		return stream
	}
	return m.toolsManager.ExecuteAgentForResponsesStream(ctx, req, stream, startStream, m.executeToolForAgent)
}

// ExecuteAgentForResponsesStream returns the stream unchanged unless the request carries a tool
// this manager would execute itself.
func (t *ToolsManager) ExecuteAgentForResponsesStream(
	ctx *schemas.BifrostContext,
	req *schemas.BifrostResponsesRequest,
	stream chan *schemas.BifrostStreamChunk,
	startStream ResponsesStreamStarter,
	executeTool MCPToolExecutor,
) chan *schemas.BifrostStreamChunk {
	if ctx == nil || req == nil || stream == nil || startStream == nil || executeTool == nil {
		return stream
	}
	if !t.hasAutoExecutableResponsesTools(req) {
		return stream
	}
	out := make(chan *schemas.BifrostStreamChunk)
	go t.relayResponsesStream(ctx, req, stream, startStream, executeTool, out)
	return out
}

// relayResponsesStream owns the client-facing channel for the whole turn, across every iteration.
func (t *ToolsManager) relayResponsesStream(
	ctx *schemas.BifrostContext,
	req *schemas.BifrostResponsesRequest,
	stream chan *schemas.BifrostStreamChunk,
	startStream ResponsesStreamStarter,
	executeTool MCPToolExecutor,
	out chan *schemas.BifrostStreamChunk,
) {
	defer close(out)

	conversation := append([]schemas.ResponsesMessage(nil), req.Input...)
	maxDepth := int(t.maxAgentDepth.Load())
	if maxDepth <= 0 {
		maxDepth = schemas.DefaultMaxAgentDepth
	}

	state := &responsesRelayState{}
	current := stream

	for depth := 1; ; depth++ {
		iteration := t.pumpResponsesIteration(ctx, current, out, state, depth == 1)
		if iteration.aborted {
			return
		}
		if len(iteration.held) == 0 {
			return
		}

		callItems := responsesFunctionCallItems(iteration.held)
		var toolCalls []schemas.ChatAssistantMessageToolCall
		if !iteration.failed && len(callItems) > 0 {
			toolCalls = extractToolCalls((&schemas.BifrostResponsesResponse{Output: callItems}).ToBifrostChatResponse())
		}
		autoExecutable, pending := t.agentModeExecutor.partitionToolCalls(ctx, toolCalls, t.clientManager)

		// A plugin holding this stream behind the pause gate has already decided what the client
		// may see, so running a tool underneath that decision could act on a blocked response.
		gated, _ := ctx.Value(schemas.BifrostContextKeyStreamGated).(bool)
		if iteration.failed || len(autoExecutable) == 0 || len(pending) > 0 || depth > maxDepth || gated {
			state.flush(ctx, out, iteration)
			return
		}

		state.carriedUsage = schemas.MergeBifrostLLMUsage(state.carriedUsage, responsesHeldUsage(iteration.held))

		results, execErr := t.agentModeExecutor.executeToolCallsInParallel(ctx, autoExecutable, executeTool)
		if execErr != nil {
			sendStreamChunk(ctx, out, &schemas.BifrostStreamChunk{BifrostError: execErr})
			return
		}

		conversation = append(conversation, callItems...)
		for _, result := range results {
			if result != nil {
				conversation = append(conversation, result.ToResponsesMessages()...)
			}
		}

		if t.fetchNewRequestIDFunc != nil {
			if newID := t.fetchNewRequestIDFunc(ctx); newID != "" {
				ctx.SetValue(schemas.BifrostContextKeyRequestID, newID)
			}
		}

		next, startErr := startStream(ctx, &schemas.BifrostResponsesRequest{
			Provider:  req.Provider,
			Model:     req.Model,
			Fallbacks: req.Fallbacks,
			Params:    req.Params,
			Input:     conversation,
		})
		if startErr != nil {
			sendStreamChunk(ctx, out, &schemas.BifrostStreamChunk{BifrostError: startErr})
			return
		}
		current = next
	}
}

// pumpResponsesIteration forwards one iteration's events until its channel closes. Events are
// renumbered onto the client-facing stream; the function-call items and the terminal event are
// held so the caller can decide whether the client ever sees them. first suppresses nothing;
// later iterations drop the lifecycle events that would announce a second response.
func (t *ToolsManager) pumpResponsesIteration(
	ctx *schemas.BifrostContext,
	in chan *schemas.BifrostStreamChunk,
	out chan *schemas.BifrostStreamChunk,
	state *responsesRelayState,
	first bool,
) responsesIteration {
	iteration := responsesIteration{itemIndexes: make(map[int]int)}
	holding := false
	for chunk := range in {
		if chunk != nil && chunk.BifrostError != nil {
			iteration.failed = true
			iteration.held = append(iteration.held, chunk)
			continue
		}
		if chunk == nil || chunk.BifrostResponsesStreamResponse == nil {
			continue
		}
		event := chunk.BifrostResponsesStreamResponse

		if !holding && event.Type == schemas.ResponsesStreamResponseTypeOutputItemAdded && isResponsesFunctionCallItem(event.Item) {
			holding = true
		}
		if holding {
			iteration.held = append(iteration.held, chunk)
			continue
		}

		switch event.Type {
		case schemas.ResponsesStreamResponseTypeCreated, schemas.ResponsesStreamResponseTypeInProgress:
			// The response was announced by the first iteration; announcing it again would read
			// as a second response starting inside the same stream.
			if !first {
				continue
			}
			if event.Response != nil && state.responseID == nil {
				state.responseID = event.Response.ID
			}
		case schemas.ResponsesStreamResponseTypeCompleted,
			schemas.ResponsesStreamResponseTypeFailed,
			schemas.ResponsesStreamResponseTypeIncomplete:
			// Terminal events carry the whole response, so they are held until the turn is known
			// to be over and the accumulated output can be rebuilt onto them.
			iteration.held = append(iteration.held, chunk)
			continue
		}

		if !state.forward(ctx, out, chunk, iteration.itemIndexes) {
			iteration.aborted = true
			// Release the producer now that nothing will read its remaining chunks.
			go func() {
				for range in {
				}
			}()
			return iteration
		}
	}
	return iteration
}

// flush releases an iteration's held events, rebuilding the terminal one so it describes the whole
// turn rather than the final iteration alone.
func (s *responsesRelayState) flush(ctx *schemas.BifrostContext, out chan *schemas.BifrostStreamChunk, iteration responsesIteration) {
	for _, chunk := range iteration.held {
		if chunk != nil && chunk.BifrostError != nil {
			if !sendStreamChunk(ctx, out, chunk) {
				return
			}
			continue
		}
		if chunk == nil || chunk.BifrostResponsesStreamResponse == nil {
			continue
		}
		event := chunk.BifrostResponsesStreamResponse
		if event.Type == schemas.ResponsesStreamResponseTypeCompleted ||
			event.Type == schemas.ResponsesStreamResponseTypeFailed ||
			event.Type == schemas.ResponsesStreamResponseTypeIncomplete {
			chunk = s.rebuildTerminal(chunk)
		}
		if !s.forward(ctx, out, chunk, iteration.itemIndexes) {
			return
		}
	}
}

// forward renumbers an event onto the client-facing stream and sends it. The provider's own chunk
// is left untouched for anything that already observed it.
func (s *responsesRelayState) forward(
	ctx *schemas.BifrostContext,
	out chan *schemas.BifrostStreamChunk,
	chunk *schemas.BifrostStreamChunk,
	itemIndexes map[int]int,
) bool {
	source := chunk.BifrostResponsesStreamResponse
	if source == nil {
		return sendStreamChunk(ctx, out, chunk)
	}
	event := *source
	event.SequenceNumber = s.nextSequence
	s.nextSequence++

	if source.OutputIndex != nil {
		clientIndex, mapped := itemIndexes[*source.OutputIndex]
		if !mapped {
			clientIndex = s.nextItemIndex
			s.nextItemIndex++
			itemIndexes[*source.OutputIndex] = clientIndex
		}
		event.OutputIndex = &clientIndex
	}
	if event.Type == schemas.ResponsesStreamResponseTypeOutputItemDone && event.Item != nil {
		s.forwardedItems = append(s.forwardedItems, *event.Item)
	}

	copied := *chunk
	copied.BifrostResponsesStreamResponse = &event
	return sendStreamChunk(ctx, out, &copied)
}

// rebuildTerminal restates a terminal event so it describes the turn: the response the client was
// told about, every output item it actually saw, and usage covering every iteration.
func (s *responsesRelayState) rebuildTerminal(chunk *schemas.BifrostStreamChunk) *schemas.BifrostStreamChunk {
	source := chunk.BifrostResponsesStreamResponse
	if source == nil || source.Response == nil {
		return chunk
	}
	event := *source
	response := *source.Response
	if s.responseID != nil {
		response.ID = s.responseID
	}
	response.Output = append(append([]schemas.ResponsesMessage(nil), s.forwardedItems...), responsesTerminalExtraItems(s.forwardedItems, response.Output)...)
	if s.carriedUsage != nil {
		merged := schemas.MergeBifrostLLMUsage(s.carriedUsage, responsesUsageToLLMUsage(response.Usage))
		response.Usage = merged.ToResponsesResponseUsage()
	}
	event.Response = &response
	copied := *chunk
	copied.BifrostResponsesStreamResponse = &event
	return &copied
}

// responsesTerminalExtraItems returns the terminal response's items that were never forwarded
// individually, so a provider that only reports output on the terminal event is not truncated.
func responsesTerminalExtraItems(forwarded, terminal []schemas.ResponsesMessage) []schemas.ResponsesMessage {
	if len(forwarded) == 0 {
		return terminal
	}
	seen := make(map[string]bool, len(forwarded))
	for _, item := range forwarded {
		if item.ID != nil {
			seen[*item.ID] = true
		}
	}
	extra := make([]schemas.ResponsesMessage, 0)
	for _, item := range terminal {
		if item.ID == nil || !seen[*item.ID] {
			extra = append(extra, item)
		}
	}
	return extra
}

// hasAutoExecutableResponsesTools reports whether the request carries any tool Bifrost would run
// itself. Streams without one skip the relay and keep the provider's channel untouched.
func (t *ToolsManager) hasAutoExecutableResponsesTools(req *schemas.BifrostResponsesRequest) bool {
	if req == nil || req.Params == nil {
		return false
	}
	for _, tool := range req.Params.Tools {
		if tool.Name == nil || *tool.Name == "" {
			continue
		}
		name := *tool.Name
		if name == ToolTypeListToolFiles || name == ToolTypeReadToolFile ||
			name == ToolTypeGetToolDocs || name == ToolTypeExecuteToolCode {
			return true
		}
		if client := t.clientManager.GetClientForTool(name); client != nil && canAutoExecuteTool(name, client.ExecutionConfig) {
			return true
		}
	}
	return false
}

// responsesFunctionCallItems collects the completed function-call items from an iteration's hold.
// The done event carries the call whole, so nothing has to be reassembled from the deltas.
func responsesFunctionCallItems(held []*schemas.BifrostStreamChunk) []schemas.ResponsesMessage {
	items := make([]schemas.ResponsesMessage, 0)
	for _, chunk := range held {
		if chunk == nil || chunk.BifrostResponsesStreamResponse == nil {
			continue
		}
		event := chunk.BifrostResponsesStreamResponse
		if event.Type != schemas.ResponsesStreamResponseTypeOutputItemDone {
			continue
		}
		if isResponsesFunctionCallItem(event.Item) {
			items = append(items, *event.Item)
		}
	}
	return items
}

// responsesHeldUsage returns the usage reported by a held iteration, if any.
func responsesHeldUsage(held []*schemas.BifrostStreamChunk) *schemas.BifrostLLMUsage {
	for _, chunk := range held {
		if chunk == nil || chunk.BifrostResponsesStreamResponse == nil {
			continue
		}
		event := chunk.BifrostResponsesStreamResponse
		if event.Response == nil {
			continue
		}
		if usage := responsesUsageToLLMUsage(event.Response.Usage); usage != nil {
			return usage
		}
	}
	return nil
}

func responsesUsageToLLMUsage(usage *schemas.ResponsesResponseUsage) *schemas.BifrostLLMUsage {
	if usage == nil {
		return nil
	}
	return usage.ToBifrostLLMUsage()
}

func isResponsesFunctionCallItem(item *schemas.ResponsesMessage) bool {
	return item != nil && item.Type != nil && *item.Type == schemas.ResponsesMessageTypeFunctionCall
}

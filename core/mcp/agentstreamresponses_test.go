package mcp

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func responsesStreamChunk(eventType schemas.ResponsesStreamResponseType, sequence int) *schemas.BifrostStreamChunk {
	return &schemas.BifrostStreamChunk{
		BifrostResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{
			Type:           eventType,
			SequenceNumber: sequence,
		},
	}
}

func responsesCreated(sequence int, responseID string) *schemas.BifrostStreamChunk {
	chunk := responsesStreamChunk(schemas.ResponsesStreamResponseTypeCreated, sequence)
	chunk.BifrostResponsesStreamResponse.Response = &schemas.BifrostResponsesResponse{ID: schemas.Ptr(responseID)}
	return chunk
}

func responsesInProgress(sequence int) *schemas.BifrostStreamChunk {
	chunk := responsesStreamChunk(schemas.ResponsesStreamResponseTypeInProgress, sequence)
	chunk.BifrostResponsesStreamResponse.Response = &schemas.BifrostResponsesResponse{}
	return chunk
}

func responsesItemAdded(sequence, outputIndex int, item schemas.ResponsesMessage) *schemas.BifrostStreamChunk {
	chunk := responsesStreamChunk(schemas.ResponsesStreamResponseTypeOutputItemAdded, sequence)
	chunk.BifrostResponsesStreamResponse.OutputIndex = &outputIndex
	chunk.BifrostResponsesStreamResponse.Item = &item
	return chunk
}

func responsesItemDone(sequence, outputIndex int, item schemas.ResponsesMessage) *schemas.BifrostStreamChunk {
	chunk := responsesStreamChunk(schemas.ResponsesStreamResponseTypeOutputItemDone, sequence)
	chunk.BifrostResponsesStreamResponse.OutputIndex = &outputIndex
	chunk.BifrostResponsesStreamResponse.Item = &item
	return chunk
}

func responsesTextDelta(sequence, outputIndex int, text string) *schemas.BifrostStreamChunk {
	chunk := responsesStreamChunk(schemas.ResponsesStreamResponseTypeOutputTextDelta, sequence)
	chunk.BifrostResponsesStreamResponse.OutputIndex = &outputIndex
	chunk.BifrostResponsesStreamResponse.Delta = schemas.Ptr(text)
	return chunk
}

func responsesCompleted(sequence int, responseID string, output []schemas.ResponsesMessage, inputTokens, outputTokens int) *schemas.BifrostStreamChunk {
	chunk := responsesStreamChunk(schemas.ResponsesStreamResponseTypeCompleted, sequence)
	chunk.BifrostResponsesStreamResponse.Response = &schemas.BifrostResponsesResponse{
		ID:     schemas.Ptr(responseID),
		Output: output,
		Usage: &schemas.ResponsesResponseUsage{
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
			TotalTokens:  inputTokens + outputTokens,
		},
	}
	return chunk
}

func responsesFunctionCallItem(itemID, callID, name, arguments string) schemas.ResponsesMessage {
	callType := schemas.ResponsesMessageTypeFunctionCall
	return schemas.ResponsesMessage{
		ID:   schemas.Ptr(itemID),
		Type: &callType,
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID:    schemas.Ptr(callID),
			Name:      schemas.Ptr(name),
			Arguments: schemas.Ptr(arguments),
		},
	}
}

func responsesMessageItem(itemID string) schemas.ResponsesMessage {
	messageType := schemas.ResponsesMessageTypeMessage
	return schemas.ResponsesMessage{ID: schemas.Ptr(itemID), Type: &messageType}
}

func responsesRelayRequest(toolNames ...string) *schemas.BifrostResponsesRequest {
	tools := make([]schemas.ResponsesTool, 0, len(toolNames))
	for _, name := range toolNames {
		tools = append(tools, schemas.ResponsesTool{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr(name)})
	}
	role := schemas.ResponsesInputMessageRoleUser
	return &schemas.BifrostResponsesRequest{
		Provider: "openai",
		Model:    "gpt-test",
		Input:    []schemas.ResponsesMessage{{Role: &role}},
		Params:   &schemas.ResponsesParameters{Tools: tools},
	}
}

func noResponsesStreamStarter(t *testing.T) ResponsesStreamStarter {
	t.Helper()
	return func(_ *schemas.BifrostContext, _ *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
		t.Fatal("no follow-up iteration was expected")
		return nil, nil
	}
}

// responsesEventTypes lists the event types a client actually received.
func responsesEventTypes(chunks []*schemas.BifrostStreamChunk) []schemas.ResponsesStreamResponseType {
	types := make([]schemas.ResponsesStreamResponseType, 0, len(chunks))
	for _, chunk := range chunks {
		if chunk != nil && chunk.BifrostResponsesStreamResponse != nil {
			types = append(types, chunk.BifrostResponsesStreamResponse.Type)
		}
	}
	return types
}

func countResponsesEvents(chunks []*schemas.BifrostStreamChunk, eventType schemas.ResponsesStreamResponseType) int {
	count := 0
	for _, t := range responsesEventTypes(chunks) {
		if t == eventType {
			count++
		}
	}
	return count
}

// responsesFunctionCallEventCount counts events that would expose a function call to the client.
func responsesFunctionCallEventCount(chunks []*schemas.BifrostStreamChunk) int {
	count := 0
	for _, chunk := range chunks {
		event := chunk.BifrostResponsesStreamResponse
		if event == nil {
			continue
		}
		if isResponsesFunctionCallItem(event.Item) {
			count++
		}
		if event.Type == schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta ||
			event.Type == schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone {
			count++
		}
	}
	return count
}

func responsesTerminalResponse(t *testing.T, chunks []*schemas.BifrostStreamChunk) *schemas.BifrostResponsesResponse {
	t.Helper()
	for _, chunk := range chunks {
		event := chunk.BifrostResponsesStreamResponse
		if event != nil && event.Type == schemas.ResponsesStreamResponseTypeCompleted {
			return event.Response
		}
	}
	t.Fatal("expected a terminal response.completed event")
	return nil
}

// TestExecuteAgentForResponsesStreamPassthrough pins the fast path: a stream with nothing Bifrost
// would execute is handed back untouched.
func TestExecuteAgentForResponsesStreamPassthrough(t *testing.T) {
	manager := newStreamRelayToolsManager(&MockClientManager{}, 5)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	stream := streamOf(responsesTextDelta(0, 0, "hello"))

	got := manager.ExecuteAgentForResponsesStream(ctx, responsesRelayRequest("client_side"), stream, noResponsesStreamStarter(t), okToolExecutor("unused"))
	if got != stream {
		t.Fatal("expected the provider's own channel to be returned unwrapped")
	}
}

// TestResponsesStreamRelayForwardsIterationWithoutToolCalls pins that a plain answer streams
// through the relay with its lifecycle intact.
func TestResponsesStreamRelayForwardsIterationWithoutToolCalls(t *testing.T) {
	manager := newStreamRelayToolsManager(&MockAutoClientManager{}, 5)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	message := responsesMessageItem("msg_1")
	stream := streamOf(
		responsesCreated(0, "resp_1"),
		responsesInProgress(1),
		responsesItemAdded(2, 0, message),
		responsesTextDelta(3, 0, "hello"),
		responsesItemDone(4, 0, message),
		responsesCompleted(5, "resp_1", []schemas.ResponsesMessage{message}, 5, 3),
	)

	out := manager.ExecuteAgentForResponsesStream(ctx, responsesRelayRequest("weather"), stream, noResponsesStreamStarter(t), okToolExecutor("unused"))
	chunks := collectStream(t, out)

	if len(chunks) != 6 {
		t.Fatalf("expected all 6 events forwarded, got %d: %v", len(chunks), responsesEventTypes(chunks))
	}
	if got := countResponsesEvents(chunks, schemas.ResponsesStreamResponseTypeCompleted); got != 1 {
		t.Fatalf("expected exactly one terminal event, got %d", got)
	}
}

// TestResponsesStreamRelayExecutesAndContinues is the core case: the function call never reaches
// the client, the tool runs, and the follow-up continues the same response.
func TestResponsesStreamRelayExecutesAndContinues(t *testing.T) {
	manager := newStreamRelayToolsManager(&MockAutoClientManager{}, 5)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	call := responsesFunctionCallItem("fc_1", "call_1", "weather", `{"city":"Pune"}`)
	first := streamOf(
		responsesCreated(0, "resp_first"),
		responsesInProgress(1),
		responsesItemAdded(2, 0, call),
		responsesItemDone(3, 0, call),
		responsesCompleted(4, "resp_first", []schemas.ResponsesMessage{call}, 10, 4),
	)

	answer := responsesMessageItem("msg_2")
	var followUp *schemas.BifrostResponsesRequest
	startStream := func(_ *schemas.BifrostContext, req *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
		followUp = req
		return streamOf(
			responsesCreated(0, "resp_second"),
			responsesInProgress(1),
			responsesItemAdded(2, 0, answer),
			responsesTextDelta(3, 0, "28C and clear"),
			responsesItemDone(4, 0, answer),
			responsesCompleted(5, "resp_second", []schemas.ResponsesMessage{answer}, 20, 6),
		), nil
	}

	var executedArguments string
	executor := func(_ *schemas.BifrostContext, req *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
		executedArguments = req.ChatAssistantMessageToolCall.Function.Arguments
		return okToolExecutor("28C, clear")(nil, req)
	}

	out := manager.ExecuteAgentForResponsesStream(ctx, responsesRelayRequest("weather"), first, startStream, executor)
	chunks := collectStream(t, out)

	if executedArguments != `{"city":"Pune"}` {
		t.Fatalf("expected the completed call's arguments to be executed, got %q", executedArguments)
	}
	if n := responsesFunctionCallEventCount(chunks); n != 0 {
		t.Fatalf("expected the function call to stay hidden from the client, saw %d event(s)", n)
	}
	if got := countResponsesEvents(chunks, schemas.ResponsesStreamResponseTypeCreated); got != 1 {
		t.Fatalf("expected one response.created for the whole turn, got %d", got)
	}
	if got := countResponsesEvents(chunks, schemas.ResponsesStreamResponseTypeCompleted); got != 1 {
		t.Fatalf("expected one terminal event for the whole turn, got %d", got)
	}

	terminal := responsesTerminalResponse(t, chunks)
	if terminal.ID == nil || *terminal.ID != "resp_first" {
		t.Fatalf("expected the terminal event to keep the announced response id, got %v", terminal.ID)
	}
	if len(terminal.Output) != 1 || terminal.Output[0].ID == nil || *terminal.Output[0].ID != "msg_2" {
		t.Fatalf("expected the terminal output to hold only what the client saw, got %d item(s)", len(terminal.Output))
	}
	if terminal.Usage == nil || terminal.Usage.TotalTokens != 40 {
		t.Fatalf("expected usage merged across both iterations (40), got %v", terminal.Usage)
	}

	if followUp == nil {
		t.Fatal("expected a follow-up iteration")
	}
	if len(followUp.Input) != 3 {
		t.Fatalf("expected user, function call and tool output in the follow-up, got %d", len(followUp.Input))
	}
	if !isResponsesFunctionCallItem(&followUp.Input[1]) {
		t.Fatal("expected the follow-up to replay the executed function call")
	}
}

// TestResponsesStreamRelayRenumbersAcrossIterations pins that the client sees one continuously
// numbered stream even though each iteration numbers its own events and items from zero.
func TestResponsesStreamRelayRenumbersAcrossIterations(t *testing.T) {
	manager := newStreamRelayToolsManager(&MockAutoClientManager{}, 5)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	preamble := responsesMessageItem("msg_1")
	call := responsesFunctionCallItem("fc_1", "call_1", "weather", "{}")
	first := streamOf(
		responsesCreated(0, "resp_first"),
		responsesInProgress(1),
		responsesItemAdded(2, 0, preamble),
		responsesTextDelta(3, 0, "checking"),
		responsesItemDone(4, 0, preamble),
		responsesItemAdded(5, 1, call),
		responsesItemDone(6, 1, call),
		responsesCompleted(7, "resp_first", []schemas.ResponsesMessage{preamble, call}, 10, 4),
	)

	answer := responsesMessageItem("msg_2")
	startStream := func(_ *schemas.BifrostContext, _ *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
		return streamOf(
			responsesCreated(0, "resp_second"),
			responsesItemAdded(1, 0, answer),
			responsesTextDelta(2, 0, " done"),
			responsesItemDone(3, 0, answer),
			responsesCompleted(4, "resp_second", []schemas.ResponsesMessage{answer}, 20, 6),
		), nil
	}

	out := manager.ExecuteAgentForResponsesStream(ctx, responsesRelayRequest("weather"), first, startStream, okToolExecutor("28C"))
	chunks := collectStream(t, out)

	sequences := make([]int, 0, len(chunks))
	indexes := make([]int, 0, len(chunks))
	for _, chunk := range chunks {
		event := chunk.BifrostResponsesStreamResponse
		if event == nil {
			continue
		}
		sequences = append(sequences, event.SequenceNumber)
		if event.OutputIndex != nil {
			indexes = append(indexes, *event.OutputIndex)
		}
	}
	for i, sequence := range sequences {
		if sequence != i {
			t.Fatalf("expected sequence numbers to run 0..n without gaps, got %v", sequences)
		}
	}
	// The preamble keeps index 0; the second iteration's message must not reuse it.
	if len(indexes) == 0 || indexes[0] != 0 || indexes[len(indexes)-1] != 1 {
		t.Fatalf("expected the follow-up item to take the next client index, got %v", indexes)
	}

	terminal := responsesTerminalResponse(t, chunks)
	if len(terminal.Output) != 2 {
		t.Fatalf("expected the terminal output to cover both iterations' visible items, got %d", len(terminal.Output))
	}
}

// TestResponsesStreamRelayReleasesHoldForClientTools pins that a call the client owns is handed
// back rather than executed.
func TestResponsesStreamRelayReleasesHoldForClientTools(t *testing.T) {
	manager := newStreamRelayToolsManager(&mockPartitionClientManager{
		MockAutoClientManager: &MockAutoClientManager{},
		autoTools:             map[string]bool{},
	}, 5)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	call := responsesFunctionCallItem("fc_1", "call_1", "client_side", "{}")
	stream := streamOf(
		responsesCreated(0, "resp_1"),
		responsesItemAdded(1, 0, call),
		responsesItemDone(2, 0, call),
		responsesCompleted(3, "resp_1", []schemas.ResponsesMessage{call}, 4, 2),
	)

	out := manager.ExecuteAgentForResponsesStream(ctx, responsesRelayRequest("client_side"), stream, noResponsesStreamStarter(t), okToolExecutor("unused"))
	chunks := collectStream(t, out)

	if n := responsesFunctionCallEventCount(chunks); n == 0 {
		t.Fatal("expected the held function call to be released to the client")
	}
	if len(chunks) != 4 {
		t.Fatalf("expected every event released, got %d", len(chunks))
	}
}

// TestResponsesStreamRelayStopsAtDepthLimit pins that an exhausted loop hands the turn back.
func TestResponsesStreamRelayStopsAtDepthLimit(t *testing.T) {
	manager := newStreamRelayToolsManager(&MockAutoClientManager{}, 1)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	call := responsesFunctionCallItem("fc_1", "call_1", "weather", "{}")
	iterations := 0
	startStream := func(_ *schemas.BifrostContext, _ *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
		iterations++
		second := responsesFunctionCallItem("fc_2", "call_2", "weather", "{}")
		return streamOf(
			responsesItemAdded(0, 0, second),
			responsesItemDone(1, 0, second),
			responsesCompleted(2, "resp_2", []schemas.ResponsesMessage{second}, 1, 1),
		), nil
	}

	first := streamOf(
		responsesCreated(0, "resp_1"),
		responsesItemAdded(1, 0, call),
		responsesItemDone(2, 0, call),
		responsesCompleted(3, "resp_1", []schemas.ResponsesMessage{call}, 1, 1),
	)
	out := manager.ExecuteAgentForResponsesStream(ctx, responsesRelayRequest("weather"), first, startStream, okToolExecutor("done"))
	chunks := collectStream(t, out)

	if iterations != 1 {
		t.Fatalf("expected exactly one follow-up iteration at depth 1, got %d", iterations)
	}
	if n := responsesFunctionCallEventCount(chunks); n == 0 {
		t.Fatal("expected the final iteration's call to be released once the depth limit is reached")
	}
}

// TestResponsesStreamRelaySkipsGatedStream pins the guardrail valve.
func TestResponsesStreamRelaySkipsGatedStream(t *testing.T) {
	manager := newStreamRelayToolsManager(&MockAutoClientManager{}, 5)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyStreamGated, true)

	executed := false
	executor := func(_ *schemas.BifrostContext, _ *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
		executed = true
		return nil, nil
	}

	call := responsesFunctionCallItem("fc_1", "call_1", "weather", "{}")
	stream := streamOf(
		responsesItemAdded(0, 0, call),
		responsesItemDone(1, 0, call),
		responsesCompleted(2, "resp_1", []schemas.ResponsesMessage{call}, 2, 1),
	)

	out := manager.ExecuteAgentForResponsesStream(ctx, responsesRelayRequest("weather"), stream, noResponsesStreamStarter(t), executor)
	chunks := collectStream(t, out)

	if executed {
		t.Fatal("a stream held by a plugin must not have its tools executed")
	}
	if n := responsesFunctionCallEventCount(chunks); n == 0 {
		t.Fatal("expected the held events to be released untouched")
	}
}

// TestResponsesStreamRelaySurfacesToolAuthError pins that a per-user auth failure ends the stream.
func TestResponsesStreamRelaySurfacesToolAuthError(t *testing.T) {
	manager := newStreamRelayToolsManager(&MockAutoClientManager{}, 5)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	executor := func(_ *schemas.BifrostContext, _ *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
		return nil, &schemas.MCPAuthRequiredError{Kind: "oauth", MCPClientName: "test-client", Message: "reauthorize"}
	}

	call := responsesFunctionCallItem("fc_1", "call_1", "weather", "{}")
	stream := streamOf(
		responsesItemAdded(0, 0, call),
		responsesItemDone(1, 0, call),
		responsesCompleted(2, "resp_1", []schemas.ResponsesMessage{call}, 2, 1),
	)

	out := manager.ExecuteAgentForResponsesStream(ctx, responsesRelayRequest("weather"), stream, noResponsesStreamStarter(t), executor)
	chunks := collectStream(t, out)

	if len(chunks) == 0 {
		t.Fatal("expected an error chunk")
	}
	last := chunks[len(chunks)-1]
	if last.BifrostError == nil || last.BifrostError.ExtraFields.MCPAuthRequired == nil {
		t.Fatal("expected the auth failure to arrive as an in-band error chunk")
	}
}

// TestResponsesStreamRelaySurfacesFollowUpFailure pins that a failed follow-up ends the stream.
func TestResponsesStreamRelaySurfacesFollowUpFailure(t *testing.T) {
	manager := newStreamRelayToolsManager(&MockAutoClientManager{}, 5)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	startStream := func(_ *schemas.BifrostContext, _ *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
		return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "provider unavailable"}}
	}

	call := responsesFunctionCallItem("fc_1", "call_1", "weather", "{}")
	stream := streamOf(
		responsesItemAdded(0, 0, call),
		responsesItemDone(1, 0, call),
		responsesCompleted(2, "resp_1", []schemas.ResponsesMessage{call}, 2, 1),
	)

	out := manager.ExecuteAgentForResponsesStream(ctx, responsesRelayRequest("weather"), stream, startStream, okToolExecutor("28C"))
	chunks := collectStream(t, out)

	if len(chunks) == 0 || chunks[len(chunks)-1].BifrostError == nil {
		t.Fatal("expected the follow-up failure to arrive as an in-band error chunk")
	}
}

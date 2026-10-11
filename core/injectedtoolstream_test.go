package bifrost

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type chatDelta struct {
	role, text, finish string
	call               *schemas.ChatAssistantMessageToolCall
	usage              *schemas.BifrostLLMUsage
	noChoice           bool
}

func chatStreamChunk(id string, d chatDelta) *schemas.BifrostChatResponse {
	resp := &schemas.BifrostChatResponse{ID: id, Created: 100, Model: "m", Object: "chat.completion.chunk", Usage: d.usage}
	if d.noChoice {
		return resp
	}
	delta := &schemas.ChatStreamResponseChoiceDelta{}
	if d.role != "" {
		delta.Role = schemas.Ptr(d.role)
	}
	if d.text != "" {
		delta.Content = schemas.Ptr(d.text)
	}
	if d.call != nil {
		delta.ToolCalls = []schemas.ChatAssistantMessageToolCall{*d.call}
	}
	choice := schemas.BifrostResponseChoice{ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{Delta: delta}}
	if d.finish != "" {
		choice.FinishReason = schemas.Ptr(d.finish)
	}
	resp.Choices = []schemas.BifrostResponseChoice{choice}
	return resp
}

func callStart(index uint16, id, name string) *schemas.ChatAssistantMessageToolCall {
	return &schemas.ChatAssistantMessageToolCall{
		Index: index, ID: schemas.Ptr(id), Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr(name)},
	}
}

func callArgs(index uint16, fragment string) *schemas.ChatAssistantMessageToolCall {
	return &schemas.ChatAssistantMessageToolCall{Index: index, Function: schemas.ChatAssistantMessageToolCallFunction{Arguments: fragment}}
}

func usage(prompt int) *schemas.BifrostLLMUsage {
	return &schemas.BifrostLLMUsage{PromptTokens: prompt, CompletionTokens: 1, TotalTokens: prompt + 1}
}

// feed runs a turn's chunks through the session; the last chunk is the stream's final one.
func feed(s *injectedChatStream, id string, deltas ...chatDelta) []streamEmit {
	var out []streamEmit
	for i, d := range deltas {
		out = append(out, s.onChunk(chatStreamChunk(id, d), i == len(deltas)-1)...)
	}
	return out
}

func emittedText(emits []streamEmit) string {
	var text string
	for _, e := range emits {
		if d := e.resp.Choices; len(d) > 0 && d[0].Delta != nil && d[0].Delta.Content != nil {
			text += *d[0].Delta.Content
		}
	}
	return text
}

func TestInjectedChatStream_TextThenInjectedCallThenAnswer(t *testing.T) {
	s := newInjectedChatStream(testInjectedSet(t))

	turn1 := feed(s, "turn-1",
		chatDelta{role: "assistant", text: "Let me "},
		chatDelta{text: "search."},
		chatDelta{call: callStart(0, "c1", "tavily-search")},
		chatDelta{call: callArgs(0, `{"query":`)},
		chatDelta{call: callArgs(0, `"paris"}`)},
		chatDelta{finish: "tool_calls"},
		chatDelta{noChoice: true, usage: usage(10)},
	)
	assert.Equal(t, "Let me search.", emittedText(turn1), "text streams to the client live")
	for _, e := range turn1 {
		assert.False(t, e.terminal)
		assert.Empty(t, e.resp.Choices[0].Delta.ToolCalls, "injected call deltas never reach the client")
		assert.Nil(t, e.resp.Choices[0].FinishReason)
		assert.Nil(t, e.resp.Usage)
	}
	require.True(t, s.continues())

	assistant, injected := s.endTurn()
	require.Len(t, injected, 1)
	assert.Equal(t, `{"query":"paris"}`, injected[0].Function.Arguments)
	assert.Equal(t, "Let me search.", *assistant.Content.ContentStr)
	require.Len(t, assistant.ToolCalls, 1)

	turn2 := feed(s, "turn-2",
		chatDelta{role: "assistant", text: "Sunny."},
		chatDelta{finish: "stop", usage: usage(20)},
	)
	require.Len(t, turn2, 2)
	assert.Nil(t, turn2[0].resp.Choices[0].Delta.Role, "the role is sent once per client stream")
	last := turn2[1]
	assert.True(t, last.terminal)
	assert.Equal(t, "stop", *last.resp.Choices[0].FinishReason)
	assert.Equal(t, 30, last.resp.Usage.PromptTokens, "the terminal chunk carries usage for every turn")
	assert.False(t, s.continues())

	all := append(turn1, turn2...)
	for i, e := range all {
		assert.Equal(t, "turn-1", e.resp.ID, "every chunk carries the first turn's id")
		assert.Equal(t, i, e.resp.ExtraFields.ChunkIndex, "chunk indexes run across turns")
	}
}

func TestInjectedChatStream_ClientCallBeforeInjectedCallIsDropped(t *testing.T) {
	s := newInjectedChatStream(testInjectedSet(t))
	emits := feed(s, "t",
		chatDelta{call: callStart(0, "c1", "get_weather")},
		chatDelta{call: callArgs(0, `{}`)},
		chatDelta{call: callStart(1, "c2", "tavily-search")},
		chatDelta{call: callArgs(1, `{"query":"x"}`)},
		chatDelta{finish: "tool_calls", usage: usage(5)},
	)
	assert.Empty(t, emits, "nothing client-visible was produced")
	require.True(t, s.continues())
	assistant, injected := s.endTurn()
	require.Len(t, injected, 1)
	assert.Equal(t, "c2", *injected[0].ID)
	require.Len(t, assistant.ToolCalls, 1, "the client call has no result to pair with, so it is not replayed")
}

func TestInjectedChatStream_ClientCallsOnlyAreReturnedWhole(t *testing.T) {
	s := newInjectedChatStream(testInjectedSet(t))
	emits := feed(s, "t",
		chatDelta{role: "assistant"},
		chatDelta{call: callStart(0, "c1", "get_weather")},
		chatDelta{call: callArgs(0, `{"city":`)},
		chatDelta{call: callArgs(0, `"paris"}`)},
		chatDelta{finish: "tool_calls", usage: usage(5)},
	)
	assert.False(t, s.continues())
	terminal := emits[len(emits)-1]
	require.True(t, terminal.terminal)
	calls := terminal.resp.Choices[0].Delta.ToolCalls
	require.Len(t, calls, 1, "buffered client call deltas are released as one complete call")
	assert.Equal(t, "c1", *calls[0].ID)
	assert.Equal(t, "get_weather", *calls[0].Function.Name)
	assert.Equal(t, `{"city":"paris"}`, calls[0].Function.Arguments)
	assert.Equal(t, "tool_calls", *terminal.resp.Choices[0].FinishReason)
}

func TestInjectedChatStream_StopsAtMaxDepth(t *testing.T) {
	set := testInjectedSet(t)
	set.maxDepth = 1
	s := newInjectedChatStream(set)
	emits := feed(s, "t",
		chatDelta{call: callStart(0, "c1", "tavily-search")},
		chatDelta{finish: "tool_calls", usage: usage(5)},
	)
	assert.False(t, s.continues())
	terminal := emits[len(emits)-1]
	assert.True(t, terminal.terminal)
	assert.Empty(t, terminal.resp.Choices[0].Delta.ToolCalls, "an unexecuted injected call never reaches the client")
	assert.Equal(t, "stop", *terminal.resp.Choices[0].FinishReason)
}

// gateCountingTracer counts every chunk that passes through the pause/resume gate.
type gateCountingTracer struct {
	schemas.NoOpTracer
	sends atomic.Int32
}

func (t *gateCountingTracer) GateSend(traceID string, chunk *schemas.BifrostStreamChunk, isFinal, isHardErr bool, ch chan *schemas.BifrostStreamChunk, ctx *schemas.BifrostContext) bool {
	t.sends.Add(1)
	return t.NoOpTracer.GateSend(traceID, chunk, isFinal, isHardErr, ch, ctx)
}

// A provider gates every chunk it sends on its own channel. Gating it again on the way
// to the client would buffer or replay it twice, so the pump forwards it as is.
func TestForwardUpstreamChunks_DoesNotGateTwice(t *testing.T) {
	tracer := &gateCountingTracer{}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyStreamGated, true)
	ctx.SetValue(schemas.BifrostContextKeyTracer, schemas.Tracer(tracer))
	ctx.SetValue(schemas.BifrostContextKeyTraceID, "trace")

	upstream := make(chan *schemas.BifrostStreamChunk, 2)
	upstream <- &schemas.BifrostStreamChunk{}
	upstream <- &schemas.BifrostStreamChunk{}
	close(upstream)
	out := make(chan *schemas.BifrostStreamChunk, 2)
	forwardUpstreamChunks(ctx, upstream, out)

	assert.Len(t, out, 2, "every provider chunk reaches the client")
	assert.Zero(t, tracer.sends.Load(), "chunks the provider already gated are not gated again")
}

type responsesEvents struct {
	seq    int
	respID string
}

func (r *responsesEvents) ev(typ schemas.ResponsesStreamResponseType) *schemas.BifrostResponsesStreamResponse {
	e := &schemas.BifrostResponsesStreamResponse{Type: typ, SequenceNumber: r.seq}
	r.seq++
	return e
}

func (r *responsesEvents) created() *schemas.BifrostResponsesStreamResponse {
	e := r.ev(schemas.ResponsesStreamResponseTypeCreated)
	e.Response = &schemas.BifrostResponsesResponse{ID: schemas.Ptr(r.respID)}
	return e
}

func (r *responsesEvents) item(typ schemas.ResponsesStreamResponseType, index int, item schemas.ResponsesMessage) *schemas.BifrostResponsesStreamResponse {
	e := r.ev(typ)
	e.OutputIndex = schemas.Ptr(index)
	e.Item = &item
	return e
}

func (r *responsesEvents) textDelta(index int, itemID, text string) *schemas.BifrostResponsesStreamResponse {
	e := r.ev(schemas.ResponsesStreamResponseTypeOutputTextDelta)
	e.OutputIndex = schemas.Ptr(index)
	e.ItemID = schemas.Ptr(itemID)
	e.Delta = schemas.Ptr(text)
	return e
}

func (r *responsesEvents) argsDelta(index int, itemID, args string) *schemas.BifrostResponsesStreamResponse {
	e := r.ev(schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta)
	e.OutputIndex = schemas.Ptr(index)
	e.ItemID = schemas.Ptr(itemID)
	e.Delta = schemas.Ptr(args)
	return e
}

func (r *responsesEvents) completed(input int) *schemas.BifrostResponsesStreamResponse {
	e := r.ev(schemas.ResponsesStreamResponseTypeCompleted)
	e.Response = &schemas.BifrostResponsesResponse{
		ID:    schemas.Ptr(r.respID),
		Usage: &schemas.ResponsesResponseUsage{InputTokens: input, OutputTokens: 1, TotalTokens: input + 1},
	}
	return e
}

func textItem(id, text string) schemas.ResponsesMessage {
	m := responsesText(text)
	m.ID = schemas.Ptr(id)
	return m
}

func feedResponses(s *injectedResponsesStream, events ...*schemas.BifrostResponsesStreamResponse) []responsesEmit {
	var out []responsesEmit
	for i, e := range events {
		out = append(out, s.onEvent(e, i == len(events)-1)...)
	}
	return out
}

func TestInjectedResponsesStream_TwoTurnsBecomeOneResponse(t *testing.T) {
	s := newInjectedResponsesStream(testInjectedSet(t))
	t1 := &responsesEvents{respID: "resp_1"}
	turn1 := feedResponses(s,
		t1.created(),
		t1.item(schemas.ResponsesStreamResponseTypeOutputItemAdded, 0, textItem("msg_1", "")),
		t1.textDelta(0, "msg_1", "Searching."),
		t1.item(schemas.ResponsesStreamResponseTypeOutputItemDone, 0, textItem("msg_1", "Searching.")),
		t1.item(schemas.ResponsesStreamResponseTypeOutputItemAdded, 1, responsesFunctionCall("c1", "tavily-search")),
		t1.argsDelta(1, "fc_c1", `{"query":"q"}`),
		t1.item(schemas.ResponsesStreamResponseTypeOutputItemDone, 1, responsesFunctionCall("c1", "tavily-search")),
		t1.completed(10),
	)
	require.True(t, s.continues())
	items, injected := s.endTurn()
	require.Len(t, injected, 1)
	assert.Equal(t, "c1", *injected[0].ID)
	require.Len(t, items, 2, "the turn's text and the injected call are replayed")

	t2 := &responsesEvents{respID: "resp_2"}
	turn2 := feedResponses(s,
		t2.created(),
		t2.item(schemas.ResponsesStreamResponseTypeOutputItemAdded, 0, textItem("msg_2", "")),
		t2.textDelta(0, "msg_2", "Sunny."),
		t2.item(schemas.ResponsesStreamResponseTypeOutputItemDone, 0, textItem("msg_2", "Sunny.")),
		t2.completed(20),
	)
	assert.False(t, s.continues())

	all := append(turn1, turn2...)
	var types []schemas.ResponsesStreamResponseType
	var indexes []int
	for i, e := range all {
		assert.Equal(t, i, e.event.SequenceNumber, "sequence numbers run across turns without gaps")
		types = append(types, e.event.Type)
		if e.event.OutputIndex != nil {
			indexes = append(indexes, *e.event.OutputIndex)
		}
		assert.NotEqual(t, schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta, e.event.Type, "no injected call event reaches the client")
	}
	assert.Equal(t, []schemas.ResponsesStreamResponseType{
		schemas.ResponsesStreamResponseTypeCreated,
		schemas.ResponsesStreamResponseTypeOutputItemAdded, schemas.ResponsesStreamResponseTypeOutputTextDelta, schemas.ResponsesStreamResponseTypeOutputItemDone,
		schemas.ResponsesStreamResponseTypeOutputItemAdded, schemas.ResponsesStreamResponseTypeOutputTextDelta, schemas.ResponsesStreamResponseTypeOutputItemDone,
		schemas.ResponsesStreamResponseTypeCompleted,
	}, types)
	assert.Equal(t, []int{0, 0, 0, 1, 1, 1}, indexes, "the second turn's message takes the next client output index")

	last := all[len(all)-1]
	require.True(t, last.terminal)
	assert.Equal(t, "resp_1", *last.event.Response.ID)
	require.Len(t, last.event.Response.Output, 2)
	assert.Equal(t, 30, last.event.Response.Usage.InputTokens)
}

func TestInjectedResponsesStream_ClientCallIsReleasedAtTurnEnd(t *testing.T) {
	s := newInjectedResponsesStream(testInjectedSet(t))
	r := &responsesEvents{respID: "resp_1"}
	emits := feedResponses(s,
		r.created(),
		r.item(schemas.ResponsesStreamResponseTypeOutputItemAdded, 0, responsesFunctionCall("c1", "get_weather")),
		r.argsDelta(0, "fc_c1", `{}`),
		r.item(schemas.ResponsesStreamResponseTypeOutputItemDone, 0, responsesFunctionCall("c1", "get_weather")),
		r.completed(5),
	)
	assert.False(t, s.continues())
	var types []schemas.ResponsesStreamResponseType
	for _, e := range emits {
		types = append(types, e.event.Type)
	}
	assert.Equal(t, []schemas.ResponsesStreamResponseType{
		schemas.ResponsesStreamResponseTypeCreated,
		schemas.ResponsesStreamResponseTypeOutputItemAdded,
		schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
		schemas.ResponsesStreamResponseTypeOutputItemDone,
		schemas.ResponsesStreamResponseTypeCompleted,
	}, types, "the client call's events are held, then released in order before completion")
	last := emits[len(emits)-1]
	require.Len(t, last.event.Response.Output, 1)
	assert.Equal(t, "c1", *last.event.Response.Output[0].CallID)
}

func TestInjectedResponsesStream_ClientCallBeforeInjectedCallIsDropped(t *testing.T) {
	s := newInjectedResponsesStream(testInjectedSet(t))
	r := &responsesEvents{respID: "resp_1"}
	emits := feedResponses(s,
		r.created(),
		r.item(schemas.ResponsesStreamResponseTypeOutputItemAdded, 0, responsesFunctionCall("c1", "get_weather")),
		r.item(schemas.ResponsesStreamResponseTypeOutputItemDone, 0, responsesFunctionCall("c1", "get_weather")),
		r.item(schemas.ResponsesStreamResponseTypeOutputItemAdded, 1, responsesFunctionCall("c2", "tavily-search")),
		r.item(schemas.ResponsesStreamResponseTypeOutputItemDone, 1, responsesFunctionCall("c2", "tavily-search")),
		r.completed(5),
	)
	require.Len(t, emits, 1, "only response.created reaches the client")
	require.True(t, s.continues())
	items, injected := s.endTurn()
	require.Len(t, injected, 1)
	require.Len(t, items, 1, "the client call is not replayed: it has no output to pair with")
	assert.Equal(t, "c2", *items[0].CallID)
}

// A client call the model sends before a text item stays ahead of it: once a client
// call is held, later items in the turn are held too and released in upstream order,
// so output indexes and response.completed output keep the model's order.
func TestInjectedResponsesStream_ClientCallBeforeTextKeepsOrder(t *testing.T) {
	s := newInjectedResponsesStream(testInjectedSet(t))
	r := &responsesEvents{respID: "resp_1"}
	emits := feedResponses(s,
		r.created(),
		r.item(schemas.ResponsesStreamResponseTypeOutputItemAdded, 0, responsesFunctionCall("c1", "get_weather")),
		r.item(schemas.ResponsesStreamResponseTypeOutputItemDone, 0, responsesFunctionCall("c1", "get_weather")),
		r.item(schemas.ResponsesStreamResponseTypeOutputItemAdded, 1, textItem("msg_1", "")),
		r.textDelta(1, "msg_1", "Checking."),
		r.item(schemas.ResponsesStreamResponseTypeOutputItemDone, 1, textItem("msg_1", "Checking.")),
		r.completed(5),
	)
	var order []string
	for _, e := range emits {
		if e.event.Type == schemas.ResponsesStreamResponseTypeOutputItemAdded {
			order = append(order, fmt.Sprintf("%s@%d", *e.event.Item.Type, *e.event.OutputIndex))
		}
	}
	assert.Equal(t, []string{"function_call@0", "message@1"}, order)
	output := emits[len(emits)-1].event.Response.Output
	require.Len(t, output, 2)
	assert.Equal(t, schemas.ResponsesMessageTypeFunctionCall, *output[0].Type, "response.completed keeps the model's order")
	assert.Equal(t, schemas.ResponsesMessageTypeMessage, *output[1].Type)
}

// When an injected call follows, the held client call is dropped and the text held
// behind it is released at once, under the next contiguous index.
func TestInjectedResponsesStream_HeldTextReleasedWhenInjectedCallDropsClientCall(t *testing.T) {
	s := newInjectedResponsesStream(testInjectedSet(t))
	r := &responsesEvents{respID: "resp_1"}
	emits := feedResponses(s,
		r.created(),
		r.item(schemas.ResponsesStreamResponseTypeOutputItemAdded, 0, responsesFunctionCall("c1", "get_weather")),
		r.item(schemas.ResponsesStreamResponseTypeOutputItemAdded, 1, textItem("msg_1", "")),
		r.textDelta(1, "msg_1", "Checking."),
		r.item(schemas.ResponsesStreamResponseTypeOutputItemAdded, 2, responsesFunctionCall("c2", "tavily-search")),
		r.item(schemas.ResponsesStreamResponseTypeOutputItemDone, 2, responsesFunctionCall("c2", "tavily-search")),
		r.completed(5),
	)
	require.True(t, s.continues())
	var got []string
	for _, e := range emits {
		if e.event.OutputIndex != nil {
			got = append(got, fmt.Sprintf("%s@%d", e.event.Type, *e.event.OutputIndex))
		}
	}
	assert.Equal(t, []string{"response.output_item.added@0", "response.output_text.delta@0"}, got,
		"the text takes index 0; the dropped client call never reaches the client")
}

// response.completed describes the same response as response.created, so it keeps the
// first turn's creation time.
func TestInjectedResponsesStream_CompletedKeepsFirstTurnCreatedAt(t *testing.T) {
	s := newInjectedResponsesStream(testInjectedSet(t))
	t1 := &responsesEvents{respID: "resp_1"}
	created := t1.created()
	created.Response.CreatedAt = 100
	feedResponses(s, created,
		t1.item(schemas.ResponsesStreamResponseTypeOutputItemAdded, 0, responsesFunctionCall("c1", "tavily-search")),
		t1.item(schemas.ResponsesStreamResponseTypeOutputItemDone, 0, responsesFunctionCall("c1", "tavily-search")),
		t1.completed(10),
	)
	s.endTurn()
	t2 := &responsesEvents{respID: "resp_2"}
	created2 := t2.created()
	created2.Response.CreatedAt = 200
	done := t2.completed(20)
	done.Response.CreatedAt = 200
	emits := feedResponses(s, created2, done)
	assert.Equal(t, 100, emits[len(emits)-1].event.Response.CreatedAt)
}

// Holding items behind a client call keeps the model's order, but not without bound:
// once held text passes heldTextLimit everything held is released in order, the client
// call first, and the rest of the turn streams live.
func TestInjectedResponsesStream_HoldIsBounded(t *testing.T) {
	defer func(limit int) { heldTextLimit = limit }(heldTextLimit)
	heldTextLimit = 8

	s := newInjectedResponsesStream(testInjectedSet(t))
	r := &responsesEvents{respID: "resp_1"}
	var live []responsesEmit
	for _, e := range []*schemas.BifrostResponsesStreamResponse{
		r.created(),
		r.item(schemas.ResponsesStreamResponseTypeOutputItemAdded, 0, responsesFunctionCall("c1", "get_weather")),
		r.item(schemas.ResponsesStreamResponseTypeOutputItemAdded, 1, textItem("msg_1", "")),
		r.textDelta(1, "msg_1", "a long "),
		r.textDelta(1, "msg_1", "answer that keeps going"),
		r.item(schemas.ResponsesStreamResponseTypeOutputItemDone, 0, responsesFunctionCall("c1", "get_weather")),
	} {
		live = append(live, s.onEvent(e, false)...)
	}
	var order []string
	var text string
	for _, e := range live {
		switch {
		case e.event.Item != nil && e.event.Item.Type != nil && *e.event.Item.Type == schemas.ResponsesMessageTypeFunctionCall:
			order = append(order, string(e.event.Type))
		case e.event.Type == schemas.ResponsesStreamResponseTypeOutputTextDelta:
			if len(order) == 0 || order[len(order)-1] != "text" {
				order = append(order, "text")
			}
			text += *e.event.Delta
		}
	}
	assert.Equal(t, []string{
		string(schemas.ResponsesStreamResponseTypeOutputItemAdded),
		"text",
		string(schemas.ResponsesStreamResponseTypeOutputItemDone),
	}, order, "the client call keeps its place ahead of the text, and its later events stream live")
	assert.Equal(t, "a long answer that keeps going", text, "text past the limit streams live, in order")

	end := s.onEvent(r.completed(5), true)
	require.NotEmpty(t, end)
	var calls int
	for _, item := range end[len(end)-1].event.Response.Output {
		if item.Type != nil && *item.Type == schemas.ResponsesMessageTypeFunctionCall {
			calls++
		}
	}
	assert.Equal(t, 1, calls, "the released client call is in the final output once")
}

// A client call released past the bound has reached the client, which will answer it.
// An injected call later in the turn therefore cannot re-ask the model: the next request
// would leave out the call the client already holds. The turn ends the loop as maxDepth
// does: every client call stays, and the injected call stays hidden and unexecuted.
func TestInjectedResponsesStream_ReleasedClientCallEndsTheLoop(t *testing.T) {
	defer func(limit int) { heldTextLimit = limit }(heldTextLimit)
	heldTextLimit = 8

	s := newInjectedResponsesStream(testInjectedSet(t))
	r := &responsesEvents{respID: "resp_1"}
	var emits []responsesEmit
	for _, e := range []*schemas.BifrostResponsesStreamResponse{
		r.created(),
		r.item(schemas.ResponsesStreamResponseTypeOutputItemAdded, 0, responsesFunctionCall("c1", "get_weather")),
		r.item(schemas.ResponsesStreamResponseTypeOutputItemAdded, 1, textItem("msg_1", "")),
		r.textDelta(1, "msg_1", "a long answer that keeps going"),
		r.item(schemas.ResponsesStreamResponseTypeOutputItemAdded, 2, responsesFunctionCall("c2", "tavily-search")),
		r.item(schemas.ResponsesStreamResponseTypeOutputItemDone, 2, responsesFunctionCall("c2", "tavily-search")),
		r.item(schemas.ResponsesStreamResponseTypeOutputItemDone, 0, responsesFunctionCall("c1", "get_weather")),
		r.item(schemas.ResponsesStreamResponseTypeOutputItemAdded, 3, responsesFunctionCall("c3", "get_time")),
		r.item(schemas.ResponsesStreamResponseTypeOutputItemDone, 3, responsesFunctionCall("c3", "get_time")),
	} {
		emits = append(emits, s.onEvent(e, false)...)
	}
	emits = append(emits, s.onEvent(r.completed(5), true)...)

	assert.False(t, s.continues(), "the client already holds a call, so the model is not re-asked")
	var calls []string
	for _, item := range emits[len(emits)-1].event.Response.Output {
		if item.ResponsesToolMessage != nil && item.CallID != nil {
			calls = append(calls, *item.CallID)
		}
	}
	assert.Equal(t, []string{"c1", "c3"}, calls, "every client call stays; the unexecuted injected call stays hidden")
}

// On the last permitted turn the loop stops executing, so a turn mixing injected and
// client calls keeps its client calls, as chat does at maxDepth.
func TestInjectedResponsesStream_ClientCallsSurviveAtMaxDepth(t *testing.T) {
	set := testInjectedSet(t)
	set.maxDepth = 1
	s := newInjectedResponsesStream(set)
	r := &responsesEvents{respID: "resp_1"}
	emits := feedResponses(s,
		r.created(),
		r.item(schemas.ResponsesStreamResponseTypeOutputItemAdded, 0, responsesFunctionCall("c1", "get_weather")),
		r.item(schemas.ResponsesStreamResponseTypeOutputItemDone, 0, responsesFunctionCall("c1", "get_weather")),
		r.item(schemas.ResponsesStreamResponseTypeOutputItemAdded, 1, responsesFunctionCall("c2", "tavily-search")),
		r.item(schemas.ResponsesStreamResponseTypeOutputItemDone, 1, responsesFunctionCall("c2", "tavily-search")),
		r.completed(5),
	)
	assert.False(t, s.continues())
	output := emits[len(emits)-1].event.Response.Output
	require.Len(t, output, 1, "the client call survives; the unexecuted injected call stays hidden")
	assert.Equal(t, "c1", *output[0].CallID)
}

// A failure delivered as an ordinary event ends the client stream; it must still carry
// the usage of the turns that finished before it.
func TestInjectedResponsesStream_FailureEventCarriesEarlierUsage(t *testing.T) {
	s := newInjectedResponsesStream(testInjectedSet(t))
	t1 := &responsesEvents{respID: "resp_1"}
	feedResponses(s, t1.created(),
		t1.item(schemas.ResponsesStreamResponseTypeOutputItemAdded, 0, responsesFunctionCall("c1", "tavily-search")),
		t1.item(schemas.ResponsesStreamResponseTypeOutputItemDone, 0, responsesFunctionCall("c1", "tavily-search")),
		t1.completed(10),
	)
	s.endTurn()
	t2 := &responsesEvents{respID: "resp_2"}
	failed := t2.ev(schemas.ResponsesStreamResponseTypeFailed)
	failed.Response = &schemas.BifrostResponsesResponse{ID: schemas.Ptr("resp_2"), Usage: &schemas.ResponsesResponseUsage{InputTokens: 3, TotalTokens: 3}}
	emits := feedResponses(s, failed)
	require.Len(t, emits, 1)
	require.NotNil(t, emits[0].event.Response.Usage)
	assert.Equal(t, 13, emits[0].event.Response.Usage.InputTokens, "the failure bills the finished turn too")
}

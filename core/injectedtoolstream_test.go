package bifrost

import (
	"context"
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

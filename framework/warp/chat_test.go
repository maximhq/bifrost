package warp

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
)

// chatService builds a service whose model is scripted and whose store holds a
// usable configuration, which is the shape both transports run against.
func chatService(model *scriptedModel, fake *fakeLogReader) *Service {
	return NewService(nil,
		WithConfigStore(&recordingStore{row: &tables.TableWarpConfig{
			ID: tables.WarpConfigRowID, Enabled: true, Provider: "openai", Model: "gpt-4o",
		}}),
		WithLogReader(fake),
		WithChatFunc(model.respond),
	)
}

func TestWarpFoldAssemblesToolCallsAndAnswer(t *testing.T) {
	f := newFold()
	for _, event := range []Event{
		{Type: EventStart},
		{Type: EventToolCallStart, ToolID: "c1", ToolName: "query_metrics", Arguments: `{"a":1}`},
		{Type: EventToolCallEnd, ToolID: "c1", ToolName: "query_metrics", DurationMs: 12, Failed: true},
		{Type: EventDelta, Delta: "Hello, "},
		{Type: EventDelta, Delta: "world."},
		{Type: EventDone, FinishReason: "stop", Iterations: 2, Usage: &schemas.BifrostLLMUsage{TotalTokens: 7}},
	} {
		f.apply(event)
	}
	response := f.result()
	require.Equal(t, "Hello, world.", response.Answer)
	require.Len(t, response.ToolCalls, 1)
	require.Equal(t, ChatToolCall{Name: "query_metrics", Arguments: `{"a":1}`, DurationMs: 12, Failed: true}, response.ToolCalls[0])
	require.Equal(t, "stop", response.FinishReason)
	require.Equal(t, 2, response.Iterations)
	require.Equal(t, 7, response.Usage.TotalTokens)
	require.Nil(t, response.Error)
}

func TestWarpFoldRecordsTerminalError(t *testing.T) {
	f := newFold()
	f.apply(Event{Type: EventError, Code: ErrUpstream, Message: "boom"})
	require.Equal(t, &ChatError{Code: ErrUpstream, Message: "boom"}, f.result().Error)
	require.Equal(t, []ChatToolCall{}, f.result().ToolCalls, "tool_calls must serialize as an empty list, never null")
}

// The two transports differ only in their sink. The buffered response and the
// streamed frames folded back together must describe the same turn.
func TestWarpRunTurnBufferedAndStreamedAgree(t *testing.T) {
	turns := func() *scriptedModel {
		return &scriptedModel{turns: []*schemas.BifrostChatResponse{
			toolTurn("call-1", "query_metrics", `{"filters":{},"metrics":["summary"]}`),
			textTurn("42 requests."),
		}}
	}
	request := &ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "how many?"}}}

	buffered := chatService(turns(), &fakeLogReader{})
	turn, err := buffered.NewTurn(context.Background(), request, 64)
	require.NoError(t, err)
	fromBuffer := buffered.RunTurn(context.Background(), turn, nil)

	streamed := chatService(turns(), &fakeLogReader{})
	turn, err = streamed.NewTurn(context.Background(), request, 64)
	require.NoError(t, err)
	replay := newFold()
	fromStream := streamed.RunTurn(context.Background(), turn, func(event Event) bool {
		replay.apply(event)
		return true
	})

	require.Equal(t, fromBuffer, fromStream)
	require.Equal(t, fromStream, replay.result(), "the sink must see every event the fold saw")
	require.Equal(t, "42 requests.", fromBuffer.Answer)
	require.Len(t, fromBuffer.ToolCalls, 1)
}

// A sink that refuses an event is a client that went away. The loop must stop
// asking the model rather than finishing an answer nobody will read.
//
// Events are buffered, so the agent can be a few steps ahead of the sink when
// the refusal lands; what matters is that the cancellation reaches the model
// call in flight and that no call starts after it.
func TestWarpRunTurnStopsWhenSinkRefuses(t *testing.T) {
	scripted := &scriptedModel{turns: []*schemas.BifrostChatResponse{
		toolTurn("loop", "query_metrics", `{"filters":{},"metrics":["summary"]}`),
	}}
	var calls atomic.Int32
	// started fires when the second model call is actually in flight. The event
	// channel is buffered, so the agent emitting tool_call_end and the sink
	// reading it are not ordered against each other: without this signal the
	// sink can refuse, cancel the run, and have the agent exit at its
	// top-of-loop context check before call 2 ever begins - leaving `released`
	// closed by nobody and the test failing on its own timeout even though
	// cancellation worked perfectly.
	started := make(chan struct{})
	released := make(chan struct{})
	blocking := func(ctx context.Context, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		if calls.Add(1) == 1 {
			return scripted.respond(ctx, req)
		}
		// The second call behaves like a real provider: it takes time, and it
		// only returns once the request is cancelled underneath it.
		close(started)
		<-ctx.Done()
		close(released)
		return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: ctx.Err().Error()}}
	}
	service := NewService(nil,
		WithConfigStore(&recordingStore{row: &tables.TableWarpConfig{
			ID: tables.WarpConfigRowID, Enabled: true, Provider: "openai", Model: "gpt-4o",
		}}),
		WithLogReader(&fakeLogReader{}),
		WithChatFunc(blocking),
	)
	turn, err := service.NewTurn(context.Background(), &ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}}, 32)
	require.NoError(t, err)

	// Bounded, so a regression in sink cancellation fails this test with a
	// readable timeout instead of blocking RunTurn until the runner kills the
	// whole package - Turn.Budget does not apply to a scripted ChatFunc that
	// only waits on its context.
	runCtx, cancelRun := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancelRun)
	response := service.RunTurn(runCtx, turn, func(event Event) bool {
		if event.Type != EventToolCallEnd {
			return true
		}
		// Refuse only once call 2 is genuinely in flight, so "the client left
		// mid-call" is the scenario under test rather than one of two schedules.
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Error("the second model call never started")
		}
		return false
	})
	require.Nil(t, response.Error, "the refused frame is not an error, the client simply left")

	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("the in-flight model call was never cancelled")
	}
	require.Equal(t, int32(2), calls.Load(), "no model call may start after the client is gone")
}

func TestWarpNewTurnMapsRequestProblems(t *testing.T) {
	service := chatService(&scriptedModel{}, &fakeLogReader{})
	_, err := service.NewTurn(context.Background(), &ChatRequest{}, 10)
	require.ErrorIs(t, err, ErrEmptyConversation)

	_, err = service.NewTurn(context.Background(), &ChatRequest{Messages: []ChatMessage{{Role: "system", Content: "x"}}}, 10)
	require.ErrorIs(t, err, ErrBadRole)

	_, err = service.NewTurn(context.Background(), &ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "x"}}}, MaxHistoryBytes+1)
	require.ErrorIs(t, err, ErrConversationTooLong)

	unconfigured := NewService(nil, WithConfigStore(&recordingStore{}), WithLogReader(&fakeLogReader{}), WithChatFunc((&scriptedModel{}).respond))
	_, err = unconfigured.NewTurn(context.Background(), &ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "x"}}}, 10)
	require.ErrorIs(t, err, ErrUnavailable)
}

// Without a log reader there is nothing to research, so the service must say
// so up front rather than register a chat route that always fails.
func TestWarpCanChatRequiresLogReader(t *testing.T) {
	require.False(t, NewService(nil, WithChatFunc((&scriptedModel{}).respond)).CanChat())
	require.True(t, NewService(nil, WithLogReader(&fakeLogReader{}), WithChatFunc((&scriptedModel{}).respond)).CanChat())
	require.False(t, NewService(nil).CanChat())
}

// SetLogReader writes s.client under the lock; every reader must take it.
//
// ReloadPlugin calls SetLogReader while requests are in flight, so an
// unsynchronized read of s.client in chatFuncFor or Shutdown is a data race on
// a pointer written concurrently - and beyond the race detector, a request can
// observe a stale nil client and return ErrNoModelClient after CanChat()
// already reported true.
func TestWarpServiceClientAccessIsRaceFree(t *testing.T) {
	service := NewService(nil, WithConfigStore(&recordingStore{row: &tables.TableWarpConfig{ID: tables.WarpConfigRowID, Enabled: true, Provider: "openai", Model: "gpt-4o"}}))
	config := &schemas.WarpConfig{Provider: "openai", Model: "gpt-4o"}

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				service.SetLogReader(&fakeLogReader{})
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				_ = service.chatFuncFor(context.Background(), config)
				_ = service.CanChat()
			}
		}()
	}
	wg.Wait()

	// Shutdown reads the same field and must be safe alongside a late rebind.
	wg.Add(2)
	go func() { defer wg.Done(); service.Shutdown() }()
	go func() { defer wg.Done(); service.SetLogReader(&fakeLogReader{}) }()
	wg.Wait()
}

// SetLogReader(nil) leaves the model client in place, so a turn could still
// resolve a usable chat func while the reader went nil underneath it - and every
// tool this agent has reads logs, so the first one the model reached for
// dereferenced nil inside the agent.
func TestWarpNewTurnRefusesWhenTheLogReaderIsGone(t *testing.T) {
	service := chatService(&scriptedModel{}, &fakeLogReader{})
	defer service.Shutdown()

	request := &ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "how much?"}}}
	turn, err := service.NewTurn(context.Background(), request, 64)
	require.NoError(t, err, "a service with a reader must still accept turns")
	require.NotNil(t, turn)

	// The logging plugin is removed while the model client stays up.
	service.SetLogReader(nil)
	_, err = service.NewTurn(context.Background(), request, 64)
	require.ErrorIs(t, err, ErrUnavailable,
		"a turn with no reader cannot answer anything, so it must be refused rather than panic in a tool")
}

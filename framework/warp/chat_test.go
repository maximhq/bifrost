package warp

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// chatService builds a service whose model is scripted and whose store holds a
// usable configuration, which is the shape both transports run against.
func chatService(model *scriptedModel, fake *fakeLogReader) *Service {
	return NewService(nil,
		WithConfigStore(&recordingStore{row: validWarpConfigRow()}),
		WithVectorStore(newFakeWarpVectorStore()),
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
		return &scriptedModel{turns: []*schemas.BifrostResponsesResponse{
			ToolTurn("call-1", "query_metrics", `{"filters":{},"metrics":["summary"]}`),
			TextTurn("42 requests."),
		}}
	}
	// Same thread on both sides: a new turn mints a fresh id, which would
	// otherwise be the one field the two responses legitimately differ on.
	request := &ChatRequest{ConversationID: "thread-1", Messages: []ChatMessage{{Role: "user", Content: "how many?"}}}

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

	// DurationMs is a real wall-clock measurement of the same near-instant
	// fake call, taken independently on each side - it was never going to
	// match to the millisecond, and comparing it is not what this test is
	// for. Everything else about the two transports' output must agree
	// exactly.
	require.Equal(t, normalizeToolCallDurations(fromBuffer), normalizeToolCallDurations(fromStream))
	require.Equal(t, normalizeToolCallDurations(fromStream), normalizeToolCallDurations(replay.result()), "the sink must see every event the fold saw")
	require.Equal(t, "42 requests.", fromBuffer.Answer)
	require.Len(t, fromBuffer.ToolCalls, 1)
}

func normalizeToolCallDurations(response ChatResponse) ChatResponse {
	normalized := make([]ChatToolCall, len(response.ToolCalls))
	for i, call := range response.ToolCalls {
		call.DurationMs = 0
		normalized[i] = call
	}
	response.ToolCalls = normalized
	return response
}

// A sink that refuses an event is a client that went away. The loop must stop
// asking the model rather than finishing an answer nobody will read.
//
// Events are buffered, so the agent can be a few steps ahead of the sink when
// the refusal lands; what matters is that the cancellation reaches the model
// call in flight and that no call starts after it.
func TestWarpRunTurnStopsWhenSinkRefuses(t *testing.T) {
	scripted := &scriptedModel{turns: []*schemas.BifrostResponsesResponse{
		ToolTurn("loop", "query_metrics", `{"filters":{},"metrics":["summary"]}`),
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
	blocking := func(ctx context.Context, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
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
		WithConfigStore(&recordingStore{row: validWarpConfigRow()}),
		WithVectorStore(newFakeWarpVectorStore()),
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

// NewTurn is the one place a raw client-sent offset exists; everything
// downstream trusts what it produces, so the sanitizing has to happen here.
func TestWarpNewTurnSanitizesUTCOffset(t *testing.T) {
	service := chatService(&scriptedModel{}, &fakeLogReader{})
	message := []ChatMessage{{Role: "user", Content: "x"}}

	turn, err := service.NewTurn(context.Background(), &ChatRequest{Messages: message, UTCOffsetMinutes: 330}, 10)
	require.NoError(t, err)
	require.Equal(t, 330, turn.utcOffsetMinutes)

	turn, err = service.NewTurn(context.Background(), &ChatRequest{Messages: message, UTCOffsetMinutes: 100000}, 10)
	require.NoError(t, err)
	require.Equal(t, 0, turn.utcOffsetMinutes, "an out-of-range offset must fall back to UTC, not ride through unchecked")

	turn, err = service.NewTurn(context.Background(), &ChatRequest{Messages: message}, 10)
	require.NoError(t, err)
	require.Equal(t, 0, turn.utcOffsetMinutes, "an omitted offset must default to UTC")
}

// NewTurn is also the one place a raw client-sent zone name exists.
func TestWarpNewTurnSanitizesTimezone(t *testing.T) {
	service := chatService(&scriptedModel{}, &fakeLogReader{})
	message := []ChatMessage{{Role: "user", Content: "x"}}

	turn, err := service.NewTurn(context.Background(), &ChatRequest{Messages: message, Timezone: "Asia/Kolkata"}, 10)
	require.NoError(t, err)
	require.Equal(t, "Asia/Kolkata", turn.timezone)

	turn, err = service.NewTurn(context.Background(), &ChatRequest{Messages: message, Timezone: "Not/AZone"}, 10)
	require.NoError(t, err)
	require.Equal(t, "", turn.timezone, "an unrecognized zone must not ride through unchecked")

	turn, err = service.NewTurn(context.Background(), &ChatRequest{Messages: message}, 10)
	require.NoError(t, err)
	require.Equal(t, "", turn.timezone, "an omitted zone must fall back to offset-only")
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
	service := NewService(nil, WithConfigStore(&recordingStore{row: validWarpConfigRow()}))
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
				_ = service.chatFuncFor(context.Background(), config, "conv-1")
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

// The done frame carries the thread id, so a client that started a new thread
// learns what to send next without a second request. The buffered response
// carries the same id.
func TestWarpRunTurnStampsConversationIDOnDone(t *testing.T) {
	store := newMemoryConversations()
	model := &scriptedModel{turns: []*schemas.BifrostResponsesResponse{TextTurn("42 requests.")}}
	service := NewService(nil,
		WithConfigStore(&recordingStore{row: validWarpConfigRow()}),
		WithVectorStore(newFakeWarpVectorStore()),
		WithLogReader(&fakeLogReader{}),
		WithChatFunc(model.respond),
		WithConversationStore(store),
	)
	turn, err := service.NewTurn(context.Background(), &ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "how many?"}}}, 64)
	require.NoError(t, err)

	var doneID string
	response := service.RunTurn(ownerCtx("u1"), turn, func(event Event) bool {
		if event.Type == EventDone {
			doneID = event.ConversationID
		}
		return true
	})
	require.NotEmpty(t, doneID)
	require.Equal(t, doneID, response.ConversationID)
	require.Len(t, store.threads[doneID].Messages, 2)
}

// A streamed error must carry the thread id, like a streamed done does.
//
// An error-terminal run is still filed - what was asked and how it failed - but
// only after the event loop ends, so the error frame had already reached the
// client with an empty ConversationID. A streaming client that hits an error
// therefore never learns which thread it was in: the next question opens a new
// one and the failed exchange is orphaned in the history it cannot reach.
func TestWarpStreamedErrorCarriesTheConversationID(t *testing.T) {
	store := newMemoryConversations()
	failing := func(context.Context, *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "provider is down"}}
	}
	service := NewService(nil,
		WithConfigStore(&recordingStore{row: validWarpConfigRow()}),
		WithConversationStore(store),
		// A reader, because CanChat requires one and the route will not dispatch
		// without it - the model failing before any tool runs is what this test is
		// about, not running without a log store.
		WithLogReader(&fakeLogReader{}),
		WithChatFunc(failing))

	turn, err := service.NewTurn(ownerCtx("u1"), &ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: "how much did we spend?"}},
	}, 64)
	require.NoError(t, err)

	var errorEvents []Event
	response := service.RunTurn(ownerCtx("u1"), turn, func(event Event) bool {
		if event.Type == EventError {
			errorEvents = append(errorEvents, event)
		}
		return true
	})

	require.NotEmpty(t, errorEvents, "the run must end on an error frame")
	require.NotEmpty(t, response.ConversationID, "the failed exchange is filed")
	require.Equal(t, response.ConversationID, errorEvents[len(errorEvents)-1].ConversationID,
		"the streamed error must name the thread it was filed under")
	require.Len(t, store.threads, 1, "and it must be filed exactly once")
}

// A conversation id longer than the storage column must be refused before the
// model runs. Accepted, it is treated as an existing thread, the model request
// is paid for, and the append then fails against varchar(36) - persistTurn
// returns an empty id and the turn silently vanishes from history. Not a UUID
// check: the local contract accepts ids like "thread-1".
func TestWarpNewTurnRejectsOversizedConversationID(t *testing.T) {
	service := chatService(&scriptedModel{}, &fakeLogReader{})
	_, err := service.NewTurn(context.Background(), &ChatRequest{
		ConversationID: strings.Repeat("x", MaxConversationIDChars+1),
		Messages:       []ChatMessage{{Role: "user", Content: "hi"}},
	}, 10)
	require.ErrorIs(t, err, ErrBadConversationID)

	// At the limit, and non-UUID shapes, stay accepted.
	turn, err := service.NewTurn(context.Background(), &ChatRequest{
		ConversationID: "thread-1",
		Messages:       []ChatMessage{{Role: "user", Content: "hi"}},
	}, 10)
	require.NoError(t, err)
	require.Equal(t, "thread-1", turn.ConversationID)
}

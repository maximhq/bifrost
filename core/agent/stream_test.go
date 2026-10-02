package agent

import (
	"context"
	"iter"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// upstreamStream is one live stream inside the fake upstream agent. Tests drive
// it explicitly — pushing events, failing it, or observing its teardown — so
// every streaming assertion is made on channels rather than on sleeps.
type upstreamStream struct {
	events chan a2a.Event
	fail   chan error
	done   chan struct{} // closed when the upstream side of this stream stops
	ctxErr chan error    // receives the upstream request context's error, if any
}

func newUpstreamStream() *upstreamStream {
	return &upstreamStream{
		events: make(chan a2a.Event, 8),
		fail:   make(chan error, 1),
		done:   make(chan struct{}),
		ctxErr: make(chan error, 1),
	}
}

// streamingUpstream extends the task-owning upstream fake with streaming methods
// whose lifetime the test controls, plus enough task state to prove that ending
// a subscription is not a task cancellation.
type streamingUpstream struct {
	taskUpstream
	mu       sync.Mutex
	streams  []*upstreamStream
	created  chan *upstreamStream
	canceled atomic.Bool
}

func newStreamingUpstream() *streamingUpstream {
	return &streamingUpstream{created: make(chan *upstreamStream, 16)}
}

// open registers a new upstream stream and returns its driver plus the sequence
// the SDK server will iterate.
func (u *streamingUpstream) open(ctx context.Context) iter.Seq2[a2a.Event, error] {
	stream := newUpstreamStream()
	u.mu.Lock()
	u.streams = append(u.streams, stream)
	u.mu.Unlock()
	u.created <- stream
	return func(y func(a2a.Event, error) bool) {
		defer close(stream.done)
		for {
			select {
			case <-ctx.Done():
				stream.ctxErr <- ctx.Err()
				return
			case err := <-stream.fail:
				y(nil, err)
				return
			case event := <-stream.events:
				if !y(event, nil) {
					return
				}
			}
		}
	}
}

func (u *streamingUpstream) streamCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.streams)
}

func (u *streamingUpstream) SendStreamingMessage(ctx context.Context, _ *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
	u.record("SendStreamingMessage")
	return u.open(ctx)
}

func (u *streamingUpstream) SubscribeToTask(ctx context.Context, _ *a2a.SubscribeToTaskRequest) iter.Seq2[a2a.Event, error] {
	u.record("SubscribeToTask")
	return u.open(ctx)
}

// CancelTask is the only thing that may move the task out of working: if a
// subscription teardown ever cancelled a task, this state would flip without an
// explicit protocol call.
func (u *streamingUpstream) CancelTask(_ context.Context, req *a2a.CancelTaskRequest) (*a2a.Task, error) {
	u.record("CancelTask")
	u.canceled.Store(true)
	return &a2a.Task{ID: req.ID, ContextID: "upstream-context", Status: a2a.TaskStatus{State: a2a.TaskStateCanceled}}, nil
}

func (u *streamingUpstream) GetTask(_ context.Context, req *a2a.GetTaskRequest) (*a2a.Task, error) {
	u.record("GetTask")
	state := a2a.TaskStateWorking
	if u.canceled.Load() {
		state = a2a.TaskStateCanceled
	}
	return &a2a.Task{ID: req.ID, ContextID: "upstream-context", Status: a2a.TaskStatus{State: state}}, nil
}

// streamingGateway wires the streaming upstream behind a real manager and returns
// an SDK client speaking to the gateway's JSON-RPC binding, mirroring the
// non-streaming taskUpstreamGatewayManager fixture. The upstream card advertises
// streaming, without which the SDK client the gateway builds would silently
// downgrade every stream to a unary send.
func streamingGateway(t *testing.T, plugins ...*recordingA2APlugin) (*streamingUpstream, *a2aclient.Client, *Manager) {
	t.Helper()
	upstreamHandler := newStreamingUpstream()
	mux := http.NewServeMux()
	upstream := httptest.NewServer(mux)
	t.Cleanup(upstream.Close)
	card := &a2a.AgentCard{
		Name:                "tasks",
		Version:             "1",
		SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface(upstream.URL+"/rpc", a2a.TransportProtocolJSONRPC)},
		Capabilities:        a2a.AgentCapabilities{Streaming: true},
	}
	mux.Handle(agentCardPath, a2asrv.NewStaticAgentCardHandler(card))
	mux.Handle("/rpc", a2asrv.NewJSONRPCHandler(upstreamHandler))

	m, err := NewManager(context.Background(), &memoryStore{regs: map[string]schemas.AgentRegistration{}}, nil, "http://gateway", nil)
	require.NoError(t, err)
	t.Cleanup(m.Close)
	if len(plugins) > 0 {
		pipeline := &fakePipeline{plugins: plugins}
		m.SetPluginPipeline(func() PluginPipeline { return pipeline }, func(PluginPipeline) {})
	}
	_, err = m.Create(context.Background(), CreateRequest{Name: "tasks", AgentCardURL: upstream.URL + agentCardPath})
	require.NoError(t, err)
	protocol, ok := m.ProtocolHandler("tasks")
	require.True(t, ok)
	proxy := httptest.NewServer(protocol)
	t.Cleanup(proxy.Close)
	client, err := a2aclient.NewFromEndpoints(context.Background(), []*a2a.AgentInterface{a2a.NewAgentInterface(proxy.URL, a2a.TransportProtocolJSONRPC)}, a2aclient.WithJSONRPCTransport(http.DefaultClient))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Destroy() })
	return upstreamHandler, client, m
}

// downstreamStream is a consumer of the gateway's SSE stream running on its own
// goroutine, with an explicit cancel standing in for the client disappearing and
// a gate channel standing in for a subscriber that stops reading.
type downstreamStream struct {
	events chan a2a.Event
	errs   chan error
	cancel context.CancelFunc
	gate   chan struct{}
	closed chan struct{}
}

// startDownstream begins one gateway stream. When gate is non-nil the consumer
// blocks on it after the first event, modelling a slow subscriber.
func startDownstream(t *testing.T, client *a2aclient.Client, gate chan struct{}) *downstreamStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	d := &downstreamStream{events: make(chan a2a.Event, 8), errs: make(chan error, 4), cancel: cancel, gate: gate, closed: make(chan struct{})}
	go func() {
		defer close(d.closed)
		first := true
		for event, err := range client.SendStreamingMessage(ctx, &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hi"))}) {
			if err != nil {
				d.errs <- err
				return
			}
			if first && d.gate != nil {
				first = false
				<-d.gate
			}
			select {
			case d.events <- event:
			case <-ctx.Done():
				return
			}
		}
	}()
	t.Cleanup(cancel)
	return d
}

// nextUpstreamStream waits for the upstream agent to have opened its side.
func nextUpstreamStream(t *testing.T, u *streamingUpstream) *upstreamStream {
	t.Helper()
	select {
	case stream := <-u.created:
		return stream
	case <-time.After(5 * time.Second):
		t.Fatal("upstream stream was never opened")
		return nil
	}
}

func waitEvent(t *testing.T, d *downstreamStream) a2a.Event {
	t.Helper()
	select {
	case event := <-d.events:
		return event
	case err := <-d.errs:
		t.Fatalf("stream failed instead of producing an event: %v", err)
		return nil
	case <-time.After(5 * time.Second):
		t.Fatal("no downstream event")
		return nil
	}
}

func waitClosed(t *testing.T, what string, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s never finished", what)
	}
}

// leaseCount reports how many in-flight operations hold the runtime's current
// client generation, which is how a test proves a stream released its lease.
func leaseCount(t *testing.T, m *Manager, name string) int {
	t.Helper()
	runtime, ok := m.runtimes.load(name)
	require.True(t, ok)
	runtime.mu.Lock()
	generation := runtime.generation
	runtime.mu.Unlock()
	require.NotNil(t, generation)
	generation.mu.Lock()
	defer generation.mu.Unlock()
	return generation.leases
}

func message(text string) a2a.Event {
	return a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(text))
}

// One downstream stream must map to exactly one upstream stream, and finishing
// the downstream stream must finish that upstream stream and release its lease.
func TestDownstreamStreamOwnsExactlyOneUpstreamStream(t *testing.T) {
	upstreamHandler, client, m := streamingGateway(t)

	downstream := startDownstream(t, client, nil)
	upstream := nextUpstreamStream(t, upstreamHandler)
	upstream.events <- message("one")
	require.NotNil(t, waitEvent(t, downstream))

	require.Equal(t, 1, upstreamHandler.streamCount(), "one downstream stream opens exactly one upstream stream")
	require.Equal(t, 1, leaseCount(t, m, "tasks"), "the stream holds its generation lease while it runs")

	close(upstream.events) // an empty closed channel keeps the select from progressing
	downstream.cancel()
	waitClosed(t, "downstream consumer", downstream.closed)
	waitClosed(t, "upstream stream", upstream.done)
	require.Eventually(t, func() bool { return leaseCount(t, m, "tasks") == 0 }, 5*time.Second, time.Millisecond)
	require.Equal(t, 1, upstreamHandler.streamCount(), "no stream is re-opened after termination")
}

// A vanished downstream client must tear down its upstream stream and release its
// lease, without cancelling the durable task the subscription was watching.
func TestDownstreamDisconnectEndsUpstreamStreamButNotTheTask(t *testing.T) {
	upstreamHandler, client, m := streamingGateway(t)

	downstream := startDownstream(t, client, nil)
	upstream := nextUpstreamStream(t, upstreamHandler)
	upstream.events <- message("one")
	require.NotNil(t, waitEvent(t, downstream))
	require.Equal(t, 1, leaseCount(t, m, "tasks"), "the lease is held for the lifetime of the stream")

	downstream.cancel() // the client disappears

	select {
	case err := <-upstream.ctxErr:
		require.Error(t, err, "the per-stream scope is cancelled when the downstream client disappears")
	case <-time.After(5 * time.Second):
		t.Fatal("the upstream stream was not cancelled by the downstream disconnect")
	}
	waitClosed(t, "upstream stream", upstream.done)
	require.Eventually(t, func() bool { return leaseCount(t, m, "tasks") == 0 }, 5*time.Second, time.Millisecond, "a vanished client releases its per-stream generation lease")

	require.NotContains(t, upstreamHandler.calls(), "CancelTask", "ending a subscription must never cancel the task")
	task, err := client.GetTask(context.Background(), &a2a.GetTaskRequest{ID: "upstream-task"})
	require.NoError(t, err)
	require.Equal(t, a2a.TaskStateWorking, task.Status.State, "the task keeps running after its subscriber left")

	canceled, err := client.CancelTask(context.Background(), &a2a.CancelTaskRequest{ID: "upstream-task"})
	require.NoError(t, err)
	require.Equal(t, a2a.TaskStateCanceled, canceled.Status.State, "only the explicit CancelTask operation cancels the task")
}

// A terminal upstream error must finish the downstream stream with the SDK-native
// error rather than a translated one, and release the stream's resources — it is
// another termination path that must release the generation lease just the same.
func TestUpstreamTerminalErrorFinishesDownstreamStream(t *testing.T) {
	upstreamHandler, client, m := streamingGateway(t)

	downstream := startDownstream(t, client, nil)
	upstream := nextUpstreamStream(t, upstreamHandler)
	upstream.fail <- a2a.ErrTaskNotFound

	select {
	case err := <-downstream.errs:
		require.ErrorIs(t, err, a2a.ErrTaskNotFound, "the SDK-native error reaches the downstream client unchanged")
	case <-time.After(5 * time.Second):
		t.Fatal("the downstream stream never finished")
	}
	waitClosed(t, "upstream stream", upstream.done)
	waitClosed(t, "downstream consumer", downstream.closed)
	require.Eventually(t, func() bool { return leaseCount(t, m, "tasks") == 0 }, 5*time.Second, time.Millisecond)
	require.Equal(t, 1, upstreamHandler.streamCount())
}

// Cancelling one stream must leave every other stream producing events, and a
// subscriber that stops reading must not stall an unrelated stream.
func TestConcurrentStreamsAreIsolated(t *testing.T) {
	upstreamHandler, client, m := streamingGateway(t)

	first := startDownstream(t, client, nil)
	firstUpstream := nextUpstreamStream(t, upstreamHandler)
	firstUpstream.events <- message("first-one")
	require.NotNil(t, waitEvent(t, first))

	second := startDownstream(t, client, nil)
	secondUpstream := nextUpstreamStream(t, upstreamHandler)
	secondUpstream.events <- message("second-one")
	require.NotNil(t, waitEvent(t, second))
	require.Equal(t, 2, upstreamHandler.streamCount())

	first.cancel()
	waitClosed(t, "first upstream stream", firstUpstream.done)

	secondUpstream.events <- message("second-two")
	require.NotNil(t, waitEvent(t, second), "cancelling one stream leaves the other producing events")
	require.Eventually(t, func() bool { return leaseCount(t, m, "tasks") == 1 }, 5*time.Second, time.Millisecond, "only the cancelled stream's lease is released")

	select {
	case <-secondUpstream.done:
		t.Fatal("the surviving upstream stream must not be torn down")
	default:
	}
}

// A stuck downstream consumer holds only its own goroutine and lease; unrelated
// streams complete normally while it is blocked.
func TestSlowSubscriberDoesNotStallOtherStreams(t *testing.T) {
	upstreamHandler, client, _ := streamingGateway(t)

	gate := make(chan struct{})
	slow := startDownstream(t, client, gate)
	slowUpstream := nextUpstreamStream(t, upstreamHandler)
	slowUpstream.events <- message("slow-one") // consumed, then the consumer blocks on gate
	slowUpstream.events <- message("slow-two")

	fast := startDownstream(t, client, nil)
	fastUpstream := nextUpstreamStream(t, upstreamHandler)
	fastUpstream.events <- message("fast-one")
	require.NotNil(t, waitEvent(t, fast), "a stuck subscriber must not stall an unrelated stream")
	fastUpstream.fail <- a2a.ErrTaskNotFound
	select {
	case err := <-fast.errs:
		require.ErrorIs(t, err, a2a.ErrTaskNotFound)
	case <-time.After(5 * time.Second):
		t.Fatal("the unrelated stream never finished while a slow subscriber was blocked")
	}
	waitClosed(t, "unrelated upstream stream", fastUpstream.done)

	select {
	case <-slowUpstream.done:
		t.Fatal("the slow stream must still be alive")
	default:
	}
	close(gate)
	require.NotNil(t, waitEvent(t, slow), "the slow subscriber resumes on its own schedule")
	slow.cancel()
	waitClosed(t, "slow upstream stream", slowUpstream.done)
}

// The A2A post-hooks must run exactly once per stream, at stream termination,
// including when the downstream client disconnected mid-stream.
func TestStreamPostHooksRunExactlyOnceOnDownstreamDisconnect(t *testing.T) {
	var order []string
	plugin := &recordingA2APlugin{name: "recorder", order: &order}
	upstreamHandler, client, _ := streamingGateway(t, plugin)

	downstream := startDownstream(t, client, nil)
	upstream := nextUpstreamStream(t, upstreamHandler)
	upstream.events <- message("one")
	require.NotNil(t, waitEvent(t, downstream))
	require.Equal(t, int64(0), plugin.postCalls.Load(), "post-hooks must not run per event")

	downstream.cancel()
	waitClosed(t, "upstream stream", upstream.done)
	require.Eventually(t, func() bool { return plugin.postCalls.Load() == 1 }, 5*time.Second, time.Millisecond, "post-hooks run once at stream termination")
	require.Never(t, func() bool { return plugin.postCalls.Load() != 1 }, 100*time.Millisecond, 10*time.Millisecond, "post-hooks must not run again")
}

func TestStreamIsLazyAndPreHookPrecedesUpstreamCreation(t *testing.T) {
	var order []string
	plugin := &recordingA2APlugin{name: "recorder", order: &order}
	_, _, m := streamingGateway(t, plugin)
	runtime, ok := m.runtimes.load("tasks")
	require.True(t, ok)
	handler := &proxyRequestHandler{runtime: runtime, manager: m, config: runtime.config}
	var calls atomic.Int64

	stream := forwardUpstreamStream(context.Background(), handler, handler.taskEnvelope(schemas.A2ARequestTypeSubscribeToTask, &a2a.SubscribeToTaskRequest{ID: "task-1"}, "task-1"), func(context.Context, sdkClient) iter.Seq2[a2a.Event, error] {
		require.Equal(t, []string{"pre:recorder"}, order)
		calls.Add(1)
		return func(func(a2a.Event, error) bool) {}
	})
	require.Empty(t, order)
	require.Zero(t, calls.Load())

	for range stream {
	}
	require.Equal(t, int64(1), calls.Load())
	require.Equal(t, []string{"pre:recorder", "post:recorder"}, order)
}

func TestStreamDenialPrecedesLeaseAndUpstreamCreation(t *testing.T) {
	var order []string
	denier := &recordingA2APlugin{name: "denier", order: &order, shortCircuit: &schemas.A2APluginShortCircuit{Error: &schemas.BifrostError{StatusCode: schemas.Ptr(403), Error: &schemas.ErrorField{Message: "denied"}}}}
	_, _, m := streamingGateway(t, denier)
	runtime, ok := m.runtimes.load("tasks")
	require.True(t, ok)
	handler := &proxyRequestHandler{runtime: runtime, manager: m, config: runtime.config}
	var calls atomic.Int64
	var yielded int

	stream := forwardUpstreamStream(context.Background(), handler, handler.taskEnvelope(schemas.A2ARequestTypeSubscribeToTask, &a2a.SubscribeToTaskRequest{ID: "task-1"}, "task-1"), func(context.Context, sdkClient) iter.Seq2[a2a.Event, error] {
		calls.Add(1)
		return nil
	})
	stream(func(_ a2a.Event, err error) bool {
		yielded++
		require.ErrorIs(t, err, a2a.ErrUnauthorized)
		return true
	})

	require.Zero(t, calls.Load())
	require.Equal(t, 1, yielded)
	require.Equal(t, []string{"pre:denier", "post:denier"}, order)
	require.Zero(t, leaseCount(t, m, "tasks"))
}

func TestStreamConsumerStopHonorsYieldAndEmitsNothingFurther(t *testing.T) {
	var order []string
	plugin := &recordingA2APlugin{name: "recorder", order: &order}
	_, _, m := streamingGateway(t, plugin)
	runtime, ok := m.runtimes.load("tasks")
	require.True(t, ok)
	handler := &proxyRequestHandler{runtime: runtime, manager: m, config: runtime.config}
	var upstreamYields atomic.Int64
	var downstreamYields atomic.Int64

	stream := forwardUpstreamStream(context.Background(), handler, handler.taskEnvelope(schemas.A2ARequestTypeSubscribeToTask, &a2a.SubscribeToTaskRequest{ID: "task-1"}, "task-1"), func(context.Context, sdkClient) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			upstreamYields.Add(1)
			if !yield(message("one"), nil) {
				return
			}
			upstreamYields.Add(1)
			yield(message("two"), nil)
		}
	})
	stream(func(a2a.Event, error) bool {
		downstreamYields.Add(1)
		return false
	})

	require.Equal(t, int64(1), upstreamYields.Load())
	require.Equal(t, int64(1), downstreamYields.Load())
	require.Equal(t, int64(1), plugin.postCalls.Load())
	require.True(t, plugin.sawError.Load(), "consumer abandonment is reported to post-hooks as cancellation")
	require.Zero(t, leaseCount(t, m, "tasks"))
}

func TestStreamObservesForwardedEventsInOrderAndNotAbandonedEvent(t *testing.T) {
	var order []string
	plugin := &recordingA2APlugin{name: "recorder", order: &order}
	_, _, m := streamingGateway(t, plugin)
	runtime, ok := m.runtimes.load("tasks")
	require.True(t, ok)
	handler := &proxyRequestHandler{runtime: runtime, manager: m, config: runtime.config}
	pipeline, _ := m.pluginPipeline()
	fake := pipeline.(*fakePipeline)

	first := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("one"))
	first.ID, first.TaskID, first.ContextID = "message-1", "task-1", "context-1"
	second := &a2a.TaskStatusUpdateEvent{TaskID: "task-1", ContextID: "context-1", Status: a2a.TaskStatus{State: a2a.TaskStateWorking}}
	stream := forwardUpstreamStream(context.Background(), handler, handler.taskEnvelope(schemas.A2ARequestTypeSubscribeToTask, &a2a.SubscribeToTaskRequest{ID: "task-1"}, "task-1"), func(context.Context, sdkClient) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			if !yield(first, nil) {
				return
			}
			yield(second, nil)
		}
	})
	stream(func(a2a.Event, error) bool { return false })

	fake.mu.Lock()
	observed := append([]*schemas.BifrostA2AEvent(nil), fake.observedEvents...)
	fake.mu.Unlock()
	require.Empty(t, observed, "an event rejected by the downstream iterator must not be minted")
	require.Equal(t, []string{"pre:recorder", "post:recorder"}, order)
	require.Equal(t, int64(1), plugin.postCalls.Load())
}

func TestStreamObservesAcceptedEventsInGatewayOrder(t *testing.T) {
	var order []string
	plugin := &recordingA2APlugin{name: "recorder", order: &order}
	_, _, m := streamingGateway(t, plugin)
	runtime, ok := m.runtimes.load("tasks")
	require.True(t, ok)
	handler := &proxyRequestHandler{runtime: runtime, manager: m, config: runtime.config}
	pipeline, _ := m.pluginPipeline()
	fake := pipeline.(*fakePipeline)

	first := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("one"))
	first.ID, first.TaskID, first.ContextID = "message-1", "task-1", "context-1"
	second := &a2a.TaskStatusUpdateEvent{TaskID: "task-1", ContextID: "context-1", Status: a2a.TaskStatus{State: a2a.TaskStateWorking}}
	stream := forwardUpstreamStream(context.Background(), handler, handler.taskEnvelope(schemas.A2ARequestTypeSubscribeToTask, &a2a.SubscribeToTaskRequest{ID: "task-1"}, "task-1"), func(context.Context, sdkClient) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			require.True(t, yield(first, nil))
			require.True(t, yield(second, nil))
		}
	})
	for range stream {
	}

	fake.mu.Lock()
	observed := append([]*schemas.BifrostA2AEvent(nil), fake.observedEvents...)
	fake.mu.Unlock()
	require.Len(t, observed, 2)
	require.Equal(t, []int64{1, 2}, []int64{observed[0].Sequence, observed[1].Sequence})
	require.Equal(t, schemas.A2AEventTypeMessage, observed[0].EventType)
	require.Equal(t, "message-1", observed[0].MessageID)
	require.Equal(t, "task-1", observed[0].TaskID)
	require.Equal(t, "context-1", observed[0].ContextID)
	require.NotNil(t, observed[0].Body)
	require.Contains(t, *observed[0].Body, "message-1")
	require.Equal(t, schemas.A2AEventTypeStatusUpdate, observed[1].EventType)
	require.Equal(t, string(a2a.TaskStateWorking), observed[1].TaskState)
	require.Equal(t, []string{"pre:recorder", "post:recorder"}, order)
	require.Equal(t, int64(1), plugin.postCalls.Load())
}

// The served gateway card must keep the upstream streaming capability, otherwise
// SDK clients silently downgrade streaming sends to unary ones.
func TestGatewayCardPreservesUpstreamStreamingCapability(t *testing.T) {
	streaming := gatewayCard(&a2a.AgentCard{Capabilities: a2a.AgentCapabilities{Streaming: true, PushNotifications: true, ExtendedAgentCard: true}}, "http://gateway", "agent-1", schemas.AgentGatewayAuthPolicy{}, false)
	require.True(t, streaming.Capabilities.Streaming, "streaming is proxied and must be advertised")
	require.False(t, streaming.Capabilities.PushNotifications, "push notifications are not advertised while the relay is unavailable")
	require.True(t, streaming.Capabilities.ExtendedAgentCard, "extended cards are proxied and must be advertised")

	relayed := gatewayCard(&a2a.AgentCard{Capabilities: a2a.AgentCapabilities{PushNotifications: true}}, "http://gateway", "agent-1", schemas.AgentGatewayAuthPolicy{}, true)
	require.True(t, relayed.Capabilities.PushNotifications, "an operational relay with a push-capable upstream is advertised")
	noUpstreamPush := gatewayCard(&a2a.AgentCard{}, "http://gateway", "agent-1", schemas.AgentGatewayAuthPolicy{}, true)
	require.False(t, noUpstreamPush.Capabilities.PushNotifications, "the two-hop path needs upstream push support")

	nonStreaming := gatewayCard(&a2a.AgentCard{Capabilities: a2a.AgentCapabilities{}}, "http://gateway", "agent-1", schemas.AgentGatewayAuthPolicy{}, false)
	require.False(t, nonStreaming.Capabilities.Streaming, "a non-streaming upstream must not be advertised as streaming")
	require.False(t, nonStreaming.Capabilities.ExtendedAgentCard, "an upstream without an extended card must not be advertised as having one")
}

// A panic unwinding through the stream must still run the deferred teardown, so
// the scope is cancelled and the generation lease is released.
func TestPanicUnwindingStreamReleasesLease(t *testing.T) {
	_, _, m := streamingGateway(t)
	runtime, ok := m.runtimes.load("tasks")
	require.True(t, ok)
	handler := &proxyRequestHandler{runtime: runtime, manager: m, config: runtime.config}

	stream := forwardUpstreamStream(context.Background(), handler, handler.taskEnvelope(schemas.A2ARequestTypeSubscribeToTask, &a2a.SubscribeToTaskRequest{ID: "task-1"}, "task-1"),
		func(context.Context, sdkClient) iter.Seq2[a2a.Event, error] {
			return func(yield func(a2a.Event, error) bool) { yield(message("one"), nil) }
		})

	require.Panics(t, func() {
		for range stream {
			panic("downstream consumer panicked")
		}
	})
	require.Eventually(t, func() bool { return leaseCount(t, m, "tasks") == 0 }, 5*time.Second, time.Millisecond, "a panic cannot leak a generation lease")
}

// An in-flight stream is bounded by its agent's runtime: replacing, deleting, or
// shutting the runtime down must terminate the stream it is serving.
func TestRuntimeLifecycleTerminatesInFlightStreams(t *testing.T) {
	inFlight := func(t *testing.T) (*streamingUpstream, *Manager, *upstreamStream, string) {
		t.Helper()
		upstreamHandler, client, m := streamingGateway(t)
		downstream := startDownstream(t, client, nil)
		upstream := nextUpstreamStream(t, upstreamHandler)
		upstream.events <- message("one")
		require.NotNil(t, waitEvent(t, downstream))
		runtime, ok := m.runtimes.load("tasks")
		require.True(t, ok)
		return upstreamHandler, m, upstream, runtime.config.agentCardURL
	}

	t.Run("update", func(t *testing.T) {
		_, m, upstream, url := inFlight(t)
		enabled := true
		_, err := m.Update(context.Background(), "tasks", UpdateRequest{AgentCardURL: &url, Enabled: &enabled})
		require.NoError(t, err)
		waitClosed(t, "upstream stream", upstream.done)
	})

	t.Run("delete", func(t *testing.T) {
		_, m, upstream, _ := inFlight(t)
		require.NoError(t, m.Delete(context.Background(), "tasks"))
		waitClosed(t, "upstream stream", upstream.done)
	})

	t.Run("shutdown", func(t *testing.T) {
		_, m, upstream, _ := inFlight(t)
		m.Close()
		waitClosed(t, "upstream stream", upstream.done)
		// Close is idempotent; repeating it disturbs nothing.
		m.Close()
		m.Close()
	})
}

package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// recordingA2APlugin is the A2A analogue of the MCP test plugin doubles: it
// appends to a shared ordering log so a test can prove forward pre-hook order and
// reverse post-hook order, and can optionally short-circuit or panic.
type recordingA2APlugin struct {
	name         string
	order        *[]string
	shortCircuit *schemas.A2APluginShortCircuit
	preErr       error
	panicOnPre   bool
	panicOnPost  bool
	nilOnPre     bool
	postCalls    atomic.Int64
	sawResponse  atomic.Bool
	sawError     atomic.Bool
	request      *schemas.BifrostA2ARequest
	response     *schemas.BifrostA2AResponse
}

func (p *recordingA2APlugin) pre(req *schemas.BifrostA2ARequest) (*schemas.BifrostA2ARequest, *schemas.A2APluginShortCircuit, error) {
	*p.order = append(*p.order, "pre:"+p.name)
	p.request = req
	if p.panicOnPre {
		panic("pre-hook exploded")
	}
	if p.nilOnPre {
		return nil, p.shortCircuit, p.preErr
	}
	return req, p.shortCircuit, p.preErr
}

func (p *recordingA2APlugin) post(resp *schemas.BifrostA2AResponse, bErr *schemas.BifrostError) (*schemas.BifrostA2AResponse, *schemas.BifrostError, error) {
	*p.order = append(*p.order, "post:"+p.name)
	p.postCalls.Add(1)
	if p.panicOnPost {
		panic("post-hook exploded")
	}
	if resp != nil {
		p.sawResponse.Store(true)
		p.response = resp
	}
	if bErr != nil {
		p.sawError.Store(true)
	}
	return resp, bErr, nil
}

// fakePipeline implements the narrow PluginPipeline interface with the same
// forward/reverse, non-blocking-error, and executed-count semantics the real core
// pipeline provides, so gate behavior can be tested without the core package.
type fakePipeline struct {
	plugins        []*recordingA2APlugin
	observedEvents []*schemas.BifrostA2AEvent
	mu             sync.Mutex
}

func (f *fakePipeline) RunA2APreHooks(_ *schemas.BifrostContext, req *schemas.BifrostA2ARequest, entered func(int)) (*schemas.BifrostA2ARequest, *schemas.A2APluginShortCircuit, int) {
	executed := 0
	for _, plugin := range f.plugins {
		executed++
		if entered != nil {
			entered(executed)
		}
		next, shortCircuit, err := plugin.pre(req)
		// Progress is recorded before invocation so a panicking plugin still
		// receives its matching post-hook.
		req = next
		if err != nil {
			continue // a returned Go error is non-blocking
		}
		if shortCircuit != nil {
			return req, shortCircuit, executed
		}
	}
	return req, nil, executed
}

func (f *fakePipeline) RunA2APostHooks(_ *schemas.BifrostContext, resp *schemas.BifrostA2AResponse, bErr *schemas.BifrostError, runFrom int) (*schemas.BifrostA2AResponse, *schemas.BifrostError) {
	if runFrom > len(f.plugins) {
		runFrom = len(f.plugins)
	}
	for i := runFrom - 1; i >= 0; i-- {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					panicErr := errors.New("post-hook panic")
					bErr = &schemas.BifrostError{IsBifrostError: true, StatusCode: schemas.Ptr(500), Error: &schemas.ErrorField{Message: panicErr.Error(), Error: panicErr}}
				}
			}()
			resp, bErr, _ = f.plugins[i].post(resp, bErr)
		}()
	}
	return resp, bErr
}

func (f *fakePipeline) ObserveA2AEvent(_ *schemas.BifrostContext, event *schemas.BifrostA2AEvent, runThrough int) {
	if event == nil || runThrough <= 0 {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observedEvents = append(f.observedEvents, event)
}

func newGatedManager(t *testing.T, plugins ...*recordingA2APlugin) *Manager {
	t.Helper()
	m := &Manager{}
	pipeline := &fakePipeline{plugins: plugins}
	m.SetPluginPipeline(func() PluginPipeline { return pipeline }, func(PluginPipeline) {})
	return m
}

func operationEnvelope(agentName string) *schemas.BifrostA2ARequest {
	return &schemas.BifrostA2ARequest{
		RequestType: schemas.A2ARequestTypeSendMessage,
		AgentName:   agentName,
	}
}

func gateCtxForTest() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
}

// The per-operation gate context must install a fresh upstream-latency
// accumulator so the call sites can attribute upstream socket time and populate
// upstream/overhead on the response, mirroring the LLM request path.
func TestGateContextInstallsUpstreamLatencyAccumulator(t *testing.T) {
	gateCtx := gateContext(context.Background())
	_, ok := schemas.GetUpstreamLatency(gateCtx)
	require.True(t, ok, "gateContext must install an upstream-latency accumulator")

	schemas.AddUpstreamLatency(gateCtx, 25*time.Millisecond)
	resp := &schemas.BifrostA2AResponse{}
	resp.PopulateUpstreamLatency(gateCtx)
	resp.PopulateOverheadLatency(gateCtx, 40*time.Millisecond)
	require.NotNil(t, resp.ExtraFields.UpstreamLatency)
	require.EqualValues(t, 25, *resp.ExtraFields.UpstreamLatency)
	require.NotNil(t, resp.ExtraFields.OverheadLatency)
	require.EqualValues(t, 15, *resp.ExtraFields.OverheadLatency)
}

// The gate must run pre-hooks in registration order and post-hooks in reverse,
// and must complete the operation exactly once.
func TestA2AGateRunsForwardPreAndReversePostExactlyOnce(t *testing.T) {
	var order []string
	first := &recordingA2APlugin{name: "first", order: &order}
	second := &recordingA2APlugin{name: "second", order: &order}
	m := newGatedManager(t, first, second)

	var opCalls atomic.Int64
	resp, bErr := m.RunWithPluginPipeline(gateCtxForTest(), operationEnvelope("fixture"), func(*schemas.BifrostA2ARequest, func(*schemas.BifrostA2AEvent)) (*schemas.BifrostA2AResponse, error) {
		opCalls.Add(1)
		return &schemas.BifrostA2AResponse{}, nil
	})

	require.Nil(t, bErr)
	require.NotNil(t, resp)
	require.Equal(t, int64(1), opCalls.Load(), "the operation must run exactly once")
	require.Equal(t, []string{"pre:first", "pre:second", "post:second", "post:first"}, order)
	require.Equal(t, int64(1), first.postCalls.Load(), "each post-hook must run exactly once")
	require.Equal(t, int64(1), second.postCalls.Load())
	require.Equal(t, schemas.A2ARequestTypeSendMessage, resp.ExtraFields.A2ARequestType)
	require.Equal(t, "fixture", resp.ExtraFields.AgentName, "the agent name is the authorization resource key and must always be carried")
}

// A short-circuit must skip the operation but still run the post-hooks of the
// short-circuiting plugin and every plugin before it.
func TestA2AGateShortCircuitSkipsOperationButStillRunsPostHooks(t *testing.T) {
	var order []string
	denial := &schemas.BifrostError{StatusCode: schemas.Ptr(403), Error: &schemas.ErrorField{Message: "denied"}}
	first := &recordingA2APlugin{name: "first", order: &order}
	second := &recordingA2APlugin{name: "second", order: &order, shortCircuit: &schemas.A2APluginShortCircuit{Error: denial}}
	third := &recordingA2APlugin{name: "third", order: &order}
	m := newGatedManager(t, first, second, third)

	var opCalls atomic.Int64
	_, bErr := m.RunWithPluginPipeline(gateCtxForTest(), operationEnvelope("fixture"), func(*schemas.BifrostA2ARequest, func(*schemas.BifrostA2AEvent)) (*schemas.BifrostA2AResponse, error) {
		opCalls.Add(1)
		return &schemas.BifrostA2AResponse{}, nil
	})

	require.NotNil(t, bErr)
	require.Equal(t, 403, *bErr.StatusCode)
	require.Equal(t, int64(0), opCalls.Load(), "a denial must precede any upstream effect")
	require.Equal(t, []string{"pre:first", "pre:second", "post:second", "post:first"}, order)
	require.Equal(t, int64(0), third.postCalls.Load(), "a plugin whose pre-hook never ran must not get a post-hook")
	require.True(t, second.sawError.Load(), "the short-circuiting plugin observes its own denial")
	require.Equal(t, schemas.A2ARequestTypeSendMessage, bErr.ExtraFields.A2ARequestType)
	require.Equal(t, "fixture", bErr.ExtraFields.A2AAgentName)
}

// The response short-circuit branch has the same exactly-once obligation as the
// error branch: the operation is skipped, the entered plugins still unwind in
// reverse exactly once, and the substituted response carries the discriminators.
func TestA2AGateResponseShortCircuitSkipsOperationButStillRunsPostHooks(t *testing.T) {
	var order []string
	substitute := &schemas.BifrostA2AResponse{}
	first := &recordingA2APlugin{name: "first", order: &order}
	second := &recordingA2APlugin{name: "second", order: &order, shortCircuit: &schemas.A2APluginShortCircuit{Response: substitute}}
	third := &recordingA2APlugin{name: "third", order: &order}
	m := newGatedManager(t, first, second, third)

	var opCalls atomic.Int64
	resp, bErr := m.RunWithPluginPipeline(gateCtxForTest(), operationEnvelope("fixture"), func(*schemas.BifrostA2ARequest, func(*schemas.BifrostA2AEvent)) (*schemas.BifrostA2AResponse, error) {
		opCalls.Add(1)
		return &schemas.BifrostA2AResponse{}, nil
	})

	require.Nil(t, bErr)
	require.NotNil(t, resp)
	require.Equal(t, int64(0), opCalls.Load(), "a response short-circuit must replace the operation, not precede it")
	require.Equal(t, []string{"pre:first", "pre:second", "post:second", "post:first"}, order)
	require.Equal(t, int64(1), first.postCalls.Load(), "post-hooks run exactly once on the response short-circuit path")
	require.Equal(t, int64(1), second.postCalls.Load())
	require.Equal(t, int64(0), third.postCalls.Load(), "a plugin whose pre-hook never ran must not get a post-hook")
	require.True(t, second.sawResponse.Load(), "the short-circuiting plugin observes its own substituted response")
	require.Equal(t, schemas.A2ARequestTypeSendMessage, resp.ExtraFields.A2ARequestType)
	require.Equal(t, "fixture", resp.ExtraFields.AgentName)
}

// The gate must convert a panic into an error and still run the post-hooks whose
// pre-hooks ran, so exactly-once completion survives a panic.
func TestA2AGatePanicStillRunsPostHooks(t *testing.T) {
	var order []string
	observer := &recordingA2APlugin{name: "observer", order: &order}
	m := newGatedManager(t, observer)

	resp, bErr := m.RunWithPluginPipeline(gateCtxForTest(), operationEnvelope("fixture"), func(*schemas.BifrostA2ARequest, func(*schemas.BifrostA2AEvent)) (*schemas.BifrostA2AResponse, error) {
		panic("operation exploded")
	})

	require.Nil(t, resp)
	require.NotNil(t, bErr, "a panic must surface as an error, not unwind past the gate")
	require.Equal(t, 500, *bErr.StatusCode)
	require.True(t, strings.Contains(bErr.Error.Message, a2aPanicMessage))
	require.Equal(t, int64(1), observer.postCalls.Load(), "post-hooks still run exactly once after a panic")
	require.True(t, observer.sawError.Load())
	require.Equal(t, []string{"pre:observer", "post:observer"}, order)
}

// A panic inside a plugin's own pre-hook is contained the same way.
func TestA2AGatePanicInPreHookStillCompletes(t *testing.T) {
	var order []string
	exploding := &recordingA2APlugin{name: "exploding", order: &order, panicOnPre: true}
	m := newGatedManager(t, exploding)

	var opCalls atomic.Int64
	_, bErr := m.RunWithPluginPipeline(gateCtxForTest(), operationEnvelope("fixture"), func(*schemas.BifrostA2ARequest, func(*schemas.BifrostA2AEvent)) (*schemas.BifrostA2AResponse, error) {
		opCalls.Add(1)
		return &schemas.BifrostA2AResponse{}, nil
	})

	require.NotNil(t, bErr)
	require.Equal(t, int64(0), opCalls.Load())
	require.Equal(t, int64(1), exploding.postCalls.Load())
	require.Equal(t, []string{"pre:exploding", "post:exploding"}, order)
}

func TestA2AGateNilRequestRunsEnteredPostHooks(t *testing.T) {
	var order []string
	plugin := &recordingA2APlugin{name: "nil", order: &order, nilOnPre: true}
	m := newGatedManager(t, plugin)

	var opCalls atomic.Int64
	_, bErr := m.RunWithPluginPipeline(gateCtxForTest(), operationEnvelope("fixture"), func(*schemas.BifrostA2ARequest, func(*schemas.BifrostA2AEvent)) (*schemas.BifrostA2AResponse, error) {
		opCalls.Add(1)
		return nil, nil
	})

	require.NotNil(t, bErr)
	require.Equal(t, int64(0), opCalls.Load())
	require.Equal(t, int64(1), plugin.postCalls.Load())
	require.Equal(t, []string{"pre:nil", "post:nil"}, order)
}

func TestA2AGatePostPanicContinuesReverseUnwind(t *testing.T) {
	var order []string
	first := &recordingA2APlugin{name: "first", order: &order}
	second := &recordingA2APlugin{name: "second", order: &order, panicOnPost: true}
	m := newGatedManager(t, first, second)

	_, bErr := m.RunWithPluginPipeline(gateCtxForTest(), operationEnvelope("fixture"), func(*schemas.BifrostA2ARequest, func(*schemas.BifrostA2AEvent)) (*schemas.BifrostA2AResponse, error) {
		return &schemas.BifrostA2AResponse{}, nil
	})

	require.NotNil(t, bErr)
	require.Equal(t, []string{"pre:first", "pre:second", "post:second", "post:first"}, order)
	require.Equal(t, int64(1), first.postCalls.Load())
	require.Equal(t, int64(1), second.postCalls.Load())
}

// A Go error returned by the operation is wrapped, preserving the original for
// errors.Is/As, and is handed to the post-hooks.
func TestA2AGateWrapsOperationErrorPreservingOriginal(t *testing.T) {
	var order []string
	observer := &recordingA2APlugin{name: "observer", order: &order}
	m := newGatedManager(t, observer)
	sentinel := errors.New("upstream unreachable")

	_, bErr := m.RunWithPluginPipeline(gateCtxForTest(), operationEnvelope("fixture"), func(*schemas.BifrostA2ARequest, func(*schemas.BifrostA2AEvent)) (*schemas.BifrostA2AResponse, error) {
		return nil, sentinel
	})

	require.NotNil(t, bErr)
	require.ErrorIs(t, bErr.Error.Error, sentinel)
	require.True(t, observer.sawError.Load())
	require.Equal(t, int64(1), observer.postCalls.Load())
}

func TestA2AGateObservesEventsWithoutRerunningLifecycle(t *testing.T) {
	var order []string
	plugin := &recordingA2APlugin{name: "observer", order: &order}
	pipeline := &fakePipeline{plugins: []*recordingA2APlugin{plugin}}
	m := &Manager{}
	m.SetPluginPipeline(func() PluginPipeline { return pipeline }, func(PluginPipeline) {})

	_, bErr := m.RunWithPluginPipeline(gateCtxForTest(), operationEnvelope("fixture"), func(_ *schemas.BifrostA2ARequest, observe func(*schemas.BifrostA2AEvent)) (*schemas.BifrostA2AResponse, error) {
		require.NotNil(t, observe)
		observe(&schemas.BifrostA2AEvent{Sequence: 1, EventType: schemas.A2AEventTypeMessage})
		observe(&schemas.BifrostA2AEvent{Sequence: 2, EventType: schemas.A2AEventTypeStatusUpdate})
		return &schemas.BifrostA2AResponse{}, nil
	})

	require.Nil(t, bErr)
	require.Equal(t, []string{"pre:observer", "post:observer"}, order, "event observation must not re-run pre/post hooks")
	require.Equal(t, int64(1), plugin.postCalls.Load())
	require.Len(t, pipeline.observedEvents, 2)
	require.Equal(t, int64(1), pipeline.observedEvents[0].Sequence)
	require.Equal(t, int64(2), pipeline.observedEvents[1].Sequence)
}

func runSuccessfulGate(t *testing.T, m *Manager) {
	t.Helper()
	resp, bErr := m.RunWithPluginPipeline(gateCtxForTest(), operationEnvelope("fixture"), func(*schemas.BifrostA2ARequest, func(*schemas.BifrostA2AEvent)) (*schemas.BifrostA2AResponse, error) {
		return &schemas.BifrostA2AResponse{}, nil
	})
	require.Nil(t, bErr)
	require.NotNil(t, resp)
}

func TestNewManagerPluginPipelineProvidersRunHooksAndRelease(t *testing.T) {
	var order []string
	plugin := &recordingA2APlugin{name: "constructor", order: &order}
	pipeline := &fakePipeline{plugins: []*recordingA2APlugin{plugin}}
	var acquired, released atomic.Int64
	m, err := NewManager(context.Background(), &memoryStore{regs: map[string]schemas.AgentRegistration{}}, nil, "", nil, ManagerConfig{
		PluginPipelineAcquire: func() PluginPipeline {
			acquired.Add(1)
			return pipeline
		},
		PluginPipelineRelease: func(got PluginPipeline) {
			require.Same(t, pipeline, got)
			released.Add(1)
		},
	})
	require.NoError(t, err)
	defer m.Close()

	runSuccessfulGate(t, m)
	require.Equal(t, []string{"pre:constructor", "post:constructor"}, order)
	require.Equal(t, int64(1), acquired.Load())
	require.Equal(t, int64(1), released.Load(), "the borrowed pipeline must be released exactly once after hooks complete")
}

func TestSetPluginPipelineProvidersRunHooksAndRelease(t *testing.T) {
	m, err := NewManager(context.Background(), &memoryStore{regs: map[string]schemas.AgentRegistration{}}, nil, "", nil)
	require.NoError(t, err)
	defer m.Close()

	var order []string
	plugin := &recordingA2APlugin{name: "late", order: &order}
	pipeline := &fakePipeline{plugins: []*recordingA2APlugin{plugin}}
	var released atomic.Int64
	m.SetPluginPipeline(func() PluginPipeline { return pipeline }, func(got PluginPipeline) {
		require.Same(t, pipeline, got)
		released.Add(1)
	})

	runSuccessfulGate(t, m)
	require.Equal(t, []string{"pre:late", "post:late"}, order)
	require.Equal(t, int64(1), released.Load())
}

// With no pipeline configured there is no plugin to decide anything, so the
// operation simply runs. This documents the fail-open shape the Agent Gateway
// shares with MCP: an unloaded governance plugin performs no authorization.
func TestA2AGateWithoutPipelineRunsOperation(t *testing.T) {
	m := &Manager{}

	var opCalls atomic.Int64
	resp, bErr := m.RunWithPluginPipeline(gateCtxForTest(), operationEnvelope("fixture"), func(*schemas.BifrostA2ARequest, func(*schemas.BifrostA2AEvent)) (*schemas.BifrostA2AResponse, error) {
		opCalls.Add(1)
		return &schemas.BifrostA2AResponse{}, nil
	})
	require.Nil(t, bErr)
	require.NotNil(t, resp)
	require.Equal(t, int64(1), opCalls.Load())

}

// spanRecordingTracer captures the spans the A2A gate opens. Everything not
// overridden is a no-op.
type spanRecordingTracer struct {
	*schemas.NoOpTracer
	spans []*recordedSpan
}

type recordedSpan struct {
	name   string
	kind   schemas.SpanKind
	attrs  map[string]any
	status schemas.SpanStatus
	ended  bool
}

func (r *spanRecordingTracer) StartSpan(ctx context.Context, name string, kind schemas.SpanKind) (context.Context, schemas.SpanHandle) {
	span := &recordedSpan{name: name, kind: kind, attrs: map[string]any{}}
	r.spans = append(r.spans, span)
	return context.WithValue(ctx, schemas.BifrostContextKeySpanID, "span-"+name), span
}

func (r *spanRecordingTracer) StartSpanID(_ context.Context, name string, kind schemas.SpanKind) (string, schemas.SpanHandle) {
	span := &recordedSpan{name: name, kind: kind, attrs: map[string]any{}}
	r.spans = append(r.spans, span)
	return "span-" + name, span
}

func (r *spanRecordingTracer) SetAttribute(handle schemas.SpanHandle, key string, value any) {
	if span, ok := handle.(*recordedSpan); ok {
		span.attrs[key] = value
	}
}

func (r *spanRecordingTracer) EndSpan(handle schemas.SpanHandle, status schemas.SpanStatus, _ string) {
	if span, ok := handle.(*recordedSpan); ok {
		span.status = status
		span.ended = true
	}
}

func tracedGateCtx(tracer schemas.Tracer) *schemas.BifrostContext {
	ctx := gateCtxForTest()
	ctx.SetValue(schemas.BifrostContextKeyTracer, tracer)
	return ctx
}

// The gate must open one a2a.operation span per executed op, stamp the A2A and
// GenAI invoke_agent attributes, and end it with the operation's outcome.
func TestA2AGateOpSpanAttributesAndOutcome(t *testing.T) {
	m := newGatedManager(t, &recordingA2APlugin{name: "solo", order: &[]string{}})
	tracer := &spanRecordingTracer{NoOpTracer: &schemas.NoOpTracer{}}
	ctx := tracedGateCtx(tracer)

	envelope := operationEnvelope("fixture")
	envelope.BifrostA2ASendMessageRequest = &schemas.BifrostA2ASendMessageRequest{ContextID: "ctx-1", TaskID: "task-1"}
	resp, bErr := m.RunWithPluginPipeline(ctx, envelope, func(*schemas.BifrostA2ARequest, func(*schemas.BifrostA2AEvent)) (*schemas.BifrostA2AResponse, error) {
		return &schemas.BifrostA2AResponse{ExtraFields: schemas.BifrostA2AResponseExtraFields{Latency: 42}}, nil
	})
	require.Nil(t, bErr)
	require.NotNil(t, resp)

	require.Len(t, tracer.spans, 1)
	span := tracer.spans[0]
	require.True(t, span.ended)
	require.Equal(t, schemas.SpanKindA2AOperation, span.kind)
	require.Equal(t, "a2a.send_message.fixture", span.name)
	require.Equal(t, schemas.SpanStatusOk, span.status)
	require.Equal(t, "SendMessage", span.attrs[schemas.AttrBifrostA2AOperationName])
	require.Equal(t, "SendMessage", span.attrs[schemas.AttrA2AMethodName])
	require.Equal(t, "fixture", span.attrs[schemas.AttrAgentName])
	require.Equal(t, schemas.OTelOperationNameInvokeAgent, span.attrs[schemas.AttrOperationName])
	require.Equal(t, "ctx-1", span.attrs[schemas.AttrConversationID])
	require.Equal(t, "task-1", span.attrs[schemas.AttrA2ATaskID])
	require.Equal(t, int64(42), span.attrs[schemas.AttrBifrostA2AOperationDurationMs])
	require.NotContains(t, span.attrs, schemas.AttrErrorTypeSpec)
}

func TestA2AGateInternalOperationOmitsJSONRPCMethod(t *testing.T) {
	m := newGatedManager(t, &recordingA2APlugin{name: "solo", order: &[]string{}})
	tracer := &spanRecordingTracer{NoOpTracer: &schemas.NoOpTracer{}}
	envelope := operationEnvelope("fixture")
	envelope.RequestType = schemas.A2ARequestTypePushDelivery

	resp, bErr := m.RunWithPluginPipeline(tracedGateCtx(tracer), envelope, func(*schemas.BifrostA2ARequest, func(*schemas.BifrostA2AEvent)) (*schemas.BifrostA2AResponse, error) {
		return &schemas.BifrostA2AResponse{}, nil
	})
	require.Nil(t, bErr)
	require.NotNil(t, resp)
	require.Len(t, tracer.spans, 1)
	require.Equal(t, "push_delivery", tracer.spans[0].attrs[schemas.AttrBifrostA2AOperationName])
	require.NotContains(t, tracer.spans[0].attrs, schemas.AttrA2AMethodName)
}

// A failed op must end the span as an error with a bounded error.type, and a
// pre-hook short-circuit must open no op span at all (mirroring the MCP gate).
func TestA2AGateOpSpanErrorAndShortCircuit(t *testing.T) {
	m := newGatedManager(t, &recordingA2APlugin{name: "solo", order: &[]string{}})
	tracer := &spanRecordingTracer{NoOpTracer: &schemas.NoOpTracer{}}

	_, bErr := m.RunWithPluginPipeline(tracedGateCtx(tracer), operationEnvelope("fixture"), func(*schemas.BifrostA2ARequest, func(*schemas.BifrostA2AEvent)) (*schemas.BifrostA2AResponse, error) {
		return nil, context.DeadlineExceeded
	})
	require.NotNil(t, bErr)
	require.Len(t, tracer.spans, 1)
	require.Equal(t, schemas.SpanStatusError, tracer.spans[0].status)
	require.Equal(t, "timeout", tracer.spans[0].attrs[schemas.AttrErrorTypeSpec])

	denying := &recordingA2APlugin{name: "deny", order: &[]string{}, shortCircuit: &schemas.A2APluginShortCircuit{
		Error: &schemas.BifrostError{StatusCode: schemas.Ptr(403), Error: &schemas.ErrorField{Message: "denied"}},
	}}
	m = newGatedManager(t, denying)
	blocked := &spanRecordingTracer{NoOpTracer: &schemas.NoOpTracer{}}
	_, bErr = m.RunWithPluginPipeline(tracedGateCtx(blocked), operationEnvelope("fixture"), func(*schemas.BifrostA2ARequest, func(*schemas.BifrostA2AEvent)) (*schemas.BifrostA2AResponse, error) {
		t.Fatal("op must not run on short-circuit")
		return nil, nil
	})
	require.NotNil(t, bErr)
	require.Empty(t, blocked.spans, "a short-circuited operation must open no op span")
}

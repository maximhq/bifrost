package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
)

// PluginPipeline is the narrow view of the core plugin pipeline the Agent
// Gateway needs, declared here so this package never imports the core bifrost
// package (which imports it). It is the A2A analogue of mcp.PluginPipeline.
type PluginPipeline interface {
	RunA2APreHooks(ctx *schemas.BifrostContext, req *schemas.BifrostA2ARequest, entered func(int)) (*schemas.BifrostA2ARequest, *schemas.A2APluginShortCircuit, int)
	RunA2APostHooks(ctx *schemas.BifrostContext, resp *schemas.BifrostA2AResponse, bifrostErr *schemas.BifrostError, runFrom int) (*schemas.BifrostA2AResponse, *schemas.BifrostError)
	ObserveA2AEvent(ctx *schemas.BifrostContext, event *schemas.BifrostA2AEvent, runThrough int)
}

// A2AOpFunc is the closure each call site hands to RunWithPluginPipeline. It
// receives the (possibly mutated) request that flowed through the pre-hooks and
// performs the actual work, returning a typed response. The plain Go error it
// returns is wrapped into a BifrostError by the gate before the post-hooks see
// it, exactly as the MCP gate does.
type A2AOpFunc func(preReq *schemas.BifrostA2ARequest, observe func(*schemas.BifrostA2AEvent)) (*schemas.BifrostA2AResponse, error)

// SetPluginPipeline wires the gateway to providers that borrow the current
// plugin pipeline generation for each operation. With no provider, operations
// pass through without hooks.
func (m *Manager) SetPluginPipeline(acquire func() PluginPipeline, release func(PluginPipeline)) {
	m.pipelineMu.Lock()
	defer m.pipelineMu.Unlock()
	m.acquirePipeline = acquire
	m.releasePipeline = release
}

// pluginPipeline borrows a pipeline for one operation, returning nil when no
// provider is configured. The paired release closure is returned so the caller
// hands the pipeline back exactly once.
func (m *Manager) pluginPipeline() (PluginPipeline, func(PluginPipeline)) {
	m.pipelineMu.RLock()
	acquire, release := m.acquirePipeline, m.releasePipeline
	m.pipelineMu.RUnlock()
	if acquire == nil {
		return nil, nil
	}
	pipeline := acquire()
	if pipeline == nil {
		return nil, nil
	}
	return pipeline, release
}

// a2aPanicMessage is the message reported when the gate converts a panic in the
// wrapped operation into an error, so operators can recognize it in logs.
const a2aPanicMessage = "panic in agent gateway operation"

// RunWithPluginPipeline wraps one Agent Gateway operation in the A2A plugin gate
// and is the single source of truth for that pattern, exactly as
// MCPManager.RunWithPluginPipeline is for MCP:
//
//  1. Acquire a pipeline (no-op pass-through when none is configured)
//  2. Run PreA2AHooks — plugins may mutate the request or short-circuit it
//  3. On short-circuit: still run PostA2AHooks with the recorded pre-hook count,
//     so the short-circuiting plugin and every plugin before it observe the outcome
//  4. Otherwise: run op with the mutated request, then run PostA2AHooks on the
//     outcome (response or error)
//
// Unlike the MCP and LLM gates, this one also installs a recover: the Agent
// Gateway contract requires exactly-once completion, so a panic in op (or in a
// plugin hook) is converted into a BifrostError and the post-hooks still run with
// the recorded count rather than unwinding past them. The panic is not re-raised;
// the caller receives it as an error.
func (m *Manager) RunWithPluginPipeline(
	ctx *schemas.BifrostContext,
	req *schemas.BifrostA2ARequest,
	op A2AOpFunc,
) (finalResponse *schemas.BifrostA2AResponse, finalError *schemas.BifrostError) {
	// Ensure a request ID exists so plugin hooks have something to correlate on.
	// Card and background operations often arrive without one.
	if ctx != nil {
		if _, ok := ctx.Value(schemas.BifrostContextKeyRequestID).(string); !ok {
			ctx.SetValue(schemas.BifrostContextKeyRequestID, uuid.New().String())
		}
	}

	// Stamped on every wrapped error so a post-hook can discriminate the operation
	// and its authorization resource from the failure path too.
	reqType := schemas.A2ARequestType("")
	agentName := ""
	if req != nil {
		reqType = req.RequestType
		agentName = req.AgentName
	}

	// Span layout: PreHook spans chain under the span active at gate entry (the
	// transport's a2a.dispatch span when present, else the root), the op span is
	// a sibling of the PreHooks under that same entry span, and PostHook spans
	// nest under the op span. A PreHook short-circuit produces no op span.
	//
	// The op span deliberately does NOT nest under the last PreHook (unlike the
	// LLM layout): the PreHook spans have already ended when the op starts, so
	// their windows never overlap it, and the overhead breakdown's
	// parent-self-time subtraction would then leave the op's wall time — mostly
	// upstream wait — inside the dispatch span's bucket.
	var tracer schemas.Tracer
	var entrySpanID any
	if ctx != nil {
		tracer, _ = ctx.Value(schemas.BifrostContextKeyTracer).(schemas.Tracer)
		entrySpanID = ctx.Value(schemas.BifrostContextKeySpanID)
	}

	// Set by startOpSpan only when the op runs (nil for short-circuits → the
	// deferred end is a no-op). startOpSpan threads the new span id onto ctx (no
	// restore) so PostHook spans nest under it.
	var opSpanHandle schemas.SpanHandle
	startOpSpan := func(r *schemas.BifrostA2ARequest) {
		if tracer == nil || r == nil {
			return
		}
		spanName := fmt.Sprintf("a2a.%s", r.RequestType)
		if r.AgentName != "" {
			spanName = fmt.Sprintf("%s.%s", spanName, r.AgentName)
		}
		// Restore the gate-entry parent so the op span is not a child of the
		// last (already-ended) PreHook span; see the layout comment above.
		ctx.SetValue(schemas.BifrostContextKeySpanID, entrySpanID)
		spanCtx, handle := tracer.StartSpan(ctx, spanName, schemas.SpanKindA2AOperation)
		opSpanHandle = handle
		if spanCtx != nil {
			if id, ok := spanCtx.Value(schemas.BifrostContextKeySpanID).(string); ok && id != "" {
				ctx.SetValue(schemas.BifrostContextKeySpanID, id)
			}
		}
		tracer.SetAttribute(handle, schemas.AttrBifrostA2AOperationName, r.RequestType.OperationName())
		if method, ok := r.RequestType.JSONRPCMethodName(); ok {
			tracer.SetAttribute(handle, schemas.AttrA2AMethodName, method)
		}
		if r.AgentName != "" {
			tracer.SetAttribute(handle, schemas.AttrAgentName, r.AgentName)
		}
		// Message operations are agent invocations per the OTel GenAI agent
		// conventions; other A2A methods are protocol plumbing without a semconv
		// operation name. TaskID is accessed through the embedded structs because
		// both promote the field.
		switch r.RequestType {
		case schemas.A2ARequestTypeSendMessage, schemas.A2ARequestTypeSendStreamingMessage:
			tracer.SetAttribute(handle, schemas.AttrOperationName, schemas.OTelOperationNameInvokeAgent)
			if r.BifrostA2ASendMessageRequest != nil {
				if r.ContextID != "" {
					tracer.SetAttribute(handle, schemas.AttrConversationID, r.ContextID)
				}
				if r.BifrostA2ASendMessageRequest.TaskID != "" {
					tracer.SetAttribute(handle, schemas.AttrA2ATaskID, r.BifrostA2ASendMessageRequest.TaskID)
				}
			}
		default:
			if r.BifrostA2ATaskRequest != nil && r.BifrostA2ATaskRequest.TaskID != "" {
				tracer.SetAttribute(handle, schemas.AttrA2ATaskID, r.BifrostA2ATaskRequest.TaskID)
			}
		}
	}
	// Runs after the recover defers below (LIFO), so finalResponse/finalError
	// already reflect any recovered panic when the span is ended.
	defer func() {
		if tracer == nil || opSpanHandle == nil {
			return
		}
		if finalResponse != nil && finalResponse.ExtraFields.Latency > 0 {
			tracer.SetAttribute(opSpanHandle, schemas.AttrBifrostA2AOperationDurationMs, finalResponse.ExtraFields.Latency)
		}
		// The op selects the upstream transport after the span starts, so it is
		// read back from context at end time.
		if transport, ok := ctx.Value(schemas.BifrostContextKeyA2AUpstreamTransport).(string); ok && transport != "" {
			tracer.SetAttribute(opSpanHandle, schemas.AttrBifrostA2AUpstreamTransport, transport)
		}
		setA2AGovernanceSpanAttrs(tracer, opSpanHandle, ctx)
		if finalError != nil {
			msg := ""
			if finalError.Error != nil {
				msg = finalError.Error.Message
			}
			tracer.SetAttribute(opSpanHandle, schemas.AttrErrorTypeSpec, a2aErrorType(finalError))
			tracer.EndSpan(opSpanHandle, schemas.SpanStatusError, msg)
		} else {
			tracer.EndSpan(opSpanHandle, schemas.SpanStatusOk, "")
		}
	}()

	pipeline, release := m.pluginPipeline()
	if pipeline == nil {
		// No pipeline configured → run the op directly, no hooks, but keep the
		// panic guarantee so callers see one uniform completion contract.
		defer func() {
			if r := recover(); r != nil {
				finalResponse, finalError = nil, a2aPanicError(r, reqType, agentName)
			}
		}()
		startOpSpan(req)
		resp, opErr := op(req, nil)
		if opErr != nil {
			return resp, wrapA2AOpError(opErr, reqType, agentName)
		}
		return resp, nil
	}
	if release != nil {
		defer release(pipeline)
	}

	// preCount is captured before op runs so the recover path can still run the
	// exact set of post-hooks whose pre-hooks ran. postHooksRun guards against
	// running them twice when the panic happens after the normal post-hook call.
	preCount := 0
	postHooksRun := false
	runPost := func(resp *schemas.BifrostA2AResponse, bErr *schemas.BifrostError) (*schemas.BifrostA2AResponse, *schemas.BifrostError) {
		if postHooksRun {
			return resp, bErr
		}
		postHooksRun = true
		return pipeline.RunA2APostHooks(ctx, resp, bErr, preCount)
	}

	defer func() {
		r := recover()
		if r == nil {
			return
		}
		bErr := a2aPanicError(r, reqType, agentName)
		if m.logger != nil {
			m.logger.Error("recovered from %s (agent=%s, type=%s): %v", a2aPanicMessage, agentName, reqType, r)
		}
		// Exactly-once completion: the post-hooks still run for the plugins whose
		// pre-hooks ran, then the panic surfaces to the caller as an error.
		resp, postErr := runPost(nil, bErr)
		if postErr != nil {
			finalResponse, finalError = resp, postErr
			return
		}
		finalResponse, finalError = resp, bErr
	}()

	preReq, shortCircuit, count := pipeline.RunA2APreHooks(ctx, req, func(entered int) {
		preCount = entered
	})
	preCount = count
	if preReq != nil {
		reqType = preReq.RequestType
		agentName = preReq.AgentName
	}

	if shortCircuit != nil {
		// The op never runs, but the post-hooks still do — they may recover.
		if shortCircuit.Response != nil {
			shortCircuit.Response.PopulateExtraFields(reqType, agentName)
			resp, bErr := runPost(shortCircuit.Response, nil)
			if bErr != nil {
				return nil, bErr
			}
			return resp, nil
		}
		if shortCircuit.Error != nil {
			stampA2AErrorFields(shortCircuit.Error, reqType, agentName)
			resp, bErr := runPost(nil, shortCircuit.Error)
			if bErr != nil {
				return nil, bErr
			}
			if resp != nil {
				resp.PopulateExtraFields(reqType, agentName)
				return resp, nil
			}
			return nil, shortCircuit.Error
		}
	}

	if preReq == nil {
		bErr := &schemas.BifrostError{
			IsBifrostError: true,
			StatusCode:     schemas.Ptr(500),
			Error:          &schemas.ErrorField{Message: "A2A request after plugin hooks cannot be nil"},
			ExtraFields:    schemas.BifrostErrorExtraFields{A2ARequestType: reqType, A2AAgentName: agentName},
		}
		return runPost(nil, bErr)
	}

	observe := func(event *schemas.BifrostA2AEvent) {
		if event != nil {
			pipeline.ObserveA2AEvent(ctx, event, preCount)
		}
	}
	startOpSpan(preReq)
	resp, opErr := op(preReq, observe)
	if resp != nil {
		resp.PopulateExtraFields(reqType, agentName)
	}
	var bErr *schemas.BifrostError
	if opErr != nil {
		bErr = wrapA2AOpError(opErr, reqType, agentName)
	}
	return runPost(resp, bErr)
}

// a2aErrorType classifies a failed A2A operation for the error.type span
// attribute and metric dimension, keeping cardinality bounded.
func a2aErrorType(bErr *schemas.BifrostError) string {
	if bErr == nil || bErr.Error == nil || bErr.Error.Error == nil {
		return "_OTHER"
	}
	switch {
	case errors.Is(bErr.Error.Error, context.Canceled):
		return "canceled"
	case errors.Is(bErr.Error.Error, context.DeadlineExceeded):
		return "timeout"
	}
	return "_OTHER"
}

// setA2AGovernanceSpanAttrs copies the metric-safe bifrost.* governance identity
// from context onto an A2A op span for per-tenant metric breakdowns, mirroring
// the MCP gate. Absent dims are skipped.
func setA2AGovernanceSpanAttrs(tracer schemas.Tracer, handle schemas.SpanHandle, ctx *schemas.BifrostContext) {
	if tracer == nil || ctx == nil {
		return
	}
	setIfPresent := func(ctxKey schemas.BifrostContextKey, attrKey string) {
		if v, ok := ctx.Value(ctxKey).(string); ok && v != "" {
			tracer.SetAttribute(handle, attrKey, v)
		}
	}
	setIfPresent(schemas.BifrostContextKeyGovernanceVirtualKeyID, schemas.AttrBifrostVirtualKeyID)
	setIfPresent(schemas.BifrostContextKeyGovernanceVirtualKeyName, schemas.AttrBifrostVirtualKeyName)
	setIfPresent(schemas.BifrostContextKeyGovernanceTeamID, schemas.AttrBifrostTeamID)
	setIfPresent(schemas.BifrostContextKeyGovernanceTeamName, schemas.AttrBifrostTeamName)
	setIfPresent(schemas.BifrostContextKeyGovernanceCustomerID, schemas.AttrBifrostCustomerID)
	setIfPresent(schemas.BifrostContextKeyGovernanceCustomerName, schemas.AttrBifrostCustomerName)
	setIfPresent(schemas.BifrostContextKeyGovernanceBusinessUnitID, schemas.AttrBifrostBusinessUnitID)
	setIfPresent(schemas.BifrostContextKeyGovernanceBusinessUnitName, schemas.AttrBifrostBusinessUnitName)
	setIfPresent(schemas.BifrostContextKeyGovernanceProjectID, schemas.AttrBifrostProjectID)
	setIfPresent(schemas.BifrostContextKeyGovernanceProjectName, schemas.AttrBifrostProjectName)
}

// wrapA2AOpError turns the operation's plain Go error into the typed error the
// post-hooks expect, keeping the original on ErrorField.Error (json:"-") so
// callers can still classify it with errors.Is/As.
func wrapA2AOpError(opErr error, reqType schemas.A2ARequestType, agentName string) *schemas.BifrostError {
	return &schemas.BifrostError{
		IsBifrostError: false,
		Error:          &schemas.ErrorField{Message: opErr.Error(), Error: opErr},
		ExtraFields:    schemas.BifrostErrorExtraFields{A2ARequestType: reqType, A2AAgentName: agentName},
	}
}

// a2aPanicError converts a recovered panic value into a BifrostError so a panic
// completes the operation like any other failure instead of unwinding past the
// post-hooks.
func a2aPanicError(recovered any, reqType schemas.A2ARequestType, agentName string) *schemas.BifrostError {
	err := fmt.Errorf("%s: %v", a2aPanicMessage, recovered)
	return &schemas.BifrostError{
		IsBifrostError: true,
		StatusCode:     schemas.Ptr(500),
		Error:          &schemas.ErrorField{Message: err.Error(), Error: err},
		ExtraFields:    schemas.BifrostErrorExtraFields{A2ARequestType: reqType, A2AAgentName: agentName},
	}
}

// stampA2AErrorFields backfills the discriminators on a plugin-supplied error so
// a post-hook can always tell which operation and agent failed, even when the
// plugin built the error itself.
func stampA2AErrorFields(bErr *schemas.BifrostError, reqType schemas.A2ARequestType, agentName string) {
	if bErr == nil {
		return
	}
	if bErr.ExtraFields.A2ARequestType == "" {
		bErr.ExtraFields.A2ARequestType = reqType
	}
	if bErr.ExtraFields.A2AAgentName == "" {
		bErr.ExtraFields.A2AAgentName = agentName
	}
}

// gateContext derives the BifrostContext the gate runs on from an inbound
// request context. Values written by a pre-hook must be visible to the operation
// that follows it, and BifrostContext.Value only walks parent-ward, so the gate
// always runs on a fresh child rather than on the inbound context itself. This
// mirrors mcp.runListToolsWithHooks.
func gateContext(ctx context.Context) *schemas.BifrostContext {
	if ctx == nil {
		ctx = context.Background()
	}
	gateCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
	// Install a fresh upstream-latency accumulator per operation so the call
	// sites can attribute upstream socket time and derive Bifrost overhead,
	// exactly as the LLM request path does at request entry.
	gateCtx.ResetUpstreamLatency()
	return gateCtx
}

// phaseSpan is the token returned by startPhaseSpan and consumed by endPhaseSpan.
// It carries the span handle plus the span ID that was active when the phase
// opened, so endPhaseSpan can restore the prior parent. The zero value is a valid
// no-op (no tracer on ctx), mirroring core's coreSpan.
type phaseSpan struct {
	tracer schemas.Tracer
	h      schemas.SpanHandle
	ctx    *schemas.BifrostContext
	prev   any
}

// startPhaseSpan opens an internal overhead phase span for a gateway segment
// (upstream-client prep, response encode). The span NAME is the overhead
// breakdown bucket. It installs the new span as the active parent on ctx so any
// spans opened during the phase nest as its children; endPhaseSpan restores the
// prior parent. Returns a zero phaseSpan (no-op) when no tracer is present.
func startPhaseSpan(ctx *schemas.BifrostContext, name string) phaseSpan {
	if ctx == nil {
		return phaseSpan{}
	}
	tracer, _ := ctx.Value(schemas.BifrostContextKeyTracer).(schemas.Tracer)
	if tracer == nil {
		return phaseSpan{}
	}
	prev := ctx.Value(schemas.BifrostContextKeySpanID)
	id, h := tracer.StartSpanID(ctx, name, schemas.SpanKindInternal)
	if h == nil {
		return phaseSpan{}
	}
	ctx.SetValue(schemas.BifrostContextKeySpanID, id)
	return phaseSpan{tracer: tracer, h: h, ctx: ctx, prev: prev}
}

// endPhaseSpan restores the parent that was active before the phase and closes
// the span. Zero-value safe, so callers can call it unconditionally.
func endPhaseSpan(ps phaseSpan) {
	if ps.h == nil {
		return
	}
	ps.ctx.SetValue(schemas.BifrostContextKeySpanID, ps.prev)
	ps.tracer.EndSpan(ps.h, schemas.SpanStatusOk, "")
}

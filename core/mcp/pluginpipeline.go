package mcp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/client"
	"github.com/maximhq/bifrost/core/schemas"
)

// Sentinels wrapped (%w) into wire-op errors so mcpErrorType classifies error.type via
// errors.Is, not message text. toolmanager.go wraps them at the CallTool site.
var (
	ErrMCPToolTimeout    = errors.New("mcp tool call timed out")
	ErrMCPToolCallFailed = errors.New("mcp tool call failed")
)

// MCPOpFunc is the closure each call site provides to RunWithPluginPipeline. It receives the
// (possibly mutated) request that flowed through PreHooks and is responsible for
// performing the wire call (including any internal retries) and building a
// BifrostMCPResponse from the outcome. The plain Go error returned here is wrapped
// into a BifrostError by the gate before being handed to PostMCPHooks.
type MCPOpFunc func(preReq *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error)

// RunWithPluginPipeline wraps an MCP wire operation (connect / ping / list_tools / execute_tool)
// with the plugin pipeline. It is the single source of truth for the MCP plugin gate
// pattern — handleMCPToolExecution in core/bifrost.go calls into this same function,
// and the Starlark codemode sandbox calls into it via the ClientManager interface for
// nested tool calls.
//
//  1. Acquire pipeline (no-op pass-through if none configured)
//  2. Run PreMCPHooks — plugins may mutate the request or short-circuit
//  3. On short-circuit: invoke PostMCPHooks with the short-circuit outcome,
//     drain plugin logs, return
//  4. Otherwise: invoke op with the mutated request, then run PostMCPHooks on
//     the outcome (response or error), drain plugin logs, return
//
// The op closure is responsible for reading mutated values from preReq's per-op
// sub-request struct (Headers, ConnectionString, ChatAssistantMessageToolCall, etc.)
// and using them for the actual wire call.
//
// Returns *BifrostError so callers can preserve rich error fields (AllowFallbacks,
// MCPAuthRequired).
func (m *MCPManager) RunWithPluginPipeline(
	ctx *schemas.BifrostContext,
	req *schemas.BifrostMCPRequest,
	op MCPOpFunc,
) (finalResponse *schemas.BifrostMCPResponse, finalError *schemas.BifrostError) {
	// Ensure a request ID exists so plugin hooks have something to correlate on.
	// Connect/ping/list_tools fire from background contexts that typically lack one.
	if ctx != nil {
		if _, ok := ctx.Value(schemas.BifrostContextKeyRequestID).(string); !ok {
			ctx.SetValue(schemas.BifrostContextKeyRequestID, uuid.New().String())
		}
	}

	// Span layout mirrors the LLM path: /mcp HTTP span is the parent, PreHook spans chain
	// under it, the op span nests under the last PreHook, and PostHook spans nest under the
	// op span. A PreHook short-circuit produces no op span (like a short-circuited llm.call).
	tracer, _ := ctx.Value(schemas.BifrostContextKeyTracer).(schemas.Tracer)

	// Stamped on every wrapped BifrostError so downstream gates discriminate execute-tool
	// from ping/list_tools.
	mcpReqType := schemas.MCPRequestType("")
	if req != nil {
		mcpReqType = req.RequestType
	}

	// Set by startOpSpan only when the wire op runs (nil for short-circuits → deferred end
	// is a no-op). startOpSpan threads the new span id onto ctx (no restore) so the op and
	// PostHook spans nest under it.
	var opSpanHandle schemas.SpanHandle
	startOpSpan := func(r *schemas.BifrostMCPRequest) {
		if tracer == nil {
			return
		}
		spanName := fmt.Sprintf("mcp.%s", mcpReqType)
		if r != nil && r.ClientName != "" {
			spanName = fmt.Sprintf("%s.%s", spanName, r.ClientName)
		}
		spanKind := schemas.SpanKindMCPClient
		if mcpReqType.IsExecuteTool() {
			spanKind = schemas.SpanKindMCPTool
		}
		spanCtx, handle := tracer.StartSpan(ctx, spanName, spanKind)
		opSpanHandle = handle
		if spanCtx != nil {
			if id, ok := spanCtx.Value(schemas.BifrostContextKeySpanID).(string); ok && id != "" {
				ctx.SetValue(schemas.BifrostContextKeySpanID, id)
			}
		}
		if r == nil {
			return
		}
		tracer.SetAttribute(handle, schemas.AttrMCPMethodName, r.RequestType.OTelMethodName())
		if transport := m.connectionTypeForClientName(r.ClientName).OTelNetworkTransport(); transport != "" {
			tracer.SetAttribute(handle, schemas.AttrNetworkTransport, transport)
		}
		// Emit OTel GenAI tool-execution attributes on execute-tool spans so downstream
		// backends can correlate tool calls with their requesting llm.call.
		if r.RequestType.IsExecuteTool() {
			tracer.SetAttribute(handle, schemas.AttrOperationName, schemas.OTelOperationNameExecuteTool)
			tracer.SetAttribute(handle, schemas.AttrToolType, "function")
			if name := r.GetToolName(); name != "" {
				tracer.SetAttribute(handle, schemas.AttrToolName, name)
			}
			// GetToolArguments returns interface{}; the Responses branch boxes a *string, so
			// a nil pointer survives the != nil guard. Deref non-nil so the attr is the JSON string.
			if args := r.GetToolArguments(); args != nil {
				if p, ok := args.(*string); ok {
					if p != nil {
						tracer.SetAttribute(handle, schemas.AttrToolCallArguments, *p)
					}
				} else {
					tracer.SetAttribute(handle, schemas.AttrToolCallArguments, args)
				}
			}
			if r.ChatAssistantMessageToolCall != nil && r.ChatAssistantMessageToolCall.ID != nil {
				tracer.SetAttribute(handle, schemas.AttrToolCallID, *r.ChatAssistantMessageToolCall.ID)
			} else if r.ResponsesToolMessage != nil && r.ResponsesToolMessage.CallID != nil {
				tracer.SetAttribute(handle, schemas.AttrToolCallID, *r.ResponsesToolMessage.CallID)
			}
		}
	}
	defer func() {
		if tracer == nil || opSpanHandle == nil {
			return
		}
		if finalResponse != nil {
			// Tool-execution (CallTool) latency, so the metric measures it, not span wall-time.
			if finalResponse.ExtraFields.Latency > 0 {
				tracer.SetAttribute(opSpanHandle, schemas.AttrBifrostMCPToolDurationMs, finalResponse.ExtraFields.Latency)
			}
			if req != nil && req.RequestType.IsExecuteTool() {
				if data, err := schemas.MarshalString(finalResponse); err == nil {
					tracer.SetAttribute(opSpanHandle, schemas.AttrToolCallResult, data)
				}
			}
		}
		setMCPGovernanceSpanAttrs(tracer, opSpanHandle, ctx)
		if finalError != nil {
			msg := ""
			if finalError.Error != nil {
				msg = finalError.Error.Message
			}
			tracer.SetAttribute(opSpanHandle, schemas.AttrErrorTypeSpec, mcpErrorType(finalError))
			tracer.EndSpan(opSpanHandle, schemas.SpanStatusError, msg)
		} else {
			tracer.EndSpan(opSpanHandle, schemas.SpanStatusOk, "")
		}
	}()

	// No pipeline configured → run the op directly under its own span, no hooks.
	pipeline := m.GetPluginPipeline()
	if pipeline == nil {
		startOpSpan(req)
		resp, opErr := op(req)
		if opErr != nil {
			return resp, &schemas.BifrostError{
				IsBifrostError: false,
				Error:          &schemas.ErrorField{Message: opErr.Error(), Error: opErr},
				ExtraFields:    schemas.BifrostErrorExtraFields{MCPRequestType: mcpReqType},
			}
		}
		return resp, nil
	}
	defer m.ReleasePluginPipeline(pipeline)

	// PreHooks. preReq is the (possibly mutated) request for the op. Their spans chain under
	// the /mcp HTTP span — no op span exists yet.
	preReq, shortCircuit, preCount := pipeline.RunMCPPreHooks(ctx, req)

	// Pull attribution from the request so short-circuit responses still carry
	// ClientName/ToolName when PostHooks or observers consume them.
	clientName := ""
	toolName := ""
	if preReq != nil {
		clientName = preReq.ClientName
		toolName = preReq.GetToolName()
	} else if req != nil {
		clientName = req.ClientName
		toolName = req.GetToolName()
	}

	if shortCircuit != nil {
		// Short-circuit: the wire op never runs, so no op span is created. PostHooks still run.
		if shortCircuit.Response != nil {
			shortCircuit.Response.PopulateExtraFields(mcpReqType, clientName, toolName)
			finalResp, finalErr := pipeline.RunMCPPostHooks(ctx, shortCircuit.Response, nil, preCount)
			drainMCPPluginLogs(ctx)
			if finalErr != nil {
				return nil, finalErr
			}
			return finalResp, nil
		}
		// Short-circuit with error — still run PostHooks (they may recover).
		if shortCircuit.Error != nil {
			if shortCircuit.Error.ExtraFields.MCPRequestType == "" {
				shortCircuit.Error.ExtraFields.MCPRequestType = mcpReqType
			}
			finalResp, finalErr := pipeline.RunMCPPostHooks(ctx, nil, shortCircuit.Error, preCount)
			drainMCPPluginLogs(ctx)
			if finalErr != nil {
				return nil, finalErr
			}
			if finalResp != nil {
				finalResp.PopulateExtraFields(mcpReqType, clientName, toolName)
				return finalResp, nil
			}
			return nil, shortCircuit.Error
		}
	}

	if preReq == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error:          &schemas.ErrorField{Message: "MCP request after plugin hooks cannot be nil"},
			ExtraFields:    schemas.BifrostErrorExtraFields{MCPRequestType: mcpReqType},
		}
	}

	// Op span created after the PreHooks so it nests under the last PreHook; PostHooks nest
	// under it.
	startOpSpan(preReq)

	// Run the actual wire op with the mutated request.
	resp, opErr := op(preReq)
	if resp != nil {
		resp.PopulateExtraFields(mcpReqType, clientName, toolName)
	}

	// Wrap opErr so PostHooks see a typed error. Keep the original on ErrorField.Error
	// (json:"-") so mcpErrorType classifies it via errors.Is.
	var bErr *schemas.BifrostError
	if opErr != nil {
		bErr = &schemas.BifrostError{
			IsBifrostError: false,
			Error:          &schemas.ErrorField{Message: opErr.Error(), Error: opErr},
			ExtraFields:    schemas.BifrostErrorExtraFields{MCPRequestType: mcpReqType},
		}
	}

	finalResp, finalErr := pipeline.RunMCPPostHooks(ctx, resp, bErr, preCount)
	drainMCPPluginLogs(ctx)

	if finalErr != nil {
		return finalResp, finalErr
	}
	return finalResp, nil
}

// MCPConnectOpFunc is the closure each Connect call site provides to
//
//	. It receives the (possibly mutated) typed sub-request
//
// that flowed through PreMCPConnectionHook plugins and performs the actual transport
// + initialize work (with internal retries), returning a typed sub-response.
type MCPConnectOpFunc func(preReq *schemas.BifrostMCPConnectRequest) (*schemas.BifrostMCPConnectResponse, error)

// runConnectWithPluginPipeline is the typed Connect-specific counterpart to
// runWithPluginPipeline. Connect ops bypass the envelope-based pipeline entirely:
// plugins implement MCPConnectionPlugin (not MCPPlugin), the request/response types
// are the typed sub-structs, and the dispatch never wraps anything in
// BifrostMCPRequest/BifrostMCPResponse.
func (m *MCPManager) runConnectWithPluginPipeline(
	ctx *schemas.BifrostContext,
	req *schemas.BifrostMCPConnectRequest,
	op MCPConnectOpFunc,
) (finalResponse *schemas.BifrostMCPConnectResponse, finalError *schemas.BifrostError) {
	if ctx != nil {
		if _, ok := ctx.Value(schemas.BifrostContextKeyRequestID).(string); !ok {
			ctx.SetValue(schemas.BifrostContextKeyRequestID, uuid.New().String())
		}
	}

	clientName := ""
	if req != nil {
		clientName = req.ClientName
	}

	// Span layout mirrors RunWithPluginPipeline: connect span created after the PreConnection
	// hooks, nesting under the last one; PostConnection hooks nest under it. Connect is the
	// MCP "initialize" method.
	tracer, _ := ctx.Value(schemas.BifrostContextKeyTracer).(schemas.Tracer)

	var connSpanHandle schemas.SpanHandle
	startConnectSpan := func(r *schemas.BifrostMCPConnectRequest) {
		if tracer == nil {
			return
		}
		name := clientName
		if r != nil && r.ClientName != "" {
			name = r.ClientName
		}
		spanName := "mcp.connect"
		if name != "" {
			spanName = fmt.Sprintf("%s.%s", spanName, name)
		}
		spanCtx, handle := tracer.StartSpan(ctx, spanName, schemas.SpanKindMCPClient)
		connSpanHandle = handle
		if spanCtx != nil {
			if id, ok := spanCtx.Value(schemas.BifrostContextKeySpanID).(string); ok && id != "" {
				ctx.SetValue(schemas.BifrostContextKeySpanID, id)
			}
		}
		tracer.SetAttribute(handle, schemas.AttrMCPMethodName, "initialize")
		if r != nil {
			if transport := r.ConnectionType.OTelNetworkTransport(); transport != "" {
				tracer.SetAttribute(handle, schemas.AttrNetworkTransport, transport)
			}
		}
	}
	defer func() {
		if tracer == nil || connSpanHandle == nil {
			return
		}
		setMCPGovernanceSpanAttrs(tracer, connSpanHandle, ctx)
		if finalError != nil {
			msg := ""
			if finalError.Error != nil {
				msg = finalError.Error.Message
			}
			tracer.SetAttribute(connSpanHandle, schemas.AttrErrorTypeSpec, mcpErrorType(finalError))
			tracer.EndSpan(connSpanHandle, schemas.SpanStatusError, msg)
		} else {
			tracer.EndSpan(connSpanHandle, schemas.SpanStatusOk, "")
		}
	}()

	// No pipeline configured → run the op directly under its own span.
	pipeline := m.GetPluginPipeline()
	if pipeline == nil {
		startConnectSpan(req)
		resp, opErr := op(req)
		if opErr != nil {
			return resp, &schemas.BifrostError{
				IsBifrostError: false,
				Error:          &schemas.ErrorField{Message: opErr.Error()},
			}
		}
		return resp, nil
	}
	defer m.ReleasePluginPipeline(pipeline)

	preReq, shortCircuit, preCount := pipeline.RunMCPPreConnectionHooks(ctx, req)
	if preReq != nil {
		clientName = preReq.ClientName
	}

	if shortCircuit != nil {
		if shortCircuit.Response != nil {
			shortCircuit.Response.PopulateExtraFields(clientName)
			finalResp, finalErr := pipeline.RunMCPPostConnectionHooks(ctx, shortCircuit.Response, nil, preCount)
			drainMCPPluginLogs(ctx)
			if finalErr != nil {
				return nil, finalErr
			}
			return finalResp, nil
		}
		if shortCircuit.Error != nil {
			finalResp, finalErr := pipeline.RunMCPPostConnectionHooks(ctx, nil, shortCircuit.Error, preCount)
			drainMCPPluginLogs(ctx)
			if finalErr != nil {
				return nil, finalErr
			}
			if finalResp != nil {
				finalResp.PopulateExtraFields(clientName)
				return finalResp, nil
			}
			return nil, shortCircuit.Error
		}
	}

	if preReq == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error:          &schemas.ErrorField{Message: "Connect request after plugin hooks cannot be nil"},
		}
	}

	// Connect span created after the PreConnection hooks so they nest under it.
	startConnectSpan(preReq)

	resp, opErr := op(preReq)
	if resp != nil {
		resp.PopulateExtraFields(clientName)
	}

	var bErr *schemas.BifrostError
	if opErr != nil {
		bErr = &schemas.BifrostError{
			IsBifrostError: false,
			Error:          &schemas.ErrorField{Message: opErr.Error()},
		}
	}

	finalResp, finalErr := pipeline.RunMCPPostConnectionHooks(ctx, resp, bErr, preCount)
	drainMCPPluginLogs(ctx)

	if finalErr != nil {
		return finalResp, finalErr
	}
	return finalResp, nil
}

// drainMCPPluginLogs mirrors bifrost.drainAndAttachPluginLogs for the mcp package.
// It attaches accumulated plugin log entries to the active trace, if any.
func drainMCPPluginLogs(ctx *schemas.BifrostContext) {
	if ctx == nil {
		return
	}
	tracer, _ := ctx.Value(schemas.BifrostContextKeyTracer).(schemas.Tracer)
	if tracer == nil {
		return
	}
	traceID, _ := ctx.Value(schemas.BifrostContextKeyTraceID).(string)
	if traceID == "" {
		return
	}
	logs := ctx.DrainPluginLogs()
	if len(logs) == 0 {
		return
	}
	tracer.AttachPluginLogs(traceID, logs)
}

// runListToolsWithHooks wraps retrieveExternalToolsDetailed in the MCP plugin gate.
// All four list_tools call sites (connect / oauth-verify / sync / health-check fallback)
// go through this helper so plugins see one consistent hook. The PostHook may mutate
// the Tools / ToolNameMapping fields on the response — the caller receives the mutated
// values via the returned maps.
//
// A PreHook short-circuit with Response is treated as a synthetic tool list (used as-is).
// A PreHook short-circuit with Error returns the error; the caller decides whether to
// keep existing state.
func (m *MCPManager) runListToolsWithHooks(ctx context.Context, conn *client.Client, clientName string) (map[string]schemas.ChatTool, map[string]string, error) {
	req := &schemas.BifrostMCPRequest{
		RequestType:                schemas.MCPRequestTypeListTools,
		ClientName:                 clientName,
		BifrostMCPListToolsRequest: &schemas.BifrostMCPListToolsRequest{},
	}
	gateCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
	start := time.Now()

	resp, bErr := m.RunWithPluginPipeline(gateCtx, req, func(preReq *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
		// Use gateCtx (not the outer ctx) so values a PreMCPHook wrote during the
		// gate — notably BifrostContextKeyMCPExtraHeaders — are visible to the wire
		// call. BifrostContext.Value walks parent-ward only, so the outer ctx cannot
		// see writes made to its gateCtx child.
		detailed, opErr := retrieveExternalToolsDetailed(gateCtx, conn, clientName, m.logger)
		if opErr != nil {
			return nil, opErr
		}
		return &schemas.BifrostMCPResponse{
			BifrostMCPListToolsResponse: &schemas.BifrostMCPListToolsResponse{
				Tools:           detailed.tools,
				ToolNameMapping: detailed.toolNameMapping,
				RawToolCount:    detailed.rawCount,
				SkippedTools:    detailed.skipped,
			},
			ExtraFields: schemas.BifrostMCPResponseExtraFields{
				Latency: time.Since(start).Milliseconds(),
			},
		}, nil
	})

	if bErr != nil {
		return nil, nil, fmt.Errorf("failed to list tools: %s", bErr.GetErrorString())
	}
	if resp == nil || resp.BifrostMCPListToolsResponse == nil {
		// Defensive: response somehow lost its list_tools payload (e.g. PostHook nilled it).
		// Surface empty maps rather than nil to mirror the underlying list_tools contract.
		return make(map[string]schemas.ChatTool), make(map[string]string), nil
	}
	return resp.Tools, resp.ToolNameMapping, nil
}

// runPingWithHooks wraps conn.Ping in the MCP plugin gate. A PreHook may short-circuit
// the ping (synthetic healthy/unhealthy) without touching the wire; a PostHook may
// inspect the latency and outcome. Any error returned here is treated identically to a
// real ping failure by the health-monitor state machine.
func (chm *ClientHealthMonitor) runPingWithHooks(ctx context.Context, conn *client.Client, clientName string) error {
	req := &schemas.BifrostMCPRequest{
		RequestType:           schemas.MCPRequestTypePing,
		ClientName:            clientName,
		BifrostMCPPingRequest: &schemas.BifrostMCPPingRequest{},
	}
	gateCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
	start := time.Now()
	_, bErr := chm.manager.RunWithPluginPipeline(gateCtx, req, func(preReq *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
		// Use gateCtx so a PreMCPHook's context writes (e.g. BifrostContextKeyMCPExtraHeaders)
		// reach the transport headerFunc on this ping. See runListToolsWithHooks for details.
		if pingErr := conn.Ping(gateCtx); pingErr != nil {
			return nil, pingErr
		}
		return &schemas.BifrostMCPResponse{
			BifrostMCPPingResponse: &schemas.BifrostMCPPingResponse{},
			ExtraFields: schemas.BifrostMCPResponseExtraFields{
				Latency: time.Since(start).Milliseconds(),
			},
		}, nil
	})
	if bErr != nil {
		return fmt.Errorf("ping failed: %s", bErr.GetErrorString())
	}
	return nil
}

// connectionTypeForClientName resolves a client's transport by name. Returns "" when no
// live client matches (per-user/ephemeral connections), so callers omit the attribute.
func (m *MCPManager) connectionTypeForClientName(name string) schemas.MCPConnectionType {
	if name == "" {
		return ""
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, state := range m.clientMap {
		// Match on ExecutionConfig.Name, as GetClientByName does.
		if state != nil && state.ExecutionConfig != nil && state.ExecutionConfig.Name == name {
			return state.ExecutionConfig.ConnectionType
		}
	}
	return ""
}

// mcpErrorType classifies a failed MCP op into a low-cardinality error.type for the
// metric (_OTHER is the OTel catch-all). Best-effort: an error swapped by a PostHook
// degrades to _OTHER.
func mcpErrorType(bErr *schemas.BifrostError) string {
	if bErr == nil {
		return "_OTHER"
	}
	if bErr.ExtraFields.MCPAuthRequired != nil {
		return "auth_required"
	}
	if bErr.Error != nil && bErr.Error.Error != nil {
		switch {
		case errors.Is(bErr.Error.Error, ErrMCPToolTimeout):
			return "timeout"
		case errors.Is(bErr.Error.Error, ErrMCPToolCallFailed):
			return "tool_error"
		}
	}
	return "_OTHER"
}

// setMCPGovernanceSpanAttrs copies the metric-safe bifrost.* governance identity from
// context onto an MCP span for per-tenant metric breakdowns. Absent dims are skipped.
func setMCPGovernanceSpanAttrs(tracer schemas.Tracer, handle schemas.SpanHandle, ctx *schemas.BifrostContext) {
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
}

package mcp

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/client"
	"github.com/maximhq/bifrost/core/schemas"
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

	// Wrap the whole gate (PreHook + op + PostHook) in an outer span so traces show one
	// row per MCP op alongside the per-plugin spans the pipeline emits internally.
	tracer, _ := ctx.Value(schemas.BifrostContextKeyTracer).(schemas.Tracer)
	var spanHandle schemas.SpanHandle
	if tracer != nil {
		spanName := fmt.Sprintf("mcp.%s", req.RequestType)
		if req.ClientName != "" {
			spanName = fmt.Sprintf("%s.%s", spanName, req.ClientName)
		}
		_, spanHandle = tracer.StartSpan(ctx, spanName, schemas.SpanKindMCPClient)
		// Emit OTel GenAI tool-execution attributes on execute-tool spans so downstream
		// backends can correlate tool calls with their requesting llm.call.
		if req != nil && req.RequestType.IsExecuteTool() {
			tracer.SetAttribute(spanHandle, schemas.AttrOperationName, schemas.OTelOperationNameExecuteTool)
			tracer.SetAttribute(spanHandle, schemas.AttrToolType, "function")
			if name := req.GetToolName(); name != "" {
				tracer.SetAttribute(spanHandle, schemas.AttrToolName, name)
			}
			// GetToolArguments returns interface{}; the Responses branch boxes a
			// *string, so a nil pointer survives the != nil guard. Unwrap and skip
			// it explicitly, and deref non-nil so the attribute is the JSON string.
			if args := req.GetToolArguments(); args != nil {
				if p, ok := args.(*string); ok {
					if p != nil {
						tracer.SetAttribute(spanHandle, schemas.AttrToolCallArguments, *p)
					}
				} else {
					tracer.SetAttribute(spanHandle, schemas.AttrToolCallArguments, args)
				}
			}
			if req.ChatAssistantMessageToolCall != nil && req.ChatAssistantMessageToolCall.ID != nil {
				tracer.SetAttribute(spanHandle, schemas.AttrToolCallID, *req.ChatAssistantMessageToolCall.ID)
			} else if req.ResponsesToolMessage != nil && req.ResponsesToolMessage.CallID != nil {
				tracer.SetAttribute(spanHandle, schemas.AttrToolCallID, *req.ResponsesToolMessage.CallID)
			}
		}
	}
	defer func() {
		if tracer == nil {
			return
		}
		// Tool-call result captured via named returns — set just before EndSpan so the
		// attribute lands on the open span before it's frozen.
		if finalResponse != nil && req != nil && req.RequestType.IsExecuteTool() {
			if data, err := schemas.MarshalString(finalResponse); err == nil {
				tracer.SetAttribute(spanHandle, schemas.AttrToolCallResult, data)
			}
		}
		if finalError != nil {
			msg := ""
			if finalError.Error != nil {
				msg = finalError.Error.Message
			}
			tracer.EndSpan(spanHandle, schemas.SpanStatusError, msg)
		} else {
			tracer.EndSpan(spanHandle, schemas.SpanStatusOk, "")
		}
	}()

	// MCP request type stamped on every wrapped BifrostError so downstream gates
	// (governance, logging) can discriminate execute-tool calls from ping/list_tools.
	mcpReqType := schemas.MCPRequestType("")
	if req != nil {
		mcpReqType = req.RequestType
	}

	// No pipeline configured → run the op directly, no hooks (but span still recorded).
	pipeline := m.GetPluginPipeline()
	if pipeline == nil {
		resp, opErr := op(req)
		if opErr != nil {
			return resp, &schemas.BifrostError{
				IsBifrostError: false,
				Error:          &schemas.ErrorField{Message: opErr.Error()},
				ExtraFields:    schemas.BifrostErrorExtraFields{MCPRequestType: mcpReqType},
			}
		}
		return resp, nil
	}
	defer m.ReleasePluginPipeline(pipeline)

	// PreHooks. preReq is the (possibly mutated) request that must flow to the op.
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
		// Short-circuit with response — still run PostHooks for plugins that ran.
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

	// Run the actual wire op with the mutated request.
	resp, opErr := op(preReq)
	if resp != nil {
		resp.PopulateExtraFields(mcpReqType, clientName, toolName)
	}

	// Wrap opErr as BifrostError so PostHooks see a typed error.
	var bErr *schemas.BifrostError
	if opErr != nil {
		bErr = &schemas.BifrostError{
			IsBifrostError: false,
			Error:          &schemas.ErrorField{Message: opErr.Error()},
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
) (*schemas.BifrostMCPConnectResponse, *schemas.BifrostError) {
	if ctx != nil {
		if _, ok := ctx.Value(schemas.BifrostContextKeyRequestID).(string); !ok {
			ctx.SetValue(schemas.BifrostContextKeyRequestID, uuid.New().String())
		}
	}

	clientName := ""
	if req != nil {
		clientName = req.ClientName
	}

	// Outer span so traces show one row per Connect op.
	tracer, _ := ctx.Value(schemas.BifrostContextKeyTracer).(schemas.Tracer)
	var spanHandle schemas.SpanHandle
	if tracer != nil {
		spanName := "mcp.connect"
		if clientName != "" {
			spanName = fmt.Sprintf("%s.%s", spanName, clientName)
		}
		_, spanHandle = tracer.StartSpan(ctx, spanName, schemas.SpanKindMCPClient)
	}
	defer func() {
		if tracer != nil {
			tracer.EndSpan(spanHandle, schemas.SpanStatusOk, "")
		}
	}()

	// No pipeline configured → run the op directly.
	pipeline := m.GetPluginPipeline()
	if pipeline == nil {
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

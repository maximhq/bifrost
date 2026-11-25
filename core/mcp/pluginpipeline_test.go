package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errorRecordingPipeline records the error its MCP post-hook is handed; short, when set, is what its
// pre-hook short-circuits with.
type errorRecordingPipeline struct {
	short *schemas.MCPPluginShortCircuit
	seen  *schemas.BifrostError
}

func (p *errorRecordingPipeline) RunMCPPreHooks(_ *schemas.BifrostContext, req *schemas.BifrostMCPRequest) (*schemas.BifrostMCPRequest, *schemas.MCPPluginShortCircuit, int) {
	return req, p.short, 1
}

func (p *errorRecordingPipeline) RunMCPPostHooks(_ *schemas.BifrostContext, resp *schemas.BifrostMCPResponse, bifrostErr *schemas.BifrostError, _ int) (*schemas.BifrostMCPResponse, *schemas.BifrostError) {
	p.seen = bifrostErr
	return resp, bifrostErr
}

func (p *errorRecordingPipeline) RunMCPPreConnectionHooks(_ *schemas.BifrostContext, req *schemas.BifrostMCPConnectRequest) (*schemas.BifrostMCPConnectRequest, *schemas.MCPConnectionShortCircuit, int) {
	return req, nil, 0
}

func (p *errorRecordingPipeline) RunMCPPostConnectionHooks(_ *schemas.BifrostContext, resp *schemas.BifrostMCPConnectResponse, bifrostErr *schemas.BifrostError, _ int) (*schemas.BifrostMCPConnectResponse, *schemas.BifrostError) {
	return resp, bifrostErr
}

// A failed MCP call's error names the client and tool it was for, the way its response would, so a
// post-hook can tell which call failed: when the call itself fails, when a pre-hook refuses it, and
// with no plugins at all.
func TestRunWithPluginPipeline_ErrorsNameTheClientAndTool(t *testing.T) {
	failing := func(*schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
		return nil, errors.New("upstream failed")
	}
	for _, tc := range []struct {
		name     string
		pipeline *errorRecordingPipeline
	}{
		{"the call fails", &errorRecordingPipeline{}},
		{"a pre-hook refuses it", &errorRecordingPipeline{short: &schemas.MCPPluginShortCircuit{
			Error: &schemas.BifrostError{Error: &schemas.ErrorField{Message: "refused"}},
		}}},
		{"no plugins", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := schemas.MCPConfig{}
			if tc.pipeline != nil {
				config.PluginPipelineProvider = func() interface{} { return tc.pipeline }
				config.ReleasePluginPipeline = func(interface{}) {}
			}
			m := NewMCPManager(context.Background(), config, newTestAccount(), nil, &MockLogger{}, nil)
			name := "notes-search"
			req := &schemas.BifrostMCPRequest{
				RequestType:                  schemas.MCPRequestTypeChatToolCall,
				ClientName:                   "notes",
				ChatAssistantMessageToolCall: &schemas.ChatAssistantMessageToolCall{Function: schemas.ChatAssistantMessageToolCallFunction{Name: &name}},
			}

			_, bErr := m.RunWithPluginPipeline(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), req, failing)
			require.NotNil(t, bErr)
			assert.Equal(t, "notes", bErr.ExtraFields.MCPClientName)
			assert.Equal(t, "notes-search", bErr.ExtraFields.MCPToolName)
			if tc.pipeline != nil {
				require.NotNil(t, tc.pipeline.seen, "the post-hook ran")
				assert.Equal(t, "notes", tc.pipeline.seen.ExtraFields.MCPClientName, "the post-hook sees the client")
				assert.Equal(t, "notes-search", tc.pipeline.seen.ExtraFields.MCPToolName, "and the tool")
			}
		})
	}
}

// responseRecordingPipeline is errorRecordingPipeline that also records the
// response its post-hook is handed.
type responseRecordingPipeline struct {
	errorRecordingPipeline
	seenResponse *schemas.BifrostMCPResponse
}

func (p *responseRecordingPipeline) RunMCPPostHooks(ctx *schemas.BifrostContext, resp *schemas.BifrostMCPResponse, bifrostErr *schemas.BifrostError, n int) (*schemas.BifrostMCPResponse, *schemas.BifrostError) {
	p.seenResponse = resp
	return p.errorRecordingPipeline.RunMCPPostHooks(ctx, resp, bifrostErr, n)
}

// pricedToolManager has one client, notes, pricing search at 0.002 and free at
// 0, with send-back cost on when enabled. A nil pipeline means no plugins.
func pricedToolManager(pipeline PluginPipeline, enabled bool) *MCPManager {
	config := schemas.MCPConfig{ResponseCostEnabled: func(*schemas.BifrostContext) bool { return enabled }}
	if pipeline != nil {
		config.PluginPipelineProvider = func() interface{} { return pipeline }
		config.ReleasePluginPipeline = func(interface{}) {}
	}
	m := NewMCPManager(context.Background(), config, newTestAccount(), nil, &MockLogger{}, nil)
	notes := &schemas.MCPClientConfig{ID: "notes-id", Name: "notes", ToolPricing: map[string]float64{"search": 0.002, "free": 0}}
	putClient(m, &schemas.MCPClientState{Name: notes.Name, ExecutionConfig: notes, State: schemas.MCPConnectionStateHealthy})
	return m
}

// toolCallOn is an execute-tool request for client's tool, named the way the
// gate receives it (prefixed); code-mode tools have no client.
func toolCallOn(client, tool string) *schemas.BifrostMCPRequest {
	name := tool
	if client != "" {
		name = client + "-" + tool
	}
	return &schemas.BifrostMCPRequest{
		RequestType:                  schemas.MCPRequestTypeChatToolCall,
		ClientName:                   client,
		ChatAssistantMessageToolCall: &schemas.ChatAssistantMessageToolCall{Function: schemas.ChatAssistantMessageToolCallFunction{Name: &name}},
	}
}

// toolResult answers a tool call the way ToolsManager does: with the client
// and the unprefixed tool name already stamped.
func toolResult(client, tool string) MCPOpFunc {
	return func(*schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
		return &schemas.BifrostMCPResponse{
			ChatMessage: &schemas.ChatMessage{Role: schemas.ChatMessageRoleTool, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("ok")}},
			ExtraFields: schemas.BifrostMCPResponseExtraFields{ClientName: client, ToolName: tool},
		}, nil
	}
}

// A successful call of a priced tool carries its tool_pricing price as MCPCost,
// set after the post-hooks on a copy: the response a post-hook saw is unpriced.
func TestRunWithPluginPipeline_PricesToolCallsOnACopy(t *testing.T) {
	pipeline := &responseRecordingPipeline{}
	m := pricedToolManager(pipeline, true)

	resp, bErr := m.RunWithPluginPipeline(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), toolCallOn("notes", "search"), toolResult("notes", "search"))
	require.Nil(t, bErr)
	require.NotNil(t, resp.ExtraFields.Cost)
	assert.Equal(t, schemas.BifrostCost{
		AdditionalCost:        0.002,
		AdditionalCostDetails: &schemas.AdditionalCostDetails{MCPCost: 0.002},
		TotalCost:             0.002,
	}, *resp.ExtraFields.Cost)
	require.NotNil(t, pipeline.seenResponse)
	assert.NotSame(t, pipeline.seenResponse, resp, "the priced response is a copy")
	assert.Nil(t, pipeline.seenResponse.ExtraFields.Cost, "the response the post-hook saw is not written")
	assert.Equal(t, "ok", *resp.ChatMessage.Content.ContentStr)

	// With no plugins configured the call is priced the same way.
	resp, bErr = pricedToolManager(nil, true).RunWithPluginPipeline(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), toolCallOn("notes", "search"), toolResult("notes", "search"))
	require.Nil(t, bErr)
	require.NotNil(t, resp.ExtraFields.Cost)
	assert.Equal(t, 0.002, resp.ExtraFields.Cost.TotalCost)
}

// The gate reads the price from the client's current record, so an edit saved and applied with
// UpdateClient reaches the next call's cost, as it reaches what governance charges.
func TestRunWithPluginPipeline_PricesFromTheEditedRecord(t *testing.T) {
	m := pricedToolManager(&responseRecordingPipeline{}, true)
	price := func() float64 {
		t.Helper()
		resp, bErr := m.RunWithPluginPipeline(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), toolCallOn("notes", "search"), toolResult("notes", "search"))
		require.Nil(t, bErr)
		require.NotNil(t, resp.ExtraFields.Cost)
		return resp.ExtraFields.Cost.TotalCost
	}
	require.Equal(t, 0.002, price())

	require.NoError(t, editClient(m, "notes-id", func(c *schemas.MCPClientConfig) { c.ToolPricing = map[string]float64{"search": 0.005} }))
	assert.Equal(t, 0.005, price(), "the edited price is the one returned")
}

// Only a successful call of a tool with a positive price is priced.
func TestRunWithPluginPipeline_LeavesUnpricedCallsUnpriced(t *testing.T) {
	failing := func(*schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
		return nil, errors.New("upstream failed")
	}
	for _, tc := range []struct {
		name    string
		enabled bool
		req     *schemas.BifrostMCPRequest
		op      MCPOpFunc
	}{
		{"setting off", false, toolCallOn("notes", "search"), toolResult("notes", "search")},
		{"failed call", true, toolCallOn("notes", "search"), failing},
		{"tool without a price", true, toolCallOn("notes", "other"), toolResult("notes", "other")},
		{"price of zero", true, toolCallOn("notes", "free"), toolResult("notes", "free")},
		{"unknown client", true, toolCallOn("drive", "search"), toolResult("drive", "search")},
		{"code-mode tool", true, toolCallOn("", ToolTypeListToolFiles), toolResult("", ToolTypeListToolFiles)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, _ := pricedToolManager(&responseRecordingPipeline{}, tc.enabled).RunWithPluginPipeline(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), tc.req, tc.op)
			if resp != nil {
				assert.Nil(t, resp.ExtraFields.Cost)
			}
		})
	}
}

// executeToolCode is not priced itself; it carries the sum of the priced tool
// calls its code made, each of which runs through the gate on a context
// derived from the outer call's.
func TestRunWithPluginPipeline_SumsCodeModeNestedToolCosts(t *testing.T) {
	m := pricedToolManager(&responseRecordingPipeline{}, true)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	var nestedCosts []*schemas.BifrostCost
	runCode := func(*schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
		for _, tool := range []string{"search", "search", "other"} {
			nestedCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			nested, bErr := m.RunWithPluginPipeline(nestedCtx, toolCallOn("notes", tool), toolResult("notes", tool))
			require.Nil(t, bErr)
			nestedCosts = append(nestedCosts, nested.ExtraFields.Cost)
		}
		return toolResult("", ToolTypeExecuteToolCode)(nil)
	}

	resp, bErr := m.RunWithPluginPipeline(ctx, toolCallOn("", ToolTypeExecuteToolCode), runCode)
	require.Nil(t, bErr)
	require.NotNil(t, resp.ExtraFields.Cost)
	assert.InDelta(t, 0.004, resp.ExtraFields.Cost.TotalCost, 1e-12)
	assert.InDelta(t, 0.004, resp.ExtraFields.Cost.AdditionalCostDetails.MCPCost, 1e-12)
	assert.NotNil(t, nestedCosts[0], "each nested call still carries its own cost")
	assert.Nil(t, nestedCosts[2])
	assert.Nil(t, ctx.Value(codeModeCostSumKey{}), "the sum is removed once the call ends")

	// No priced nested call means no cost on executeToolCode either.
	resp, bErr = m.RunWithPluginPipeline(ctx, toolCallOn("", ToolTypeExecuteToolCode), toolResult("", ToolTypeExecuteToolCode))
	require.Nil(t, bErr)
	assert.Nil(t, resp.ExtraFields.Cost)
}

// With send-back cost off, the gate's tool-cost steps add no allocation. The
// callback reads the per-request override through a context chain, as core's does.
func BenchmarkWithToolCostOff(b *testing.B) {
	m := pricedToolManager(nil, false)
	m.responseCostEnabled = func(ctx *schemas.BifrostContext) bool {
		enabled, _ := ctx.Value(schemas.BifrostContextKeySendBackCost).(bool)
		return enabled
	}
	ctx := schemas.NewBifrostContext(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), schemas.NoDeadline)
	req := toolCallOn("notes", "search")
	resp, _ := toolResult("notes", "search")(nil)
	b.ReportAllocs()
	for b.Loop() {
		callCtx := m.codeModeCallContext(ctx, req)
		sum, end := m.startCodeModeCostSum(callCtx, req)
		_ = m.withToolCost(callCtx, req.RequestType, resp, sum)
		end()
	}
}

// An executeToolCode call collects its nested costs on a context of its own: the
// caller's may be shared (a nil-context SDK call runs on Bifrost's own context),
// and a sum left there would collect a concurrent call's costs.
func TestCodeModeCostSumStaysOffTheCallersContext(t *testing.T) {
	m := pricedToolManager(nil, true)
	shared := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := toolCallOn("", ToolTypeExecuteToolCode)

	callCtx := m.codeModeCallContext(shared, req)
	require.NotSame(t, shared, callCtx, "executeToolCode runs on its own child context")
	sum, end := m.startCodeModeCostSum(callCtx, req)
	require.NotNil(t, sum)
	assert.Nil(t, shared.Value(codeModeCostSumKey{}), "the shared context never sees the sum")
	assert.Same(t, sum, schemas.NewBifrostContext(callCtx, schemas.NoDeadline).Value(codeModeCostSumKey{}), "the call's nested contexts do")

	// A concurrent priced call on the shared context is not added to the sum.
	other, bErr := m.RunWithPluginPipeline(shared, toolCallOn("notes", "search"), toolResult("notes", "search"))
	require.Nil(t, bErr)
	require.NotNil(t, other.ExtraFields.Cost)
	assert.Nil(t, sum.total())
	end()

	// Other tools, and executeToolCode with the setting off, keep the caller's context.
	assert.Same(t, shared, m.codeModeCallContext(shared, toolCallOn("notes", "search")))
	assert.Same(t, shared, pricedToolManager(nil, false).codeModeCallContext(shared, req))
}

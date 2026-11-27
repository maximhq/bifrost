package mcp

import (
	"sync"

	"github.com/maximhq/bifrost/core/schemas"
)

// Send back cost for MCP tool executions. A successful call of a priced tool
// carries its client's tool_pricing price in ExtraFields.Cost (as MCPCost), set
// on a copy after the post-hooks. The conditions mirror the governance plugin's
// PostMCPHook, so the cost returned is what governance charges. executeToolCode
// is never priced itself; it carries the sum of the tool calls its code made.

// codeModeCostSumKey keys the running sum of an executeToolCode call's nested
// tool costs. The nested calls' contexts descend from the outer call's, so they
// find it through the parent chain.
type codeModeCostSumKey struct{}

// codeModeCostSum adds up the costs of the tool calls one executeToolCode made.
type codeModeCostSum struct {
	mu   sync.Mutex
	cost *schemas.BifrostCost
}

func (s *codeModeCostSum) add(cost *schemas.BifrostCost) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cost = s.cost.Add(cost)
}

func (s *codeModeCostSum) total() *schemas.BifrostCost {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cost
}

// toolCostEnabled reports whether tool executions on ctx carry their cost.
func (m *MCPManager) toolCostEnabled(ctx *schemas.BifrostContext) bool {
	return ctx != nil && m.responseCostEnabled != nil && m.responseCostEnabled(ctx)
}

// codeModeCallContext gives an executeToolCode call its own child of ctx when
// send-back cost is on, so the sum startCodeModeCostSum installs is seen only by
// that call's nested tool calls. ctx may be shared: a nil-context SDK call runs on
// Bifrost's own context, and a caller may reuse one for concurrent calls. The
// returned function cancels the child once the call has finished. Any other
// request keeps ctx and gets a no-op.
func (m *MCPManager) codeModeCallContext(ctx *schemas.BifrostContext, req *schemas.BifrostMCPRequest) (*schemas.BifrostContext, func()) {
	if req == nil || req.GetToolName() != ToolTypeExecuteToolCode || !m.toolCostEnabled(ctx) {
		return ctx, func() {}
	}
	callCtx, cancel := schemas.NewBifrostContextWithCancel(ctx)
	return callCtx, cancel
}

// startCodeModeCostSum installs a sum for an executeToolCode call when send-back
// cost is on, and returns a function that removes it. It returns a nil sum and a
// no-op for every other request. ownContext is whether the call reached the gate
// as executeToolCode, read before the pre-hooks, which may rewrite the request in
// place; that is the call codeModeCallContext gave a context of its own. executed
// is the call the pre-hooks let through. The sum is started only when both hold,
// so a call retargeted to another tool is priced as that tool, and one retargeted
// to executeToolCode, whose context may be shared, carries no sum (each of its
// nested calls still carries its own cost).
func (m *MCPManager) startCodeModeCostSum(ctx *schemas.BifrostContext, ownContext bool, executed *schemas.BifrostMCPRequest) (*codeModeCostSum, func()) {
	if !ownContext || !isExecuteToolCode(executed) || !m.toolCostEnabled(ctx) {
		return nil, func() {}
	}
	sum := &codeModeCostSum{}
	ctx.SetValue(codeModeCostSumKey{}, sum)
	return sum, func() { ctx.ClearValue(codeModeCostSumKey{}) }
}

// isExecuteToolCode reports whether req executes the executeToolCode tool.
func isExecuteToolCode(req *schemas.BifrostMCPRequest) bool {
	return req != nil && req.RequestType.IsExecuteTool() && req.GetToolName() == ToolTypeExecuteToolCode
}

// withToolCost returns resp with its tool cost attached, or resp unchanged when
// the setting is off, reqType is not a tool execution, the tool has no price, or
// the cost is already set. sum is the executeToolCode sum started for this call,
// or nil. The client and unprefixed tool name come from resp, where the op
// stamped them and where governance reads them.
func (m *MCPManager) withToolCost(ctx *schemas.BifrostContext, reqType schemas.MCPRequestType, resp *schemas.BifrostMCPResponse, sum *codeModeCostSum) *schemas.BifrostMCPResponse {
	if resp == nil || !reqType.IsExecuteTool() || resp.ExtraFields.Cost != nil || !m.toolCostEnabled(ctx) {
		return resp
	}
	if sum != nil {
		if total := sum.total(); total != nil && total.TotalCost > 0 {
			return resp.WithCost(total)
		}
		return resp
	}
	clientName, toolName := resp.ExtraFields.ClientName, resp.ExtraFields.ToolName
	if clientName == "" || toolName == "" || IsCodeModeTool(toolName) {
		return resp
	}
	client := m.GetClientByName(clientName)
	if client == nil || client.ExecutionConfig == nil {
		return resp
	}
	price := client.ExecutionConfig.ToolPricing[toolName]
	if price <= 0 {
		return resp
	}
	cost := &schemas.BifrostCost{
		AdditionalCost:        price,
		AdditionalCostDetails: &schemas.AdditionalCostDetails{MCPCost: price},
		TotalCost:             price,
	}
	// A call made by executeToolCode's code also counts toward that call's sum.
	if outer, ok := ctx.Value(codeModeCostSumKey{}).(*codeModeCostSum); ok {
		outer.add(cost)
	}
	return resp.WithCost(cost)
}

// captureToolCost hands a direct tool call's cost to the capture its caller
// installed on ctx (see schemas.MCPToolCostCapture), whose public result is
// only the tool message. Nothing is written when there is no capture.
func captureToolCost(ctx *schemas.BifrostContext, resp *schemas.BifrostMCPResponse) {
	if capture := toolCostCapture(ctx); capture != nil && resp != nil {
		capture.Cost = resp.ExtraFields.Cost
	}
}

// toolCostCapture returns the capture a direct tool caller installed on ctx, or nil.
func toolCostCapture(ctx *schemas.BifrostContext) *schemas.MCPToolCostCapture {
	if ctx == nil {
		return nil
	}
	capture, _ := ctx.Value(schemas.BifrostContextKeyMCPToolCostCapture).(*schemas.MCPToolCostCapture)
	return capture
}

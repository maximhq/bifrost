package telemetry

import (
	"context"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/prometheus/client_golang/prometheus"
)

// TestInitFiltersCustomLabelsCollidingWithMCP is a regression for the P1 panic: a custom
// label matching an MCP metric label was appended twice to the MCP HistogramVec, so promauto
// panicked at Init. Colliding labels must be dropped; non-colliding ones kept.
func TestInitFiltersCustomLabelsCollidingWithMCP(t *testing.T) {
	p, err := Init(&Config{CustomLabels: []string{"mcp_client", "error_type", "keep_me"}}, nil, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if containsLabel(p.customLabels, "mcp_client") || containsLabel(p.customLabels, "error_type") {
		t.Fatalf("MCP-colliding custom labels not filtered: %v", p.customLabels)
	}
	if !containsLabel(p.customLabels, "keep_me") {
		t.Fatalf("non-colliding custom label was dropped: %v", p.customLabels)
	}
}

// mcpSample is one gathered series of the MCP duration histogram.
type mcpSample struct {
	count  uint64
	sum    float64
	labels map[string]string
}

// gatherMCP returns every series of bifrost_mcp_client_operation_duration_seconds.
func gatherMCP(t *testing.T, reg *prometheus.Registry) []mcpSample {
	t.Helper()
	fams, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var out []mcpSample
	for _, mf := range fams {
		if mf.GetName() != "bifrost_mcp_client_operation_duration_seconds" {
			continue
		}
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			out = append(out, mcpSample{m.GetHistogram().GetSampleCount(), m.GetHistogram().GetSampleSum(), labels})
		}
	}
	return out
}

func mcpHookContext() *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(time.Minute))
	ctx.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyID, "vk-1")
	ctx.SetValue(schemas.BifrostContextKeyGovernanceTeamID, "team-9")
	return ctx
}

// TestPostMCPHookRecordsToolDuration verifies a successful execute-tool call records one
// histogram sample using the wire latency (ms→s) with the semconv + governance labels.
func TestPostMCPHookRecordsToolDuration(t *testing.T) {
	p := newTestPlugin(t)
	ctx := mcpHookContext()

	req := &schemas.BifrostMCPRequest{RequestType: schemas.MCPRequestTypeChatToolCall}
	if _, _, err := p.PreMCPHook(ctx, req); err != nil {
		t.Fatalf("PreMCPHook: %v", err)
	}
	resp := &schemas.BifrostMCPResponse{
		ExtraFields: schemas.BifrostMCPResponseExtraFields{
			MCPRequestType: schemas.MCPRequestTypeChatToolCall,
			ClientName:     "ctx7",
			ToolName:       "query-docs",
			Latency:        2574,
		},
	}
	if _, _, err := p.PostMCPHook(ctx, resp, nil); err != nil {
		t.Fatalf("PostMCPHook: %v", err)
	}

	samples := gatherMCP(t, p.registry)
	if len(samples) != 1 {
		t.Fatalf("want 1 mcp series, got %d", len(samples))
	}
	s := samples[0]
	if s.count != 1 {
		t.Fatalf("sample count = %d, want 1", s.count)
	}
	if s.sum < 2.573 || s.sum > 2.575 {
		t.Fatalf("duration sum = %v, want ~2.574 (from Latency ms)", s.sum)
	}
	for k, want := range map[string]string{
		"mcp_client":     "ctx7",
		"mcp_tool_name":  "query-docs",
		"mcp_method":     "tools/call",
		"error_type":     "",
		"virtual_key_id": "vk-1",
		"team_id":        "team-9",
	} {
		if s.labels[k] != want {
			t.Fatalf("label %s = %q, want %q", k, s.labels[k], want)
		}
	}
}

// TestPostMCPHookSkipsNonToolAndCodemode verifies lifecycle ops (list_tools) and codemode
// tools are not recorded.
func TestPostMCPHookSkipsNonToolAndCodemode(t *testing.T) {
	p := newTestPlugin(t)
	ctx := mcpHookContext()

	// list_tools is a lifecycle op, not a tool call.
	listResp := &schemas.BifrostMCPResponse{
		ExtraFields: schemas.BifrostMCPResponseExtraFields{
			MCPRequestType: schemas.MCPRequestTypeListTools,
			ClientName:     "ctx7",
			Latency:        10,
		},
	}
	if _, _, err := p.PostMCPHook(ctx, listResp, nil); err != nil {
		t.Fatalf("PostMCPHook(list_tools): %v", err)
	}
	// A codemode tool call must be skipped.
	codemodeResp := &schemas.BifrostMCPResponse{
		ExtraFields: schemas.BifrostMCPResponseExtraFields{
			MCPRequestType: schemas.MCPRequestTypeChatToolCall,
			ClientName:     "codemode",
			ToolName:       "executeToolCode",
			Latency:        20,
		},
	}
	if _, _, err := p.PostMCPHook(ctx, codemodeResp, nil); err != nil {
		t.Fatalf("PostMCPHook(codemode): %v", err)
	}

	if samples := gatherMCP(t, p.registry); len(samples) != 0 {
		t.Fatalf("want 0 mcp series (both skipped), got %d", len(samples))
	}
}

// TestPostMCPHookErrorTypeAndWallTimeFallback verifies the error path tags error_type and
// falls back to wall-time latency when no response latency exists.
func TestPostMCPHookErrorTypeAndWallTimeFallback(t *testing.T) {
	p := newTestPlugin(t)
	ctx := mcpHookContext()

	req := mcpToolReq("ctx7", "query-docs")
	if _, _, err := p.PreMCPHook(ctx, req); err != nil {
		t.Fatalf("PreMCPHook: %v", err)
	}
	// Seed a known past start time so the wall-time fallback is a detectable ~1s.
	ctx.SetValue(mcpStartTimeKey, time.Now().Add(-time.Second))

	// No response — latency comes from the wall-time stash; identity from PreMCPHook.
	bifrostErr := &schemas.BifrostError{
		ExtraFields: schemas.BifrostErrorExtraFields{
			MCPRequestType:  schemas.MCPRequestTypeChatToolCall,
			MCPAuthRequired: &schemas.MCPAuthRequiredError{},
		},
	}
	if _, _, err := p.PostMCPHook(ctx, nil, bifrostErr); err != nil {
		t.Fatalf("PostMCPHook: %v", err)
	}

	samples := gatherMCP(t, p.registry)
	if len(samples) != 1 {
		t.Fatalf("want 1 mcp series, got %d", len(samples))
	}
	s := samples[0]
	if s.count != 1 {
		t.Fatalf("sample count = %d, want 1", s.count)
	}
	if s.labels["error_type"] != "auth_required" {
		t.Fatalf("error_type = %q, want auth_required", s.labels["error_type"])
	}
	if s.sum < 1 || s.sum > 2 {
		t.Fatalf("wall-time duration = %v, want ~1s (fallback active)", s.sum)
	}
	// Identity must be recovered on the error path (no response present).
	if s.labels["mcp_client"] != "ctx7" || s.labels["mcp_tool_name"] != "query-docs" {
		t.Fatalf("recovered identity = (%q, %q), want (ctx7, query-docs)", s.labels["mcp_client"], s.labels["mcp_tool_name"])
	}
}

// TestPostMCPHookErrorPathSkipsCodemode verifies a failed codemode tool call is skipped:
// the tool name recovered from PreMCPHook drives the codemode filter even with no response.
func TestPostMCPHookErrorPathSkipsCodemode(t *testing.T) {
	p := newTestPlugin(t)
	ctx := mcpHookContext()

	req := mcpToolReq("codemode", "executeToolCode")
	if _, _, err := p.PreMCPHook(ctx, req); err != nil {
		t.Fatalf("PreMCPHook: %v", err)
	}
	bifrostErr := &schemas.BifrostError{
		ExtraFields: schemas.BifrostErrorExtraFields{MCPRequestType: schemas.MCPRequestTypeChatToolCall},
	}
	if _, _, err := p.PostMCPHook(ctx, nil, bifrostErr); err != nil {
		t.Fatalf("PostMCPHook: %v", err)
	}
	if samples := gatherMCP(t, p.registry); len(samples) != 0 {
		t.Fatalf("want 0 mcp series (failed codemode call must be skipped), got %d", len(samples))
	}
}

// mcpToolReq builds an execute-tool MCP request with a client name and tool name.
func mcpToolReq(client, tool string) *schemas.BifrostMCPRequest {
	name := tool
	return &schemas.BifrostMCPRequest{
		RequestType: schemas.MCPRequestTypeChatToolCall,
		ClientName:  client,
		ChatAssistantMessageToolCall: &schemas.ChatAssistantMessageToolCall{
			Function: schemas.ChatAssistantMessageToolCallFunction{Name: &name},
		},
	}
}

// TestPostMCPHookRecordsPromptAndResourceReads verifies prompts/get and resources/read record a
// sample under their own method; a resource read carries no name, since URIs are unbounded.
func TestPostMCPHookRecordsPromptAndResourceReads(t *testing.T) {
	p := newTestPlugin(t)
	for _, req := range []*schemas.BifrostMCPRequest{
		{RequestType: schemas.MCPRequestTypeGetPrompt, ClientName: "docs", BifrostMCPGetPromptRequest: &schemas.BifrostMCPGetPromptRequest{Name: "docs-review"}},
		{RequestType: schemas.MCPRequestTypeReadResource, ClientName: "docs", BifrostMCPReadResourceRequest: &schemas.BifrostMCPReadResourceRequest{URI: "repo://acme/widgets"}},
	} {
		ctx := mcpHookContext()
		if _, _, err := p.PreMCPHook(ctx, req); err != nil {
			t.Fatalf("PreMCPHook: %v", err)
		}
		resp := &schemas.BifrostMCPResponse{ExtraFields: schemas.BifrostMCPResponseExtraFields{MCPRequestType: req.RequestType, ClientName: "docs", Latency: 5}}
		if _, _, err := p.PostMCPHook(ctx, resp, nil); err != nil {
			t.Fatalf("PostMCPHook: %v", err)
		}
	}

	got := map[string]string{}
	for _, s := range gatherMCP(t, p.registry) {
		if s.labels["mcp_client"] != "docs" {
			t.Fatalf("mcp_client = %q, want docs", s.labels["mcp_client"])
		}
		got[s.labels["mcp_method"]] = s.labels["mcp_tool_name"]
	}
	want := map[string]string{"prompts/get": "review", "resources/read": ""}
	if len(got) != len(want) || got["prompts/get"] != want["prompts/get"] || got["resources/read"] != want["resources/read"] {
		t.Fatalf("series = %v, want %v", got, want)
	}
}

// TestPostMCPHookRecordsReadUnderTheServerThatAnswered verifies a resource read core moved to
// another server after the pre-hooks is recorded under the server the response names.
func TestPostMCPHookRecordsReadUnderTheServerThatAnswered(t *testing.T) {
	p := newTestPlugin(t)
	req := &schemas.BifrostMCPRequest{RequestType: schemas.MCPRequestTypeReadResource, ClientName: "a", BifrostMCPReadResourceRequest: &schemas.BifrostMCPReadResourceRequest{URI: "file:///handbook.md"}}
	ctx := mcpHookContext()
	if _, _, err := p.PreMCPHook(ctx, req); err != nil {
		t.Fatalf("PreMCPHook: %v", err)
	}
	resp := &schemas.BifrostMCPResponse{ExtraFields: schemas.BifrostMCPResponseExtraFields{MCPRequestType: req.RequestType, ClientName: "b", Latency: 5}}
	if _, _, err := p.PostMCPHook(ctx, resp, nil); err != nil {
		t.Fatalf("PostMCPHook: %v", err)
	}

	samples := gatherMCP(t, p.registry)
	if len(samples) != 1 || samples[0].labels["mcp_client"] != "b" {
		t.Fatalf("samples = %v, want one under mcp_client b", samples)
	}
}

// BenchmarkMCPHooks measures recording one MCP call's duration: a tool call, the baseline, beside the
// prompt and resource reads now recorded too.
func BenchmarkMCPHooks(b *testing.B) {
	p, err := Init(&Config{}, nil, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		b.Fatal(err)
	}
	tool := "ctx7-query-docs"
	for _, tc := range []struct {
		name string
		req  *schemas.BifrostMCPRequest
	}{
		{"tool-call", &schemas.BifrostMCPRequest{RequestType: schemas.MCPRequestTypeChatToolCall, ClientName: "ctx7", ChatAssistantMessageToolCall: &schemas.ChatAssistantMessageToolCall{Function: schemas.ChatAssistantMessageToolCallFunction{Name: &tool}}}},
		{"prompt-get", &schemas.BifrostMCPRequest{RequestType: schemas.MCPRequestTypeGetPrompt, ClientName: "ctx7", BifrostMCPGetPromptRequest: &schemas.BifrostMCPGetPromptRequest{Name: "ctx7-review"}}},
		{"resource-read", &schemas.BifrostMCPRequest{RequestType: schemas.MCPRequestTypeReadResource, ClientName: "ctx7", BifrostMCPReadResourceRequest: &schemas.BifrostMCPReadResourceRequest{URI: "file:///handbook.md"}}},
	} {
		resp := &schemas.BifrostMCPResponse{ExtraFields: schemas.BifrostMCPResponseExtraFields{MCPRequestType: tc.req.RequestType, ClientName: "ctx7", ToolName: "query-docs", Latency: 12}}
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				ctx := mcpHookContext()
				if _, _, err := p.PreMCPHook(ctx, tc.req); err != nil {
					b.Fatal(err)
				}
				if _, _, err := p.PostMCPHook(ctx, resp, nil); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

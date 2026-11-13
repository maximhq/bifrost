package logging

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
)

// TestPostMCPHookLogsDiscoveryRankerCalls pins that the decision calls the code
// mode discovery ranker made for a listToolFiles call, which skip the plugin
// pipeline, are each written as their own decision row with the model, tokens
// and cost, while the code mode tool itself stays unlogged.
func TestPostMCPHookLogsDiscoveryRankerCalls(t *testing.T) {
	plugin := &LoggerPlugin{logger: testLogger{}, writeQueue: make(chan *writeQueueEntry, 4), pricingManager: newTestPricingManager(t)}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, "req-list-tool-files")
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-caller")
	started := time.Now().Add(-2 * time.Second)
	schemas.AppendMCPDiscoveryRankerCallOnContext(ctx, schemas.MCPDiscoveryRankerCall{
		Provider:  "openai",
		Model:     "gpt-4o-mini",
		Usage:     &schemas.BifrostLLMUsage{PromptTokens: 412, CompletionTokens: 9, TotalTokens: 421},
		StartedAt: started,
		Latency:   1500 * time.Millisecond,
	})
	schemas.AppendMCPDiscoveryRankerCallOnContext(ctx, schemas.MCPDiscoveryRankerCall{
		Provider:  "openai",
		Model:     "gpt-4o-mini",
		StartedAt: started,
		Latency:   5 * time.Second,
		Error:     &schemas.BifrostError{Error: &schemas.ErrorField{Message: "context deadline exceeded"}},
	})

	result := "servers/github/create_issue.pyi"
	resp := &schemas.BifrostMCPResponse{
		ChatMessage: &schemas.ChatMessage{Role: schemas.ChatMessageRoleTool, Content: &schemas.ChatMessageContent{ContentStr: &result}},
		ExtraFields: schemas.BifrostMCPResponseExtraFields{MCPRequestType: schemas.MCPRequestTypeChatToolCall, ToolName: "listToolFiles"},
	}
	if _, _, err := plugin.PostMCPHook(ctx, resp, nil); err != nil {
		t.Fatalf("PostMCPHook() error = %v", err)
	}

	var rows []*logstore.Log
	for len(plugin.writeQueue) > 0 {
		qe := <-plugin.writeQueue
		if qe.mcpLog != nil {
			t.Fatalf("the code mode tool call must stay unlogged, got MCP row %+v", qe.mcpLog)
		}
		rows = append(rows, qe.log)
	}
	if len(rows) != 2 {
		t.Fatalf("expected one row per decision call, got %d", len(rows))
	}
	for _, row := range rows {
		if row.Object != string(schemas.DecisionRequest) || row.Provider != "openai" || row.Model != "gpt-4o-mini" {
			t.Fatalf("unexpected row %s %s/%s", row.Object, row.Provider, row.Model)
		}
		if row.ID == "" || row.ID == "req-list-tool-files" || !row.Timestamp.Equal(started.UTC()) {
			t.Fatalf("row needs its own ID and the call's start time, got %q at %v", row.ID, row.Timestamp)
		}
		if row.MetadataParsed["mcp_request_id"] != "req-list-tool-files" || row.MetadataParsed["internal_request"] != "code_mode_discovery_ranker" {
			t.Fatalf("row does not name its tool call: %v", row.MetadataParsed)
		}
		if row.VirtualKeyID != nil || row.VirtualKeyName != nil || row.UserID != nil || row.TeamID != nil {
			t.Fatal("the row must not attribute the call to the caller")
		}
	}

	answered := rows[0]
	if answered.Status != logStatusSuccess || answered.Latency == nil || *answered.Latency != 1500 {
		t.Fatalf("unexpected answered row status %q latency %v", answered.Status, answered.Latency)
	}
	if usage := answered.TokenUsageParsed; usage == nil || usage.PromptTokens != 412 || usage.CompletionTokens != 9 || usage.TotalTokens != 421 {
		t.Fatalf("unexpected tokens %+v", answered.TokenUsageParsed)
	}
	// gpt-4o-mini has no decision rates, so the call is priced at its chat rates.
	wantCost := 412*1.5e-07 + 9*6e-07
	if answered.Cost == nil || math.Abs(*answered.Cost-wantCost) > 1e-12 {
		t.Fatalf("cost = %v, want %v", answered.Cost, wantCost)
	}

	failed := rows[1]
	if failed.Status != logStatusError || failed.ErrorDetailsParsed == nil || failed.ErrorDetailsParsed.Error.Message != "context deadline exceeded" {
		t.Fatalf("unexpected failed row status %q error %+v", failed.Status, failed.ErrorDetailsParsed)
	}
	if failed.TokenUsageParsed != nil || failed.Cost != nil {
		t.Fatalf("a call with no reported usage has no tokens or cost, got %+v %v", failed.TokenUsageParsed, failed.Cost)
	}

	if _, _, err := plugin.PostMCPHook(ctx, resp, nil); err != nil {
		t.Fatalf("PostMCPHook() error = %v", err)
	}
	if len(plugin.writeQueue) != 0 {
		t.Fatal("each decision call must be logged once")
	}
}

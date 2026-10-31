package bedrock

import (
	"context"
	"reflect"
	"strconv"
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

func cachePoint() *BedrockCachePoint {
	return &BedrockCachePoint{Type: BedrockCachePointTypeDefault}
}

// TestStripCachePoints_MessageBlocks — standalone CachePoint content blocks are
// removed; non-CachePoint blocks survive untouched.
func TestStripCachePoints_MessageBlocks(t *testing.T) {
	req := &BedrockConverseRequest{
		Messages: []BedrockMessage{
			{
				Role: BedrockMessageRoleUser,
				Content: []BedrockContentBlock{
					{Text: schemas.Ptr("hello")},
					{CachePoint: cachePoint()},
					{Text: schemas.Ptr("world")},
				},
			},
		},
	}

	stripCachePointsFromBedrockRequest(req)

	blocks := req.Messages[0].Content
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks after stripping, got %d", len(blocks))
	}
	if blocks[0].Text == nil || *blocks[0].Text != "hello" {
		t.Errorf("expected first block text='hello', got %+v", blocks[0])
	}
	if blocks[1].Text == nil || *blocks[1].Text != "world" {
		t.Errorf("expected second block text='world', got %+v", blocks[1])
	}
}

// TestStripCachePoints_NestedToolResultContent — CachePoint blocks inside a
// ToolResult's Content slice are filtered out; the tool result itself survives.
func TestStripCachePoints_NestedToolResultContent(t *testing.T) {
	req := &BedrockConverseRequest{
		Messages: []BedrockMessage{
			{
				Role: BedrockMessageRoleUser,
				Content: []BedrockContentBlock{
					{
						ToolResult: &BedrockToolResult{
							ToolUseID: "call_1",
							Content: []BedrockContentBlock{
								{Text: schemas.Ptr("result text")},
								{CachePoint: cachePoint()},
							},
						},
					},
				},
			},
		},
	}

	stripCachePointsFromBedrockRequest(req)

	inner := req.Messages[0].Content[0].ToolResult.Content
	if len(inner) != 1 {
		t.Fatalf("expected 1 inner block after stripping, got %d", len(inner))
	}
	if inner[0].Text == nil || *inner[0].Text != "result text" {
		t.Errorf("expected inner text block to survive, got %+v", inner[0])
	}
}

// TestStripCachePoints_SystemMessagesDropCachePointOnly — a system message
// that contained only a CachePoint (text==nil) is removed entirely; a real
// text system message is kept.
func TestStripCachePoints_SystemMessagesDropCachePointOnly(t *testing.T) {
	req := &BedrockConverseRequest{
		System: []BedrockSystemMessage{
			{Text: schemas.Ptr("You are helpful.")},
			{CachePoint: cachePoint()}, // cache-point-only → must be removed
			{Text: schemas.Ptr("Be concise.")},
		},
	}

	stripCachePointsFromBedrockRequest(req)

	if len(req.System) != 2 {
		t.Fatalf("expected 2 system messages after stripping, got %d", len(req.System))
	}
	if req.System[0].Text == nil || *req.System[0].Text != "You are helpful." {
		t.Errorf("first system message wrong: %+v", req.System[0])
	}
	if req.System[1].Text == nil || *req.System[1].Text != "Be concise." {
		t.Errorf("second system message wrong: %+v", req.System[1])
	}
}

// TestStripCachePoints_SystemMessageClearsCachePoint — a system message that
// has both Text and CachePoint keeps its Text and loses only the CachePoint.
func TestStripCachePoints_SystemMessageClearsCachePoint(t *testing.T) {
	req := &BedrockConverseRequest{
		System: []BedrockSystemMessage{
			{Text: schemas.Ptr("sys prompt"), CachePoint: cachePoint()},
		},
	}

	stripCachePointsFromBedrockRequest(req)

	if len(req.System) != 1 {
		t.Fatalf("expected system message to survive (has text), got %d entries", len(req.System))
	}
	if req.System[0].CachePoint != nil {
		t.Errorf("expected CachePoint to be cleared, still set: %+v", req.System[0].CachePoint)
	}
	if req.System[0].Text == nil || *req.System[0].Text != "sys prompt" {
		t.Errorf("expected text to be preserved, got %+v", req.System[0])
	}
}

// TestStripCachePoints_ToolConfigCachePoints — cache-point-only tool entries
// are removed entirely (same as system messages); real tool specs survive.
func TestStripCachePoints_ToolConfigCachePoints(t *testing.T) {
	req := &BedrockConverseRequest{
		ToolConfig: &BedrockToolConfig{
			Tools: []BedrockTool{
				{ToolSpec: &BedrockToolSpec{Name: "get_weather"}},
				{CachePoint: cachePoint()}, // cache-point-only → must be removed
			},
		},
	}

	stripCachePointsFromBedrockRequest(req)

	tools := req.ToolConfig.Tools
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool after stripping cache-point-only entry, got %d", len(tools))
	}
	if tools[0].ToolSpec == nil || tools[0].ToolSpec.Name != "get_weather" {
		t.Errorf("expected real tool to survive, got %+v", tools[0])
	}
}

// TestStripCachePoints_NilToolConfig — no panic when ToolConfig is nil.
func TestStripCachePoints_NilToolConfig(t *testing.T) {
	req := &BedrockConverseRequest{}
	// Should not panic.
	stripCachePointsFromBedrockRequest(req)
}

// TestStripCachePoints_EmptyRequest — no panic and no mutation on an empty request.
func TestStripCachePoints_EmptyRequest(t *testing.T) {
	req := &BedrockConverseRequest{}
	stripCachePointsFromBedrockRequest(req)
	if len(req.Messages) != 0 || len(req.System) != 0 {
		t.Errorf("expected empty request to remain empty, got %+v", req)
	}
}

func cachePointTTL(ttl BedrockCacheWriteTTL) *BedrockCachePoint {
	return &BedrockCachePoint{Type: BedrockCachePointTypeDefault, TTL: schemas.Ptr(string(ttl))}
}

// TestDowngradeExtendedCacheTTL — 1h cache TTLs across messages, nested tool
// results, system, and tools are dropped to default; 5m and unset TTLs are
// untouched and the cache points themselves are preserved.
func TestDowngradeExtendedCacheTTL(t *testing.T) {
	req := &BedrockConverseRequest{
		Messages: []BedrockMessage{
			{
				Role: BedrockMessageRoleUser,
				Content: []BedrockContentBlock{
					{Text: schemas.Ptr("hello"), CachePoint: cachePointTTL(BedrockCacheWriteTTL1h)},
					{Text: schemas.Ptr("world"), CachePoint: cachePointTTL(BedrockCacheWriteTTL5m)},
					{
						ToolResult: &BedrockToolResult{
							ToolUseID: "call_1",
							Content: []BedrockContentBlock{
								{Text: schemas.Ptr("tr"), CachePoint: cachePointTTL(BedrockCacheWriteTTL1h)},
							},
						},
					},
				},
			},
		},
		System:     []BedrockSystemMessage{{Text: schemas.Ptr("sys"), CachePoint: cachePointTTL(BedrockCacheWriteTTL1h)}},
		ToolConfig: &BedrockToolConfig{Tools: []BedrockTool{{ToolSpec: &BedrockToolSpec{Name: "t"}, CachePoint: cachePointTTL(BedrockCacheWriteTTL1h)}}},
	}

	downgradeExtendedCacheTTLInBedrockRequest(req)

	blocks := req.Messages[0].Content
	if cp := blocks[0].CachePoint; cp == nil || cp.TTL != nil {
		t.Errorf("expected 1h message cache point downgraded to nil TTL, got %+v", cp)
	}
	if cp := blocks[1].CachePoint; cp == nil || cp.TTL == nil || *cp.TTL != string(BedrockCacheWriteTTL5m) {
		t.Errorf("expected 5m cache point preserved, got %+v", cp)
	}
	if cp := blocks[2].ToolResult.Content[0].CachePoint; cp == nil || cp.TTL != nil {
		t.Errorf("expected nested 1h tool-result cache point downgraded, got %+v", cp)
	}
	if cp := req.System[0].CachePoint; cp == nil || cp.TTL != nil {
		t.Errorf("expected 1h system cache point downgraded, got %+v", cp)
	}
	if cp := req.ToolConfig.Tools[0].CachePoint; cp == nil || cp.TTL != nil {
		t.Errorf("expected 1h tool cache point downgraded, got %+v", cp)
	}
}

// TestDowngradeExtendedCacheTTL_NilSafe — no panic on nil tool config / cache points.
func TestDowngradeExtendedCacheTTL_NilSafe(t *testing.T) {
	req := &BedrockConverseRequest{
		Messages: []BedrockMessage{{Role: BedrockMessageRoleUser, Content: []BedrockContentBlock{{Text: schemas.Ptr("hi")}}}},
	}
	downgradeExtendedCacheTTLInBedrockRequest(req)
}

// TestBedrockModelSupportsExtendedCacheTTL — only Anthropic models support 1h TTL.
func TestBedrockModelSupportsExtendedCacheTTL(t *testing.T) {
	cases := map[string]bool{
		"anthropic.claude-sonnet-4-5": true,
		"amazon.nova-pro-v1:0":        false,
		"minimax.minimax-m2.5":        false,
	}
	for model, want := range cases {
		if got := schemas.BedrockModelSupportsExtendedCacheTTL(model); got != want {
			t.Errorf("BedrockModelSupportsExtendedCacheTTL(%q) = %v, want %v", model, got, want)
		}
	}
}

// TestToBedrockConverseStreamResponse_CopiesCacheTokens guards the streaming Converse
// path against dropping prompt-cache token counts (issue #4746). Cached tokens are folded
// into InputTokens upstream, so the converter must copy them into the cache fields AND
// subtract them back out of InputTokens.
func TestToBedrockConverseStreamResponse_CopiesCacheTokens(t *testing.T) {
	resp := &schemas.BifrostResponsesStreamResponse{
		Type: schemas.ResponsesStreamResponseTypeCompleted,
		Response: &schemas.BifrostResponsesResponse{
			Usage: &schemas.ResponsesResponseUsage{
				InputTokens:  2660, // cached tokens folded in (as FinalizeBedrockStream does)
				OutputTokens: 4,
				TotalTokens:  2664,
				InputTokensDetails: &schemas.ResponsesResponseInputTokens{
					CachedReadTokens: 2647,
				},
			},
		},
	}

	event, err := ToBedrockConverseStreamResponse(resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if event == nil || event.Usage == nil {
		t.Fatal("expected usage on completed event")
	}
	if event.Usage.CacheReadInputTokens != 2647 {
		t.Errorf("CacheReadInputTokens: want 2647, got %d", event.Usage.CacheReadInputTokens)
	}
	// Cached tokens must be subtracted so inputTokens is not double-counted.
	if event.Usage.InputTokens != 13 {
		t.Errorf("InputTokens: want 13 (cached subtracted), got %d", event.Usage.InputTokens)
	}
}

// TestBuildBedrockTokenUsage_StreamMatchesNonStream asserts the streaming and non-streaming
// Converse converters report identical usage from the same Responses usage — the drift that
// caused issue #4746. Exercises cache write + 5m/1h breakdown in addition to cache read.
func TestBuildBedrockTokenUsage_StreamMatchesNonStream(t *testing.T) {
	usage := &schemas.ResponsesResponseUsage{
		InputTokens:  2660, // cached read + write folded in
		OutputTokens: 4,
		TotalTokens:  2664,
		InputTokensDetails: &schemas.ResponsesResponseInputTokens{
			CachedReadTokens:  2000,
			CachedWriteTokens: 647,
			CachedWriteTokenDetails: &schemas.ChatCachedWriteTokenDetails{
				CachedWriteTokens5m: 600,
				CachedWriteTokens1h: 47,
			},
		},
	}

	streamResp := &schemas.BifrostResponsesStreamResponse{
		Type:     schemas.ResponsesStreamResponseTypeCompleted,
		Response: &schemas.BifrostResponsesResponse{Usage: usage},
	}
	streamEvent, err := ToBedrockConverseStreamResponse(streamResp)
	if err != nil {
		t.Fatalf("stream conversion error: %v", err)
	}

	nonStreamResp, err := ToBedrockConverseResponse(&schemas.BifrostResponsesResponse{Usage: usage})
	if err != nil {
		t.Fatalf("non-stream conversion error: %v", err)
	}

	if streamEvent.Usage == nil || nonStreamResp.Usage == nil {
		t.Fatal("expected usage on both converters")
	}
	if !reflect.DeepEqual(streamEvent.Usage, nonStreamResp.Usage) {
		t.Errorf("usage mismatch:\n  stream:     %+v\n  non-stream: %+v", *streamEvent.Usage, *nonStreamResp.Usage)
	}
	// Spot-check the expected values: 2660 - 2000 - 647 = 13.
	if streamEvent.Usage.InputTokens != 13 {
		t.Errorf("InputTokens: want 13, got %d", streamEvent.Usage.InputTokens)
	}
	if streamEvent.Usage.CacheReadInputTokens != 2000 || streamEvent.Usage.CacheWriteInputTokens != 647 {
		t.Errorf("cache tokens: want read=2000 write=647, got read=%d write=%d",
			streamEvent.Usage.CacheReadInputTokens, streamEvent.Usage.CacheWriteInputTokens)
	}
}

// TestToBedrockChatCompletionRequest_ToolCallCacheControlBecomesCachePoint — a
// cache_control on a chat tool_calls entry becomes a standalone cachePoint block
// directly after the toolUse it was placed on; unmarked siblings get none.
func TestToBedrockChatCompletionRequest_ToolCallCacheControlBecomesCachePoint(t *testing.T) {
	ttl := "1h"
	req := &schemas.BifrostChatRequest{
		Provider: schemas.Bedrock,
		Model:    "anthropic.claude-sonnet-4-6",
		Input: []schemas.ChatMessage{
			{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("weather in paris and berlin?")},
			},
			{
				Role: schemas.ChatMessageRoleAssistant,
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					ToolCalls: []schemas.ChatAssistantMessageToolCall{
						{
							ID:           schemas.Ptr("toolu_paris"),
							Type:         schemas.Ptr(string(schemas.ChatToolTypeFunction)),
							Function:     schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("get_weather"), Arguments: `{"city":"Paris"}`},
							CacheControl: &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral, TTL: &ttl},
						},
						{
							ID:       schemas.Ptr("toolu_berlin"),
							Type:     schemas.Ptr(string(schemas.ChatToolTypeFunction)),
							Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("get_weather"), Arguments: `{"city":"Berlin"}`},
						},
					},
				},
			},
			{
				Role:            schemas.ChatMessageRoleTool,
				ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr("toolu_paris")},
				Content:         &schemas.ChatMessageContent{ContentStr: schemas.Ptr("18C")},
			},
			{
				Role:            schemas.ChatMessageRoleTool,
				ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr("toolu_berlin")},
				Content:         &schemas.ChatMessageContent{ContentStr: schemas.Ptr("15C")},
			},
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	result, err := ToBedrockChatCompletionRequest(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var assistant *BedrockMessage
	for i := range result.Messages {
		if result.Messages[i].Role == BedrockMessageRoleAssistant {
			assistant = &result.Messages[i]
		}
	}
	if assistant == nil {
		t.Fatal("expected an assistant message in the converted request")
	}
	blocks := assistant.Content
	if len(blocks) != 3 {
		t.Fatalf("expected [toolUse, cachePoint, toolUse], got %d blocks: %+v", len(blocks), blocks)
	}
	if blocks[0].ToolUse == nil || blocks[0].ToolUse.ToolUseID != "toolu_paris" {
		t.Fatalf("block 0 must be the paris toolUse, got %+v", blocks[0])
	}
	if blocks[1].CachePoint == nil {
		t.Fatalf("expected a cachePoint directly after the marked toolUse, got %+v", blocks[1])
	}
	if blocks[1].CachePoint.Type != BedrockCachePointTypeDefault {
		t.Errorf("cachePoint type = %q, want %q", blocks[1].CachePoint.Type, BedrockCachePointTypeDefault)
	}
	if blocks[1].CachePoint.TTL == nil || *blocks[1].CachePoint.TTL != "1h" {
		t.Errorf("cachePoint ttl must carry the marker's 1h ttl, got %+v", blocks[1].CachePoint.TTL)
	}
	if blocks[2].ToolUse == nil || blocks[2].ToolUse.ToolUseID != "toolu_berlin" {
		t.Fatalf("block 2 must be the unmarked berlin toolUse with no cachePoint after it, got %+v", blocks[2])
	}
}
// TestToBedrockChatCompletionRequest_TopLevelCacheControlBecomesCachePoint is the Chat
// Completions twin of the Anthropic-ingress test for issue #8541: a top-level cache_control
// carried in ExtraParams becomes a cachePoint after the last cacheable block of the last message.
func TestToBedrockChatCompletionRequest_TopLevelCacheControlBecomesCachePoint(t *testing.T) {
	extra := map[string]interface{}{"cache_control": map[string]interface{}{"type": "ephemeral"}}
	req := &schemas.BifrostChatRequest{
		Provider: schemas.Bedrock,
		Model:    "eu.anthropic.claude-sonnet-5-5",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleSystem, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("You are terse.")}},
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Say OK.")}},
		},
		Params: &schemas.ChatParameters{ExtraParams: extra},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	result, err := ToBedrockChatCompletionRequest(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Messages) == 0 {
		t.Fatal("expected messages in the converted request")
	}
	last := result.Messages[len(result.Messages)-1].Content
	if len(last) < 2 || last[len(last)-1].CachePoint == nil {
		t.Fatalf("expected [text, cachePoint] on the last message, got %+v", last)
	}
	if _, ok := result.ExtraParams["cache_control"]; ok {
		t.Errorf("cache_control must not be forwarded as an unknown field, ExtraParams = %+v", result.ExtraParams)
	}
	if _, ok := req.Params.ExtraParams["cache_control"]; !ok {
		t.Errorf("the shared Bifrost request must keep cache_control for fallbacks")
	}
}

// BenchmarkToBedrockResponsesRequestTopLevelCacheControl measures the automatic-caching
// translation (issue #8541). "absent" is the common case and must not allocate more than before;
// "present" pays one ExtraParams clone and one cachePoint insert.
func BenchmarkToBedrockResponsesRequestTopLevelCacheControl(b *testing.B) {
	build := func(extra map[string]interface{}) *schemas.BifrostResponsesRequest {
		return &schemas.BifrostResponsesRequest{
			Provider: schemas.Bedrock,
			Model:    "eu.anthropic.claude-sonnet-5-5",
			Input: []schemas.ResponsesMessage{
				{Role: schemas.Ptr(schemas.ResponsesInputMessageRoleSystem), Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("You are terse.")}},
				{Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser), Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Say OK.")}},
			},
			Params: &schemas.ResponsesParameters{ExtraParams: extra},
		}
	}
	cases := []struct {
		name  string
		extra func() map[string]interface{}
	}{
		{"absent", func() map[string]interface{} { return map[string]interface{}{"requestMetadata": map[string]string{"k": "v"}} }},
		{"present", func() map[string]interface{} {
			return map[string]interface{}{"cache_control": &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral}}
		}},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				req := build(c.extra())
				b.StartTimer()
				if _, err := ToBedrockResponsesRequest(ctx, req); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// TestToBedrockChatCompletionRequest_TypedTopLevelCacheControl pins the PR #8573 review finding
// that a Go SDK caller setting the typed ChatParameters.CacheControl (with no ExtraParams) still
// gets the automatic cachePoint, and that the typed field wins when both carry a marker.
func TestToBedrockChatCompletionRequest_TypedTopLevelCacheControl(t *testing.T) {
	oneHour, fiveMin := "1h", "5m"
	build := func(params *schemas.ChatParameters) *schemas.BifrostChatRequest {
		return &schemas.BifrostChatRequest{
			Provider: schemas.Bedrock,
			Model:    "eu.anthropic.claude-sonnet-5-5",
			Input: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleSystem, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("You are terse.")}},
				{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Say OK.")}},
			},
			Params: params,
		}
	}
	lastCachePoint := func(t *testing.T, req *schemas.BifrostChatRequest) *BedrockCachePoint {
		t.Helper()
		ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
		defer cancel()
		result, err := ToBedrockChatCompletionRequest(ctx, req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		last := result.Messages[len(result.Messages)-1].Content
		if len(last) < 2 || last[len(last)-1].CachePoint == nil {
			t.Fatalf("expected [text, cachePoint] on the last message, got %+v", last)
		}
		return last[len(last)-1].CachePoint
	}

	t.Run("typed field alone", func(t *testing.T) {
		cp := lastCachePoint(t, build(&schemas.ChatParameters{
			CacheControl: &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral, TTL: &oneHour},
		}))
		if cp.TTL == nil || *cp.TTL != "1h" {
			t.Errorf("cachePoint ttl = %v, want 1h", cp.TTL)
		}
	})

	t.Run("typed field wins over ExtraParams", func(t *testing.T) {
		cp := lastCachePoint(t, build(&schemas.ChatParameters{
			CacheControl: &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral, TTL: &oneHour},
			ExtraParams:  map[string]interface{}{"cache_control": map[string]interface{}{"type": "ephemeral", "ttl": fiveMin}},
		}))
		if cp.TTL == nil || *cp.TTL != "1h" {
			t.Errorf("cachePoint ttl = %v, want the typed field's 1h", cp.TTL)
		}
	})
}

// TestTopLevelCacheControlTTLConflict pins the PR #8573 review finding on TTL conflicts: when the
// block the automatic breakpoint would land on already carries an explicit marker, a matching TTL
// is a no-op and a different TTL is a caller error (400), as on the Claude API, instead of the
// explicit TTL silently replacing the requested retention.
func TestTopLevelCacheControlTTLConflict(t *testing.T) {
	oneHour := "1h"
	convert := func(topTTL *string, input []schemas.ChatMessage) (*BedrockConverseRequest, error) {
		req := &schemas.BifrostChatRequest{
			Provider: schemas.Bedrock,
			Model:    "eu.anthropic.claude-sonnet-5-5",
			Input:    input,
			Params: &schemas.ChatParameters{
				CacheControl: &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral, TTL: topTTL},
			},
		}
		ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
		defer cancel()
		return ToBedrockChatCompletionRequest(ctx, req)
	}
	markedUser := func(ttl *string) []schemas.ChatMessage {
		return []schemas.ChatMessage{{
			Role: schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{{
				Type:         schemas.ChatContentBlockTypeText,
				Text:         schemas.Ptr("Say OK."),
				CacheControl: &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral, TTL: ttl},
			}}},
		}}
	}
	markedToolResult := func(ttl *string) []schemas.ChatMessage {
		return []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("weather in Paris?")}},
			{Role: schemas.ChatMessageRoleAssistant, ChatAssistantMessage: &schemas.ChatAssistantMessage{
				ToolCalls: []schemas.ChatAssistantMessageToolCall{{
					ID:       schemas.Ptr("toolu_paris"),
					Type:     schemas.Ptr(string(schemas.ChatToolTypeFunction)),
					Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("get_weather"), Arguments: `{"city":"Paris"}`},
				}},
			}},
			{
				Role:            schemas.ChatMessageRoleTool,
				ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr("toolu_paris")},
				Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{{
					Type:         schemas.ChatContentBlockTypeText,
					Text:         schemas.Ptr("18C"),
					CacheControl: &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral, TTL: ttl},
				}}},
			},
		}
	}
	countCachePoints := func(req *BedrockConverseRequest) int {
		n := 0
		for _, m := range req.Messages {
			for _, b := range m.Content {
				if b.CachePoint != nil {
					n++
				}
				if b.ToolResult != nil {
					for _, c := range b.ToolResult.Content {
						if c.CachePoint != nil {
							n++
						}
					}
				}
			}
		}
		return n
	}

	t.Run("explicit 5m marker on last block vs top-level 1h is a 400", func(t *testing.T) {
		_, err := convert(&oneHour, markedUser(nil))
		if err == nil || !providerUtils.IsInvalidRequestError(err) {
			t.Fatalf("expected an invalid-request error for the TTL conflict, got %v", err)
		}
	})

	t.Run("explicit 1h marker on last block vs top-level 1h is a no-op", func(t *testing.T) {
		out, err := convert(&oneHour, markedUser(&oneHour))
		if err != nil {
			t.Fatalf("matching TTLs must not error: %v", err)
		}
		if n := countCachePoints(out); n != 1 {
			t.Errorf("expected only the explicit cachePoint, got %d", n)
		}
	})

	t.Run("explicit 5m marker nested in a trailing tool result vs top-level 1h is a 400", func(t *testing.T) {
		_, err := convert(&oneHour, markedToolResult(nil))
		if err == nil || !providerUtils.IsInvalidRequestError(err) {
			t.Fatalf("expected an invalid-request error for the nested TTL conflict, got %v", err)
		}
	})

	t.Run("matching nested tool-result marker is a no-op", func(t *testing.T) {
		out, err := convert(nil, markedToolResult(nil))
		if err != nil {
			t.Fatalf("matching TTLs must not error: %v", err)
		}
		if n := countCachePoints(out); n != 1 {
			t.Errorf("expected only the explicit nested cachePoint, got %d", n)
		}
	})
}

// TestTopLevelCacheControlUnsupportedTTL pins the PR #8573 review finding on unsupported TTLs:
// an automatic marker asking for a retention Bedrock cannot honour (anything but "5m"/"1h") is a
// 400, as on the Claude API, instead of silently caching for the 5m default. Block-level markers
// keep normalizing through newBedrockCachePoint, and the supported values are unchanged.
func TestTopLevelCacheControlUnsupportedTTL(t *testing.T) {
	convert := func(top *schemas.CacheControl, blockTTL *string) (*BedrockConverseRequest, error) {
		user := schemas.ChatMessage{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Say OK.")}}
		if blockTTL != nil {
			user.Content = &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{{
				Type:         schemas.ChatContentBlockTypeText,
				Text:         schemas.Ptr("Say OK."),
				CacheControl: &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral, TTL: blockTTL},
			}}}
		}
		req := &schemas.BifrostChatRequest{
			Provider: schemas.Bedrock,
			Model:    "eu.anthropic.claude-sonnet-5-5",
			Input:    []schemas.ChatMessage{user},
			Params:   &schemas.ChatParameters{CacheControl: top},
		}
		ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
		defer cancel()
		return ToBedrockChatCompletionRequest(ctx, req)
	}
	ephemeral := func(ttl *string) *schemas.CacheControl {
		return &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral, TTL: ttl}
	}

	t.Run("top-level 2h is a 400", func(t *testing.T) {
		_, err := convert(ephemeral(schemas.Ptr("2h")), nil)
		if err == nil || !providerUtils.IsInvalidRequestError(err) {
			t.Fatalf("expected an invalid-request error for ttl 2h, got %v", err)
		}
	})

	for _, ttl := range []*string{nil, schemas.Ptr(""), schemas.Ptr("5m"), schemas.Ptr("1h")} {
		name := "absent"
		if ttl != nil {
			name = "ttl " + strconv.Quote(*ttl)
		}
		t.Run("top-level "+name+" still gets a cachePoint", func(t *testing.T) {
			out, err := convert(ephemeral(ttl), nil)
			if err != nil {
				t.Fatalf("supported ttl must not error: %v", err)
			}
			last := out.Messages[len(out.Messages)-1].Content
			if len(last) < 2 || last[len(last)-1].CachePoint == nil {
				t.Fatalf("expected a trailing cachePoint, got %+v", last)
			}
		})
	}

	t.Run("block-level 2h keeps normalizing to the default", func(t *testing.T) {
		out, err := convert(nil, schemas.Ptr("2h"))
		if err != nil {
			t.Fatalf("block-level markers must keep normalizing, got %v", err)
		}
		last := out.Messages[len(out.Messages)-1].Content
		if len(last) < 2 || last[len(last)-1].CachePoint == nil || last[len(last)-1].CachePoint.TTL != nil {
			t.Fatalf("expected a default (no-ttl) cachePoint, got %+v", last)
		}
	})
}

// TestToBedrockChatCompletionRequest_ToolResultCacheControlBecomesSiblingCachePoint — a
// cache_control on a chat tool message's content block becomes one standalone cachePoint
// directly after that toolResult. Converse has no cachePoint member inside toolResult content
// and silently ignores one placed there, so nothing may be nested.
func TestToBedrockChatCompletionRequest_ToolResultCacheControlBecomesSiblingCachePoint(t *testing.T) {
	ttl1h, ttl5m := "1h", "5m"
	req := &schemas.BifrostChatRequest{
		Provider: schemas.Bedrock,
		Model:    "anthropic.claude-sonnet-4-6",
		Input: []schemas.ChatMessage{
			{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("weather in paris and berlin?")},
			},
			{
				Role: schemas.ChatMessageRoleAssistant,
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					ToolCalls: []schemas.ChatAssistantMessageToolCall{
						{
							ID:       schemas.Ptr("toolu_paris"),
							Type:     schemas.Ptr(string(schemas.ChatToolTypeFunction)),
							Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("get_weather"), Arguments: `{"city":"Paris"}`},
						},
						{
							ID:       schemas.Ptr("toolu_berlin"),
							Type:     schemas.Ptr(string(schemas.ChatToolTypeFunction)),
							Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("get_weather"), Arguments: `{"city":"Berlin"}`},
						},
					},
				},
			},
			{
				Role:            schemas.ChatMessageRoleTool,
				ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr("toolu_paris")},
				Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
					{Type: schemas.ChatContentBlockTypeText, Text: schemas.Ptr("18C"), CacheControl: &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral, TTL: &ttl1h}},
					{Type: schemas.ChatContentBlockTypeText, Text: schemas.Ptr("sunny"), CacheControl: &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral, TTL: &ttl5m}},
				}},
			},
			{
				Role:            schemas.ChatMessageRoleTool,
				ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr("toolu_berlin")},
				Content:         &schemas.ChatMessageContent{ContentStr: schemas.Ptr("15C")},
			},
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	result, err := ToBedrockChatCompletionRequest(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	last := result.Messages[len(result.Messages)-1]
	if last.Role != BedrockMessageRoleUser {
		t.Fatalf("expected the tool results in a trailing user message, got role %q", last.Role)
	}
	blocks := last.Content
	if len(blocks) != 3 {
		t.Fatalf("expected [toolResult, cachePoint, toolResult], got %d blocks: %+v", len(blocks), blocks)
	}
	if blocks[0].ToolResult == nil || blocks[0].ToolResult.ToolUseID != "toolu_paris" {
		t.Fatalf("block 0 must be the paris toolResult, got %+v", blocks[0])
	}
	for k, inner := range blocks[0].ToolResult.Content {
		if inner.CachePoint != nil {
			t.Errorf("toolResult content[%d] carries a nested cachePoint, which Converse ignores: %+v", k, inner)
		}
	}
	if len(blocks[0].ToolResult.Content) != 2 {
		t.Errorf("both paris text blocks must survive, got %+v", blocks[0].ToolResult.Content)
	}
	if blocks[1].CachePoint == nil {
		t.Fatalf("expected a cachePoint directly after the marked toolResult, got %+v", blocks[1])
	}
	if blocks[1].CachePoint.TTL == nil || *blocks[1].CachePoint.TTL != ttl5m {
		t.Errorf("one cachePoint carrying the last marker's 5m ttl expected, got %+v", blocks[1].CachePoint.TTL)
	}
	if blocks[2].ToolResult == nil || blocks[2].ToolResult.ToolUseID != "toolu_berlin" {
		t.Fatalf("block 2 must be the unmarked berlin toolResult with no cachePoint after it, got %+v", blocks[2])
	}
}

package bedrock

import (
	"context"
	"reflect"
	"testing"

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

// --------------------------------------------------------------------------
// Inlined mid-conversation system reminders keep their cache breakpoint.
//
// role:"system" inside the messages array is a real Anthropic API feature
// (mid-conversation system messages, Opus 4.8+) that Claude Code uses for
// <system-reminder> turns. Bedrock has no message-level system role, so
// ConvertBifrostMessagesToBedrockMessages inlines those turns as user messages.
// That inlining must carry the block's CacheControl across: it is the client's
// conversation-level cache anchor, and dropping it pins the cacheable prefix at
// the system/tools floor, leaving the whole conversation body uncached.
// --------------------------------------------------------------------------

func ephemeral() *schemas.CacheControl {
	return &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral}
}

func responsesMessage(role schemas.ResponsesMessageRoleType, blocks ...schemas.ResponsesMessageContentBlock) schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		Role:    schemas.Ptr(role),
		Content: &schemas.ResponsesMessageContent{ContentBlocks: blocks},
	}
}

func responsesTextBlock(text string, cacheControl *schemas.CacheControl) schemas.ResponsesMessageContentBlock {
	return schemas.ResponsesMessageContentBlock{
		Type:         schemas.ResponsesInputMessageContentBlockTypeText,
		Text:         schemas.Ptr(text),
		CacheControl: cacheControl,
	}
}

func countMessageCachePoints(messages []BedrockMessage) int {
	total := 0
	for _, msg := range messages {
		for _, block := range msg.Content {
			if block.CachePoint != nil {
				total++
			}
		}
	}
	return total
}

func countSystemCachePoints(systemMessages []BedrockSystemMessage) int {
	total := 0
	for _, msg := range systemMessages {
		if msg.CachePoint != nil {
			total++
		}
	}
	return total
}

// claudeCodeShape mirrors what Claude Code sends: two breakpoints in the leading
// system run, then a mid-conversation reminder carrying the third,
// conversation-level breakpoint.
func claudeCodeShape(reminderRole schemas.ResponsesMessageRoleType) []schemas.ResponsesMessage {
	return []schemas.ResponsesMessage{
		responsesMessage(schemas.ResponsesInputMessageRoleSystem,
			responsesTextBlock("You are Claude Code.", ephemeral()),
			responsesTextBlock("Extra instructions.", ephemeral())),
		responsesMessage(schemas.ResponsesInputMessageRoleUser, responsesTextBlock("Analyze this.", nil)),
		responsesMessage(schemas.ResponsesInputMessageRoleAssistant, responsesTextBlock("Understood.", nil)),
		responsesMessage(reminderRole, responsesTextBlock("Keep going.", ephemeral())),
	}
}

// TestInlinedSystemReminder_KeepsCachePoint — the client sends 3 cache_control
// breakpoints, so 3 cachePoints must reach Bedrock: 2 hoisted into system, and 1
// on the inlined reminder.
func TestInlinedSystemReminder_KeepsCachePoint(t *testing.T) {
	messages, systemMessages, err := ConvertBifrostMessagesToBedrockMessages(
		context.Background(), claudeCodeShape(schemas.ResponsesInputMessageRoleSystem), true)
	if err != nil {
		t.Fatalf("conversion error: %v", err)
	}

	if got := countSystemCachePoints(systemMessages); got != 2 {
		t.Errorf("system cachePoints: want 2 (hoisted leading run), got %d", got)
	}
	if got := countMessageCachePoints(messages); got != 1 {
		t.Errorf("message cachePoints: want 1 (the inlined reminder's anchor), got %d", got)
	}
	if got := countSystemCachePoints(systemMessages) + countMessageCachePoints(messages); got != 3 {
		t.Errorf("total cachePoints: want 3 to match the 3 client breakpoints, got %d", got)
	}
}

// TestInlinedSystemReminder_CachePointFollowsText — a cachePoint terminates the
// cacheable prefix, so it must come after the text it closes over.
func TestInlinedSystemReminder_CachePointFollowsText(t *testing.T) {
	messages, _, err := ConvertBifrostMessagesToBedrockMessages(
		context.Background(), claudeCodeShape(schemas.ResponsesInputMessageRoleSystem), true)
	if err != nil {
		t.Fatalf("conversion error: %v", err)
	}

	for _, msg := range messages {
		for i, block := range msg.Content {
			if block.CachePoint == nil {
				continue
			}
			if i == 0 {
				t.Fatal("cachePoint is the first block in its message; it must follow the text it terminates")
			}
			if msg.Content[i-1].Text == nil {
				t.Errorf("block before cachePoint has no text; cachePoint must terminate a text block")
			}
		}
	}
}

// TestInlinedSystemReminder_UserRoleTailUnchanged — the equivalent user-role tail
// is the control: it already worked and must keep working identically.
func TestInlinedSystemReminder_UserRoleTailUnchanged(t *testing.T) {
	messages, systemMessages, err := ConvertBifrostMessagesToBedrockMessages(
		context.Background(), claudeCodeShape(schemas.ResponsesInputMessageRoleUser), true)
	if err != nil {
		t.Fatalf("conversion error: %v", err)
	}

	if got := countSystemCachePoints(systemMessages) + countMessageCachePoints(messages); got != 3 {
		t.Errorf("total cachePoints: want 3 for the control shape, got %d", got)
	}
}

// TestInlinedSystemReminder_MultipleBreakpointsEmitOne — only the last breakpoint
// in a single reminder is emitted, so a pathological input cannot exceed
// Bedrock's per-request checkpoint budget.
func TestInlinedSystemReminder_MultipleBreakpointsEmitOne(t *testing.T) {
	input := []schemas.ResponsesMessage{
		responsesMessage(schemas.ResponsesInputMessageRoleSystem, responsesTextBlock("You are Claude Code.", ephemeral())),
		responsesMessage(schemas.ResponsesInputMessageRoleUser, responsesTextBlock("Analyze this.", nil)),
		responsesMessage(schemas.ResponsesInputMessageRoleSystem,
			responsesTextBlock("reminder one", ephemeral()),
			responsesTextBlock("reminder two", ephemeral()),
			responsesTextBlock("reminder three", ephemeral())),
	}

	messages, _, err := ConvertBifrostMessagesToBedrockMessages(context.Background(), input, true)
	if err != nil {
		t.Fatalf("conversion error: %v", err)
	}

	if got := countMessageCachePoints(messages); got != 1 {
		t.Errorf("message cachePoints: want 1 (last breakpoint only), got %d", got)
	}
}

// TestInlinedSystemReminder_NoCacheControlNoCachePoint — a reminder carrying no
// cache_control must not gain an invented cachePoint.
func TestInlinedSystemReminder_NoCacheControlNoCachePoint(t *testing.T) {
	input := []schemas.ResponsesMessage{
		responsesMessage(schemas.ResponsesInputMessageRoleSystem, responsesTextBlock("You are Claude Code.", nil)),
		responsesMessage(schemas.ResponsesInputMessageRoleUser, responsesTextBlock("Analyze this.", nil)),
		responsesMessage(schemas.ResponsesInputMessageRoleSystem, responsesTextBlock("plain reminder", nil)),
	}

	messages, _, err := ConvertBifrostMessagesToBedrockMessages(context.Background(), input, true)
	if err != nil {
		t.Fatalf("conversion error: %v", err)
	}

	if got := countMessageCachePoints(messages); got != 0 {
		t.Errorf("message cachePoints: want 0 when the client sent no cache_control, got %d", got)
	}
}

// TestInlinedSystemReminder_PreservesOneHourTTL — a 1h breakpoint must not be
// silently downgraded to the 5m default.
func TestInlinedSystemReminder_PreservesOneHourTTL(t *testing.T) {
	input := []schemas.ResponsesMessage{
		responsesMessage(schemas.ResponsesInputMessageRoleSystem, responsesTextBlock("You are Claude Code.", nil)),
		responsesMessage(schemas.ResponsesInputMessageRoleUser, responsesTextBlock("Analyze this.", nil)),
		responsesMessage(schemas.ResponsesInputMessageRoleSystem,
			responsesTextBlock("reminder", &schemas.CacheControl{
				Type: schemas.CacheControlTypeEphemeral,
				TTL:  schemas.Ptr("1h"),
			})),
	}

	messages, _, err := ConvertBifrostMessagesToBedrockMessages(context.Background(), input, true)
	if err != nil {
		t.Fatalf("conversion error: %v", err)
	}

	for _, msg := range messages {
		for _, block := range msg.Content {
			if block.CachePoint == nil {
				continue
			}
			if block.CachePoint.TTL == nil || *block.CachePoint.TTL != "1h" {
				t.Errorf("cachePoint TTL: want \"1h\", got %v", block.CachePoint.TTL)
			}
			return
		}
	}
	t.Fatal("no cachePoint emitted inside messages")
}

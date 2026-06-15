package bedrock

import (
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

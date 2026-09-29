package bedrock

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Converse's ToolResultContentBlock union has no cachePoint member, so a cachePoint nested in
// toolResult.content is not a checkpoint (#7614). cache_control on a tool message's content must
// surface as a sibling cachePoint content block immediately after that message's toolResult.

func toolMsgWithBlocks(id string, blocks ...schemas.ChatContentBlock) schemas.ChatMessage {
	return schemas.ChatMessage{
		Role:            schemas.ChatMessageRoleTool,
		ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr(id)},
		Content:         &schemas.ChatMessageContent{ContentBlocks: blocks},
	}
}

func textContent(s string, cc *schemas.CacheControl) schemas.ChatContentBlock {
	return schemas.ChatContentBlock{Type: schemas.ChatContentBlockTypeText, Text: schemas.Ptr(s), CacheControl: cc}
}

func assertNoNestedCachePoints(t *testing.T, content []BedrockContentBlock) {
	t.Helper()
	for i, b := range content {
		if b.ToolResult == nil {
			continue
		}
		for k, inner := range b.ToolResult.Content {
			assert.Nil(t, inner.CachePoint, "content[%d].toolResult.content[%d] must not carry a cachePoint", i, k)
		}
	}
}

func TestConvertToolMessages_CacheControlBecomesSiblingCachePoint(t *testing.T) {
	ephemeral := &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral}
	oneHour := &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral, TTL: schemas.Ptr("1h")}

	t.Run("single tool message", func(t *testing.T) {
		msg, err := convertToolMessages(context.Background(), "anthropic.claude-sonnet-4-5-20250929-v1:0",
			[]schemas.ChatMessage{toolMsgWithBlocks("call_1", textContent("large tool output", ephemeral))})
		require.NoError(t, err)
		require.Len(t, msg.Content, 2, "[toolResult, cachePoint]")
		require.NotNil(t, msg.Content[0].ToolResult)
		require.Len(t, msg.Content[0].ToolResult.Content, 1)
		assert.Equal(t, "large tool output", *msg.Content[0].ToolResult.Content[0].Text)
		require.NotNil(t, msg.Content[1].CachePoint)
		assert.Equal(t, BedrockCachePointTypeDefault, msg.Content[1].CachePoint.Type)
		assert.Nil(t, msg.Content[1].CachePoint.TTL)
		assertNoNestedCachePoints(t, msg.Content)

		raw, err := json.Marshal(msg)
		require.NoError(t, err)
		assert.JSONEq(t,
			`{"role":"user","content":[{"toolResult":{"toolUseId":"call_1","content":[{"text":"large tool output"}],"status":"success"}},{"cachePoint":{"type":"default"}}]}`,
			string(raw))
	})

	t.Run("consecutive tool messages keep one marker each at their own position", func(t *testing.T) {
		msg, err := convertToolMessages(context.Background(), "anthropic.claude-sonnet-4-5-20250929-v1:0",
			[]schemas.ChatMessage{
				toolMsgWithBlocks("call_1", textContent("first", ephemeral)),
				toolMsgWithBlocks("call_2", textContent("second", nil)),
				toolMsgWithBlocks("call_3", textContent("third", oneHour)),
			})
		require.NoError(t, err)
		require.Len(t, msg.Content, 5, "[tr1, cp, tr2, tr3, cp]")
		assert.Equal(t, "call_1", msg.Content[0].ToolResult.ToolUseID)
		assert.NotNil(t, msg.Content[1].CachePoint)
		assert.Equal(t, "call_2", msg.Content[2].ToolResult.ToolUseID)
		assert.Equal(t, "call_3", msg.Content[3].ToolResult.ToolUseID)
		require.NotNil(t, msg.Content[4].CachePoint)
		require.NotNil(t, msg.Content[4].CachePoint.TTL)
		assert.Equal(t, "1h", *msg.Content[4].CachePoint.TTL)
		assertNoNestedCachePoints(t, msg.Content)
	})

	t.Run("several marked blocks in one tool message collapse to one marker", func(t *testing.T) {
		msg, err := convertToolMessages(context.Background(), "anthropic.claude-sonnet-4-5-20250929-v1:0",
			[]schemas.ChatMessage{toolMsgWithBlocks("call_1",
				textContent("a", oneHour),
				textContent("b", ephemeral),
			)})
		require.NoError(t, err)
		require.Len(t, msg.Content, 2)
		require.Len(t, msg.Content[0].ToolResult.Content, 2)
		require.NotNil(t, msg.Content[1].CachePoint)
		assert.Nil(t, msg.Content[1].CachePoint.TTL, "the last marked block's TTL wins")
		assertNoNestedCachePoints(t, msg.Content)
	})

	t.Run("no cache_control emits no marker", func(t *testing.T) {
		msg, err := convertToolMessages(context.Background(), "anthropic.claude-sonnet-4-5-20250929-v1:0",
			[]schemas.ChatMessage{toolMsgWithBlocks("call_1", textContent("plain", nil))})
		require.NoError(t, err)
		require.Len(t, msg.Content, 1)
		assert.NotNil(t, msg.Content[0].ToolResult)
	})
}

// End to end through ToBedrockChatCompletionRequest: the sibling marker survives the
// post-conversion passes and is counted once by the clamp.
func TestToBedrockChatCompletionRequest_ToolResultCacheControlSibling(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := &schemas.BifrostChatRequest{
		Model: "anthropic.claude-sonnet-4-5-20250929-v1:0",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("list files")}},
			{
				Role: schemas.ChatMessageRoleAssistant,
				ChatAssistantMessage: &schemas.ChatAssistantMessage{
					ToolCalls: []schemas.ChatAssistantMessageToolCall{{
						ID:   schemas.Ptr("call_1"),
						Type: schemas.Ptr(string(schemas.ChatToolChoiceTypeFunction)),
						Function: schemas.ChatAssistantMessageToolCallFunction{
							Name:      schemas.Ptr("bash"),
							Arguments: `{"command":"ls"}`,
						},
					}},
				},
			},
			toolMsgWithBlocks("call_1", textContent("build/ src/", &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral})),
		},
	}

	bedrockReq, err := ToBedrockChatCompletionRequest(ctx, req)
	require.NoError(t, err)

	last := bedrockReq.Messages[len(bedrockReq.Messages)-1]
	require.Equal(t, BedrockMessageRoleUser, last.Role)
	require.Len(t, last.Content, 2)
	require.NotNil(t, last.Content[0].ToolResult)
	require.NotNil(t, last.Content[1].CachePoint, "cachePoint must be a sibling after toolResult")
	assertNoNestedCachePoints(t, last.Content)

	_, _, messages, total := countCachePointsInRequest(bedrockReq)
	assert.Equal(t, 1, messages)
	assert.Equal(t, 1, total)
}

// A marked image block in a tool result (e.g. a screenshot) must also surface as a sibling
// cachePoint. For models that reject images inside a toolResult the images are hoisted to
// follow the last toolResult, and the marker must still come after them so it caches the images.
func TestConvertToolMessages_ImageBlockMarker_SiblingAfterHoist(t *testing.T) {
	const model = "us.anthropic.claude-sonnet-5"
	buildReq := func() *schemas.BifrostChatRequest {
		return &schemas.BifrostChatRequest{
			Provider: schemas.Bedrock,
			Model:    model,
			Input: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("dump the page")}},
				{
					Role: schemas.ChatMessageRoleAssistant,
					ChatAssistantMessage: &schemas.ChatAssistantMessage{
						ToolCalls: []schemas.ChatAssistantMessageToolCall{{
							ID:   schemas.Ptr("call_1"),
							Type: schemas.Ptr(string(schemas.ChatToolChoiceTypeFunction)),
							Function: schemas.ChatAssistantMessageToolCallFunction{
								Name:      schemas.Ptr("get_page"),
								Arguments: `{}`,
							},
						}},
					},
				},
				toolMsgWithBlocks("call_1",
					textContent("page dump", nil),
					schemas.ChatContentBlock{
						Type:           schemas.ChatContentBlockTypeImage,
						ImageURLStruct: &schemas.ChatInputImage{URL: toolResultImageDataURL},
						CacheControl:   &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral},
					},
				),
			},
		}
	}

	tests := []struct {
		name       string
		caps       *schemas.ModelCapabilities
		wantHoists bool
	}{
		// Force the hoisted layout so the ordering guarantee is exercised on this model.
		{name: "hoisted", caps: &schemas.ModelCapabilities{SupportsConverseToolResultImages: schemas.Ptr(false)}, wantHoists: true},
		{name: "nested", wantHoists: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.caps != nil {
				schemas.SetCapabilityResolver(func(schemas.ModelProvider, string) *schemas.ModelCapabilities { return tt.caps })
				t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })
			}
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			bedrockReq, err := ToBedrockChatCompletionRequest(ctx, buildReq())
			require.NoError(t, err)

			last := bedrockReq.Messages[len(bedrockReq.Messages)-1]
			require.Equal(t, BedrockMessageRoleUser, last.Role)
			layout := describeBedrockBlocks(last.Content)

			final := last.Content[len(last.Content)-1]
			require.NotNil(t, final.CachePoint, "final block must be the sibling cachePoint: %v", layout)
			assert.Equal(t, BedrockCachePointTypeDefault, final.CachePoint.Type)
			assertNoNestedCachePoints(t, last.Content)

			cpIdx := len(last.Content) - 1
			hoisted := 0
			for i, b := range last.Content {
				if b.Image != nil {
					hoisted++
					assert.Less(t, i, cpIdx, "hoisted image must precede the cachePoint: %v", layout)
				}
			}
			if tt.wantHoists {
				assert.Equal(t, 1, hoisted, "image must be hoisted out of the toolResult: %v", layout)
			} else {
				assert.Zero(t, hoisted, "image stays nested in the toolResult: %v", layout)
			}

			_, _, messages, total := countCachePointsInRequest(bedrockReq)
			assert.Equal(t, 1, messages)
			assert.Equal(t, 1, total)
		})
	}
}

// Hoisting must preserve a checkpoint's coverage when more tool results follow it.
func TestHoistToolResultImages_PreservesIntermediateCacheCoverage(t *testing.T) {
	oneHour := "1h"
	checkpoint := func(ttl *string) BedrockContentBlock {
		return BedrockContentBlock{CachePoint: newBedrockCachePoint(ttl)}
	}
	text := func(s string) BedrockContentBlock { return BedrockContentBlock{Text: &s} }
	tests := []struct {
		name    string
		content []BedrockContentBlock
		want    []string
		ttls    []string
	}{
		{
			name:    "marked screenshot followed by unmarked text result",
			content: []BedrockContentBlock{toolResultBlock("a", text("first"), toolResultImageBlock("red")), checkpoint(&oneHour), toolResultBlock("b", text("second"))},
			want:    []string{"toolResult(a)[text:first]", "toolResult(b)[text:second]", "image:red", "cachePoint"},
			ttls:    []string{"1h"},
		},
		{
			name:    "separate image checkpoints preserve order and TTL",
			content: []BedrockContentBlock{toolResultBlock("a", text("first"), toolResultImageBlock("red")), checkpoint(&oneHour), toolResultBlock("b", text("second"), toolResultImageBlock("blue")), checkpoint(nil)},
			want:    []string{"toolResult(a)[text:first]", "toolResult(b)[text:second]", "image:red", "cachePoint", "image:blue", "cachePoint"},
			ttls:    []string{"1h", ""},
		},
		{
			name:    "earlier text checkpoint is not moved over a later image",
			content: []BedrockContentBlock{toolResultBlock("a", text("first")), checkpoint(&oneHour), toolResultBlock("b", text("second"), toolResultImageBlock("blue")), checkpoint(nil)},
			want:    []string{"toolResult(a)[text:first]", "cachePoint", "toolResult(b)[text:second]", "image:blue", "cachePoint"},
			ttls:    []string{"1h", ""},
		},
		{
			name:    "trailing text remains covered by trailing checkpoint",
			content: []BedrockContentBlock{toolResultBlock("a", text("first"), toolResultImageBlock("red")), checkpoint(&oneHour), toolResultBlock("b", text("second")), text("tail"), checkpoint(nil)},
			want:    []string{"toolResult(a)[text:first]", "toolResult(b)[text:second]", "image:red", "cachePoint", "text:tail", "cachePoint"},
			ttls:    []string{"1h", ""},
		},
		{
			name:    "multiple intermediate checkpoints after one image",
			content: []BedrockContentBlock{toolResultBlock("a", text("first"), toolResultImageBlock("red")), checkpoint(&oneHour), toolResultBlock("b", text("second")), checkpoint(nil), toolResultBlock("c", text("third"))},
			want:    []string{"toolResult(a)[text:first]", "toolResult(b)[text:second]", "toolResult(c)[text:third]", "image:red", "cachePoint"},
			ttls:    []string{""},
		},
		{
			name:    "moved checkpoint next to a trailing checkpoint collapses to the later one",
			content: []BedrockContentBlock{toolResultBlock("a", text("first"), toolResultImageBlock("red")), checkpoint(&oneHour), toolResultBlock("b", text("second")), checkpoint(nil)},
			want:    []string{"toolResult(a)[text:first]", "toolResult(b)[text:second]", "image:red", "cachePoint"},
			ttls:    []string{""},
		},
		{
			name:    "text-only results keep checkpoint positions",
			content: []BedrockContentBlock{toolResultBlock("a", text("first")), checkpoint(&oneHour), toolResultBlock("b", text("second")), checkpoint(nil)},
			want:    []string{"toolResult(a)[text:first]", "cachePoint", "toolResult(b)[text:second]", "cachePoint"},
			ttls:    []string{"1h", ""},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := make([]BedrockContentBlock, len(tt.content), len(tt.content)+8)
			copy(content, tt.content)
			req := &BedrockConverseRequest{Messages: []BedrockMessage{{Role: BedrockMessageRoleUser, Content: content}}}
			hoistToolResultImages(req)
			assert.Equal(t, tt.want, describeBedrockBlocks(req.Messages[0].Content))
			var gotTTLs []string
			for _, b := range req.Messages[0].Content {
				if b.CachePoint != nil {
					ttl := ""
					if b.CachePoint.TTL != nil {
						ttl = *b.CachePoint.TTL
					}
					gotTTLs = append(gotTTLs, ttl)
				}
				if b.ToolResult != nil {
					for _, inner := range b.ToolResult.Content {
						assert.Nil(t, inner.Image)
					}
				}
			}
			assert.Equal(t, tt.ttls, gotTTLs)
			hoistToolResultImages(req)
			assert.Equal(t, tt.want, describeBedrockBlocks(req.Messages[0].Content), "a second pass must not change the layout")
		})
	}
}

func TestToBedrockChatCompletionRequest_ToolCacheCapabilityPasses(t *testing.T) {
	tests := []struct {
		name              string
		cache, extended   bool
		count, wantPoints int
	}{
		{"hoisted checkpoints retain extended TTL", true, true, 2, 2},
		{"unsupported TTL is downgraded", true, false, 2, 2},
		{"unsupported caching strips all checkpoints", false, false, 2, 0},
		{"clamp keeps the four latest image checkpoints", true, true, 6, 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			caps := &schemas.ModelCapabilities{
				SupportsConverseToolResultImages: schemas.Ptr(false),
				SupportsCachePoint:               &tt.cache,
				SupportsExtendedCacheTTL:         &tt.extended,
			}
			schemas.SetCapabilityResolver(func(schemas.ModelProvider, string) *schemas.ModelCapabilities { return caps })
			t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })
			req := &schemas.BifrostChatRequest{Provider: schemas.Bedrock, Model: "us.anthropic.claude-sonnet-5"}
			assistant := schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, ChatAssistantMessage: &schemas.ChatAssistantMessage{}}
			var results []schemas.ChatMessage
			for i := 0; i < tt.count; i++ {
				id := fmt.Sprintf("call_%d", i)
				assistant.ToolCalls = append(assistant.ToolCalls, schemas.ChatAssistantMessageToolCall{
					ID: &id, Type: schemas.Ptr("function"),
					Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("get_page"), Arguments: `{}`},
				})
				results = append(results, toolMsgWithBlocks(id, schemas.ChatContentBlock{
					Type:           schemas.ChatContentBlockTypeImage,
					ImageURLStruct: &schemas.ChatInputImage{URL: toolResultImageDataURL},
					CacheControl:   &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral, TTL: schemas.Ptr("1h")},
				}))
			}
			req.Input = append([]schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("inspect pages")}},
				assistant,
			}, results...)
			got, err := ToBedrockChatCompletionRequest(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), req)
			require.NoError(t, err)
			// Verify the serialized request, not just the intermediate Go structs.
			raw, err := json.Marshal(got)
			require.NoError(t, err)
			var wire BedrockConverseRequest
			require.NoError(t, json.Unmarshal(raw, &wire))
			last := wire.Messages[len(wire.Messages)-1].Content
			assertNoNestedCachePoints(t, last)
			var want []string
			for i := 0; i < tt.count; i++ {
				want = append(want, fmt.Sprintf("toolResult(call_%d)[text:%s]", i, toolResultImagePlaceholder))
			}
			for i := 0; i < tt.count; i++ {
				want = append(want, "image:png")
				if tt.cache && i >= tt.count-tt.wantPoints {
					want = append(want, "cachePoint")
				}
			}
			assert.Equal(t, want, describeBedrockBlocks(last))
			_, _, _, total := countCachePointsInRequest(&wire)
			assert.Equal(t, tt.wantPoints, total)
			for _, b := range last {
				if b.CachePoint == nil {
					continue
				}
				if tt.extended {
					require.NotNil(t, b.CachePoint.TTL)
					assert.Equal(t, "1h", *b.CachePoint.TTL)
				} else {
					assert.Nil(t, b.CachePoint.TTL)
				}
			}
		})
	}
}

// Two adjacent checkpoints mark the same prefix and waste one of the four slots.
func TestToBedrockChatCompletionRequest_HoistCollapsesAdjacentCheckpoints(t *testing.T) {
	caps := &schemas.ModelCapabilities{
		SupportsConverseToolResultImages: schemas.Ptr(false),
		SupportsCachePoint:               schemas.Ptr(true),
		SupportsExtendedCacheTTL:         schemas.Ptr(true),
	}
	schemas.SetCapabilityResolver(func(schemas.ModelProvider, string) *schemas.ModelCapabilities { return caps })
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })
	ids := []string{"call_0", "call_1"}
	assistant := schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, ChatAssistantMessage: &schemas.ChatAssistantMessage{}}
	for i := range ids {
		assistant.ToolCalls = append(assistant.ToolCalls, schemas.ChatAssistantMessageToolCall{
			ID: &ids[i], Type: schemas.Ptr("function"),
			Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("get_page"), Arguments: `{}`},
		})
	}
	req := &schemas.BifrostChatRequest{Provider: schemas.Bedrock, Model: "us.anthropic.claude-sonnet-5"}
	req.Input = []schemas.ChatMessage{
		{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("inspect pages")}},
		assistant,
		toolMsgWithBlocks(ids[0], schemas.ChatContentBlock{
			Type:           schemas.ChatContentBlockTypeImage,
			ImageURLStruct: &schemas.ChatInputImage{URL: toolResultImageDataURL},
			CacheControl:   &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral},
		}),
		toolMsgWithBlocks(ids[1], textContent("second", &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral, TTL: schemas.Ptr("1h")})),
	}
	got, err := ToBedrockChatCompletionRequest(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), req)
	require.NoError(t, err)
	last := got.Messages[len(got.Messages)-1].Content
	layout := describeBedrockBlocks(last)
	assert.Equal(t, []string{
		fmt.Sprintf("toolResult(call_0)[text:%s]", toolResultImagePlaceholder),
		"toolResult(call_1)[text:second]",
		"image:png",
		"cachePoint",
	}, layout)
	require.NotNil(t, last[len(last)-1].CachePoint)
	require.NotNil(t, last[len(last)-1].CachePoint.TTL, "the later checkpoint's TTL wins: %v", layout)
	assert.Equal(t, "1h", *last[len(last)-1].CachePoint.TTL)
	_, _, messages, total := countCachePointsInRequest(got)
	assert.Equal(t, 1, messages)
	assert.Equal(t, 1, total)
}

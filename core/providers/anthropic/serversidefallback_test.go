package anthropic

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

// --- usage.iterations[].model ---

func TestUsageIterationsModel_RoundTrip(t *testing.T) {
	t.Parallel()

	anthropicUsage := &AnthropicUsage{
		Type:         schemas.Ptr("message"),
		Model:        schemas.Ptr("claude-opus-4-8"),
		InputTokens:  412,
		OutputTokens: 264,
		Iterations: []AnthropicUsage{
			{Type: schemas.Ptr("message"), Model: schemas.Ptr("claude-fable-5"), InputTokens: 535, OutputTokens: 0},
			{Type: schemas.Ptr("fallback_message"), Model: schemas.Ptr("claude-opus-4-8"), InputTokens: 412, OutputTokens: 264},
		},
	}

	// Anthropic -> Bifrost
	bifrostUsage := ConvertAnthropicUsageToBifrostUsage(anthropicUsage)
	if bifrostUsage.Model == nil || *bifrostUsage.Model != "claude-opus-4-8" {
		t.Fatalf("top-level model = %v, want claude-opus-4-8", bifrostUsage.Model)
	}
	if len(bifrostUsage.Iterations) != 2 {
		t.Fatalf("expected 2 iterations, got %d", len(bifrostUsage.Iterations))
	}
	if m := bifrostUsage.Iterations[0].Model; m == nil || *m != "claude-fable-5" {
		t.Errorf("iteration[0].model = %v, want claude-fable-5", m)
	}
	if m := bifrostUsage.Iterations[1].Model; m == nil || *m != "claude-opus-4-8" {
		t.Errorf("iteration[1].model = %v, want claude-opus-4-8", m)
	}

	// Bifrost -> Anthropic
	back := ConvertBifrostUsageToAnthropicUsage(bifrostUsage)
	if back.Model == nil || *back.Model != "claude-opus-4-8" {
		t.Errorf("round-trip top-level model = %v, want claude-opus-4-8", back.Model)
	}
	if len(back.Iterations) != 2 {
		t.Fatalf("round-trip expected 2 iterations, got %d", len(back.Iterations))
	}
	if m := back.Iterations[0].Model; m == nil || *m != "claude-fable-5" {
		t.Errorf("round-trip iteration[0].model = %v, want claude-fable-5", m)
	}
}

// --- isFallbackItem ---

func TestIsFallbackItem(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		item     *schemas.ResponsesMessage
		expected bool
	}{
		{name: "nil item", item: nil, expected: false},
		{
			name: "message with fallback block",
			item: &schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{
						{Type: schemas.ResponsesOutputMessageContentTypeFallback},
					},
				},
			},
			expected: true,
		},
		{
			name: "message with text block",
			item: &schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{
						{Type: schemas.ResponsesOutputMessageContentTypeText, Text: schemas.Ptr("hi")},
					},
				},
			},
			expected: false,
		},
		{
			name:     "message with nil content",
			item:     &schemas.ResponsesMessage{Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage)},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isFallbackItem(tt.item); got != tt.expected {
				t.Errorf("isFallbackItem() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// --- Non-Streaming: fallback content block round-trip ---

func TestFallbackContentBlock_NonStreamingRoundTrip(t *testing.T) {
	t.Parallel()

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	anthropicResp := &AnthropicMessageResponse{
		ID:         "msg_fallback_test",
		Type:       "message",
		Role:       "assistant",
		Model:      "claude-opus-4-8",
		StopReason: AnthropicStopReasonEndTurn,
		Content: []AnthropicContentBlock{
			{
				Type: AnthropicContentBlockTypeFallback,
				From: &AnthropicFallbackModel{Model: "claude-fable-5"},
				To:   &AnthropicFallbackModel{Model: "claude-opus-4-8"},
			},
			{Type: AnthropicContentBlockTypeText, Text: schemas.Ptr("Hi! How can I help you today?")},
		},
	}

	// Step 1: Anthropic -> Bifrost
	bifrostResp := anthropicResp.ToBifrostResponsesResponse(ctx)

	var fb *schemas.ResponsesOutputMessageContentFallback
	for _, msg := range bifrostResp.Output {
		if msg.Content == nil {
			continue
		}
		for _, block := range msg.Content.ContentBlocks {
			if block.Type == schemas.ResponsesOutputMessageContentTypeFallback {
				fb = block.ResponsesOutputMessageContentFallback
			}
		}
	}
	if fb == nil {
		t.Fatal("fallback block not found in Bifrost output")
	}
	if fb.FromModel != "claude-fable-5" || fb.ToModel != "claude-opus-4-8" {
		t.Fatalf("fallback from/to = %q/%q, want claude-fable-5/claude-opus-4-8", fb.FromModel, fb.ToModel)
	}

	// Step 2: Bifrost -> Anthropic
	result := ToAnthropicResponsesResponse(ctx, bifrostResp)

	var found bool
	for _, block := range result.Content {
		if block.Type == AnthropicContentBlockTypeFallback {
			found = true
			if block.From == nil || block.From.Model != "claude-fable-5" {
				t.Errorf("result from = %v, want claude-fable-5", block.From)
			}
			if block.To == nil || block.To.Model != "claude-opus-4-8" {
				t.Errorf("result to = %v, want claude-opus-4-8", block.To)
			}
		}
	}
	if !found {
		t.Error("fallback block not found in Anthropic result")
	}
}

// --- Streaming: Anthropic -> Bifrost (inbound) ---

func newFallbackStreamState() *AnthropicResponsesStreamState {
	return &AnthropicResponsesStreamState{
		ContentIndexToOutputIndex: make(map[int]int),
		ContentIndexToBlockType:   make(map[int]AnthropicContentBlockType),
		ToolArgumentBuffers:       make(map[int]string),
		MCPCallOutputIndices:      make(map[int]bool),
		ItemIDs:                   make(map[int]string),
		OutputItems:               make(map[int]*schemas.ResponsesMessage),
		ReasoningSignatures:       make(map[int]string),
		TextContentIndices:        make(map[int]bool),
		ReasoningContentIndices:   make(map[int]bool),
		CompactionContentIndices:  make(map[int]*schemas.CacheControl),
		CurrentOutputIndex:        0,
		CreatedAt:                 1234567890,
		HasEmittedCreated:         true,
		HasEmittedInProgress:      true,
	}
}

func TestToBifrostResponsesStream_FallbackContentBlockStart(t *testing.T) {
	t.Parallel()

	state := newFallbackStreamState()

	chunk := &AnthropicStreamEvent{
		Type:  AnthropicStreamEventTypeContentBlockStart,
		Index: schemas.Ptr(0),
		ContentBlock: &AnthropicContentBlock{
			Type: AnthropicContentBlockTypeFallback,
			From: &AnthropicFallbackModel{Model: "claude-fable-5"},
			To:   &AnthropicFallbackModel{Model: "claude-opus-4-8"},
		},
	}

	responses, err, isLast := chunk.ToBifrostResponsesStream(context.Background(), 0, state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isLast {
		t.Error("should not be last chunk")
	}
	// Fallback has no deltas: added + done are both emitted here.
	if len(responses) != 2 {
		t.Fatalf("expected 2 responses (added+done), got %d", len(responses))
	}
	if responses[0].Type != schemas.ResponsesStreamResponseTypeOutputItemAdded {
		t.Errorf("response[0] = %v, want output_item.added", responses[0].Type)
	}
	if responses[1].Type != schemas.ResponsesStreamResponseTypeOutputItemDone {
		t.Errorf("response[1] = %v, want output_item.done", responses[1].Type)
	}
	added := responses[0]
	if added.Item == nil || added.Item.Content == nil || len(added.Item.Content.ContentBlocks) == 0 {
		t.Fatal("output_item.added should have content blocks")
	}
	block := added.Item.Content.ContentBlocks[0]
	if block.Type != schemas.ResponsesOutputMessageContentTypeFallback {
		t.Fatalf("content block type = %v, want fallback", block.Type)
	}
	if block.ResponsesOutputMessageContentFallback == nil ||
		block.ResponsesOutputMessageContentFallback.FromModel != "claude-fable-5" ||
		block.ResponsesOutputMessageContentFallback.ToModel != "claude-opus-4-8" {
		t.Errorf("fallback content = %+v, want from claude-fable-5 to claude-opus-4-8", block.ResponsesOutputMessageContentFallback)
	}
	if bt, ok := state.ContentIndexToBlockType[0]; !ok || bt != AnthropicContentBlockTypeFallback {
		t.Error("expected fallback block type tracked in ContentIndexToBlockType")
	}
}

func TestToBifrostResponsesStream_FallbackContentBlockStop(t *testing.T) {
	t.Parallel()

	state := newFallbackStreamState()
	state.ContentIndexToBlockType[0] = AnthropicContentBlockTypeFallback
	state.ContentIndexToOutputIndex[0] = 0
	state.ItemIDs[0] = "fb_0"
	state.CurrentOutputIndex = 1

	chunk := &AnthropicStreamEvent{
		Type:  AnthropicStreamEventTypeContentBlockStop,
		Index: schemas.Ptr(0),
	}

	responses, err, isLast := chunk.ToBifrostResponsesStream(context.Background(), 0, state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isLast {
		t.Error("should not be last chunk")
	}
	// added+done already emitted at content_block_start; stop yields nothing.
	if len(responses) != 0 {
		t.Errorf("expected 0 responses for fallback content_block_stop, got %d", len(responses))
	}
}

// --- Streaming: Bifrost -> Anthropic (outbound) ---

func fallbackStreamItem() *schemas.ResponsesMessage {
	return &schemas.ResponsesMessage{
		ID:     schemas.Ptr("fb_test123"),
		Type:   schemas.Ptr(schemas.ResponsesMessageTypeMessage),
		Status: schemas.Ptr("completed"),
		Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
		Content: &schemas.ResponsesMessageContent{
			ContentBlocks: []schemas.ResponsesMessageContentBlock{
				{
					Type: schemas.ResponsesOutputMessageContentTypeFallback,
					ResponsesOutputMessageContentFallback: &schemas.ResponsesOutputMessageContentFallback{
						FromModel: "claude-fable-5",
						ToModel:   "claude-opus-4-8",
					},
				},
			},
		},
	}
}

func TestToAnthropicResponsesStreamResponse_FallbackOutputItemAdded(t *testing.T) {
	t.Parallel()

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	bifrostResp := &schemas.BifrostResponsesStreamResponse{
		Type:        schemas.ResponsesStreamResponseTypeOutputItemAdded,
		OutputIndex: schemas.Ptr(0),
		Item:        fallbackStreamItem(),
	}

	events := ToAnthropicResponsesStreamResponse(ctx, bifrostResp)
	if len(events) == 0 {
		t.Fatal("expected at least 1 event")
	}
	start := events[0]
	if start.Type != AnthropicStreamEventTypeContentBlockStart {
		t.Errorf("event[0] type = %v, want content_block_start", start.Type)
	}
	if start.ContentBlock == nil || start.ContentBlock.Type != AnthropicContentBlockTypeFallback {
		t.Fatalf("ContentBlock = %+v, want fallback", start.ContentBlock)
	}
	if start.ContentBlock.From == nil || start.ContentBlock.From.Model != "claude-fable-5" {
		t.Errorf("from = %v, want claude-fable-5", start.ContentBlock.From)
	}
	if start.ContentBlock.To == nil || start.ContentBlock.To.Model != "claude-opus-4-8" {
		t.Errorf("to = %v, want claude-opus-4-8", start.ContentBlock.To)
	}
}

func TestToAnthropicResponsesStreamResponse_FallbackOutputItemDone(t *testing.T) {
	t.Parallel()

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	// Prime the stream state so the item resolves to a block index (mirror the added event first).
	added := &schemas.BifrostResponsesStreamResponse{
		Type:        schemas.ResponsesStreamResponseTypeOutputItemAdded,
		OutputIndex: schemas.Ptr(0),
		Item:        fallbackStreamItem(),
	}
	ToAnthropicResponsesStreamResponse(ctx, added)

	done := &schemas.BifrostResponsesStreamResponse{
		Type:        schemas.ResponsesStreamResponseTypeOutputItemDone,
		OutputIndex: schemas.Ptr(0),
		ItemID:      schemas.Ptr("fb_test123"),
		Item:        fallbackStreamItem(),
	}
	events := ToAnthropicResponsesStreamResponse(ctx, done)
	if len(events) != 1 {
		t.Fatalf("expected 1 event for output_item.done, got %d", len(events))
	}
	if events[0].Type != AnthropicStreamEventTypeContentBlockStop {
		t.Errorf("event type = %v, want content_block_stop", events[0].Type)
	}
}

// --- Replay: fallback content blocks gated per provider ---

func fallbackHistoryRequest() *AnthropicMessageRequest {
	return &AnthropicMessageRequest{
		Model:     "claude-opus-4-8",
		MaxTokens: 64,
		Messages: []AnthropicMessage{{
			Role: AnthropicMessageRoleAssistant,
			Content: AnthropicContent{ContentBlocks: []AnthropicContentBlock{
				{
					Type: AnthropicContentBlockTypeFallback,
					From: &AnthropicFallbackModel{Model: "claude-fable-5"},
					To:   &AnthropicFallbackModel{Model: "claude-opus-4-8"},
				},
				{Type: AnthropicContentBlockTypeText, Text: schemas.Ptr("Hi there")},
			}},
		}},
	}
}

func countFallbackBlocks(req *AnthropicMessageRequest) int {
	n := 0
	for _, m := range req.Messages {
		for _, b := range m.Content.ContentBlocks {
			if b.Type == AnthropicContentBlockTypeFallback {
				n++
			}
		}
	}
	return n
}

func TestStripUnsupportedAnthropicFields_FallbackBlockReplay(t *testing.T) {
	t.Parallel()

	t.Run("anthropic keeps the fallback block in place", func(t *testing.T) {
		req := fallbackHistoryRequest()
		stripUnsupportedAnthropicFields(req, schemas.Anthropic, "claude-opus-4-8")
		if got := countFallbackBlocks(req); got != 1 {
			t.Fatalf("expected fallback block preserved for Anthropic, got %d", got)
		}
		// Position matters on Anthropic - it must stay the first block.
		if req.Messages[0].Content.ContentBlocks[0].Type != AnthropicContentBlockTypeFallback {
			t.Error("fallback block moved from its original position")
		}
	})

	for _, p := range []schemas.ModelProvider{schemas.Vertex, schemas.Bedrock, schemas.BedrockMantle, schemas.Azure} {
		t.Run(string(p)+" strips the fallback block", func(t *testing.T) {
			req := fallbackHistoryRequest()
			stripUnsupportedAnthropicFields(req, p, "claude-opus-4-8")
			if got := countFallbackBlocks(req); got != 0 {
				t.Fatalf("expected fallback block stripped for %s, got %d", p, got)
			}
			// The surrounding conversation must survive.
			blocks := req.Messages[0].Content.ContentBlocks
			if len(blocks) != 1 || blocks[0].Type != AnthropicContentBlockTypeText {
				t.Fatalf("expected the text block to survive, got %+v", blocks)
			}
		})
	}
}

func TestStripUnsupportedFieldsFromRawBody_FallbackBlockReplay(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"model":"claude-opus-4-8","max_tokens":64,"messages":[{"role":"assistant","content":[{"type":"fallback","from":{"model":"claude-fable-5"},"to":{"model":"claude-opus-4-8"}},{"type":"text","text":"Hi there"}]}]}`)

	t.Run("anthropic keeps it", func(t *testing.T) {
		out, err := StripUnsupportedFieldsFromRawBody(raw, schemas.Anthropic, "claude-opus-4-8")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !gjson.GetBytes(out, `messages.0.content.#(type=="fallback")`).Exists() {
			t.Errorf("expected fallback block kept for Anthropic, got: %s", out)
		}
	})

	for _, p := range []schemas.ModelProvider{schemas.Vertex, schemas.Bedrock, schemas.BedrockMantle} {
		t.Run(string(p)+" strips it", func(t *testing.T) {
			out, err := StripUnsupportedFieldsFromRawBody(raw, p, "claude-opus-4-8")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gjson.GetBytes(out, `messages.0.content.#(type=="fallback")`).Exists() {
				t.Errorf("expected fallback block stripped for %s, got: %s", p, out)
			}
			if !gjson.GetBytes(out, `messages.0.content.#(type=="text")`).Exists() {
				t.Errorf("expected the text block to survive for %s, got: %s", p, out)
			}
		})
	}
}

// --- Provider gating: native fallbacks stripped on unsupported providers ---

func TestStripBifrostFallbacksFromBody_ProviderGating(t *testing.T) {
	t.Parallel()

	nativeBody := []byte(`{"model":"claude-fable-5","fallbacks":[{"model":"claude-opus-4-8"}]}`)

	tests := []struct {
		name       string
		provider   schemas.ModelProvider
		keepNative bool
	}{
		{name: "anthropic keeps native", provider: schemas.Anthropic, keepNative: true},
		{name: "vertex strips native", provider: schemas.Vertex, keepNative: false},
		{name: "bedrock strips native", provider: schemas.Bedrock, keepNative: false},
		{name: "bedrock mantle strips native", provider: schemas.BedrockMantle, keepNative: false},
		{name: "azure strips native", provider: schemas.Azure, keepNative: false},
		{name: "unknown provider keeps native", provider: schemas.ModelProvider("custom-x"), keepNative: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := stripBifrostFallbacksFromBody(nativeBody, tt.provider)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			exists := gjson.GetBytes(out, "fallbacks").Exists()
			if exists != tt.keepNative {
				t.Errorf("fallbacks present = %v, want %v (body: %s)", exists, tt.keepNative, out)
			}
		})
	}

	t.Run("bifrost string fallbacks always stripped", func(t *testing.T) {
		stringBody := []byte(`{"model":"claude-fable-5","fallbacks":["openai/gpt-4o"]}`)
		for _, p := range []schemas.ModelProvider{schemas.Anthropic, schemas.Vertex} {
			out, err := stripBifrostFallbacksFromBody(stringBody, p)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gjson.GetBytes(out, "fallbacks").Exists() {
				t.Errorf("expected string fallbacks stripped for %s, got: %s", p, out)
			}
		}
	})
}

func TestBuildAnthropicResponsesRequestBody_StripsNativeFallbacksOnVertex(t *testing.T) {
	t.Parallel()

	rawBody := []byte(`{"model":"claude-fable-5","max_tokens":1024,"messages":[{"role":"user","content":"hi"}],"fallbacks":[{"model":"claude-opus-4-8"}]}`)
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)

	request := &schemas.BifrostResponsesRequest{
		Provider:       schemas.Vertex,
		Model:          "claude-fable-5",
		RawRequestBody: rawBody,
	}
	result, bifrostErr := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
		Provider: schemas.Vertex,
	})
	if bifrostErr != nil {
		t.Fatalf("unexpected error: %v", bifrostErr)
	}
	if gjson.GetBytes(result, "fallbacks").Exists() {
		t.Errorf("expected native fallbacks stripped for Vertex, got: %s", result)
	}
	// And the beta header must not be injected for an unsupported provider.
	extraHeaders, _ := ctx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string)
	if slices.Contains(extraHeaders[AnthropicBetaHeader], AnthropicServerSideFallbackBetaHeader) {
		t.Errorf("did not expect server-side-fallback beta header on Vertex, got %v", extraHeaders[AnthropicBetaHeader])
	}
}

// --- Chat path: native fallbacks promotion + beta header ---

func TestToAnthropicChatRequest_PromotesNativeFallbacks(t *testing.T) {
	t.Parallel()

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	// Realistic wire form: fallbacks arrive as decoded JSON in ExtraParams.
	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-fable-5",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")},
		}},
		Params: &schemas.ChatParameters{
			ExtraParams: map[string]interface{}{
				"fallbacks": []interface{}{map[string]interface{}{"model": "claude-opus-4-8"}},
			},
		},
	}

	result, err := ToAnthropicChatRequest(ctx, bifrostReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	native := result.nativeFallbacks()
	if len(native) != 1 || native[0].Model != "claude-opus-4-8" {
		t.Fatalf("expected native fallback claude-opus-4-8 promoted to Fallbacks, got %+v", native)
	}
	if _, exists := result.ExtraParams["fallbacks"]; exists {
		t.Error("expected fallbacks removed from ExtraParams after promotion")
	}
}

func TestBuildAnthropicChatRequestBody_NativeFallbacksInjectsBetaHeader(t *testing.T) {
	t.Parallel()

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-fable-5",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")},
		}},
		Params: &schemas.ChatParameters{
			ExtraParams: map[string]interface{}{
				"fallbacks": []interface{}{map[string]interface{}{"model": "claude-opus-4-8"}},
			},
		},
	}

	result, bifrostErr := BuildAnthropicChatRequestBody(ctx, bifrostReq, AnthropicRequestBuildConfig{
		Provider: schemas.Anthropic,
	})
	if bifrostErr != nil {
		t.Fatalf("unexpected error: %v", bifrostErr)
	}

	fb := gjson.GetBytes(result, "fallbacks")
	if !fb.IsArray() || len(fb.Array()) != 1 || fb.Array()[0].Get("model").String() != "claude-opus-4-8" {
		t.Errorf("expected native fallbacks in chat body, got: %s", fb.Raw)
	}
	extraHeaders, _ := ctx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string)
	if !slices.Contains(extraHeaders[AnthropicBetaHeader], AnthropicServerSideFallbackBetaHeader) {
		t.Errorf("expected server-side-fallback beta header injected on chat path, got %v", extraHeaders[AnthropicBetaHeader])
	}
}

// --- stop_details ---

func TestStopDetails_NonStreamingRoundTrip(t *testing.T) {
	t.Parallel()

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	anthropicResp := &AnthropicMessageResponse{
		ID:         "msg_refusal",
		Type:       "message",
		Role:       "assistant",
		Model:      "claude-fable-5",
		StopReason: AnthropicStopReasonRefusal,
		StopDetails: &AnthropicStopDetails{
			Type:             "refusal",
			Category:         schemas.Ptr("cyber"),
			Explanation:      schemas.Ptr("This request was declined because it could enable cyber harm."),
			RecommendedModel: schemas.Ptr("claude-opus-4-8"),
		},
		Content: []AnthropicContentBlock{},
	}

	// Anthropic -> Bifrost
	bifrostResp := anthropicResp.ToBifrostResponsesResponse(ctx)
	if bifrostResp.StopDetails == nil {
		t.Fatal("expected StopDetails to be preserved on Bifrost response")
	}
	if bifrostResp.StopDetails.Type != "refusal" ||
		bifrostResp.StopDetails.Category == nil || *bifrostResp.StopDetails.Category != "cyber" ||
		bifrostResp.StopDetails.RecommendedModel == nil || *bifrostResp.StopDetails.RecommendedModel != "claude-opus-4-8" {
		t.Fatalf("unexpected StopDetails: %+v", bifrostResp.StopDetails)
	}

	// Bifrost -> Anthropic
	result := ToAnthropicResponsesResponse(ctx, bifrostResp)
	if result.StopDetails == nil {
		t.Fatal("expected StopDetails to be re-emitted on Anthropic response")
	}
	if result.StopDetails.Category == nil || *result.StopDetails.Category != "cyber" ||
		result.StopDetails.Explanation == nil ||
		result.StopDetails.RecommendedModel == nil || *result.StopDetails.RecommendedModel != "claude-opus-4-8" {
		t.Errorf("unexpected round-trip StopDetails: %+v", result.StopDetails)
	}
}

func TestStopDetails_AbsentOnNormalStop(t *testing.T) {
	t.Parallel()

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	anthropicResp := &AnthropicMessageResponse{
		ID:         "msg_ok",
		Type:       "message",
		Role:       "assistant",
		Model:      "claude-fable-5",
		StopReason: AnthropicStopReasonEndTurn,
		Content:    []AnthropicContentBlock{{Type: AnthropicContentBlockTypeText, Text: schemas.Ptr("hi")}},
	}

	bifrostResp := anthropicResp.ToBifrostResponsesResponse(ctx)
	if bifrostResp.StopDetails != nil {
		t.Errorf("expected nil StopDetails on end_turn, got %+v", bifrostResp.StopDetails)
	}
	if result := ToAnthropicResponsesResponse(ctx, bifrostResp); result.StopDetails != nil {
		t.Errorf("expected nil StopDetails re-emitted, got %+v", result.StopDetails)
	}
}

func TestToBifrostResponsesStream_MessageDeltaStopDetails(t *testing.T) {
	t.Parallel()

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	ctx.SetValue(schemas.BifrostContextKeyIntegrationType, "anthropic")

	state := newFallbackStreamState()
	state.Model = schemas.Ptr("claude-fable-5")

	chunk := &AnthropicStreamEvent{
		Type: AnthropicStreamEventTypeMessageDelta,
		Delta: &AnthropicStreamDelta{
			StopReason: schemas.Ptr(AnthropicStopReasonRefusal),
			StopDetails: &AnthropicStopDetails{
				Type:     "refusal",
				Category: schemas.Ptr("bio"),
			},
		},
		Usage: &AnthropicUsage{InputTokens: 412, OutputTokens: 0},
	}

	responses, err, _ := chunk.ToBifrostResponsesStream(ctx, 0, state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(responses) != 1 || responses[0].Response == nil {
		t.Fatalf("expected 1 message_delta response with a Response, got %d", len(responses))
	}
	sd := responses[0].Response.StopDetails
	if sd == nil || sd.Type != "refusal" || sd.Category == nil || *sd.Category != "bio" {
		t.Fatalf("unexpected StopDetails on stream response: %+v", sd)
	}
}

func TestToAnthropicResponsesStreamResponse_CompletedWithStopDetails(t *testing.T) {
	t.Parallel()

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	bifrostResp := &schemas.BifrostResponsesStreamResponse{
		Type: schemas.ResponsesStreamResponseTypeCompleted,
		Response: &schemas.BifrostResponsesResponse{
			ID:         schemas.Ptr("resp_refusal"),
			Model:      "claude-fable-5",
			StopReason: schemas.Ptr("refusal"),
			StopDetails: &schemas.ResponsesStopDetails{
				Type:        "refusal",
				Explanation: schemas.Ptr("declined"),
			},
			Usage: &schemas.ResponsesResponseUsage{InputTokens: 412, OutputTokens: 0},
		},
	}

	events := ToAnthropicResponsesStreamResponse(ctx, bifrostResp)
	if len(events) != 2 {
		t.Fatalf("expected 2 events (message_delta + message_stop), got %d", len(events))
	}
	delta := events[0]
	if delta.Type != AnthropicStreamEventTypeMessageDelta || delta.Delta == nil {
		t.Fatalf("event[0] = %+v, want message_delta with Delta", delta)
	}
	if delta.Delta.StopDetails == nil || delta.Delta.StopDetails.Type != "refusal" ||
		delta.Delta.StopDetails.Explanation == nil || *delta.Delta.StopDetails.Explanation != "declined" {
		t.Errorf("unexpected message_delta StopDetails: %+v", delta.Delta.StopDetails)
	}
}

// --- Fallback credit (fallback-credit-2026-06-01 / -09 on AWS) ---

func TestFallbackCredit_StopDetailsRoundTrip(t *testing.T) {
	t.Parallel()

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	resp := &AnthropicMessageResponse{
		ID:         "msg_credit",
		Type:       "message",
		Role:       "assistant",
		Model:      "claude-fable-5",
		Content:    []AnthropicContentBlock{},
		StopReason: AnthropicStopReasonRefusal,
		StopDetails: &AnthropicStopDetails{
			Type:                    "refusal",
			Category:                schemas.Ptr("cyber"),
			Explanation:             schemas.Ptr("declined"),
			FallbackCreditToken:     schemas.Ptr("tok_opaque_123"),
			FallbackHasPrefillClaim: schemas.Ptr(true),
		},
		Usage: &AnthropicUsage{InputTokens: 412, OutputTokens: 0},
	}

	bifrostResp := resp.ToBifrostResponsesResponse(ctx)
	sd := bifrostResp.StopDetails
	if sd == nil {
		t.Fatal("expected StopDetails on the neutral response")
	}
	if sd.FallbackCreditToken == nil || *sd.FallbackCreditToken != "tok_opaque_123" {
		t.Errorf("FallbackCreditToken = %v, want tok_opaque_123", sd.FallbackCreditToken)
	}
	if sd.FallbackHasPrefillClaim == nil || !*sd.FallbackHasPrefillClaim {
		t.Errorf("FallbackHasPrefillClaim = %v, want true", sd.FallbackHasPrefillClaim)
	}

	// ...and back out to the Anthropic wire form unchanged.
	back := stopDetailsToAnthropic(sd)
	if back.FallbackCreditToken == nil || *back.FallbackCreditToken != "tok_opaque_123" {
		t.Errorf("round-tripped token = %v, want tok_opaque_123", back.FallbackCreditToken)
	}
	if back.FallbackHasPrefillClaim == nil || !*back.FallbackHasPrefillClaim {
		t.Errorf("round-tripped prefill claim = %v, want true", back.FallbackHasPrefillClaim)
	}
}

// A false prefill claim must survive as false, not collapse into "absent" —
// the two select different retry body shapes.
func TestFallbackCredit_PrefillClaimFalsePreserved(t *testing.T) {
	t.Parallel()

	sd := stopDetailsToBifrost(&AnthropicStopDetails{
		Type:                    "refusal",
		FallbackCreditToken:     schemas.Ptr("tok"),
		FallbackHasPrefillClaim: schemas.Ptr(false),
	})
	if sd.FallbackHasPrefillClaim == nil {
		t.Fatal("prefill claim false was dropped; callers would read it as unknown")
	}
	if *sd.FallbackHasPrefillClaim {
		t.Error("prefill claim flipped to true")
	}
}

func TestFallbackCredit_RequestRoundTrip_Responses(t *testing.T) {
	t.Parallel()

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	req := &AnthropicMessageRequest{
		Model:               "claude-opus-4-8",
		MaxTokens:           1024,
		Messages:            []AnthropicMessage{{Role: "user", Content: AnthropicContent{ContentStr: schemas.Ptr("hi")}}},
		FallbackCreditToken: schemas.Ptr("tok_opaque_123"),
	}

	bifrostReq := req.ToBifrostResponsesRequest(ctx)
	if got := bifrostReq.Params.ExtraParams["fallback_credit_token"]; got != "tok_opaque_123" {
		t.Fatalf("ExtraParams[fallback_credit_token] = %v, want tok_opaque_123", got)
	}

	back, err := ToAnthropicResponsesRequest(ctx, bifrostReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if back.FallbackCreditToken == nil || *back.FallbackCreditToken != "tok_opaque_123" {
		t.Fatalf("FallbackCreditToken = %v, want tok_opaque_123", back.FallbackCreditToken)
	}
	if _, exists := back.ExtraParams["fallback_credit_token"]; exists {
		t.Error("expected fallback_credit_token removed from ExtraParams after promotion")
	}
}

func TestFallbackCredit_RequestRoundTrip_Chat(t *testing.T) {
	t.Parallel()

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	bifrostReq := &schemas.BifrostChatRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-opus-4-8",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")},
		}},
		Params: &schemas.ChatParameters{
			ExtraParams: map[string]interface{}{"fallback_credit_token": "tok_opaque_123"},
		},
	}

	result, err := ToAnthropicChatRequest(ctx, bifrostReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.FallbackCreditToken == nil || *result.FallbackCreditToken != "tok_opaque_123" {
		t.Fatalf("FallbackCreditToken = %v, want tok_opaque_123", result.FallbackCreditToken)
	}
	if _, exists := result.ExtraParams["fallback_credit_token"]; exists {
		t.Error("expected fallback_credit_token removed from ExtraParams after promotion")
	}
}

// The header is injected from the token's presence, and gated per provider.
// Unlike server-side fallback, fallback credit is supported nearly everywhere.
func TestFallbackCredit_BetaHeaderInjectionGating(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		provider schemas.ModelProvider
		want     bool
	}{
		{schemas.Anthropic, true},
		{schemas.Vertex, true},
		{schemas.Bedrock, true},
		{schemas.BedrockMantle, true},
		{schemas.Azure, true},
		{schemas.DeepSeek, false},
	} {
		ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
		req := &AnthropicMessageRequest{
			Model:               "claude-opus-4-8",
			MaxTokens:           16,
			Messages:            []AnthropicMessage{{Role: "user", Content: AnthropicContent{ContentStr: schemas.Ptr("hi")}}},
			FallbackCreditToken: schemas.Ptr("tok"),
		}
		if err := AddMissingBetaHeadersToContext(ctx, req, tc.provider); err != nil {
			cancel()
			t.Fatalf("%s: unexpected error: %v", tc.provider, err)
		}
		extraHeaders, _ := ctx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string)
		got := slices.Contains(extraHeaders[AnthropicBetaHeader], AnthropicFallbackCreditBetaHeader)
		if got != tc.want {
			t.Errorf("%s: fallback-credit beta header present = %v, want %v (headers: %v)",
				tc.provider, got, tc.want, extraHeaders[AnthropicBetaHeader])
		}
		cancel()
	}
}

// AWS surfaces ship the same feature under a later date; the canonical header
// must be rewritten rather than dropped or forwarded verbatim.
func TestFilterBetaHeadersForProvider_FallbackCreditVersionRewrite(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		provider schemas.ModelProvider
		want     []string
	}{
		{schemas.Anthropic, []string{AnthropicFallbackCreditBetaHeader}},
		{schemas.Vertex, []string{AnthropicFallbackCreditBetaHeader}},
		{schemas.Azure, []string{AnthropicFallbackCreditBetaHeader}},
		{schemas.Bedrock, []string{AnthropicFallbackCreditBetaHeaderAWS}},
		{schemas.BedrockMantle, []string{AnthropicFallbackCreditBetaHeaderAWS}},
		{schemas.DeepSeek, []string{}},
	} {
		got := FilterBetaHeadersForProvider([]string{AnthropicFallbackCreditBetaHeader}, tc.provider)
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: filtered = %v, want %v", tc.provider, got, tc.want)
		}
	}
}

// The rewrite is prefix-driven, so an inbound AWS-dated header sent to the
// Claude API is normalised back to the canonical date rather than duplicated.
func TestFilterBetaHeadersForProvider_FallbackCreditRewriteIsBidirectional(t *testing.T) {
	t.Parallel()

	got := FilterBetaHeadersForProvider([]string{AnthropicFallbackCreditBetaHeaderAWS}, schemas.Bedrock)
	if !slices.Equal(got, []string{AnthropicFallbackCreditBetaHeaderAWS}) {
		t.Errorf("Bedrock: filtered = %v, want the AWS date unchanged", got)
	}
	// Anthropic keeps whatever date arrived — only providers in the rewrite table
	// are remapped, so a caller pinning a specific date on the Claude API is honoured.
	got = FilterBetaHeadersForProvider([]string{AnthropicFallbackCreditBetaHeaderAWS}, schemas.Anthropic)
	if !slices.Equal(got, []string{AnthropicFallbackCreditBetaHeaderAWS}) {
		t.Errorf("Anthropic: filtered = %v, want the inbound date preserved", got)
	}
}

func TestStripFallbackCreditToken_UnsupportedProvider(t *testing.T) {
	t.Parallel()

	newReq := func() *AnthropicMessageRequest {
		return &AnthropicMessageRequest{
			Model:               "claude-opus-4-8",
			MaxTokens:           16,
			Messages:            []AnthropicMessage{{Role: "user", Content: AnthropicContent{ContentStr: schemas.Ptr("hi")}}},
			FallbackCreditToken: schemas.Ptr("tok"),
		}
	}

	t.Run("typed kept on a supported provider", func(t *testing.T) {
		req := newReq()
		stripUnsupportedAnthropicFields(req, schemas.Bedrock, "claude-opus-4-8")
		if req.FallbackCreditToken == nil {
			t.Error("expected fallback_credit_token kept on Bedrock")
		}
	})

	t.Run("typed stripped on an unsupported provider", func(t *testing.T) {
		req := newReq()
		stripUnsupportedAnthropicFields(req, schemas.DeepSeek, "claude-opus-4-8")
		if req.FallbackCreditToken != nil {
			t.Error("expected fallback_credit_token stripped on DeepSeek")
		}
	})

	raw := []byte(`{"model":"claude-opus-4-8","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"fallback_credit_token":"tok"}`)

	t.Run("raw kept on a supported provider", func(t *testing.T) {
		out, err := StripUnsupportedFieldsFromRawBody(raw, schemas.BedrockMantle, "claude-opus-4-8")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !gjson.GetBytes(out, "fallback_credit_token").Exists() {
			t.Errorf("expected token kept on bedrock_mantle, got: %s", out)
		}
	})

	t.Run("raw stripped on an unsupported provider", func(t *testing.T) {
		out, err := StripUnsupportedFieldsFromRawBody(raw, schemas.DeepSeek, "claude-opus-4-8")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if gjson.GetBytes(out, "fallback_credit_token").Exists() {
			t.Errorf("expected token stripped on DeepSeek, got: %s", out)
		}
		if !gjson.GetBytes(out, "messages").Exists() {
			t.Errorf("strip damaged the body: %s", out)
		}
	})
}

func TestBuildAnthropicResponsesRequestBody_CountTokensStripsFallbackCreditToken(t *testing.T) {
	t.Parallel()

	rawBody := []byte(`{"model":"claude-opus-4-8","max_tokens":1024,"messages":[{"role":"user","content":"hi"}],"fallback_credit_token":"tok"}`)
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)

	request := &schemas.BifrostResponsesRequest{
		Provider:       schemas.Anthropic,
		Model:          "claude-opus-4-8",
		RawRequestBody: rawBody,
	}
	result, bifrostErr := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
		Provider:      schemas.Anthropic,
		Model:         "claude-opus-4-8",
		IsCountTokens: true,
	})
	if bifrostErr != nil {
		t.Fatalf("unexpected error: %v", bifrostErr)
	}
	if gjson.GetBytes(result, "fallback_credit_token").Exists() {
		t.Errorf("count_tokens rejects fallback_credit_token; expected it stripped, got: %s", result)
	}
}

// --- fallback block "trigger" (live-response field, absent from the docs page) ---

func TestFallbackBlockTrigger_RoundTrip(t *testing.T) {
	t.Parallel()

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	resp := &AnthropicMessageResponse{
		ID:    "msg_011CdD6baVeAMUq34gFq1huR",
		Type:  "message",
		Role:  "assistant",
		Model: "claude-opus-4-8",
		Content: []AnthropicContentBlock{
			{
				Type:    AnthropicContentBlockTypeFallback,
				From:    &AnthropicFallbackModel{Model: "claude-fable-5"},
				To:      &AnthropicFallbackModel{Model: "claude-opus-4-8"},
				Trigger: &AnthropicFallbackTrigger{Type: "refusal", Category: schemas.Ptr("cyber")},
			},
			{Type: AnthropicContentBlockTypeText, Text: schemas.Ptr("I can't help with this.")},
		},
		StopReason: AnthropicStopReasonEndTurn,
		Usage:      &AnthropicUsage{InputTokens: 31, OutputTokens: 257},
	}

	bifrostResp := resp.ToBifrostResponsesResponse(ctx)

	var fb *schemas.ResponsesOutputMessageContentFallback
	for _, item := range bifrostResp.Output {
		if item.Content == nil {
			continue
		}
		for _, b := range item.Content.ContentBlocks {
			if b.Type == schemas.ResponsesOutputMessageContentTypeFallback {
				fb = b.ResponsesOutputMessageContentFallback
			}
		}
	}
	if fb == nil {
		t.Fatal("fallback block did not survive into the neutral response")
	}
	if fb.FromModel != "claude-fable-5" || fb.ToModel != "claude-opus-4-8" {
		t.Errorf("from/to = %q -> %q, want claude-fable-5 -> claude-opus-4-8", fb.FromModel, fb.ToModel)
	}
	if fb.TriggerType != "refusal" {
		t.Errorf("TriggerType = %q, want refusal", fb.TriggerType)
	}
	if fb.TriggerCategory == nil || *fb.TriggerCategory != "cyber" {
		t.Errorf("TriggerCategory = %v, want cyber", fb.TriggerCategory)
	}
}

// A fallback block with no trigger must not emit "trigger":{"type":""}.
func TestFallbackBlockTrigger_OmittedWhenAbsent(t *testing.T) {
	t.Parallel()

	block := convertContentBlockToAnthropic(schemas.ResponsesMessageContentBlock{
		Type: schemas.ResponsesOutputMessageContentTypeFallback,
		ResponsesOutputMessageContentFallback: &schemas.ResponsesOutputMessageContentFallback{
			FromModel: "claude-fable-5",
			ToModel:   "claude-opus-4-8",
		},
	})
	if block == nil {
		t.Fatal("expected a fallback block")
	}
	if block.Trigger != nil {
		t.Errorf("expected no trigger emitted, got %+v", block.Trigger)
	}
}

// --- RoutingInfo.ServerSideFallbackModel population (drives cost) ---

func TestServerSideFallbackModel_FromIterations(t *testing.T) {
	t.Parallel()

	usage := &AnthropicUsage{
		InputTokens: 31, OutputTokens: 257,
		Iterations: []AnthropicUsage{
			{Type: schemas.Ptr("message"), Model: schemas.Ptr("claude-fable-5"), InputTokens: 31, OutputTokens: 1},
			{Type: schemas.Ptr(AnthropicUsageIterationTypeFallbackMessage), Model: schemas.Ptr("claude-opus-4-8"), InputTokens: 31, OutputTokens: 257},
		},
	}
	got := usage.ServerSideFallbackModel()
	if got == nil || *got != "claude-opus-4-8" {
		t.Errorf("ServerSideFallbackModel() = %v, want claude-opus-4-8", got)
	}

	// Sticky routing: the requested model is never tried, so there is only the
	// serving entry and no declining one.
	sticky := &AnthropicUsage{Iterations: []AnthropicUsage{
		{Type: schemas.Ptr(AnthropicUsageIterationTypeFallbackMessage), Model: schemas.Ptr("claude-opus-4-8")},
	}}
	if got := sticky.ServerSideFallbackModel(); got == nil || *got != "claude-opus-4-8" {
		t.Errorf("sticky ServerSideFallbackModel() = %v, want claude-opus-4-8", got)
	}

	// Ordinary responses carry no iterations and must stay nil so pricing is
	// unchanged for the overwhelming majority of traffic.
	if got := (&AnthropicUsage{InputTokens: 31, OutputTokens: 257}).ServerSideFallbackModel(); got != nil {
		t.Errorf("expected nil on a response with no iterations, got %v", got)
	}
	// Compaction also uses iterations, but has no fallback_message entry.
	compaction := &AnthropicUsage{Iterations: []AnthropicUsage{
		{Type: schemas.Ptr("compaction"), InputTokens: 1000},
		{Type: schemas.Ptr("message"), Model: schemas.Ptr("claude-opus-4-8")},
	}}
	if got := compaction.ServerSideFallbackModel(); got != nil {
		t.Errorf("expected nil for compaction iterations, got %v", got)
	}
	if got := (*AnthropicUsage)(nil).ServerSideFallbackModel(); got != nil {
		t.Errorf("expected nil for nil usage, got %v", got)
	}
}

func TestServerSideFallbackModel_OnResponsesAndChatResponses(t *testing.T) {
	t.Parallel()

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	usage := &AnthropicUsage{
		InputTokens: 31, OutputTokens: 257,
		Iterations: []AnthropicUsage{
			{Type: schemas.Ptr("message"), Model: schemas.Ptr("claude-fable-5"), InputTokens: 31, OutputTokens: 1},
			{Type: schemas.Ptr(AnthropicUsageIterationTypeFallbackMessage), Model: schemas.Ptr("claude-opus-4-8"), InputTokens: 31, OutputTokens: 257},
		},
	}
	resp := &AnthropicMessageResponse{
		ID: "msg_fb", Type: "message", Role: "assistant", Model: "claude-opus-4-8",
		Content:    []AnthropicContentBlock{{Type: AnthropicContentBlockTypeText, Text: schemas.Ptr("hi")}},
		StopReason: AnthropicStopReasonEndTurn,
		Usage:      usage,
	}

	got := resp.ToBifrostResponsesResponse(ctx).ExtraFields.RoutingInfo.ServerSideFallbackModel
	if got == nil || *got != "claude-opus-4-8" {
		t.Errorf("responses path: ServerSideFallbackModel = %v, want claude-opus-4-8", got)
	}

	// The chat path drops iterations from the neutral usage, so this must be
	// captured from the wire response before that happens.
	got = resp.ToBifrostChatResponse(ctx).ExtraFields.RoutingInfo.ServerSideFallbackModel
	if got == nil || *got != "claude-opus-4-8" {
		t.Errorf("chat path: ServerSideFallbackModel = %v, want claude-opus-4-8", got)
	}
}

// PopulateRoutingInfo overwrites RoutingInfo wholesale; the provider-owned
// serving model must survive that, or streaming loses it entirely.
func TestServerSideFallbackModel_SurvivesPopulateRoutingInfo(t *testing.T) {
	t.Parallel()

	resp := &schemas.BifrostResponse{
		ResponsesResponse: &schemas.BifrostResponsesResponse{
			ExtraFields: schemas.BifrostResponseExtraFields{
				RoutingInfo: schemas.RoutingInfo{ServerSideFallbackModel: schemas.Ptr("claude-opus-4-8")},
			},
		},
	}

	// Core's snapshot predates the handoff and carries no serving model.
	resp.PopulateRoutingInfo(schemas.RoutingInfo{Provider: schemas.Anthropic, Model: "claude-fable-5"})

	got := resp.ResponsesResponse.ExtraFields.RoutingInfo.ServerSideFallbackModel
	if got == nil || *got != "claude-opus-4-8" {
		t.Fatalf("serving model was clobbered by PopulateRoutingInfo: %v", got)
	}
	if resp.ResponsesResponse.ExtraFields.RoutingInfo.Model != "claude-fable-5" {
		t.Error("expected the rest of RoutingInfo to come from core's snapshot")
	}
}

// Chat streaming has no chunk to stamp at the converter (its message_delta case
// returns nil), so HandleAnthropicChatCompletionStreaming latches the serving
// model off the usage event and applies it to the final chunk. This pins the
// contract that latch depends on: the exact stream events Anthropic sends.
func TestServerSideFallbackModel_FromStreamUsageEvents(t *testing.T) {
	t.Parallel()

	// message_start carries usage but never iterations — must not latch anything.
	start := &AnthropicStreamEvent{
		Type: AnthropicStreamEventTypeMessageStart,
		Message: &AnthropicMessageResponse{
			Model: "claude-opus-4-8",
			Usage: &AnthropicUsage{InputTokens: 29, OutputTokens: 1},
		},
	}
	if got := start.Message.Usage.ServerSideFallbackModel(); got != nil {
		t.Errorf("message_start must not yield a serving model, got %v", got)
	}

	// The final message_delta is the only event carrying iterations.
	delta := &AnthropicStreamEvent{
		Type: AnthropicStreamEventTypeMessageDelta,
		Usage: &AnthropicUsage{
			InputTokens: 29, OutputTokens: 354,
			Iterations: []AnthropicUsage{
				{Type: schemas.Ptr("message"), Model: schemas.Ptr("claude-fable-5"), InputTokens: 29, OutputTokens: 7},
				{Type: schemas.Ptr(AnthropicUsageIterationTypeFallbackMessage), Model: schemas.Ptr("claude-opus-4-8"), InputTokens: 29, OutputTokens: 354},
			},
		},
	}
	got := delta.Usage.ServerSideFallbackModel()
	if got == nil || *got != "claude-opus-4-8" {
		t.Fatalf("message_delta ServerSideFallbackModel = %v, want claude-opus-4-8", got)
	}
}

// Every BetaFallbackParam field must survive the union type's custom
// UnmarshalJSON — anything unmodelled is silently dropped on the typed path,
// so the fallback attempt would run without the override the caller asked for.
func TestNativeFallbackEntry_AllFieldsRoundTrip(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
		"model": "claude-fable-5",
		"max_tokens": 1024,
		"fallbacks": [{
			"model": "claude-opus-4-8",
			"max_tokens": 2048,
			"speed": "fast",
			"thinking": {"type": "enabled", "budget_tokens": 512},
			"output_config": {"effort": "high"}
		}],
		"messages": [{"role": "user", "content": "hi"}]
	}`)

	var req AnthropicMessageRequest
	if err := sonic.Unmarshal(raw, &req); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	native := req.nativeFallbacks()
	if len(native) != 1 {
		t.Fatalf("expected 1 native fallback, got %d", len(native))
	}
	fb := native[0]
	if fb.Model != "claude-opus-4-8" {
		t.Errorf("Model = %q, want claude-opus-4-8", fb.Model)
	}
	if fb.MaxTokens == nil || *fb.MaxTokens != 2048 {
		t.Errorf("MaxTokens = %v, want 2048", fb.MaxTokens)
	}
	if fb.Speed == nil || *fb.Speed != "fast" {
		t.Errorf("Speed = %v, want fast", fb.Speed)
	}
	if fb.Thinking == nil || fb.Thinking.Type != "enabled" ||
		fb.Thinking.BudgetTokens == nil || *fb.Thinking.BudgetTokens != 512 {
		t.Errorf("Thinking = %+v, want enabled/512", fb.Thinking)
	}
	if fb.OutputConfig == nil || fb.OutputConfig.Effort == nil || *fb.OutputConfig.Effort != "high" {
		t.Errorf("OutputConfig = %+v, want effort=high", fb.OutputConfig)
	}

	// ...and back onto the wire with every field intact.
	out, err := sonic.Marshal(req.Fallbacks)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	for _, want := range []string{`"model":"claude-opus-4-8"`, `"max_tokens":2048`, `"speed":"fast"`, `"budget_tokens":512`, `"effort":"high"`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("re-marshalled entry missing %s: %s", want, out)
		}
	}
}

// Regression: AnthropicProvider.CountTokens omitted IsCountTokens, so the
// count-tokens strip never ran and the endpoint answered
// "fallback_credit_token: Extra inputs are not permitted".
func TestBuildAnthropicResponsesRequestBody_CountTokensStripsRejectedFields(t *testing.T) {
	t.Parallel()

	rawBody := []byte(`{"model":"claude-opus-4-8","max_tokens":1024,"temperature":0.5,` +
		`"messages":[{"role":"user","content":"hi"}],"fallback_credit_token":"tok"}`)
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)

	result, bifrostErr := BuildAnthropicResponsesRequestBody(ctx,
		&schemas.BifrostResponsesRequest{
			Provider:       schemas.Anthropic,
			Model:          "claude-opus-4-8",
			RawRequestBody: rawBody,
		},
		AnthropicRequestBuildConfig{
			Provider:      schemas.Anthropic,
			Model:         "claude-opus-4-8",
			IsCountTokens: true,
		})
	if bifrostErr != nil {
		t.Fatalf("unexpected error: %v", bifrostErr)
	}
	for _, field := range []string{"fallback_credit_token", "max_tokens", "temperature"} {
		if gjson.GetBytes(result, field).Exists() {
			t.Errorf("count_tokens rejects %q; expected it stripped, got: %s", field, result)
		}
	}
	// The model must survive — the count-tokens branch rewrites it from cfg.Model,
	// so an empty cfg.Model here would blank it out.
	if got := gjson.GetBytes(result, "model").String(); got != "claude-opus-4-8" {
		t.Errorf("model = %q, want claude-opus-4-8: %s", got, result)
	}
	if !gjson.GetBytes(result, "messages").Exists() {
		t.Errorf("strip damaged the body: %s", result)
	}
}

// A fallback entry's speed override needs the fast-mode beta just as a top-level
// speed does; without it the parameter is rejected as unrecognised.
func TestFallbackEntrySpeed_InjectsFastModeBetaHeader(t *testing.T) {
	t.Parallel()

	newReq := func(entry AnthropicNativeFallback) *AnthropicMessageRequest {
		return &AnthropicMessageRequest{
			Model:     "claude-fable-5",
			MaxTokens: 64,
			Messages:  []AnthropicMessage{{Role: "user", Content: AnthropicContent{ContentStr: schemas.Ptr("hi")}}},
			Fallbacks: &AnthropicFallbacks{Entries: []AnthropicFallbackEntry{{Native: &entry}}},
		}
	}
	hasFastMode := func(t *testing.T, req *AnthropicMessageRequest, provider schemas.ModelProvider) bool {
		t.Helper()
		ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
		defer cancel()
		if err := AddMissingBetaHeadersToContext(ctx, req, provider); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		hdrs, _ := ctx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string)
		return slices.Contains(hdrs[AnthropicBetaHeader], AnthropicFastModeBetaHeader)
	}

	// Speed lives only on the fallback entry — no top-level speed to trigger it.
	if !hasFastMode(t, newReq(AnthropicNativeFallback{Model: "claude-opus-4-8", Speed: schemas.Ptr("fast")}), schemas.Anthropic) {
		t.Error("expected fast-mode beta header for a fallback entry requesting speed")
	}
	// No speed anywhere — the header must not appear.
	if hasFastMode(t, newReq(AnthropicNativeFallback{Model: "claude-opus-4-8"}), schemas.Anthropic) {
		t.Error("did not expect fast-mode beta header when no entry requests speed")
	}
	// Gated on the entry's own model: sending the header for a model that cannot
	// serve fast mode guarantees a 400.
	if hasFastMode(t, newReq(AnthropicNativeFallback{Model: "claude-haiku-4-5", Speed: schemas.Ptr("fast")}), schemas.Anthropic) {
		t.Error("did not expect fast-mode beta header for a model without fast mode")
	}
}

// Documents the contract for the overloaded "fallbacks" key: entries are routed by
// shape, so the two features coexist in one array and neither consumes the other's.
func TestFallbacksArray_RoutedByEntryShape(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		fallbacks   string
		wantGateway []schemas.Fallback // Bifrost cross-provider failover
		wantNative  []string           // forwarded to Anthropic
	}{
		{
			name:        "strings only -> gateway failover, nothing forwarded",
			fallbacks:   `["openai/gpt-4o-mini","vertex/claude-opus-4-7"]`,
			wantGateway: []schemas.Fallback{{Provider: "openai", Model: "gpt-4o-mini"}, {Provider: "vertex", Model: "claude-opus-4-7"}},
		},
		{
			name:       "objects only -> forwarded to the provider, no gateway failover",
			fallbacks:  `[{"model":"claude-opus-4-8"}]`,
			wantNative: []string{"claude-opus-4-8"},
		},
		{
			name:        "mixed -> each half goes to its own owner",
			fallbacks:   `["openai/gpt-4o-mini",{"model":"claude-opus-4-8"}]`,
			wantGateway: []schemas.Fallback{{Provider: "openai", Model: "gpt-4o-mini"}},
			wantNative:  []string{"claude-opus-4-8"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
			defer cancel()

			var req AnthropicMessageRequest
			body := `{"model":"claude-fable-5","max_tokens":64,` +
				`"messages":[{"role":"user","content":"hi"}],"fallbacks":` + tc.fallbacks + `}`
			if err := sonic.Unmarshal([]byte(body), &req); err != nil {
				t.Fatalf("unmarshal failed: %v", err)
			}

			bifrostReq := req.ToBifrostResponsesRequest(ctx)
			if !slices.Equal(bifrostReq.Fallbacks, tc.wantGateway) {
				t.Errorf("gateway fallbacks = %+v, want %+v", bifrostReq.Fallbacks, tc.wantGateway)
			}

			var gotNative []string
			if v, ok := bifrostReq.Params.ExtraParams["fallbacks"].([]AnthropicNativeFallback); ok {
				for _, n := range v {
					gotNative = append(gotNative, n.Model)
				}
			}
			if !slices.Equal(gotNative, tc.wantNative) {
				t.Errorf("native fallbacks forwarded = %v, want %v", gotNative, tc.wantNative)
			}
		})
	}
}

// TestFallbacksDefault_RoundTrip pins the Opus 5 fallbacks:"default" form: it parses
// as a preset (not an array), fallbacksDefaultRouting() reports it, and it re-marshals
// back to the bare string rather than an array.
func TestFallbacksDefault_RoundTrip(t *testing.T) {
	t.Parallel()

	body := `{"model":"claude-opus-5","max_tokens":64,` +
		`"messages":[{"role":"user","content":"hi"}],"fallbacks":"default"}`
	var req AnthropicMessageRequest
	if err := sonic.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if req.Fallbacks == nil || req.Fallbacks.Preset != "default" || len(req.Fallbacks.Entries) != 0 {
		t.Fatalf("expected preset=\"default\" with no entries, got %+v", req.Fallbacks)
	}
	if !req.fallbacksDefaultRouting() {
		t.Error("fallbacksDefaultRouting() = false, want true")
	}

	out, err := sonic.Marshal(&req)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if fb := gjson.GetBytes(out, "fallbacks"); fb.Type != gjson.String || fb.String() != "default" {
		t.Errorf(`re-marshalled fallbacks = %s, want "default"`, fb.Raw)
	}
}

// TestFallbacksDefault_IntegrationRoundTrip reproduces the /anthropic/v1/messages
// typed path (AnthropicMessageRequest → ToBifrostResponsesRequest → rebuild): the
// "default" preset must survive the Bifrost round-trip and re-emit with the superset
// beta header, otherwise Anthropic 400s ("fallbacks: Input should be a valid array").
func TestFallbacksDefault_IntegrationRoundTrip(t *testing.T) {
	t.Parallel()

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	body := `{"model":"claude-opus-5","max_tokens":1024,"fallbacks":"default",` +
		`"messages":[{"role":"user","content":"hi"}],"output_config":{"effort":"xhigh"},` +
		`"thinking":{"type":"adaptive"}}`
	var req AnthropicMessageRequest
	if err := sonic.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	// AnthropicMessageRequest → BifrostResponsesRequest must preserve the preset.
	bifrostReq := req.ToBifrostResponsesRequest(ctx)
	if got, _ := bifrostReq.Params.ExtraParams["fallbacks"].(string); got != "default" {
		t.Fatalf(`bifrost ExtraParams["fallbacks"] = %#v, want "default"`, bifrostReq.Params.ExtraParams["fallbacks"])
	}

	// Rebuild → the typed field is restored...
	rebuilt, err := ToAnthropicResponsesRequest(ctx, bifrostReq)
	if err != nil {
		t.Fatalf("rebuild failed: %v", err)
	}
	if !rebuilt.fallbacksDefaultRouting() {
		t.Fatalf("rebuilt.fallbacksDefaultRouting() = false, want true (Fallbacks=%+v)", rebuilt.Fallbacks)
	}

	// ...it re-marshals to the bare string...
	out, err := sonic.Marshal(rebuilt)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if fb := gjson.GetBytes(out, "fallbacks"); fb.Type != gjson.String || fb.String() != "default" {
		t.Errorf(`rebuilt fallbacks = %s, want "default"`, fb.Raw)
	}

	// ...and the superset beta header is injected (this is what was missing).
	if err := AddMissingBetaHeadersToContext(ctx, rebuilt, schemas.Anthropic); err != nil {
		t.Fatalf("AddMissingBetaHeadersToContext failed: %v", err)
	}
	hdrs, _ := ctx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string)
	if !slices.Contains(hdrs[AnthropicBetaHeader], AnthropicServerSideFallbackDefaultBetaHeader) {
		t.Errorf("expected %q injected, got %v", AnthropicServerSideFallbackDefaultBetaHeader, hdrs[AnthropicBetaHeader])
	}
}

// TestFallbacksDefault_BetaHeader verifies default routing drives the superset
// -2026-07-01 beta header (not -06-01) on Anthropic, and is gated off where
// server-side fallback is unsupported.
func TestFallbacksDefault_BetaHeader(t *testing.T) {
	t.Parallel()

	newReq := func() *AnthropicMessageRequest {
		return &AnthropicMessageRequest{
			Model:     "claude-opus-5",
			MaxTokens: 64,
			Messages:  []AnthropicMessage{{Role: "user", Content: AnthropicContent{ContentStr: schemas.Ptr("hi")}}},
			Fallbacks: &AnthropicFallbacks{Preset: "default"},
		}
	}
	has := func(t *testing.T, provider schemas.ModelProvider, header string) bool {
		t.Helper()
		ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
		defer cancel()
		if err := AddMissingBetaHeadersToContext(ctx, newReq(), provider); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		hdrs, _ := ctx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string)
		return slices.Contains(hdrs[AnthropicBetaHeader], header)
	}

	if !has(t, schemas.Anthropic, AnthropicServerSideFallbackDefaultBetaHeader) {
		t.Errorf("expected %q on Anthropic", AnthropicServerSideFallbackDefaultBetaHeader)
	}
	if has(t, schemas.Anthropic, AnthropicServerSideFallbackBetaHeader) {
		t.Error("did not expect the -06-01 header for default routing")
	}
	if has(t, schemas.Vertex, AnthropicServerSideFallbackDefaultBetaHeader) {
		t.Error("did not expect the default-routing header on Vertex (server-side fallback unsupported)")
	}
}

// TestStripBifrostFallbacks_DefaultString verifies the "default" string form survives
// body stripping on providers that support server-side fallback and is dropped
// fail-closed elsewhere (matching the native-object gating).
func TestStripBifrostFallbacks_DefaultString(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"claude-opus-5","fallbacks":"default"}`)

	got, err := stripBifrostFallbacksFromBody(body, schemas.Anthropic)
	if err != nil {
		t.Fatalf("strip (anthropic) failed: %v", err)
	}
	if fb := gjson.GetBytes(got, "fallbacks"); fb.Type != gjson.String || fb.String() != "default" {
		t.Errorf("anthropic: fallbacks = %s, want kept \"default\"", fb.Raw)
	}

	for _, p := range []schemas.ModelProvider{schemas.Bedrock, schemas.Vertex} {
		got, err := stripBifrostFallbacksFromBody(body, p)
		if err != nil {
			t.Fatalf("strip (%s) failed: %v", p, err)
		}
		if gjson.GetBytes(got, "fallbacks").Exists() {
			t.Errorf("%s: expected fallbacks stripped, got %s", p, got)
		}
	}
}

package anthropic

import (
	"context"
	"slices"
	"testing"
	"time"

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

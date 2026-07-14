package sarvam

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestIsTextOnlyContentBlocks(t *testing.T) {
	text := "hello"
	tests := []struct {
		name   string
		blocks []schemas.ChatContentBlock
		want   bool
	}{
		{"empty slice", []schemas.ChatContentBlock{}, true},
		{"single text block", []schemas.ChatContentBlock{{Type: schemas.ChatContentBlockTypeText, Text: &text}}, true},
		{"multiple text blocks", []schemas.ChatContentBlock{
			{Type: schemas.ChatContentBlockTypeText, Text: &text},
			{Type: schemas.ChatContentBlockTypeText, Text: &text},
		}, true},
		{"text block with nil Text", []schemas.ChatContentBlock{{Type: schemas.ChatContentBlockTypeText, Text: nil}}, false},
		{"nil Text mixed with real text", []schemas.ChatContentBlock{
			{Type: schemas.ChatContentBlockTypeText, Text: nil},
			{Type: schemas.ChatContentBlockTypeText, Text: &text},
		}, false},
		{"image block", []schemas.ChatContentBlock{{Type: schemas.ChatContentBlockTypeImage}}, false},
		{"mixed text and image", []schemas.ChatContentBlock{
			{Type: schemas.ChatContentBlockTypeText, Text: &text},
			{Type: schemas.ChatContentBlockTypeImage},
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTextOnlyContentBlocks(tt.blocks); got != tt.want {
				t.Errorf("isTextOnlyContentBlocks(%+v) = %v, want %v", tt.blocks, got, tt.want)
			}
		})
	}
}

func TestFlattenMultiPartMessageContent(t *testing.T) {
	part1, part2 := "Part one.", "Part two."

	t.Run("no content blocks - request returned unchanged (same pointer)", func(t *testing.T) {
		str := "plain string"
		req := &schemas.BifrostChatRequest{Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &str}},
		}}
		got := flattenMultiPartMessageContent(req)
		if got != req {
			t.Errorf("expected the same request pointer back when nothing needs flattening")
		}
	})

	t.Run("multi-part text content collapses to a single newline-joined string", func(t *testing.T) {
		req := &schemas.BifrostChatRequest{Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleSystem, Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
				{Type: schemas.ChatContentBlockTypeText, Text: &part1},
				{Type: schemas.ChatContentBlockTypeText, Text: &part2},
			}}},
		}}
		got := flattenMultiPartMessageContent(req)
		if got == req {
			t.Fatalf("expected a new request copy, got the same pointer")
		}
		content := got.Input[0].Content
		if content.ContentBlocks != nil {
			t.Errorf("expected ContentBlocks to be cleared, got %+v", content.ContentBlocks)
		}
		if content.ContentStr == nil || *content.ContentStr != "Part one.\nPart two." {
			t.Errorf("expected flattened string %q, got %v", "Part one.\nPart two.", content.ContentStr)
		}
	})

	t.Run("single-element content blocks also collapse (Sarvam rejects any array)", func(t *testing.T) {
		req := &schemas.BifrostChatRequest{Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
				{Type: schemas.ChatContentBlockTypeText, Text: &part1},
			}}},
		}}
		got := flattenMultiPartMessageContent(req)
		if *got.Input[0].Content.ContentStr != "Part one." {
			t.Errorf("expected %q, got %v", "Part one.", got.Input[0].Content.ContentStr)
		}
	})

	t.Run("non-text blocks (e.g. image) are left untouched", func(t *testing.T) {
		req := &schemas.BifrostChatRequest{Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
				{Type: schemas.ChatContentBlockTypeImage},
			}}},
		}}
		got := flattenMultiPartMessageContent(req)
		if got != req {
			t.Errorf("expected the original request back since no message qualifies for flattening")
		}
		if got.Input[0].Content.ContentBlocks == nil {
			t.Errorf("expected the image block to be left as ContentBlocks, got it cleared")
		}
	})

	t.Run("original request is not mutated", func(t *testing.T) {
		original := []schemas.ChatContentBlock{
			{Type: schemas.ChatContentBlockTypeText, Text: &part1},
			{Type: schemas.ChatContentBlockTypeText, Text: &part2},
		}
		req := &schemas.BifrostChatRequest{Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleSystem, Content: &schemas.ChatMessageContent{ContentBlocks: original}},
		}}
		_ = flattenMultiPartMessageContent(req)
		if req.Input[0].Content.ContentBlocks == nil {
			t.Errorf("original request's Content was mutated in place")
		}
		if len(req.Input[0].Content.ContentBlocks) != 2 {
			t.Errorf("original request's ContentBlocks slice was mutated")
		}
	})
}

func TestNormalizeDeveloperRole(t *testing.T) {
	str := "hi"

	t.Run("no developer-role messages - request returned unchanged (same pointer)", func(t *testing.T) {
		req := &schemas.BifrostChatRequest{Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &str}},
		}}
		got := normalizeDeveloperRole(req)
		if got != req {
			t.Errorf("expected the same request pointer back when nothing needs normalizing")
		}
	})

	t.Run("developer role rewritten to system", func(t *testing.T) {
		req := &schemas.BifrostChatRequest{Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleDeveloper, Content: &schemas.ChatMessageContent{ContentStr: &str}},
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &str}},
		}}
		got := normalizeDeveloperRole(req)
		if got == req {
			t.Fatalf("expected a new request copy, got the same pointer")
		}
		if got.Input[0].Role != schemas.ChatMessageRoleSystem {
			t.Errorf("expected role %q, got %q", schemas.ChatMessageRoleSystem, got.Input[0].Role)
		}
		if got.Input[1].Role != schemas.ChatMessageRoleUser {
			t.Errorf("expected user role to be left alone, got %q", got.Input[1].Role)
		}
	})

	t.Run("original request is not mutated", func(t *testing.T) {
		req := &schemas.BifrostChatRequest{Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleDeveloper, Content: &schemas.ChatMessageContent{ContentStr: &str}},
		}}
		_ = normalizeDeveloperRole(req)
		if req.Input[0].Role != schemas.ChatMessageRoleDeveloper {
			t.Errorf("original request's role was mutated in place, got %q", req.Input[0].Role)
		}
	})

	t.Run("composes with flattenMultiPartMessageContent", func(t *testing.T) {
		part1, part2 := "Part one.", "Part two."
		req := &schemas.BifrostChatRequest{Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleDeveloper, Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
				{Type: schemas.ChatContentBlockTypeText, Text: &part1},
				{Type: schemas.ChatContentBlockTypeText, Text: &part2},
			}}},
		}}
		got := normalizeDeveloperRole(flattenMultiPartMessageContent(req))
		if got.Input[0].Role != schemas.ChatMessageRoleSystem {
			t.Errorf("expected role %q, got %q", schemas.ChatMessageRoleSystem, got.Input[0].Role)
		}
		if got.Input[0].Content.ContentBlocks != nil {
			t.Errorf("expected ContentBlocks cleared, got %+v", got.Input[0].Content.ContentBlocks)
		}
		if *got.Input[0].Content.ContentStr != "Part one.\nPart two." {
			t.Errorf("expected flattened string, got %v", got.Input[0].Content.ContentStr)
		}
	})
}

func TestNormalizeReasoningEffort(t *testing.T) {
	newReq := func(effort *string) *schemas.BifrostChatRequest {
		req := &schemas.BifrostChatRequest{Input: []schemas.ChatMessage{}}
		if effort != nil {
			req.Params = &schemas.ChatParameters{Reasoning: &schemas.ChatReasoning{Effort: effort}}
		}
		return req
	}
	strPtr := func(s string) *string { return &s }

	t.Run("no params - request returned unchanged (same pointer)", func(t *testing.T) {
		req := newReq(nil)
		got := normalizeReasoningEffort(req)
		if got != req {
			t.Errorf("expected the same request pointer back when there's no reasoning param")
		}
	})

	t.Run("supported effort values pass through untouched", func(t *testing.T) {
		for _, effort := range []string{"low", "medium", "high"} {
			req := newReq(strPtr(effort))
			got := normalizeReasoningEffort(req)
			if got != req {
				t.Errorf("effort %q: expected the same request pointer back, got a copy", effort)
			}
			if *got.Params.Reasoning.Effort != effort {
				t.Errorf("effort %q: expected it left untouched, got %q", effort, *got.Params.Reasoning.Effort)
			}
		}
	})

	t.Run(`"none" is cleared - Sarvam rejects it outright and has no equivalent`, func(t *testing.T) {
		req := newReq(strPtr("none"))
		got := normalizeReasoningEffort(req)
		if got == req {
			t.Fatalf("expected a new request copy, got the same pointer")
		}
		if got.Params.Reasoning.Effort != nil {
			t.Errorf("expected Effort to be cleared, got %v", *got.Params.Reasoning.Effort)
		}
	})

	t.Run(`"none" with MaxTokens also clears MaxTokens, not just Effort`, func(t *testing.T) {
		// Regression test: core/providers/openai/chat.go's own
		// normalizeReasoningEffort infers a fresh Effort from MaxTokens
		// whenever Effort is nil - clearing only Effort here and leaving a
		// caller-supplied MaxTokens budget in place would let that generic
		// mapping silently recreate the exact effort this function is
		// supposed to drop. Caught in review before merge.
		effort := "none"
		maxTokens := 2048
		req := &schemas.BifrostChatRequest{
			Input: []schemas.ChatMessage{},
			Params: &schemas.ChatParameters{
				Reasoning: &schemas.ChatReasoning{Effort: &effort, MaxTokens: &maxTokens},
			},
		}
		got := normalizeReasoningEffort(req)
		if got.Params.Reasoning.Effort != nil {
			t.Errorf("expected Effort to be cleared, got %v", *got.Params.Reasoning.Effort)
		}
		if got.Params.Reasoning.MaxTokens != nil {
			t.Errorf("expected MaxTokens to also be cleared, got %v", *got.Params.Reasoning.MaxTokens)
		}
	})

	t.Run(`"None" (mixed case) is also cleared`, func(t *testing.T) {
		req := newReq(strPtr("None"))
		got := normalizeReasoningEffort(req)
		if got.Params.Reasoning.Effort != nil {
			t.Errorf("expected Effort to be cleared, got %v", *got.Params.Reasoning.Effort)
		}
	})

	t.Run(`"minimal" is left for the generic openai mapping to translate to "low", not dropped here`, func(t *testing.T) {
		// Regression test: an earlier version of this function treated "minimal"
		// the same as "none" and cleared it, silently downgrading an explicit
		// low-effort request to Sarvam's full-reasoning default. "minimal" has a
		// real Sarvam-compatible mapping via core/providers/openai/chat.go's
		// generic filterOpenAISpecificParameters ("minimal" -> "low"), so this
		// function must leave it alone and let that generic mapping run.
		req := newReq(strPtr("minimal"))
		got := normalizeReasoningEffort(req)
		if got != req {
			t.Fatalf(`expected the same request pointer back for "minimal" (not this function's concern), got a copy`)
		}
		if got.Params.Reasoning.Effort == nil || *got.Params.Reasoning.Effort != "minimal" {
			t.Errorf(`expected "minimal" left untouched, got %v`, got.Params.Reasoning.Effort)
		}
	})

	t.Run("original request is not mutated", func(t *testing.T) {
		req := newReq(strPtr("none"))
		original := req.Params
		_ = normalizeReasoningEffort(req)
		if req.Params != original {
			t.Errorf("original request's Params pointer was replaced in place")
		}
		if req.Params.Reasoning.Effort == nil || *req.Params.Reasoning.Effort != "none" {
			t.Errorf("original request's Reasoning.Effort was mutated in place, got %v", req.Params.Reasoning.Effort)
		}
	})
}

func TestNormalizeRequest(t *testing.T) {
	t.Run("nil request is a no-op", func(t *testing.T) {
		if got := normalizeRequest(nil); got != nil {
			t.Errorf("expected nil back for a nil request, got %+v", got)
		}
	})

	t.Run("composes all three normalizations", func(t *testing.T) {
		part1, part2 := "Part one.", "Part two."
		noneEffort := "none"
		req := &schemas.BifrostChatRequest{
			Input: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleDeveloper, Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
					{Type: schemas.ChatContentBlockTypeText, Text: &part1},
					{Type: schemas.ChatContentBlockTypeText, Text: &part2},
				}}},
			},
			Params: &schemas.ChatParameters{Reasoning: &schemas.ChatReasoning{Effort: &noneEffort}},
		}
		got := normalizeRequest(req)
		if got.Input[0].Role != schemas.ChatMessageRoleSystem {
			t.Errorf("expected developer role normalized to system, got %q", got.Input[0].Role)
		}
		if got.Input[0].Content.ContentBlocks != nil || *got.Input[0].Content.ContentStr != "Part one.\nPart two." {
			t.Errorf("expected content flattened, got %+v", got.Input[0].Content)
		}
		if got.Params.Reasoning.Effort != nil {
			t.Errorf("expected reasoning effort cleared, got %v", *got.Params.Reasoning.Effort)
		}
	})
}

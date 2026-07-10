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

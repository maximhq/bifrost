package schemas

import (
	"testing"
)

// Regression tests for issue #6245: flattenSystemTextBlocks (via ToChatMessages) must
// join multi-block system/developer text content into a single string prompt, and must
// leave every other shape untouched. The Anthropic ingress path is covered in
// core/providers/anthropic/systemflatten_test.go.
func TestFlattenSystemTextBlocksShapes(t *testing.T) {
	t.Run("multi-block developer message flattens to string", func(t *testing.T) {
		devRole := ResponsesInputMessageRoleDeveloper
		chatMessages := ToChatMessages([]ResponsesMessage{
			{
				Role: &devRole,
				Content: &ResponsesMessageContent{
					ContentBlocks: []ResponsesMessageContentBlock{
						{Type: ResponsesInputMessageContentBlockTypeText, Text: Ptr("dev block A")},
						{Type: ResponsesInputMessageContentBlockTypeText, Text: Ptr("dev block B")},
					},
				},
			},
		})
		if len(chatMessages) != 1 {
			t.Fatalf("expected 1 chat message, got %d", len(chatMessages))
		}
		dev := chatMessages[0]
		if dev.Role != ChatMessageRoleDeveloper {
			t.Fatalf("expected developer role, got %s", dev.Role)
		}
		if dev.Content == nil || dev.Content.ContentStr == nil {
			t.Fatalf("expected multi-block developer message to flatten to string content, got: %+v", dev.Content)
		}
		if want := "dev block A\n\ndev block B"; *dev.Content.ContentStr != want {
			t.Errorf("flattened developer prompt mismatch:\ngot:  %q\nwant: %q", *dev.Content.ContentStr, want)
		}
	})

	t.Run("empty text blocks are skipped when joining", func(t *testing.T) {
		sysRole := ResponsesInputMessageRoleSystem
		chatMessages := ToChatMessages([]ResponsesMessage{
			{
				Role: &sysRole,
				Content: &ResponsesMessageContent{
					ContentBlocks: []ResponsesMessageContentBlock{
						{Type: ResponsesInputMessageContentBlockTypeText, Text: Ptr("block A")},
						{Type: ResponsesInputMessageContentBlockTypeText, Text: Ptr("   ")},
						{Type: ResponsesInputMessageContentBlockTypeText, Text: Ptr("block B")},
					},
				},
			},
		})
		if len(chatMessages) != 1 {
			t.Fatalf("expected 1 chat message, got %d", len(chatMessages))
		}
		sys := chatMessages[0]
		if sys.Content == nil || sys.Content.ContentStr == nil {
			t.Fatalf("expected flattened string content, got: %+v", sys.Content)
		}
		if want := "block A\n\nblock B"; *sys.Content.ContentStr != want {
			t.Errorf("expected empty block skipped in join:\ngot:  %q\nwant: %q", *sys.Content.ContentStr, want)
		}
	})

	t.Run("multi-block user message keeps content array", func(t *testing.T) {
		// Flattening applies to system/developer roles only: user messages keep their
		// content array through ToChatMessages (image/file parts depend on it).
		userRole := ResponsesInputMessageRoleUser
		chatMessages := ToChatMessages([]ResponsesMessage{
			{
				Role: &userRole,
				Content: &ResponsesMessageContent{
					ContentBlocks: []ResponsesMessageContentBlock{
						{Type: ResponsesInputMessageContentBlockTypeText, Text: Ptr("part one")},
						{Type: ResponsesInputMessageContentBlockTypeText, Text: Ptr("part two")},
					},
				},
			},
		})
		if len(chatMessages) != 1 {
			t.Fatalf("expected 1 chat message, got %d", len(chatMessages))
		}
		user := chatMessages[0]
		if user.Role != ChatMessageRoleUser {
			t.Fatalf("expected user role, got %s", user.Role)
		}
		if user.Content == nil || user.Content.ContentBlocks == nil {
			t.Fatalf("expected multi-block user message to keep content array, got: %+v", user.Content)
		}
		blocks := user.Content.ContentBlocks
		if len(blocks) != 2 {
			t.Fatalf("expected 2 user content blocks, got %d", len(blocks))
		}
		for i, want := range []string{"part one", "part two"} {
			if blocks[i].Type != ChatContentBlockTypeText {
				t.Errorf("block %d: expected text type, got %q", i, blocks[i].Type)
			}
			if blocks[i].Text == nil || *blocks[i].Text != want {
				t.Errorf("block %d: expected text %q, got %+v", i, want, blocks[i].Text)
			}
		}
	})
}

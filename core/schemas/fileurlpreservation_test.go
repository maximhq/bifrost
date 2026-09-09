package schemas

import "testing"

// A URL-sourced document (Anthropic `{"type":"document","source":{"type":"url",...}}`,
// normalized to `{"type":"file","file":{"file_url":...}}` by
// ChatContentBlock.UnmarshalJSON) carries its entire payload in file_url.
// The chat<->responses converters used to copy only file_data/filename, so a
// URL-sourced file arrived at the provider as a bare `{"type":"input_file"}`
// with nothing in it -- which OpenAI rejects as
// "Missing required parameter: 'input[N].content[0]'", the same failure as a
// dropped document block.
func fileURLChatMessage() ChatMessage {
	return ChatMessage{
		Role: ChatMessageRoleUser,
		Content: &ChatMessageContent{
			ContentBlocks: []ChatContentBlock{
				{
					Type: ChatContentBlockTypeFile,
					File: &ChatInputFile{
						FileURL:  Ptr("https://example.com/report.pdf"),
						FileType: Ptr("application/pdf"),
					},
				},
			},
		},
	}
}

func assertFileURLKept(t *testing.T, blocks []ResponsesMessageContentBlock) {
	t.Helper()
	if len(blocks) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(blocks))
	}
	file := blocks[0].ResponsesInputMessageContentBlockFile
	if file == nil {
		t.Fatal("input_file block has no file payload")
	}
	if file.FileURL == nil {
		t.Fatal("file_url was dropped in chat->responses conversion")
	}
	if *file.FileURL != "https://example.com/report.pdf" {
		t.Fatalf("unexpected file_url: %q", *file.FileURL)
	}
	if file.FileType == nil || *file.FileType != "application/pdf" {
		t.Fatal("file_type was dropped in chat->responses conversion")
	}
}

func TestChatToResponsesKeepsFileURL(t *testing.T) {
	cm := fileURLChatMessage()
	rms := cm.ToResponsesMessages()
	if len(rms) != 1 {
		t.Fatalf("expected 1 responses message, got %d", len(rms))
	}
	if rms[0].Content == nil {
		t.Fatal("converted message has no content")
	}
	assertFileURLKept(t, rms[0].Content.ContentBlocks)
}

// And the reverse direction must not lose it either.
func TestResponsesToChatKeepsFileURL(t *testing.T) {
	cm := fileURLChatMessage()
	back := ToChatMessages(cm.ToResponsesMessages())
	if len(back) != 1 {
		t.Fatalf("expected 1 chat message, got %d", len(back))
	}

	if back[0].Content == nil || len(back[0].Content.ContentBlocks) != 1 {
		t.Fatal("round-tripped message lost its content block")
	}
	file := back[0].Content.ContentBlocks[0].File
	if file == nil {
		t.Fatal("round-tripped block has no file payload")
	}
	if file.FileURL == nil || *file.FileURL != "https://example.com/report.pdf" {
		t.Fatal("file_url was dropped in responses->chat conversion")
	}
	if file.FileType == nil || *file.FileType != "application/pdf" {
		t.Fatal("file_type was dropped in responses->chat conversion")
	}
}

package anthropic

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func convertToolResultDocumentBlocksForTest(grouped bool, content []AnthropicContentBlock) []schemas.ResponsesMessage {
	role := schemas.ResponsesInputMessageRoleUser
	blocks := []AnthropicContentBlock{
		{
			Type:      AnthropicContentBlockTypeToolResult,
			ToolUseID: schemas.Ptr("toolu_document"),
			Content:   &AnthropicContent{ContentBlocks: content},
		},
	}
	if grouped {
		return convertAnthropicContentBlocksToResponsesMessagesGrouped(blocks, &role, false)
	}
	return convertAnthropicContentBlocksToResponsesMessages(nil, blocks, &role, false, "")
}

func requireToolResultDocumentBlocks(t *testing.T, messages []schemas.ResponsesMessage) []schemas.ResponsesMessageContentBlock {
	t.Helper()
	if len(messages) != 1 {
		t.Fatalf("expected 1 converted message, got %d", len(messages))
	}
	message := messages[0]
	if message.Type == nil || *message.Type != schemas.ResponsesMessageTypeFunctionCallOutput {
		t.Fatalf("expected function_call_output, got %#v", message.Type)
	}
	if message.ResponsesToolMessage == nil || message.Output == nil {
		t.Fatal("expected a populated tool message output")
	}
	return message.Output.ResponsesFunctionToolCallOutputBlocks
}

func TestToolResultDocumentURLPreserved(t *testing.T) {
	for _, grouped := range []bool{true, false} {
		mode := "non-grouped"
		if grouped {
			mode = "grouped"
		}
		t.Run(mode, func(t *testing.T) {
			content := []AnthropicContentBlock{
				{
					Type: AnthropicContentBlockTypeText,
					Text: schemas.Ptr("Document generated"),
				},
				{
					Type:  AnthropicContentBlockTypeDocument,
					Title: schemas.Ptr("report.pdf"),
					Source: &AnthropicBlockSource{SourceObj: &AnthropicSource{
						Type: "url",
						URL:  schemas.Ptr("https://example.com/report.pdf"),
					}},
				},
			}

			blocks := requireToolResultDocumentBlocks(t, convertToolResultDocumentBlocksForTest(grouped, content))
			if len(blocks) != 2 {
				t.Fatalf("expected text and document blocks, got %d", len(blocks))
			}
			if blocks[0].Text == nil || *blocks[0].Text != "Document generated" {
				t.Fatalf("expected text block first, got %#v", blocks[0])
			}
			if blocks[1].Type != schemas.ResponsesInputMessageContentBlockTypeFile {
				t.Fatalf("expected file block second, got %q", blocks[1].Type)
			}
			file := blocks[1].ResponsesInputMessageContentBlockFile
			if file == nil {
				t.Fatal("expected canonical Bifrost file block")
			}
			if file.FileURL == nil || *file.FileURL != "https://example.com/report.pdf" {
				t.Fatalf("expected document URL to be preserved, got %#v", file.FileURL)
			}
			if file.Filename == nil || *file.Filename != "report.pdf" {
				t.Fatalf("expected document title as filename, got %#v", file.Filename)
			}
		})
	}
}

func TestToolResultDocumentBase64Preserved(t *testing.T) {
	for _, grouped := range []bool{true, false} {
		mode := "non-grouped"
		if grouped {
			mode = "grouped"
		}
		t.Run(mode, func(t *testing.T) {
			content := []AnthropicContentBlock{
				{
					Type:  AnthropicContentBlockTypeDocument,
					Title: schemas.Ptr("inline.pdf"),
					Source: &AnthropicBlockSource{SourceObj: &AnthropicSource{
						Type:      "base64",
						MediaType: schemas.Ptr("application/pdf"),
						Data:      schemas.Ptr("JVBERi0xLjQ="),
					}},
				},
			}

			blocks := requireToolResultDocumentBlocks(t, convertToolResultDocumentBlocksForTest(grouped, content))
			if len(blocks) != 1 {
				t.Fatalf("expected 1 document block, got %d", len(blocks))
			}
			file := blocks[0].ResponsesInputMessageContentBlockFile
			if file == nil || file.FileData == nil {
				t.Fatal("expected inline file data")
			}
			if got := *file.FileData; got != "data:application/pdf;base64,JVBERi0xLjQ=" {
				t.Fatalf("expected base64 data and media type to be preserved, got %q", got)
			}
			if file.FileType == nil || *file.FileType != "application/pdf" {
				t.Fatalf("expected application/pdf file type, got %#v", file.FileType)
			}
		})
	}
}

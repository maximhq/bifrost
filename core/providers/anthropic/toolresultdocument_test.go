package anthropic

import (
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// Anthropic-format clients (Claude Code's Read tool among them) return a PDF
// to the model as a `document` block nested inside a `tool_result`. Both
// anthropic->responses converters historically handled only `text` and
// `image` inside a tool_result, so the document block was dropped on the
// floor. When the tool_result carried ONLY that document, the resulting
// function_call_output went out with an empty content array, and OpenAI
// rejected the whole request with:
//
//	400 Missing required parameter: 'input[N].content[0]'.
//
// These tests pin the document block surviving the conversion in both
// converters.
func toolResultWithDocument(sourceObj *AnthropicSource) []AnthropicContentBlock {
	return []AnthropicContentBlock{
		{
			Type:      AnthropicContentBlockTypeToolResult,
			ToolUseID: schemas.Ptr("toolu_test_1"),
			Content: &AnthropicContent{
				ContentBlocks: []AnthropicContentBlock{
					{
						Type:   AnthropicContentBlockTypeDocument,
						Title:  schemas.Ptr("sample.pdf"),
						Source: &AnthropicBlockSource{SourceObj: sourceObj},
					},
				},
			},
		},
	}
}

func base64PDFSource() *AnthropicSource {
	return &AnthropicSource{
		Type:      "base64",
		MediaType: schemas.Ptr("application/pdf"),
		Data:      schemas.Ptr("JVBERi0xLjQK"),
	}
}

// assertDocumentSurvived checks that exactly one function_call_output was
// produced and that it carries a non-empty input_file content block.
func assertDocumentSurvived(t *testing.T, msgs []schemas.ResponsesMessage) {
	t.Helper()

	var out *schemas.ResponsesMessage
	for i := range msgs {
		if msgs[i].Type != nil && *msgs[i].Type == schemas.ResponsesMessageTypeFunctionCallOutput {
			out = &msgs[i]
			break
		}
	}
	if out == nil {
		t.Fatalf("expected a function_call_output message, got %d messages", len(msgs))
	}
	if out.ResponsesToolMessage == nil || out.ResponsesToolMessage.Output == nil {
		t.Fatal("function_call_output has no tool message output")
	}

	blocks := out.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks
	if len(blocks) == 0 {
		t.Fatal("document block was dropped: tool output content array is empty " +
			"(this is what OpenAI rejects as \"Missing required parameter: 'input[N].content[0]'\")")
	}
	if blocks[0].Type != schemas.ResponsesInputMessageContentBlockTypeFile {
		t.Fatalf("expected first block type %q, got %q",
			schemas.ResponsesInputMessageContentBlockTypeFile, blocks[0].Type)
	}
	if blocks[0].ResponsesInputMessageContentBlockFile == nil {
		t.Fatal("input_file block has no file payload")
	}
	file := blocks[0].ResponsesInputMessageContentBlockFile
	if file.FileData == nil && file.FileURL == nil && blocks[0].FileID == nil {
		t.Fatal("input_file block carries none of file_data / file_url / file_id")
	}
	// OpenAI requires a filename whenever inline file_data is sent.
	if file.FileData != nil && (file.Filename == nil || *file.Filename == "") {
		t.Fatal("input_file block has file_data but no filename; OpenAI rejects this")
	}
}

func TestToolResultDocumentSurvivesGroupedConversion(t *testing.T) {
	role := schemas.ResponsesInputMessageRoleUser
	msgs := convertAnthropicContentBlocksToResponsesMessagesGrouped(
		toolResultWithDocument(base64PDFSource()), &role, false)
	assertDocumentSurvived(t, msgs)
}

func TestToolResultDocumentSurvivesConversion(t *testing.T) {
	role := schemas.ResponsesInputMessageRoleUser
	msgs := convertAnthropicContentBlocksToResponsesMessages(
		nil, toolResultWithDocument(base64PDFSource()), &role, false, "")
	assertDocumentSurvived(t, msgs)
}

// A document with no title must still produce a usable input_file block:
// OpenAI rejects inline file_data that has no filename, so the converter has
// to synthesize one rather than leave it unset.
func TestToolResultDocumentWithoutTitleGetsFilename(t *testing.T) {
	src := base64PDFSource()
	blocks := toolResultWithDocument(src)
	blocks[0].Content.ContentBlocks[0].Title = nil

	role := schemas.ResponsesInputMessageRoleUser
	msgs := convertAnthropicContentBlocksToResponsesMessagesGrouped(blocks, &role, false)
	assertDocumentSurvived(t, msgs)

	msgs = convertAnthropicContentBlocksToResponsesMessages(nil, blocks, &role, false, "")
	assertDocumentSurvived(t, msgs)
}

// A tool_result mixing text and a document must preserve both, in order.
func TestToolResultDocumentAlongsideText(t *testing.T) {
	blocks := toolResultWithDocument(base64PDFSource())
	blocks[0].Content.ContentBlocks = append(
		[]AnthropicContentBlock{{
			Type: AnthropicContentBlockTypeText,
			Text: schemas.Ptr("Here is the file:"),
		}},
		blocks[0].Content.ContentBlocks...,
	)

	role := schemas.ResponsesInputMessageRoleUser
	msgs := convertAnthropicContentBlocksToResponsesMessagesGrouped(blocks, &role, false)

	var out *schemas.ResponsesMessage
	for i := range msgs {
		if msgs[i].Type != nil && *msgs[i].Type == schemas.ResponsesMessageTypeFunctionCallOutput {
			out = &msgs[i]
			break
		}
	}
	if out == nil {
		t.Fatal("expected a function_call_output message")
	}
	got := out.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks
	if len(got) != 2 {
		t.Fatalf("expected 2 content blocks (text + file), got %d", len(got))
	}
	if got[0].Type != schemas.ResponsesInputMessageContentBlockTypeText {
		t.Fatalf("expected first block to be input_text, got %q", got[0].Type)
	}
	if got[1].Type != schemas.ResponsesInputMessageContentBlockTypeFile {
		t.Fatalf("expected second block to be input_file, got %q", got[1].Type)
	}
}

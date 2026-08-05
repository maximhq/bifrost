package bedrock

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/schemas"
)

func toolResultDocumentMessage(blocks []schemas.ResponsesMessageContentBlock) schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID: schemas.Ptr("toolu_document"),
			Output: &schemas.ResponsesToolMessageOutputStruct{
				ResponsesFunctionToolCallOutputBlocks: blocks,
			},
		},
	}
}

func requireBedrockToolResultDocument(t *testing.T, messages []BedrockMessage) *BedrockToolResult {
	t.Helper()
	if len(messages) != 1 {
		t.Fatalf("expected 1 Bedrock message, got %d", len(messages))
	}
	if len(messages[0].Content) != 1 || messages[0].Content[0].ToolResult == nil {
		t.Fatalf("expected one tool result block, got %#v", messages[0].Content)
	}
	return messages[0].Content[0].ToolResult
}

func TestToolResultDocumentInlinePreserved(t *testing.T) {
	input := []schemas.ResponsesMessage{
		toolResultDocumentMessage([]schemas.ResponsesMessageContentBlock{
			{
				Type: schemas.ResponsesInputMessageContentBlockTypeText,
				Text: schemas.Ptr("Document generated"),
			},
			{
				Type: schemas.ResponsesInputMessageContentBlockTypeFile,
				ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
					FileData: schemas.Ptr("data:application/pdf;base64,JVBERi0xLjQ="),
					Filename: schemas.Ptr("quarterly/report.pdf"),
					FileType: schemas.Ptr("application/pdf"),
				},
			},
		}),
	}

	messages, _, err := ConvertBifrostMessagesToBedrockMessages(context.Background(), input, false)
	if err != nil {
		t.Fatalf("unexpected conversion error: %v", err)
	}
	toolResult := requireBedrockToolResultDocument(t, messages)
	if len(toolResult.Content) != 2 {
		t.Fatalf("expected text and document blocks, got %d", len(toolResult.Content))
	}
	if toolResult.Content[0].Text == nil || *toolResult.Content[0].Text != "Document generated" {
		t.Fatalf("expected text block first, got %#v", toolResult.Content[0])
	}
	document := toolResult.Content[1].Document
	if document == nil {
		t.Fatalf("expected document block second, got %#v", toolResult.Content[1])
	}
	if document.Format != "pdf" {
		t.Fatalf("expected pdf format, got %q", document.Format)
	}
	if document.Name != "quarterly_report_pdf" {
		t.Fatalf("expected normalized filename, got %q", document.Name)
	}
	if document.Source == nil || document.Source.Bytes == nil || *document.Source.Bytes != "JVBERi0xLjQ=" {
		t.Fatalf("expected inline PDF bytes, got %#v", document.Source)
	}
}

func TestToolResultDocumentUnknownDataURLTypePreservesDeclaredFormat(t *testing.T) {
	input := []schemas.ResponsesMessage{
		toolResultDocumentMessage([]schemas.ResponsesMessageContentBlock{
			{
				Type: schemas.ResponsesInputMessageContentBlockTypeFile,
				ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
					FileData: schemas.Ptr("data:application/octet-stream;base64,QQ=="),
					Filename: schemas.Ptr("report.csv"),
					FileType: schemas.Ptr("text/csv"),
				},
			},
		}),
	}

	messages, _, err := ConvertBifrostMessagesToBedrockMessages(context.Background(), input, false)
	if err != nil {
		t.Fatalf("unexpected conversion error: %v", err)
	}
	toolResult := requireBedrockToolResultDocument(t, messages)
	if len(toolResult.Content) != 1 || toolResult.Content[0].Document == nil {
		t.Fatalf("expected one document block, got %#v", toolResult.Content)
	}
	document := toolResult.Content[0].Document
	if document.Format != "csv" {
		t.Fatalf("expected declared csv format to survive unknown data URL type, got %q", document.Format)
	}
	if document.Source == nil || document.Source.Bytes == nil || *document.Source.Bytes != "QQ==" {
		t.Fatalf("expected original inline bytes, got %#v", document.Source)
	}
}

func TestChatDocumentGenericTextTypeTreatsRawDataAsText(t *testing.T) {
	rawData := "PHJvb3QvPg=="
	blocks, err := convertContentBlock(context.Background(), schemas.ChatContentBlock{
		Type: schemas.ChatContentBlockTypeFile,
		File: &schemas.ChatInputFile{
			FileData: &rawData,
			Filename: schemas.Ptr("document.xml"),
			FileType: schemas.Ptr("text/xml"),
		},
	})
	if err != nil {
		t.Fatalf("unexpected conversion error: %v", err)
	}
	if len(blocks) != 1 || blocks[0].Document == nil {
		t.Fatalf("expected one document block, got %#v", blocks)
	}
	document := blocks[0].Document
	if document.Format != "txt" {
		t.Fatalf("expected Bedrock txt format, got %q", document.Format)
	}
	if document.Source == nil || document.Source.Text == nil || *document.Source.Text != rawData {
		t.Fatalf("expected no-prefix file data to remain literal text, got %#v", document.Source)
	}
	if document.Source.Bytes != nil {
		t.Fatalf("expected DocumentSource union to contain only text, got bytes %#v", document.Source.Bytes)
	}
}

func TestToolResultTextDocumentUsesSingleSourceMember(t *testing.T) {
	input := []schemas.ResponsesMessage{
		toolResultDocumentMessage([]schemas.ResponsesMessageContentBlock{
			{
				Type: schemas.ResponsesInputMessageContentBlockTypeFile,
				ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
					FileData: schemas.Ptr("Hello from the tool"),
					Filename: schemas.Ptr("result.txt"),
					FileType: schemas.Ptr("text/plain"),
				},
			},
		}),
	}

	messages, _, err := ConvertBifrostMessagesToBedrockMessages(context.Background(), input, false)
	if err != nil {
		t.Fatalf("unexpected conversion error: %v", err)
	}
	toolResult := requireBedrockToolResultDocument(t, messages)
	if len(toolResult.Content) != 1 || toolResult.Content[0].Document == nil {
		t.Fatalf("expected one document block, got %#v", toolResult.Content)
	}
	source := toolResult.Content[0].Document.Source
	if source == nil || source.Text == nil || *source.Text != "Hello from the tool" {
		t.Fatalf("expected plain text document source, got %#v", source)
	}
	if source.Bytes != nil {
		t.Fatalf("expected DocumentSource union to contain only text, got bytes %#v", source.Bytes)
	}
}

func TestToolResultTextDocumentDataURLUsesSingleSourceMember(t *testing.T) {
	tests := []struct {
		name    string
		dataURL string
	}{
		{name: "lowercase scheme", dataURL: "data:text/plain;base64,SGVsbG8gZnJvbSB0aGUgdG9vbA=="},
		{name: "mixed-case scheme", dataURL: "DaTa:text/plain;base64,SGVsbG8gZnJvbSB0aGUgdG9vbA=="},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dataURL := tt.dataURL
			input := []schemas.ResponsesMessage{
				toolResultDocumentMessage([]schemas.ResponsesMessageContentBlock{
					{
						Type: schemas.ResponsesInputMessageContentBlockTypeFile,
						ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
							FileData: &dataURL,
							Filename: schemas.Ptr("result.txt"),
							FileType: schemas.Ptr("text/plain"),
						},
					},
				}),
			}

			messages, _, err := ConvertBifrostMessagesToBedrockMessages(context.Background(), input, false)
			if err != nil {
				t.Fatalf("unexpected conversion error for %q: %v", dataURL, err)
			}
			toolResult := requireBedrockToolResultDocument(t, messages)
			if len(toolResult.Content) != 1 || toolResult.Content[0].Document == nil {
				t.Fatalf("expected one document block, got %#v", toolResult.Content)
			}
			source := toolResult.Content[0].Document.Source
			if source == nil || source.Text == nil || *source.Text != "Hello from the tool" {
				t.Fatalf("expected decoded text document source, got %#v", source)
			}
			if source.Bytes != nil {
				t.Fatalf("expected DocumentSource union to contain only text, got bytes %#v", source.Bytes)
			}
		})
	}
}

func TestToolResultDocumentUnsupportedFileTypeRejected(t *testing.T) {
	input := []schemas.ResponsesMessage{
		toolResultDocumentMessage([]schemas.ResponsesMessageContentBlock{
			{
				Type: schemas.ResponsesInputMessageContentBlockTypeFile,
				ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
					FileData: schemas.Ptr("e30="),
					Filename: schemas.Ptr("result.json"),
					FileType: schemas.Ptr("application/json"),
				},
			},
		}),
	}

	messages, _, err := ConvertBifrostMessagesToBedrockMessages(context.Background(), input, false)
	if err == nil {
		t.Fatalf("expected unsupported-format error, got messages %#v", messages)
	}
	if !strings.Contains(err.Error(), `unsupported Bedrock document format "application/json"`) {
		t.Fatalf("expected explicit unsupported-format error, got %v", err)
	}
}

func TestToolResultDocumentURLFetchErrorReturned(t *testing.T) {
	input := []schemas.ResponsesMessage{
		toolResultDocumentMessage([]schemas.ResponsesMessageContentBlock{
			{
				Type: schemas.ResponsesInputMessageContentBlockTypeFile,
				ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
					FileURL:  schemas.Ptr("file:///tmp/report.pdf"),
					Filename: schemas.Ptr("report.pdf"),
					FileType: schemas.Ptr("application/pdf"),
				},
			},
		}),
	}

	messages, _, err := ConvertBifrostMessagesToBedrockMessages(context.Background(), input, false)
	if err == nil {
		t.Fatalf("expected URL materialization error, got messages %#v", messages)
	}
	if !strings.Contains(err.Error(), "unsupported URL scheme") {
		t.Fatalf("expected bounded URL fetch error, got %v", err)
	}
}

func TestToolResultDocumentURLUsesSSRFSafeFetcher(t *testing.T) {
	var reached atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached.Store(true)
	}))
	defer server.Close()

	input := []schemas.ResponsesMessage{
		toolResultDocumentMessage([]schemas.ResponsesMessageContentBlock{
			{
				Type: schemas.ResponsesInputMessageContentBlockTypeFile,
				ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
					FileURL:  schemas.Ptr(server.URL + "/report.pdf"),
					Filename: schemas.Ptr("report.pdf"),
					FileType: schemas.Ptr("application/pdf"),
				},
			},
		}),
	}

	messages, _, err := ConvertBifrostMessagesToBedrockMessages(context.Background(), input, false)
	if err == nil {
		t.Fatalf("expected loopback URL to be rejected, got messages %#v", messages)
	}
	if !strings.Contains(err.Error(), "blocked connection to non-public address") {
		t.Fatalf("expected SSRF-safe fetch rejection, got %v", err)
	}
	if reached.Load() {
		t.Fatal("SSRF-safe fetcher reached the loopback server")
	}
}

func TestToolResultDocumentAnthropicToBedrockRoundTrip(t *testing.T) {
	request := &anthropic.AnthropicMessageRequest{
		Model:     "bedrock/anthropic.claude-3-5-sonnet-v2",
		MaxTokens: 1024,
		Messages: []anthropic.AnthropicMessage{
			{
				Role: anthropic.AnthropicMessageRoleAssistant,
				Content: anthropic.AnthropicContent{ContentBlocks: []anthropic.AnthropicContentBlock{
					{
						Type:  anthropic.AnthropicContentBlockTypeToolUse,
						ID:    schemas.Ptr("toolu_document"),
						Name:  schemas.Ptr("create_report"),
						Input: []byte(`{"topic":"quarterly results"}`),
					},
				}},
			},
			{
				Role: anthropic.AnthropicMessageRoleUser,
				Content: anthropic.AnthropicContent{ContentBlocks: []anthropic.AnthropicContentBlock{
					{
						Type:      anthropic.AnthropicContentBlockTypeToolResult,
						ToolUseID: schemas.Ptr("toolu_document"),
						Content: &anthropic.AnthropicContent{ContentBlocks: []anthropic.AnthropicContentBlock{
							{
								Type: anthropic.AnthropicContentBlockTypeText,
								Text: schemas.Ptr("Document generated"),
							},
							{
								Type:  anthropic.AnthropicContentBlockTypeDocument,
								Title: schemas.Ptr("report.pdf"),
								Source: &anthropic.AnthropicBlockSource{SourceObj: &anthropic.AnthropicSource{
									Type:      "base64",
									MediaType: schemas.Ptr("application/pdf"),
									Data:      schemas.Ptr("JVBERi0xLjQ="),
								}},
							},
						}},
					},
				}},
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bifrostRequest := request.ToBifrostResponsesRequest(ctx)
	bedrockRequest, err := ToBedrockResponsesRequest(ctx, bifrostRequest)
	if err != nil {
		t.Fatalf("unexpected cross-provider conversion error: %v", err)
	}
	if len(bedrockRequest.Messages) != 2 {
		t.Fatalf("expected assistant tool use and user tool result, got %d messages", len(bedrockRequest.Messages))
	}
	toolResult := bedrockRequest.Messages[1].Content[0].ToolResult
	if toolResult == nil || len(toolResult.Content) != 2 {
		t.Fatalf("expected text and document in final tool result, got %#v", toolResult)
	}
	if toolResult.Content[0].Text == nil || toolResult.Content[1].Document == nil {
		t.Fatalf("expected text then document, got %#v", toolResult.Content)
	}
	document := toolResult.Content[1].Document
	if document.Name != "report_pdf" || document.Format != "pdf" {
		t.Fatalf("expected normalized report_pdf metadata with pdf format, got %#v", document)
	}
	if document.Source == nil || document.Source.Bytes == nil || *document.Source.Bytes != "JVBERi0xLjQ=" {
		t.Fatalf("expected original inline document bytes, got %#v", document.Source)
	}
	if document.Source.Text != nil {
		t.Fatalf("expected binary document source to contain only bytes, got text %q", *document.Source.Text)
	}
}

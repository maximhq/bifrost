package bedrock

import (
	"context"
	"net/http"
	"net/http/httptest"
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

func requireBedrockToolResult(t *testing.T, messages []BedrockMessage) *BedrockToolResult {
	t.Helper()
	if len(messages) != 1 || len(messages[0].Content) != 1 || messages[0].Content[0].ToolResult == nil {
		t.Fatalf("expected one Bedrock tool result, got %#v", messages)
	}
	return messages[0].Content[0].ToolResult
}

func toolResultDocumentInput(file *schemas.ResponsesInputMessageContentBlockFile) []schemas.ResponsesMessage {
	return []schemas.ResponsesMessage{
		toolResultDocumentMessage([]schemas.ResponsesMessageContentBlock{{
			Type:                                  schemas.ResponsesInputMessageContentBlockTypeFile,
			ResponsesInputMessageContentBlockFile: file,
		}}),
	}
}

func convertToolResultDocument(t *testing.T, file *schemas.ResponsesInputMessageContentBlockFile) *BedrockDocumentSource {
	t.Helper()
	messages, _, err := ConvertBifrostMessagesToBedrockMessages(context.Background(), toolResultDocumentInput(file), false)
	if err != nil {
		t.Fatalf("unexpected conversion error: %v", err)
	}
	toolResult := requireBedrockToolResult(t, messages)
	if len(toolResult.Content) != 1 || toolResult.Content[0].Document == nil {
		t.Fatalf("expected one document block, got %#v", toolResult.Content)
	}
	return toolResult.Content[0].Document
}

func TestToolResultDocumentInlinePreserved(t *testing.T) {
	input := []schemas.ResponsesMessage{
		toolResultDocumentMessage([]schemas.ResponsesMessageContentBlock{
			{Type: schemas.ResponsesInputMessageContentBlockTypeText, Text: schemas.Ptr("Document generated")},
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
	toolResult := requireBedrockToolResult(t, messages)
	if len(toolResult.Content) != 2 || toolResult.Content[0].Text == nil || toolResult.Content[1].Document == nil {
		t.Fatalf("expected text then document, got %#v", toolResult.Content)
	}
	if *toolResult.Content[0].Text != "Document generated" {
		t.Fatalf("expected original text first, got %q", *toolResult.Content[0].Text)
	}
	document := toolResult.Content[1].Document
	if document.Name != "quarterly_report_pdf" || document.Format != "pdf" {
		t.Fatalf("expected normalized PDF metadata, got %#v", document)
	}
	if document.Source == nil || document.Source.Bytes == nil || *document.Source.Bytes != "JVBERi0xLjQ=" {
		t.Fatalf("expected original inline bytes, got %#v", document.Source)
	}
}

func TestToolResultDocumentKeepsDeclaredFormatForOpaqueDataURL(t *testing.T) {
	document := convertToolResultDocument(t, &schemas.ResponsesInputMessageContentBlockFile{
		FileData: schemas.Ptr("data:application/octet-stream;base64,QQ=="),
		Filename: schemas.Ptr("report.csv"),
		FileType: schemas.Ptr("text/csv"),
	})
	if document.Format != "csv" || document.Source == nil || document.Source.Bytes == nil || *document.Source.Bytes != "QQ==" {
		t.Fatalf("expected declared CSV format with original bytes, got %#v", document)
	}
}

func TestToolResultDocumentS3URIUsesS3Location(t *testing.T) {
	s3URI := "s3://reports/quarterly/q4.pdf"
	document := convertToolResultDocument(t, &schemas.ResponsesInputMessageContentBlockFile{
		FileURL: &s3URI,
	})

	if document.Format != "pdf" || document.Source == nil || document.Source.S3Location == nil {
		t.Fatalf("expected PDF backed by an S3 location, got %#v", document)
	}
	if document.Source.S3Location.URI != s3URI {
		t.Fatalf("expected S3 URI %q, got %q", s3URI, document.Source.S3Location.URI)
	}
	if document.Source.Bytes != nil || document.Source.Text != nil {
		t.Fatalf("expected DocumentSource union to contain only s3Location, got %#v", document.Source)
	}
}

func TestToolResultDocumentRejectsUnsupportedExplicitTypes(t *testing.T) {
	tests := []struct {
		name string
		file *schemas.ResponsesInputMessageContentBlockFile
	}{
		{
			name: "declared JSON",
			file: &schemas.ResponsesInputMessageContentBlockFile{FileData: schemas.Ptr("e30="), FileType: schemas.Ptr("application/json")},
		},
		{
			name: "declared ZIP",
			file: &schemas.ResponsesInputMessageContentBlockFile{FileData: schemas.Ptr("UEsDBA=="), FileType: schemas.Ptr("application/zip")},
		},
		{
			name: "declared PNG",
			file: &schemas.ResponsesInputMessageContentBlockFile{FileData: schemas.Ptr("iVBORw=="), FileType: schemas.Ptr("image/png")},
		},
		{
			name: "data URL JSON",
			file: &schemas.ResponsesInputMessageContentBlockFile{FileData: schemas.Ptr("data:application/json;base64,e30=")},
		},
		{
			name: "data URL ZIP",
			file: &schemas.ResponsesInputMessageContentBlockFile{FileData: schemas.Ptr("data:application/zip;base64,UEsDBA==")},
		},
		{
			name: "data URL PNG",
			file: &schemas.ResponsesInputMessageContentBlockFile{FileData: schemas.Ptr("data:image/png;base64,iVBORw==")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			messages, _, err := ConvertBifrostMessagesToBedrockMessages(context.Background(), toolResultDocumentInput(tt.file), false)
			if err == nil {
				t.Fatalf("expected unsupported document format error, got %#v", messages)
			}
		})
	}
}

func TestToolResultDocumentRejectsMissingSource(t *testing.T) {
	file := &schemas.ResponsesInputMessageContentBlockFile{
		Filename: schemas.Ptr("report.pdf"),
		FileType: schemas.Ptr("application/pdf"),
	}
	messages, _, err := ConvertBifrostMessagesToBedrockMessages(context.Background(), toolResultDocumentInput(file), false)
	if err == nil {
		t.Fatalf("expected missing document source error, got %#v", messages)
	}
}

func TestToolResultTextDocumentUsesSingleSourceMember(t *testing.T) {
	tests := []struct {
		name     string
		fileData string
	}{
		{name: "literal text", fileData: "Hello from the tool"},
		{name: "base64 data URL", fileData: "data:text/plain;base64,SGVsbG8gZnJvbSB0aGUgdG9vbA=="},
		{name: "mixed-case data URL", fileData: "DaTa:text/plain;base64,SGVsbG8gZnJvbSB0aGUgdG9vbA=="},
		{name: "percent-encoded data URL", fileData: "data:text/plain,Hello%20from%20the%20tool"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			document := convertToolResultDocument(t, &schemas.ResponsesInputMessageContentBlockFile{
				FileData: &tt.fileData,
				Filename: schemas.Ptr("result.txt"),
				FileType: schemas.Ptr("text/plain"),
			})
			if document.Format != "txt" || document.Source == nil || document.Source.Text == nil || *document.Source.Text != "Hello from the tool" {
				t.Fatalf("expected plain text document source, got %#v", document)
			}
			if document.Source.Bytes != nil {
				t.Fatalf("expected DocumentSource union to contain only text, got bytes %#v", document.Source.Bytes)
			}
		})
	}
}

func TestToolResultDocumentURLFetchErrorReturned(t *testing.T) {
	input := []schemas.ResponsesMessage{
		toolResultDocumentMessage([]schemas.ResponsesMessageContentBlock{{
			Type: schemas.ResponsesInputMessageContentBlockTypeFile,
			ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
				FileURL:  schemas.Ptr("file:///tmp/report.pdf"),
				Filename: schemas.Ptr("report.pdf"),
				FileType: schemas.Ptr("application/pdf"),
			},
		}}),
	}

	messages, _, err := ConvertBifrostMessagesToBedrockMessages(context.Background(), input, false)
	if err == nil {
		t.Fatalf("expected bounded URL fetch error, got messages=%#v err=%v", messages, err)
	}
}

func TestToolResultDocumentURLUsesSSRFSafeFetcher(t *testing.T) {
	var reached atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached.Store(true)
	}))
	defer server.Close()

	input := []schemas.ResponsesMessage{
		toolResultDocumentMessage([]schemas.ResponsesMessageContentBlock{{
			Type: schemas.ResponsesInputMessageContentBlockTypeFile,
			ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
				FileURL:  schemas.Ptr(server.URL + "/report.pdf"),
				Filename: schemas.Ptr("report.pdf"),
				FileType: schemas.Ptr("application/pdf"),
			},
		}}),
	}

	messages, _, err := ConvertBifrostMessagesToBedrockMessages(context.Background(), input, false)
	if err == nil {
		t.Fatalf("expected SSRF-safe fetch rejection, got messages=%#v err=%v", messages, err)
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
				Content: anthropic.AnthropicContent{ContentBlocks: []anthropic.AnthropicContentBlock{{
					Type: anthropic.AnthropicContentBlockTypeToolUse,
					ID:   schemas.Ptr("toolu_document"), Name: schemas.Ptr("create_report"),
					Input: []byte(`{"topic":"quarterly results"}`),
				}}},
			},
			{
				Role: anthropic.AnthropicMessageRoleUser,
				Content: anthropic.AnthropicContent{ContentBlocks: []anthropic.AnthropicContentBlock{{
					Type: anthropic.AnthropicContentBlockTypeToolResult, ToolUseID: schemas.Ptr("toolu_document"),
					Content: &anthropic.AnthropicContent{ContentBlocks: []anthropic.AnthropicContentBlock{
						{Type: anthropic.AnthropicContentBlockTypeText, Text: schemas.Ptr("Document generated")},
						{
							Type: anthropic.AnthropicContentBlockTypeDocument, Title: schemas.Ptr("report.pdf"),
							Source: &anthropic.AnthropicBlockSource{SourceObj: &anthropic.AnthropicSource{
								Type: "base64", MediaType: schemas.Ptr("application/pdf"), Data: schemas.Ptr("JVBERi0xLjQ="),
							}},
						},
					}},
				}}},
			},
		},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bedrockRequest, err := ToBedrockResponsesRequest(ctx, request.ToBifrostResponsesRequest(ctx))
	if err != nil {
		t.Fatalf("unexpected cross-provider conversion error: %v", err)
	}
	if len(bedrockRequest.Messages) != 2 {
		t.Fatalf("expected assistant tool use and user tool result, got %d messages", len(bedrockRequest.Messages))
	}
	toolResult := bedrockRequest.Messages[1].Content[0].ToolResult
	if toolResult == nil || len(toolResult.Content) != 2 || toolResult.Content[0].Text == nil || toolResult.Content[1].Document == nil {
		t.Fatalf("expected text then document in final tool result, got %#v", toolResult)
	}
	document := toolResult.Content[1].Document
	if document.Name != "report_pdf" || document.Format != "pdf" || document.Source == nil || document.Source.Bytes == nil || *document.Source.Bytes != "JVBERi0xLjQ=" {
		t.Fatalf("expected normalized PDF with original inline bytes, got %#v", document)
	}
}

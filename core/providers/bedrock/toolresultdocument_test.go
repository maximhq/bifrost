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
}

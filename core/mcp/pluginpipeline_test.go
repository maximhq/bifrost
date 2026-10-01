package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/schemas"
)

type redactingRawResultPipeline struct{}

func (redactingRawResultPipeline) RunMCPPreHooks(_ *schemas.BifrostContext, req *schemas.BifrostMCPRequest) (*schemas.BifrostMCPRequest, *schemas.MCPPluginShortCircuit, int) {
	return req, nil, 1
}

func (redactingRawResultPipeline) RunMCPPostHooks(_ *schemas.BifrostContext, response *schemas.BifrostMCPResponse, bifrostErr *schemas.BifrostError, _ int) (*schemas.BifrostMCPResponse, *schemas.BifrostError) {
	redacted := "Attachment [redacted]"
	response.ChatMessage.Content = &schemas.ChatMessageContent{ContentStr: &redacted}
	response.ChatMessage.ChatToolMessage.IsError = schemas.Ptr(false)
	return response, bifrostErr
}

func (redactingRawResultPipeline) RunMCPPreConnectionHooks(_ *schemas.BifrostContext, req *schemas.BifrostMCPConnectRequest) (*schemas.BifrostMCPConnectRequest, *schemas.MCPConnectionShortCircuit, int) {
	return req, nil, 1
}

func (redactingRawResultPipeline) RunMCPPostConnectionHooks(_ *schemas.BifrostContext, response *schemas.BifrostMCPConnectResponse, bifrostErr *schemas.BifrostError, _ int) (*schemas.BifrostMCPConnectResponse, *schemas.BifrostError) {
	return response, bifrostErr
}

func TestRunWithPluginPipelineAppliesChatRedactionToRawResult(t *testing.T) {
	textMeta := &mcpgo.Meta{AdditionalFields: map[string]any{"source": "fixture"}}
	raw := &mcpgo.CallToolResult{
		Result: mcpgo.Result{Meta: &mcpgo.Meta{AdditionalFields: map[string]any{
			"fixture": "mcp-gateway-rich-result",
		}}},
		Content: []mcpgo.Content{
			mcpgo.TextContent{Type: mcpgo.ContentTypeText, Text: "Attachment secret.pdf", Meta: textMeta},
			mcpgo.ImageContent{Type: mcpgo.ContentTypeImage, Data: "aW1hZ2U=", MIMEType: "image/png"},
			mcpgo.TextContent{Type: mcpgo.ContentTypeText, Text: " second secret"},
			mcpgo.EmbeddedResource{
				Type: mcpgo.ContentTypeResource,
				Resource: mcpgo.BlobResourceContents{
					URI:      "gmail-attachment://message/report.pdf",
					MIMEType: "application/pdf",
					Blob:     "JVBERi0xLjQK",
				},
			},
		},
		StructuredContent: map[string]any{"filename": "secret.pdf", "size": 606},
		IsError:           true,
	}
	toolCallID := "call-1"
	toolName := "gmail-download_attachment"
	toolCall := schemas.ChatAssistantMessageToolCall{
		ID: &toolCallID,
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name: &toolName,
		},
	}
	response := &schemas.BifrostMCPResponse{
		ChatMessage:   createToolResponseMessage(toolCall, extractTextFromMCPResponse(raw, toolName), raw.IsError),
		MCPToolResult: raw,
	}
	pipeline := redactingRawResultPipeline{}
	manager := &MCPManager{
		pluginPipelineProvider: func() PluginPipeline { return pipeline },
		releasePluginPipeline:  func(PluginPipeline) {},
	}
	request := &schemas.BifrostMCPRequest{
		RequestType:                  schemas.MCPRequestTypeChatToolCall,
		ChatAssistantMessageToolCall: &toolCall,
	}

	got, bifrostErr := manager.RunWithPluginPipeline(
		schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
		request,
		func(*schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) { return response, nil },
	)
	if bifrostErr != nil {
		t.Fatalf("RunWithPluginPipeline returned an error: %v", bifrostErr)
	}
	gotRaw, ok := got.MCPToolResult.(*mcpgo.CallToolResult)
	if !ok || gotRaw == nil {
		t.Fatalf("expected native result, got %#v", got.MCPToolResult)
	}
	if len(gotRaw.Content) != 4 {
		t.Fatalf("content block count = %d, want 4", len(gotRaw.Content))
	}
	firstText, ok := gotRaw.Content[0].(mcpgo.TextContent)
	if !ok || firstText.Text != "Attachment [redacted]" {
		t.Fatalf("first text block = %#v, want redacted text", gotRaw.Content[0])
	}
	if firstText.Meta != textMeta {
		t.Fatalf("text metadata was not preserved: %#v", firstText.Meta)
	}
	if _, ok := gotRaw.Content[1].(mcpgo.ImageContent); !ok {
		t.Fatalf("image block was not preserved: %#v", gotRaw.Content[1])
	}
	secondText, ok := gotRaw.Content[2].(mcpgo.TextContent)
	if !ok || secondText.Text != "" {
		t.Fatalf("second text block = %#v, want preserved empty text block", gotRaw.Content[2])
	}
	if _, ok := gotRaw.Content[3].(mcpgo.EmbeddedResource); !ok {
		t.Fatalf("resource block was not preserved: %#v", gotRaw.Content[3])
	}
	structuredContent, ok := gotRaw.StructuredContent.(map[string]any)
	if !ok || structuredContent["filename"] != "secret.pdf" {
		t.Fatalf("structured content was not preserved: %#v", gotRaw.StructuredContent)
	}
	if gotRaw.Meta == nil || gotRaw.Meta.AdditionalFields["fixture"] != "mcp-gateway-rich-result" {
		t.Fatalf("result metadata was not preserved: %#v", gotRaw.Meta)
	}
	if gotRaw.IsError {
		t.Fatal("post-hook isError mutation was not applied to the native result")
	}
}

// resultReturningPipeline exercises the existing public APIs without an upstream server.
type resultReturningPipeline struct {
	redactingRawResultPipeline
	response *schemas.BifrostMCPResponse
	redact   bool
}

func (p resultReturningPipeline) RunMCPPreHooks(_ *schemas.BifrostContext, req *schemas.BifrostMCPRequest) (*schemas.BifrostMCPRequest, *schemas.MCPPluginShortCircuit, int) {
	return req, &schemas.MCPPluginShortCircuit{Response: p.response}, 1
}

func (p resultReturningPipeline) RunMCPPostHooks(ctx *schemas.BifrostContext, response *schemas.BifrostMCPResponse, err *schemas.BifrostError, count int) (*schemas.BifrostMCPResponse, *schemas.BifrostError) {
	if p.redact {
		return p.redactingRawResultPipeline.RunMCPPostHooks(ctx, response, err, count)
	}
	return response, err
}

func TestExistingMCPExecuteAPIsPreserveNativeResult(t *testing.T) {
	for _, responses := range []bool{false, true} {
		t.Run(fmt.Sprintf("responses=%v", responses), func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			native := mcpgo.NewToolResultText("legacy text")
			message := createToolResponseMessage(schemas.ChatAssistantMessageToolCall{ID: schemas.Ptr("call-1")}, "legacy text", false)
			response := &schemas.BifrostMCPResponse{ChatMessage: message, MCPToolResult: native}
			if responses {
				response.ChatMessage = nil
				response.ResponsesMessage = message.ToResponsesToolMessage()
			}
			pipeline := resultReturningPipeline{response: response}
			manager := &MCPManager{
				pluginPipelineProvider: func() PluginPipeline { return pipeline },
				releasePluginPipeline:  func(PluginPipeline) {},
			}
			var result any
			var wireMessage any
			if responses {
				got, err := manager.ExecuteResponsesTool(ctx, &schemas.ResponsesToolMessage{Name: schemas.Ptr("fixture-tool"), CallID: schemas.Ptr("call-1")})
				if err != nil || got == nil || got.ResponsesToolMessage == nil {
					t.Fatalf("existing Responses API failed: result=%+v error=%+v", got, err)
				}
				if got.Output == nil || got.Output.ResponsesToolCallOutputStr == nil || *got.Output.ResponsesToolCallOutputStr != "legacy text" {
					t.Fatal("legacy Responses text result changed")
				}
				result = got.MCPToolResult
				wireMessage = got
			} else {
				got, err := manager.ExecuteChatTool(ctx, &schemas.ChatAssistantMessageToolCall{Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("fixture-tool")}})
				if err != nil || got == nil || got.ChatToolMessage == nil {
					t.Fatalf("existing Chat API failed: result=%+v error=%+v", got, err)
				}
				if got.Content == nil || got.Content.ContentStr == nil || *got.Content.ContentStr != "legacy text" {
					t.Fatal("legacy Chat text result changed")
				}
				result = got.GetMCPToolResult()
				wireMessage = got
			}
			encoded, marshalErr := json.Marshal(wireMessage)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			var fields map[string]any
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatal(err)
			}
			if _, exists := fields["MCPToolResult"]; exists {
				t.Fatal("native MCP result leaked into LLM wire payload")
			}
			if result != native {
				t.Fatalf("existing API lost the native result: %+v", result)
			}
		})
	}
}

func TestExistingChatMCPAPIAppliesPostHookRedaction(t *testing.T) {
	native := mcpgo.NewToolResultText("Attachment secret.pdf")
	message := createToolResponseMessage(schemas.ChatAssistantMessageToolCall{ID: schemas.Ptr("call-1")}, "Attachment secret.pdf", false)
	pipeline := resultReturningPipeline{response: &schemas.BifrostMCPResponse{ChatMessage: message, MCPToolResult: native}, redact: true}
	manager := &MCPManager{pluginPipelineProvider: func() PluginPipeline { return pipeline }, releasePluginPipeline: func(PluginPipeline) {}}
	got, err := manager.ExecuteChatTool(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), &schemas.ChatAssistantMessageToolCall{Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("fixture-tool")}})
	if err != nil {
		t.Fatal(err)
	}
	result, ok := got.GetMCPToolResult().(*mcpgo.CallToolResult)
	if !ok || result == nil {
		t.Fatal("missing native result")
	}
	text, ok := result.Content[0].(mcpgo.TextContent)
	if !ok || text.Text != "Attachment [redacted]" {
		t.Fatalf("native result bypassed post-hook redaction: %+v", result)
	}
}

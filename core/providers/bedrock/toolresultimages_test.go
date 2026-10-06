package bedrock

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const toolResultImageDataURL = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8DwHwAFBQIAX8jx0gAAAABJRU5ErkJggg=="

func toolResultImageBlock(label string) BedrockContentBlock {
	return BedrockContentBlock{Image: &BedrockImageSource{Format: label}}
}

func toolResultBlock(id string, content ...BedrockContentBlock) BedrockContentBlock {
	return BedrockContentBlock{ToolResult: &BedrockToolResult{ToolUseID: id, Content: content}}
}

// describeBedrockBlocks renders blocks as short labels so expected layouts read at a glance.
func describeBedrockBlocks(blocks []BedrockContentBlock) []string {
	out := make([]string, 0, len(blocks))
	for _, b := range blocks {
		switch {
		case b.ToolResult != nil:
			out = append(out, fmt.Sprintf("toolResult(%s)[%s]", b.ToolResult.ToolUseID, strings.Join(describeBedrockBlocks(b.ToolResult.Content), ", ")))
		case b.Image != nil:
			out = append(out, "image:"+b.Image.Format)
		case b.Document != nil:
			out = append(out, "document")
		case b.CachePoint != nil:
			out = append(out, "cachePoint")
		case b.Text != nil:
			out = append(out, "text:"+*b.Text)
		default:
			out = append(out, "?")
		}
	}
	return out
}

func TestHoistToolResultImages(t *testing.T) {
	placeholder := "text:" + toolResultImagePlaceholder
	tests := []struct {
		name    string
		content []BedrockContentBlock
		want    []string
	}{
		{
			name:    "image-only result keeps a placeholder",
			content: []BedrockContentBlock{toolResultBlock("a", toolResultImageBlock("red"))},
			want:    []string{"toolResult(a)[" + placeholder + "]", "image:red"},
		},
		{
			name:    "text beside the image is kept without a placeholder",
			content: []BedrockContentBlock{toolResultBlock("a", BedrockContentBlock{Text: new("done")}, toolResultImageBlock("red"))},
			want:    []string{"toolResult(a)[text:done]", "image:red"},
		},
		{
			name: "parallel results come first and images follow in result order",
			content: []BedrockContentBlock{
				toolResultBlock("a", toolResultImageBlock("red")),
				toolResultBlock("b", toolResultImageBlock("blue")),
			},
			want: []string{"toolResult(a)[" + placeholder + "]", "toolResult(b)[" + placeholder + "]", "image:red", "image:blue"},
		},
		{
			name: "text-only sibling result is untouched",
			content: []BedrockContentBlock{
				toolResultBlock("a", toolResultImageBlock("red")),
				toolResultBlock("b", BedrockContentBlock{Text: new("closed")}),
			},
			want: []string{"toolResult(a)[" + placeholder + "]", "toolResult(b)[text:closed]", "image:red"},
		},
		{
			name:    "cachePoint after the last result ends up after the images",
			content: []BedrockContentBlock{toolResultBlock("a", toolResultImageBlock("red")), {CachePoint: cachePoint()}},
			want:    []string{"toolResult(a)[" + placeholder + "]", "image:red", "cachePoint"},
		},
		{
			name:    "trailing text stays after the images",
			content: []BedrockContentBlock{toolResultBlock("a", toolResultImageBlock("red")), {Text: new("next")}},
			want:    []string{"toolResult(a)[" + placeholder + "]", "image:red", "text:next"},
		},
		{
			name:    "documents stay nested",
			content: []BedrockContentBlock{toolResultBlock("a", BedrockContentBlock{Text: new("report")}, BedrockContentBlock{Document: &BedrockDocumentSource{}})},
			want:    []string{"toolResult(a)[text:report, document]"},
		},
		{
			name:    "results without images are left alone, empty ones included",
			content: []BedrockContentBlock{toolResultBlock("a"), toolResultBlock("b", BedrockContentBlock{Text: new("ok")})},
			want:    []string{"toolResult(a)[]", "toolResult(b)[text:ok]"},
		},
		{
			name:    "images outside tool results are left alone",
			content: []BedrockContentBlock{toolResultImageBlock("red"), {Text: new("describe this")}},
			want:    []string{"image:red", "text:describe this"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &BedrockConverseRequest{Messages: []BedrockMessage{{Role: BedrockMessageRoleUser, Content: tt.content}}}
			hoistToolResultImages(req)
			assert.Equal(t, tt.want, describeBedrockBlocks(req.Messages[0].Content))
		})
	}
}

func toolResultImageChatRequest(model string) *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: schemas.Bedrock,
		Model:    model,
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: new("Take a screenshot.")}},
			{Role: schemas.ChatMessageRoleAssistant, ChatAssistantMessage: &schemas.ChatAssistantMessage{
				ToolCalls: []schemas.ChatAssistantMessageToolCall{{
					ID:       new("call_a"),
					Type:     new("function"),
					Function: schemas.ChatAssistantMessageToolCallFunction{Name: new("get_screenshot"), Arguments: "{}"},
				}},
			}},
			{
				Role:            schemas.ChatMessageRoleTool,
				ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: new("call_a")},
				Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{{
					Type:           schemas.ChatContentBlockTypeImage,
					ImageURLStruct: &schemas.ChatInputImage{URL: toolResultImageDataURL},
				}}},
			},
		},
	}
}

func toolResultImageResponsesRequest(model string) *schemas.BifrostResponsesRequest {
	return &schemas.BifrostResponsesRequest{
		Provider: schemas.Bedrock,
		Model:    model,
		Input: []schemas.ResponsesMessage{
			{
				Type:    new(schemas.ResponsesMessageTypeMessage),
				Role:    new(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: new("Take a screenshot.")},
			},
			{
				Type:                 new(schemas.ResponsesMessageTypeFunctionCall),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{CallID: new("call_a"), Name: new("get_screenshot"), Arguments: new("{}")},
			},
			{
				Type: new(schemas.ResponsesMessageTypeFunctionCallOutput),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: new("call_a"),
					Output: &schemas.ResponsesToolMessageOutputStruct{
						ResponsesFunctionToolCallOutputBlocks: []schemas.ResponsesMessageContentBlock{{
							Type:                                   schemas.ResponsesInputMessageContentBlockTypeImage,
							ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{ImageURL: new(toolResultImageDataURL)},
						}},
					},
				},
			},
		},
	}
}

// toolResultImageLayout reports where the image landed in the last user message.
func toolResultImageLayout(t *testing.T, req *BedrockConverseRequest) string {
	t.Helper()
	for i := len(req.Messages) - 1; i >= 0; i-- {
		msg := req.Messages[i]
		if msg.Role != BedrockMessageRoleUser {
			continue
		}
		layout := describeBedrockBlocks(msg.Content)
		joined := strings.Join(layout, " | ")
		require.Contains(t, joined, "toolResult(call_a)", "last user message carries no tool result: %v", layout)
		switch {
		case strings.Contains(joined, "toolResult(call_a)[image:"):
			return "nested"
		case len(layout) == 2 && layout[0] == "toolResult(call_a)[text:"+toolResultImagePlaceholder+"]" && strings.HasPrefix(layout[1], "image:"):
			return "hoisted"
		default:
			t.Fatalf("unexpected layout: %v", layout)
		}
	}
	t.Fatal("no user message")
	return ""
}

func TestToolResultImagesGate(t *testing.T) {
	tests := []struct {
		model string
		row   string
		caps  *schemas.ModelCapabilities
		want  string
	}{
		{model: "us.openai.gpt-5.6-luna", want: "hoisted"},
		{model: "global.openai.gpt-6-astra", want: "hoisted"},
		{model: "us.xai.grok-4.6", want: "hoisted"},
		{model: "us.anthropic.claude-sonnet-4-6", want: "nested"},
		{model: "us.amazon.nova-pro-v1:0", want: "nested"},
		// Llama 4 takes nested images and rejects the hoisted layout.
		{model: "us.meta.llama4-maverick-17b-instruct-v1:0", want: "nested"},
		{model: "qwen.qwen3-vl-235b-a22b", row: "converse_false", caps: &schemas.ModelCapabilities{SupportsConverseToolResultImages: new(false)}, want: "hoisted"},
		{model: "us.openai.gpt-5.6-luna", row: "converse_true", caps: &schemas.ModelCapabilities{SupportsConverseToolResultImages: new(true)}, want: "nested"},
		// The generic multimodal tool-output flag describes the model, not Converse.
		{model: "us.openai.gpt-5.6-luna", row: "multimodal_tool_output_true", caps: &schemas.ModelCapabilities{SupportsMultimodalToolOutput: new(true)}, want: "hoisted"},
	}
	for _, tt := range tests {
		name := tt.model
		if tt.row != "" {
			name += "/" + tt.row
		}
		t.Run(name, func(t *testing.T) {
			if tt.caps != nil {
				schemas.SetCapabilityResolver(func(schemas.ModelProvider, string) *schemas.ModelCapabilities { return tt.caps })
				t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })
			}
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

			chatReq, err := ToBedrockChatCompletionRequest(ctx, toolResultImageChatRequest(tt.model))
			require.NoError(t, err)
			assert.Equal(t, tt.want, toolResultImageLayout(t, chatReq), "chat path")

			responsesReq, err := ToBedrockResponsesRequest(ctx, toolResultImageResponsesRequest(tt.model))
			require.NoError(t, err)
			assert.Equal(t, tt.want, toolResultImageLayout(t, responsesReq), "responses path")
		})
	}
}

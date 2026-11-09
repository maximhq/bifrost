package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// TestToolResultIsErrorStrippedFromOpenAIWire verifies that IsError — an
// Anthropic-family marker with no OpenAI wire equivalent — never serializes
// into an OpenAI message. OpenAI-compatible providers reject unknown message
// parameters (see the Responses-path incident with `input[N].error`), so the
// converter must strip it rather than forward it.
func TestToolResultIsErrorStrippedFromOpenAIWire(t *testing.T) {
	original := &schemas.ChatToolMessage{
		ToolCallID: schemas.Ptr("call_1"),
		IsError:    schemas.Ptr(true),
	}
	messages := []schemas.ChatMessage{
		{
			Role:            schemas.ChatMessageRoleTool,
			ChatToolMessage: original,
			Content:         &schemas.ChatMessageContent{ContentStr: schemas.Ptr("command exited with code 1")},
		},
	}

	converted := ConvertBifrostMessagesToOpenAIMessages(messages)
	if len(converted) != 1 {
		t.Fatalf("expected 1 converted message, got %d", len(converted))
	}
	if converted[0].ChatToolMessage == nil {
		t.Fatal("expected tool message fields to survive conversion")
	}
	if converted[0].ChatToolMessage.IsError != nil {
		t.Fatal("IsError must be stripped from the OpenAI-wire message")
	}
	if converted[0].ChatToolMessage.ToolCallID == nil || *converted[0].ChatToolMessage.ToolCallID != "call_1" {
		t.Fatal("tool_call_id must survive the strip")
	}

	wire, err := schemas.MarshalSorted(converted[0])
	if err != nil {
		t.Fatalf("marshal converted message: %v", err)
	}
	if strings.Contains(string(wire), "is_error") {
		t.Fatalf("serialized OpenAI message must not contain is_error, got: %s", wire)
	}

	// The caller's input is shared; the strip must clone, never mutate.
	if original.IsError == nil || !*original.IsError {
		t.Fatal("caller's ChatToolMessage must not be mutated by the strip")
	}
}

// TestToolResultIsErrorSurvivesAnthropicToOpenAIResponsesRoundTrip walks a
// tool_result flagged is_error through the whole /anthropic/v1/messages ->
// /v1/responses path. The Anthropic converter carries the flag as
// status:"incomplete" on the function_call_output item, which is the only
// marker the Responses surface has for a failed tool result -- the chat path
// strips is_error on the OpenAI wire by design. Blanket-stripping status in
// ToOpenAIResponsesRequest made a failed tool call indistinguishable from a
// successful one.
func TestToolResultIsErrorSurvivesAnthropicToOpenAIResponsesRoundTrip(t *testing.T) {
	const body = `{
		"max_tokens": 64,
		"model": "openai/gpt-4o",
		"tools": [{"name":"run_cmd","description":"run a shell command","input_schema":{"type":"object","properties":{"cmd":{"type":"string"}}}}],
		"messages": [
			{"role":"user","content":"Run ls /root"},
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"run_cmd","input":{"cmd":"ls /root"}},{"type":"tool_use","id":"toolu_2","name":"run_cmd","input":{"cmd":"ls /tmp"}}]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"toolu_1","content":"permission denied","is_error":true},
				{"type":"tool_result","tool_use_id":"toolu_2","content":"ok"}
			]}
		]
	}`

	var anthropicReq anthropic.AnthropicMessageRequest
	if err := json.Unmarshal([]byte(body), &anthropicReq); err != nil {
		t.Fatalf("unmarshal anthropic request: %v", err)
	}

	bifrostReq := anthropicReq.ToBifrostResponsesRequest(nil)
	if bifrostReq == nil {
		t.Fatal("ToBifrostResponsesRequest returned nil")
	}

	openaiReq := ToOpenAIResponsesRequest(nil, bifrostReq)
	if openaiReq == nil || len(openaiReq.Input.OpenAIResponsesRequestInputArray) == 0 {
		t.Fatal("ToOpenAIResponsesRequest returned no input")
	}

	statusFor := func(callID string) (*schemas.ResponsesMessage, bool) {
		for i, item := range openaiReq.Input.OpenAIResponsesRequestInputArray {
			if item.Type == nil || *item.Type != schemas.ResponsesMessageTypeFunctionCallOutput {
				continue
			}
			if item.ResponsesToolMessage != nil && item.ResponsesToolMessage.CallID != nil &&
				*item.ResponsesToolMessage.CallID == callID {
				return &openaiReq.Input.OpenAIResponsesRequestInputArray[i], true
			}
		}
		return nil, false
	}

	failed, ok := statusFor("toolu_1")
	if !ok {
		t.Fatal("no function_call_output item for the failed tool result")
	}
	if failed.Status == nil || *failed.Status != "incomplete" {
		t.Fatalf("expected the failed tool result to keep status \"incomplete\", got %#v", failed.Status)
	}

	succeeded, ok := statusFor("toolu_2")
	if !ok {
		t.Fatal("no function_call_output item for the successful tool result")
	}
	if succeeded.Status != nil {
		t.Fatalf("expected no status on the successful tool result, got %q", *succeeded.Status)
	}

	// The marker has to reach the wire, not just the struct, and nothing else
	// may ride along: `error` on an input item is rejected by OpenAI.
	wire, err := schemas.MarshalSorted(failed)
	if err != nil {
		t.Fatalf("marshal function_call_output: %v", err)
	}
	if !strings.Contains(string(wire), `"status":"incomplete"`) {
		t.Fatalf("serialized function_call_output must carry the incomplete status, got: %s", wire)
	}
	if strings.Contains(string(wire), `"error"`) {
		t.Fatalf("serialized function_call_output must not carry an error field, got: %s", wire)
	}
}

// Strict Chat accepts images in user content, not tool content. The image must
// survive on the serialized wire without interrupting parallel tool results.
func TestToolResultImagesHoistedOnOpenAIWire(t *testing.T) {
	const png = "data:image/png;base64,iVBORw0KGgo="
	const jpeg = "data:image/jpeg;base64,/9j/2w=="
	text := func(s string) schemas.ChatContentBlock {
		return schemas.ChatContentBlock{Type: schemas.ChatContentBlockTypeText, Text: schemas.Ptr(s)}
	}
	image := func(url string) schemas.ChatContentBlock {
		return schemas.ChatContentBlock{Type: schemas.ChatContentBlockTypeImage, ImageURLStruct: &schemas.ChatInputImage{URL: url, Detail: schemas.Ptr("low")}}
	}
	tool := func(id string, blocks ...schemas.ChatContentBlock) schemas.ChatMessage {
		return schemas.ChatMessage{
			Role: schemas.ChatMessageRoleTool, ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr(id)},
			Content: &schemas.ChatMessageContent{ContentBlocks: blocks},
		}
	}
	user := func(s string) schemas.ChatMessage {
		return schemas.ChatMessage{Role: schemas.ChatMessageRoleUser, Name: schemas.Ptr("reader"), Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr(s)}}
	}
	audio := schemas.ChatContentBlock{Type: schemas.ChatContentBlockTypeInputAudio, InputAudio: &schemas.ChatInputAudio{Data: "SUQz", Format: schemas.Ptr("mp3")}}
	file := schemas.ChatContentBlock{Type: schemas.ChatContentBlockTypeFile, File: &schemas.ChatInputFile{Filename: schemas.Ptr("report.pdf"), FileData: schemas.Ptr("JVBERg=="), FileType: schemas.Ptr("application/pdf")}}
	tests := []struct {
		name         string
		input        []schemas.ChatMessage
		wantRoles    string
		wantToolText []string
		wantUserText []string
		wantImages   []string
	}{
		{
			name: "image only at end", input: []schemas.ChatMessage{tool("call_a", image(png))},
			wantRoles: "tool,user", wantToolText: []string{"Image attached below."},
			wantUserText: []string{"Images returned by tool call call_a:"}, wantImages: []string{png},
		},
		{
			name: "mixed content", input: []schemas.ChatMessage{tool("call_a", text("before"), image(png), text("after"), audio, file)},
			wantRoles:    "tool,user",
			wantToolText: []string{"before", "Image attached below.", "after", "[audio/mp3, 3 bytes returned by the tool, not shown]", "[application/pdf report.pdf, 6 bytes returned by the tool, not shown]"},
			wantUserText: []string{"Images returned by tool call call_a:"}, wantImages: []string{png},
		},
		{
			name: "refusal remains tool text", input: []schemas.ChatMessage{tool("call_a", schemas.ChatContentBlock{Type: schemas.ChatContentBlockTypeRefusal, Refusal: schemas.Ptr("not available")}, image(png))},
			wantRoles: "tool,user", wantToolText: []string{"not available", "Image attached below."},
			wantUserText: []string{"Images returned by tool call call_a:"}, wantImages: []string{png},
		},
		{
			name: "multiple images precede next user string", input: []schemas.ChatMessage{tool("call_a", image(png), image(jpeg)), user("describe both")},
			wantRoles: "tool,user", wantToolText: []string{"Image attached below.", "Image attached below."},
			wantUserText: []string{"Images returned by tool call call_a:", "describe both"}, wantImages: []string{png, jpeg},
		},
		{
			name: "next user blocks preserved", input: []schemas.ChatMessage{tool("call_a", image(png)), {Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{text("follow up"), image(jpeg)}}}},
			wantRoles: "tool,user", wantToolText: []string{"Image attached below."},
			wantUserText: []string{"Images returned by tool call call_a:", "follow up"}, wantImages: []string{png, jpeg},
		},
		{
			name: "next user without content", input: []schemas.ChatMessage{tool("call_a", image(png)), {Role: schemas.ChatMessageRoleUser}},
			wantRoles: "tool,user", wantToolText: []string{"Image attached below."},
			wantUserText: []string{"Images returned by tool call call_a:"}, wantImages: []string{png},
		},
		{
			name: "parallel results in completion order", input: []schemas.ChatMessage{tool("call_b", image(jpeg)), tool("call_a", image(png))},
			wantRoles: "tool,tool,user", wantToolText: []string{"Image attached below.", "Image attached below."},
			wantUserText: []string{"Images returned by tool call call_b:", "Images returned by tool call call_a:"}, wantImages: []string{jpeg, png},
		},
		{
			name: "parallel text result remains before images", input: []schemas.ChatMessage{tool("call_a", image(png)), tool("call_b", text("done"))},
			wantRoles: "tool,tool,user", wantToolText: []string{"Image attached below.", "done"},
			wantUserText: []string{"Images returned by tool call call_a:"}, wantImages: []string{png},
		},
		{
			name: "separate result groups", input: []schemas.ChatMessage{tool("call_a", image(png)), {Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("next call")}}, tool("call_b", image(jpeg))},
			wantRoles: "tool,user,assistant,tool,user", wantToolText: []string{"Image attached below.", "Image attached below."},
			wantUserText: []string{"Images returned by tool call call_a:", "Images returned by tool call call_b:"}, wantImages: []string{png, jpeg},
		},
		{
			name: "remote image URL", input: []schemas.ChatMessage{tool("call_a", image("https://example.com/image.png"))},
			wantRoles: "tool,user", wantToolText: []string{"Image attached below."},
			wantUserText: []string{"Images returned by tool call call_a:"}, wantImages: []string{"https://example.com/image.png"},
		},
		{
			name: "caption uses outgoing ID", input: []schemas.ChatMessage{tool("call_a_ts_"+strings.Repeat("x", 80), image(png))},
			wantRoles: "tool,user", wantToolText: []string{"Image attached below."},
			wantUserText: []string{"Images returned by tool call call_a:"}, wantImages: []string{png},
		},
		{
			name: "text blocks unchanged", input: []schemas.ChatMessage{tool("call_a", text("first"), text("second"))},
			wantRoles: "tool", wantToolText: []string{"first", "second"},
		},
		{
			name: "text string unchanged", input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleTool, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("done")}}},
			wantRoles: "tool", wantToolText: []string{"done"},
		},
		{
			name: "empty and nil tool content", input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleTool}, tool("call_a"), {Role: schemas.ChatMessageRoleTool, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("")}}},
			wantRoles: "tool,tool,tool", wantToolText: []string{""},
		},
		{
			name: "unsupported images remain placeholders", input: []schemas.ChatMessage{tool("call_a", image("data:image/svg+xml;base64,PHN2Zz4="), image(""), schemas.ChatContentBlock{Type: schemas.ChatContentBlockTypeImage}, schemas.ChatContentBlock{Type: schemas.ChatContentBlockTypeImage, ImageURLStruct: &schemas.ChatInputImage{FileID: schemas.Ptr("file_image")}})},
			wantRoles: "tool", wantToolText: []string{"[image/svg+xml, 6 bytes returned by the tool, not shown]\n[image returned by the tool, not shown]\n[image_url returned by the tool, not shown]\n[image returned by the tool, not shown]"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before, err := schemas.MarshalSorted(tt.input)
			require.NoError(t, err)
			converted := ConvertBifrostMessagesToOpenAIMessages(tt.input)
			wire, err := schemas.MarshalSorted(converted)
			require.NoError(t, err)
			var messages []OpenAIMessage
			require.NoError(t, schemas.Unmarshal(wire, &messages), string(wire))
			var roles, toolText, userText, images []string
			for _, message := range messages {
				roles = append(roles, string(message.Role))
				if message.Content == nil {
					continue
				}
				var texts []string
				if message.Content.ContentStr != nil {
					texts = append(texts, *message.Content.ContentStr)
				}
				for _, block := range message.Content.ContentBlocks {
					if message.Role == schemas.ChatMessageRoleTool {
						require.Equal(t, schemas.ChatContentBlockTypeText, block.Type, string(wire))
					}
					if block.Text != nil {
						texts = append(texts, *block.Text)
						require.NotContains(t, *block.Text, "base64,", "image data must not become text")
					}
					if block.Type == schemas.ChatContentBlockTypeImage {
						require.Equal(t, schemas.ChatMessageRoleUser, message.Role)
						require.NotNil(t, block.ImageURLStruct)
						require.Equal(t, "low", *block.ImageURLStruct.Detail)
						images = append(images, block.ImageURLStruct.URL)
					}
				}
				switch message.Role {
				case schemas.ChatMessageRoleTool:
					toolText = append(toolText, texts...)
				case schemas.ChatMessageRoleUser:
					userText = append(userText, texts...)
				}
			}
			for _, original := range tt.input {
				if original.Role == schemas.ChatMessageRoleUser && original.Name != nil {
					found := false
					for _, message := range messages {
						found = found || (message.Name != nil && *message.Name == *original.Name)
					}
					require.True(t, found, "following user's name was lost")
				}
			}
			require.Equal(t, tt.wantRoles, strings.Join(roles, ","), string(wire))
			require.Equal(t, tt.wantToolText, toolText, string(wire))
			require.Equal(t, tt.wantUserText, userText, string(wire))
			require.Equal(t, tt.wantImages, images, string(wire))
			after, err := schemas.MarshalSorted(tt.input)
			require.NoError(t, err)
			require.Equal(t, string(before), string(after), "caller input was mutated")
			repeated, err := schemas.MarshalSorted(ConvertBifrostMessagesToOpenAIMessages(tt.input))
			require.NoError(t, err)
			require.Equal(t, string(wire), string(repeated), "serialization must be deterministic")
			if len(tt.wantImages) > 0 {
				roundTrip, err := schemas.MarshalSorted(ConvertBifrostMessagesToOpenAIMessages(ConvertOpenAIMessagesToBifrostMessages(messages)))
				require.NoError(t, err)
				require.Equal(t, string(wire), string(roundTrip), "already hoisted images must not be duplicated")
			}
		})
	}
}

func TestHoistedToolImagesStripProviderMetadata(t *testing.T) {
	const imageURL = "data:image/png;base64,iVBORw0KGgo="
	for _, tt := range []struct {
		name  string
		guard bool
		cache bool
	}{
		{"guard content", true, false},
		{"cache point", false, true},
		{"both", true, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			image := schemas.ChatContentBlock{
				Type:           schemas.ChatContentBlockTypeImage,
				ImageURLStruct: &schemas.ChatInputImage{URL: imageURL, Detail: schemas.Ptr("low")},
			}
			if tt.guard {
				image.GuardContent = &schemas.GuardContent{}
			}
			if tt.cache {
				image.CachePoint = &schemas.CachePoint{Type: "default"}
			}
			req := &schemas.BifrostChatRequest{
				Provider: schemas.OpenAI, Model: "gpt-4o",
				Input: []schemas.ChatMessage{{
					Role:            schemas.ChatMessageRoleTool,
					ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr("call_a")},
					Content:         &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{image}},
				}},
			}
			before, err := schemas.MarshalSorted(req)
			require.NoError(t, err)
			ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
			wire, err := ToOpenAIChatRequest(ctx, req).MarshalJSON()
			require.NoError(t, err)
			require.Contains(t, string(wire), imageURL)
			require.NotContains(t, string(wire), "guard_content", "Bedrock-only fields are not valid Chat image parts")
			require.NotContains(t, string(wire), "cachePoint", "Bedrock-only fields are not valid Chat image parts")
			after, err := schemas.MarshalSorted(req)
			require.NoError(t, err)
			require.Equal(t, string(before), string(after), "the source metadata must remain available to another provider")
		})
	}
}

package schemas

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func toolMediaContent() *ChatMessageContent {
	text, data := "chart below", strings.Repeat("A", 4096)
	format, pdf, name := "mp3", "application/pdf", "report.pdf"
	return &ChatMessageContent{ContentBlocks: []ChatContentBlock{
		{Type: ChatContentBlockTypeText, Text: &text},
		{Type: ChatContentBlockTypeImage, ImageURLStruct: &ChatInputImage{URL: "data:image/png;base64," + data}},
		{Type: ChatContentBlockTypeInputAudio, InputAudio: &ChatInputAudio{Data: data, Format: &format}},
		{Type: ChatContentBlockTypeFile, File: &ChatInputFile{FileData: &data, FileType: &pdf, Filename: &name}},
	}}
}

// With no media kept, a tool result becomes one string with each medium named, never its base64.
func TestFlattenToolResultMedia_TextOnly(t *testing.T) {
	original := toolMediaContent()
	flattened := FlattenToolResultMedia(original, nil)
	require.NotNil(t, flattened.ContentStr)
	assert.Equal(t, "chart below\n[image/png, 3 KB returned by the tool, not shown]\n[audio/mp3, 3 KB returned by the tool, not shown]\n[application/pdf report.pdf, 3 KB returned by the tool, not shown]", *flattened.ContentStr)
	assert.Len(t, original.ContentBlocks, 4, "the caller's content is not mutated")
}

// A file carried as a data URL is sized by its payload, not the URL's prefix.
func TestToolResultMediaPlaceholder_DataURLFileSizedByPayload(t *testing.T) {
	data, plain, name := "data:text/plain;base64,SGVsbG8=", "text/plain", "hello.txt"
	placeholder := ToolResultMediaPlaceholder(ChatContentBlock{Type: ChatContentBlockTypeFile, File: &ChatInputFile{FileData: &data, FileType: &plain, Filename: &name}})
	assert.Equal(t, "[text/plain hello.txt, 6 bytes returned by the tool, not shown]", placeholder)
	placeholder, ok := ResponsesToolResultMediaPlaceholder(ResponsesMessageContentBlock{ResponsesInputMessageContentBlockFile: &ResponsesInputMessageContentBlockFile{FileData: &data, FileType: &plain, Filename: &name}})
	require.True(t, ok)
	assert.Equal(t, "[text/plain hello.txt, 6 bytes returned by the tool, not shown]", placeholder)
}

// Kept media stays a block; the rest becomes placeholders beside it.
func TestFlattenToolResultMedia_KeepsImages(t *testing.T) {
	flattened := FlattenToolResultMedia(toolMediaContent(), IsImageBlock)
	require.Len(t, flattened.ContentBlocks, 4)
	assert.Equal(t, ChatContentBlockTypeImage, flattened.ContentBlocks[1].Type)
	assert.Equal(t, ChatContentBlockTypeText, flattened.ContentBlocks[2].Type)
	assert.Contains(t, *flattened.ContentBlocks[2].Text, "audio/mp3")
}

// A cache marker survives flattening: on its text block, or on the placeholder of the media it was on,
// and a marked result stays blocks so providers can place the marker.
func TestFlattenToolResultMedia_KeepsCacheMarkers(t *testing.T) {
	marker := &CacheControl{Type: CacheControlTypeEphemeral}
	onText := toolMediaContent()
	onText.ContentBlocks[0].CacheControl = marker
	flattened := FlattenToolResultMedia(onText, nil)
	require.Len(t, flattened.ContentBlocks, 4, "a marked result is not joined into a string")
	assert.Same(t, marker, flattened.ContentBlocks[0].CacheControl)

	onMedia := toolMediaContent()
	onMedia.ContentBlocks[3].CacheControl = marker
	flattened = FlattenToolResultMedia(onMedia, IsImageBlock)
	require.Len(t, flattened.ContentBlocks, 4)
	assert.Equal(t, ChatContentBlockTypeText, flattened.ContentBlocks[3].Type)
	assert.Same(t, marker, flattened.ContentBlocks[3].CacheControl, "the placeholder carries the replaced block's marker")
}

// OpenAI's block-level prompt-cache breakpoint is kept the same way.
func TestFlattenToolResultMedia_KeepsPromptCacheBreakpoints(t *testing.T) {
	breakpoint := &PromptCacheBreakpoint{Mode: Ptr("explicit")}
	onText := toolMediaContent()
	onText.ContentBlocks[0].PromptCacheBreakpoint = breakpoint
	flattened := FlattenToolResultMedia(onText, nil)
	require.Len(t, flattened.ContentBlocks, 4, "a marked result is not joined into a string")
	assert.Same(t, breakpoint, flattened.ContentBlocks[0].PromptCacheBreakpoint)

	onMedia := toolMediaContent()
	onMedia.ContentBlocks[2].PromptCacheBreakpoint = breakpoint
	flattened = FlattenToolResultMedia(onMedia, IsImageBlock)
	require.Len(t, flattened.ContentBlocks, 4)
	assert.Same(t, breakpoint, flattened.ContentBlocks[2].PromptCacheBreakpoint, "the placeholder carries the replaced block's breakpoint")
}

// Providers read png, jpeg, gif and webp in a tool result; another inline format, or none, is named instead.
func TestIsImageBlock_OnlyFormatsProvidersRead(t *testing.T) {
	image := func(url string) ChatContentBlock {
		return ChatContentBlock{Type: ChatContentBlockTypeImage, ImageURLStruct: &ChatInputImage{URL: url}}
	}
	for url, want := range map[string]bool{
		"data:image/png;base64,iVBO":            true,
		"data:image/JPEG;base64,/9j/":           true,
		"data:image/webp;base64,UklG":           true,
		"data:image/png;name=chart;base64,iVBO": true, // RFC 2397 parameters before ;base64
		"data:image/svg+xml;base64,PHN2":        false,
		"data:;base64,iVBO":                     false,
		"https://example.com/chart.png":         true, // a URL the provider fetches is left to it
	} {
		assert.Equal(t, want, IsImageBlock(image(url)), url)
	}
}

// Every Responses provider reads PDF and plain-text files and the common image formats in a
// function_call_output; other files and images become placeholders.
func TestToolResultForResponses_KeepsOnlyPortableMedia(t *testing.T) {
	file := func(fileType string) ChatContentBlock {
		data := "UEsDBA=="
		return ChatContentBlock{Type: ChatContentBlockTypeFile, File: &ChatInputFile{FileData: &data, FileType: Ptr(fileType)}}
	}
	message := &ChatMessage{Role: ChatMessageRoleTool, Content: &ChatMessageContent{ContentBlocks: []ChatContentBlock{
		file("application/pdf"),
		file("text/plain"),
		file("application/zip"),
		{Type: ChatContentBlockTypeImage, ImageURLStruct: &ChatInputImage{URL: "data:image/svg+xml;base64,PHN2"}},
	}}}
	blocks := ToolResultForResponses(message).Content.ContentBlocks
	require.Len(t, blocks, 4)
	assert.Equal(t, ChatContentBlockTypeFile, blocks[0].Type)
	assert.Equal(t, ChatContentBlockTypeFile, blocks[1].Type)
	assert.Equal(t, ChatContentBlockTypeText, blocks[2].Type, "a zip is named, not sent")
	assert.Contains(t, *blocks[2].Text, "application/zip")
	assert.Equal(t, ChatContentBlockTypeText, blocks[3].Type, "an svg is named, not sent")

	// The function_call_output Bifrost answers with: the PDF as an input_file, the zip named.
	message.ChatToolMessage = &ChatToolMessage{ToolCallID: Ptr("call_archive")}
	wire, err := json.Marshal(ToolResultForResponses(message).ToResponsesToolMessage())
	require.NoError(t, err)
	assert.Contains(t, string(wire), `"input_file"`)
	assert.Contains(t, string(wire), "[application/zip")
	assert.Contains(t, string(wire), "[image/svg+xml")
}

// Content that needs nothing removed is returned as is, so text results cost nothing.
func TestFlattenToolResultMedia_Unchanged(t *testing.T) {
	text := "plain"
	str := &ChatMessageContent{ContentStr: &text}
	assert.Same(t, str, FlattenToolResultMedia(str, nil))
	assert.Nil(t, FlattenToolResultMedia(nil, nil))
	onlyImages := &ChatMessageContent{ContentBlocks: toolMediaContent().ContentBlocks[:2]}
	assert.Same(t, onlyImages, FlattenToolResultMedia(onlyImages, IsImageBlock))
}

// The Responses wire takes images and files in a function output, not audio.
func TestToolResultForResponses(t *testing.T) {
	message := &ChatMessage{Role: ChatMessageRoleTool, Content: toolMediaContent()}
	adapted := ToolResultForResponses(message)
	require.Len(t, adapted.Content.ContentBlocks, 4)
	assert.Equal(t, ChatContentBlockTypeImage, adapted.Content.ContentBlocks[1].Type)
	assert.Equal(t, ChatContentBlockTypeText, adapted.Content.ContentBlocks[2].Type)
	assert.Equal(t, ChatContentBlockTypeFile, adapted.Content.ContentBlocks[3].Type)
	assert.Equal(t, ChatContentBlockTypeInputAudio, message.Content.ContentBlocks[2].Type, "the original message is untouched")
	assert.Nil(t, ToolResultForResponses(nil))
}

// A chat request routed to Responses (reasoning.mode, compat) sends a function_call_output with only
// the media Responses reads; audio and other files are named instead.
func TestToResponsesRequest_ToolResultKeepsOnlyPortableMedia(t *testing.T) {
	csv, csvType := "data:text/csv;base64,YSxiCjEsMgo=", "text/csv"
	tool := ChatMessage{Role: ChatMessageRoleTool, ChatToolMessage: &ChatToolMessage{ToolCallID: Ptr("call_media")}, Content: toolMediaContent()}
	tool.Content.ContentBlocks = append(tool.Content.ContentBlocks, ChatContentBlock{Type: ChatContentBlockTypeFile, File: &ChatInputFile{FileData: &csv, FileType: &csvType}})
	user := ChatMessage{Role: ChatMessageRoleUser, Content: toolMediaContent()}
	req := &BifrostChatRequest{Provider: "openai", Model: "gpt-5", Input: []ChatMessage{user, tool}}

	wire, err := json.Marshal(req.ToResponsesRequest().Input)
	require.NoError(t, err)
	output := string(wire[strings.Index(string(wire), `"function_call_output"`):])
	assert.False(t, strings.Contains(output, `"input_audio"`), "Responses rejects audio in a function output")
	assert.False(t, strings.Contains(output, "text/csv;base64"), "Responses rejects a csv file in a function output")
	assert.True(t, strings.Contains(output, "[audio/mp3"), "audio is named")
	assert.True(t, strings.Contains(output, "[text/csv"), "the csv is named")
	assert.True(t, strings.Contains(output, `"input_image"`), "the image is kept")
	assert.True(t, strings.Contains(output, `"input_file"`), "the pdf is kept")
	assert.Equal(t, ChatContentBlockTypeInputAudio, req.Input[1].Content.ContentBlocks[2].Type, "the caller's request is untouched")
	assert.True(t, strings.Contains(string(wire[:strings.Index(string(wire), `"function_call_output"`)]), `"input_audio"`), "user messages keep their audio")
}

// Only tool messages lose their media, and a request with nothing to flatten is returned as is.
func TestFlattenToolMessagesMedia(t *testing.T) {
	user := ChatMessage{Role: ChatMessageRoleUser, Content: toolMediaContent()}
	tool := ChatMessage{Role: ChatMessageRoleTool, Content: toolMediaContent()}
	messages := []ChatMessage{user, tool}
	flattened := FlattenToolMessagesMedia(messages)
	require.Len(t, flattened, 2)
	assert.Same(t, user.Content, flattened[0].Content, "non-tool messages are untouched")
	require.NotNil(t, flattened[1].Content.ContentStr)
	assert.Contains(t, *flattened[1].Content.ContentStr, "[image/png")
	assert.Len(t, messages[1].Content.ContentBlocks, 4, "the caller's messages are not mutated")

	plain := []ChatMessage{{Role: ChatMessageRoleTool, Content: &ChatMessageContent{ContentStr: Ptr("ok")}}}
	assert.Equal(t, &plain[0], &FlattenToolMessagesMedia(plain)[0], "nothing to flatten returns the same slice")
}

// BenchmarkToResponsesRequest_ToolResult measures converting a chat request with one tool result:
// text, the common case, and media that has to be named.
func BenchmarkToResponsesRequest_ToolResult(b *testing.B) {
	text := "the sum is 42"
	for _, tc := range []struct {
		name    string
		content *ChatMessageContent
	}{
		{"text", &ChatMessageContent{ContentStr: &text}},
		{"media", toolMediaContent()},
	} {
		req := &BifrostChatRequest{Provider: "openai", Model: "gpt-5", Input: []ChatMessage{
			{Role: ChatMessageRoleUser, Content: &ChatMessageContent{ContentStr: &text}},
			{Role: ChatMessageRoleTool, ChatToolMessage: &ChatToolMessage{ToolCallID: Ptr("call_1")}, Content: tc.content},
		}}
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = req.ToResponsesRequest()
			}
		})
	}
}

// Logged prompt and resource reads name their media instead of carrying its base64, without
// touching the result served to the caller.
func TestPromptAndResourceForLog(t *testing.T) {
	data := strings.Repeat("A", 4096)
	resource := &mcp.ReadResourceResult{Contents: []mcp.ResourceContents{
		mcp.TextResourceContents{URI: "file:///a.md", Text: "# A"},
		mcp.BlobResourceContents{URI: "file:///a.png", MIMEType: "image/png", Blob: data},
	}}
	logged := ResourceContentsForLog(resource)
	require.Len(t, logged, 2)
	assert.Equal(t, "# A", logged[0].(mcp.TextResourceContents).Text)
	assert.Equal(t, "[image/png, 3 KB returned by the tool, not shown]", logged[1].(mcp.BlobResourceContents).Blob)
	assert.Equal(t, data, resource.Contents[1].(mcp.BlobResourceContents).Blob, "the served result is untouched")

	prompt := &mcp.GetPromptResult{Messages: []mcp.PromptMessage{
		mcp.NewPromptMessage(mcp.RoleUser, mcp.NewTextContent("look")),
		mcp.NewPromptMessage(mcp.RoleUser, mcp.NewImageContent(data, "image/png")),
	}}
	loggedPrompt := PromptForLog(prompt)
	assert.Equal(t, "look", loggedPrompt.Messages[0].Content.(mcp.TextContent).Text)
	assert.Equal(t, "[image/png, 3 KB returned by the tool, not shown]", loggedPrompt.Messages[1].Content.(mcp.ImageContent).Data)
	assert.Equal(t, data, prompt.Messages[1].Content.(mcp.ImageContent).Data, "the served result is untouched")

	// In-process servers can return pointer content; off-spec servers can nest media in tool_result.
	image := mcp.NewImageContent(data, "image/png")
	nested := mcp.NewToolResultContent("call-1", []mcp.Content{mcp.NewTextContent("ok"), mcp.NewAudioContent(data, "audio/wav")}, false)
	prompt = &mcp.GetPromptResult{Messages: []mcp.PromptMessage{
		{Role: mcp.RoleUser, Content: &image},
		{Role: mcp.RoleUser, Content: nested},
	}}
	loggedPrompt = PromptForLog(prompt)
	require.IsType(t, mcp.ImageContent{}, loggedPrompt.Messages[0].Content, "pointer content is logged as a redacted copy")
	assert.Equal(t, "[image/png, 3 KB returned by the tool, not shown]", loggedPrompt.Messages[0].Content.(mcp.ImageContent).Data)
	loggedNested := loggedPrompt.Messages[1].Content.(mcp.ToolResultContent).Content
	assert.Equal(t, "ok", loggedNested[0].(mcp.TextContent).Text)
	assert.Equal(t, "[audio/wav, 3 KB returned by the tool, not shown]", loggedNested[1].(mcp.AudioContent).Data)
	assert.Equal(t, data, image.Data, "the served result is untouched")
	assert.Equal(t, data, nested.Content[1].(mcp.AudioContent).Data, "the served result is untouched")
	assert.Nil(t, PromptForLog(nil))
	assert.Nil(t, ResourceContentsForLog(nil))
}

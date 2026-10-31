package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Text-only results stay one string, as before; media becomes content blocks, never base64 text.
func TestToolResultContent(t *testing.T) {
	text := func(s string) *string { return &s }
	cases := []struct {
		name   string
		result *mcp.CallToolResult
		want   *schemas.ChatMessageContent
	}{
		{"no result", nil, &schemas.ChatMessageContent{ContentStr: text("MCP tool 'echo' executed successfully")}},
		{"text only stays a string", &mcp.CallToolResult{Content: []mcp.Content{mcp.NewTextContent("a"), &mcp.TextContent{Text: "b "}}},
			&schemas.ChatMessageContent{ContentStr: text("ab")}},
		{"structured content when there is no text", &mcp.CallToolResult{StructuredContent: map[string]any{"temp": 21}},
			&schemas.ChatMessageContent{ContentStr: text(`{"temp":21}`)}},
		{"text wins over structured content", &mcp.CallToolResult{Content: []mcp.Content{mcp.NewTextContent("21 degrees")}, StructuredContent: map[string]any{"temp": 21}},
			&schemas.ChatMessageContent{ContentStr: text("21 degrees")}},
		{"image and audio become blocks", &mcp.CallToolResult{Content: []mcp.Content{mcp.NewTextContent("chart"), mcp.NewImageContent("AAAA", "image/png"), mcp.NewAudioContent("BBBB", "audio/mpeg")}},
			&schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
				{Type: schemas.ChatContentBlockTypeText, Text: text("chart")},
				{Type: schemas.ChatContentBlockTypeImage, ImageURLStruct: &schemas.ChatInputImage{URL: "data:image/png;base64,AAAA"}},
				{Type: schemas.ChatContentBlockTypeInputAudio, InputAudio: &schemas.ChatInputAudio{Data: "BBBB", Format: text("mp3")}},
			}}},
		{"embedded resources", &mcp.CallToolResult{Content: []mcp.Content{
			mcp.NewEmbeddedResource(mcp.TextResourceContents{URI: "file:///notes.md", Text: "# Notes"}),
			mcp.NewEmbeddedResource(mcp.BlobResourceContents{URI: "file:///report.pdf", MIMEType: "application/pdf", Blob: "CCCC"}),
		}}, &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
			{Type: schemas.ChatContentBlockTypeText, Text: text("# Notes")},
			{Type: schemas.ChatContentBlockTypeFile, File: &schemas.ChatInputFile{FileData: text("data:application/pdf;base64,CCCC"), Filename: text("report.pdf"), FileType: text("application/pdf")}},
		}}},
		// A blob is base64 whatever its type; as bare text/plain file_data it would read as raw text.
		{"text blob is a base64 data URL", &mcp.CallToolResult{Content: []mcp.Content{
			mcp.NewEmbeddedResource(mcp.BlobResourceContents{URI: "file:///hello.txt", MIMEType: "text/plain", Blob: "SGVsbG8="}),
			mcp.NewEmbeddedResource(mcp.BlobResourceContents{URI: "file:///raw", Blob: "AAEC"}),
		}}, &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
			{Type: schemas.ChatContentBlockTypeFile, File: &schemas.ChatInputFile{FileData: text("data:text/plain;base64,SGVsbG8="), Filename: text("hello.txt"), FileType: text("text/plain")}},
			{Type: schemas.ChatContentBlockTypeFile, File: &schemas.ChatInputFile{FileData: text("data:application/octet-stream;base64,AAEC"), Filename: text("raw")}},
		}}},
		{"resource link is a line", &mcp.CallToolResult{Content: []mcp.Content{mcp.NewResourceLink("file:///a.txt", "a", "", "text/plain")}},
			&schemas.ChatMessageContent{ContentStr: text("[Resource: a file:///a.txt (text/plain)]")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, toolResultContent(tc.result, "echo"))
		})
	}
}

// The upstream's own result rides on the message for the gateway, and is withheld once a plugin has
// rewritten the content.
func TestCreateToolResponseMessage_KeepsTheUpstreamResultUntilRewritten(t *testing.T) {
	result := &mcp.CallToolResult{Content: []mcp.Content{mcp.NewTextContent("secret: 42")}, StructuredContent: map[string]any{"secret": 42}, IsError: true}
	message := createToolResponseMessage(testToolCall("call_1"), result, "echo")
	require.NotNil(t, message.ChatToolMessage.IsError)
	assert.Same(t, result, message.ChatToolMessage.MCPResult(message.Content))

	*message.Content.ContentStr = "secret: [redacted]"
	assert.Nil(t, message.ChatToolMessage.MCPResult(message.Content), "a result rewritten in place must not pass the raw one through")
	redacted := "secret: [redacted]"
	message.Content = &schemas.ChatMessageContent{ContentStr: &redacted}
	assert.Nil(t, message.ChatToolMessage.MCPResult(message.Content), "nor one whose content was replaced")
	*message.Content.ContentStr = "secret: 42"
	assert.Same(t, result, message.ChatToolMessage.MCPResult(message.Content))

	// Media results are compared field by field, in-place edits included.
	media := createToolResponseMessage(testToolCall("call_2"), &mcp.CallToolResult{Content: []mcp.Content{mcp.NewTextContent("x"), mcp.NewImageContent("AAAA", "image/png")}}, "echo")
	require.NotNil(t, media.ChatToolMessage.MCPResult(media.Content))
	*media.Content.ContentBlocks[0].Text = "y"
	assert.Nil(t, media.ChatToolMessage.MCPResult(media.Content))

	// A plugin that only corrects a media type has rewritten the result too: the rebuild uses it.
	files := createToolResponseMessage(testToolCall("call_3"), &mcp.CallToolResult{Content: []mcp.Content{
		mcp.NewTextContent("x"),
		mcp.NewEmbeddedResource(mcp.BlobResourceContents{URI: "file:///report", MIMEType: "application/octet-stream", Blob: "JVBERi0xLjQK"}),
		mcp.NewAudioContent("SUQz", "audio/wav"),
	}}, "echo")
	require.NotNil(t, files.ChatToolMessage.MCPResult(files.Content))
	files.Content.ContentBlocks[1].File.FileType = schemas.Ptr("application/pdf")
	assert.Nil(t, files.ChatToolMessage.MCPResult(files.Content), "a corrected file type is a rewrite")
	files.Content.ContentBlocks[1].File.FileType = schemas.Ptr("application/octet-stream")
	require.NotNil(t, files.ChatToolMessage.MCPResult(files.Content))
	files.Content.ContentBlocks[2].InputAudio.Format = schemas.Ptr("mp3")
	assert.Nil(t, files.ChatToolMessage.MCPResult(files.Content), "a corrected audio format is a rewrite")

	// A field that was unset and is now set is a rewrite whatever it holds, NUL included.
	image := createToolResponseMessage(testToolCall("call_4"), &mcp.CallToolResult{Content: []mcp.Content{mcp.NewTextContent("x"), mcp.NewImageContent("AAAA", "image/png")}}, "echo")
	require.NotNil(t, image.ChatToolMessage.MCPResult(image.Content))
	require.Nil(t, image.Content.ContentBlocks[1].Text)
	image.Content.ContentBlocks[1].Text = schemas.Ptr("\x00")
	assert.Nil(t, image.ChatToolMessage.MCPResult(image.Content), "setting an unset field is a rewrite")
}

// Discovery keeps a tool's title, icons and output schema beside its function definition.
func TestConvertMCPTool_KeepsTitleIconsAndOutputSchema(t *testing.T) {
	tool := mcp.NewTool("weather", mcp.WithOutputSchema[struct {
		Temp int `json:"temp"`
	}]())
	tool.Title = "Weather"
	tool.Icons = []mcp.Icon{{Src: "https://example.com/weather.png", MIMEType: "image/png"}}
	converted := convertMCPToolToBifrostSchema(&tool, &MockLogger{})
	require.NotNil(t, converted.MCP)
	assert.Equal(t, "Weather", converted.MCP.Title)
	assert.Equal(t, tool.Icons, converted.MCP.Icons)
	assert.Contains(t, string(converted.MCP.OutputSchema), `"temp"`)

	plain := mcp.NewTool("echo")
	assert.Nil(t, convertMCPToolToBifrostSchema(&plain, &MockLogger{}).MCP)

	// A type-less output schema is kept, and no "type" is added.
	var untyped mcp.Tool
	require.NoError(t, json.Unmarshal([]byte(`{"name":"answer","inputSchema":{"type":"object"},"outputSchema":{"properties":{"answer":{"type":"string"}}}}`), &untyped))
	converted = convertMCPToolToBifrostSchema(&untyped, &MockLogger{})
	require.NotNil(t, converted.MCP, "the output schema must not be dropped")
	var schema map[string]any
	require.NoError(t, json.Unmarshal(converted.MCP.OutputSchema, &schema))
	assert.Contains(t, schema, "properties")
	assert.NotContains(t, schema, "type")
}

var toolResultSink *schemas.ChatMessage

// BenchmarkCreateToolResponseMessage measures turning an upstream result into a tool message: the
// common text-only case, and one carrying a 1 MB image.
func BenchmarkCreateToolResponseMessage(b *testing.B) {
	for _, tc := range []struct {
		name   string
		result *mcp.CallToolResult
	}{
		{"text", &mcp.CallToolResult{Content: []mcp.Content{mcp.NewTextContent(strings.Repeat("weather is fine ", 64))}}},
		{"image-1MB", &mcp.CallToolResult{Content: []mcp.Content{mcp.NewTextContent("chart"), mcp.NewImageContent(strings.Repeat("A", 1<<20), "image/png")}}},
	} {
		b.Run(tc.name, func(b *testing.B) {
			call := testToolCall("call_1")
			b.ReportAllocs()
			for b.Loop() {
				toolResultSink = createToolResponseMessage(call, tc.result, "echo")
			}
		})
	}
}

// An upstream tool's image and structured content reach the tool message through the real call path,
// and its title, icons and output schema reach the discovered tool, for Chat and Responses alike.
func TestExecuteTool_MultiModalResultAndToolMetadata(t *testing.T) {
	upstream := server.NewMCPServer("chart-upstream", "1.0.0", server.WithToolCapabilities(true))
	chart := mcp.NewTool("chart", mcp.WithOutputSchema[struct {
		Points int `json:"points"`
	}]())
	chart.Title = "Chart"
	chart.Icons = []mcp.Icon{{Src: "https://example.com/chart.png"}}
	upstream.AddTool(chart, func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		result := mcp.NewToolResultStructured(map[string]any{"points": 3}, "3 points")
		result.Content = append(result.Content, mcp.NewImageContent("iVBORw0KGgo=", "image/png"))
		return result, nil
	})
	ts := httptest.NewServer(server.NewStreamableHTTPServer(upstream))
	t.Cleanup(ts.Close)
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	config := &schemas.MCPClientConfig{
		ID: "chart", Name: "plots", ConnectionType: schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(ts.URL), ToolsToExecute: []string{"*"}, NeedsSessionStickiness: schemas.Ptr(true),
	}
	require.NoError(t, m.AddClient(context.Background(), config))
	t.Cleanup(func() { _ = m.RemoveClient(config.ID) })

	state, ok := snapshotClientState(m, config.ID)
	require.True(t, ok)
	discovered := state.ToolMap["plots-chart"]
	require.NotNil(t, discovered.MCP)
	assert.Equal(t, "Chart", discovered.MCP.Title)
	assert.Contains(t, string(discovered.MCP.OutputSchema), "points")

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	call := testToolCall("call_1")
	call.Function.Name = schemas.Ptr("plots-chart")
	call.Function.Arguments = "{}"
	message, bErr := m.ExecuteChatTool(ctx, &call)
	require.Nil(t, bErr)
	require.Len(t, message.Content.ContentBlocks, 2)
	assert.Equal(t, "3 points", *message.Content.ContentBlocks[0].Text)
	assert.Equal(t, "data:image/png;base64,iVBORw0KGgo=", message.Content.ContentBlocks[1].ImageURLStruct.URL)
	upstreamResult := message.ChatToolMessage.MCPResult(message.Content)
	require.NotNil(t, upstreamResult, "the gateway can pass the upstream's result through")
	assert.NotNil(t, upstreamResult.StructuredContent)

	responses, bErr := m.ExecuteResponsesTool(ctx, &schemas.ResponsesToolMessage{CallID: schemas.Ptr("call_2"), Name: schemas.Ptr("plots-chart"), Arguments: schemas.Ptr("{}")})
	require.Nil(t, bErr)
	require.NotNil(t, responses.ResponsesToolMessage.Output)
	blocks := responses.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks
	require.Len(t, blocks, 2, "the Responses function output carries the image too")
	require.NotNil(t, blocks[1].ResponsesInputMessageContentBlockImage)
}

// A text/plain blob reaches each provider as the text it encodes, not as its base64: OpenAI's and
// Anthropic's Responses converters both read bare text/plain file_data as raw text. (Chat tool messages
// carry files as placeholders, so only Responses sends them.)
func TestTextBlobToolResult_ReachesProvidersDecoded(t *testing.T) {
	result := &mcp.CallToolResult{Content: []mcp.Content{
		mcp.NewTextContent("file below"),
		mcp.NewEmbeddedResource(mcp.BlobResourceContents{URI: "file:///hello.txt", MIMEType: "text/plain", Blob: "SGVsbG8="}),
	}}
	call := testToolCall("call_1")
	message := createToolResponseMessage(call, result, "echo")
	decoded := func(t *testing.T, fileData string) string {
		t.Helper()
		mediaType, payload, ok := schemas.SplitBase64DataURL(fileData)
		require.True(t, ok, "file_data is not a base64 data URL: %q", fileData)
		assert.Equal(t, "text/plain", mediaType)
		raw, err := base64.StdEncoding.DecodeString(payload)
		require.NoError(t, err)
		return string(raw)
	}

	t.Run("openai responses", func(t *testing.T) {
		input := openai.OpenAIResponsesRequestInput{OpenAIResponsesRequestInputArray: []schemas.ResponsesMessage{*schemas.ToolResultForResponses(message).ToResponsesToolMessage()}}
		body, err := input.MarshalJSON()
		require.NoError(t, err)
		fileData := gjson.GetBytes(body, `0.output.#(type=="input_file").file_data`).String()
		assert.Equal(t, "Hello", decoded(t, fileData), "body: %s", body)
	})

	t.Run("anthropic responses", func(t *testing.T) {
		ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
		defer cancel()
		assistant := schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, ChatAssistantMessage: &schemas.ChatAssistantMessage{ToolCalls: []schemas.ChatAssistantMessageToolCall{call}}}
		input := []schemas.ResponsesMessage{{Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser), Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("read it")}}}
		input = append(input, assistant.ToResponsesMessages()...)
		input = append(input, *schemas.ToolResultForResponses(message).ToResponsesToolMessage())
		out, err := anthropic.ToAnthropicResponsesRequest(ctx, &schemas.BifrostResponsesRequest{Provider: schemas.Anthropic, Model: "claude-sonnet-5-5", Input: input, Params: &schemas.ResponsesParameters{}})
		require.NoError(t, err)
		body, err := schemas.MarshalSorted(out)
		require.NoError(t, err)
		var source gjson.Result
		gjson.GetBytes(body, "messages.#.content|@flatten").ForEach(func(_, block gjson.Result) bool {
			if found := block.Get(`content.#(type=="document").source`); found.Exists() {
				source = found
			}
			return true
		})
		require.True(t, source.Exists(), "no document in the tool result: %s", body)
		data := source.Get("data").String()
		if source.Get("type").String() == "base64" {
			raw, err := base64.StdEncoding.DecodeString(data)
			require.NoError(t, err)
			data = string(raw)
		}
		assert.Equal(t, "Hello", data, "body: %s", body)
	})
}

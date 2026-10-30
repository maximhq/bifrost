//go:build !tinygo && !wasm

package starlark

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	codemcp "github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/schemas"
)

type mediaClientManager struct {
	testClientManager
	conn     *client.Client
	postHook func(*schemas.BifrostMCPResponse)
}

func (m *mediaClientManager) AcquireClientConn(_ *schemas.BifrostContext, _ *schemas.MCPClientState) (*client.Client, func(), error) {
	return m.conn, func() {}, nil
}

func (m *mediaClientManager) RunWithPluginPipeline(_ *schemas.BifrostContext, req *schemas.BifrostMCPRequest, op codemcp.MCPOpFunc) (*schemas.BifrostMCPResponse, *schemas.BifrostError) {
	resp, err := op(req)
	if err != nil {
		return nil, &schemas.BifrostError{IsBifrostError: false, Error: &schemas.ErrorField{Message: err.Error()}}
	}
	if m.postHook != nil {
		m.postHook(resp)
	}
	return resp, nil
}

func mediaMode(t *testing.T, result *mcp.CallToolResult) (*StarlarkCodeMode, *mediaClientManager) {
	t.Helper()
	const clientName, toolName = "shots", "capture"
	srv := server.NewMCPServer("media-test", "1.0.0", server.WithToolCapabilities(true))
	srv.AddTool(mcp.NewTool(toolName), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return result, nil
	})
	conn, err := client.NewInProcessClient(srv)
	if err != nil {
		t.Fatalf("NewInProcessClient: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := conn.Start(context.Background()); err != nil {
		t.Fatalf("client.Start: %v", err)
	}
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "media-test", Version: "1.0.0"}
	if _, err := conn.Initialize(context.Background(), initReq); err != nil {
		t.Fatalf("client.Initialize: %v", err)
	}
	mode := NewStarlarkCodeMode(&codemcp.CodeModeConfig{
		BindingLevel:         schemas.CodeModeBindingLevelServer,
		ToolExecutionTimeout: 5 * time.Second,
	}, nil)
	manager := &mediaClientManager{
		testClientManager: testClientManager{
			clients: map[string]*schemas.MCPClientState{
				clientName: {
					Name:            clientName,
					ExecutionConfig: &schemas.MCPClientConfig{Name: clientName, IsCodeModeClient: true, ToolsToExecute: []string{"*"}},
				},
			},
			tools: map[string][]schemas.ChatTool{
				clientName: {{Function: &schemas.ChatToolFunction{Name: clientName + "-" + toolName}}},
			},
		},
		conn: conn,
	}
	mode.clientManager = manager
	return mode, manager
}

func runMediaCode(t *testing.T, mode *StarlarkCodeMode, code string) *schemas.ChatMessage {
	t.Helper()
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyIsMCPGateway, true)
	return runMediaCodeIn(t, ctx, mode, code)
}

func runMediaCodeIn(t *testing.T, ctx *schemas.BifrostContext, mode *StarlarkCodeMode, code string) *schemas.ChatMessage {
	t.Helper()
	args, err := schemas.MarshalSorted(map[string]string{"code": code})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := mode.handleExecuteToolCode(ctx, schemas.ChatAssistantMessageToolCall{
		ID: schemas.Ptr("media-call"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("executeToolCode"),
			Arguments: string(args),
		},
	})
	if err != nil {
		t.Fatalf("handleExecuteToolCode: %v", err)
	}
	return msg
}

func mediaResponseText(t *testing.T, msg *schemas.ChatMessage) string {
	t.Helper()
	if msg == nil || msg.Content == nil || msg.Content.ContentStr == nil {
		t.Fatal("expected a text reply")
	}
	return *msg.Content.ContentStr
}

func forwardedResult(msg *schemas.ChatMessage) *mcp.CallToolResult {
	if msg == nil || msg.ChatToolMessage == nil {
		return nil
	}
	return msg.ChatToolMessage.MCPResult(msg.Content)
}

func mediaRewrites() map[string]func(*schemas.ChatMessage) *schemas.ChatMessage {
	return map[string]func(*schemas.ChatMessage) *schemas.ChatMessage{
		"text in place": func(msg *schemas.ChatMessage) *schemas.ChatMessage {
			*msg.Content.ContentStr = "[redacted]"
			return msg
		},
		"text replacement": func(msg *schemas.ChatMessage) *schemas.ChatMessage {
			msg.Content.ContentStr = schemas.Ptr("[redacted]")
			return msg
		},
		"content replacement": func(msg *schemas.ChatMessage) *schemas.ChatMessage {
			msg.Content = &schemas.ChatMessageContent{ContentStr: schemas.Ptr("[redacted]")}
			return msg
		},
		"message replacement": func(msg *schemas.ChatMessage) *schemas.ChatMessage {
			replacement := *msg
			replacement.Content = &schemas.ChatMessageContent{ContentStr: schemas.Ptr("[redacted]")}
			return &replacement
		},
	}
}

const pngData = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
const wavData = "UklGRg=="

func TestExecuteToolCodeForwardsNestedImages(t *testing.T) {
	mode, _ := mediaMode(t, &mcp.CallToolResult{Content: []mcp.Content{
		mcp.NewTextContent("captured"),
		&mcp.ImageContent{Type: "image", Data: pngData, MIMEType: "image/png"},
		mcp.AudioContent{Type: "audio", Data: wavData, MIMEType: "audio/wav"},
	}})
	msg := runMediaCode(t, mode, "result = shots.capture()")
	text := mediaResponseText(t, msg)
	imageName := schemas.MediaPlaceholder("image/png", len(pngData))
	audioName := schemas.MediaPlaceholder("audio/wav", len(wavData))
	if strings.Contains(text, pngData) || strings.Contains(text, wavData) {
		t.Fatal("encoded media leaked into the text projection")
	}
	if !strings.Contains(text, "Return value: ") || !strings.Contains(text, imageName) || !strings.Contains(text, audioName) {
		t.Fatalf("script value lost its media placeholders: %s", text)
	}
	if !strings.Contains(text, "Attached media: "+imageName+" "+audioName) {
		t.Fatalf("reply does not name the attached media: %s", text)
	}
	native := forwardedResult(msg)
	if native == nil || len(native.Content) != 3 {
		t.Fatalf("forwarded %#v, want text, image and audio", native)
	}
	if first, ok := native.Content[0].(mcp.TextContent); !ok || first.Text != text {
		t.Fatalf("first native block = %#v, want the reply text", native.Content[0])
	}
	if image, ok := native.Content[1].(mcp.ImageContent); !ok || image.Data != pngData || image.MIMEType != "image/png" {
		t.Fatalf("image block = %#v", native.Content[1])
	}
	if audio, ok := native.Content[2].(mcp.AudioContent); !ok || audio.Data != wavData || audio.MIMEType != "audio/wav" {
		t.Fatalf("audio block = %#v", native.Content[2])
	}
	if native.IsError || (msg.ChatToolMessage.IsError != nil && *msg.ChatToolMessage.IsError) {
		t.Fatal("successful reply was marked as an error")
	}
}

func TestExecuteToolCodeOutsideGatewayAttachesNoMedia(t *testing.T) {
	mode, manager := mediaMode(t, &mcp.CallToolResult{Content: []mcp.Content{
		mcp.ImageContent{Type: "image", Data: pngData, MIMEType: "image/png"},
	}})
	manager.postHook = func(resp *schemas.BifrostMCPResponse) {
		if forwardedResult(resp.ChatMessage) != nil {
			t.Fatal("non-gateway nested result retained native media")
		}
	}
	msg := runMediaCodeIn(t, schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), mode, "result = shots.capture()")
	if native := forwardedResult(msg); native != nil {
		t.Fatalf("non-gateway reply carries a native result: %#v", native)
	}
	text := mediaResponseText(t, msg)
	if strings.Contains(text, "Attached media") || strings.Contains(text, pngData) || !strings.Contains(text, schemas.MediaPlaceholder("image/png", len(pngData))) {
		t.Fatalf("non-gateway projection changed: %s", text)
	}
}

func TestExecuteToolCodeForwardsMediaBesideStructuredContent(t *testing.T) {
	mode, _ := mediaMode(t, &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.NewTextContent(`{"title":"text copy"}`),
			mcp.ImageContent{Type: "image", Data: pngData, MIMEType: "image/png"},
			&mcp.AudioContent{Type: "audio", Data: wavData, MIMEType: "audio/wav"},
		},
		StructuredContent: map[string]any{"title": "Example Domain"},
	})
	msg := runMediaCode(t, mode, "r = shots.capture()\nprint(\"media:\", r[\"_media\"][0], r[\"_media\"][1])\nresult = r[\"title\"]")
	text := mediaResponseText(t, msg)
	if strings.Contains(text, pngData) || strings.Contains(text, wavData) || strings.Contains(text, "text copy") {
		t.Fatal("structured projection exposed encoded media or its text copy")
	}
	want := "media: " + schemas.MediaPlaceholder("image/png", len(pngData)) + " " + schemas.MediaPlaceholder("audio/wav", len(wavData))
	if !strings.Contains(text, want) || !strings.Contains(text, "Example Domain") {
		t.Fatalf("script did not read the structured value and its placeholders: %s", text)
	}
	if native := forwardedResult(msg); native == nil || len(native.Content) != 3 {
		t.Fatalf("forwarded %#v, want text, image and audio", native)
	}
}

func TestExecuteToolCodeWithholdsMediaHiddenByStructuredContent(t *testing.T) {
	for name, structured := range map[string]any{
		"non-object": []any{"a"},
		"key taken":  map[string]any{"_media": "server value"},
	} {
		t.Run(name, func(t *testing.T) {
			mode, _ := mediaMode(t, &mcp.CallToolResult{
				Content:           []mcp.Content{mcp.ImageContent{Type: "image", Data: pngData, MIMEType: "image/png"}},
				StructuredContent: structured,
			})
			msg := runMediaCode(t, mode, "result = shots.capture()\nprint(\"finished\")")
			if native := forwardedResult(msg); native != nil {
				t.Fatalf("media absent from the hook projection was forwarded: %#v", native)
			}
		})
	}
}

func TestExecuteToolCodeWithholdsMediaRedactedByPostHook(t *testing.T) {
	for _, structured := range []bool{false, true} {
		name := "content"
		if structured {
			name = "structured"
		}
		t.Run(name, func(t *testing.T) {
			for name, rewrite := range mediaRewrites() {
				t.Run(name, func(t *testing.T) {
					result := &mcp.CallToolResult{Content: []mcp.Content{
						mcp.NewTextContent("secret"),
						mcp.ImageContent{Type: "image", Data: pngData, MIMEType: "image/png"},
					}}
					if structured {
						result.StructuredContent = map[string]any{"title": "secret"}
					}
					mode, manager := mediaMode(t, result)
					manager.postHook = func(resp *schemas.BifrostMCPResponse) {
						text := mediaResponseText(t, resp.ChatMessage)
						if strings.Contains(text, pngData) || !strings.Contains(text, schemas.MediaPlaceholder("image/png", len(pngData))) {
							t.Fatal("post-hook did not receive the media placeholder")
						}
						if forwardedResult(resp.ChatMessage) == nil {
							t.Fatal("nested native result was not registered before post-hooks")
						}
						resp.ChatMessage = rewrite(resp.ChatMessage)
					}
					msg := runMediaCode(t, mode, "result = shots.capture()")
					if native := forwardedResult(msg); native != nil {
						t.Fatalf("media survived a redacting post-hook: %#v", native)
					}
					if text := mediaResponseText(t, msg); !strings.Contains(text, "[redacted]") || strings.Contains(text, "Attached media") {
						t.Fatalf("redacted projection changed: %s", text)
					}
				})
			}
		})
	}
}

func TestExecuteToolCodeReplyMediaWithheldWhenReplyTextRewritten(t *testing.T) {
	for name, rewrite := range mediaRewrites() {
		t.Run(name, func(t *testing.T) {
			mode, _ := mediaMode(t, &mcp.CallToolResult{Content: []mcp.Content{
				mcp.ImageContent{Type: "image", Data: pngData, MIMEType: "image/png"},
			}})
			msg := runMediaCode(t, mode, "result = shots.capture()")
			if forwardedResult(msg) == nil {
				t.Fatal("expected media on the unmodified reply")
			}
			msg = rewrite(msg)
			if native := forwardedResult(msg); native != nil {
				t.Fatalf("reply media survived an outer rewrite: %#v", native)
			}
		})
	}
}

func TestExecuteToolCodeWithoutMediaKeepsTextResult(t *testing.T) {
	for _, gateway := range []bool{false, true} {
		mode, _ := mediaMode(t, mcp.NewToolResultText("plain"))
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyIsMCPGateway, gateway)
		msg := runMediaCodeIn(t, ctx, mode, "result = shots.capture()")
		if native := forwardedResult(msg); native != nil {
			t.Fatalf("text-only run carries a native result: %#v", native)
		}
		if text := mediaResponseText(t, msg); text != "Execution completed successfully.\nReturn value: \"plain\"" {
			t.Fatalf("text-only reply changed: %s", text)
		}
	}
}

func TestExecuteToolCodeWithholdsMediaFromFailedRun(t *testing.T) {
	for name, code := range map[string]string{
		"runtime failure": "r = shots.capture()\nfail(\"stop\")",
		"no data":         "shots.capture()",
	} {
		t.Run(name, func(t *testing.T) {
			mode, _ := mediaMode(t, &mcp.CallToolResult{Content: []mcp.Content{
				mcp.ImageContent{Type: "image", Data: pngData, MIMEType: "image/png"},
			}})
			msg := runMediaCode(t, mode, code)
			if native := forwardedResult(msg); native != nil {
				t.Fatalf("failed run forwarded media: %#v", native)
			}
			if msg.ChatToolMessage.IsError == nil || !*msg.ChatToolMessage.IsError {
				t.Fatal("failed run lost its error flag")
			}
			if text := mediaResponseText(t, msg); strings.Contains(text, "Attached media") || strings.Contains(text, pngData) {
				t.Fatal("failed run exposed media")
			}
		})
	}
}

func TestExecuteToolCodeWithholdsNestedErrorMedia(t *testing.T) {
	for _, clearFlag := range []bool{false, true} {
		mode, manager := mediaMode(t, &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{
				mcp.NewTextContent("camera offline"),
				mcp.ImageContent{Type: "image", Data: pngData, MIMEType: "image/png"},
			},
		})
		manager.postHook = func(resp *schemas.BifrostMCPResponse) {
			if resp.ChatMessage.ChatToolMessage.IsError == nil || !*resp.ChatMessage.ChatToolMessage.IsError {
				t.Fatal("nested result lost its error flag before post-hooks")
			}
			if native := forwardedResult(resp.ChatMessage); native == nil || !native.IsError {
				t.Fatal("nested native result lost its error flag")
			}
			if clearFlag {
				resp.ChatMessage.ChatToolMessage.IsError = schemas.Ptr(false)
			}
		}
		msg := runMediaCode(t, mode, "r = shots.capture()\nif hasattr(r, \"error\"):\n    result = r.error\nelse:\n    result = r")
		if native := forwardedResult(msg); native != nil {
			t.Fatalf("nested error forwarded media: %#v", native)
		}
		if msg.ChatToolMessage.IsError != nil && *msg.ChatToolMessage.IsError {
			t.Fatal("handled nested error changed the successful run's error flag")
		}
		if text := mediaResponseText(t, msg); !strings.Contains(text, "camera offline") {
			t.Fatalf("nested error was lost: %s", text)
		}
	}
}

func TestExecuteToolCodeBoundsForwardedMedia(t *testing.T) {
	const blockLimit = 20
	for _, multipleCalls := range []bool{false, true} {
		content := []mcp.Content{mcp.ImageContent{Type: "image", Data: pngData, MIMEType: "image/png"}}
		code := "for i in range(23):\n    shots.capture()\nresult = i"
		if !multipleCalls {
			for range blockLimit + 2 {
				content = append(content, content[0])
			}
			code = "result = len(shots.capture())"
		}
		mode, _ := mediaMode(t, &mcp.CallToolResult{Content: content})
		msg := runMediaCode(t, mode, code)
		native := forwardedResult(msg)
		if native == nil || len(native.Content) != blockLimit+1 {
			t.Fatalf("forwarded %#v, want %d media blocks plus text", native, blockLimit)
		}
		if text := mediaResponseText(t, msg); !strings.Contains(text, "3 media block(s)") {
			t.Fatalf("dropped media not reported: %s", text)
		}
	}
}

func TestExecuteToolCodeBoundsForwardedMediaBytes(t *testing.T) {
	const byteLimit = 20 << 20
	for _, tc := range []struct {
		name string
		size int
		kept int
	}{
		{name: "exact encoded limit", size: byteLimit / 2, kept: 2},
		{name: "aggregate encoded limit", size: byteLimit/2 + 1, kept: 1},
		{name: "single oversized block", size: byteLimit + 1, kept: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := strings.Repeat("A", tc.size)
			mode, _ := mediaMode(t, &mcp.CallToolResult{Content: []mcp.Content{
				mcp.ImageContent{Type: "image", Data: data, MIMEType: "image/png"},
				mcp.AudioContent{Type: "audio", Data: "", MIMEType: "audio/wav"},
			}})
			msg := runMediaCode(t, mode, "shots.capture()\nshots.capture()\nshots.capture()\nresult = \"finished\"")
			native := forwardedResult(msg)
			if tc.kept == 0 {
				if native != nil {
					t.Fatal("oversized media was forwarded")
				}
			} else {
				if native == nil || len(native.Content) != tc.kept+1 {
					t.Fatalf("forwarded %#v, want %d media blocks plus text", native, tc.kept)
				}
				for _, block := range native.Content[1:] {
					if image, ok := block.(mcp.ImageContent); !ok || image.Data != data {
						t.Fatal("forwarded media was changed")
					}
				}
			}
			want := map[int]string{0: "3 media block(s)", 1: "2 media block(s)", 2: "1 media block(s)"}[tc.kept]
			if text := mediaResponseText(t, msg); !strings.Contains(text, want) || strings.Contains(text, data) {
				t.Fatal("encoded-byte drops were not reported safely")
			}
		})
	}
}

func TestExecuteToolCodeForwardsMediaFromMultipleNestedCalls(t *testing.T) {
	mode, _ := mediaMode(t, &mcp.CallToolResult{Content: []mcp.Content{
		mcp.ImageContent{Type: "image", Data: pngData, MIMEType: "image/png"},
		&mcp.AudioContent{Type: "audio", Data: wavData, MIMEType: "audio/wav"},
	}})
	msg := runMediaCode(t, mode, "result = [shots.capture(), shots.capture()]")
	native := forwardedResult(msg)
	if native == nil || len(native.Content) != 5 {
		t.Fatalf("forwarded %#v, want text plus both calls' image/audio blocks", native)
	}
	for _, index := range []int{1, 3} {
		if image, ok := native.Content[index].(mcp.ImageContent); !ok || image.Data != pngData {
			t.Fatalf("image block %d = %#v", index, native.Content[index])
		}
		if audio, ok := native.Content[index+1].(mcp.AudioContent); !ok || audio.Data != wavData {
			t.Fatalf("audio block %d = %#v", index+1, native.Content[index+1])
		}
	}
}

func TestExtractTextFromMCPResponseMediaProjection(t *testing.T) {
	object := map[string]any{"title": "page"}
	result := &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.ImageContent{Type: "image", Data: "AAAA", MIMEType: "image/png"},
			mcp.AudioContent{Type: "audio", Data: "AAAA", MIMEType: "audio/wav"},
		},
		StructuredContent: object,
	}
	got, err := extractTextFromMCPResponse(result, "capture", 1024)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"_media":["[image/png, 3 bytes returned by the tool, not shown]","[audio/wav, 3 bytes returned by the tool, not shown]"],"title":"page"}`
	if got != want {
		t.Fatalf("projection = %q, want %q", got, want)
	}
	if _, mutated := object["_media"]; mutated || len(object) != 1 || object["title"] != "page" {
		t.Fatalf("structured content was mutated: %#v", object)
	}
	for _, tc := range []struct {
		name  string
		value any
		want  string
	}{
		{name: "non-object", value: []any{"a"}, want: `["a"]`},
		{name: "collision", value: map[string]any{"_media": "server value"}, want: `{"_media":"server value"}`},
		{name: "null map", value: map[string]any(nil), want: `null`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result.StructuredContent = tc.value
			got, err := extractTextFromMCPResponse(result, "capture", 1024)
			if err != nil || got != tc.want {
				t.Fatalf("projection = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestExtractTextFromMCPResponseStructuredMediaLimits(t *testing.T) {
	for name, content := range map[string][]mcp.Content{
		"count": {
			mcp.ImageContent{Data: "AAAA", MIMEType: "image/png"},
			mcp.ImageContent{Data: "AAAA", MIMEType: "image/png"},
			mcp.ImageContent{Data: "AAAA", MIMEType: "image/png"},
		},
		"placeholder size": {mcp.ImageContent{Data: "AAAA", MIMEType: strings.Repeat("x", 257)}},
	} {
		t.Run(name, func(t *testing.T) {
			result := &mcp.CallToolResult{Content: content, StructuredContent: map[string]any{"ok": true}}
			if _, err := extractTextFromMCPResponse(result, "capture", 256); err == nil {
				t.Fatal("oversized structured media projection accepted")
			}
		})
	}
}

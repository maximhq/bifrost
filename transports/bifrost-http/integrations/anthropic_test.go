package integrations

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

func TestCheckAnthropicPassthroughAnchorsOAuthToAnthropic(t *testing.T) {
	const (
		model          = "claude-fable-5"
		qualifiedModel = "anthropic/" + model
	)

	tests := []struct {
		name    string
		request any
		convert func(*schemas.BifrostContext, any) (schemas.ModelProvider, string)
	}{
		{
			name:    "messages",
			request: &anthropic.AnthropicMessageRequest{Model: model},
			convert: func(ctx *schemas.BifrostContext, req any) (schemas.ModelProvider, string) {
				converted := req.(*anthropic.AnthropicMessageRequest).ToBifrostResponsesRequest(ctx)
				return converted.Provider, converted.Model
			},
		},
		{
			name:    "text completion",
			request: &anthropic.AnthropicTextRequest{Model: model},
			convert: func(ctx *schemas.BifrostContext, req any) (schemas.ModelProvider, string) {
				converted := req.(*anthropic.AnthropicTextRequest).ToBifrostTextCompletionRequest(ctx)
				return converted.Provider, converted.Model
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			httpCtx := &fasthttp.RequestCtx{}
			httpCtx.Request.Header.Set("user-agent", "claude-cli/2.1.209")
			httpCtx.Request.Header.Set("authorization", "Bearer sk-ant-oat-test")
			httpCtx.Request.SetBodyString(`{"model":"claude-fable-5"}`)
			originalBody := string(httpCtx.Request.Body())

			bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			if err := checkAnthropicPassthrough(httpCtx, bifrostCtx, tt.request); err != nil {
				t.Fatalf("checkAnthropicPassthrough: %v", err)
			}

			var gotRequestModel string
			switch req := tt.request.(type) {
			case *anthropic.AnthropicMessageRequest:
				gotRequestModel = req.Model
			case *anthropic.AnthropicTextRequest:
				gotRequestModel = req.Model
			}
			if gotRequestModel != qualifiedModel {
				t.Fatalf("request model = %q, want %q", gotRequestModel, qualifiedModel)
			}

			provider, gotModel := tt.convert(bifrostCtx, tt.request)
			if provider != schemas.Anthropic {
				t.Errorf("provider = %q, want %q", provider, schemas.Anthropic)
			}
			if gotModel != model {
				t.Errorf("converted model = %q, want %q", gotModel, model)
			}
			if skipKeySelection, _ := bifrostCtx.Value(schemas.BifrostContextKeySkipKeySelection).(bool); !skipKeySelection {
				t.Error("SkipKeySelection was not enabled for Anthropic OAuth passthrough")
			}
			if gotBody := string(httpCtx.Request.Body()); gotBody != originalBody {
				t.Errorf("raw request body changed: got %q, want %q", gotBody, originalBody)
			}
		})
	}
}

func TestCheckAnthropicPassthroughLeavesAPIKeyModelUnprefixed(t *testing.T) {
	req := &anthropic.AnthropicMessageRequest{Model: "claude-fable-5"}
	httpCtx := &fasthttp.RequestCtx{}
	httpCtx.Request.Header.Set("user-agent", "claude-cli/2.1.209")
	httpCtx.Request.Header.Set("x-api-key", "test-key")

	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	if err := checkAnthropicPassthrough(httpCtx, bifrostCtx, req); err != nil {
		t.Fatalf("checkAnthropicPassthrough: %v", err)
	}
	if req.Model != "claude-fable-5" {
		t.Errorf("API-key request model = %q, want unprefixed model", req.Model)
	}
}

// TestMustConvertInPassthrough pins the passthrough routing decision that fixes
// the Claude Code advisor/server-tool streaming bug: server tools (advisor,
// web_search, web_fetch, code_execution) expand one Responses item into several
// Anthropic content blocks with re-numbered indices, so their frames — and every
// output_item.added (to keep the converter's block counter in lockstep) — must be
// rendered by the converter instead of forwarded raw. Computer, plain messages,
// and function/mcp tool calls stream one block each and stay on the raw path.
//
// core/providers/anthropic passthroughstream_test.go mirrors this rule for its
// end-to-end index-consistency test; keep the two in sync.
func TestMustConvertInPassthrough(t *testing.T) {
	itemDone := func(mt schemas.ResponsesMessageType) *schemas.BifrostResponsesStreamResponse {
		return &schemas.BifrostResponsesStreamResponse{
			Type: schemas.ResponsesStreamResponseTypeOutputItemDone,
			Item: &schemas.ResponsesMessage{Type: &mt},
		}
	}
	typed := func(rt schemas.ResponsesStreamResponseType) *schemas.BifrostResponsesStreamResponse {
		return &schemas.BifrostResponsesStreamResponse{Type: rt}
	}

	cases := []struct {
		name string
		resp *schemas.BifrostResponsesStreamResponse
		want bool
	}{
		// output_item.added always converts (keeps the block counter in lockstep).
		{"added_message", &schemas.BifrostResponsesStreamResponse{
			Type: schemas.ResponsesStreamResponseTypeOutputItemAdded,
			Item: &schemas.ResponsesMessage{Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage)},
		}, true},
		{"added_advisor", &schemas.BifrostResponsesStreamResponse{
			Type: schemas.ResponsesStreamResponseTypeOutputItemAdded,
			Item: &schemas.ResponsesMessage{Type: schemas.Ptr(schemas.ResponsesMessageTypeAdvisorCall)},
		}, true},
		{"added_nil_item", typed(schemas.ResponsesStreamResponseTypeOutputItemAdded), true},

		// output_item.done: only result-block-synthesizing server tools convert.
		{"done_advisor", itemDone(schemas.ResponsesMessageTypeAdvisorCall), true},
		{"done_web_search", itemDone(schemas.ResponsesMessageTypeWebSearchCall), true},
		{"done_web_fetch", itemDone(schemas.ResponsesMessageTypeWebFetchCall), true},
		{"done_code_interpreter", itemDone(schemas.ResponsesMessageTypeCodeInterpreterCall), true},
		{"done_computer", itemDone(schemas.ResponsesMessageTypeComputerCall), false},
		{"done_message", itemDone(schemas.ResponsesMessageTypeMessage), false},
		{"done_function_call", itemDone(schemas.ResponsesMessageTypeFunctionCall), false},
		{"done_mcp_call", itemDone(schemas.ResponsesMessageTypeMCPCall), false},
		{"done_nil_item", typed(schemas.ResponsesStreamResponseTypeOutputItemDone), false},

		// Server-tool lifecycle events convert (they collapse to nothing, dropping
		// the duplicate raw content_block frame they would otherwise carry).
		{"web_search_in_progress", typed(schemas.ResponsesStreamResponseTypeWebSearchCallInProgress), true},
		{"web_search_completed", typed(schemas.ResponsesStreamResponseTypeWebSearchCallCompleted), true},
		{"web_fetch_completed", typed(schemas.ResponsesStreamResponseTypeWebFetchCallCompleted), true},
		{"code_interpreter_code_done", typed(schemas.ResponsesStreamResponseTypeCodeInterpreterCallCodeDone), true},
		{"code_interpreter_completed", typed(schemas.ResponsesStreamResponseTypeCodeInterpreterCallCompleted), true},

		// Everything else stays on the raw passthrough path.
		{"text_delta", typed(schemas.ResponsesStreamResponseTypeOutputTextDelta), false},
		{"function_args_delta", typed(schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta), false},
		{"content_part_added", typed(schemas.ResponsesStreamResponseTypeContentPartAdded), false},
		{"created", typed(schemas.ResponsesStreamResponseTypeCreated), false},
		{"completed", typed(schemas.ResponsesStreamResponseTypeCompleted), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mustConvertInPassthrough(tc.resp); got != tc.want {
				t.Errorf("mustConvertInPassthrough(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// A user message containing a container_upload block must survive the full
// Anthropic integration request pipeline (parse → Bifrost → normalize → Anthropic
// wire) with its file_id intact, and must NOT be replaced by the "..." empty-content
// placeholder that normalizeBifrostInputContentBlocks backfills for otherwise-empty
// user messages.
func TestAnthropicContainerUploadSurvivesNormalization(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	fileID := "file_011CcpBQA2BV1gthmNPSYzkh"
	req := &anthropic.AnthropicMessageRequest{
		Model:     "anthropic/claude-sonnet-4-6",
		MaxTokens: 1024,
		Messages: []anthropic.AnthropicMessage{
			{
				Role: anthropic.AnthropicMessageRoleUser,
				Content: anthropic.AnthropicContent{
					ContentBlocks: []anthropic.AnthropicContentBlock{
						{Type: anthropic.AnthropicContentBlockTypeText, Text: schemas.Ptr("analyse and share model names")},
						{Type: anthropic.AnthropicContentBlockTypeContainerUpload, FileID: &fileID},
					},
				},
			},
		},
	}

	bifrostReq := req.ToBifrostResponsesRequest(ctx)
	normalizeBifrostInputContentBlocks(bifrostReq)

	out, err := anthropic.ToAnthropicResponsesRequest(ctx, bifrostReq)
	if err != nil {
		t.Fatalf("ToAnthropicResponsesRequest: %v", err)
	}

	var containerFileID *string
	sawPlaceholder := false
	for _, m := range out.Messages {
		for _, b := range m.Content.ContentBlocks {
			switch b.Type {
			case anthropic.AnthropicContentBlockTypeContainerUpload:
				containerFileID = b.FileID
			case anthropic.AnthropicContentBlockTypeText:
				if b.Text != nil && *b.Text == "..." {
					sawPlaceholder = true
				}
			}
		}
	}

	if sawPlaceholder {
		t.Errorf("container_upload was replaced by the \"...\" empty-content placeholder")
	}
	if containerFileID == nil {
		t.Fatalf("container_upload block missing from Anthropic request")
	}
	if *containerFileID != fileID {
		t.Errorf("file_id = %q, want %q", *containerFileID, fileID)
	}
}

// TestCheckAnthropicPassthrough_OutputConfigEscapeHatch verifies that a Claude Code
// request carrying a raw output_config.format is forced off the raw-passthrough path
// (UseRawRequestBody=false) for every provider whose native Anthropic endpoint rejects
// that field (Vertex, Bedrock Mantle, Azure), so the field gets converted/stripped
// downstream instead of being forwarded verbatim. Anthropic itself supports the field
// natively and must stay on the raw path.
func TestCheckAnthropicPassthrough_OutputConfigEscapeHatch(t *testing.T) {
	cases := []struct {
		name       string
		model      string
		wantRawOff bool
	}{
		{"vertex", "vertex/claude-haiku-4-5", true},
		{"bedrock_mantle", "bedrock_mantle/claude-haiku-4-5", true},
		{"azure", "azure/claude-haiku-4-5", true},
		{"anthropic", "anthropic/claude-haiku-4-5", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reqCtx := &fasthttp.RequestCtx{}
			reqCtx.Request.Header.SetMethod(fasthttp.MethodPost)
			reqCtx.Request.Header.Set("user-agent", "claude-code/1.0")
			reqCtx.Request.Header.Set("x-api-key", "sk-ant-test")

			bifrostCtx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
			defer cancel()

			req := &anthropic.AnthropicMessageRequest{
				Model:     tc.model,
				MaxTokens: 1024,
				OutputConfig: &anthropic.AnthropicOutputConfig{
					Format: []byte(`{"type":"json_schema","json_schema":{"name":"my_schema"}}`),
				},
			}

			if err := checkAnthropicPassthrough(reqCtx, bifrostCtx, req); err != nil {
				t.Fatalf("checkAnthropicPassthrough: %v", err)
			}

			useRaw, _ := bifrostCtx.Value(schemas.BifrostContextKeyUseRawRequestBody).(bool)
			if tc.wantRawOff && useRaw {
				t.Errorf("expected UseRawRequestBody=false for %s (output_config.format unsupported natively), got true", tc.model)
			}
			if !tc.wantRawOff && !useRaw {
				t.Errorf("expected UseRawRequestBody to stay true for %s, got false", tc.model)
			}
		})
	}
}

package integrations

import (
	"context"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

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

// TestCheckAnthropicPassthrough_OAuthHeaderRouting locks in the split that stopped Bifrost
// leaking x-bf-vk upstream and breaking Bedrock's SigV4. In OAuth mode the caller's raw
// headers must land ONLY in the Anthropic-only passthrough key; BifrostContextKeyExtraHeaders
// is read by every provider, so anything placed there reaches Bedrock/Vertex/Azure too.
func TestCheckAnthropicPassthrough_OAuthHeaderRouting(t *testing.T) {
	reqCtx := &fasthttp.RequestCtx{}
	reqCtx.Request.Header.SetMethod(fasthttp.MethodPost)
	reqCtx.Request.Header.Set("user-agent", "claude-code/1.0")
	// OAuth mode: an sk-ant-oat bearer and no x-api-key.
	reqCtx.Request.Header.Set("Authorization", "Bearer sk-ant-oat01-caller-token")
	reqCtx.Request.Header.Set("anthropic-beta", "interleaved-thinking-2025-05-14")
	reqCtx.Request.Header.Set("x-bf-vk", "sk-bf-must-not-leak")
	reqCtx.Request.Header.Set("x-forwarded-for", "10.30.10.147")
	reqCtx.Request.Header.Set("x-claude-code-session-id", "sess-1")

	bifrostCtx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	// Whatever x-bf-eh-* already put in ExtraHeaders must survive: the OAuth branch used to
	// overwrite this key wholesale, silently dropping caller-requested forwarded headers.
	bifrostCtx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{
		"my-custom-header": {"kept"},
	})

	req := &anthropic.AnthropicMessageRequest{Model: "claude-opus-4-8", MaxTokens: 1024}
	if err := checkAnthropicPassthrough(reqCtx, bifrostCtx, req); err != nil {
		t.Fatalf("checkAnthropicPassthrough: %v", err)
	}

	// The transport must be able to write the reserved key (restricted writes gate plugins,
	// not the transport). If this is empty, OAuth passthrough is dead.
	passthrough, ok := bifrostCtx.Value(schemas.BifrostContextKeyPassthroughHeaders).(map[string][]string)
	if !ok || len(passthrough) == 0 {
		t.Fatalf("passthrough headers were not set — OAuth would lose the caller's credential")
	}
	// fasthttp canonicalizes header names, so look up case-insensitively (the product code
	// does the same via strings.ToLower / EqualFold at every check).
	lookup := func(m map[string][]string, name string) []string {
		for k, v := range m {
			if strings.EqualFold(k, name) {
				return v
			}
		}
		return nil
	}
	if got := lookup(passthrough, "authorization"); len(got) == 0 || got[0] != "Bearer sk-ant-oat01-caller-token" {
		t.Errorf("caller OAuth token missing from passthrough set: %v", got)
	}

	extra, _ := bifrostCtx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string)
	// x-bf-eh-* survives.
	if got := lookup(extra, "my-custom-header"); len(got) == 0 || got[0] != "kept" {
		t.Errorf("x-bf-eh-* header was clobbered: %v", extra)
	}
	// Nothing the caller sent may sit in the every-provider key.
	for _, leaked := range []string{"authorization", "x-bf-vk", "x-forwarded-for", "x-claude-code-session-id"} {
		if lookup(extra, leaked) != nil {
			t.Errorf("%q reached BifrostContextKeyExtraHeaders — every provider forwards that key", leaked)
		}
	}
}

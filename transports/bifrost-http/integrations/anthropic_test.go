package integrations

import (
	"context"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"
)

// TestAnthropicRawStreamTextCodecRewritesOnlyTextDelta verifies the codec preserves provider-native event structure.
func TestAnthropicRawStreamTextCodecRewritesOnlyTextDelta(t *testing.T) {
	codec := anthropicRawStreamTextCodec{}
	raw := `{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"Contact alice@example.com"},"usage":{"output_tokens":4}}`
	event, eligible, err := codec.Inspect(raw)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if !eligible || event.TargetID != "2" || event.Text != "Contact alice@example.com" {
		t.Fatalf("Inspect() = (%+v, %v), want target 2 text event", event, eligible)
	}

	rewritten, err := codec.Rewrite(raw, "Contact [EMAIL]\nnext")
	if err != nil {
		t.Fatalf("Rewrite() error = %v", err)
	}
	if got := gjson.Get(rewritten, "delta.text").String(); got != "Contact [EMAIL]\nnext" {
		t.Errorf("delta.text = %q", got)
	}
	if got := gjson.Get(rewritten, "type").String(); got != "content_block_delta" {
		t.Errorf("type = %q", got)
	}
	if got := gjson.Get(rewritten, "usage.output_tokens").Int(); got != 4 {
		t.Errorf("usage.output_tokens = %d", got)
	}
	if got := gjson.Get(raw, "delta.text").String(); got != "Contact alice@example.com" {
		t.Errorf("provider-original raw event changed to %q", got)
	}
}

// TestAnthropicRawStreamTextCodecIgnoresNonTextEvents verifies reasoning, tool JSON, and lifecycle events remain opaque.
func TestAnthropicRawStreamTextCodecIgnoresNonTextEvents(t *testing.T) {
	codec := anthropicRawStreamTextCodec{}
	cases := []string{
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"alice@example.com"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"signature_delta","signature":"alice@example.com"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"email\":\"alice@example.com\"}"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"message_stop"}`,
	}
	for _, raw := range cases {
		if event, eligible, err := codec.Inspect(raw); err != nil || eligible {
			t.Errorf("Inspect(%s) = (%+v, %v, %v), want ineligible", raw, event, eligible, err)
		}
	}
}

// TestAnthropicRawStreamTextCodecRejectsMalformedEligibleEvents verifies malformed native text events cannot be synchronized.
func TestAnthropicRawStreamTextCodecRejectsMalformedEligibleEvents(t *testing.T) {
	codec := anthropicRawStreamTextCodec{}
	cases := []string{
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta"}}`,
		`{"type":"content_block_delta","index":"zero","delta":{"type":"text_delta","text":"hello"}}`,
		`{"type":`,
	}
	for _, raw := range cases {
		if _, _, err := codec.Inspect(raw); err == nil {
			t.Errorf("Inspect(%q) error = nil", raw)
		}
	}
}

// TestRewriteAnthropicRawRequestBodyRedactsOnlyContentFields verifies native redaction covers conversation content without touching request metadata or tool arguments.
func TestRewriteAnthropicRawRequestBodyRedactsOnlyContentFields(t *testing.T) {
	rawBody := []byte(`{
		"model":"claude-sonnet-4-5",
		"prompt":"legacy alice@example.com",
		"system":[{"type":"text","text":"system alice@example.com"}],
		"messages":[
			{"role":"user","content":"first alice@example.com"},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"reason alice@example.com","signature":"alice@example.com"},
				{"type":"redacted_thinking","data":"alice@example.com"},
				{"type":"compaction","content":"summary alice@example.com"},
				{"type":"text","text":"answer alice@example.com"},
				{"type":"tool_use","id":"call_1","name":"lookup","input":{"email":"alice@example.com"}}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"call_1","content":"tool alice@example.com"},
				{"type":"mcp_tool_result","tool_use_id":"call_2","content":[{"type":"text","text":"nested alice@example.com"}]}
			]}
		],
		"metadata":{"user_id":"alice@example.com"},
		"tools":[{"name":"lookup","description":"alice@example.com","input_schema":{"type":"object"}}]
	}`)

	got, err := rewriteAnthropicRawRequestBody(rawBody, map[string]string{"alice@example.com": "[EMAIL]"})
	if err != nil {
		t.Fatalf("rewriteAnthropicRawRequestBody() error = %v", err)
	}

	redactedPaths := []string{
		"prompt",
		"system.0.text",
		"messages.0.content",
		"messages.1.content.3.text",
		"messages.2.content.0.content",
		"messages.2.content.1.content.0.text",
	}
	for _, path := range redactedPaths {
		if value := gjson.GetBytes(got, path).String(); strings.Contains(value, "alice@example.com") || !strings.Contains(value, "[EMAIL]") {
			t.Errorf("%s = %q, want redacted content", path, value)
		}
	}

	untouchedPaths := map[string]string{
		"messages.1.content.0.thinking":    "reason alice@example.com",
		"messages.1.content.0.signature":   "alice@example.com",
		"messages.1.content.1.data":        "alice@example.com",
		"messages.1.content.2.content":     "summary alice@example.com",
		"messages.1.content.4.input.email": "alice@example.com",
		"metadata.user_id":                 "alice@example.com",
		"tools.0.description":              "alice@example.com",
	}
	for path, expected := range untouchedPaths {
		if value := gjson.GetBytes(got, path).String(); value != expected {
			t.Errorf("%s = %q, want untouched %q", path, value, expected)
		}
	}
}

// TestRewriteAnthropicRawRequestBodyRejectsUnmappedLiteral verifies a normalized runtime mutation cannot silently leave native content unredacted.
func TestRewriteAnthropicRawRequestBodyRejectsUnmappedLiteral(t *testing.T) {
	_, err := rewriteAnthropicRawRequestBody(
		[]byte(`{"messages":[{"role":"user","content":"hello"}]}`),
		map[string]string{"alice@example.com": "[EMAIL]"},
	)
	if err == nil {
		t.Fatal("rewriteAnthropicRawRequestBody() error = nil, want unmapped literal error")
	}
}

// TestRewriteAnthropicRawRequestBodyRejectsMalformedJSON verifies native rewrites never repair or forward a malformed body.
func TestRewriteAnthropicRawRequestBodyRejectsMalformedJSON(t *testing.T) {
	_, err := rewriteAnthropicRawRequestBody([]byte(`{"messages":`), map[string]string{"alice@example.com": "[EMAIL]"})
	if err == nil {
		t.Fatal("rewriteAnthropicRawRequestBody() error = nil, want malformed JSON error")
	}
}

// TestRewriteAnthropicRawRequestBodyRejectsDuplicateKeys prevents parser precedence from selecting an unredacted duplicate value.
func TestRewriteAnthropicRawRequestBodyRejectsDuplicateKeys(t *testing.T) {
	_, err := rewriteAnthropicRawRequestBody(
		[]byte(`{"messages":[{"role":"user","content":"alice@example.com","content":"alice@example.com"}]}`),
		map[string]string{"alice@example.com": "[EMAIL]"},
	)
	if err == nil {
		t.Fatal("rewriteAnthropicRawRequestBody() error = nil, want duplicate-key error")
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
			_, hasRewriter := bifrostCtx.Value(schemas.BifrostContextKeyRawRequestBodyTextRewriter).(schemas.RawRequestBodyTextRewriter)
			_, hasStreamCodec := bifrostCtx.Value(schemas.BifrostContextKeyRawStreamTextCodec).(schemas.RawStreamTextCodec)
			if tc.wantRawOff && hasRewriter {
				t.Errorf("expected raw request body text rewriter to remain unset for %s", tc.model)
			}
			if !tc.wantRawOff && !hasRewriter {
				t.Errorf("expected Anthropic raw request body text rewriter for %s", tc.model)
			}
			if tc.wantRawOff && hasStreamCodec {
				t.Errorf("expected raw stream text codec to remain unset for %s", tc.model)
			}
			if !tc.wantRawOff && !hasStreamCodec {
				t.Errorf("expected Anthropic raw stream text codec for %s", tc.model)
			}
		})
	}
}

// TestCheckAnthropicPassthrough_VertexBetaKeepsNativeResponseCodec verifies
// request-only Vertex escape hatches do not disable native Messages response rewriting.
func TestCheckAnthropicPassthrough_VertexBetaKeepsNativeResponseCodec(t *testing.T) {
	betaHeaders := []string{
		anthropic.AnthropicPromptCachingScopeBetaHeader,
		anthropic.AnthropicFastModeBetaHeader,
	}

	for _, betaHeader := range betaHeaders {
		t.Run(betaHeader, func(t *testing.T) {
			reqCtx := &fasthttp.RequestCtx{}
			reqCtx.Request.Header.SetMethod(fasthttp.MethodPost)
			reqCtx.Request.Header.Set("user-agent", "claude-code/1.0")
			reqCtx.Request.Header.Set("x-api-key", "sk-ant-test")
			reqCtx.Request.Header.Set(anthropic.AnthropicBetaHeader, betaHeader)

			bifrostCtx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
			defer cancel()

			req := &anthropic.AnthropicMessageRequest{
				Model:     "vertex/claude-opus-4-6",
				MaxTokens: 1024,
			}
			if err := checkAnthropicPassthrough(reqCtx, bifrostCtx, req); err != nil {
				t.Fatalf("checkAnthropicPassthrough() error = %v", err)
			}

			if useRaw, _ := bifrostCtx.Value(schemas.BifrostContextKeyUseRawRequestBody).(bool); useRaw {
				t.Fatal("Vertex beta request kept raw request-body passthrough enabled")
			}
			if sendRaw, _ := bifrostCtx.Value(schemas.BifrostContextKeySendBackRawResponse).(bool); !sendRaw {
				t.Fatal("Vertex beta request disabled native response forwarding")
			}
			if _, ok := bifrostCtx.Value(schemas.BifrostContextKeyRawRequestBodyTextRewriter).(schemas.RawRequestBodyTextRewriter); ok {
				t.Fatal("Vertex beta request registered a raw request-body rewriter")
			}
			if _, ok := bifrostCtx.Value(schemas.BifrostContextKeyRawStreamTextCodec).(schemas.RawStreamTextCodec); !ok {
				t.Fatal("Vertex beta request did not register the native response codec")
			}
		})
	}
}

// TestCheckAnthropicPassthroughLegacyCompleteOmitsStreamCodec verifies only Messages streams register the native SSE codec.
func TestCheckAnthropicPassthroughLegacyCompleteOmitsStreamCodec(t *testing.T) {
	reqCtx := &fasthttp.RequestCtx{}
	reqCtx.Request.Header.SetMethod(fasthttp.MethodPost)
	reqCtx.Request.Header.Set("user-agent", "claude-code/1.0")
	reqCtx.Request.Header.Set("x-api-key", "sk-ant-test")
	bifrostCtx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	req := &anthropic.AnthropicTextRequest{Model: "anthropic/claude-haiku-4-5"}
	if err := checkAnthropicPassthrough(reqCtx, bifrostCtx, req); err != nil {
		t.Fatalf("checkAnthropicPassthrough() error = %v", err)
	}
	if _, ok := bifrostCtx.Value(schemas.BifrostContextKeyRawStreamTextCodec).(schemas.RawStreamTextCodec); ok {
		t.Fatal("legacy complete request registered a Messages raw stream codec")
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

// TestIsClaudeModel_MultiFamilyProvidersResolveFamily locks in that the response-side raw
// passthrough decision resolves the model family instead of sniffing names. An alias labelled
// "claude-opus" on Bedrock Mantle may point at an OpenAI model; forwarding that provider's
// Responses body verbatim to an Anthropic client produced a malformed HTTP 200 ("body is JSON
// but not a Message") in Claude Code. The alias's ModelID — not its label — decides.
func TestIsClaudeModel_MultiFamilyProvidersResolveFamily(t *testing.T) {
	claudeAlias := &schemas.ResolvedAlias{
		Key:    "claude-opus",
		Config: &schemas.AliasConfig{ModelID: "openai.gpt-5.6-sol"},
	}
	neutralAlias := &schemas.ResolvedAlias{
		Key:    "fast-model",
		Config: &schemas.AliasConfig{ModelID: "anthropic.claude-sonnet-4-20250514-v1:0"},
	}
	// Azure deployment names / Vertex endpoint ids: the Claude identity lives in ModelName, not ModelID.
	deploymentAlias := &schemas.ResolvedAlias{
		Key:    "fast-model",
		Config: &schemas.AliasConfig{ModelID: "prod-deployment-01", ModelName: schemas.Ptr("claude-sonnet-4")},
	}

	cases := []struct {
		name     string
		provider schemas.ModelProvider
		model    string // ExtraFields.OriginalModelRequested
		alias    string // ExtraFields.ResolvedModelUsed (the wire model)
		resolved *schemas.ResolvedAlias
		want     bool
	}{
		{"mantle claude-named alias to openai model", schemas.BedrockMantle, "claude-opus", "openai.gpt-5.6-sol", claudeAlias, false},
		{"vertex claude-named alias to openai model", schemas.Vertex, "claude-opus", "openai.gpt-5.6-sol", claudeAlias, false},
		{"azure claude-named alias to openai model", schemas.Azure, "claude-opus", "openai.gpt-5.6-sol", claudeAlias, false},
		{"mantle neutral alias to claude model", schemas.BedrockMantle, "fast-model", "anthropic.claude-sonnet-4-20250514-v1:0", neutralAlias, true},
		{"mantle unaliased claude model", schemas.BedrockMantle, "claude-sonnet-4-20250514", "claude-sonnet-4-20250514", nil, true},
		{"mantle unaliased openai model", schemas.BedrockMantle, "gpt-5-6-luna", "gpt-5-6-luna", nil, false},
		{
			// Neither name looks Claude, so this keeps converting exactly as it did before the family
			// check existed — the predicate must never newly enable raw passthrough.
			name:     "azure deployment alias naming claude only in model_name",
			provider: schemas.Azure,
			model:    "fast-model",
			alias:    "prod-deployment-01",
			resolved: deploymentAlias,
			want:     false,
		},
		{"anthropic provider always native", schemas.Anthropic, "claude-sonnet-4-20250514", "claude-sonnet-4-20250514", nil, true},
		{"bedrock is never native", schemas.Bedrock, "claude-sonnet-4-20250514", "anthropic.claude-sonnet-4-20250514-v1:0", nil, false},
		// Ingress: no alias is resolved yet, so the caller-sent model is all there is to go on.
		{"ingress claude model", "", "claude-sonnet-5", "", nil, true},
		{"ingress non-claude model", "", "gpt-5", "", nil, false},
		{"ingress mantle claude model", schemas.BedrockMantle, "claude-haiku-4-5", "", nil, true},
		{"ingress vertex claude model", schemas.Vertex, "claude-haiku-4-5", "", nil, true},
		{"ingress azure claude model", schemas.Azure, "claude-haiku-4-5", "", nil, true},
		{"ingress mantle openai model", schemas.BedrockMantle, "gpt-5-6-luna", "", nil, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
			defer cancel()
			if tc.resolved != nil {
				ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, tc.resolved)
			}

			if got := isClaudeModel(ctx, tc.model, tc.alias, string(tc.provider)); got != tc.want {
				t.Errorf("isClaudeModel(%q, %q, %q) = %v, want %v", tc.model, tc.alias, tc.provider, got, tc.want)
			}
		})
	}
}

// TestCheckAnthropicPassthrough_ProviderPrefixedClaudeModel pins the ingress half of the
// passthrough decision for provider-prefixed Claude requests. TestCheckAnthropicPassthrough_
// OutputConfigEscapeHatch cannot cover this: it asserts UseRawRequestBody ends up false for these
// providers, which also holds when passthrough was never enabled at all.
func TestCheckAnthropicPassthrough_ProviderPrefixedClaudeModel(t *testing.T) {
	cases := []struct {
		name      string
		model     string
		wantRawOn bool
	}{
		{"vertex claude", "vertex/claude-haiku-4-5", true},
		{"bedrock_mantle claude", "bedrock_mantle/claude-haiku-4-5", true},
		{"azure claude", "azure/claude-haiku-4-5", true},
		{"bedrock_mantle openai", "bedrock_mantle/gpt-5-6-luna", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reqCtx := &fasthttp.RequestCtx{}
			reqCtx.Request.Header.SetMethod(fasthttp.MethodPost)
			reqCtx.Request.Header.Set("user-agent", "claude-code/1.0")
			reqCtx.Request.Header.Set("x-api-key", "sk-ant-test")

			bifrostCtx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
			defer cancel()

			req := &anthropic.AnthropicMessageRequest{Model: tc.model, MaxTokens: 1024}
			if err := checkAnthropicPassthrough(reqCtx, bifrostCtx, req); err != nil {
				t.Fatalf("checkAnthropicPassthrough: %v", err)
			}

			useRaw, _ := bifrostCtx.Value(schemas.BifrostContextKeyUseRawRequestBody).(bool)
			if useRaw != tc.wantRawOn {
				t.Errorf("UseRawRequestBody = %v, want %v for %s", useRaw, tc.wantRawOn, tc.model)
			}
		})
	}
}

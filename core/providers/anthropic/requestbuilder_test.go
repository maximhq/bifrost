package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

func makeSimpleInput(text string) []schemas.ResponsesMessage {
	role := schemas.ResponsesInputMessageRoleUser
	return []schemas.ResponsesMessage{
		{
			Role:    &role,
			Content: &schemas.ResponsesMessageContent{ContentStr: &text},
		},
	}
}

func TestSafeguardsRequestBuilders(t *testing.T) {
	const beta = "dangerous-tool-use-2026-09-03"
	for _, provider := range []schemas.ModelProvider{schemas.Anthropic, schemas.Bedrock, schemas.BedrockMantle, schemas.Vertex, schemas.Azure} {
		for _, raw := range []bool{false, true} {
			for _, chat := range []bool{false, true} {
				for _, streaming := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/raw=%v/chat=%v/stream=%v", provider, raw, chat, streaming), func(t *testing.T) {
						ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
						ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, raw)
						payload := json.RawMessage(`{"z":1,"a":{"b":true}}`)
						extra := map[string]interface{}{"safeguards": payload}
						body := []byte(`{"model":"claude-opus-4-8","max_tokens":32,"messages":[{"role":"user","content":"hi"}],"safeguards":{"z":1,"a":{"b":true}}}`)
						cfg := AnthropicRequestBuildConfig{Provider: provider, Model: "claude-opus-4-8", IsStreaming: streaming}
						var out []byte
						var err *schemas.BifrostError
						if chat {
							out, err = BuildAnthropicChatRequestBody(ctx, &schemas.BifrostChatRequest{Provider: provider, Model: "claude-opus-4-8", RawRequestBody: body, Input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}}, Params: &schemas.ChatParameters{ExtraParams: extra}}, cfg)
						} else {
							out, err = BuildAnthropicResponsesRequestBody(ctx, &schemas.BifrostResponsesRequest{Provider: provider, Model: "claude-opus-4-8", RawRequestBody: body, Input: makeSimpleInput("hi"), Params: &schemas.ResponsesParameters{ExtraParams: extra}}, cfg)
						}
						if err != nil {
							t.Fatalf("build: %v", err)
						}
						if got := providerUtils.GetJSONField(out, "safeguards").Raw; got != string(payload) {
							t.Errorf("safeguards = %s; body=%s", got, out)
						}
						if _, ok := extra["safeguards"]; !ok {
							t.Error("conversion consumed safeguards from the input used by fallbacks")
						}
						betas := FilterBetaHeadersForProvider(MergeBetaHeaders(ctx, nil), provider)
						if !slices.Contains(betas, beta) {
							t.Errorf("missing required beta: %v", betas)
						}
						if provider == schemas.Bedrock || provider == schemas.Vertex {
							if !strings.Contains(providerUtils.GetJSONField(out, "anthropic_beta").Raw, beta) {
								t.Errorf("missing body beta: %s", out)
							}
						} else if providerUtils.JSONFieldExists(out, "anthropic_beta") {
							t.Errorf("unexpected body beta: %s", out)
						}
					})
				}
			}
		}
	}
}

func TestBuildAnthropicResponsesRequestBody_RawBodyPath(t *testing.T) {
	t.Run("anthropic_native_uses_resolved_model", func(t *testing.T) {
		// request.Model is always the alias-resolved value by the time the provider
		// method is called (k.Aliases.Resolve runs in executeRequestWithRetries before
		// the provider is invoked). The raw body may still carry the governance-modified
		// form ("anthropic/anthropic.claude-sonnet-4-5"), but the output should use the
		// resolved model from request.Model.
		ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)

		request := &schemas.BifrostResponsesRequest{
			Provider:       schemas.Anthropic,
			Model:          "claude-sonnet-4-5", // alias-resolved; no prefix
			RawRequestBody: []byte(`{"model":"anthropic/anthropic.claude-sonnet-4-5","max_tokens":1024,"messages":[{"role":"user","content":"hello"}]}`),
		}

		result, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
			Provider: schemas.Anthropic,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		modelVal := providerUtils.GetJSONField(result, "model").String()
		if modelVal != "claude-sonnet-4-5" {
			t.Errorf("expected model to be 'claude-sonnet-4-5', got %q", modelVal)
		}
	})

	t.Run("dot_notation_alias_resolved_in_raw_body", func(t *testing.T) {
		// Regression test for: alias "anthropic.claude-sonnet-4-6" → "claude-sonnet-4-6"
		// being skipped on the raw-body path. Governance rewrites the body model to
		// "anthropic/anthropic.claude-sonnet-4-6"; request.Model holds the alias-resolved
		// value "claude-sonnet-4-6". The output must use the resolved model, not the raw bytes.
		ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)

		request := &schemas.BifrostResponsesRequest{
			Provider:       schemas.Anthropic,
			Model:          "claude-sonnet-4-6", // alias-resolved
			RawRequestBody: []byte(`{"model":"anthropic/anthropic.claude-sonnet-4-6","max_tokens":1024,"messages":[{"role":"user","content":"hello"}]}`),
		}

		result, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
			Provider: schemas.Anthropic,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		modelVal := providerUtils.GetJSONField(result, "model").String()
		if modelVal != "claude-sonnet-4-6" {
			t.Errorf("expected dot-notation alias to be resolved to 'claude-sonnet-4-6', got %q", modelVal)
		}
	})

	t.Run("vertex_deletes_model_field", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)

		request := &schemas.BifrostResponsesRequest{
			Provider:       schemas.Vertex,
			Model:          "claude-sonnet-4-5",
			RawRequestBody: []byte(`{"model":"claude-sonnet-4-5","max_tokens":1024,"messages":[{"role":"user","content":"hello"}]}`),
		}

		result, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
			Provider: schemas.Vertex,
			Model:    "claude-sonnet-4-5",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if providerUtils.JSONFieldExists(result, "model") {
			t.Error("expected model field to be deleted for Vertex")
		}
	})

	t.Run("azure_replaces_model_with_deployment", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)

		request := &schemas.BifrostResponsesRequest{
			Provider:       schemas.Azure,
			Model:          "claude-sonnet-4-5",
			RawRequestBody: []byte(`{"model":"claude-sonnet-4-5","max_tokens":1024,"messages":[{"role":"user","content":"hello"}]}`),
		}

		result, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
			Provider: schemas.Azure,
			Model:    "my-azure-deployment",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		modelVal := providerUtils.GetJSONField(result, "model").String()
		if modelVal != "my-azure-deployment" {
			t.Errorf("expected model to be 'my-azure-deployment', got %q", modelVal)
		}
	})

	t.Run("azure_strips_claude_code_diagnostics", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)

		request := &schemas.BifrostResponsesRequest{
			Provider: schemas.Azure,
			Model:    "claude-opus-4-7",
			RawRequestBody: []byte(`{
				"model":"claude-opus-4-7",
				"max_tokens":64000,
				"messages":[{"role":"user","content":"hi"}],
				"diagnostics":{"previous_message_id":null}
			}`),
		}

		result, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
			Provider: schemas.Azure,
			Model:    "my-azure-deployment",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if providerUtils.JSONFieldExists(result, "diagnostics") {
			t.Fatalf("expected diagnostics to be stripped for Azure, got: %s", string(result))
		}
		if providerUtils.GetJSONField(result, "model").String() != "my-azure-deployment" {
			t.Fatalf("expected Azure deployment model rewrite, got: %s", string(result))
		}
	})

	t.Run("adds_max_tokens_if_missing", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)

		request := &schemas.BifrostResponsesRequest{
			Provider:       schemas.Anthropic,
			Model:          "claude-sonnet-4-5",
			RawRequestBody: []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}]}`),
		}

		result, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
			Provider: schemas.Anthropic,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if !providerUtils.JSONFieldExists(result, "max_tokens") {
			t.Error("expected max_tokens to be added")
		}
	})

	t.Run("adds_stream_when_streaming", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)

		request := &schemas.BifrostResponsesRequest{
			Provider:       schemas.Anthropic,
			Model:          "claude-sonnet-4-5",
			RawRequestBody: []byte(`{"model":"claude-sonnet-4-5","max_tokens":1024,"messages":[{"role":"user","content":"hello"}]}`),
		}

		result, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
			Provider:    schemas.Anthropic,
			IsStreaming: true,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		streamVal := providerUtils.GetJSONField(result, "stream").Bool()
		if !streamVal {
			t.Error("expected stream to be true")
		}
	})

	t.Run("deletes_region_field_when_configured", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)

		request := &schemas.BifrostResponsesRequest{
			Provider:       schemas.Vertex,
			Model:          "claude-sonnet-4-5",
			RawRequestBody: []byte(`{"model":"claude-sonnet-4-5","max_tokens":1024,"region":"us-central1","messages":[{"role":"user","content":"hello"}]}`),
		}

		result, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
			Provider: schemas.Vertex,
			Model:    "claude-sonnet-4-5",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if providerUtils.JSONFieldExists(result, "region") {
			t.Error("expected region field to be deleted")
		}
	})

	t.Run("adds_anthropic_version_when_configured", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)

		request := &schemas.BifrostResponsesRequest{
			Provider:       schemas.Vertex,
			Model:          "claude-sonnet-4-5",
			RawRequestBody: []byte(`{"model":"claude-sonnet-4-5","max_tokens":1024,"messages":[{"role":"user","content":"hello"}]}`),
		}

		result, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
			Provider: schemas.Vertex,
			Model:    "claude-sonnet-4-5",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		versionVal := providerUtils.GetJSONField(result, "anthropic_version").String()
		if versionVal != "vertex-2023-10-16" {
			t.Errorf("expected anthropic_version 'vertex-2023-10-16', got %q", versionVal)
		}
	})

	t.Run("excludes_specified_fields", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)

		request := &schemas.BifrostResponsesRequest{
			Provider:       schemas.Anthropic,
			Model:          "claude-sonnet-4-5",
			RawRequestBody: []byte(`{"model":"claude-sonnet-4-5","max_tokens":1024,"temperature":0.7,"messages":[{"role":"user","content":"hello"}]}`),
		}

		result, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
			Provider:      schemas.Anthropic,
			ExcludeFields: []string{"temperature"},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if providerUtils.JSONFieldExists(result, "temperature") {
			t.Error("expected temperature to be excluded")
		}
	})

	t.Run("always_deletes_fallbacks", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)

		request := &schemas.BifrostResponsesRequest{
			Provider:       schemas.Anthropic,
			Model:          "claude-sonnet-4-5",
			RawRequestBody: []byte(`{"model":"claude-sonnet-4-5","max_tokens":1024,"fallbacks":["claude-haiku-4-5"],"messages":[{"role":"user","content":"hello"}]}`),
		}

		result, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
			Provider: schemas.Anthropic,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if providerUtils.JSONFieldExists(result, "fallbacks") {
			t.Error("expected fallbacks to be deleted")
		}
	})

	t.Run("injects_beta_headers_into_body", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
		ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{
			"anthropic-beta": {AnthropicCompactionBetaHeader},
		})

		request := &schemas.BifrostResponsesRequest{
			Provider:       schemas.Vertex,
			Model:          "claude-sonnet-4-5",
			RawRequestBody: []byte(`{"model":"claude-sonnet-4-5","max_tokens":1024,"messages":[{"role":"user","content":"hello"}]}`),
		}

		result, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
			Provider: schemas.Vertex,
			Model:    "claude-sonnet-4-5",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if !providerUtils.JSONFieldExists(result, "anthropic_beta") {
			t.Error("expected anthropic_beta to be injected into body")
		}
	})
}

func TestBuildAnthropicResponsesRequestBody_ThreadFieldStripped(t *testing.T) {
	// Server-side thread state is bound to the account that created it; per-request
	// key selection, retries, and fallbacks cannot keep a continuation there, so the
	// raw path never forwards the field. Continuations themselves are refused at the
	// transport (anthropicRefuseThreadContinue) before reaching this builder.
	rawBody := []byte(`{"model":"claude-sonnet-4-5","max_tokens":1024,"messages":[{"role":"user","content":"hello"}],"thread":{"type":"create"}}`)

	t.Run("raw_path_strips_thread", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)

		request := &schemas.BifrostResponsesRequest{
			Provider:       schemas.Anthropic,
			Model:          "claude-sonnet-4-5",
			RawRequestBody: rawBody,
		}

		result, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
			Provider: schemas.Anthropic,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if providerUtils.JSONFieldExists(result, "thread") {
			t.Errorf("expected thread field to be stripped from the raw body, got %s", string(result))
		}
		if !providerUtils.JSONFieldExists(result, "messages") {
			t.Error("expected messages to survive the thread strip")
		}
	})

	t.Run("count_tokens_mode_strips_thread", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)

		request := &schemas.BifrostResponsesRequest{
			Provider:       schemas.Anthropic,
			Model:          "claude-sonnet-4-5",
			RawRequestBody: rawBody,
		}

		result, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
			Provider:      schemas.Anthropic,
			Model:         "claude-sonnet-4-5",
			IsCountTokens: true,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if providerUtils.JSONFieldExists(result, "thread") {
			t.Errorf("expected thread field to be stripped in count_tokens mode, got %s", string(result))
		}
	})
}

func TestBuildAnthropicResponsesRequestBody_CountTokensMode(t *testing.T) {
	t.Run("count_tokens_strips_max_tokens_and_temperature_raw", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)

		request := &schemas.BifrostResponsesRequest{
			Provider:       schemas.Vertex,
			Model:          "claude-sonnet-4-5",
			RawRequestBody: []byte(`{"model":"claude-sonnet-4-5","max_tokens":1024,"temperature":0.7,"messages":[{"role":"user","content":"hello"}]}`),
		}

		result, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
			Provider:      schemas.Vertex,
			Model:         "claude-sonnet-4-5",
			IsCountTokens: true,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if providerUtils.JSONFieldExists(result, "max_tokens") {
			t.Error("expected max_tokens to be stripped in count-tokens mode")
		}
		if providerUtils.JSONFieldExists(result, "temperature") {
			t.Error("expected temperature to be stripped in count-tokens mode")
		}
		if !providerUtils.JSONFieldExists(result, "model") {
			t.Error("expected model to be retained in count-tokens mode")
		}
	})

	t.Run("count_tokens_sets_deployment_as_model", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)

		request := &schemas.BifrostResponsesRequest{
			Provider:       schemas.Vertex,
			Model:          "claude-sonnet-4-5",
			RawRequestBody: []byte(`{"model":"old-model","max_tokens":1024,"messages":[{"role":"user","content":"hello"}]}`),
		}

		result, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
			Provider:      schemas.Vertex,
			Model:         "new-deployment",
			IsCountTokens: true,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		modelVal := providerUtils.GetJSONField(result, "model").String()
		if modelVal != "new-deployment" {
			t.Errorf("expected model 'new-deployment', got %q", modelVal)
		}
	})
}

func TestBuildAnthropicResponsesRequestBody_TypedPath(t *testing.T) {
	t.Run("typed_path_basic_request", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), time.Time{})

		request := &schemas.BifrostResponsesRequest{
			Provider: schemas.Anthropic,
			Model:    "claude-sonnet-4-5",
			Input:    makeSimpleInput("Hello, world!"),
		}

		result, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
			Provider: schemas.Anthropic,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if !providerUtils.JSONFieldExists(result, "model") {
			t.Error("expected model to be present")
		}
		if !providerUtils.JSONFieldExists(result, "messages") {
			t.Error("expected messages to be present")
		}
	})

	t.Run("typed_path_with_streaming", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), time.Time{})

		request := &schemas.BifrostResponsesRequest{
			Provider: schemas.Anthropic,
			Model:    "claude-sonnet-4-5",
			Input:    makeSimpleInput("Hello!"),
		}

		result, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
			Provider:    schemas.Anthropic,
			IsStreaming: true,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		streamVal := providerUtils.GetJSONField(result, "stream").Bool()
		if !streamVal {
			t.Error("expected stream to be true")
		}
	})

	t.Run("typed_path_vertex_deletes_model", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), time.Time{})

		request := &schemas.BifrostResponsesRequest{
			Provider: schemas.Vertex,
			Model:    "claude-sonnet-4-5",
			Input:    makeSimpleInput("Hello!"),
		}

		result, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
			Provider: schemas.Vertex,
			Model:    "claude-sonnet-4-5",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if providerUtils.JSONFieldExists(result, "model") {
			t.Error("expected model to be deleted for Vertex")
		}
	})

	t.Run("typed_path_adds_anthropic_version", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), time.Time{})

		request := &schemas.BifrostResponsesRequest{
			Provider: schemas.Vertex,
			Model:    "claude-sonnet-4-5",
			Input:    makeSimpleInput("Hello!"),
		}

		result, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
			Provider: schemas.Vertex,
			Model:    "claude-sonnet-4-5",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		versionVal := providerUtils.GetJSONField(result, "anthropic_version").String()
		if versionVal != "vertex-2023-10-16" {
			t.Errorf("expected anthropic_version 'vertex-2023-10-16', got %q", versionVal)
		}
	})

	t.Run("typed_path_count_tokens_strips_fields", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(nil, time.Time{})

		temp := 0.7
		request := &schemas.BifrostResponsesRequest{
			Provider: schemas.Vertex,
			Model:    "claude-sonnet-4-5",
			Input:    makeSimpleInput("Hello!"),
			Params: &schemas.ResponsesParameters{
				Temperature: &temp,
			},
		}

		result, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
			Provider:      schemas.Vertex,
			Model:         "claude-sonnet-4-5",
			IsCountTokens: true,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if providerUtils.JSONFieldExists(result, "max_tokens") {
			t.Error("expected max_tokens to be stripped in count-tokens mode")
		}
		if providerUtils.JSONFieldExists(result, "temperature") {
			t.Error("expected temperature to be stripped in count-tokens mode")
		}
	})

	t.Run("typed_path_strips_unsupported_tools_when_configured", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), time.Time{})

		// A genuinely unsupported tool on Bedrock (web_fetch) must be silently
		// dropped — not error the whole request (mirrors the Chat path and the
		// Bedrock Responses path; restores pre-v1.5.0 behavior, see issue #3795).
		// The supported function tool must survive.
		request := &schemas.BifrostResponsesRequest{
			Provider: schemas.Bedrock,
			Model:    "claude-sonnet-4-5",
			Input:    makeSimpleInput("Hello!"),
			Params: &schemas.ResponsesParameters{
				Tools: []schemas.ResponsesTool{
					{
						Type:                  schemas.ResponsesToolTypeFunction,
						Name:                  schemas.Ptr("keep_me"),
						ResponsesToolFunction: &schemas.ResponsesToolFunction{},
					},
					{Type: schemas.ResponsesToolTypeWebFetch},
				},
			},
		}

		result, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
			Provider:      schemas.Bedrock,
			ValidateTools: true,
		})
		if err != nil {
			t.Fatalf("unexpected error (web_fetch should be stripped, not rejected): %v", err)
		}
		if !strings.Contains(string(result), "keep_me") {
			t.Error("expected supported function tool to survive stripping")
		}
		if strings.Contains(string(result), "web_fetch") {
			t.Error("expected unsupported web_fetch tool to be stripped from the request body")
		}
		// The inbound request must not be mutated by the shallow-copy strip.
		if len(request.Params.Tools) != 2 {
			t.Errorf("inbound Params.Tools must be untouched, got %d tools", len(request.Params.Tools))
		}
	})
}

// TestBuildAnthropicResponsesRequestBody_ReasoningMaxTokensTooLow is a regression test:
// a max_tokens too low for the resolved reasoning budget must surface as a clean 400,
// not an opaque 500. Before the fix, GetBudgetTokensFromReasoningEffort's plain error
// (and the equivalent explicit MinimumReasoningMaxTokens check) got wrapped by
// NewBifrostOperationError, which never sets StatusCode, so the HTTP layer defaulted
// to 500.
func TestBuildAnthropicResponsesRequestBody_ReasoningMaxTokensTooLow(t *testing.T) {
	t.Run("adaptive_effort_on_non_adaptive_model", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(nil, time.Time{})

		// claude-haiku-4-5 supports neither adaptive thinking nor native effort, so
		// this falls to the budget_tokens-only branch, which 500'd on a too-low
		// max_tokens before this fix.
		request := &schemas.BifrostResponsesRequest{
			Provider: schemas.Anthropic,
			Model:    "claude-haiku-4-5",
			Input:    makeSimpleInput("Hello, world!"),
			Params: &schemas.ResponsesParameters{
				MaxOutputTokens: schemas.Ptr(500),
				Reasoning: &schemas.ResponsesParametersReasoning{
					Effort: schemas.Ptr("high"),
				},
			},
		}

		_, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
			Provider: schemas.Anthropic,
		})
		if err == nil {
			t.Fatal("expected an error for max_tokens below the reasoning minimum")
		}
		if err.StatusCode == nil || *err.StatusCode != 400 {
			got := "nil"
			if err.StatusCode != nil {
				got = fmt.Sprintf("%d", *err.StatusCode)
			}
			t.Errorf("expected StatusCode 400, got %s", got)
		}
	})

	t.Run("explicit_reasoning_max_tokens_below_minimum", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(nil, time.Time{})

		request := &schemas.BifrostResponsesRequest{
			Provider: schemas.Anthropic,
			Model:    "claude-haiku-4-5",
			Input:    makeSimpleInput("Hello, world!"),
			Params: &schemas.ResponsesParameters{
				MaxOutputTokens: schemas.Ptr(2000),
				Reasoning: &schemas.ResponsesParametersReasoning{
					MaxTokens: schemas.Ptr(100), // below MinimumReasoningMaxTokens (1024)
				},
			},
		}

		_, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
			Provider: schemas.Anthropic,
		})
		if err == nil {
			t.Fatal("expected an error for reasoning.max_tokens below the minimum")
		}
		if err.StatusCode == nil || *err.StatusCode != 400 {
			got := "nil"
			if err.StatusCode != nil {
				got = fmt.Sprintf("%d", *err.StatusCode)
			}
			t.Errorf("expected StatusCode 400, got %s", got)
		}
	})
}

func TestBuildAnthropicResponsesRequestBody_LargePayloadPassthrough(t *testing.T) {
	t.Run("returns_nil_when_large_payload_enabled", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(nil, time.Time{})
		ctx.SetValue(schemas.BifrostContextKeyLargePayloadMode, true)
		ctx.SetValue(schemas.BifrostContextKeyLargePayloadReader, io.NopCloser(strings.NewReader(`{"model":"claude-sonnet-4-5"}`)))

		request := &schemas.BifrostResponsesRequest{
			Provider:       schemas.Anthropic,
			Model:          "claude-sonnet-4-5",
			RawRequestBody: []byte(`{"model":"claude-sonnet-4-5"}`),
		}

		result, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
			Provider: schemas.Anthropic,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != nil {
			t.Error("expected nil result when large payload passthrough enabled")
		}
	})
}

func TestDoesWebSearchOrFetchAutoInjectCodeExecution(t *testing.T) {
	tests := []struct {
		toolType string
		expected bool
	}{
		{string(AnthropicToolTypeWebSearch20250305), false},
		{string(AnthropicToolTypeWebSearch20260209), true},
		{string(AnthropicToolTypeWebFetch20250910), false},
		{string(AnthropicToolTypeWebFetch20260209), true},
		{string(AnthropicToolTypeWebFetch20260309), true},
		{string(AnthropicToolTypeWebFetch20260318), true},
		{"web_search_unknown", true},
		{"web_fetch_unknown", true},
		{"unknown_type", true},
	}

	for _, tt := range tests {
		t.Run(tt.toolType, func(t *testing.T) {
			got := doesWebSearchOrFetchAutoInjectCodeExecution(tt.toolType)
			if got != tt.expected {
				t.Errorf("doesWebSearchOrFetchAutoInjectCodeExecution(%q) = %v, want %v", tt.toolType, got, tt.expected)
			}
		})
	}
}

func TestStripAutoInjectableTools_VersionAware(t *testing.T) {
	t.Run("web_search_20250305_does_not_trigger_code_execution_strip", func(t *testing.T) {
		input := []byte(`{"tools":[{"type":"code_execution_20250825","name":"code_execution"},{"type":"web_search_20250305","name":"web_search"}]}`)
		result, err := StripAutoInjectableTools(input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		tools := providerUtils.GetJSONField(result, "tools")
		arr := tools.Array()
		if len(arr) != 2 {
			t.Errorf("expected 2 tools (code_execution preserved with old web_search), got %d", len(arr))
		}
	})

	t.Run("web_search_20260209_triggers_code_execution_strip", func(t *testing.T) {
		input := []byte(`{"tools":[{"type":"code_execution_20250825","name":"code_execution"},{"type":"web_search_20260209","name":"web_search"}]}`)
		result, err := StripAutoInjectableTools(input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		tools := providerUtils.GetJSONField(result, "tools")
		arr := tools.Array()
		if len(arr) != 1 {
			t.Errorf("expected 1 tool (code_execution stripped), got %d", len(arr))
		}
		if arr[0].Get("name").String() != "web_search" {
			t.Errorf("expected remaining tool to be 'web_search', got %q", arr[0].Get("name").String())
		}
	})

	t.Run("web_fetch_20250910_does_not_trigger_code_execution_strip", func(t *testing.T) {
		input := []byte(`{"tools":[{"type":"code_execution_20250825","name":"code_execution"},{"type":"web_fetch_20250910","name":"web_fetch"}]}`)
		result, err := StripAutoInjectableTools(input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		tools := providerUtils.GetJSONField(result, "tools")
		arr := tools.Array()
		if len(arr) != 2 {
			t.Errorf("expected 2 tools (code_execution preserved with old web_fetch), got %d", len(arr))
		}
	})

	t.Run("web_fetch_20260209_triggers_code_execution_strip", func(t *testing.T) {
		input := []byte(`{"tools":[{"type":"code_execution_20250825","name":"code_execution"},{"type":"web_fetch_20260209","name":"web_fetch"}]}`)
		result, err := StripAutoInjectableTools(input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		tools := providerUtils.GetJSONField(result, "tools")
		arr := tools.Array()
		if len(arr) != 1 {
			t.Errorf("expected 1 tool (code_execution stripped), got %d", len(arr))
		}
	})

	t.Run("web_fetch_20260309_triggers_code_execution_strip", func(t *testing.T) {
		input := []byte(`{"tools":[{"type":"code_execution_20250825","name":"code_execution"},{"type":"web_fetch_20260309","name":"web_fetch"}]}`)
		result, err := StripAutoInjectableTools(input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		tools := providerUtils.GetJSONField(result, "tools")
		arr := tools.Array()
		if len(arr) != 1 {
			t.Errorf("expected 1 tool (code_execution stripped), got %d", len(arr))
		}
	})

	t.Run("mixed_old_and_new_web_tools_first_match_wins", func(t *testing.T) {
		input := []byte(`{"tools":[{"type":"code_execution_20250825","name":"code_execution"},{"type":"web_search_20250305","name":"old_search"},{"type":"web_search_20260209","name":"new_search"}]}`)
		result, err := StripAutoInjectableTools(input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		tools := providerUtils.GetJSONField(result, "tools")
		arr := tools.Array()
		if len(arr) != 3 {
			t.Errorf("expected 3 tools (first web tool is old version, no strip), got %d", len(arr))
		}
	})

	t.Run("new_web_fetch_first_strips_code_execution", func(t *testing.T) {
		input := []byte(`{"tools":[{"type":"code_execution_20250825","name":"code_execution"},{"type":"web_fetch_20260209","name":"new_fetch"},{"type":"web_search_20250305","name":"old_search"}]}`)
		result, err := StripAutoInjectableTools(input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		tools := providerUtils.GetJSONField(result, "tools")
		arr := tools.Array()
		if len(arr) != 2 {
			t.Errorf("expected 2 tools (code_execution stripped due to new web_fetch), got %d", len(arr))
		}
	})
}

func TestAnthropicToolTypeString(t *testing.T) {
	tests := []struct {
		toolType AnthropicToolType
		expected string
	}{
		{AnthropicToolTypeWebSearch20250305, "web_search_20250305"},
		{AnthropicToolTypeWebSearch20260209, "web_search_20260209"},
		{AnthropicToolTypeWebFetch20250910, "web_fetch_20250910"},
		{AnthropicToolTypeWebFetch20260209, "web_fetch_20260209"},
		{AnthropicToolTypeWebFetch20260309, "web_fetch_20260309"},
		{AnthropicToolTypeComputer20251124, "computer_20251124"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			got := string(tt.toolType)
			if got != tt.expected {
				t.Errorf("AnthropicToolType.String() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestBuildAnthropicResponsesRequestBody_StripCacheControlScope(t *testing.T) {
	t.Run("typed_path_strips_cache_control_scope_when_configured", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), time.Time{})

		request := &schemas.BifrostResponsesRequest{
			Provider: schemas.Vertex,
			Model:    "claude-sonnet-4-5",
			Input:    makeSimpleInput("Hello!"),
		}

		_, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
			Provider: schemas.Vertex,
			Model:    "claude-sonnet-4-5",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestBuildAnthropicResponsesRequestBody_RemapToolVersions(t *testing.T) {
	t.Run("raw_path_remaps_tool_versions_when_configured", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(nil, time.Time{})
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)

		request := &schemas.BifrostResponsesRequest{
			Provider:       schemas.Vertex,
			Model:          "claude-sonnet-4-5",
			RawRequestBody: []byte(`{"model":"claude-sonnet-4-5","max_tokens":1024,"tools":[{"type":"web_search_20260209","name":"web_search"}],"messages":[{"role":"user","content":"hello"}]}`),
		}

		result, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
			Provider: schemas.Vertex,
			Model:    "claude-sonnet-4-5",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		tools := providerUtils.GetJSONField(result, "tools")
		if !tools.Exists() {
			t.Fatal("expected tools to exist")
		}
		arr := tools.Array()
		if len(arr) == 0 {
			t.Fatal("expected at least one tool")
		}
		toolType := arr[0].Get("type").String()
		if toolType == "web_search_20260209" {
			t.Error("expected tool type to be remapped from web_search_20260209")
		}
	})
}

// Regression tests for maximhq/bifrost#6825.
//
// The Bedrock provider routes Claude requests that carry a compact_20260112
// edit to InvokeModel / InvokeModelWithResponseStream, because AWS documents
// compaction as unsupported on Converse:
// https://docs.aws.amazon.com/bedrock/latest/userguide/claude-messages-compaction.html
//
// InvokeModel takes the native Anthropic Messages body with three Bedrock
// specifics, per
// https://docs.aws.amazon.com/bedrock/latest/userguide/model-parameters-anthropic-claude-messages-request-response.html:
//   - anthropic_version must be "bedrock-2023-05-31"
//   - the model is in the URL, so the body carries no "model"
//   - streaming is selected by the URL, so the body carries no "stream"
//   - beta features are opted into via the anthropic_beta body array
// The shared anthropic request builder must produce exactly that shape when
// cfg.Provider is schemas.Bedrock.

const bedrockInvokeCompactionContextManagement = `{"edits":[{"type":"compact_20260112","trigger":{"type":"input_tokens","value":50000}}]}`

func assertBedrockInvokeBodyShape(t *testing.T, body []byte) {
	t.Helper()
	if providerUtils.JSONFieldExists(body, "model") {
		t.Errorf("InvokeModel body must not carry model (it is in the URL), got: %s", string(body))
	}
	if providerUtils.JSONFieldExists(body, "stream") {
		t.Errorf("InvokeModel body must not carry stream (the URL selects streaming), got: %s", string(body))
	}
	if got := providerUtils.GetJSONField(body, "anthropic_version").String(); got != "bedrock-2023-05-31" {
		t.Errorf("anthropic_version = %q, want %q", got, "bedrock-2023-05-31")
	}
	betas := providerUtils.GetJSONField(body, "anthropic_beta")
	if !betas.Exists() || !betas.IsArray() {
		t.Fatalf("anthropic_beta array missing, got: %s", string(body))
	}
	var betaValues []string
	for _, b := range betas.Array() {
		betaValues = append(betaValues, b.String())
	}
	if !slices.Contains(betaValues, AnthropicCompactionBetaHeader) {
		t.Errorf("anthropic_beta = %v, want it to contain %q", betaValues, AnthropicCompactionBetaHeader)
	}
	if got := providerUtils.GetJSONField(body, "context_management.edits.0.type").String(); got != string(ContextManagementEditTypeCompact) {
		t.Errorf("context_management.edits.0.type = %q, want %q; body=%s", got, ContextManagementEditTypeCompact, string(body))
	}
}

func TestBuildAnthropicResponsesRequestBody_BedrockInvokeShape(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	request := &schemas.BifrostResponsesRequest{
		Provider: schemas.Bedrock,
		Model:    "us.anthropic.claude-sonnet-4-6",
		Input:    makeSimpleInput("Hello!"),
		Params: &schemas.ResponsesParameters{
			ContextManagement: json.RawMessage(bedrockInvokeCompactionContextManagement),
		},
	}
	body, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
		Provider:    schemas.Bedrock,
		Model:       "us.anthropic.claude-sonnet-4-6",
		IsStreaming: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertBedrockInvokeBodyShape(t, body)
}

func TestBuildAnthropicChatRequestBody_BedrockInvokeShape(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	request := &schemas.BifrostChatRequest{
		Provider: schemas.Bedrock,
		Model:    "us.anthropic.claude-sonnet-4-6",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Hello!")}}},
		Params: &schemas.ChatParameters{
			ContextManagement: json.RawMessage(bedrockInvokeCompactionContextManagement),
		},
	}
	body, err := BuildAnthropicChatRequestBody(ctx, request, AnthropicRequestBuildConfig{
		Provider:    schemas.Bedrock,
		Model:       "us.anthropic.claude-sonnet-4-6",
		IsStreaming: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertBedrockInvokeBodyShape(t, body)
}

// Tool search is InvokeModel-only on Bedrock (see the routing tests in the
// bedrock package). Once a request is routed there, the shared builder must keep
// the tool_search tool, keep defer_loading on the deferred function tool, and
// opt in with the tool-search-tool-2025-10-19 beta in the anthropic_beta array.
func TestBuildAnthropicResponsesRequestBody_BedrockInvokeKeepsToolSearch(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	request := &schemas.BifrostResponsesRequest{
		Provider: schemas.Bedrock,
		Model:    "us.anthropic.claude-sonnet-4-6",
		Input:    makeSimpleInput("What is the weather in Paris?"),
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{
				responsesToolFromJSON(t, `{"type":"tool_search_tool_regex_20251119","name":"tool_search_tool_regex"}`),
				responsesToolFromJSON(t, `{"type":"function","name":"get_weather","description":"Get the weather","parameters":{"type":"object","properties":{"location":{"type":"string"}},"required":["location"]},"defer_loading":true}`),
			},
		},
	}
	body, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
		Provider:      schemas.Bedrock,
		Model:         "us.anthropic.claude-sonnet-4-6",
		ValidateTools: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tools := providerUtils.GetJSONField(body, "tools").Array()
	var sawToolSearch, sawDeferred bool
	for _, tool := range tools {
		if strings.HasPrefix(tool.Get("type").String(), "tool_search_tool_") {
			sawToolSearch = true
		}
		if tool.Get("name").String() == "get_weather" && tool.Get("defer_loading").Bool() {
			sawDeferred = true
		}
	}
	if !sawToolSearch {
		t.Errorf("tool_search tool was stripped from the InvokeModel body: %s", string(body))
	}
	if !sawDeferred {
		t.Errorf("defer_loading was stripped from the deferred function tool: %s", string(body))
	}
	var betas []string
	for _, b := range providerUtils.GetJSONField(body, "anthropic_beta").Array() {
		betas = append(betas, b.String())
	}
	if !slices.Contains(betas, AnthropicToolSearchBetaHeader) {
		t.Errorf("anthropic_beta = %v, want it to contain %q", betas, AnthropicToolSearchBetaHeader)
	}
}

// TestRawBodyBuilderKeepsToolsAndSetsBetaHeaders checks the thing that actually goes
// upstream, rather than any one step of building it.
//
// The beta probe strips input_schema and description from a local copy, and
// TestBetaProbeNeverMutatesTheOutboundBody proves that copy never touches the caller's
// bytes. But the builder does a great deal more to the body after that — strips thinking
// blocks, remaps tool versions, deletes fields, injects anthropic_version. This asserts
// the end of that pipeline: the body it returns still carries every tool intact, and the
// context carries the beta headers those tools imply.
//
// Put plainly: the final request gets all the tools AND all the headers.
func TestRawBodyBuilderKeepsToolsAndSetsBetaHeaders(t *testing.T) {
	rawBody := []byte(`{"model":"claude-opus-4-8","max_tokens":1024,` +
		`"tools":[` +
		`{"type":"custom","name":"lookup","description":"Look something up",` +
		`"input_schema":{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]},"strict":true},` +
		`{"type":"computer_20250124","name":"computer","description":"Use the computer",` +
		`"input_schema":{"type":"object"}}` +
		`],"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)

	out, bErr := BuildAnthropicResponsesRequestBody(ctx, &schemas.BifrostResponsesRequest{
		Provider:       schemas.Anthropic,
		Model:          "claude-opus-4-8",
		RawRequestBody: rawBody,
	}, AnthropicRequestBuildConfig{Provider: schemas.Anthropic})
	if bErr != nil {
		t.Fatalf("building request body: %v", bErr)
	}

	// 1. Every tool survives, with the fields the probe strips from its own copy.
	tools := providerUtils.GetJSONField(out, "tools")
	if !tools.IsArray() || len(tools.Array()) != 2 {
		t.Fatalf("outbound body lost tools: %s", out)
	}
	for i, want := range []struct{ name, description string }{
		{"lookup", "Look something up"},
		{"computer", "Use the computer"},
	} {
		base := fmt.Sprintf("tools.%d", i)
		if got := providerUtils.GetJSONField(out, base+".name").String(); got != want.name {
			t.Errorf("%s.name = %q, want %q", base, got, want.name)
		}
		if got := providerUtils.GetJSONField(out, base+".description").String(); got != want.description {
			t.Errorf("%s.description = %q, want %q (the probe's strip reached the wire)", base, got, want.description)
		}
		if !providerUtils.JSONFieldExists(out, base+".input_schema") {
			t.Errorf("%s.input_schema is missing from the outbound body", base)
		}
	}
	// The nested schema must be byte-intact, not merely present.
	if got := providerUtils.GetJSONField(out, "tools.0.input_schema.properties.q.type").String(); got != "string" {
		t.Errorf("nested schema altered: tools.0.input_schema.properties.q.type = %q, want \"string\"", got)
	}
	if got := providerUtils.GetJSONField(out, "tools.0.input_schema.required.0").String(); got != "q" {
		t.Errorf("nested schema altered: tools.0.input_schema.required[0] = %q, want \"q\"", got)
	}

	// 2. The beta headers those tools imply are on the context, ready for the request.
	extra, ok := ctx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string)
	if !ok {
		t.Fatal("no extra headers on the context; the beta probe did not run")
	}
	got := extra[AnthropicBetaHeader]
	for _, want := range []string{
		AnthropicStructuredOutputsBetaHeader,   // from tools.0.strict
		AnthropicComputerUseBetaHeader20250124, // from tools.1.type
	} {
		if !slices.Contains(got, want) {
			t.Errorf("beta header %q missing from the outbound request; got %v", want, got)
		}
	}
}

// TestBuildAnthropicResponsesRequestBody_IncludeFields: IncludeFields lands on the final body
// after ExcludeFields on both the raw and typed paths (Bedrock InvokeModel input tagging).
func TestBuildAnthropicResponsesRequestBody_IncludeFields(t *testing.T) {
	config := AnthropicRequestBuildConfig{
		Provider:      schemas.Bedrock,
		Model:         "claude-sonnet-4-5",
		ExcludeFields: []string{"guardrailConfig"},
		IncludeFields: map[string]any{"amazon-bedrock-guardrailConfig": map[string]any{"tagSuffix": "xyz"}},
	}

	t.Run("raw_path", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
		request := &schemas.BifrostResponsesRequest{
			Provider:       schemas.Bedrock,
			Model:          "claude-sonnet-4-5",
			RawRequestBody: []byte(`{"max_tokens":64,"guardrailConfig":{"guardrailIdentifier":"g"},"messages":[{"role":"user","content":"hello"}]}`),
		}
		result, err := BuildAnthropicResponsesRequestBody(ctx, request, config)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := providerUtils.GetJSONField(result, "amazon-bedrock-guardrailConfig.tagSuffix").String(); got != "xyz" {
			t.Errorf("expected tagSuffix xyz, got %q in %s", got, result)
		}
		if providerUtils.JSONFieldExists(result, "guardrailConfig") {
			t.Errorf("expected guardrailConfig to be excluded: %s", result)
		}
	})

	t.Run("typed_path", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
		role := schemas.ResponsesInputMessageRoleUser
		request := &schemas.BifrostResponsesRequest{
			Provider: schemas.Bedrock,
			Model:    "claude-sonnet-4-5",
			Input:    []schemas.ResponsesMessage{{Role: &role, Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hello")}}},
			Params:   &schemas.ResponsesParameters{MaxOutputTokens: schemas.Ptr(64)},
		}
		result, err := BuildAnthropicResponsesRequestBody(ctx, request, config)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := providerUtils.GetJSONField(result, "amazon-bedrock-guardrailConfig.tagSuffix").String(); got != "xyz" {
			t.Errorf("expected tagSuffix xyz, got %q in %s", got, result)
		}
	})

	t.Run("nil_include_fields_is_noop", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
		role := schemas.ResponsesInputMessageRoleUser
		request := &schemas.BifrostResponsesRequest{
			Provider: schemas.Bedrock,
			Model:    "claude-sonnet-4-5",
			Input:    []schemas.ResponsesMessage{{Role: &role, Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hello")}}},
			Params:   &schemas.ResponsesParameters{MaxOutputTokens: schemas.Ptr(64)},
		}
		result, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{Provider: schemas.Bedrock, Model: "claude-sonnet-4-5"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if providerUtils.JSONFieldExists(result, "amazon-bedrock-guardrailConfig") {
			t.Errorf("unexpected amazon-bedrock-guardrailConfig: %s", result)
		}
	})
}

// installMaxOutputRow answers every provider's lookup for model with a datasheet
// row capping output at ceiling, mirroring the live claude-haiku-4-5 rows.
func installMaxOutputRow(t *testing.T, model string, ceiling int) {
	t.Helper()
	providerUtils.SetCapabilityResolver(func(_ schemas.ModelProvider, m string) *schemas.ModelCapabilities {
		if m == model {
			return &schemas.ModelCapabilities{MaxOutputTokens: new(ceiling)}
		}
		return nil
	})
	t.Cleanup(func() { providerUtils.SetCapabilityResolver(nil) })
}

// A routing rule or fallback can retarget a request sized for a 128K model onto a
// smaller one; upstream rejects max_tokens above the target's ceiling with a 400
// ("max_tokens: 128000 > 64000, which is the maximum allowed number of output
// tokens for claude-haiku-4-5-20251001"), so every builder path clamps to the row.
func TestMaxTokensClampedToModelCeiling(t *testing.T) {
	const haiku = "claude-haiku-4-5-20251001"
	installMaxOutputRow(t, haiku, 64000)

	build := func(t *testing.T, ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string, chat bool, maxTokens int) int64 {
		t.Helper()
		body := fmt.Appendf(nil, `{"model":%q,"max_tokens":%d,"messages":[{"role":"user","content":"hi"}]}`, model, maxTokens)
		cfg := AnthropicRequestBuildConfig{Provider: provider, Model: model}
		var out []byte
		var err *schemas.BifrostError
		if chat {
			out, err = BuildAnthropicChatRequestBody(ctx, &schemas.BifrostChatRequest{Provider: provider, Model: model, RawRequestBody: body, Input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: new("hi")}}}, Params: &schemas.ChatParameters{MaxCompletionTokens: new(maxTokens)}}, cfg)
		} else {
			out, err = BuildAnthropicResponsesRequestBody(ctx, &schemas.BifrostResponsesRequest{Provider: provider, Model: model, RawRequestBody: body, Input: makeSimpleInput("hi"), Params: &schemas.ResponsesParameters{MaxOutputTokens: new(maxTokens)}}, cfg)
		}
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		return providerUtils.GetJSONField(out, "max_tokens").Int()
	}

	for _, provider := range []schemas.ModelProvider{schemas.Anthropic, schemas.Vertex} {
		for _, raw := range []bool{false, true} {
			for _, chat := range []bool{false, true} {
				newCtx := func() *schemas.BifrostContext {
					ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
					ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, raw)
					return ctx
				}
				name := fmt.Sprintf("%s/raw=%v/chat=%v", provider, raw, chat)
				t.Run(name+"/above_ceiling_clamped", func(t *testing.T) {
					if got := build(t, newCtx(), provider, haiku, chat, 128000); got != 64000 {
						t.Errorf("max_tokens = %d, want 64000", got)
					}
				})
				t.Run(name+"/below_ceiling_kept", func(t *testing.T) {
					if got := build(t, newCtx(), provider, haiku, chat, 32000); got != 32000 {
						t.Errorf("max_tokens = %d, want 32000", got)
					}
				})
				t.Run(name+"/no_known_ceiling_kept", func(t *testing.T) {
					if got := build(t, newCtx(), provider, "claude-unknown-9", chat, 128000); got != 128000 {
						t.Errorf("max_tokens = %d, want 128000", got)
					}
				})
				// No datasheet row: the static Claude table still knows Haiku 4.5 caps at 64K.
				t.Run(name+"/no_row_uses_claude_table", func(t *testing.T) {
					if got := build(t, newCtx(), provider, "claude-haiku-4-5", chat, 128000); got != 64000 {
						t.Errorf("max_tokens = %d, want 64000", got)
					}
				})
				// output-300k is Message Batches only, and these builders serve synchronous Messages.
				t.Run(name+"/output_300k_beta_still_clamped", func(t *testing.T) {
					ctx := newCtx()
					ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{AnthropicBetaHeader: {"output-300k-2026-03-24"}})
					if got := build(t, ctx, provider, haiku, chat, 128000); got != 64000 {
						t.Errorf("max_tokens = %d, want 64000", got)
					}
				})
			}
		}
	}
}

// The body a Claude Code session sized for an Opus model, forwarded verbatim after
// a routing rule retargeted it to Haiku 4.5. Haiku caps output at 64K and has no
// adaptive thinking ("adaptive thinking is not supported on this model"), so the
// raw path must clamp max_tokens and turn adaptive into a budget the model takes.
func TestClaudeCodeBodyRetargetedToHaiku(t *testing.T) {
	const haiku = "claude-haiku-4-5-20251001"
	installMaxOutputRow(t, haiku, 64000)

	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
	out, err := BuildAnthropicResponsesRequestBody(ctx, &schemas.BifrostResponsesRequest{
		Provider:       schemas.Anthropic,
		Model:          haiku,
		RawRequestBody: []byte(`{"model":"claude-opus-4-6","max_tokens":128000,"thinking":{"type":"adaptive"},"output_config":{"effort":"medium"},"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]},"messages":[{"role":"user","content":"hi"}]}`),
	}, AnthropicRequestBuildConfig{Provider: schemas.Anthropic, IsStreaming: true})
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	if got := providerUtils.GetJSONField(out, "max_tokens").Int(); got != 64000 {
		t.Errorf("max_tokens = %d, want 64000; body: %s", got, out)
	}
	if got := providerUtils.GetJSONField(out, "thinking.type").String(); got != "enabled" {
		t.Errorf("thinking.type = %q, want \"enabled\"; body: %s", got, out)
	}
	// medium effort over the clamped 64000: 1024 + int(0.425 * 62976).
	if got := providerUtils.GetJSONField(out, "thinking.budget_tokens").Int(); got != 27788 {
		t.Errorf("thinking.budget_tokens = %d, want 27788; body: %s", got, out)
	}
	if providerUtils.JSONFieldExists(out, "output_config.effort") {
		t.Errorf("output_config.effort survived on a model without the effort parameter; body: %s", out)
	}
	if got := providerUtils.GetJSONField(out, "context_management.edits.0.type").String(); got != "clear_thinking_20251015" {
		t.Errorf("context_management edit = %q, want clear_thinking_20251015 kept; body: %s", got, out)
	}
}

// Enabled thinking needs budget_tokens < max_tokens. When the clamp lowers
// max_tokens (128000 -> 64000 on Haiku 4.5) below the caller's explicit budget,
// the budget is refit from effort (default "high") like an unfitting adaptive
// budget; a pair the caller sent invalid without any clamp is left as sent.
func TestClampedMaxTokensRefitsThinkingBudget(t *testing.T) {
	const haiku = "claude-haiku-4-5-20251001"
	installMaxOutputRow(t, haiku, 64000)
	const highBudget, lowBudget = 51404, 10470 // 1024 + int(ratio * (64000 - 1024))

	type sent struct{ maxTokens, budget int64 }
	build := func(t *testing.T, raw, chat bool, maxTokens, budget int, effort string) sent {
		t.Helper()
		ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, raw)
		oc := ""
		if effort != "" {
			oc = fmt.Sprintf(`,"output_config":{"effort":%q}`, effort)
		}
		body := fmt.Appendf(nil, `{"model":%q,"max_tokens":%d,"thinking":{"type":"enabled","budget_tokens":%d}%s,"messages":[{"role":"user","content":"hi"}]}`, haiku, maxTokens, budget, oc)
		var eff *string
		if effort != "" {
			eff = new(effort)
		}
		cfg := AnthropicRequestBuildConfig{Provider: schemas.Anthropic}
		var out []byte
		var err *schemas.BifrostError
		if chat {
			out, err = BuildAnthropicChatRequestBody(ctx, &schemas.BifrostChatRequest{Provider: schemas.Anthropic, Model: haiku, RawRequestBody: body,
				Input:  []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: new("hi")}}},
				Params: &schemas.ChatParameters{MaxCompletionTokens: new(maxTokens), Reasoning: &schemas.ChatReasoning{MaxTokens: new(budget), Effort: eff}}}, cfg)
		} else {
			out, err = BuildAnthropicResponsesRequestBody(ctx, &schemas.BifrostResponsesRequest{Provider: schemas.Anthropic, Model: haiku, RawRequestBody: body,
				Input:  makeSimpleInput("hi"),
				Params: &schemas.ResponsesParameters{MaxOutputTokens: new(maxTokens), Reasoning: &schemas.ResponsesParametersReasoning{MaxTokens: new(budget), Effort: eff}}}, cfg)
		}
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		if got := providerUtils.GetJSONField(out, "thinking.type").String(); got != "enabled" {
			t.Fatalf("thinking.type = %q, want enabled; body: %s", got, out)
		}
		return sent{providerUtils.GetJSONField(out, "max_tokens").Int(), providerUtils.GetJSONField(out, "thinking.budget_tokens").Int()}
	}

	for _, raw := range []bool{false, true} {
		for _, chat := range []bool{false, true} {
			name := fmt.Sprintf("raw=%v/chat=%v", raw, chat)
			t.Run(name+"/budget_above_clamped_max_refit_from_default_effort", func(t *testing.T) {
				if got := build(t, raw, chat, 128000, 80000, ""); got != (sent{64000, highBudget}) {
					t.Errorf("sent max_tokens=%d budget=%d, want 64000/%d", got.maxTokens, got.budget, highBudget)
				}
			})
			t.Run(name+"/budget_above_clamped_max_refit_from_effort", func(t *testing.T) {
				if got := build(t, raw, chat, 128000, 80000, "low"); got != (sent{64000, lowBudget}) {
					t.Errorf("sent max_tokens=%d budget=%d, want 64000/%d", got.maxTokens, got.budget, lowBudget)
				}
			})
			t.Run(name+"/budget_still_fitting_clamped_max_kept", func(t *testing.T) {
				if got := build(t, raw, chat, 128000, 30000, ""); got != (sent{64000, 30000}) {
					t.Errorf("sent max_tokens=%d budget=%d, want 64000/30000", got.maxTokens, got.budget)
				}
			})
			t.Run(name+"/unclamped_pair_left_as_sent", func(t *testing.T) {
				if got := build(t, raw, chat, 16000, 20000, ""); got != (sent{16000, 20000}) {
					t.Errorf("sent max_tokens=%d budget=%d, want 16000/20000", got.maxTokens, got.budget)
				}
			})
		}
	}
}

// TestBuildAnthropicRequestBody_DefaultEagerInputStreaming pins the fine-grained
// tool streaming default. Claude Code pointed at a gateway sends custom tools
// without eager_input_streaming; on Vertex and Bedrock that makes Claude emit a
// tool's input one complete JSON value at a time, so a long Write content
// argument arrives as one burst after minutes of silence and the client's idle
// watchdog aborts. The builder must opt such tools in where the upstream needs
// it, derive the beta into the body, keep an explicit false, leave server tools
// alone, and leave Anthropic direct (which streams natively) untouched.
func TestBuildAnthropicRequestBody_DefaultEagerInputStreaming(t *testing.T) {
	const rawTools = `[` +
		`{"name":"Write","description":"Write a file","input_schema":{"type":"object","properties":{"content":{"type":"string"}}}},` +
		`{"type":"custom","name":"Optout","description":"Opted out","input_schema":{"type":"object"},"eager_input_streaming":false},` +
		`{"type":"computer_20250124","name":"computer","display_width_px":1024,"display_height_px":768}` +
		`]`
	typedTools := func(t *testing.T) []schemas.ResponsesTool {
		return []schemas.ResponsesTool{
			responsesToolFromJSON(t, `{"type":"function","name":"Write","description":"Write a file","parameters":{"type":"object","properties":{"content":{"type":"string"}}}}`),
			responsesToolFromJSON(t, `{"type":"function","name":"Optout","description":"Opted out","parameters":{"type":"object"},"eager_input_streaming":false}`),
		}
	}

	cases := []struct {
		name      string
		provider  schemas.ModelProvider
		model     string
		wantEager bool
	}{
		{"vertex claude", schemas.Vertex, "claude-sonnet-4-5", true},
		{"vertex opus 5", schemas.Vertex, "claude-opus-5", true},
		{"bedrock invoke opus 5", schemas.Bedrock, "us.anthropic.claude-opus-5", true},
		{"bedrock invoke sonnet 4.6", schemas.Bedrock, "global.anthropic.claude-sonnet-4-6", true},
		{"bedrock invoke sonnet 4.5 not in catalog", schemas.Bedrock, "us.anthropic.claude-sonnet-4-5-20250929-v1:0", false},
		{"anthropic direct streams natively", schemas.Anthropic, "claude-opus-5", false},
	}

	for _, tc := range cases {
		for _, raw := range []bool{true, false} {
			name := tc.name + "/typed"
			if raw {
				name = tc.name + "/raw"
			}
			t.Run(name, func(t *testing.T) {
				ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
				request := &schemas.BifrostResponsesRequest{
					Provider: tc.provider,
					Model:    tc.model,
					Input:    makeSimpleInput("write the file"),
				}
				if raw {
					ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
					request.RawRequestBody = []byte(`{"model":"` + tc.model + `","max_tokens":1024,"tools":` + rawTools +
						`,"messages":[{"role":"user","content":"write the file"}]}`)
				} else {
					request.Params = &schemas.ResponsesParameters{Tools: typedTools(t)}
				}
				cfgModel := ""
				if tc.provider != schemas.Anthropic {
					cfgModel = tc.model
				}
				body, bErr := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
					Provider:    tc.provider,
					Model:       cfgModel,
					IsStreaming: true,
				})
				if bErr != nil {
					t.Fatalf("building request body: %v", bErr.Error.Message)
				}

				byName := map[string]gjson.Result{}
				for _, tool := range providerUtils.GetJSONField(body, "tools").Array() {
					byName[tool.Get("name").String()] = tool
				}
				write, ok := byName["Write"]
				if !ok {
					t.Fatalf("custom tool Write missing from body: %s", body)
				}
				gotEager := write.Get("eager_input_streaming")
				if tc.wantEager && (!gotEager.Exists() || !gotEager.Bool()) {
					t.Errorf("Write.eager_input_streaming = %s, want true (tool input would be buffered upstream): %s", gotEager.Raw, body)
				}
				if !tc.wantEager && gotEager.Exists() {
					t.Errorf("Write.eager_input_streaming = %s, want absent for %s/%s", gotEager.Raw, tc.provider, tc.model)
				}
				if optout := byName["Optout"].Get("eager_input_streaming"); !optout.Exists() || optout.Bool() {
					t.Errorf("explicit eager_input_streaming:false was not kept: %s", byName["Optout"].Raw)
				}
				if computer, ok := byName["computer"]; ok && computer.Get("eager_input_streaming").Exists() {
					t.Errorf("server tool got eager_input_streaming: %s", computer.Raw)
				}

				if tc.provider == schemas.Anthropic {
					return
				}
				var betas []string
				for _, b := range providerUtils.GetJSONField(body, "anthropic_beta").Array() {
					betas = append(betas, b.String())
				}
				if hasBeta := slices.Contains(betas, AnthropicEagerInputStreamingBetaHeader); hasBeta != tc.wantEager {
					t.Errorf("anthropic_beta = %v, want fine-grained beta present=%v", betas, tc.wantEager)
				}
			})
		}
	}
}

func TestDefaultEagerInputStreaming(t *testing.T) {
	cases := []struct {
		provider schemas.ModelProvider
		model    string
		want     bool
	}{
		{schemas.Vertex, "claude-3-5-haiku@20241022", true},
		{schemas.Vertex, "claude-opus-4-6", true},
		{schemas.Vertex, "gemini-2.5-pro", false},
		{schemas.Bedrock, "us.anthropic.claude-sonnet-4-6", true},
		{schemas.Bedrock, "global.anthropic.claude-sonnet-5", true},
		{schemas.Bedrock, "anthropic.claude-opus-4-7", true},
		{schemas.Bedrock, "anthropic.claude-opus-5-5", true},
		{schemas.Bedrock, "anthropic.claude-fable-5-1", true},
		{schemas.Bedrock, "anthropic.claude-opus-4-6-v1", false},
		{schemas.Bedrock, "anthropic.claude-sonnet-4-5", false},
		{schemas.Bedrock, "amazon.nova-pro-v1:0", false},
		{schemas.Anthropic, "claude-opus-5", false},
		{schemas.Azure, "claude-opus-5", false},
		{schemas.BedrockMantle, "anthropic.claude-opus-5", false},
	}
	for _, tc := range cases {
		if got := DefaultEagerInputStreaming(tc.provider, tc.model); got != tc.want {
			t.Errorf("DefaultEagerInputStreaming(%s, %q) = %v, want %v", tc.provider, tc.model, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Caller request-surface preservation for models that document a thinking mode
// or sampling surface this converter used to rewrite.
// ---------------------------------------------------------------------------

// These tests exercise the REAL request-builder entry point
// (BuildAnthropicResponsesRequestBody) rather than the isolated converters, so
// they observe the JSON that actually reaches a provider: a converter branch can
// fire correctly and still be undone by a later generic strip pass. That is not
// hypothetical here -- stripUnsupportedAnthropicFields and
// StripUnsupportedFieldsFromRawBody both rewrite thinking AFTER the conversion.
//
// Every expectation below is a MEASURED stock or patched output, not a guess:
// the unchanged-model tables were read off the untouched upstream dev tree at
// adea078c4 and are pinned here verbatim, so "byte for byte unchanged" is an
// assertion about exact bytes rather than the absence of one particular value.
//
// Upstream moved under this change while it was in review (PR #7665), and the
// baseline below is the CURRENT one, not the one the first draft measured.
// Three stock behaviours changed and are re-measured here rather than argued
// with:
//
//   - thinking:{"type":"between_tools"} now round-trips through the neutral
//     ResponsesParametersReasoning.Type for a model id that resolves to the
//     Anthropic family, so most Sonnet 5.5 spellings already keep it. A bare
//     "sonnet-5-5" (which resolves to no family) still loses it outright, and
//     a sibling thinking.display is still dropped on every typed spelling --
//     those are the halves this change still fixes.
//   - between_tools is now downgraded to "disabled" on the RAW path too for a
//     model that does not accept it, so the older-model raw column below is
//     "disabled"/"adaptive" rather than the forwarded "between_tools" the
//     first draft measured.
//   - Sonnet 5.5 and Opus 5.5 are now always-on (DefaultCanDisableReasoning is
//     false for them), so thinking:{"type":"disabled"} is OMITTED on the typed
//     path and rewritten to "adaptive" on the raw path instead of being
//     forwarded. Nothing here re-asserts the old forwarding.
//
// TestSonnet55StockVersusPatchedSurface records both columns explicitly, so a
// case that stops being a fix -- or starts being one -- is a red test rather
// than a stale comment.

// surfaceProbe is one request shape put through one path.
type surfaceProbe struct {
	model     string
	thinking  *AnthropicThinking
	effort    *string
	temp      *float64
	topP      *float64
	topK      *int
	raw       bool
	streaming bool
	count     bool
	// dropNeutral simulates a request-normalizing layer running BETWEEN the two
	// conversions and clearing sampling parameters off the neutral request --
	// which is not hypothetical: the built-in compat plugin drops
	// ResponsesParameters.Temperature / .TopP for any model whose catalog row
	// does not list them, and reached the Anthropic egress with nothing left to
	// forward. "named" is exactly what that plugin drops (the two struct
	// fields, never ExtraParams); "all" additionally drops top_k.
	dropNeutral string
	// mutateNeutral is the other thing a layer between the two conversions can
	// do: not clear a parameter but deliberately CHANGE it. A drop is a loss
	// the egress should recover; a change is an instruction the egress must
	// obey, and the two have to stay distinguishable.
	mutateNeutral func(*schemas.ResponsesParameters)
	// compatDropped is what the BUILT-IN compat plugin publishes when it removes
	// a parameter the model catalog does not allowlist. dropNeutral above only
	// clears the value, which is what an anonymous layer looks like; this is the
	// plugin saying "I removed this on purpose", and the two must be answered
	// differently -- recover the first, respect the second.
	compatDropped []string
	// maxTokens overrides the probe's default 1024, so a model ceiling can be
	// made to actually lower it.
	maxTokens int
}

// wire is the projection of the built body this change is responsible for: the
// caller-controlled reasoning and sampling surface. Absent fields render as "-"
// so an omission is asserted as precisely as a value.
func (p surfaceProbe) wire(t *testing.T) string {
	t.Helper()
	body, _ := p.build(t)
	return projectAnthropicWire(body)
}

// build runs the probe and returns both halves of what the builder produces:
// the body bytes AND the context it decorated. Beta headers are derived from
// the body inside the builder, so a body-only assertion cannot see a request
// whose headers disagree with the body it ships with.
func (p surfaceProbe) build(t *testing.T) ([]byte, *schemas.BifrostContext) {
	t.Helper()
	ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)

	maxTokens := 1024
	if p.maxTokens != 0 {
		maxTokens = p.maxTokens
	}
	in := &AnthropicMessageRequest{
		Model:     p.model,
		MaxTokens: maxTokens,
		Messages: []AnthropicMessage{
			{Role: "user", Content: AnthropicContent{ContentStr: schemas.Ptr("hi")}},
		},
		Thinking:    p.thinking,
		Temperature: p.temp,
		TopP:        p.topP,
		TopK:        p.topK,
	}
	if p.effort != nil {
		in.OutputConfig = &AnthropicOutputConfig{Effort: p.effort}
	}

	neutral := in.ToBifrostResponsesRequest(ctx)
	if p.dropNeutral != "" && neutral.Params != nil {
		neutral.Params.Temperature = nil
		neutral.Params.TopP = nil
		if p.dropNeutral == "all" {
			delete(neutral.Params.ExtraParams, "top_k")
		}
	}
	if p.mutateNeutral != nil && neutral.Params != nil {
		p.mutateNeutral(neutral.Params)
	}
	if len(p.compatDropped) > 0 {
		ctx.SetValue(schemas.BifrostContextKeyCompatDroppedParams, p.compatDropped)
	}
	if p.raw {
		// Claude Code's passthrough path: the inbound body is forwarded rather
		// than rebuilt from the neutral parameters.
		body, err := providerUtils.MarshalProviderRequest(in)
		if err != nil {
			t.Fatalf("marshal raw body: %v", err)
		}
		neutral.RawRequestBody = body
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
	}

	body, bErr := BuildAnthropicResponsesRequestBody(ctx, neutral, AnthropicRequestBuildConfig{
		Provider:      schemas.Anthropic,
		IsStreaming:   p.streaming,
		IsCountTokens: p.count,
		ValidateTools: true,
	})
	if bErr != nil {
		t.Fatalf("build request body: %v", bErr)
	}
	return body, ctx
}

// projectAnthropicWire is the projection of a built body this patch is
// responsible for: the caller-controlled reasoning and sampling surface. Absent
// fields render as "-" so an omission is asserted as precisely as a value.
func projectAnthropicWire(body []byte) string {
	field := func(path string) string {
		r := providerUtils.GetJSONField(body, path)
		if !r.Exists() {
			return "-"
		}
		return r.Raw
	}
	return fmt.Sprintf("thinking=%s effort=%s temperature=%s top_p=%s top_k=%s",
		field("thinking"), field("output_config.effort"),
		field("temperature"), field("top_p"), field("top_k"))
}

func (p surfaceProbe) label() string {
	path := "typed"
	if p.raw {
		path = "raw"
	}
	switch {
	case p.streaming:
		path += "/streaming"
	case p.count:
		path += "/count"
	default:
		path += "/unary"
	}
	return p.model + " " + path
}

func assertWire(t *testing.T, p surfaceProbe, want string) {
	t.Helper()
	if got := p.wire(t); got != want {
		t.Errorf("%s:\n got  %s\n want %s", p.label(), got, want)
	}
}

func betweenTools() *AnthropicThinking {
	return &AnthropicThinking{Type: "between_tools"}
}

func enabledWithBudget() *AnthropicThinking {
	return &AnthropicThinking{Type: "enabled", BudgetTokens: schemas.Ptr(4096)}
}

// sonnet55Spellings are the model strings that must all resolve to the same
// preserved surface: the two reviewed client spellings, a provider-qualified
// form, a date-suffixed form and a Bedrock/Vertex form.
var sonnet55Spellings = []string{
	"claude-sonnet-5-5",
	"sonnet-5-5",
	"anthropic/claude-sonnet-5-5",
	"claude-sonnet-5-5-20260928",
	"anthropic.claude-sonnet-5-5-v1:0",
}

// between_tools is a VALUE of thinking.type that Sonnet 5.5 documents. It must
// survive verbatim on every request shape, on BOTH request paths, and for every
// spelling of the model -- including the ones whose id resolves to no model
// family, where the neutral round trip added upstream still drops it (measured
// stock for "sonnet-5-5", typed: thinking absent).
func TestBetweenToolsSurvivesEveryPath(t *testing.T) {
	const want = `thinking={"type":"between_tools"} effort=- temperature=- top_p=- top_k=-`
	for _, model := range sonnet55Spellings {
		t.Run(model, func(t *testing.T) {
			for _, p := range []surfaceProbe{
				{model: model, thinking: betweenTools()},
				{model: model, thinking: betweenTools(), streaming: true},
				{model: model, thinking: betweenTools(), count: true},
				{model: model, thinking: betweenTools(), raw: true},
				{model: model, thinking: betweenTools(), raw: true, streaming: true},
				{model: model, thinking: betweenTools(), raw: true, count: true},
			} {
				assertWire(t, p, want)
			}
		})
	}
}

// The legacy extended-thinking shape was rewritten to adaptive with the budget
// discarded and an effort synthesized from that discarded budget -- three
// substitutions in one request. For the one model whose surface this build does
// not claim to know, the caller's own object is forwarded and the provider
// answers. Measured stock for comparison (claude-sonnet-5-5): typed
// thinking={"type":"adaptive","display":"summarized"} effort="high"; raw
// thinking={"type":"adaptive"} -- the budget is discarded on both paths.
func TestEnabledBudgetThinkingIsForwardedVerbatim(t *testing.T) {
	const want = `thinking={"type":"enabled","budget_tokens":4096} effort=- temperature=- top_p=- top_k=-`
	for _, model := range sonnet55Spellings {
		t.Run(model, func(t *testing.T) {
			for _, p := range []surfaceProbe{
				{model: model, thinking: enabledWithBudget()},
				{model: model, thinking: enabledWithBudget(), streaming: true},
				{model: model, thinking: enabledWithBudget(), count: true},
				{model: model, thinking: enabledWithBudget(), raw: true},
				{model: model, thinking: enabledWithBudget(), raw: true, streaming: true},
				{model: model, thinking: enabledWithBudget(), raw: true, count: true},
			} {
				assertWire(t, p, want)
			}
		})
	}
}

// temperature / top_p / top_k were dropped on the typed path and forwarded on
// the raw path, so the same body got a silent 200 from one client and the
// provider's own 400 from another. Both paths now forward the caller's values,
// and the assertion is the FULL tuple on every request shape: the earlier
// version of this test accepted top_p disappearing on the typed path whenever
// temperature was also present, which is the divergence, not a rule to keep.
// Upstream's "prefer temperature over top_p" rule is left exactly as it is for
// every other model (see TestOtherModelsAreByteUnchanged); it does not apply
// here because for this model the conversion forwards neither, and the
// raw/passthrough path -- the standing control below -- sends both.
//
// Measured stock (claude-sonnet-5-5, typed): all three absent on every shape.
func TestSamplingParametersReachBothPaths(t *testing.T) {
	temp, topP, topK := 0.7, 0.9, 40
	all := func(raw, streaming, count bool) surfaceProbe {
		return surfaceProbe{model: "claude-sonnet-5-5", temp: &temp, topP: &topP, topK: &topK,
			raw: raw, streaming: streaming, count: count}
	}
	for _, model := range sonnet55Spellings {
		t.Run(model, func(t *testing.T) {
			// The whole tuple, both paths, unary and streaming.
			for _, raw := range []bool{false, true} {
				for _, streaming := range []bool{false, true} {
					p := all(raw, streaming, false)
					p.model = model
					assertWire(t, p, `thinking=- effort=- temperature=0.7 top_p=0.9 top_k=40`)
				}
			}
			// count_tokens: upstream deletes temperature from the built body on
			// BOTH paths for every model, because the endpoint does not take it.
			// That is not this patch's loss, so it stays -- and it stays
			// identically on both paths.
			for _, raw := range []bool{false, true} {
				p := all(raw, false, true)
				p.model = model
				assertWire(t, p, `thinking=- effort=- temperature=- top_p=0.9 top_k=40`)
			}
			// Each parameter alone also survives on both paths, so "the tuple
			// arrives" is not satisfied by one field standing in for the others.
			for _, raw := range []bool{false, true} {
				assertWire(t, surfaceProbe{model: model, temp: &temp, raw: raw},
					`thinking=- effort=- temperature=0.7 top_p=- top_k=-`)
				assertWire(t, surfaceProbe{model: model, topP: &topP, raw: raw},
					`thinking=- effort=- temperature=- top_p=0.9 top_k=-`)
				assertWire(t, surfaceProbe{model: model, topK: &topK, raw: raw},
					`thinking=- effort=- temperature=- top_p=- top_k=40`)
			}
		})
	}
}

// The unit-level converter test above was green while the BUILT gateway still
// lost temperature on the wire, because a request-normalizing layer between the
// two conversions had already cleared it off the neutral parameters: reading
// them back at egress cannot recover a value that is no longer there. The
// caller's own scalars are therefore recorded at ingress, and this is the
// regression that pins the loss point rather than the symptom.
func TestNativeSamplingSurvivesIntermediateNormalization(t *testing.T) {
	temp, topP, topK := 0.7, 0.9, 40
	for _, drop := range []string{"named", "all"} {
		t.Run(drop, func(t *testing.T) {
			for _, streaming := range []bool{false, true} {
				assertWire(t, surfaceProbe{
					model: "claude-sonnet-5-5", temp: &temp, topP: &topP, topK: &topK,
					streaming: streaming, dropNeutral: drop,
				}, `thinking=- effort=- temperature=0.7 top_p=0.9 top_k=40`)
			}
			assertWire(t, surfaceProbe{
				model: "claude-sonnet-5-5", temp: &temp, topP: &topP, topK: &topK,
				count: true, dropNeutral: drop,
			}, `thinking=- effort=- temperature=- top_p=0.9 top_k=40`)
			// An older model keeps upstream's answer to the same normalization:
			// what the layer dropped stays dropped, and top_k (which that layer
			// never touches) still resolves exactly as upstream resolves it.
			assertWire(t, surfaceProbe{
				model: "claude-sonnet-4-6", temp: &temp, topP: &topP, topK: &topK,
				dropNeutral: drop,
			}, `thinking=- effort=- temperature=- top_p=- top_k=`+map[string]string{"named": "40", "all": "-"}[drop])
		})
	}
}

// A layer between the two conversions can DROP a parameter (the case above) or
// deliberately CHANGE it. The two are different instructions and the egress has
// to tell them apart: recovering a dropped value is the point of the witness,
// but overwriting a value somebody deliberately set answers a request nobody
// made -- and silently, since the caller's own value is a plausible one.
//
// The neutral request at egress is what settles it. A parameter still carrying
// a value there is current by definition, whoever put it there; only an ABSENT
// one is a loss for the witness to recover.
func TestExplicitNeutralSamplingChangesWin(t *testing.T) {
	temp, topP, topK := 0.7, 0.9, 40
	caller := func(mutate func(*schemas.ResponsesParameters)) surfaceProbe {
		return surfaceProbe{
			model: "claude-sonnet-5-5", temp: &temp, topP: &topP, topK: &topK,
			mutateNeutral: mutate,
		}
	}
	cases := []struct {
		name   string
		mutate func(*schemas.ResponsesParameters)
		want   string
	}{
		{
			"a changed temperature is sent, not the caller's",
			func(p *schemas.ResponsesParameters) { p.Temperature = schemas.Ptr(0.2) },
			`thinking=- effort=- temperature=0.2 top_p=0.9 top_k=40`,
		},
		{
			"a changed top_p is sent, not the caller's",
			func(p *schemas.ResponsesParameters) { p.TopP = schemas.Ptr(0.5) },
			`thinking=- effort=- temperature=0.7 top_p=0.5 top_k=40`,
		},
		{
			"a changed top_k is sent, not the caller's",
			func(p *schemas.ResponsesParameters) { p.ExtraParams["top_k"] = 10 },
			`thinking=- effort=- temperature=0.7 top_p=0.9 top_k=10`,
		},
		{
			// The mixed case is the one a per-field rule has to get right: one
			// scalar changed, one cleared, one untouched, in a single pass.
			"one changed, one cleared and one untouched resolve independently",
			func(p *schemas.ResponsesParameters) {
				p.Temperature = schemas.Ptr(0.2)
				p.TopP = nil
			},
			`thinking=- effort=- temperature=0.2 top_p=0.9 top_k=40`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertWire(t, caller(c.mutate), c.want)
		})
	}
}

// The same rule for the reasoning half. The witness restores thinking because
// the neutral shape cannot carry a budget beside the caller's own thinking.type
// -- but a layer that deliberately rewrote the reasoning configuration has
// stated an intent that outranks the recovery, so the restore stands down and
// the neutral request is built as it stands.
//
// What the egress compares against is the projection the INGRESS conversion
// produced, recorded beside the witness. That is the only way to tell "nobody
// touched this" from "somebody set it to something", because the projection of
// a preserved thinking object is lossy by construction: Reasoning already
// differs from the caller's own thinking before any plugin runs.
func TestExplicitNeutralReasoningChangesWin(t *testing.T) {
	cases := []struct {
		name string
		p    surfaceProbe
		want string
	}{
		{
			// Stock for a neutral Reasoning{Effort:"low"} on this model, i.e.
			// exactly what the request now says rather than what it once said.
			"a rewritten reasoning configuration is built as it stands",
			surfaceProbe{model: "claude-sonnet-5-5", thinking: enabledWithBudget(),
				mutateNeutral: func(p *schemas.ResponsesParameters) {
					p.Reasoning = &schemas.ResponsesParametersReasoning{Effort: schemas.Ptr("low")}
				}},
			`thinking={"type":"adaptive","display":"summarized"} effort="low" temperature=- top_p=- top_k=-`,
		},
		{
			// An effort ADDED downstream must survive. The restore clears an
			// effort it considers synthesized -- one this converter derived
			// rather than the caller sending it -- and that clearing must not
			// reach an effort a later layer deliberately supplied. between_tools
			// is the shape where the caller's projection genuinely carries no
			// effort, so the addition is unambiguous.
			"an effort added downstream is not deleted as synthesized",
			surfaceProbe{model: "claude-sonnet-5-5", thinking: betweenTools(),
				mutateNeutral: func(p *schemas.ResponsesParameters) {
					p.Reasoning.Effort = schemas.Ptr("low")
				}},
			`thinking={"type":"between_tools"} effort="low" temperature=- top_p=- top_k=-`,
		},
		{
			// Reasoning cleared outright is a drop, not a change: the caller's
			// own thinking is still what this request asks for.
			"reasoning cleared downstream is still recovered",
			surfaceProbe{model: "claude-sonnet-5-5", thinking: enabledWithBudget(),
				mutateNeutral: func(p *schemas.ResponsesParameters) { p.Reasoning = nil }},
			`thinking={"type":"enabled","budget_tokens":4096} effort=- temperature=- top_p=- top_k=-`,
		},
		{
			// between_tools reaches the neutral shape intact, so a rewrite of it
			// is just as visible and just as binding.
			"a rewritten between_tools configuration is built as it stands",
			surfaceProbe{model: "claude-sonnet-5-5", thinking: betweenTools(),
				mutateNeutral: func(p *schemas.ResponsesParameters) {
					p.Reasoning = &schemas.ResponsesParametersReasoning{Effort: schemas.Ptr("none")}
				}},
			`thinking=- effort=- temperature=- top_p=- top_k=-`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertWire(t, c.p, c.want)
		})
	}
}

// Effort is independent of thinking. An effort the caller never sent must not be
// synthesized from a budget that is now being forwarded; an effort the caller DID
// send must reach the provider unchanged beside the preserved thinking.
func TestEffortIsNeitherSynthesizedNorLost(t *testing.T) {
	low := "low"
	cases := []struct {
		name string
		p    surfaceProbe
		want string
	}{
		{
			"between_tools without effort keeps effort absent",
			surfaceProbe{model: "claude-sonnet-5-5", thinking: betweenTools()},
			`thinking={"type":"between_tools"} effort=- temperature=- top_p=- top_k=-`,
		},
		{
			"between_tools with explicit effort keeps both",
			surfaceProbe{model: "claude-sonnet-5-5", thinking: betweenTools(), effort: &low},
			`thinking={"type":"between_tools"} effort="low" temperature=- top_p=- top_k=-`,
		},
		{
			"enabled+budget without effort keeps effort absent",
			surfaceProbe{model: "claude-sonnet-5-5", thinking: enabledWithBudget()},
			`thinking={"type":"enabled","budget_tokens":4096} effort=- temperature=- top_p=- top_k=-`,
		},
		{
			"enabled+budget with explicit effort keeps both",
			surfaceProbe{model: "claude-sonnet-5-5", thinking: enabledWithBudget(), effort: &low},
			`thinking={"type":"enabled","budget_tokens":4096} effort="low" temperature=- top_p=- top_k=-`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { assertWire(t, c.p, c.want) })
	}
}

// projectAnthropicBetas renders the anthropic-beta tokens the builder derived
// for this request. Absent renders as "-" so "no header" is asserted as
// precisely as a value.
func projectAnthropicBetas(t *testing.T, ctx *schemas.BifrostContext) string {
	t.Helper()
	raw := ctx.Value(schemas.BifrostContextKeyExtraHeaders)
	if raw == nil {
		return "-"
	}
	headers, ok := raw.(map[string][]string)
	if !ok {
		t.Fatalf("extra headers are %T, not map[string][]string", raw)
	}
	tokens := append([]string(nil), headers[AnthropicBetaHeader]...)
	if len(tokens) == 0 {
		return "-"
	}
	slices.Sort(tokens)
	return strings.Join(tokens, ",")
}

// Beta headers are DERIVED FROM THE BODY, so the body a request ships with and
// the headers that describe it have to be decided in that order. The restore
// runs after a strip pass that rewrites thinking, so if it also ran after beta
// derivation the typed path would ship an "enabled" body described by headers
// computed from the "adaptive" body it no longer sends -- and would disagree
// with the raw path, which derives its headers from the already-exempt body.
// AddMissingBetaHeadersToContext adds interleaved-thinking-2025-05-14 for
// thinking.type "enabled"; that token is the observable for the ordering.
//
// This is a request-consistency assertion, not a claim that the provider
// requires the header for this model: whether Sonnet 5.5 accepts enabled
// thinking at all is a live fact this change deliberately does not predict.
// What is asserted is that both paths describe the same request the same way.
func TestPreservedThinkingDerivesBetaHeadersFromTheRestoredBody(t *testing.T) {
	const (
		wantBody = `thinking={"type":"enabled","budget_tokens":4096} effort=- temperature=- top_p=- top_k=-`
		wantBeta = AnthropicInterleavedThinkingBetaHeader
	)
	for _, raw := range []bool{false, true} {
		p := surfaceProbe{model: "claude-sonnet-5-5", thinking: enabledWithBudget(), raw: raw}
		t.Run(p.label(), func(t *testing.T) {
			body, ctx := p.build(t)
			if got := projectAnthropicWire(body); got != wantBody {
				t.Errorf("body:\n got  %s\n want %s", got, wantBody)
			}
			if got := projectAnthropicBetas(t, ctx); got != wantBeta {
				t.Errorf("anthropic-beta:\n got  %s\n want %s", got, wantBeta)
			}
		})
	}
}

// The ordering fix must not start inventing headers for the modes it preserves
// but that do NOT ask for interleaved thinking, nor for the models it does not
// touch. between_tools is not "enabled", so it derives no interleaved token;
// stock's own adaptive rebuild for another model derives none either. Both are
// measured existing behaviour and stay that way.
func TestBetaDerivationIsNotWidenedByTheRestore(t *testing.T) {
	cases := []struct {
		name string
		p    surfaceProbe
	}{
		{"between_tools/typed", surfaceProbe{model: "claude-sonnet-5-5", thinking: betweenTools()}},
		{"between_tools/raw", surfaceProbe{model: "claude-sonnet-5-5", thinking: betweenTools(), raw: true}},
		{"sampling only/typed", surfaceProbe{model: "claude-sonnet-5-5", temp: schemas.Ptr(0.5)}},
		{"other model enabled/typed", surfaceProbe{model: "claude-sonnet-5", thinking: enabledWithBudget()}},
		{"other model enabled/raw", surfaceProbe{model: "claude-sonnet-5", thinking: enabledWithBudget(), raw: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, ctx := c.p.build(t)
			if got := projectAnthropicBetas(t, ctx); got != "-" {
				t.Errorf("%s derived anthropic-beta %s, want none", c.p.label(), got)
			}
		})
	}
}

// A sibling display is part of the caller's configuration and is carried with it,
// rather than being re-synthesized from the neutral parameters. The neutral
// ResponsesParametersReasoning that now carries thinking.type has no field for
// display, so this is the half of between_tools the neutral round trip still
// cannot express: measured stock drops it on the typed path for EVERY spelling
// (thinking={"type":"between_tools"}) while the raw path forwards it, and the
// two paths answering the same body differently is the divergence being closed.
func TestPreservedThinkingKeepsDisplayVerbatim(t *testing.T) {
	const want = `thinking={"type":"between_tools","display":"omitted"} effort=- temperature=- top_p=- top_k=-`
	withDisplay := func() *AnthropicThinking {
		return &AnthropicThinking{Type: "between_tools", Display: schemas.Ptr("omitted")}
	}
	for _, model := range sonnet55Spellings {
		t.Run(model, func(t *testing.T) {
			for _, raw := range []bool{false, true} {
				for _, mode := range []string{"unary", "streaming", "count"} {
					assertWire(t, surfaceProbe{
						model: model, thinking: withDisplay(), raw: raw,
						streaming: mode == "streaming", count: mode == "count",
					}, want)
				}
			}
		})
	}
}

// The closed set is closed. An unknown or mistyped thinking.type is NOT carried
// and keeps resolving exactly as stock does, so this is not an arbitrary
// passthrough that would forward a caller's typo to the provider.
//
// Measured stock, unchanged by this patch: the typed path folds an unrecognised
// type into reasoning-off, and Sonnet 5.5 is always-on, so the parameter is
// OMITTED rather than sent as "disabled". The raw path forwards the typo
// untouched, which is also stock and also left alone -- asserted here so "not
// carried" means the typed path resolves it exactly as before, not that the
// patch started sanitizing the raw body.
func TestUnrecognisedThinkingTypeIsNotCarried(t *testing.T) {
	for _, bad := range []string{"between-tools", "betweentools", "BETWEEN_TOOLS", "enabled_extended", ""} {
		t.Run(bad, func(t *testing.T) {
			assertWire(t, surfaceProbe{
				model:    "claude-sonnet-5-5",
				thinking: &AnthropicThinking{Type: bad},
			}, `thinking=- effort=- temperature=- top_p=- top_k=-`)
			assertWire(t, surfaceProbe{
				model:    "claude-sonnet-5-5",
				thinking: &AnthropicThinking{Type: bad},
				raw:      true,
			}, fmt.Sprintf(`thinking={"type":%q} effort=- temperature=- top_p=- top_k=-`, bad))
		})
	}
}

// adaptive and disabled are NOT in the preserved set, so they keep upstream's
// behaviour exactly -- including the two asymmetries this patch deliberately
// does NOT touch: the typed path adds the family's documented default
// display:"summarized" (and, for adaptive, effort "high") where the raw path
// forwards the bare object, and a caller-sent thinking:{"type":"disabled"} is
// omitted on the typed path but rewritten to "adaptive" on the raw one,
// because Sonnet 5.5 is always-on and rejects "disabled". All four are
// measured stock and pinned so a future change to them is a red test rather
// than a production discovery.
func TestAdaptiveAndDisabledKeepUpstreamBehaviour(t *testing.T) {
	cases := []struct {
		name string
		p    surfaceProbe
		want string
	}{
		{"adaptive typed", surfaceProbe{model: "claude-sonnet-5-5", thinking: &AnthropicThinking{Type: "adaptive"}},
			`thinking={"type":"adaptive","display":"summarized"} effort="high" temperature=- top_p=- top_k=-`},
		{"adaptive raw", surfaceProbe{model: "claude-sonnet-5-5", thinking: &AnthropicThinking{Type: "adaptive"}, raw: true},
			`thinking={"type":"adaptive"} effort=- temperature=- top_p=- top_k=-`},
		{"disabled typed", surfaceProbe{model: "claude-sonnet-5-5", thinking: &AnthropicThinking{Type: "disabled"}},
			`thinking=- effort=- temperature=- top_p=- top_k=-`},
		{"disabled raw", surfaceProbe{model: "claude-sonnet-5-5", thinking: &AnthropicThinking{Type: "disabled"}, raw: true},
			`thinking={"type":"adaptive"} effort=- temperature=- top_p=- top_k=-`},
		{"no thinking typed", surfaceProbe{model: "claude-sonnet-5-5"},
			`thinking=- effort=- temperature=- top_p=- top_k=-`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { assertWire(t, c.p, c.want) })
	}
}

// unchangedModel pins the measured stock output of every model this patch must
// not touch. These strings were read off the untouched upstream dev tree at
// adea078c4 -- the CURRENT baseline, not the one the first draft measured. The
// raw between_tools column moved with PR #7665, which downgrades it in the raw
// body too: to "disabled" where the model accepts that, and on from there to
// "adaptive" on the always-on Fable/Mythos family. Both are stock and both are
// pinned as such.
type unchangedModel struct {
	model string
	// each pair is {typed, raw}
	betweenTools  [2]string
	enabledBudget [2]string
	sampling      [2]string
	adaptive      [2]string
	disabled      [2]string
	countBetween  string
}

const (
	wDisabled  = `thinking={"type":"disabled"} effort=- temperature=- top_p=- top_k=-`
	wAbsent    = `thinking=- effort=- temperature=- top_p=- top_k=-`
	wEnabled   = `thinking={"type":"enabled","budget_tokens":4096} effort=- temperature=- top_p=- top_k=-`
	wAdaptBare = `thinking={"type":"adaptive"} effort=- temperature=- top_p=- top_k=-`
	wAdaptLow  = `thinking={"type":"adaptive","display":"summarized"} effort="low" temperature=- top_p=- top_k=-`
	wAdaptHigh = `thinking={"type":"adaptive","display":"summarized"} effort="high" temperature=- top_p=- top_k=-`
	wSampleAll = `thinking=- effort=- temperature=0.7 top_p=0.9 top_k=40`
	wSampleTyp = `thinking=- effort=- temperature=0.7 top_p=- top_k=40`
)

// Every model that does not document the preserved surface keeps the pre-patch
// bytes EXACTLY: an assertion that the value is unchanged, not merely that it is
// not the new one.
func TestOtherModelsAreByteUnchanged(t *testing.T) {
	table := []unchangedModel{
		{
			model:         "claude-sonnet-5",
			betweenTools:  [2]string{wDisabled, wDisabled},
			enabledBudget: [2]string{wAdaptLow, wAdaptBare},
			sampling:      [2]string{wAbsent, wSampleAll},
			adaptive:      [2]string{wAdaptHigh, wAdaptBare},
			disabled:      [2]string{wDisabled, wDisabled},
			countBetween:  wDisabled,
		},
		{
			model:         "claude-sonnet-4-6",
			betweenTools:  [2]string{wDisabled, wDisabled},
			enabledBudget: [2]string{wEnabled, wEnabled},
			sampling:      [2]string{wSampleTyp, wSampleAll},
			adaptive:      [2]string{`thinking={"type":"adaptive"} effort="high" temperature=- top_p=- top_k=-`, wAdaptBare},
			disabled:      [2]string{wDisabled, wDisabled},
			countBetween:  wDisabled,
		},
		{
			model:         "claude-opus-4-6",
			betweenTools:  [2]string{wDisabled, wDisabled},
			enabledBudget: [2]string{wEnabled, wEnabled},
			sampling:      [2]string{wSampleTyp, wSampleAll},
			adaptive:      [2]string{`thinking={"type":"adaptive"} effort="high" temperature=- top_p=- top_k=-`, wAdaptBare},
			disabled:      [2]string{wDisabled, wDisabled},
			countBetween:  wDisabled,
		},
		{
			model:         "claude-opus-5",
			betweenTools:  [2]string{wDisabled, wDisabled},
			enabledBudget: [2]string{wAdaptLow, wAdaptBare},
			sampling:      [2]string{wAbsent, wSampleAll},
			adaptive:      [2]string{wAdaptHigh, wAdaptBare},
			disabled:      [2]string{wDisabled, wDisabled},
			countBetween:  wDisabled,
		},
		{
			// Fable/Mythos cannot disable reasoning, so the folded-to-"none"
			// effort leaves thinking ABSENT rather than disabled.
			model:         "claude-fable-5",
			betweenTools:  [2]string{wAbsent, wAdaptBare},
			enabledBudget: [2]string{wAdaptLow, wAdaptBare},
			sampling:      [2]string{wAbsent, wSampleAll},
			adaptive:      [2]string{wAdaptHigh, wAdaptBare},
			disabled:      [2]string{wAbsent, wAdaptBare},
			countBetween:  wAbsent,
		},
		{
			// Mythos Preview is the one adaptive-only model that keeps "enabled"
			// on the raw path, so it proves the raw-path guard did not widen.
			model:         "claude-mythos-preview",
			betweenTools:  [2]string{wAbsent, wAdaptBare},
			enabledBudget: [2]string{wAdaptLow, wEnabled},
			sampling:      [2]string{wAbsent, wSampleAll},
			adaptive:      [2]string{wAdaptHigh, wAdaptBare},
			disabled:      [2]string{wAbsent, wAdaptBare},
			countBetween:  wAbsent,
		},
		{
			// Pre-adaptive generation: budget-token thinking is the only mode,
			// and an "adaptive" ask is converted DOWN to a budget. Since
			// upstream #7732 the raw path rewrites adaptive the same way, and
			// drops thinking outright when max_tokens (1024 here) leaves no
			// room for a budget -- so the raw column is the absent wire, not a
			// verbatim passthrough. Both columns are stock behaviour: this
			// change never touches a model outside the Sonnet 5.5 spellings.
			model:         "claude-3-5-sonnet",
			betweenTools:  [2]string{wDisabled, wDisabled},
			enabledBudget: [2]string{wEnabled, wEnabled},
			sampling:      [2]string{wSampleTyp, wSampleAll},
			adaptive:      [2]string{`thinking={"type":"enabled","budget_tokens":1024} effort=- temperature=- top_p=- top_k=-`, wAbsent},
			disabled:      [2]string{wDisabled, wDisabled},
			countBetween:  wDisabled,
		},
	}

	temp, topP, topK := 0.7, 0.9, 40
	for _, row := range table {
		t.Run(row.model, func(t *testing.T) {
			m := row.model
			assertWire(t, surfaceProbe{model: m, thinking: betweenTools()}, row.betweenTools[0])
			assertWire(t, surfaceProbe{model: m, thinking: betweenTools(), raw: true}, row.betweenTools[1])
			assertWire(t, surfaceProbe{model: m, thinking: betweenTools(), count: true}, row.countBetween)
			assertWire(t, surfaceProbe{model: m, thinking: enabledWithBudget()}, row.enabledBudget[0])
			assertWire(t, surfaceProbe{model: m, thinking: enabledWithBudget(), raw: true}, row.enabledBudget[1])
			assertWire(t, surfaceProbe{model: m, temp: &temp, topP: &topP, topK: &topK}, row.sampling[0])
			assertWire(t, surfaceProbe{model: m, temp: &temp, topP: &topP, topK: &topK, raw: true}, row.sampling[1])
			assertWire(t, surfaceProbe{model: m, thinking: &AnthropicThinking{Type: "adaptive"}}, row.adaptive[0])
			assertWire(t, surfaceProbe{model: m, thinking: &AnthropicThinking{Type: "adaptive"}, raw: true}, row.adaptive[1])
			assertWire(t, surfaceProbe{model: m, thinking: &AnthropicThinking{Type: "disabled"}}, row.disabled[0])
			assertWire(t, surfaceProbe{model: m, thinking: &AnthropicThinking{Type: "disabled"}, raw: true}, row.disabled[1])
		})
	}
}

// The two paths must agree about the preserved forms: the whole reason the typed
// path was wrong is that a raw-path client and an ordinary client got different
// requests out of the same body.
func TestTypedAndRawAgreeOnPreservedForms(t *testing.T) {
	for _, model := range sonnet55Spellings {
		for _, th := range []*AnthropicThinking{betweenTools(), enabledWithBudget()} {
			name := model + "/" + th.Type
			t.Run(name, func(t *testing.T) {
				typed := surfaceProbe{model: model, thinking: th}.wire(t)
				raw := surfaceProbe{model: model, thinking: th, raw: true}.wire(t)
				if typed != raw {
					t.Errorf("typed and raw disagree for %s:\n typed %s\n raw   %s", name, typed, raw)
				}
			})
		}
	}
}

// A request that never came in as an Anthropic Messages body carries no witness,
// so it keeps the existing behaviour: an OpenAI-shaped inbound dialect cannot
// acquire a preserved thinking mode or a preserved budget by accident. Driven
// through the real builder, because that is where the restore lives.
//
// Both probes are measured stock on the SAME model the restore applies to, and
// the budget probe is the one that can actually see a leak: if the restore ran
// witness-free it would ship thinking:{"type":"enabled","budget_tokens":4096}
// where upstream ships the adaptive rewrite.
func TestNoWitnessMeansNoRestore(t *testing.T) {
	cases := []struct {
		name   string
		params *schemas.ResponsesParameters
		want   string
	}{
		{
			// Sonnet 5.5 is always-on, so a neutral reasoning-off ask leaves the
			// parameter absent rather than sending thinking:{"type":"disabled"}.
			"reasoning off",
			&schemas.ResponsesParameters{
				Reasoning: &schemas.ResponsesParametersReasoning{Effort: schemas.Ptr("none")},
			},
			`thinking=- effort=- temperature=- top_p=- top_k=-`,
		},
		{
			"reasoning budget plus every sampling scalar",
			&schemas.ResponsesParameters{
				Reasoning:   &schemas.ResponsesParametersReasoning{MaxTokens: schemas.Ptr(4096)},
				Temperature: schemas.Ptr(0.7),
				TopP:        schemas.Ptr(0.9),
				ExtraParams: map[string]interface{}{"top_k": 40},
			},
			`thinking={"type":"adaptive","display":"summarized"} effort=- temperature=- top_p=- top_k=-`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := neutralProbe{model: "claude-sonnet-5-5", params: c.params}.wire(t)
			if got != c.want {
				t.Errorf("witness-free neutral request:\n got  %s\n want %s", got, c.want)
			}
		})
	}
}

// The witness is per-request context state, so one caller's preserved mode and
// sampling scalars can never be applied to another caller's request. Each probe
// builds its own context, so a leak would show up as the second request
// acquiring the first one's surface -- a restored thinking object where
// upstream omits the parameter, or a temperature the second caller never sent.
func TestWitnessDoesNotLeakAcrossRequests(t *testing.T) {
	temp := 0.7
	assertWire(t, surfaceProbe{model: "claude-sonnet-5-5", thinking: enabledWithBudget(), temp: &temp},
		`thinking={"type":"enabled","budget_tokens":4096} effort=- temperature=0.7 top_p=- top_k=-`)
	// Measured stock for this second shape, and the shape a leak would corrupt:
	// Sonnet 5.5 is always-on, so an explicit "disabled" leaves thinking absent,
	// and this caller sent no sampling scalars at all.
	assertWire(t, surfaceProbe{model: "claude-sonnet-5-5", thinking: &AnthropicThinking{Type: "disabled"}},
		`thinking=- effort=- temperature=- top_p=- top_k=-`)
}

// The witness rides on the context, and BifrostContext.Value reads THROUGH to
// its parent -- so the per-request isolation above only holds for independent
// contexts. A plugin issuing its own sub-request derives a context from the
// caller's, and would inherit this caller's thinking mode and sampling scalars
// onto a request that never carried them: external parameters steering a
// plugin-owned call.
//
// Bifrost's documented boundary for that is ClearContextForInternalRequest,
// which sheds every piece of caller-request state a derived context must not
// keep. The witness is registered there (asserted in
// core/utils_test.go:TestPrepareContextForInternalRequestShedsAnthropicNativeSurface,
// which cannot live here -- core imports this package, so the dependency only
// runs that way). This is the provider half: once the witness is shed, the
// egress restores nothing and the sub-request is answered as the stock,
// witness-free request it is.
func TestShedWitnessRestoresNothing(t *testing.T) {
	caller := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
	callerReq := &AnthropicMessageRequest{
		Model:     "claude-sonnet-5-5",
		MaxTokens: 1024,
		Messages:  []AnthropicMessage{{Role: "user", Content: AnthropicContent{ContentStr: schemas.Ptr("hi")}}},
		Thinking:  enabledWithBudget(),
		// The scalars a leak would apply to somebody else's request.
		Temperature: schemas.Ptr(0.7),
		TopP:        schemas.Ptr(0.9),
		TopK:        schemas.Ptr(40),
	}
	callerReq.ToBifrostResponsesRequest(caller)

	internal := schemas.NewBifrostContext(caller, time.Now())
	if _, inherited := anthropicNativeRequestSurfaceFrom(internal); !inherited {
		t.Fatal("a derived context must read the caller's witness through to the parent; " +
			"if it no longer does, this test has stopped covering the leak it exists for")
	}
	// The shed itself is core's: ClearContextForInternalRequest parks an
	// explicit nil on this key, on a derived context with nothing else written
	// to it. That is asserted on the core side
	// (TestClearContextForInternalRequestShedsAnthropicNativeSurfaceOnFreshChild),
	// which is the only side that can call the helper. What this half pins is
	// the provider-side contract that makes the shed effective: a nil parked on
	// the key is no witness at all, so the egress restores nothing.
	internal.SetValue(schemas.BifrostContextKeyAnthropicNativeRequestSurface, nil)

	// A fresh Sonnet 5.5 Responses request the plugin owns: no thinking, no
	// sampling. Stock (and therefore correct) is that none of it appears.
	body, bErr := BuildAnthropicResponsesRequestBody(internal, &schemas.BifrostResponsesRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-sonnet-5-5",
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")},
		}},
	}, AnthropicRequestBuildConfig{Provider: schemas.Anthropic, ValidateTools: true})
	if bErr != nil {
		t.Fatalf("build internal request body: %v", bErr)
	}
	const want = `thinking=- effort=- temperature=- top_p=- top_k=-`
	if got := projectAnthropicWire(body); got != want {
		t.Errorf("internal sub-request inherited the caller's surface:\n got  %s\n want %s", got, want)
	}

	// Shedding it on the derived context must not disarm the caller's own
	// request, which is still in flight on the parent.
	if _, ok := anthropicNativeRequestSurfaceFrom(caller); !ok {
		t.Error("the caller's own witness must survive an internal sub-request")
	}
}

// The model predicate is the narrowness guarantee, so it is asserted directly
// as well as through the wire tables above. PreservesCallerRequestSurface is
// the one predicate this change adds; it must agree exactly with upstream's
// existing Sonnet 5.5 predicates rather than introduce a second, drifting
// spelling of the same model family.
func TestSonnet55PredicateNarrowness(t *testing.T) {
	for _, model := range append(append([]string(nil), sonnet55Spellings...), "claude-sonnet-5.5") {
		if !PreservesCallerRequestSurface(model) {
			t.Errorf("%q must preserve the caller request surface", model)
		}
		if !IsSonnet55Plus(model) || !DefaultSupportsBetweenToolsThinking(model) {
			t.Errorf("%q must match upstream's Sonnet 5.5 predicates", model)
		}
	}
	for _, model := range []string{
		"claude-sonnet-5", "claude-sonnet-5-20260101", "claude-sonnet-4-5",
		"claude-sonnet-4-6", "claude-opus-5", "claude-opus-5-5", "claude-opus-4-6",
		"claude-fable-5", "claude-mythos-preview", "claude-3-5-sonnet", "sonnet-5", "",
	} {
		if PreservesCallerRequestSurface(model) {
			t.Errorf("%q must not preserve the caller request surface", model)
		}
		if IsSonnet55Plus(model) {
			t.Errorf("%q must not match upstream's Sonnet 5.5 predicate", model)
		}
	}
}

// ---------------------------------------------------------------------------
// Stock versus patched, recorded side by side.
// ---------------------------------------------------------------------------

// wireAndBetas is the projection plus the beta tokens the builder derived for
// the same request. Betas are derived FROM the body, so they belong in the same
// record: a case that moves the body without moving the headers computed from
// it is a request that describes itself wrongly.
func (p surfaceProbe) wireAndBetas(t *testing.T) string {
	t.Helper()
	body, ctx := p.build(t)
	return projectAnthropicWire(body) + " | beta=" + projectAnthropicBetas(t, ctx)
}

// surfaceDelta is one request shape with BOTH columns recorded: what upstream
// dev forwards at adea078c4 (stock) and what this change must forward (want).
//
// The reason both are here rather than only `want`: upstream moved under this
// change (PR #7665) and three of the first draft's goldens were quietly no
// longer describing a fix. A single `want` column cannot tell a fix apart from
// a case that agrees with stock, so the classification is declared and checked:
// a `fix` row must record a real drift off stock, and a `control` row must
// record none. If upstream changes again, the mismatch is a red test naming the
// row rather than a comment that has stopped being true.
//
// Both columns are measured, not argued: `stock` off the untouched tree and
// `want` off this one, through BuildAnthropicResponsesRequestBody.
type surfaceDelta struct {
	name  string
	probe surfaceProbe
	// stock is the measured upstream output for this probe.
	stock string
	// want is the required output for this probe on this tree.
	want string
	// fix is true when this row is a behaviour change, false when it is an
	// invariance control that must read identically on both trees.
	fix bool
}

func TestSonnet55StockVersusPatchedSurface(t *testing.T) {
	const (
		beta    = " | beta=" + AnthropicInterleavedThinkingBetaHeader
		noBeta  = " | beta=-"
		between = `thinking={"type":"between_tools"} effort=- temperature=- top_p=- top_k=-`
		absent  = `thinking=- effort=- temperature=- top_p=- top_k=-`
	)
	temp, topP, topK := 0.7, 0.9, 40
	low := "low"
	sampling := func(model string, raw, count bool, drop string) surfaceProbe {
		return surfaceProbe{
			model: model, temp: &temp, topP: &topP, topK: &topK,
			raw: raw, count: count, dropNeutral: drop,
		}
	}

	rows := []surfaceDelta{
		// -- between_tools -------------------------------------------------
		{
			// PR #7665 already carries this one through the neutral
			// thinking.type for a model id that resolves to the Anthropic
			// family, so it is a control, not a fix.
			"between_tools typed, family-resolving spelling",
			surfaceProbe{model: "claude-sonnet-5-5", thinking: betweenTools()},
			between + noBeta, between + noBeta, false,
		},
		{
			// The bare spelling resolves to no family, so upstream's neutral
			// carrier never fires and the mode is dropped outright. The
			// ingress witness does not depend on family resolution.
			"between_tools typed, bare spelling",
			surfaceProbe{model: "sonnet-5-5", thinking: betweenTools()},
			absent + noBeta, between + noBeta, true,
		},
		{
			"between_tools count, bare spelling",
			surfaceProbe{model: "sonnet-5-5", thinking: betweenTools(), count: true},
			absent + noBeta, between + noBeta, true,
		},
		{
			"between_tools with caller effort typed, bare spelling",
			surfaceProbe{model: "sonnet-5-5", thinking: betweenTools(), effort: &low},
			`thinking={"type":"adaptive","display":"summarized"} effort="low" temperature=- top_p=- top_k=-` + noBeta,
			`thinking={"type":"between_tools"} effort="low" temperature=- top_p=- top_k=-` + noBeta, true,
		},
		{
			"between_tools raw",
			surfaceProbe{model: "claude-sonnet-5-5", thinking: betweenTools(), raw: true},
			between + noBeta, between + noBeta, false,
		},
		{
			// display has no neutral carrier at all, so it is still dropped on
			// every typed spelling while the raw path forwards it.
			"between_tools with display typed",
			surfaceProbe{model: "claude-sonnet-5-5", thinking: &AnthropicThinking{Type: "between_tools", Display: schemas.Ptr("omitted")}},
			between + noBeta,
			`thinking={"type":"between_tools","display":"omitted"} effort=- temperature=- top_p=- top_k=-` + noBeta, true,
		},
		{
			"between_tools with display raw",
			surfaceProbe{model: "claude-sonnet-5-5", thinking: &AnthropicThinking{Type: "between_tools", Display: schemas.Ptr("omitted")}, raw: true},
			`thinking={"type":"between_tools","display":"omitted"} effort=- temperature=- top_p=- top_k=-` + noBeta,
			`thinking={"type":"between_tools","display":"omitted"} effort=- temperature=- top_p=- top_k=-` + noBeta, false,
		},

		// -- enabled + budget ----------------------------------------------
		{
			"enabled+budget typed",
			surfaceProbe{model: "claude-sonnet-5-5", thinking: enabledWithBudget()},
			`thinking={"type":"adaptive","display":"summarized"} effort="high" temperature=- top_p=- top_k=-` + noBeta,
			`thinking={"type":"enabled","budget_tokens":4096} effort=- temperature=- top_p=- top_k=-` + beta, true,
		},
		{
			"enabled+budget streaming",
			surfaceProbe{model: "claude-sonnet-5-5", thinking: enabledWithBudget(), streaming: true},
			`thinking={"type":"adaptive","display":"summarized"} effort="high" temperature=- top_p=- top_k=-` + noBeta,
			`thinking={"type":"enabled","budget_tokens":4096} effort=- temperature=- top_p=- top_k=-` + beta, true,
		},
		{
			"enabled+budget count",
			surfaceProbe{model: "claude-sonnet-5-5", thinking: enabledWithBudget(), count: true},
			`thinking={"type":"adaptive","display":"summarized"} effort="high" temperature=- top_p=- top_k=-` + noBeta,
			`thinking={"type":"enabled","budget_tokens":4096} effort=- temperature=- top_p=- top_k=-` + beta, true,
		},
		{
			"enabled+budget raw",
			surfaceProbe{model: "claude-sonnet-5-5", thinking: enabledWithBudget(), raw: true},
			`thinking={"type":"adaptive"} effort=- temperature=- top_p=- top_k=-` + noBeta,
			`thinking={"type":"enabled","budget_tokens":4096} effort=- temperature=- top_p=- top_k=-` + beta, true,
		},
		{
			// An effort the caller DID send stays exactly as sent, beside the
			// restored budget, and the beta token still tracks the body.
			"enabled+budget with caller effort typed",
			surfaceProbe{model: "claude-sonnet-5-5", thinking: enabledWithBudget(), effort: &low},
			`thinking={"type":"adaptive","display":"summarized"} effort="low" temperature=- top_p=- top_k=-` + noBeta,
			`thinking={"type":"enabled","budget_tokens":4096} effort="low" temperature=- top_p=- top_k=-` + beta, true,
		},
		{
			"enabled+budget with caller effort raw",
			surfaceProbe{model: "claude-sonnet-5-5", thinking: enabledWithBudget(), effort: &low, raw: true},
			`thinking={"type":"adaptive"} effort="low" temperature=- top_p=- top_k=-` + noBeta,
			`thinking={"type":"enabled","budget_tokens":4096} effort="low" temperature=- top_p=- top_k=-` + beta, true,
		},

		// -- sampling scalars ----------------------------------------------
		{
			"sampling tuple typed",
			sampling("claude-sonnet-5-5", false, false, ""),
			absent + noBeta,
			`thinking=- effort=- temperature=0.7 top_p=0.9 top_k=40` + noBeta, true,
		},
		{
			// count_tokens deletes temperature for every model on both paths.
			// This change keeps that, so the row pins the deletion.
			"sampling tuple count",
			sampling("claude-sonnet-5-5", false, true, ""),
			absent + noBeta,
			`thinking=- effort=- temperature=- top_p=0.9 top_k=40` + noBeta, true,
		},
		{
			// The loss point is the neutral hop, not the egress read: a
			// normalizing layer that cleared the two named fields off the
			// neutral request leaves the egress nothing to forward.
			"sampling tuple typed after neutral normalization",
			sampling("claude-sonnet-5-5", false, false, "named"),
			absent + noBeta,
			`thinking=- effort=- temperature=0.7 top_p=0.9 top_k=40` + noBeta, true,
		},
		{
			"sampling tuple raw",
			sampling("claude-sonnet-5-5", true, false, ""),
			`thinking=- effort=- temperature=0.7 top_p=0.9 top_k=40` + noBeta,
			`thinking=- effort=- temperature=0.7 top_p=0.9 top_k=40` + noBeta, false,
		},
		{
			"sampling tuple raw count",
			sampling("claude-sonnet-5-5", true, true, ""),
			`thinking=- effort=- temperature=- top_p=0.9 top_k=40` + noBeta,
			`thinking=- effort=- temperature=- top_p=0.9 top_k=40` + noBeta, false,
		},

		// -- modes outside the closed set, same model ----------------------
		{
			"adaptive typed",
			surfaceProbe{model: "claude-sonnet-5-5", thinking: &AnthropicThinking{Type: "adaptive"}},
			`thinking={"type":"adaptive","display":"summarized"} effort="high" temperature=- top_p=- top_k=-` + noBeta,
			`thinking={"type":"adaptive","display":"summarized"} effort="high" temperature=- top_p=- top_k=-` + noBeta, false,
		},
		{
			// Sonnet 5.5 is always-on since PR #7665, so an explicit "disabled"
			// is omitted on the typed path and rewritten to "adaptive" on the
			// raw one. Neither is re-litigated here.
			"disabled typed",
			surfaceProbe{model: "claude-sonnet-5-5", thinking: &AnthropicThinking{Type: "disabled"}},
			absent + noBeta, absent + noBeta, false,
		},
		{
			"disabled raw",
			surfaceProbe{model: "claude-sonnet-5-5", thinking: &AnthropicThinking{Type: "disabled"}, raw: true},
			`thinking={"type":"adaptive"} effort=- temperature=- top_p=- top_k=-` + noBeta,
			`thinking={"type":"adaptive"} effort=- temperature=- top_p=- top_k=-` + noBeta, false,
		},

		// -- the neighbouring model: nothing may move ----------------------
		{
			"neighbour model enabled+budget typed",
			surfaceProbe{model: "claude-sonnet-5", thinking: enabledWithBudget()},
			`thinking={"type":"adaptive","display":"summarized"} effort="low" temperature=- top_p=- top_k=-` + noBeta,
			`thinking={"type":"adaptive","display":"summarized"} effort="low" temperature=- top_p=- top_k=-` + noBeta, false,
		},
		{
			"neighbour model between_tools typed",
			surfaceProbe{model: "claude-sonnet-5", thinking: betweenTools()},
			`thinking={"type":"disabled"} effort=- temperature=- top_p=- top_k=-` + noBeta,
			`thinking={"type":"disabled"} effort=- temperature=- top_p=- top_k=-` + noBeta, false,
		},
		{
			"neighbour model between_tools raw",
			surfaceProbe{model: "claude-sonnet-5", thinking: betweenTools(), raw: true},
			`thinking={"type":"disabled"} effort=- temperature=- top_p=- top_k=-` + noBeta,
			`thinking={"type":"disabled"} effort=- temperature=- top_p=- top_k=-` + noBeta, false,
		},
		{
			"neighbour model sampling tuple typed",
			sampling("claude-sonnet-5", false, false, ""),
			absent + noBeta, absent + noBeta, false,
		},
		{
			"neighbour model sampling tuple raw",
			sampling("claude-sonnet-5", true, false, ""),
			`thinking=- effort=- temperature=0.7 top_p=0.9 top_k=40` + noBeta,
			`thinking=- effort=- temperature=0.7 top_p=0.9 top_k=40` + noBeta, false,
		},
	}

	fixes := 0
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			if got := row.probe.wireAndBetas(t); got != row.want {
				t.Errorf("%s:\n got  %s\n want %s", row.probe.label(), got, row.want)
			}
			// The declared classification is checked against the recorded
			// columns, so a row that silently stops being a fix -- or starts
			// being one -- fails here instead of going unnoticed.
			if row.fix && row.stock == row.want {
				t.Errorf("declared a fix but records no drift off stock:\n stock %s", row.stock)
			}
			if !row.fix && row.stock != row.want {
				t.Errorf("declared an invariance control but records a drift off stock:\n stock %s\n want  %s", row.stock, row.want)
			}
		})
		if row.fix {
			fixes++
		}
	}
	// Pinned so a wholesale demotion of the table to controls is visible in
	// review rather than a quietly green run.
	if want := 13; fixes != want {
		t.Errorf("table declares %d fixes, want %d", fixes, want)
	}
}

// neutralProbe is one request shape that NEVER arrived as an Anthropic Messages
// body: an OpenAI-shaped inbound dialect reaching the same Anthropic egress.
// Nothing in this patch may change what these produce -- the preserved surface
// is scoped to a recorded native Anthropic ingress precisely so that a caller of
// the neutral or OpenAI-compatible surfaces gets byte-identical bytes before and
// after. Every expectation below is a MEASURED stock output of the untouched
// pinned tree.
type neutralProbe struct {
	model     string
	params    *schemas.ResponsesParameters
	streaming bool
	count     bool
}

func (p neutralProbe) wire(t *testing.T) string {
	t.Helper()
	ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.Anthropic,
		Model:    p.model,
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")},
		}},
		Params: p.params,
	}
	body, bErr := BuildAnthropicResponsesRequestBody(ctx, req, AnthropicRequestBuildConfig{
		Provider:      schemas.Anthropic,
		IsStreaming:   p.streaming,
		IsCountTokens: p.count,
		ValidateTools: true,
	})
	if bErr != nil {
		t.Fatalf("build neutral request body: %v", bErr)
	}
	return projectAnthropicWire(body)
}

// A neutral Responses request on the SAME model keeps stock bytes for every
// sampling field individually, for all three together, and for each reasoning
// shape the neutral parameters can express -- including the thinking.type
// upstream added to ResponsesParametersReasoning in PR #7665, whose neutral
// answer must stay exactly what it is today on both models.
//
// Asserted against an older model too, so the two are proven to still answer
// identically where they agree AND to still differ where upstream makes them
// differ: Sonnet 5.5 is always-on, so a neutral reasoning-off ask omits the
// parameter where Sonnet 5 sends thinking:{"type":"disabled"}, and a neutral
// between_tools is forwarded where Sonnet 5 gets the downgrade. A single shared
// golden would have had to pick one of those and would silently accept the
// other, so the diverging cases carry a per-model column.
func TestNeutralResponsesInboundIsByteUnchanged(t *testing.T) {
	temp, topP, topK := 0.7, 0.9, 40
	mk := func(f func(p *schemas.ResponsesParameters)) *schemas.ResponsesParameters {
		p := &schemas.ResponsesParameters{ExtraParams: map[string]interface{}{}}
		f(p)
		return p
	}
	const (
		nAbsent   = `thinking=- effort=- temperature=- top_p=- top_k=-`
		nAdaptive = `thinking={"type":"adaptive","display":"summarized"} effort=- temperature=- top_p=- top_k=-`
		nAdaptLow = `thinking={"type":"adaptive","display":"summarized"} effort="low" temperature=- top_p=- top_k=-`
		nDisabled = `thinking={"type":"disabled"} effort=- temperature=- top_p=- top_k=-`
		nBetween  = `thinking={"type":"between_tools"} effort=- temperature=- top_p=- top_k=-`
	)
	cases := []struct {
		name   string
		params func() *schemas.ResponsesParameters
		want   string
		// perModel overrides want where upstream itself answers the two models
		// differently. Measured, not assumed.
		perModel map[string]string
	}{
		{"temperature", func() *schemas.ResponsesParameters {
			return mk(func(p *schemas.ResponsesParameters) { p.Temperature = &temp })
		}, nAbsent, nil},
		{"top_p", func() *schemas.ResponsesParameters {
			return mk(func(p *schemas.ResponsesParameters) { p.TopP = &topP })
		}, nAbsent, nil},
		{"top_k", func() *schemas.ResponsesParameters {
			return mk(func(p *schemas.ResponsesParameters) { p.ExtraParams["top_k"] = topK })
		}, nAbsent, nil},
		{"all-three", func() *schemas.ResponsesParameters {
			return mk(func(p *schemas.ResponsesParameters) {
				p.Temperature = &temp
				p.TopP = &topP
				p.ExtraParams["top_k"] = topK
			})
		}, nAbsent, nil},
		{"reasoning-effort-low", func() *schemas.ResponsesParameters {
			return mk(func(p *schemas.ResponsesParameters) {
				p.Reasoning = &schemas.ResponsesParametersReasoning{Effort: schemas.Ptr("low")}
			})
		}, nAdaptLow, nil},
		{"reasoning-effort-none", func() *schemas.ResponsesParameters {
			return mk(func(p *schemas.ResponsesParameters) {
				p.Reasoning = &schemas.ResponsesParametersReasoning{Effort: schemas.Ptr("none")}
			})
		}, nDisabled, map[string]string{"claude-sonnet-5-5": nAbsent}},
		{"reasoning-budget", func() *schemas.ResponsesParameters {
			return mk(func(p *schemas.ResponsesParameters) {
				p.Reasoning = &schemas.ResponsesParametersReasoning{MaxTokens: schemas.Ptr(4096)}
			})
		}, nAdaptive, nil},
		{"reasoning-budget-and-all-sampling", func() *schemas.ResponsesParameters {
			return mk(func(p *schemas.ResponsesParameters) {
				p.Reasoning = &schemas.ResponsesParametersReasoning{MaxTokens: schemas.Ptr(4096)}
				p.Temperature = &temp
				p.TopP = &topP
				p.ExtraParams["top_k"] = topK
			})
		}, nAdaptive, nil},
		{"reasoning-type-between-tools", func() *schemas.ResponsesParameters {
			return mk(func(p *schemas.ResponsesParameters) {
				p.Reasoning = &schemas.ResponsesParametersReasoning{Type: schemas.Ptr("between_tools")}
			})
		}, nDisabled, map[string]string{"claude-sonnet-5-5": nBetween}},
	}
	for _, model := range []string{"claude-sonnet-5-5", "claude-sonnet-5"} {
		t.Run(model, func(t *testing.T) {
			for _, c := range cases {
				want := c.want
				if override, ok := c.perModel[model]; ok {
					want = override
				}
				for _, mode := range []string{"unary", "streaming", "count"} {
					got := neutralProbe{
						model: model, params: c.params(),
						streaming: mode == "streaming", count: mode == "count",
					}.wire(t)
					if got != want {
						t.Errorf("%s %s/%s:\n got  %s\n want %s", model, c.name, mode, got, want)
					}
				}
			}
		})
	}
}

// The OpenAI-compatible CHAT surface reaches the Anthropic egress through a
// different converter and a SHARED strip pass. An earlier attempt exempted that
// shared strip on the model alone, which left a promoted
// thinking:{"type":"enabled"} (no budget) as "enabled" for an OpenAI Chat caller
// where stock emits "adaptive". The strip is no longer touched at all; the
// preserved surface is re-asserted after it instead, only for a native Anthropic
// ingress. These are the measured stock bytes that prove it.
//
// The between_tools rows are the ones upstream itself answers per-model since
// PR #7665 -- Sonnet 5.5 keeps it, Sonnet 5 gets the "disabled" downgrade -- so
// they carry a per-model column rather than one golden that would accept either.
func TestNeutralChatInboundIsByteUnchanged(t *testing.T) {
	temp, topP, topK := 0.7, 0.9, 40
	mk := func(f func(p *schemas.ChatParameters)) *schemas.ChatParameters {
		p := &schemas.ChatParameters{ExtraParams: map[string]interface{}{}}
		f(p)
		return p
	}
	const (
		cAbsent   = `thinking=- effort=- temperature=- top_p=- top_k=-`
		cAdaptive = `thinking={"type":"adaptive","display":"summarized"} effort=- temperature=- top_p=- top_k=-`
		cAdaptLow = `thinking={"type":"adaptive","display":"summarized"} effort="low" temperature=- top_p=- top_k=-`
		cDisabled = `thinking={"type":"disabled"} effort=- temperature=- top_p=- top_k=-`
		cBetween  = `thinking={"type":"between_tools"} effort=- temperature=- top_p=- top_k=-`
	)
	cases := []struct {
		name     string
		params   func() *schemas.ChatParameters
		want     string
		perModel map[string]string
	}{
		{"extraparams-thinking-enabled-no-budget", func() *schemas.ChatParameters {
			return mk(func(p *schemas.ChatParameters) {
				p.ExtraParams["thinking"] = map[string]interface{}{"type": "enabled"}
			})
		}, cAdaptive, nil},
		{"extraparams-thinking-enabled-with-budget", func() *schemas.ChatParameters {
			return mk(func(p *schemas.ChatParameters) {
				p.ExtraParams["thinking"] = map[string]interface{}{"type": "enabled", "budget_tokens": 4096}
			})
		}, cAdaptive, nil},
		{"extraparams-thinking-between-tools", func() *schemas.ChatParameters {
			return mk(func(p *schemas.ChatParameters) {
				p.ExtraParams["thinking"] = map[string]interface{}{"type": "between_tools"}
			})
		}, cDisabled, map[string]string{"claude-sonnet-5-5": cBetween}},
		{"reasoning-type-between-tools", func() *schemas.ChatParameters {
			return mk(func(p *schemas.ChatParameters) {
				p.Reasoning = &schemas.ChatReasoning{Type: schemas.Ptr("between_tools")}
			})
		}, cDisabled, map[string]string{"claude-sonnet-5-5": cBetween}},
		{"extraparams-thinking-adaptive", func() *schemas.ChatParameters {
			return mk(func(p *schemas.ChatParameters) {
				p.ExtraParams["thinking"] = map[string]interface{}{"type": "adaptive"}
			})
		}, cAdaptive, nil},
		{"all-sampling", func() *schemas.ChatParameters {
			return mk(func(p *schemas.ChatParameters) {
				p.Temperature = &temp
				p.TopP = &topP
				p.TopK = &topK
			})
		}, cAbsent, nil},
		{"reasoning-budget", func() *schemas.ChatParameters {
			return mk(func(p *schemas.ChatParameters) {
				p.Reasoning = &schemas.ChatReasoning{MaxTokens: schemas.Ptr(4096)}
			})
		}, cAdaptive, nil},
		{"reasoning-effort-low", func() *schemas.ChatParameters {
			return mk(func(p *schemas.ChatParameters) {
				p.Reasoning = &schemas.ChatReasoning{Effort: schemas.Ptr("low")}
			})
		}, cAdaptLow, nil},
	}
	for _, model := range []string{"claude-sonnet-5-5", "claude-sonnet-5"} {
		t.Run(model, func(t *testing.T) {
			for _, c := range cases {
				want := c.want
				if override, ok := c.perModel[model]; ok {
					want = override
				}
				ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
				req := &schemas.BifrostChatRequest{
					Provider: schemas.Anthropic,
					Model:    model,
					Input: []schemas.ChatMessage{{
						Role:    schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")},
					}},
					Params: c.params(),
				}
				body, bErr := BuildAnthropicChatRequestBody(ctx, req, AnthropicRequestBuildConfig{
					Provider: schemas.Anthropic, ValidateTools: true,
				})
				if bErr != nil {
					t.Fatalf("build neutral chat body: %v", bErr)
				}
				if got := projectAnthropicWire(body); got != want {
					t.Errorf("%s %s:\n got  %s\n want %s", model, c.name, got, want)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The restore touches the preserved surface and nothing else: large and deeply
// nested tool context, thinking blocks and their signatures, and the typed
// shape of every preserved field.
// ---------------------------------------------------------------------------

// surfaceContextDepth is the nesting depth the tool schema below is built to.
// It is deliberately past any round number a defensive recursion bound would
// pick, so a cap reintroduced anywhere between the ingress witness and the
// marshalled body shows up as a missing leaf rather than as a slower test.
const surfaceContextDepth = 420

// surfaceContextBlobBytes is the size of the single largest string in that
// request, chosen past 8 KiB for the same reason: a truncation bound added to
// any strip, copy or re-marshal pass on this path fails the length assertion
// instead of silently shortening a caller's tool description.
const surfaceContextBlobBytes = 16384

// deepToolSchema builds a tool input schema nested `depth` objects deep, with a
// leaf that names its own depth so a truncated tree is identified by WHERE it
// stopped rather than merely reported as absent.
func deepToolSchema(depth int) *schemas.OrderedMap {
	leaf := schemas.NewOrderedMapFromPairs(
		schemas.Pair{Key: "type", Value: "string"},
		schemas.Pair{Key: "const", Value: fmt.Sprintf("leaf-at-depth-%d", depth)},
	)
	node := leaf
	for i := depth - 1; i >= 1; i-- {
		node = schemas.NewOrderedMapFromPairs(
			schemas.Pair{Key: "type", Value: "object"},
			schemas.Pair{Key: "properties", Value: schemas.NewOrderedMapFromPairs(
				schemas.Pair{Key: "next", Value: node},
			)},
		)
	}
	return node
}

// deepToolSchemaPath is the gjson path to that leaf's const value.
func deepToolSchemaPath(depth int) string {
	path := "tools.0.input_schema.properties.root"
	for i := 1; i < depth; i++ {
		path += ".properties.next"
	}
	return path + ".const"
}

// largeToolContextRequest is one native Anthropic Messages request carrying the
// kinds of content this change must not touch: a deeply nested tool schema, a
// large enum, a 16 KiB description, an assistant thinking block with its
// signature, a redacted_thinking block, a tool_use / tool_result pair, and the
// request-level container and metadata objects.
func largeToolContextRequest(model string, thinking *AnthropicThinking) *AnthropicMessageRequest {
	enum := make([]string, 0, 2048)
	for i := range 2048 {
		enum = append(enum, fmt.Sprintf("choice-%04d", i))
	}
	blob := strings.Repeat("d", surfaceContextBlobBytes)

	toolType := AnthropicToolTypeCustom
	return &AnthropicMessageRequest{
		Model:     model,
		MaxTokens: 1024,
		Thinking:  thinking,
		Tools: []AnthropicTool{{
			Name:        "deep_lookup",
			Type:        &toolType,
			Description: schemas.Ptr(blob),
			InputSchema: &schemas.ToolFunctionParameters{
				Type: "object",
				Properties: schemas.NewOrderedMapFromPairs(
					schemas.Pair{Key: "root", Value: deepToolSchema(surfaceContextDepth)},
					schemas.Pair{Key: "choice", Value: schemas.NewOrderedMapFromPairs(
						schemas.Pair{Key: "type", Value: "string"},
						schemas.Pair{Key: "enum", Value: enum},
					)},
				),
				Required: []string{"root"},
			},
		}},
		Metadata:  &AnthropicMetaData{UserID: schemas.Ptr("surface-user")},
		Container: &AnthropicContainer{ContainerStr: schemas.Ptr("surface-container")},
		Messages: []AnthropicMessage{
			{Role: "user", Content: AnthropicContent{ContentStr: schemas.Ptr("run the deep tool")}},
			{Role: "assistant", Content: AnthropicContent{ContentBlocks: []AnthropicContentBlock{
				{
					Type:      AnthropicContentBlockTypeThinking,
					Thinking:  schemas.Ptr("a thought that must survive the round trip"),
					Signature: schemas.Ptr("sig-0123456789abcdef"),
				},
				{
					Type: AnthropicContentBlockTypeRedactedThinking,
					Data: schemas.Ptr("redacted-payload"),
				},
				{
					Type:  AnthropicContentBlockTypeToolUse,
					ID:    schemas.Ptr("toolu_surface_1"),
					Name:  schemas.Ptr("deep_lookup"),
					Input: json.RawMessage(`{"root":{"next":"value"}}`),
				},
			}}},
			{Role: "user", Content: AnthropicContent{ContentBlocks: []AnthropicContentBlock{
				{
					Type:      AnthropicContentBlockTypeToolResult,
					ToolUseID: schemas.Ptr("toolu_surface_1"),
					Content:   &AnthropicContent{ContentStr: schemas.Ptr("tool answered")},
				},
			}}},
		},
	}
}

// buildLargeToolContext puts that request through the real builder. shed drops
// the ingress witness the way ClearContextForInternalRequest does, which is the
// in-tree control for "what this build does without the restore": the same
// tree, the same request, the same model, the only difference being whether the
// native-ingress witness is on the context.
func buildLargeToolContext(t *testing.T, model string, thinking *AnthropicThinking, sampling bool, shed bool) []byte {
	t.Helper()
	ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
	in := largeToolContextRequest(model, thinking)
	if sampling {
		in.Temperature = schemas.Ptr(0.7)
		in.TopP = schemas.Ptr(0.9)
		in.TopK = schemas.Ptr(40)
	}
	neutral := in.ToBifrostResponsesRequest(ctx)
	if shed {
		ctx.SetValue(schemas.BifrostContextKeyAnthropicNativeRequestSurface, nil)
	}
	body, bErr := BuildAnthropicResponsesRequestBody(ctx, neutral, AnthropicRequestBuildConfig{
		Provider:      schemas.Anthropic,
		ValidateTools: true,
	})
	if bErr != nil {
		t.Fatalf("build request body (model=%s shed=%v): %v", model, shed, bErr)
	}
	return body
}

// TestLargeDeepToolContextSurvivesThePreservedBuild pins what the restore must
// NOT do to a request carrying real agent context. It asserts presence, type
// and exact value of the content this change never reads: the deepest leaf of a
// 420-level tool schema, a 2048-entry enum, a 16 KiB description, a thinking
// block's own text and signature, a redacted_thinking block, a tool_use /
// tool_result pair, and the container and metadata objects.
//
// Measured against the live body, not against a recorded string, so a cap or a
// dropped object anywhere between the ingress witness and the marshalled bytes
// fails here.
func TestLargeDeepToolContextSurvivesThePreservedBuild(t *testing.T) {
	body := buildLargeToolContext(t, "claude-sonnet-5-5", enabledWithBudget(), true, false)

	if got := providerUtils.GetJSONField(body, deepToolSchemaPath(surfaceContextDepth)).String(); got != fmt.Sprintf("leaf-at-depth-%d", surfaceContextDepth) {
		t.Errorf("deepest tool-schema leaf lost or truncated: got %q", got)
	}
	if got := len(providerUtils.GetJSONField(body, "tools.0.input_schema.properties.choice.enum").Array()); got != 2048 {
		t.Errorf("tool enum truncated: %d entries, want 2048", got)
	}
	if got := len(providerUtils.GetJSONField(body, "tools.0.description").String()); got != surfaceContextBlobBytes {
		t.Errorf("tool description truncated: %d bytes, want %d", got, surfaceContextBlobBytes)
	}

	for path, want := range map[string]string{
		"messages.1.content.0.type":        "thinking",
		"messages.1.content.0.thinking":    "a thought that must survive the round trip",
		"messages.1.content.0.signature":   "sig-0123456789abcdef",
		"messages.1.content.1.type":        "redacted_thinking",
		"messages.1.content.1.data":        "redacted-payload",
		"messages.1.content.2.type":        "tool_use",
		"messages.1.content.2.id":          "toolu_surface_1",
		"messages.2.content.0.type":        "tool_result",
		"messages.2.content.0.tool_use_id": "toolu_surface_1",
		"container":                        "surface-container",
		"metadata.user_id":                 "surface-user",
	} {
		if got := providerUtils.GetJSONField(body, path).String(); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	if !providerUtils.GetJSONField(body, "messages.1.content.2.input").IsObject() {
		t.Errorf("tool_use input is not an object: %s", providerUtils.GetJSONField(body, "messages.1.content.2.input").Raw)
	}

	// The preserved surface is asserted on the SAME body, so this is not two
	// independent facts: the caller's thinking and sampling are forwarded WHILE
	// the deep tool context is intact, which is the only combination a real
	// agent request has.
	if got, want := projectAnthropicWire(body), `thinking={"type":"enabled","budget_tokens":4096} effort=- temperature=0.7 top_p=0.9 top_k=40`; got != want {
		t.Errorf("preserved surface on the large request:\n got  %s\n want %s", got, want)
	}
}

// TestPreservedRestoreIsConfinedToTheCallerSurface is the equivalence half of
// the test above: the SAME tree, the SAME request and the SAME model built with
// and without the ingress witness differ in EXACTLY the five fields this change
// is responsible for. Everything else -- every tool, every nested schema level,
// every thinking block and signature, the container, the metadata and the
// messages -- must be byte-identical.
//
// Shedding the witness is upstream's behaviour for this build, which is what
// makes this a stock-versus-patched comparison that needs no second checkout.
func TestPreservedRestoreIsConfinedToTheCallerSurface(t *testing.T) {
	for _, tc := range []struct {
		name     string
		thinking *AnthropicThinking
		sampling bool
	}{
		{"enabled+budget and sampling", enabledWithBudget(), true},
		{"between_tools with display", &AnthropicThinking{Type: "between_tools", Display: schemas.Ptr("omitted")}, false},
		{"sampling only", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			preserved := buildLargeToolContext(t, "claude-sonnet-5-5", tc.thinking, tc.sampling, false)
			shed := buildLargeToolContext(t, "claude-sonnet-5-5", tc.thinking, tc.sampling, true)

			if string(preserved) == string(shed) {
				t.Fatalf("the restore changed nothing at all; the witness is not reaching the builder")
			}
			for _, path := range []string{"thinking", "output_config", "temperature", "top_p", "top_k"} {
				var err error
				if preserved, err = providerUtils.DeleteJSONField(preserved, path); err != nil {
					t.Fatalf("delete %s from preserved body: %v", path, err)
				}
				if shed, err = providerUtils.DeleteJSONField(shed, path); err != nil {
					t.Fatalf("delete %s from shed body: %v", path, err)
				}
			}
			if string(preserved) != string(shed) {
				t.Errorf("the restore reached outside the preserved surface; bodies differ with the five fields removed\n preserved %d bytes\n shed      %d bytes",
					len(preserved), len(shed))
			}
		})
	}
}

// TestPreservedSurfaceFieldTypes pins the JSON TYPE and raw spelling of each
// preserved field, not just its value. A budget restored as a float, a display
// restored as a number or a top_k restored as 40.0 would all satisfy a
// value-only assertion and all be a different request on the wire.
func TestPreservedSurfaceFieldTypes(t *testing.T) {
	ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
	in := &AnthropicMessageRequest{
		Model:     "claude-sonnet-5-5",
		MaxTokens: 1024,
		Messages: []AnthropicMessage{
			{Role: "user", Content: AnthropicContent{ContentStr: schemas.Ptr("hi")}},
		},
		Thinking:    &AnthropicThinking{Type: "enabled", BudgetTokens: schemas.Ptr(4096)},
		Temperature: schemas.Ptr(0.7),
		TopP:        schemas.Ptr(0.9),
		TopK:        schemas.Ptr(40),
	}
	neutral := in.ToBifrostResponsesRequest(ctx)
	body, bErr := BuildAnthropicResponsesRequestBody(ctx, neutral, AnthropicRequestBuildConfig{
		Provider: schemas.Anthropic,
	})
	if bErr != nil {
		t.Fatalf("build request body: %v", bErr)
	}

	for _, want := range []struct {
		path string
		raw  string
		kind string
	}{
		{"thinking.type", `"enabled"`, "String"},
		{"thinking.budget_tokens", "4096", "Number"},
		{"temperature", "0.7", "Number"},
		{"top_p", "0.9", "Number"},
		{"top_k", "40", "Number"},
	} {
		got := providerUtils.GetJSONField(body, want.path)
		if !got.Exists() {
			t.Errorf("%s is absent from the built body", want.path)
			continue
		}
		if got.Type.String() != want.kind {
			t.Errorf("%s has JSON type %s, want %s", want.path, got.Type, want.kind)
		}
		if got.Raw != want.raw {
			t.Errorf("%s raw = %s, want %s", want.path, got.Raw, want.raw)
		}
	}

	// display is restored as a string, beside a thinking.type this build keeps.
	ctx2 := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
	in2 := &AnthropicMessageRequest{
		Model:     "claude-sonnet-5-5",
		MaxTokens: 1024,
		Messages: []AnthropicMessage{
			{Role: "user", Content: AnthropicContent{ContentStr: schemas.Ptr("hi")}},
		},
		Thinking: &AnthropicThinking{Type: "between_tools", Display: schemas.Ptr("omitted")},
	}
	body2, bErr := BuildAnthropicResponsesRequestBody(ctx2, in2.ToBifrostResponsesRequest(ctx2), AnthropicRequestBuildConfig{
		Provider: schemas.Anthropic,
	})
	if bErr != nil {
		t.Fatalf("build request body: %v", bErr)
	}
	display := providerUtils.GetJSONField(body2, "thinking.display")
	if display.Type.String() != "String" || display.Raw != `"omitted"` {
		t.Errorf("thinking.display = %s (%s), want \"omitted\" (String)", display.Raw, display.Type)
	}
	if providerUtils.JSONFieldExists(body2, "thinking.budget_tokens") {
		t.Errorf("between_tools must not gain a budget_tokens: %s", providerUtils.GetJSONField(body2, "thinking").Raw)
	}
}

// TestCompatPluginDropIsRespectedNotUndone draws the line this restore has to
// hold: it recovers what the CONVERSION could not carry, and leaves alone what a
// layer removed on purpose.
//
// The built-in compat plugin drops temperature, top_p or reasoning when the
// model catalog does not allowlist them, and publishes the list on the context.
// Recovering those would hand the provider back the very parameter the plugin
// removed to make the request servable -- the restore would be reintroducing a
// 400. An anonymous layer that merely clears a value (dropNeutral, with nothing
// published) is still treated as a loss, because nothing says otherwise.
func TestCompatPluginDropIsRespectedNotUndone(t *testing.T) {
	const model = "claude-sonnet-5-5"
	temp, topP := 0.7, 0.9

	t.Run("a published temperature/top_p drop is not undone", func(t *testing.T) {
		assertWire(t, surfaceProbe{
			model: model, temp: &temp, topP: &topP,
			dropNeutral:   "named",
			compatDropped: []string{"temperature", "top_p"},
		}, wAbsent)
	})

	t.Run("an unpublished clear is still recovered", func(t *testing.T) {
		// Same drop, nothing published: this is the case the witness exists for.
		assertWire(t, surfaceProbe{
			model: model, temp: &temp, topP: &topP,
			dropNeutral: "named",
		}, `thinking=- effort=- temperature=0.7 top_p=0.9 top_k=-`)
	})

	t.Run("an unrelated published drop does not suppress sampling", func(t *testing.T) {
		assertWire(t, surfaceProbe{
			model: model, temp: &temp, topP: &topP,
			dropNeutral:   "named",
			compatDropped: []string{"seed", "service_tier"},
		}, `thinking=- effort=- temperature=0.7 top_p=0.9 top_k=-`)
	})

	// The plugin both CLEARS the neutral reasoning and publishes the drop, so
	// these legs do the same -- publishing alone would leave the converter a
	// reasoning to emit from and prove nothing about the restore.
	clearReasoning := func(params *schemas.ResponsesParameters) { params.Reasoning = nil }

	t.Run("a published reasoning drop is not undone", func(t *testing.T) {
		assertWire(t, surfaceProbe{
			model: model, thinking: betweenTools(),
			mutateNeutral: clearReasoning,
			compatDropped: []string{"reasoning"},
		}, wAbsent)
	})

	t.Run("an unpublished reasoning clear is still recovered", func(t *testing.T) {
		assertWire(t, surfaceProbe{
			model: model, thinking: betweenTools(),
			mutateNeutral: clearReasoning,
		}, `thinking={"type":"between_tools"} effort=- temperature=- top_p=- top_k=-`)
	})

	t.Run("reasoning is recovered when the published drop was something else", func(t *testing.T) {
		assertWire(t, surfaceProbe{
			model: model, thinking: betweenTools(),
			mutateNeutral: clearReasoning,
			compatDropped: []string{"temperature"},
		}, `thinking={"type":"between_tools"} effort=- temperature=- top_p=- top_k=-`)
	})
}

// TestClampedMaxTokensRefitsTheRestoredBudget covers the ordering hazard this
// restore creates for itself. It runs deliberately AFTER the strip pass, and that
// pass is where clampToModelOutputCeiling lowers max_tokens -- so a budget_tokens
// that was valid when the caller sent it can exceed the ceiling by the time it is
// restored. The raw path refits (fitRawThinkingBudget); the typed path must agree.
//
// The converse matters just as much: a budget that never fitted the caller's OWN
// max_tokens is their request as written, and is forwarded verbatim for the
// provider to answer for, rather than quietly rewritten.
func TestClampedMaxTokensRefitsTheRestoredBudget(t *testing.T) {
	const model = "claude-sonnet-5-5"

	t.Run("a lowered max_tokens refits the budget", func(t *testing.T) {
		installMaxOutputRow(t, model, 4096)
		got := surfaceProbe{
			model: model, thinking: &AnthropicThinking{Type: "enabled", BudgetTokens: schemas.Ptr(8192)},
			maxTokens: 16000,
		}.wire(t)
		if strings.Contains(got, `"budget_tokens":8192`) {
			t.Fatalf("restored a budget the clamped max_tokens cannot cover: %s", got)
		}
		if !strings.Contains(got, `"type":"enabled"`) {
			t.Fatalf("thinking should survive a refit that has room: %s", got)
		}
	})

	t.Run("an unclamped request is forwarded verbatim", func(t *testing.T) {
		// Ceiling above the caller's max_tokens, so nothing is lowered. The
		// caller's own pair stands even though 4096 does not fit 1024 -- that is
		// their request, and the provider answers for it.
		installMaxOutputRow(t, model, 200000)
		assertWire(t, surfaceProbe{model: model, thinking: enabledWithBudget()}, wEnabled)
	})
}

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

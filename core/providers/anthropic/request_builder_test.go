package anthropic

import (
	"io"
	"strings"
	"testing"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
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

func TestBuildAnthropicResponsesRequestBody_RawBodyPath(t *testing.T) {
	t.Run("anthropic_native_uses_resolved_model", func(t *testing.T) {
		// request.Model is always the alias-resolved value by the time the provider
		// method is called (k.Aliases.Resolve runs in executeRequestWithRetries before
		// the provider is invoked). The raw body may still carry the governance-modified
		// form ("anthropic/anthropic.claude-sonnet-4-5"), but the output should use the
		// resolved model from request.Model.
		ctx := schemas.NewBifrostContext(nil, time.Time{})
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
		ctx := schemas.NewBifrostContext(nil, time.Time{})
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
		ctx := schemas.NewBifrostContext(nil, time.Time{})
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)

		request := &schemas.BifrostResponsesRequest{
			Provider:       schemas.Vertex,
			Model:          "claude-sonnet-4-5",
			RawRequestBody: []byte(`{"model":"claude-sonnet-4-5","max_tokens":1024,"messages":[{"role":"user","content":"hello"}]}`),
		}

		result, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
			Provider:         schemas.Vertex,
			Deployment:       "claude-sonnet-4-5",
			DeleteModelField: true,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if providerUtils.JSONFieldExists(result, "model") {
			t.Error("expected model field to be deleted for Vertex")
		}
	})

	t.Run("azure_replaces_model_with_deployment", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(nil, time.Time{})
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)

		request := &schemas.BifrostResponsesRequest{
			Provider:       schemas.Azure,
			Model:          "claude-sonnet-4-5",
			RawRequestBody: []byte(`{"model":"claude-sonnet-4-5","max_tokens":1024,"messages":[{"role":"user","content":"hello"}]}`),
		}

		result, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
			Provider:   schemas.Azure,
			Deployment: "my-azure-deployment",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		modelVal := providerUtils.GetJSONField(result, "model").String()
		if modelVal != "my-azure-deployment" {
			t.Errorf("expected model to be 'my-azure-deployment', got %q", modelVal)
		}
	})

	t.Run("adds_max_tokens_if_missing", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(nil, time.Time{})
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
		ctx := schemas.NewBifrostContext(nil, time.Time{})
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
		ctx := schemas.NewBifrostContext(nil, time.Time{})
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)

		request := &schemas.BifrostResponsesRequest{
			Provider:       schemas.Vertex,
			Model:          "claude-sonnet-4-5",
			RawRequestBody: []byte(`{"model":"claude-sonnet-4-5","max_tokens":1024,"region":"us-central1","messages":[{"role":"user","content":"hello"}]}`),
		}

		result, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
			Provider:          schemas.Vertex,
			Deployment:        "claude-sonnet-4-5",
			DeleteModelField:  true,
			DeleteRegionField: true,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if providerUtils.JSONFieldExists(result, "region") {
			t.Error("expected region field to be deleted")
		}
	})

	t.Run("adds_anthropic_version_when_configured", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(nil, time.Time{})
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)

		request := &schemas.BifrostResponsesRequest{
			Provider:       schemas.Vertex,
			Model:          "claude-sonnet-4-5",
			RawRequestBody: []byte(`{"model":"claude-sonnet-4-5","max_tokens":1024,"messages":[{"role":"user","content":"hello"}]}`),
		}

		result, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
			Provider:            schemas.Vertex,
			Deployment:          "claude-sonnet-4-5",
			DeleteModelField:    true,
			AddAnthropicVersion: true,
			AnthropicVersion:    "vertex-2023-10-16",
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
		ctx := schemas.NewBifrostContext(nil, time.Time{})
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
		ctx := schemas.NewBifrostContext(nil, time.Time{})
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
		ctx := schemas.NewBifrostContext(nil, time.Time{})
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
			Provider:                  schemas.Vertex,
			Deployment:                "claude-sonnet-4-5",
			DeleteModelField:          true,
			InjectBetaHeadersIntoBody: true,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if !providerUtils.JSONFieldExists(result, "anthropic_beta") {
			t.Error("expected anthropic_beta to be injected into body")
		}
	})
}

func TestBuildAnthropicResponsesRequestBody_CountTokensMode(t *testing.T) {
	t.Run("count_tokens_strips_max_tokens_and_temperature_raw", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(nil, time.Time{})
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)

		request := &schemas.BifrostResponsesRequest{
			Provider:       schemas.Vertex,
			Model:          "claude-sonnet-4-5",
			RawRequestBody: []byte(`{"model":"claude-sonnet-4-5","max_tokens":1024,"temperature":0.7,"messages":[{"role":"user","content":"hello"}]}`),
		}

		result, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
			Provider:      schemas.Vertex,
			Deployment:    "claude-sonnet-4-5",
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
		ctx := schemas.NewBifrostContext(nil, time.Time{})
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)

		request := &schemas.BifrostResponsesRequest{
			Provider:       schemas.Vertex,
			Model:          "claude-sonnet-4-5",
			RawRequestBody: []byte(`{"model":"old-model","max_tokens":1024,"messages":[{"role":"user","content":"hello"}]}`),
		}

		result, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
			Provider:      schemas.Vertex,
			Deployment:    "new-deployment",
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
		ctx := schemas.NewBifrostContext(nil, time.Time{})

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
		ctx := schemas.NewBifrostContext(nil, time.Time{})

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
		ctx := schemas.NewBifrostContext(nil, time.Time{})

		request := &schemas.BifrostResponsesRequest{
			Provider: schemas.Vertex,
			Model:    "claude-sonnet-4-5",
			Input:    makeSimpleInput("Hello!"),
		}

		result, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
			Provider:         schemas.Vertex,
			Deployment:       "claude-sonnet-4-5",
			DeleteModelField: true,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if providerUtils.JSONFieldExists(result, "model") {
			t.Error("expected model to be deleted for Vertex")
		}
	})

	t.Run("typed_path_adds_anthropic_version", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(nil, time.Time{})

		request := &schemas.BifrostResponsesRequest{
			Provider: schemas.Vertex,
			Model:    "claude-sonnet-4-5",
			Input:    makeSimpleInput("Hello!"),
		}

		result, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
			Provider:            schemas.Vertex,
			Deployment:          "claude-sonnet-4-5",
			DeleteModelField:    true,
			AddAnthropicVersion: true,
			AnthropicVersion:    "vertex-2023-10-16",
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
			Deployment:    "claude-sonnet-4-5",
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

	t.Run("typed_path_validates_tools_when_configured", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(nil, time.Time{})

		request := &schemas.BifrostResponsesRequest{
			Provider: schemas.Bedrock,
			Model:    "claude-sonnet-4-5",
			Input:    makeSimpleInput("Hello!"),
			Params: &schemas.ResponsesParameters{
				Tools: []schemas.ResponsesTool{
					{Type: schemas.ResponsesToolTypeWebSearch},
				},
			},
		}

		_, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
			Provider:      schemas.Bedrock,
			ValidateTools: true,
		})
		if err == nil {
			t.Error("expected error for unsupported tool on Bedrock")
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
		ctx := schemas.NewBifrostContext(nil, time.Time{})

		request := &schemas.BifrostResponsesRequest{
			Provider: schemas.Vertex,
			Model:    "claude-sonnet-4-5",
			Input:    makeSimpleInput("Hello!"),
		}

		_, err := BuildAnthropicResponsesRequestBody(ctx, request, AnthropicRequestBuildConfig{
			Provider:               schemas.Vertex,
			Deployment:             "claude-sonnet-4-5",
			StripCacheControlScope: true,
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
			Provider:          schemas.Vertex,
			Deployment:        "claude-sonnet-4-5",
			DeleteModelField:  true,
			RemapToolVersions: true,
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

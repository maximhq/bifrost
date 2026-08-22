package anthropic

import (
	"context"
	"strings"
	"testing"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

func buildRawAnthropicResponsesBodyWithCapability(t *testing.T, ctx *schemas.BifrostContext, requestProvider, buildProvider, capabilityProvider schemas.ModelProvider, model string, raw string) ([]byte, *schemas.BifrostError) {
	t.Helper()
	ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
	return BuildAnthropicResponsesRequestBody(ctx, &schemas.BifrostResponsesRequest{
		Provider:       requestProvider,
		Model:          model,
		RawRequestBody: []byte(raw),
	}, AnthropicRequestBuildConfig{
		Provider:                      buildProvider,
		MidConversationSystemProvider: capabilityProvider,
	})
}

func buildRawAnthropicResponsesBody(t *testing.T, ctx *schemas.BifrostContext, requestProvider, buildProvider schemas.ModelProvider, model string, raw string) ([]byte, *schemas.BifrostError) {
	t.Helper()
	return buildRawAnthropicResponsesBodyWithCapability(t, ctx, requestProvider, buildProvider, buildProvider, model, raw)
}

func buildRawAnthropicChatBodyWithCapability(t *testing.T, ctx *schemas.BifrostContext, requestProvider, buildProvider, capabilityProvider schemas.ModelProvider, model string, raw string) ([]byte, *schemas.BifrostError) {
	t.Helper()
	ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
	return BuildAnthropicChatRequestBody(ctx, &schemas.BifrostChatRequest{
		Provider:       requestProvider,
		Model:          model,
		RawRequestBody: []byte(raw),
	}, AnthropicRequestBuildConfig{
		Provider:                      buildProvider,
		MidConversationSystemProvider: capabilityProvider,
	})
}

func typedMidConversationResponsesRequest(provider schemas.ModelProvider, model string) *schemas.BifrostResponsesRequest {
	return &schemas.BifrostResponsesRequest{
		Provider: provider,
		Model:    model,
		Input: []schemas.ResponsesMessage{
			{Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser), Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hello")}},
			{Role: schemas.Ptr(schemas.ResponsesInputMessageRoleSystem), Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("policy")}},
			{Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant), Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("ok")}},
		},
	}
}

func typedMidConversationChatRequest(provider schemas.ModelProvider, model string) *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: provider,
		Model:    model,
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}},
			{Role: schemas.ChatMessageRoleSystem, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("policy")}},
			{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("ok")}},
		},
	}
}

func TestAnthropicRequestBuilders_MidConversationFormatCompatibility(t *testing.T) {
	const (
		formatNative  = "native"
		formatInline  = "inline"
		formatHoisted = "hoisted"
	)

	type testCase struct {
		name               string
		requestProvider    schemas.ModelProvider
		buildProvider      schemas.ModelProvider
		capabilityProvider schemas.ModelProvider
		model              string
		canonicalModel     string
		modelFamily        schemas.ModelFamily
		wantFormat         string
	}

	customAnthropic := schemas.ModelProvider("Qiniu-Anthropic")
	tests := []testCase{
		// Official transports and the exact model allowlist documented by Anthropic.
		{name: "anthropic-opus-4.8", requestProvider: schemas.Anthropic, buildProvider: schemas.Anthropic, capabilityProvider: schemas.Anthropic, model: "claude-opus-4-8", wantFormat: formatNative},
		{name: "anthropic-sonnet-5", requestProvider: schemas.Anthropic, buildProvider: schemas.Anthropic, capabilityProvider: schemas.Anthropic, model: "claude-sonnet-5", wantFormat: formatNative},
		{name: "vertex-mythos-5", requestProvider: schemas.Vertex, buildProvider: schemas.Vertex, capabilityProvider: schemas.Vertex, model: "claude-mythos-5", wantFormat: formatNative},
		{name: "bedrock-mantle-fable-5", requestProvider: schemas.BedrockMantle, buildProvider: schemas.BedrockMantle, capabilityProvider: schemas.BedrockMantle, model: "anthropic.claude-fable-5", wantFormat: formatNative},

		// Claude transports not listed by the feature docs retain the safe reminder form.
		{name: "classic-bedrock", requestProvider: schemas.Bedrock, buildProvider: schemas.Bedrock, capabilityProvider: schemas.Bedrock, model: "anthropic.claude-opus-4-8", wantFormat: formatInline},
		{name: "azure-foundry", requestProvider: schemas.Azure, buildProvider: schemas.Azure, capabilityProvider: schemas.Azure, model: "claude-opus-4-8", wantFormat: formatInline},

		// A custom Anthropic-compatible endpoint must fail closed even when its base
		// request shape is Anthropic and its model name matches the official allowlist.
		{name: "custom-anthropic", requestProvider: customAnthropic, buildProvider: schemas.Anthropic, capabilityProvider: customAnthropic, model: "claude-opus-4-8", wantFormat: formatInline},
		{name: "custom-anthropic-opaque-alias", requestProvider: customAnthropic, buildProvider: schemas.Anthropic, capabilityProvider: customAnthropic, model: "opaque-deployment", canonicalModel: "claude-sonnet-5", modelFamily: schemas.ModelFamilyAnthropic, wantFormat: formatInline},

		// Other Anthropic-compatible providers keep the historical top-level hoist
		// for non-Claude models. If they host Claude without declaring native support,
		// they get the cache-preserving reminder rather than an unsupported wire role.
		{name: "deepseek-non-claude", requestProvider: schemas.DeepSeek, buildProvider: schemas.DeepSeek, capabilityProvider: schemas.DeepSeek, model: "deepseek-chat", wantFormat: formatHoisted},
		{name: "fireworks-non-claude", requestProvider: schemas.Fireworks, buildProvider: schemas.Fireworks, capabilityProvider: schemas.Fireworks, model: "accounts/fireworks/models/llama-v3p1-8b-instruct", wantFormat: formatHoisted},
		{name: "sgl-compatible-claude", requestProvider: schemas.SGL, buildProvider: schemas.SGL, capabilityProvider: schemas.SGL, model: "claude-opus-4-8", wantFormat: formatInline},
	}

	newContext := func(tc testCase) *schemas.BifrostContext {
		ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
		if tc.canonicalModel != "" || tc.modelFamily != "" {
			alias := &schemas.AliasConfig{ModelID: tc.model}
			if tc.canonicalModel != "" {
				alias.ModelName = schemas.Ptr(tc.canonicalModel)
			}
			if tc.modelFamily != "" {
				alias.ModelFamily = schemas.Ptr(tc.modelFamily)
			}
			ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{Key: tc.model, Config: alias})
		}
		return ctx
	}

	assertFormat := func(t *testing.T, body []byte, want string) {
		t.Helper()
		switch want {
		case formatNative:
			if got := providerUtils.GetJSONField(body, "messages.1.role").String(); got != "system" {
				t.Fatalf("expected native mid-conversation system role, got %q in %s", got, body)
			}
			if providerUtils.JSONFieldExists(body, "system") {
				t.Fatalf("native mid-conversation system must not be hoisted: %s", body)
			}
		case formatInline:
			if got := providerUtils.GetJSONField(body, "messages.1.role").String(); got != "user" {
				t.Fatalf("expected inline user reminder, got role %q in %s", got, body)
			}
			if got := providerUtils.GetJSONField(body, "messages.1.content.0.text").String(); got != "<system-reminder>\npolicy\n</system-reminder>\n" {
				t.Fatalf("unexpected inline reminder %q in %s", got, body)
			}
			if providerUtils.JSONFieldExists(body, "system") {
				t.Fatalf("inline mid-conversation system must not be hoisted: %s", body)
			}
		case formatHoisted:
			if got := providerUtils.GetJSONField(body, "messages.1.role").String(); got != "assistant" {
				t.Fatalf("expected system message removal before assistant, got role %q in %s", got, body)
			}
			system := providerUtils.GetJSONField(body, "system")
			if got := system.String(); got != "policy" && system.Get("0.text").String() != "policy" {
				t.Fatalf("expected historical top-level system hoist, got %q in %s", got, body)
			}
		default:
			t.Fatalf("unknown expected format %q", want)
		}
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, rawPath := range []struct {
				name  string
				build func(*schemas.BifrostContext) ([]byte, *schemas.BifrostError)
			}{
				{
					name: "raw-responses",
					build: func(ctx *schemas.BifrostContext) ([]byte, *schemas.BifrostError) {
						return buildRawAnthropicResponsesBodyWithCapability(t, ctx, tc.requestProvider, tc.buildProvider, tc.capabilityProvider, tc.model,
							`{"model":"alias","max_tokens":128,"messages":[{"role":"user","content":"hello"},{"role":"system","content":"policy"},{"role":"assistant","content":"ok"}]}`)
					},
				},
				{
					name: "raw-chat",
					build: func(ctx *schemas.BifrostContext) ([]byte, *schemas.BifrostError) {
						return buildRawAnthropicChatBodyWithCapability(t, ctx, tc.requestProvider, tc.buildProvider, tc.capabilityProvider, tc.model,
							`{"model":"alias","max_tokens":128,"messages":[{"role":"user","content":"hello"},{"role":"system","content":"policy"},{"role":"assistant","content":"ok"}]}`)
					},
				},
			} {
				t.Run(rawPath.name, func(t *testing.T) {
					body, err := rawPath.build(newContext(tc))
					if err != nil {
						t.Fatalf("unexpected raw builder error: %v", err)
					}
					assertFormat(t, body, tc.wantFormat)
				})
			}

			t.Run("responses", func(t *testing.T) {
				body, err := BuildAnthropicResponsesRequestBody(newContext(tc), typedMidConversationResponsesRequest(tc.requestProvider, tc.model), AnthropicRequestBuildConfig{
					Provider:                      tc.buildProvider,
					MidConversationSystemProvider: tc.capabilityProvider,
				})
				if err != nil {
					t.Fatalf("unexpected responses builder error: %v", err)
				}
				assertFormat(t, body, tc.wantFormat)
			})

			t.Run("chat", func(t *testing.T) {
				body, err := BuildAnthropicChatRequestBody(newContext(tc), typedMidConversationChatRequest(tc.requestProvider, tc.model), AnthropicRequestBuildConfig{
					Provider:                      tc.buildProvider,
					MidConversationSystemProvider: tc.capabilityProvider,
				})
				if err != nil {
					t.Fatalf("unexpected chat builder error: %v", err)
				}
				assertFormat(t, body, tc.wantFormat)
			})
		})
	}
}

func TestAnthropicTypedConverters_CustomProviderFailsClosed(t *testing.T) {
	customProvider := schemas.ModelProvider("Qiniu-Anthropic")
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyBaseProviderType, schemas.Anthropic)

	responses, err := ToAnthropicResponsesRequest(ctx, typedMidConversationResponsesRequest(customProvider, "claude-sonnet-5"))
	if err != nil {
		t.Fatalf("unexpected responses conversion error: %v", err)
	}
	if got := responses.Messages[1].Role; got != AnthropicMessageRoleUser {
		t.Fatalf("custom responses converter must fail closed, got role %q", got)
	}

	chat, err := ToAnthropicChatRequest(ctx, typedMidConversationChatRequest(customProvider, "claude-sonnet-5"))
	if err != nil {
		t.Fatalf("unexpected chat conversion error: %v", err)
	}
	if got := chat.Messages[1].Role; got != AnthropicMessageRoleUser {
		t.Fatalf("custom chat converter must fail closed, got role %q", got)
	}
}

func TestBuildAnthropicChatRequestBody_ToolResultAllowsNativeMidConversationSystem(t *testing.T) {
	toolCallID := "toolu_weather"
	toolName := "get_weather"
	req := &schemas.BifrostChatRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-opus-4-8",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Check the weather")}},
			{
				Role: schemas.ChatMessageRoleAssistant,
				ChatAssistantMessage: &schemas.ChatAssistantMessage{ToolCalls: []schemas.ChatAssistantMessageToolCall{{
					ID: schemas.Ptr(toolCallID),
					Function: schemas.ChatAssistantMessageToolCallFunction{
						Name:      schemas.Ptr(toolName),
						Arguments: `{"city":"NYC"}`,
					},
				}}},
			},
			{
				Role:            schemas.ChatMessageRoleTool,
				ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr(toolCallID)},
				Content:         &schemas.ChatMessageContent{ContentStr: schemas.Ptr("68F")},
			},
			{Role: schemas.ChatMessageRoleSystem, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Use Celsius from now on")}},
			{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Understood")}},
		},
	}

	body, err := BuildAnthropicChatRequestBody(
		schemas.NewBifrostContext(context.Background(), time.Time{}),
		req,
		AnthropicRequestBuildConfig{
			Provider:                      schemas.Anthropic,
			MidConversationSystemProvider: schemas.Anthropic,
		},
	)
	if err != nil {
		t.Fatalf("unexpected chat builder error: %v", err)
	}

	messages := providerUtils.GetJSONField(body, "messages").Array()
	if len(messages) != 5 {
		t.Fatalf("expected five converted messages, got %d in %s", len(messages), body)
	}
	if messages[2].Get("role").String() != "user" || messages[2].Get("content.0.type").String() != "tool_result" {
		t.Fatalf("tool output was not converted to an Anthropic user turn: %s", body)
	}
	if messages[3].Get("role").String() != "system" {
		t.Fatalf("expected native system role after converted tool-result user turn: %s", body)
	}
}

func TestBuildAnthropicResponsesRequestBody_RawMidConversationSystemParity(t *testing.T) {
	newContext := func() *schemas.BifrostContext {
		return schemas.NewBifrostContext(context.Background(), time.Time{})
	}

	t.Run("unsupported Claude models use the shared inline reminder", func(t *testing.T) {
		for _, model := range []string{"claude-sonnet-4-6", "claude-opus-4-7"} {
			t.Run(model, func(t *testing.T) {
				result, err := buildRawAnthropicResponsesBody(t, newContext(), schemas.Anthropic, schemas.Anthropic, model,
					`{"model":"alias","max_tokens":128,"messages":[{"role":"user","content":"hello"},{"role":"system","content":"policy"},{"role":"assistant","content":"ok"}]}`)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				message := providerUtils.GetJSONField(result, "messages.1")
				if message.Get("role").String() != "user" {
					t.Fatalf("expected inline user reminder, got %s", result)
				}
				if got := message.Get("content.0.text").String(); got != "<system-reminder>\npolicy\n</system-reminder>\n" {
					t.Fatalf("unexpected reminder %q in %s", got, result)
				}
				if providerUtils.JSONFieldExists(result, "system") {
					t.Fatalf("mid-conversation fallback must not rewrite top-level system: %s", result)
				}
			})
		}
	})

	t.Run("documented models keep only legal native placement", func(t *testing.T) {
		valid, err := buildRawAnthropicResponsesBody(t, newContext(), schemas.Anthropic, schemas.Anthropic, "claude-opus-4-8",
			`{"max_tokens":128,"messages":[{"role":"user","content":"hello"},{"role":"system","content":"policy","extension":"keep"},{"role":"assistant","content":"ok"}]}`)
		if err != nil {
			t.Fatalf("unexpected valid-placement error: %v", err)
		}
		if role := providerUtils.GetJSONField(valid, "messages.1.role").String(); role != "system" {
			t.Fatalf("expected native system role, got %q in %s", role, valid)
		}
		if got := providerUtils.GetJSONField(valid, "messages.1.extension").String(); got != "keep" {
			t.Fatalf("unknown raw field was lost: %s", valid)
		}

		invalid, err := buildRawAnthropicResponsesBody(t, newContext(), schemas.Anthropic, schemas.Anthropic, "claude-opus-4-8",
			`{"max_tokens":128,"messages":[{"role":"user","content":"hello"},{"role":"system","content":"policy"},{"role":"user","content":"continue"}]}`)
		if err != nil {
			t.Fatalf("unexpected invalid-placement error: %v", err)
		}
		if role := providerUtils.GetJSONField(invalid, "messages.1.role").String(); role != "user" {
			t.Fatalf("expected invalid placement to inline, got %s", invalid)
		}
	})

	t.Run("documented cloud transports keep native system role", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			provider schemas.ModelProvider
			model    string
		}{
			{"vertex", schemas.Vertex, "claude-opus-4-8"},
			{"bedrock-mantle", schemas.BedrockMantle, "anthropic.claude-opus-4-8"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				result, err := buildRawAnthropicResponsesBody(t, newContext(), tc.provider, tc.provider, tc.model,
					`{"max_tokens":128,"messages":[{"role":"user","content":"hello"},{"role":"system","content":"policy"},{"role":"assistant","content":"ok"}]}`)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if role := providerUtils.GetJSONField(result, "messages.1.role").String(); role != "system" {
					t.Fatalf("expected native system role, got %q in %s", role, result)
				}
			})
		}
	})

	t.Run("non-native transports inline instead of sending an invalid role", func(t *testing.T) {
		for _, provider := range []schemas.ModelProvider{schemas.Bedrock, schemas.Azure} {
			t.Run(string(provider), func(t *testing.T) {
				result, err := buildRawAnthropicResponsesBody(t, newContext(), provider, provider, "claude-opus-4-8",
					`{"max_tokens":128,"messages":[{"role":"user","content":"hello"},{"role":"system","content":"policy"},{"role":"assistant","content":"ok"}]}`)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if role := providerUtils.GetJSONField(result, "messages.1.role").String(); role != "user" {
					t.Fatalf("expected inline fallback, got %q in %s", role, result)
				}
			})
		}
	})

	t.Run("rewriting one system turn preserves untouched message bytes", func(t *testing.T) {
		userMessage := `{"role":"user","content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}}],"id":"u1"}`
		assistantMessage := `{"role":"assistant","content":"ok","id":"a1"}`
		result, err := buildRawAnthropicResponsesBody(t, newContext(), schemas.Anthropic, schemas.Anthropic, "claude-sonnet-4-6",
			`{"max_tokens":128,"messages":[`+userMessage+`,{"role":"system","content":"policy"},`+assistantMessage+`]}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		messages := providerUtils.GetJSONField(result, "messages").Array()
		if len(messages) != 3 {
			t.Fatalf("expected three messages, got %d: %s", len(messages), result)
		}
		if messages[0].Raw != userMessage {
			t.Fatalf("user message bytes changed:\n got: %s\nwant: %s", messages[0].Raw, userMessage)
		}
		if messages[2].Raw != assistantMessage {
			t.Fatalf("assistant message bytes changed:\n got: %s\nwant: %s", messages[2].Raw, assistantMessage)
		}
	})

	t.Run("developer role converts to native system when legal", func(t *testing.T) {
		result, err := buildRawAnthropicResponsesBody(t, newContext(), schemas.Anthropic, schemas.Anthropic, "claude-opus-4-8",
			`{"max_tokens":128,"messages":[{"role":"user","content":"hello"},{"role":"developer","content":"policy","extension":7},{"role":"assistant","content":"ok"}]}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if providerUtils.GetJSONField(result, "messages.1.role").String() != "system" || providerUtils.GetJSONField(result, "messages.1.extension").Int() != 7 {
			t.Fatalf("developer conversion did not preserve raw fields: %s", result)
		}
	})

	t.Run("custom provider uses actual Anthropic build capabilities", func(t *testing.T) {
		custom := schemas.ModelProvider("Qiniu-Anthropic")
		result, err := buildRawAnthropicResponsesBody(t, newContext(), custom, schemas.Anthropic, "claude-opus-4-8",
			`{"max_tokens":128,"messages":[{"role":"user","content":"hello"},{"role":"system","content":"policy"},{"role":"assistant","content":"ok"}]}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if providerUtils.GetJSONField(result, "messages.1.role").String() != "system" {
			t.Fatalf("expected native system role from build provider capabilities: %s", result)
		}
	})

	t.Run("leading system is hoisted while later system is inlined", func(t *testing.T) {
		result, err := buildRawAnthropicResponsesBody(t, newContext(), schemas.Anthropic, schemas.Anthropic, "claude-sonnet-4-6",
			`{"max_tokens":128,"system":"existing","messages":[{"role":"developer","content":"leading"},{"role":"user","content":"hello"},{"role":"system","content":"later"},{"role":"assistant","content":"ok"}]}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		system := providerUtils.GetJSONField(result, "system").Array()
		if len(system) != 2 || system[0].Get("text").String() != "existing" || system[1].Get("text").String() != "leading" {
			t.Fatalf("unexpected hoisted system content: %s", result)
		}
		if providerUtils.GetJSONField(result, "messages.1.role").String() != "user" || !strings.Contains(providerUtils.GetJSONField(result, "messages.1.content.0.text").String(), "later") {
			t.Fatalf("later system was not kept inline: %s", result)
		}
	})

	t.Run("non Claude Anthropic-shape models retain top-level hoist", func(t *testing.T) {
		result, err := buildRawAnthropicResponsesBody(t, newContext(), schemas.SGL, schemas.SGL, "deepseek-v3",
			`{"max_tokens":128,"messages":[{"role":"user","content":"hello"},{"role":"system","content":"policy"},{"role":"assistant","content":"ok"}]}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(providerUtils.GetJSONField(result, "messages").Array()) != 2 || providerUtils.GetJSONField(result, "system").String() != "policy" {
			t.Fatalf("expected historical top-level hoist for non-Claude model: %s", result)
		}
	})

	t.Run("block fallback preserves only the final effective cache breakpoint", func(t *testing.T) {
		result, err := buildRawAnthropicResponsesBody(t, newContext(), schemas.Anthropic, schemas.Anthropic, "claude-sonnet-4-6",
			`{"max_tokens":128,"messages":[{"role":"user","content":"hello"},{"role":"system","content":[{"type":"text","text":"one","cache_control":{"type":"ephemeral"}},{"type":"text","text":"two","cache_control":{"type":"ephemeral","ttl":"1h"}}]},{"role":"assistant","content":"ok"}]}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		blocks := providerUtils.GetJSONField(result, "messages.1.content").Array()
		if len(blocks) != 2 || blocks[0].Get("cache_control").Exists() || blocks[1].Get("cache_control.ttl").String() != "1h" {
			t.Fatalf("cache breakpoint behavior diverged from the shared helper: %s", result)
		}
	})

	t.Run("officially valid consecutive group stays native", func(t *testing.T) {
		result, err := buildRawAnthropicResponsesBody(t, newContext(), schemas.Anthropic, schemas.Anthropic, "claude-opus-4-8",
			`{"max_tokens":128,"messages":[{"role":"user","content":"hello"},{"role":"system","content":"one"},{"role":"system","content":"two"},{"role":"assistant","content":"ok"}]}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if providerUtils.GetJSONField(result, "messages.1.role").String() != "system" || providerUtils.GetJSONField(result, "messages.2.role").String() != "system" {
			t.Fatalf("expected consecutive native system section: %s", result)
		}
	})

	t.Run("assistant server-tool result exception stays native", func(t *testing.T) {
		result, err := buildRawAnthropicResponsesBody(t, newContext(), schemas.Anthropic, schemas.Anthropic, "claude-opus-4-8",
			`{"max_tokens":128,"messages":[{"role":"assistant","content":[{"type":"server_tool_use","id":"srv_1","name":"web_search","input":{}},{"type":"web_search_tool_result","tool_use_id":"srv_1","content":[]}]},{"role":"system","content":"policy"}]}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if providerUtils.GetJSONField(result, "messages.1.role").String() != "system" {
			t.Fatalf("expected native system after server tool result: %s", result)
		}
	})

	t.Run("bare server-tool use does not satisfy the completed-result exception", func(t *testing.T) {
		result, err := buildRawAnthropicResponsesBody(t, newContext(), schemas.Anthropic, schemas.Anthropic, "claude-opus-4-8",
			`{"max_tokens":128,"messages":[{"role":"assistant","content":[{"type":"server_tool_use","id":"srv_1","name":"web_search","input":{}}]},{"role":"system","content":"policy"}]}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if providerUtils.GetJSONField(result, "messages.1.role").String() != "user" {
			t.Fatalf("expected inline fallback after incomplete server tool use: %s", result)
		}
	})

	t.Run("ordinary assistant does not satisfy the server-tool exception", func(t *testing.T) {
		result, err := buildRawAnthropicResponsesBody(t, newContext(), schemas.Anthropic, schemas.Anthropic, "claude-opus-4-8",
			`{"max_tokens":128,"messages":[{"role":"assistant","content":"ordinary"},{"role":"system","content":"policy"}]}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if providerUtils.GetJSONField(result, "messages.1.role").String() != "user" {
			t.Fatalf("expected inline fallback after ordinary assistant: %s", result)
		}
	})

	t.Run("alias canonical model drives native support", func(t *testing.T) {
		ctx := newContext()
		ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{
			Key: "best-claude",
			Config: &schemas.AliasConfig{
				ModelID:   "opaque-deployment",
				ModelName: schemas.Ptr("claude-opus-4-8"),
			},
		})
		result, err := buildRawAnthropicResponsesBody(t, ctx, schemas.Anthropic, schemas.Anthropic, "best-claude",
			`{"max_tokens":128,"messages":[{"role":"user","content":"hello"},{"role":"system","content":"policy"},{"role":"assistant","content":"ok"}]}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if providerUtils.GetJSONField(result, "messages.1.role").String() != "system" {
			t.Fatalf("canonical model was not used for capability gating: %s", result)
		}
	})

	t.Run("system-only request remains a valid user message", func(t *testing.T) {
		result, err := buildRawAnthropicResponsesBody(t, newContext(), schemas.Anthropic, schemas.Anthropic, "claude-sonnet-4-6",
			`{"max_tokens":128,"system":"existing","messages":[{"role":"system","content":"one","extension":"keep"},{"role":"developer","content":"two"}]}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		messages := providerUtils.GetJSONField(result, "messages").Array()
		if len(messages) != 1 || messages[0].Get("role").String() != "user" || messages[0].Get("extension").String() != "keep" {
			t.Fatalf("unexpected system-only normalization: %s", result)
		}
		blocks := messages[0].Get("content").Array()
		if len(blocks) != 2 || blocks[0].Get("text").String() != "one" || blocks[1].Get("text").String() != "two" {
			t.Fatalf("system-only content was not combined: %s", result)
		}
		if providerUtils.GetJSONField(result, "system").String() != "existing" {
			t.Fatalf("existing top-level system was changed: %s", result)
		}
	})

	t.Run("unsupported scalar content is a bad request", func(t *testing.T) {
		builders := []struct {
			name  string
			build func(string) ([]byte, *schemas.BifrostError)
		}{
			{
				name: "raw_responses",
				build: func(rawBody string) ([]byte, *schemas.BifrostError) {
					return buildRawAnthropicResponsesBody(t, newContext(), schemas.Anthropic, schemas.Anthropic, "claude-sonnet-4-6", rawBody)
				},
			},
			{
				name: "raw_chat",
				build: func(rawBody string) ([]byte, *schemas.BifrostError) {
					return buildRawAnthropicChatBodyWithCapability(t, newContext(), schemas.Anthropic, schemas.Anthropic, schemas.Anthropic, "claude-sonnet-4-6", rawBody)
				},
			},
		}
		inputs := []struct {
			name string
			raw  string
		}{
			{name: "mid_conversation_system", raw: `{"max_tokens":128,"messages":[{"role":"user","content":"hello"},{"role":"system","content":123}]}`},
			{name: "mid_conversation_developer", raw: `{"max_tokens":128,"messages":[{"role":"user","content":"hello"},{"role":"developer","content":true}]}`},
			{name: "system_only_system", raw: `{"max_tokens":128,"messages":[{"role":"system","content":123}]}`},
			{name: "system_only_developer", raw: `{"max_tokens":128,"messages":[{"role":"developer","content":true}]}`},
		}

		for _, input := range inputs {
			t.Run(input.name, func(t *testing.T) {
				for _, builder := range builders {
					t.Run(builder.name, func(t *testing.T) {
						body, err := builder.build(input.raw)
						if body != nil {
							t.Fatalf("body = %s, want nil on validation failure", body)
						}
						if err == nil || err.Error == nil {
							t.Fatalf("expected scalar-content validation error, got %v", err)
						}
						if !strings.Contains(err.Error.Message, "unsupported raw system content") {
							t.Fatalf("error message = %q, want unsupported raw system content", err.Error.Message)
						}
						if err.StatusCode == nil || *err.StatusCode != 400 {
							t.Fatalf("status code = %v, want 400", err.StatusCode)
						}
						if err.Error.Type == nil || *err.Error.Type != "invalid_request_error" {
							t.Fatalf("error type = %v, want invalid_request_error", err.Error.Type)
						}
					})
				}
			})
		}
	})
}

func TestAnthropicRequestBuilders_ToolUseSystemToolResultCompatibility(t *testing.T) {
	const model = "claude-opus-4-8"
	const rawBody = `{"max_tokens":128,"messages":[{"role":"user","content":"run it"},{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"lookup","input":{"q":"x"}}]},{"role":"system","content":"keep the answer concise"},{"role":"user","opaque":{"z":2,"a":1},"content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"done","extension":{"b":2,"a":1}}]}]}`

	assertWire := func(t *testing.T, body []byte, preserveRaw bool) {
		t.Helper()
		messages := providerUtils.GetJSONField(body, "messages").Array()
		if len(messages) != 3 {
			t.Fatalf("expected system section to merge into the tool-result turn, got %d messages: %s", len(messages), body)
		}
		if got := messages[1].Get("role").String(); got != "assistant" {
			t.Fatalf("message[1] role = %q, want assistant: %s", got, body)
		}
		assistantBlocks := messages[1].Get("content").Array()
		if len(assistantBlocks) == 0 || assistantBlocks[len(assistantBlocks)-1].Get("type").String() != "tool_use" {
			t.Fatalf("assistant turn must end in tool_use: %s", body)
		}
		result := messages[2]
		if got := result.Get("role").String(); got != "user" {
			t.Fatalf("message[2] role = %q, want user: %s", got, body)
		}
		if got := result.Get("content.0.type").String(); got != "tool_result" {
			t.Fatalf("tool_result must remain the first user content block, got %q: %s", got, body)
		}
		if got := result.Get("content.1.type").String(); got != "text" {
			t.Fatalf("reminder must follow tool_result as a text block, got %q: %s", got, body)
		}
		if got := result.Get("content.1.text").String(); got != "<system-reminder>\nkeep the answer concise\n</system-reminder>\n" {
			t.Fatalf("unexpected inline reminder %q: %s", got, body)
		}
		for i, msg := range messages {
			if msg.Get("role").String() == "system" || msg.Get("role").String() == "developer" {
				t.Fatalf("message[%d] retained an invalid native system role: %s", i, body)
			}
		}
		if preserveRaw {
			if got := result.Get("opaque.z").Int(); got != 2 {
				t.Fatalf("raw user-message extension was lost: %s", body)
			}
			if got := result.Get("content.0.extension.b").Int(); got != 2 {
				t.Fatalf("raw tool_result extension was lost: %s", body)
			}
			if !strings.Contains(string(body), `"opaque":{"z":2,"a":1}`) || !strings.Contains(string(body), `"extension":{"b":2,"a":1}`) {
				t.Fatalf("untouched raw field ordering changed: %s", body)
			}
		}
	}

	newContext := func() *schemas.BifrostContext {
		return schemas.NewBifrostContext(context.Background(), time.Time{})
	}
	buildConfig := AnthropicRequestBuildConfig{
		Provider:                      schemas.Anthropic,
		MidConversationSystemProvider: schemas.Anthropic,
	}

	t.Run("raw-chat", func(t *testing.T) {
		body, err := buildRawAnthropicChatBodyWithCapability(t, newContext(), schemas.Anthropic, schemas.Anthropic, schemas.Anthropic, model, rawBody)
		if err != nil {
			t.Fatalf("unexpected raw chat build error: %v", err)
		}
		assertWire(t, body, true)
	})

	t.Run("raw-responses", func(t *testing.T) {
		body, err := buildRawAnthropicResponsesBodyWithCapability(t, newContext(), schemas.Anthropic, schemas.Anthropic, schemas.Anthropic, model, rawBody)
		if err != nil {
			t.Fatalf("unexpected raw responses build error: %v", err)
		}
		assertWire(t, body, true)
	})

	t.Run("typed-chat", func(t *testing.T) {
		body, err := BuildAnthropicChatRequestBody(newContext(), &schemas.BifrostChatRequest{
			Provider: schemas.Anthropic,
			Model:    model,
			Input: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("run it")}},
				{
					Role: schemas.ChatMessageRoleAssistant,
					ChatAssistantMessage: &schemas.ChatAssistantMessage{ToolCalls: []schemas.ChatAssistantMessageToolCall{{
						ID: schemas.Ptr("toolu_1"),
						Function: schemas.ChatAssistantMessageToolCallFunction{
							Name:      schemas.Ptr("lookup"),
							Arguments: `{"q":"x"}`,
						},
					}}},
				},
				{Role: schemas.ChatMessageRoleSystem, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("keep the answer concise")}},
				{
					Role:            schemas.ChatMessageRoleTool,
					ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr("toolu_1")},
					Content:         &schemas.ChatMessageContent{ContentStr: schemas.Ptr("done")},
				},
			},
		}, buildConfig)
		if err != nil {
			t.Fatalf("unexpected typed chat build error: %v", err)
		}
		assertWire(t, body, false)
	})

	t.Run("typed-responses", func(t *testing.T) {
		body, err := BuildAnthropicResponsesRequestBody(newContext(), &schemas.BifrostResponsesRequest{
			Provider: schemas.Anthropic,
			Model:    model,
			Input: []schemas.ResponsesMessage{
				{Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser), Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("run it")}},
				{
					Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID:    schemas.Ptr("toolu_1"),
						Name:      schemas.Ptr("lookup"),
						Arguments: schemas.Ptr(`{"q":"x"}`),
					},
				},
				{Role: schemas.Ptr(schemas.ResponsesInputMessageRoleSystem), Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("keep the answer concise")}},
				{
					Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID: schemas.Ptr("toolu_1"),
						Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: schemas.Ptr("done")},
					},
				},
			},
		}, buildConfig)
		if err != nil {
			t.Fatalf("unexpected typed responses build error: %v", err)
		}
		assertWire(t, body, false)
	})
}

func TestConvertBifrostResponses_ToolBoundaryReminderOutputVariants(t *testing.T) {
	const model = "claude-opus-4-8"

	functionPair := func() (schemas.ResponsesMessage, schemas.ResponsesMessage) {
		return schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:    schemas.Ptr("toolu_function"),
					Name:      schemas.Ptr("lookup"),
					Arguments: schemas.Ptr(`{"q":"x"}`),
				},
			}, schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: schemas.Ptr("toolu_function"),
					Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: schemas.Ptr("done")},
				},
			}
	}
	computerPair := func() (schemas.ResponsesMessage, schemas.ResponsesMessage) {
		return schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeComputerCall),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: schemas.Ptr("toolu_computer"),
					Name:   schemas.Ptr(string(AnthropicToolNameComputer)),
				},
			}, schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeComputerCallOutput),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: schemas.Ptr("toolu_computer"),
					Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: schemas.Ptr("done")},
				},
			}
	}
	mcpPair := func() (schemas.ResponsesMessage, schemas.ResponsesMessage) {
		return schemas.ResponsesMessage{
				ID:   schemas.Ptr("toolu_mcp"),
				Type: schemas.Ptr(schemas.ResponsesMessageTypeMCPCall),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					Name:      schemas.Ptr("remote_lookup"),
					Arguments: schemas.Ptr(`{"q":"x"}`),
				},
			}, schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeMCPCall),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: schemas.Ptr("toolu_mcp"),
					Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: schemas.Ptr("done")},
				},
			}
	}

	for _, tc := range []struct {
		name           string
		pair           func() (schemas.ResponsesMessage, schemas.ResponsesMessage)
		wantResultType AnthropicContentBlockType
	}{
		{name: "function_call_output", pair: functionPair, wantResultType: AnthropicContentBlockTypeToolResult},
		{name: "computer_call_output", pair: computerPair, wantResultType: AnthropicContentBlockTypeToolResult},
		{name: "mcp_call_result", pair: mcpPair, wantResultType: AnthropicContentBlockTypeMCPToolResult},
	} {
		t.Run(tc.name, func(t *testing.T) {
			call, output := tc.pair()
			messages, system := ConvertBifrostMessagesToAnthropicMessages(
				schemas.NewBifrostContext(context.Background(), time.Time{}),
				[]schemas.ResponsesMessage{
					{Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser), Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("run it")}},
					call,
					{Role: schemas.Ptr(schemas.ResponsesInputMessageRoleSystem), Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("policy")}},
					output,
				},
				true,
				schemas.ResolveModelCaps(schemas.Anthropic, model),
			)
			if system != nil {
				t.Fatalf("tool-boundary system message must not be hoisted: %#v", system)
			}
			if len(messages) != 3 {
				t.Fatalf("expected user, assistant tool use, and merged user result; got %d messages: %#v", len(messages), messages)
			}
			blocks := messages[2].Content.ContentBlocks
			if len(blocks) != 2 {
				t.Fatalf("expected result plus reminder, got %d blocks: %#v", len(blocks), blocks)
			}
			if blocks[0].Type != tc.wantResultType {
				t.Fatalf("first block type = %q, want %q", blocks[0].Type, tc.wantResultType)
			}
			if blocks[1].Type != AnthropicContentBlockTypeText || blocks[1].Text == nil || *blocks[1].Text != "<system-reminder>\npolicy\n</system-reminder>\n" {
				t.Fatalf("unexpected reminder block: %#v", blocks[1])
			}
		})
	}
}

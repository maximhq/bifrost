package saladcloud

import (
	"context"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestPrepareSaladCloudChatRequestDefaultsThinkingOffWithoutMutatingOriginal(t *testing.T) {
	original := &schemas.BifrostChatRequest{
		Provider: schemas.SaladCloud,
		Model:    "qwen3.6-35b-a3b",
		Params: &schemas.ChatParameters{
			ExtraParams: map[string]interface{}{"temperature_floor": 0.1},
		},
	}

	prepared := prepareSaladCloudChatRequest(original)
	if prepared == original {
		t.Fatal("expected a cloned request")
	}
	if prepared.Params == original.Params {
		t.Fatal("expected cloned params")
	}
	if _, exists := original.Params.ExtraParams[saladCloudChatTemplateKwargsKey]; exists {
		t.Fatal("original request was mutated")
	}

	kwargs, ok := prepared.Params.ExtraParams[saladCloudChatTemplateKwargsKey].(map[string]interface{})
	if !ok {
		t.Fatalf("expected %s map, got %#v", saladCloudChatTemplateKwargsKey, prepared.Params.ExtraParams[saladCloudChatTemplateKwargsKey])
	}
	if enabled, ok := kwargs["enable_thinking"].(bool); !ok || enabled {
		t.Fatalf("expected enable_thinking=false, got %#v", kwargs["enable_thinking"])
	}

	prepared.Params.ExtraParams["temperature_floor"] = 0.2
	if original.Params.ExtraParams["temperature_floor"] != 0.1 {
		t.Fatal("prepared request extra params share the original map")
	}
}

func TestPrepareSaladCloudChatRequestEnablesThinkingFromReasoning(t *testing.T) {
	prepared := prepareSaladCloudChatRequest(&schemas.BifrostChatRequest{
		Provider: schemas.SaladCloud,
		Model:    "qwen3.6-35b-a3b",
		Params: &schemas.ChatParameters{
			Reasoning: &schemas.ChatReasoning{
				Effort: schemas.Ptr("high"),
			},
		},
	})

	kwargs := prepared.Params.ExtraParams[saladCloudChatTemplateKwargsKey].(map[string]interface{})
	if enabled, ok := kwargs["enable_thinking"].(bool); !ok || !enabled {
		t.Fatalf("expected enable_thinking=true, got %#v", kwargs["enable_thinking"])
	}
	if prepared.Params.Reasoning != nil {
		t.Fatal("expected OpenAI-style reasoning params to be cleared after SaladCloud mapping")
	}
}

func TestPrepareSaladCloudChatRequestUsesSaladThinkingJSONShape(t *testing.T) {
	prepared := prepareSaladCloudChatRequest(&schemas.BifrostChatRequest{
		Provider: schemas.SaladCloud,
		Model:    "qwen3.6-35b-a3b",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("think"),
				},
			},
		},
		Params: &schemas.ChatParameters{
			Reasoning: &schemas.ChatReasoning{
				Effort: schemas.Ptr("high"),
			},
		},
	})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
	body, bifrostErr := providerUtils.CheckContextAndGetRequestBody(ctx, prepared, func() (providerUtils.RequestBodyWithExtraParams, error) {
		return openai.ToOpenAIChatRequest(ctx, prepared), nil
	})
	if bifrostErr != nil {
		t.Fatalf("failed to build request body: %v", bifrostErr)
	}

	bodyText := string(body)
	if strings.Contains(bodyText, "reasoning_effort") {
		t.Fatalf("did not expect OpenAI reasoning_effort in SaladCloud request body: %s", bodyText)
	}
	if !strings.Contains(bodyText, `"chat_template_kwargs"`) || !strings.Contains(bodyText, `"enable_thinking": true`) {
		t.Fatalf("expected SaladCloud thinking kwargs in request body: %s", bodyText)
	}
}

func TestPrepareSaladCloudChatRequestPreservesExplicitTemplateKwargs(t *testing.T) {
	customKwargs := map[string]interface{}{"enable_thinking": true}
	prepared := prepareSaladCloudChatRequest(&schemas.BifrostChatRequest{
		Provider: schemas.SaladCloud,
		Model:    "qwen3.6-35b-a3b",
		Params: &schemas.ChatParameters{
			ExtraParams: map[string]interface{}{
				saladCloudChatTemplateKwargsKey: customKwargs,
			},
			Reasoning: &schemas.ChatReasoning{
				Enabled: schemas.Ptr(false),
			},
		},
	})

	kwargs, ok := prepared.Params.ExtraParams[saladCloudChatTemplateKwargsKey].(map[string]interface{})
	if !ok {
		t.Fatalf("expected explicit chat_template_kwargs map, got %#v", prepared.Params.ExtraParams[saladCloudChatTemplateKwargsKey])
	}
	if enabled, ok := kwargs["enable_thinking"].(bool); !ok || !enabled {
		t.Fatalf("expected explicit enable_thinking=true to be preserved, got %#v", kwargs["enable_thinking"])
	}
	if prepared.Params.Reasoning != nil {
		t.Fatal("expected OpenAI-style reasoning params to be cleared when custom kwargs are present")
	}
}

func TestEnableSaladCloudExtraParamPassthroughRestoresPreviousValue(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, false)

	restore := enableSaladCloudExtraParamPassthrough(ctx)
	if value := ctx.Value(schemas.BifrostContextKeyPassthroughExtraParams); value != true {
		t.Fatalf("expected passthrough to be enabled, got %#v", value)
	}

	restore()
	if value := ctx.Value(schemas.BifrostContextKeyPassthroughExtraParams); value != false {
		t.Fatalf("expected previous passthrough value to be restored, got %#v", value)
	}
}

func TestNormalizeSaladCloudChatResponsePromotesReasoningWhenContentIsEmpty(t *testing.T) {
	reasoning := "The answer is 42."
	response := &schemas.BifrostChatResponse{
		Choices: []schemas.BifrostResponseChoice{
			{
				ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
					Message: &schemas.ChatMessage{
						ChatAssistantMessage: &schemas.ChatAssistantMessage{Reasoning: &reasoning},
					},
				},
			},
		},
	}

	normalizeSaladCloudChatResponse(response)

	content := response.Choices[0].Message.Content
	if content == nil || content.ContentStr == nil || *content.ContentStr != reasoning {
		t.Fatalf("expected reasoning to be promoted to content, got %#v", content)
	}
	if response.Choices[0].Message.ChatAssistantMessage.Reasoning == nil {
		t.Fatal("expected reasoning field to be preserved")
	}
}

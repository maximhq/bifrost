package anthropic

import (
	"context"
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// TestDeepSeekV4FlashUsesOutputConfigEffort verifies that chat and Responses
// requests, including forced-tool variants, use effort without legacy thinking.
func TestDeepSeekV4FlashUsesOutputConfigEffort(t *testing.T) {
	t.Run("chat", func(t *testing.T) {
		ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
		defer cancel()

		effort := "medium"
		maxTokens := 4096
		req, err := ToAnthropicChatRequest(ctx, &schemas.BifrostChatRequest{
			Provider: schemas.DeepSeek,
			Model:    "deepseek-v4-flash",
			Input: []schemas.ChatMessage{{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")},
			}},
			Params: &schemas.ChatParameters{
				Reasoning: &schemas.ChatReasoning{Effort: &effort, MaxTokens: &maxTokens},
			},
		})
		if err != nil {
			t.Fatalf("ToAnthropicChatRequest: %v", err)
		}
		assertDeepSeekEffortOnly(t, req, effort)
	})

	t.Run("responses alias", func(t *testing.T) {
		ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
		defer cancel()
		ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{
			Key: "workhorse",
			Config: &schemas.AliasConfig{
				ModelID: "deepseek-v4-flash",
			},
		})

		effort := "medium"
		maxTokens := 4096
		req, err := ToAnthropicResponsesRequest(ctx, &schemas.BifrostResponsesRequest{
			Provider: schemas.DeepSeek,
			Model:    "workhorse",
			Params: &schemas.ResponsesParameters{
				Reasoning: &schemas.ResponsesParametersReasoning{Effort: &effort, MaxTokens: &maxTokens},
			},
		})
		if err != nil {
			t.Fatalf("ToAnthropicResponsesRequest: %v", err)
		}
		assertDeepSeekEffortOnly(t, req, effort)
	})

	t.Run("chat forced tool", func(t *testing.T) {
		ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
		defer cancel()

		effort := "medium"
		req, err := ToAnthropicChatRequest(ctx, &schemas.BifrostChatRequest{
			Provider: schemas.DeepSeek,
			Model:    "deepseek-v4-flash",
			Input: []schemas.ChatMessage{{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")},
			}},
			Params: &schemas.ChatParameters{
				Reasoning: &schemas.ChatReasoning{Effort: &effort},
				ToolChoice: &schemas.ChatToolChoice{ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
					Type:     schemas.ChatToolChoiceTypeFunction,
					Function: &schemas.ChatToolChoiceFunction{Name: "lookup"},
				}},
			},
		})
		if err != nil {
			t.Fatalf("ToAnthropicChatRequest: %v", err)
		}
		if req.ToolChoice == nil || req.ToolChoice.Type != "tool" || req.ToolChoice.Name != "lookup" {
			t.Fatalf("forced tool choice not preserved: %#v", req.ToolChoice)
		}
		assertDeepSeekEffortOnly(t, req, effort)
	})

	t.Run("responses forced tool", func(t *testing.T) {
		ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
		defer cancel()

		effort := "medium"
		req, err := ToAnthropicResponsesRequest(ctx, &schemas.BifrostResponsesRequest{
			Provider: schemas.DeepSeek,
			Model:    "deepseek-v4-flash",
			Params: &schemas.ResponsesParameters{
				Reasoning: &schemas.ResponsesParametersReasoning{Effort: &effort},
				ToolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{
					Type: schemas.ResponsesToolChoiceTypeFunction,
					Name: schemas.Ptr("lookup"),
				}},
			},
		})
		if err != nil {
			t.Fatalf("ToAnthropicResponsesRequest: %v", err)
		}
		if req.ToolChoice == nil || req.ToolChoice.Type != "tool" || req.ToolChoice.Name != "lookup" {
			t.Fatalf("forced tool choice not preserved: %#v", req.ToolChoice)
		}
		assertDeepSeekEffortOnly(t, req, effort)
	})
}

// TestDeepSeekV4FlashEffortGateIsExact verifies that near-miss models and
// providers retain the stock effort behavior.
func TestDeepSeekV4FlashEffortGateIsExact(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider schemas.ModelProvider
		model    string
	}{
		{name: "dated model", provider: schemas.DeepSeek, model: "deepseek-v4-flash-0731"},
		{name: "case variant", provider: schemas.DeepSeek, model: "DeepSeek-V4-Flash"},
		{name: "other provider", provider: schemas.Anthropic, model: "deepseek-v4-flash"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
			defer cancel()

			effort := "medium"
			req, err := ToAnthropicResponsesRequest(ctx, &schemas.BifrostResponsesRequest{
				Provider: tc.provider,
				Model:    tc.model,
				Params: &schemas.ResponsesParameters{
					Reasoning: &schemas.ResponsesParametersReasoning{Effort: &effort},
				},
			})
			if err != nil {
				t.Fatalf("ToAnthropicResponsesRequest: %v", err)
			}
			if req.OutputConfig != nil && req.OutputConfig.Effort != nil {
				t.Fatalf("near miss emitted output_config.effort=%q", *req.OutputConfig.Effort)
			}
		})
	}
}

// TestBuildDeepSeekV4FlashRequestBodyPreservesEffort verifies the final wire
// body retains output_config.effort and omits unsupported thinking metadata.
func TestBuildDeepSeekV4FlashRequestBodyPreservesEffort(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	effort := "medium"
	body, bifrostErr := BuildAnthropicResponsesRequestBody(ctx, &schemas.BifrostResponsesRequest{
		Provider: schemas.DeepSeek,
		Model:    "deepseek-v4-flash",
		Params: &schemas.ResponsesParameters{
			Reasoning: &schemas.ResponsesParametersReasoning{Effort: &effort},
		},
	}, AnthropicRequestBuildConfig{Provider: schemas.DeepSeek})
	if bifrostErr != nil {
		t.Fatalf("BuildAnthropicResponsesRequestBody: %v", bifrostErr)
	}
	if got := providerUtils.GetJSONField(body, "output_config.effort").String(); got != effort {
		t.Fatalf("output_config.effort = %q, want %q; body=%s", got, effort, body)
	}
	if providerUtils.JSONFieldExists(body, "thinking") {
		t.Fatalf("DeepSeek wire body gained unsupported thinking: %s", body)
	}
}

// assertDeepSeekEffortOnly checks the shared effort-only request invariant.
func assertDeepSeekEffortOnly(t *testing.T, req *AnthropicMessageRequest, want string) {
	t.Helper()
	if req.OutputConfig == nil || req.OutputConfig.Effort == nil || *req.OutputConfig.Effort != want {
		t.Fatalf("output_config.effort = %#v, want %q", req.OutputConfig, want)
	}
	if req.Thinking != nil {
		t.Fatalf("DeepSeek request gained unsupported thinking: %#v", req.Thinking)
	}
}

package compat

import (
	"context"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

type testAccount struct {
	configs map[schemas.ModelProvider]*schemas.ProviderConfig
}

func (a *testAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return nil, nil
}

func (a *testAccount) GetKeysForProvider(context.Context, schemas.ModelProvider) ([]schemas.Key, error) {
	return nil, nil
}

func (a *testAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	if a == nil {
		return nil, nil
	}
	return a.configs[provider], nil
}

func newCompatPluginForCountTokensFallback(t *testing.T, account schemas.Account) *CompatPlugin {
	t.Helper()

	plugin, err := Init(Config{CountTokensFallback: true}, bifrost.NewNoOpLogger(), nil, account)
	if err != nil {
		t.Fatalf("init compat plugin: %v", err)
	}
	return plugin
}

func newCountTokensRequest(provider schemas.ModelProvider, model string, fallbacks ...schemas.Fallback) *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		RequestType: schemas.CountTokensRequest,
		CountTokensRequest: &schemas.BifrostResponsesRequest{
			Provider:  provider,
			Model:     model,
			Fallbacks: fallbacks,
			Input: []schemas.ResponsesMessage{
				{
					Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
					Content: &schemas.ResponsesMessageContent{
						ContentStr: schemas.Ptr("Hello, how are you?"),
					},
				},
			},
		},
	}
}

func TestCompatPluginCountTokensFallbackRecoversUnsupportedOperation(t *testing.T) {
	plugin := newCompatPluginForCountTokensFallback(t, nil)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := newCountTokensRequest(schemas.Azure, "azure/gpt-4o-mini")

	if _, _, err := plugin.PreLLMHook(ctx, req); err != nil {
		t.Fatalf("pre hook: %v", err)
	}

	resp, bifrostErr, err := plugin.PostLLMHook(ctx, nil, unsupportedOperationErr())
	if err != nil {
		t.Fatalf("post hook returned plugin error: %v", err)
	}
	if bifrostErr != nil {
		t.Fatalf("expected fallback response, got error: %#v", bifrostErr)
	}
	if resp == nil || resp.CountTokensResponse == nil {
		t.Fatalf("expected count tokens response, got %#v", resp)
	}
	if resp.CountTokensResponse.InputTokens != 4 {
		t.Fatalf("expected estimated input_tokens 4, got %d", resp.CountTokensResponse.InputTokens)
	}
}

func TestCompatPluginCountTokensFallbackPreservesConfiguredFallbacksUntilLastAttempt(t *testing.T) {
	plugin := newCompatPluginForCountTokensFallback(t, nil)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := newCountTokensRequest(schemas.Azure, "azure/gpt-4o-mini", schemas.Fallback{Provider: schemas.OpenAI, Model: "gpt-4o-mini"})

	if _, _, err := plugin.PreLLMHook(ctx, req); err != nil {
		t.Fatalf("pre hook: %v", err)
	}

	resp, bifrostErr, err := plugin.PostLLMHook(ctx, nil, unsupportedOperationErr())
	if err != nil {
		t.Fatalf("post hook returned plugin error: %v", err)
	}
	if resp != nil {
		t.Fatalf("expected no synthesized response before trying real fallbacks, got %#v", resp)
	}
	if bifrostErr == nil || bifrostErr.Error == nil || bifrostErr.Error.Code == nil || *bifrostErr.Error.Code != "unsupported_operation" {
		t.Fatalf("expected unsupported_operation to preserve configured fallbacks, got %#v", bifrostErr)
	}

	ctx.SetValue(schemas.BifrostContextKeyFallbackIndex, 1)
	resp, bifrostErr, err = plugin.PostLLMHook(ctx, nil, unsupportedOperationErr())
	if err != nil {
		t.Fatalf("post hook returned plugin error on last attempt: %v", err)
	}
	if bifrostErr != nil {
		t.Fatalf("expected synthesized response on last fallback attempt, got error: %#v", bifrostErr)
	}
	if resp == nil || resp.CountTokensResponse == nil {
		t.Fatalf("expected synthesized response on last fallback attempt, got %#v", resp)
	}
}

func TestCompatPluginCountTokensFallbackRespectsExplicitDisallow(t *testing.T) {
	account := &testAccount{
		configs: map[schemas.ModelProvider]*schemas.ProviderConfig{
			schemas.ModelProvider("custom-mistral"): {
				CustomProviderConfig: &schemas.CustomProviderConfig{
					CustomProviderKey: "custom-mistral",
					BaseProviderType:  schemas.Mistral,
					AllowedRequests:   &schemas.AllowedRequests{},
				},
			},
		},
	}
	plugin := newCompatPluginForCountTokensFallback(t, account)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := newCountTokensRequest(schemas.ModelProvider("custom-mistral"), "custom-mistral/mistral-small-latest")

	if _, _, err := plugin.PreLLMHook(ctx, req); err != nil {
		t.Fatalf("pre hook: %v", err)
	}

	resp, bifrostErr, err := plugin.PostLLMHook(ctx, nil, unsupportedOperationErr())
	if err != nil {
		t.Fatalf("post hook returned plugin error: %v", err)
	}
	if resp != nil {
		t.Fatalf("expected no synthesized response when count_tokens is disallowed, got %#v", resp)
	}
	if bifrostErr == nil || bifrostErr.Error == nil || bifrostErr.Error.Code == nil || *bifrostErr.Error.Code != "unsupported_operation" {
		t.Fatalf("expected unsupported_operation when count_tokens is disallowed, got %#v", bifrostErr)
	}
}

func TestBuildGracefulCountTokensFallbackResponseTracksMixedInputDetails(t *testing.T) {
	request := &schemas.BifrostResponsesRequest{
		Model: "azure/gpt-4o-mini",
		Input: []schemas.ResponsesMessage{
			{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{
						{
							Type: schemas.ResponsesInputMessageContentBlockTypeText,
							Text: schemas.Ptr("hello world"),
						},
						{
							Type: schemas.ResponsesInputMessageContentBlockTypeImage,
							ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{
								ImageURL: schemas.Ptr("https://example.com/image.png"),
							},
						},
						{
							Type: schemas.ResponsesInputMessageContentBlockTypeAudio,
							Audio: &schemas.ResponsesInputMessageContentBlockAudio{
								Format: "wav",
								Data:   "abcdefgh",
							},
						},
					},
				},
			},
		},
	}

	response := buildGracefulCountTokensFallbackResponse(request)
	if response == nil {
		t.Fatal("expected graceful fallback response")
	}
	if response.InputTokens != 402 {
		t.Fatalf("expected input tokens 402, got %d", response.InputTokens)
	}
	if response.TotalTokens == nil || *response.TotalTokens != 402 {
		t.Fatalf("expected total tokens 402, got %#v", response.TotalTokens)
	}
	if response.InputTokensDetails == nil {
		t.Fatal("expected input token details")
	}
	if response.InputTokensDetails.TextTokens != 18 {
		t.Fatalf("expected text tokens 18, got %d", response.InputTokensDetails.TextTokens)
	}
	if response.InputTokensDetails.ImageTokens != 256 {
		t.Fatalf("expected image tokens 256, got %d", response.InputTokensDetails.ImageTokens)
	}
	if response.InputTokensDetails.AudioTokens != 128 {
		t.Fatalf("expected audio tokens 128, got %d", response.InputTokensDetails.AudioTokens)
	}
}

func unsupportedOperationErr() *schemas.BifrostError {
	return &schemas.BifrostError{
		Error: &schemas.ErrorField{
			Code:    schemas.Ptr("unsupported_operation"),
			Message: "count_tokens is not supported",
		},
	}
}

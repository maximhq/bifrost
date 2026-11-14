package bifrost

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A custom provider surfaces its own key wherever a ModelProvider is reported, so
// GetProviderKey() answers "my-anthropic", not schemas.Anthropic. Every capability
// lookup that takes a raw provider key therefore has to resolve through the base
// provider Bifrost records on the context, or the switch in ModelSupportsPromptCaching
// falls through to its default and injection silently never runs for these providers.
const customAnthropicProviderKey = schemas.ModelProvider("my-anthropic")

const customOpenAIProviderKey = schemas.ModelProvider("my-openai")

func baseProviderCtx(base schemas.ModelProvider) *schemas.BifrostContext {
	return schemas.NewBifrostContextWithValue(context.Background(), schemas.NoDeadline, schemas.BifrostContextKeyBaseProviderType, base)
}

func autoInjectingConfig() *schemas.ProviderConfig {
	return &schemas.ProviderConfig{PromptCache: &schemas.PromptCacheConfig{AutoInject: true}}
}

func responsesReqFor(model, text string) *schemas.BifrostResponsesRequest {
	return &schemas.BifrostResponsesRequest{
		Model: model,
		Input: []schemas.ResponsesMessage{{
			Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{
				ContentBlocks: []schemas.ResponsesMessageContentBlock{{
					Type: schemas.ResponsesInputMessageContentBlockTypeText,
					Text: schemas.Ptr(text),
				}},
			},
		}},
	}
}

func chatReqFor(model, text string) *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Model: model,
		Input: []schemas.ChatMessage{{
			Role: schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{
				ContentBlocks: []schemas.ChatContentBlock{{
					Type: schemas.ChatContentBlockTypeText,
					Text: schemas.Ptr(text),
				}},
			},
		}},
	}
}

func TestPromptCacheResponsesRequest_CustomProviderResolvesBaseProvider(t *testing.T) {
	ctx := baseProviderCtx(schemas.Anthropic)
	req := responsesReqFor("claude-sonnet-4", "stable prefix")

	out := promptCacheResponsesRequest(ctx, autoInjectingConfig(), customAnthropicProviderKey, req)

	require.NotNil(t, out)
	require.NotNil(t, out.Input[0].Content.ContentBlocks[0].CacheControl,
		"a custom provider wrapping Anthropic must receive injected breakpoints")
	assert.Equal(t, schemas.CacheControlTypeEphemeral, out.Input[0].Content.ContentBlocks[0].CacheControl.Type)
	assert.Nil(t, req.Input[0].Content.ContentBlocks[0].CacheControl,
		"the shared request must not be written through")
}

func TestPromptCacheChatRequest_CustomProviderResolvesBaseProvider(t *testing.T) {
	ctx := baseProviderCtx(schemas.Anthropic)
	req := chatReqFor("claude-sonnet-4", "stable prefix")

	out := promptCacheChatRequest(ctx, autoInjectingConfig(), customAnthropicProviderKey, req)

	require.NotNil(t, out)
	require.NotNil(t, out.Input[0].Content.ContentBlocks[0].CacheControl,
		"a custom provider wrapping Anthropic must receive injected breakpoints")
	assert.Nil(t, req.Input[0].Content.ContentBlocks[0].CacheControl,
		"the shared request must not be written through")
}

// Resolving the base provider must not turn the capability gate into a rubber stamp.
// A custom provider wrapping OpenAI on a model with no per-block marker support is
// still left alone, exactly as the built-in OpenAI key would be.
func TestPromptCacheResponsesRequest_BaseResolutionStillHonoursCapability(t *testing.T) {
	ctx := baseProviderCtx(schemas.OpenAI)
	req := responsesReqFor("gpt-4o", "stable prefix")

	out := promptCacheResponsesRequest(ctx, autoInjectingConfig(), customOpenAIProviderKey, req)

	require.NotNil(t, out)
	assert.Nil(t, out.Input[0].Content.ContentBlocks[0].CacheControl,
		"a model without explicit caching must not be marked")
}

// With no base provider on the context (direct calls, tests), the passed key stands.
func TestPromptCacheResponsesRequest_NoBaseProviderOnContextUsesPassedKey(t *testing.T) {
	req := responsesReqFor("claude-sonnet-4", "stable prefix")

	out := promptCacheResponsesRequest(nil, autoInjectingConfig(), schemas.Anthropic, req)

	require.NotNil(t, out)
	require.NotNil(t, out.Input[0].Content.ContentBlocks[0].CacheControl)
}

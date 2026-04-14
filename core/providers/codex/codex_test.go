package codex

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func TestEnsureCodexResponseDefaults_StripsUnsupportedParams(t *testing.T) {
	temperature := 0.2
	topP := 0.5
	maxOutputTokens := 32
	promptCacheKey := "keep-me"
	store := true
	request := &schemas.BifrostResponsesRequest{
		Params: &schemas.ResponsesParameters{
			Temperature:     &temperature,
			TopP:            &topP,
			MaxOutputTokens: &maxOutputTokens,
			PromptCacheKey:  &promptCacheKey,
			Store:           &store,
			ExtraParams: map[string]interface{}{
				"temperature":         0.2,
				"top_p":               0.5,
				"max_output_tokens":   32,
				"presence_penalty":    0,
				"frequency_penalty":   0,
				"parallel_tool_calls": true,
			},
		},
	}

	ensureCodexResponseDefaults(nil, request)

	require.NotNil(t, request.Params)
	require.Nil(t, request.Params.Temperature)
	require.Nil(t, request.Params.TopP)
	require.Nil(t, request.Params.MaxOutputTokens)
	require.NotNil(t, request.Params.Store)
	require.False(t, *request.Params.Store)
	require.NotNil(t, request.Params.PromptCacheKey)
	require.Equal(t, promptCacheKey, *request.Params.PromptCacheKey)
	require.NotContains(t, request.Params.ExtraParams, "temperature")
	require.NotContains(t, request.Params.ExtraParams, "top_p")
	require.NotContains(t, request.Params.ExtraParams, "max_output_tokens")
	require.NotContains(t, request.Params.ExtraParams, "presence_penalty")
	require.NotContains(t, request.Params.ExtraParams, "frequency_penalty")
	require.Contains(t, request.Params.ExtraParams, "parallel_tool_calls")
}

func TestEnsureCodexResponseDefaults_AddsInstructionsWhenMissing(t *testing.T) {
	request := &schemas.BifrostResponsesRequest{}

	ensureCodexResponseDefaults(nil, request)

	require.NotNil(t, request.Params)
	require.NotNil(t, request.Params.Instructions)
	require.Equal(t, defaultInstructions, *request.Params.Instructions)
}

func TestNormalizeCodexInput_PreservesAssistantOutputText(t *testing.T) {
	userRole := schemas.ResponsesInputMessageRoleUser
	assistantRole := schemas.ResponsesInputMessageRoleAssistant
	userText := "user text"
	assistantText := "assistant text"
	request := &schemas.BifrostResponsesRequest{
		Input: []schemas.ResponsesMessage{
			{
				Role:    &userRole,
				Content: &schemas.ResponsesMessageContent{ContentStr: &userText},
			},
			{
				Role:    &assistantRole,
				Content: &schemas.ResponsesMessageContent{ContentStr: &assistantText},
			},
		},
	}

	normalizeCodexInput(request)

	require.Len(t, request.Input, 2)
	require.Len(t, request.Input[0].Content.ContentBlocks, 1)
	require.Equal(t, schemas.ResponsesInputMessageContentBlockTypeText, request.Input[0].Content.ContentBlocks[0].Type)
	require.Len(t, request.Input[1].Content.ContentBlocks, 1)
	require.Equal(t, schemas.ResponsesOutputMessageContentTypeText, request.Input[1].Content.ContentBlocks[0].Type)
	require.NotNil(t, request.Input[1].Content.ContentBlocks[0].ResponsesOutputMessageContentText)
}

func TestCodexResponsesAccumulator_UsesCompletedItems(t *testing.T) {
	assistantRole := schemas.ResponsesInputMessageRoleAssistant
	itemType := schemas.ResponsesMessageTypeMessage
	text := "hello"
	status := "completed"
	accumulator := newCodexResponsesAccumulator("gpt-5.4-mini")
	accumulator.add(&schemas.BifrostResponsesStreamResponse{
		Type:        schemas.ResponsesStreamResponseTypeOutputItemDone,
		OutputIndex: schemas.Ptr(0),
		Item: &schemas.ResponsesMessage{
			Type:   &itemType,
			Role:   &assistantRole,
			Status: &status,
			Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{{
				Type: schemas.ResponsesOutputMessageContentTypeText,
				Text: &text,
			}}},
		},
	})
	accumulator.add(&schemas.BifrostResponsesStreamResponse{
		Type:     schemas.ResponsesStreamResponseTypeCompleted,
		Response: &schemas.BifrostResponsesResponse{Model: "gpt-5.4-mini", Object: "response", Status: schemas.Ptr("completed")},
	})

	response := accumulator.response()
	require.NotNil(t, response)
	require.Equal(t, "gpt-5.4-mini", response.Model)
	require.Len(t, response.Output, 1)
	require.NotNil(t, response.Output[0].Content)
	require.Len(t, response.Output[0].Content.ContentBlocks, 1)
	require.Equal(t, "hello", *response.Output[0].Content.ContentBlocks[0].Text)
}

func TestPersistRefreshedCredentials_UsesContextPersister(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	called := false
	ctx.SetValue(schemas.BifrostContextKeyCodexCredentialPersister, schemas.CodexCredentialPersister(func(keyID string, keyConfig *schemas.CodexKeyConfig) error {
		called = true
		require.Equal(t, "key-1", keyID)
		require.NotNil(t, keyConfig)
		require.Equal(t, "refresh-1", keyConfig.RefreshToken.GetValue())
		require.NotNil(t, keyConfig.AccessToken)
		require.Equal(t, "access-1", keyConfig.AccessToken.GetValue())
		require.NotNil(t, keyConfig.AccountID)
		require.Equal(t, "acct-1", keyConfig.AccountID.GetValue())
		return nil
	}))
	provider := &CodexProvider{}
	key := schemas.Key{ID: "key-1", CodexKeyConfig: &schemas.CodexKeyConfig{RefreshToken: *schemas.NewEnvVar("refresh-1"), AuthMethod: schemas.CodexAuthMethodDevice}}

	provider.persistRefreshedCredentials(ctx, key, &TokenResponse{AccessToken: "access-1", RefreshToken: "", ExpiresIn: 60}, "acct-1")

	require.True(t, called)
}

func TestCodexChatStreamState_ConvertsResponsesDeltasToChatChunks(t *testing.T) {
	state := newCodexChatStreamState(nil, "codex/gpt-5.4-mini")
	assistantRole := schemas.ResponsesInputMessageRoleAssistant
	messageType := schemas.ResponsesMessageTypeMessage
	text := "hi"
	chunks := state.convert(&schemas.BifrostResponsesStreamResponse{
		Type: schemas.ResponsesStreamResponseTypeOutputItemAdded,
		Item: &schemas.ResponsesMessage{Role: &assistantRole, Type: &messageType},
	})
	require.Len(t, chunks, 1)
	require.NotNil(t, chunks[0].Choices[0].ChatStreamResponseChoice)
	require.Equal(t, "assistant", *chunks[0].Choices[0].ChatStreamResponseChoice.Delta.Role)

	chunks = state.convert(&schemas.BifrostResponsesStreamResponse{Type: schemas.ResponsesStreamResponseTypeOutputTextDelta, Delta: &text})
	require.Len(t, chunks, 1)
	require.Equal(t, "hi", *chunks[0].Choices[0].ChatStreamResponseChoice.Delta.Content)

	completed := state.convert(&schemas.BifrostResponsesStreamResponse{
		Type:     schemas.ResponsesStreamResponseTypeCompleted,
		Response: &schemas.BifrostResponsesResponse{Usage: &schemas.ResponsesResponseUsage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3}},
	})
	require.Len(t, completed, 1)
	require.NotNil(t, completed[0].Choices[0].FinishReason)
	require.Equal(t, "stop", *completed[0].Choices[0].FinishReason)
	require.NotNil(t, completed[0].Usage)
	require.Equal(t, 3, completed[0].Usage.TotalTokens)
}

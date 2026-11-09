package evals

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func chatReply(text, finishReason string, usage *schemas.BifrostLLMUsage) *schemas.BifrostChatResponse {
	return &schemas.BifrostChatResponse{
		Choices: []schemas.BifrostResponseChoice{{
			FinishReason: &finishReason,
			ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
				Message: &schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: &text}},
			},
		}},
		Usage: usage,
	}
}

func bifrostError(message string) *schemas.BifrostError {
	return &schemas.BifrostError{Error: &schemas.ErrorField{Message: message}}
}

func TestBifrostJudge_BuildsATaggedPinnedRequest(t *testing.T) {
	var gotCtx *schemas.BifrostContext
	var gotReq *schemas.BifrostChatRequest
	chat := func(ctx *schemas.BifrostContext, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		gotCtx, gotReq = ctx, req
		return chatReply(`{"score":true,"reasoning":"ok"}`, "stop", &schemas.BifrostLLMUsage{
			PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110, Cost: &schemas.BifrostCost{TotalCost: 0.002},
		}), nil
	}
	prepared := false
	judge := NewBifrostJudge(chat, nil, BifrostJudgeOptions{PrepareContext: func(_ context.Context, bctx *schemas.BifrostContext) {
		prepared = bctx != nil
	}})

	resp, err := judge(WithKeyID(context.Background(), "key-evals"), JudgeRequest{
		Provider: "openai",
		Model:    "gpt-4o-mini",
		System:   "grade it",
		User:     `{{output}} = "hi"`,
		Params:   map[string]any{"max_completion_tokens": float64(300), "temperature": 0.0},
		Schema:   map[string]any{"type": "object"},
	})
	require.NoError(t, err)

	assert.Equal(t, `{"score":true,"reasoning":"ok"}`, resp.Text)
	assert.False(t, resp.Truncated)
	assert.Equal(t, &Usage{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110}, resp.Usage)
	require.NotNil(t, resp.Cost)
	assert.InDelta(t, 0.002, *resp.Cost, 1e-12)

	require.NotNil(t, gotReq)
	assert.Equal(t, schemas.ModelProvider("openai"), gotReq.Provider)
	assert.Equal(t, "gpt-4o-mini", gotReq.Model)
	require.Len(t, gotReq.Input, 2)
	assert.Equal(t, schemas.ChatMessageRoleSystem, gotReq.Input[0].Role)
	assert.Equal(t, "grade it", *gotReq.Input[0].Content.ContentStr)
	assert.Equal(t, schemas.ChatMessageRoleUser, gotReq.Input[1].Role)
	assert.Equal(t, `{{output}} = "hi"`, *gotReq.Input[1].Content.ContentStr)
	require.NotNil(t, gotReq.Params)
	require.NotNil(t, gotReq.Params.MaxCompletionTokens)
	assert.Equal(t, 300, *gotReq.Params.MaxCompletionTokens)
	require.NotNil(t, gotReq.Params.ResponseFormat)
	format, ok := (*gotReq.Params.ResponseFormat).(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "json_schema", format["type"])

	// The call is pinned to the eval key and tagged, so it is logged and governed like
	// any request but can be told apart from production traffic.
	assert.Equal(t, "key-evals", gotCtx.Value(schemas.BifrostContextKeyAPIKeyID))
	headers, ok := gotCtx.Value(schemas.BifrostContextKeyRequestHeaders).(map[string]string)
	require.True(t, ok)
	assert.Equal(t, JudgeUserAgent, headers["user-agent"])
	assert.True(t, prepared, "the caller's hook can add what its deployment needs")
}

// A model call with no eval key would land on whatever key routing picks, which is
// exactly the production traffic the eval flag exists to protect.
func TestBifrostJudge_RefusesToRunWithoutAnEvalKey(t *testing.T) {
	called := false
	chat := func(*schemas.BifrostContext, *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		called = true
		return chatReply("{}", "stop", nil), nil
	}
	_, err := NewBifrostJudge(chat, nil, BifrostJudgeOptions{})(context.Background(), JudgeRequest{Provider: "openai", Model: "gpt-4o-mini"})
	require.ErrorIs(t, err, ErrNoEvalKey)
	assert.False(t, called)
}

func TestBifrostJudge_PlainTextRequestHasNoResponseFormat(t *testing.T) {
	var gotReq *schemas.BifrostChatRequest
	chat := func(_ *schemas.BifrostContext, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		gotReq = req
		return chatReply("fine", "stop", nil), nil
	}
	_, err := NewBifrostJudge(chat, nil, BifrostJudgeOptions{})(WithKeyID(context.Background(), "k"), JudgeRequest{Provider: "openai", Model: "m"})
	require.NoError(t, err)
	assert.True(t, gotReq.Params == nil || gotReq.Params.ResponseFormat == nil)
}

func TestBifrostJudge_OmitsAnEmptySystemMessage(t *testing.T) {
	var gotReq *schemas.BifrostChatRequest
	chat := func(_ *schemas.BifrostContext, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		gotReq = req
		return chatReply("fine", "stop", nil), nil
	}
	_, err := NewBifrostJudge(chat, nil, BifrostJudgeOptions{})(WithKeyID(context.Background(), "k"), JudgeRequest{Provider: "openai", Model: "m", User: "the whole prompt"})
	require.NoError(t, err)
	require.Len(t, gotReq.Input, 1)
	assert.Equal(t, schemas.ChatMessageRoleUser, gotReq.Input[0].Role)
	assert.Equal(t, "the whole prompt", *gotReq.Input[0].Content.ContentStr)
}

func TestBifrostJudge_ReportsTruncation(t *testing.T) {
	chat := func(*schemas.BifrostContext, *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		return chatReply(`{"score": tr`, "length", nil), nil
	}
	resp, err := NewBifrostJudge(chat, nil, BifrostJudgeOptions{})(WithKeyID(context.Background(), "k"), JudgeRequest{Provider: "openai", Model: "m"})
	require.NoError(t, err)
	assert.True(t, resp.Truncated)
}

func TestBifrostJudge_ReturnsProviderErrors(t *testing.T) {
	chat := func(*schemas.BifrostContext, *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		return nil, bifrostError("Invalid parameter: response_format json_schema is not supported")
	}
	_, err := NewBifrostJudge(chat, nil, BifrostJudgeOptions{})(WithKeyID(context.Background(), "k"), JudgeRequest{Provider: "openai", Model: "m"})
	require.Error(t, err)
	assert.True(t, isStructuredOutputRejection(err), "the provider's message survives so the evaluator can retry as plain text")
}

// Some reasoning models only serve this request shape on the Responses API.
func TestBifrostJudge_FallsBackToResponsesAPI(t *testing.T) {
	chat := func(*schemas.BifrostContext, *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		return nil, bifrostError("This model is only supported in /v1/responses and not in /v1/chat/completions.")
	}
	var responsesReq *schemas.BifrostResponsesRequest
	responses := func(_ *schemas.BifrostContext, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		responsesReq = req
		return nil, bifrostError("responses backend reached")
	}
	_, err := NewBifrostJudge(chat, responses, BifrostJudgeOptions{})(WithKeyID(context.Background(), "k"), JudgeRequest{Provider: "openai", Model: "o3", System: "s", User: "u"})

	require.NotNil(t, responsesReq, "the Responses API was tried")
	assert.Equal(t, "o3", responsesReq.Model)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "responses backend reached", "the Responses error is the informative one")
}

func TestBifrostJudge_AppliesItsTimeout(t *testing.T) {
	chat := func(ctx *schemas.BifrostContext, _ *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > time.Minute {
			return nil, bifrostError("no deadline on the request")
		}
		return chatReply("ok", "stop", nil), nil
	}
	_, err := NewBifrostJudge(chat, nil, BifrostJudgeOptions{Timeout: 5 * time.Second})(WithKeyID(context.Background(), "k"), JudgeRequest{Provider: "openai", Model: "m"})
	require.NoError(t, err)
}

func TestBifrostEmbedder(t *testing.T) {
	var gotCtx *schemas.BifrostContext
	var gotReq *schemas.BifrostEmbeddingRequest
	embed := func(ctx *schemas.BifrostContext, req *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		gotCtx, gotReq = ctx, req
		return &schemas.BifrostEmbeddingResponse{
			// Returned out of order on purpose.
			Data: []schemas.EmbeddingData{
				{Index: 1, Embedding: schemas.EmbeddingStruct{EmbeddingArray: []float64{3, 4}}},
				{Index: 0, Embedding: schemas.EmbeddingStruct{EmbeddingArray: []float64{1, 2}}},
			},
			Usage: &schemas.BifrostLLMUsage{PromptTokens: 9, TotalTokens: 9, Cost: &schemas.BifrostCost{TotalCost: 0.0001}},
		}, nil
	}
	embedder := NewBifrostEmbedder(embed, BifrostJudgeOptions{})

	resp, err := embedder(WithKeyID(context.Background(), "key-evals"), "openai", "text-embedding-3-small", []string{"first", "second"})
	require.NoError(t, err)
	assert.Equal(t, [][]float64{{1, 2}, {3, 4}}, resp.Vectors, "vectors come back in input order")
	assert.Equal(t, &Usage{PromptTokens: 9, TotalTokens: 9}, resp.Usage)
	require.NotNil(t, resp.Cost)

	assert.Equal(t, schemas.ModelProvider("openai"), gotReq.Provider)
	assert.Equal(t, "text-embedding-3-small", gotReq.Model)
	require.Len(t, gotReq.Input, 2)
	assert.Equal(t, "second", *gotReq.Input[1].Content[0].Text)
	assert.Equal(t, "key-evals", gotCtx.Value(schemas.BifrostContextKeyAPIKeyID))
	headers, _ := gotCtx.Value(schemas.BifrostContextKeyRequestHeaders).(map[string]string)
	assert.Equal(t, JudgeUserAgent, headers["user-agent"])

	_, err = embedder(context.Background(), "openai", "m", []string{"x"})
	require.ErrorIs(t, err, ErrNoEvalKey, "an embedding call without an eval key is refused")

	failing := NewBifrostEmbedder(func(*schemas.BifrostContext, *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		return nil, bifrostError("quota exceeded")
	}, BifrostJudgeOptions{})
	_, err = failing(WithKeyID(context.Background(), "k"), "openai", "m", []string{"x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "quota exceeded")
}

// An evaluator encodes its model parameters once. The judge uses that encoding, and
// every call still gets parameters of its own.
func TestBifrostJudge_UsesParametersEncodedOnce(t *testing.T) {
	params := map[string]any{"temperature": 0.25, "max_completion_tokens": 64}
	encoded := encodeJudgeParams(params)
	require.JSONEq(t, `{"temperature":0.25,"max_completion_tokens":64}`, string(encoded))
	assert.Nil(t, encodeJudgeParams(nil))

	first, err := buildJudgeChatRequest(JudgeRequest{Provider: "openai", Model: "gpt-4o", User: "u", Params: params, ParamsJSON: encoded})
	require.NoError(t, err)
	second, err := buildJudgeChatRequest(JudgeRequest{Provider: "openai", Model: "gpt-4o", User: "u", Params: params, ParamsJSON: encoded})
	require.NoError(t, err)
	require.NotNil(t, first.Params.Temperature)
	assert.InDelta(t, 0.25, *first.Params.Temperature, 1e-9)
	assert.NotSame(t, first.Params, second.Params)

	plain, err := buildJudgeChatRequest(JudgeRequest{Provider: "openai", Model: "gpt-4o", User: "u", Params: params})
	require.NoError(t, err)
	assert.Equal(t, first.Params.Temperature, plain.Params.Temperature)
}

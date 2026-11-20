package bedrock

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// Converse has no typed top_k slot, so a Claude caller's top_k must reach Bedrock as
// additionalModelRequestFields.top_k, the way the invoke route already sends it (#9930).

const topKClaudeModel = "us.anthropic.claude-sonnet-4-6"

func requireTopK(t *testing.T, req *BedrockConverseRequest, want int) {
	t.Helper()
	require.NotNil(t, req.AdditionalModelRequestFields, "top_k must ride additionalModelRequestFields")
	got, ok := req.AdditionalModelRequestFields.Get("top_k")
	require.True(t, ok, "additionalModelRequestFields has no top_k")
	require.EqualValues(t, want, got)
	_, leaked := req.ExtraParams["top_k"]
	require.False(t, leaked, "top_k must not stay in ExtraParams")
}

func requireNoTopK(t *testing.T, req *BedrockConverseRequest) {
	t.Helper()
	if req.AdditionalModelRequestFields != nil {
		_, ok := req.AdditionalModelRequestFields.Get("top_k")
		require.False(t, ok, "top_k must not be sent to this model")
	}
}

// The reporter's path: POST /anthropic/v1/messages with a bedrock/ model is converted
// Anthropic -> Bifrost Responses -> Converse, and top_k used to be dropped on the way.
func TestAnthropicIngressTopKReachesConverse(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"model":       "bedrock/" + topKClaudeModel,
		"max_tokens":  16,
		"top_k":       41,
		"temperature": 0.37,
		"messages":    []map[string]any{{"role": "user", "content": "Invent a new name for a fictional town"}},
	})
	require.NoError(t, err)

	var ingressReq anthropic.AnthropicMessageRequest
	require.NoError(t, json.Unmarshal(body, &ingressReq))
	ctx := &schemas.BifrostContext{}
	bifrostReq := ingressReq.ToBifrostResponsesRequest(ctx)
	require.NotNil(t, bifrostReq)

	converseReq, err := ToBedrockResponsesRequest(ctx, bifrostReq)
	require.NoError(t, err)
	requireTopK(t, converseReq, 41)
	require.NotNil(t, converseReq.InferenceConfig)
	require.NotNil(t, converseReq.InferenceConfig.Temperature, "temperature still goes to inferenceConfig")

	wire, err := json.Marshal(converseReq)
	require.NoError(t, err)
	require.Contains(t, string(wire), `"additionalModelRequestFields":{"top_k":41}`)
}

func TestResponsesTopKKeepsExplicitAdditionalField(t *testing.T) {
	fields := schemas.NewOrderedMap()
	fields.Set("top_k", 7)
	bifrostReq := &schemas.BifrostResponsesRequest{
		Provider: schemas.Bedrock,
		Model:    topKClaudeModel,
		Params: &schemas.ResponsesParameters{ExtraParams: map[string]interface{}{
			"top_k":                            41,
			"additionalModelRequestFieldPaths": fields,
		}},
	}
	converseReq, err := ToBedrockResponsesRequest(&schemas.BifrostContext{}, bifrostReq)
	require.NoError(t, err)
	requireTopK(t, converseReq, 7)
}

func TestResponsesTopKSkippedForAdaptiveOnlyClaude(t *testing.T) {
	bifrostReq := &schemas.BifrostResponsesRequest{
		Provider: schemas.Bedrock,
		Model:    adaptiveOnlyModel,
		Params:   &schemas.ResponsesParameters{ExtraParams: map[string]interface{}{"top_k": 41}},
	}
	converseReq, err := ToBedrockResponsesRequest(&schemas.BifrostContext{}, bifrostReq)
	require.NoError(t, err)
	requireNoTopK(t, converseReq)
	_, leaked := converseReq.ExtraParams["top_k"]
	require.False(t, leaked, "a top_k the model rejects is dropped, as on the Anthropic provider")
}

func TestChatTopKReachesConverse(t *testing.T) {
	caps := schemas.ResolveModelCaps(schemas.Bedrock, topKClaudeModel)

	t.Run("typed field", func(t *testing.T) {
		req := &BedrockConverseRequest{}
		require.NoError(t, convertChatParameters(nil, &schemas.BifrostChatRequest{
			Model:  topKClaudeModel,
			Params: &schemas.ChatParameters{TopK: schemas.Ptr(41)},
		}, req, caps))
		requireTopK(t, req, 41)
	})

	t.Run("extra param", func(t *testing.T) {
		req := &BedrockConverseRequest{}
		require.NoError(t, convertChatParameters(nil, &schemas.BifrostChatRequest{
			Model:  topKClaudeModel,
			Params: &schemas.ChatParameters{ExtraParams: map[string]interface{}{"top_k": 41}},
		}, req, caps))
		requireTopK(t, req, 41)
		require.Nil(t, req.ExtraParams, "nothing else was in ExtraParams")
	})

	t.Run("not a Claude model", func(t *testing.T) {
		req := &BedrockConverseRequest{}
		model := "meta.llama3-70b-instruct-v1:0"
		require.NoError(t, convertChatParameters(nil, &schemas.BifrostChatRequest{
			Model:  model,
			Params: &schemas.ChatParameters{TopK: schemas.Ptr(41)},
		}, req, schemas.ResolveModelCaps(schemas.Bedrock, model)))
		requireNoTopK(t, req)
	})

	t.Run("adaptive-only Claude", func(t *testing.T) {
		req := &BedrockConverseRequest{}
		require.NoError(t, convertChatParameters(nil, &schemas.BifrostChatRequest{
			Model:  adaptiveOnlyModel,
			Params: &schemas.ChatParameters{TopK: schemas.Ptr(41)},
		}, req, schemas.ResolveModelCaps(schemas.Bedrock, adaptiveOnlyModel)))
		requireNoTopK(t, req)
	})
}

// A fallback converts the same Bifrost request again, so the converters must not delete the keys
// they consume from the caller's ExtraParams: the second conversion would lose top_k.
func TestTopKConversionLeavesCallerExtraParams(t *testing.T) {
	caps := schemas.ResolveModelCaps(schemas.Bedrock, topKClaudeModel)

	t.Run("responses", func(t *testing.T) {
		extra := map[string]interface{}{"top_k": 41, "stop": []string{"END"}}
		bifrostReq := &schemas.BifrostResponsesRequest{
			Provider: schemas.Bedrock,
			Model:    topKClaudeModel,
			Params:   &schemas.ResponsesParameters{ExtraParams: extra},
		}
		for i := 0; i < 2; i++ {
			converseReq, err := ToBedrockResponsesRequest(&schemas.BifrostContext{}, bifrostReq)
			require.NoError(t, err)
			requireTopK(t, converseReq, 41)
			require.Contains(t, extra, "top_k", "conversion %d deleted top_k from the caller's map", i+1)
			require.Contains(t, extra, "stop", "conversion %d deleted stop from the caller's map", i+1)
		}
	})

	t.Run("chat", func(t *testing.T) {
		extra := map[string]interface{}{"top_k": 41, "stop": []string{"END"}}
		bifrostReq := &schemas.BifrostChatRequest{
			Model:  topKClaudeModel,
			Params: &schemas.ChatParameters{ExtraParams: extra},
		}
		for i := 0; i < 2; i++ {
			req := &BedrockConverseRequest{}
			require.NoError(t, convertChatParameters(nil, bifrostReq, req, caps))
			requireTopK(t, req, 41)
			require.Contains(t, extra, "top_k", "conversion %d deleted top_k from the caller's map", i+1)
			require.Contains(t, extra, "stop", "conversion %d deleted stop from the caller's map", i+1)
		}
	})
}

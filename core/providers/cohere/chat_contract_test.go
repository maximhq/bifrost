package cohere

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests pin the Cohere v2 wire contract close to the converter logic so
// field-name regressions are caught even when live integration coverage is skipped.
func TestToCohereChatCompletionRequest_UsesNativeV2Fields(t *testing.T) {
	var responseFormat interface{} = map[string]interface{}{
		"type": "json_object",
		"schema": map[string]interface{}{
			"type": "object",
		},
	}
	seed := 42
	logprobs := true
	strictTools := true
	detail := "auto"
	priority := 7
	safetyMode := "STRICT"
	mode := "FAST"

	req, err := ToCohereChatCompletionRequest(&schemas.BifrostChatRequest{
		Model: "command-a-03-2025",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentBlocks: []schemas.ChatContentBlock{
						{
							Type: schemas.ChatContentBlockTypeText,
							Text: schemas.Ptr("Describe this image"),
						},
						{
							Type: schemas.ChatContentBlockTypeImage,
							ImageURLStruct: &schemas.ChatInputImage{
								URL:    "https://example.com/image.png",
								Detail: &detail,
							},
						},
					},
				},
			},
		},
		Params: &schemas.ChatParameters{
			LogProbs:       &logprobs,
			ResponseFormat: &responseFormat,
			Seed:           &seed,
			ToolChoice: &schemas.ChatToolChoice{
				ChatToolChoiceStr: schemas.Ptr(string(schemas.ChatToolChoiceTypeAuto)),
			},
			ExtraParams: map[string]interface{}{
				"strict_tools": strictTools,
				"safety_mode":  safetyMode,
				"priority":     priority,
				"citation_options": map[string]interface{}{
					"mode": mode,
				},
				"documents": []interface{}{
					"plain document",
					map[string]interface{}{
						"data": map[string]interface{}{
							"title": "Doc title",
						},
						"id": "doc-1",
					},
				},
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, req)

	assert.Nil(t, req.ToolChoice, "free-choice tool mode should omit tool_choice entirely")
	require.Len(t, req.Messages, 1)
	require.NotNil(t, req.Messages[0].Content)
	require.Len(t, req.Messages[0].Content.GetBlocks(), 2)
	require.NotNil(t, req.Messages[0].Content.GetBlocks()[1].ImageURL)
	assert.Equal(t, &detail, req.Messages[0].Content.GetBlocks()[1].ImageURL.Detail)
	require.NotNil(t, req.StrictTools)
	assert.True(t, *req.StrictTools)
	require.NotNil(t, req.CitationOptions)
	assert.Equal(t, &mode, req.CitationOptions.Mode)
	require.NotNil(t, req.Priority)
	assert.Equal(t, priority, *req.Priority)
	require.Len(t, req.Documents, 2)
	assert.Equal(t, "plain document", *req.Documents[0].StringDocument)
	require.NotNil(t, req.Documents[1].ObjectDocument)
	assert.Equal(t, "doc-1", *req.Documents[1].ObjectDocument.ID)
	require.NotNil(t, req.Seed)
	assert.Equal(t, seed, *req.Seed)

	payload, err := json.Marshal(req)
	require.NoError(t, err)

	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(payload, &body))

	assert.NotContains(t, body, "tool_choice")
	assert.Contains(t, body, "logprobs")
	assert.NotContains(t, body, "log_probs")
	assert.Contains(t, body, "strict_tools")
	assert.NotContains(t, body, "strict_tool_choice")

	responseFormatBody, ok := body["response_format"].(map[string]interface{})
	require.True(t, ok)
	assert.Contains(t, responseFormatBody, "schema")
	assert.NotContains(t, responseFormatBody, "json_schema")
}

func TestCohereChatRequestToBifrostChatRequest_PreservesNativeFields(t *testing.T) {
	seed := 99
	logprobs := true
	strictTools := true
	priority := 3
	mode := "ACCURATE"
	detail := "low"
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	req := &CohereChatRequest{
		Model:     "command-a-03-2025",
		Seed:      &seed,
		LogProbs:  &logprobs,
		Priority:  &priority,
		Documents: []CohereChatDocument{{StringDocument: schemas.Ptr("plain document")}},
		CitationOptions: &CohereCitationOptions{
			Mode: &mode,
		},
		StrictTools: &strictTools,
		Messages: []CohereMessage{
			{
				Role: "user",
				Content: NewBlocksContent([]CohereContentBlock{
					{
						Type: CohereContentBlockTypeImage,
						ImageURL: &CohereImageURL{
							URL:    "https://example.com/image.png",
							Detail: &detail,
						},
					},
				}),
			},
		},
	}

	bifrostReq := req.ToBifrostChatRequest(ctx)
	require.NotNil(t, bifrostReq)
	require.NotNil(t, bifrostReq.Params)
	require.NotNil(t, bifrostReq.Params.Seed)
	assert.Equal(t, seed, *bifrostReq.Params.Seed)
	require.NotNil(t, bifrostReq.Params.LogProbs)
	assert.True(t, *bifrostReq.Params.LogProbs)

	require.Len(t, bifrostReq.Input, 1)
	require.NotNil(t, bifrostReq.Input[0].Content)
	require.Len(t, bifrostReq.Input[0].Content.ContentBlocks, 1)
	require.NotNil(t, bifrostReq.Input[0].Content.ContentBlocks[0].ImageURLStruct)
	assert.Equal(t, &detail, bifrostReq.Input[0].Content.ContentBlocks[0].ImageURLStruct.Detail)

	require.NotNil(t, bifrostReq.Params.ExtraParams)
	assert.Equal(t, strictTools, bifrostReq.Params.ExtraParams["strict_tools"])
	assert.Equal(t, priority, bifrostReq.Params.ExtraParams["priority"])
	_, ok := bifrostReq.Params.ExtraParams["documents"]
	assert.True(t, ok)
	_, ok = bifrostReq.Params.ExtraParams["citation_options"]
	assert.True(t, ok)
}

// Responses tool-choice normalization shares the same omit-for-auto behavior as
// the chat path, so keep a direct unit test here rather than relying on integration coverage.
func TestConvertBifrostToolChoiceToCohereToolChoice_AutoIsNil(t *testing.T) {
	auto := "auto"
	any := "any"
	required := "required"

	assert.Nil(t, convertBifrostToolChoiceToCohereToolChoice(schemas.ResponsesToolChoice{
		ResponsesToolChoiceStr: &auto,
	}))
	assert.Nil(t, convertBifrostToolChoiceToCohereToolChoice(schemas.ResponsesToolChoice{
		ResponsesToolChoiceStr: &any,
	}))

	choice := convertBifrostToolChoiceToCohereToolChoice(schemas.ResponsesToolChoice{
		ResponsesToolChoiceStr: &required,
	})
	require.NotNil(t, choice)
	assert.Equal(t, ToolChoiceRequired, *choice)
}

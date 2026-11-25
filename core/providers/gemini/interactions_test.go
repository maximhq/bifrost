package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"
)

func interactionsUserText(text string) schemas.ResponsesMessage {
	return userResponsesMessage(&schemas.ResponsesMessageContent{ContentStr: schemas.Ptr(text)})
}

func marshalInteractionsBody(t *testing.T, req *GeminiInteractionsRequest) string {
	t.Helper()
	raw, err := providerUtils.MarshalSorted(req)
	require.NoError(t, err)
	return string(raw)
}

// TestShouldUseGeminiInteractions pins the gate: only the Interactions route, a stored previous
// turn, a background run, or an agent leave generateContent.
func TestShouldUseGeminiInteractions(t *testing.T) {
	plain := &schemas.BifrostResponsesRequest{Model: "gemini-3.8-flash", Params: &schemas.ResponsesParameters{}}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	assert.False(t, ShouldUseGeminiInteractions(ctx, plain))
	assert.False(t, ShouldUseGeminiInteractions(ctx, &schemas.BifrostResponsesRequest{Model: "gemini-3.8-flash", Params: &schemas.ResponsesParameters{PreviousResponseID: schemas.Ptr("")}}))

	assert.True(t, ShouldUseGeminiInteractions(ctx, &schemas.BifrostResponsesRequest{Model: "gemini-3.8-flash", Params: &schemas.ResponsesParameters{PreviousResponseID: schemas.Ptr("v1_abc")}}))
	assert.True(t, ShouldUseGeminiInteractions(ctx, &schemas.BifrostResponsesRequest{Model: "gemini-3.8-flash", Params: &schemas.ResponsesParameters{Background: schemas.Ptr(true)}}))
	assert.True(t, ShouldUseGeminiInteractions(ctx, &schemas.BifrostResponsesRequest{Model: "deep-research-preview-04-2026"}))

	routed := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	routed.SetValue(schemas.BifrostContextKeyInteractionsAPI, true)
	assert.True(t, ShouldUseGeminiInteractions(routed, plain))
}

func TestToGeminiInteractionsRequestSingleTurn(t *testing.T) {
	req := &schemas.BifrostResponsesRequest{
		Model: "gemini-3.8-flash",
		Input: []schemas.ResponsesMessage{
			{Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage), Role: schemas.Ptr(schemas.ResponsesInputMessageRoleDeveloper), Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Be brief.")}},
			interactionsUserText("What is the capital of France?"),
		},
		Params: &schemas.ResponsesParameters{
			Instructions:       schemas.Ptr("You are a geographer."),
			PreviousResponseID: schemas.Ptr("v1_prev"),
			Store:              schemas.Ptr(true),
			Background:         schemas.Ptr(false),
			MaxOutputTokens:    schemas.Ptr(256),
			Temperature:        schemas.Ptr(0.2),
			Reasoning:          &schemas.ResponsesParametersReasoning{Effort: schemas.Ptr("high"), Summary: schemas.Ptr("auto")},
			ServiceTier:        schemas.Ptr(schemas.BifrostServiceTierFlex),
			ToolChoice:         &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{Type: schemas.ResponsesToolChoiceTypeFunction, Name: schemas.Ptr("get_weather")}},
			Tools: []schemas.ResponsesTool{
				{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("get_weather"), Description: schemas.Ptr("Weather by city"), ResponsesToolFunction: &schemas.ResponsesToolFunction{Parameters: &schemas.ToolFunctionParameters{Type: "object", Required: []string{"city"}}}},
				{Type: schemas.ResponsesToolTypeWebSearch},
			},
			ExtraParams: map[string]interface{}{"seed": 7, "stop": []string{"END"}},
		},
	}
	native, err := ToGeminiInteractionsRequest(req)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"model":"gemini-3.8-flash",
		"input":"What is the capital of France?",
		"system_instruction":"You are a geographer.\n\nBe brief.",
		"previous_interaction_id":"v1_prev",
		"store":true,
		"background":false,
		"service_tier":"flex",
		"tools":[
			{"type":"function","name":"get_weather","description":"Weather by city","parameters":{"type":"object","properties":{},"required":["city"]}},
			{"type":"google_search"}
		],
		"generation_config":{
			"max_output_tokens":256,"temperature":0.2,"seed":7,"stop_sequences":["END"],
			"thinking_level":"high","thinking_summaries":"auto",
			"tool_choice":{"allowed_tools":{"mode":"any","tools":["get_weather"]}}
		}
	}`, marshalInteractionsBody(t, native))
	assert.Nil(t, native.ExtraParams, "seed and stop are moved into generation_config")
}

// TestToGeminiInteractionsRequestHistoryAsSteps pins stateless replay: a history with model turns,
// reasoning and tool traffic is sent as steps, with the thought signature kept.
func TestToGeminiInteractionsRequestHistoryAsSteps(t *testing.T) {
	req := &schemas.BifrostResponsesRequest{
		Model: "gemini-3.8-flash",
		Input: []schemas.ResponsesMessage{
			interactionsUserText("Weather in Paris?"),
			{Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning), ResponsesReasoning: &schemas.ResponsesReasoning{EncryptedContent: schemas.Ptr("sig_1"), Summary: []schemas.ResponsesReasoningSummary{{Type: schemas.ResponsesReasoningContentBlockTypeSummaryText, Text: "Need weather."}}}},
			{Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall), ResponsesToolMessage: &schemas.ResponsesToolMessage{CallID: schemas.Ptr("call_1"), Name: schemas.Ptr("get_weather"), Arguments: schemas.Ptr(`{"city":"Paris"}`)}},
			{Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput), ResponsesToolMessage: &schemas.ResponsesToolMessage{CallID: schemas.Ptr("call_1"), Name: schemas.Ptr("get_weather"), Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: schemas.Ptr(`{"temp":21}`)}}},
			{Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage), Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant), Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{{Type: schemas.ResponsesOutputMessageContentTypeText, Text: schemas.Ptr("It is 21C.")}}}},
			interactionsUserText("Thanks"),
		},
	}
	native, err := ToGeminiInteractionsRequest(req)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"model":"gemini-3.8-flash",
		"input":[
			{"type":"user_input","content":[{"type":"text","text":"Weather in Paris?"}]},
			{"type":"thought","signature":"sig_1","summary":[{"type":"text","text":"Need weather."}]},
			{"type":"function_call","id":"call_1","name":"get_weather","arguments":{"city":"Paris"}},
			{"type":"function_result","call_id":"call_1","name":"get_weather","result":[{"type":"text","text":"{\"temp\":21}"}]},
			{"type":"model_output","content":[{"type":"text","text":"It is 21C."}]},
			{"type":"user_input","content":[{"type":"text","text":"Thanks"}]}
		]
	}`, marshalInteractionsBody(t, native))
}

func TestToGeminiInteractionsRequestAgentAndMedia(t *testing.T) {
	req := &schemas.BifrostResponsesRequest{
		Model: "deep-research-preview-04-2026",
		Input: []schemas.ResponsesMessage{userResponsesMessage(&schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{
			{Type: schemas.ResponsesInputMessageContentBlockTypeText, Text: schemas.Ptr("Summarize")},
			{Type: schemas.ResponsesInputMessageContentBlockTypeImage, ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{ImageURL: schemas.Ptr("data:image/png;base64,AAAA")}},
			{Type: schemas.ResponsesInputMessageContentBlockTypeFile, ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{FileURL: schemas.Ptr("https://example.com/report.pdf")}},
		}})},
		Params: &schemas.ResponsesParameters{Background: schemas.Ptr(true), Text: &schemas.ResponsesTextConfig{Format: &schemas.ResponsesTextConfigFormat{Type: "json_object"}}},
	}
	native, err := ToGeminiInteractionsRequest(req)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"agent":"deep-research-preview-04-2026",
		"background":true,
		"input":[
			{"type":"text","text":"Summarize"},
			{"type":"image","data":"AAAA","mime_type":"image/png"},
			{"type":"document","uri":"https://example.com/report.pdf","mime_type":"application/pdf"}
		],
		"response_format":{"type":"text","mime_type":"application/json"}
	}`, marshalInteractionsBody(t, native))
}

func TestToGeminiInteractionsRequestRefusesUnservedTool(t *testing.T) {
	req := &schemas.BifrostResponsesRequest{
		Model:  "gemini-3.8-flash",
		Input:  []schemas.ResponsesMessage{interactionsUserText("hi")},
		Params: &schemas.ResponsesParameters{Tools: []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeLocalShell}}},
	}
	_, err := ToGeminiInteractionsRequest(req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"local_shell"`)

	// A file search the API cannot narrow the same way is refused rather than widened.
	fileSearch := func(fs *schemas.ResponsesToolFileSearch) *schemas.BifrostResponsesRequest {
		return &schemas.BifrostResponsesRequest{
			Model:  "gemini-3.8-flash",
			Input:  []schemas.ResponsesMessage{interactionsUserText("hi")},
			Params: &schemas.ResponsesParameters{Tools: []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeFileSearch, ResponsesToolFileSearch: fs}}},
		}
	}
	_, err = ToGeminiInteractionsRequest(fileSearch(&schemas.ResponsesToolFileSearch{VectorStoreIDs: []string{"fileSearchStores/s"}, Filters: &schemas.ResponsesToolFileSearchFilter{Type: "eq"}}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "filters")
	_, err = ToGeminiInteractionsRequest(fileSearch(&schemas.ResponsesToolFileSearch{VectorStoreIDs: []string{"fileSearchStores/s"}, RankingOptions: &schemas.ResponsesToolFileSearchRankingOptions{}}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ranking_options")
	native, err := ToGeminiInteractionsRequest(fileSearch(&schemas.ResponsesToolFileSearch{VectorStoreIDs: []string{"fileSearchStores/s"}, MaxNumResults: schemas.Ptr(3)}))
	require.NoError(t, err)
	assert.Contains(t, marshalInteractionsBody(t, native), `"file_search_store_names":["fileSearchStores/s"]`)
}

func TestGeminiInteractionsInputUnmarshal(t *testing.T) {
	var req GeminiInteractionsRequest
	require.NoError(t, sonic.Unmarshal([]byte(`{"model":"m","input":"hello"}`), &req))
	require.NotNil(t, req.Input.Text)
	assert.Equal(t, "hello", *req.Input.Text)

	req = GeminiInteractionsRequest{}
	require.NoError(t, sonic.Unmarshal([]byte(`{"model":"m","input":{"type":"function_result","call_id":"c1","result":"ok"}}`), &req))
	require.Len(t, req.Input.Items, 1)
	assert.Equal(t, "function_result", req.Input.Items[0].Type)

	req = GeminiInteractionsRequest{}
	require.NoError(t, sonic.Unmarshal([]byte(`{"model":"m","input":[{"type":"text","text":"a"},{"type":"text","text":"b"}],"stream":true}`), &req))
	assert.Len(t, req.Input.Items, 2)
	assert.True(t, req.IsStreamingRequested())
}

// TestGeminiInteractionsRequestToBifrost pins the inbound translation used when an Interactions
// caller's request runs on another provider.
func TestGeminiInteractionsRequestToBifrost(t *testing.T) {
	body := `{
		"model":"openai/gpt-5",
		"system_instruction":"Be brief.",
		"previous_interaction_id":"resp_1",
		"input":[
			{"type":"function_result","call_id":"call_1","name":"get_weather","result":[{"type":"text","text":"21C"}]},
			{"type":"text","text":"and tomorrow?"}
		],
		"tools":[{"type":"function","name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}],
		"generation_config":{"max_output_tokens":100,"thinking_level":"low","tool_choice":"any","seed":3},
		"response_format":{"type":"text","mime_type":"application/json","schema":{"type":"object"}},
		"service_tier":"priority",
		"fallbacks":["anthropic/claude-sonnet-5-5"]
	}`
	var req GeminiInteractionsRequest
	require.NoError(t, sonic.Unmarshal([]byte(body), &req))
	require.NoError(t, req.ValidateTranslatable())
	out, err := req.ToBifrostResponsesRequest()
	require.NoError(t, err)
	assert.Equal(t, schemas.OpenAI, out.Provider)
	assert.Equal(t, "gpt-5", out.Model)
	require.Len(t, out.Fallbacks, 1)
	assert.Equal(t, schemas.Anthropic, out.Fallbacks[0].Provider)

	p := out.Params
	assert.Equal(t, "Be brief.", *p.Instructions)
	assert.Equal(t, "resp_1", *p.PreviousResponseID)
	assert.Equal(t, 100, *p.MaxOutputTokens)
	assert.Equal(t, "low", *p.Reasoning.Effort)
	assert.Equal(t, "required", *p.ToolChoice.ResponsesToolChoiceStr)
	assert.Equal(t, 3, p.ExtraParams["seed"])
	assert.Equal(t, schemas.BifrostServiceTierPriority, *p.ServiceTier)
	assert.Equal(t, "json_schema", p.Text.Format.Type)
	require.Len(t, p.Tools, 1)
	assert.Equal(t, "get_weather", *p.Tools[0].Name)

	require.Len(t, out.Input, 2)
	assert.Equal(t, schemas.ResponsesMessageTypeFunctionCallOutput, *out.Input[0].Type)
	assert.Equal(t, "call_1", *out.Input[0].CallID)
	assert.Equal(t, "21C", *out.Input[0].Output.ResponsesToolCallOutputStr)
	assert.Equal(t, schemas.ResponsesInputMessageRoleUser, *out.Input[1].Role)
	assert.Equal(t, "and tomorrow?", *out.Input[1].Content.ContentBlocks[0].Text)
}

func TestGeminiInteractionsRequestNativeOnlyFields(t *testing.T) {
	var req GeminiInteractionsRequest
	require.NoError(t, sonic.Unmarshal([]byte(`{"agent":"deep-research-preview-04-2026","input":"go","agent_config":{"type":"deep-research"},"background":true}`), &req))
	err := req.ValidateTranslatable()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent")
	assert.Contains(t, err.Error(), "agent_config")

	out, convErr := req.ToBifrostResponsesRequest()
	require.NoError(t, convErr)
	assert.Equal(t, "deep-research-preview-04-2026", out.Model)

	// An output format with no text entry cannot be honoured by another provider.
	var audioOnly GeminiInteractionsRequest
	require.NoError(t, sonic.Unmarshal([]byte(`{"model":"gemini-3.8-flash","input":"say hi","response_format":{"type":"audio"}}`), &audioOnly))
	err = audioOnly.ValidateTranslatable()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "response_format")
	var mixed GeminiInteractionsRequest
	require.NoError(t, sonic.Unmarshal([]byte(`{"model":"gemini-3.8-flash","input":"hi","response_format":[{"type":"audio"},{"type":"text","mime_type":"application/json"}]}`), &mixed))
	err = mixed.ValidateTranslatable()
	require.Error(t, err, "a format list with an audio entry would lose the audio on another provider")
	assert.Contains(t, err.Error(), "response_format")
	var textList GeminiInteractionsRequest
	require.NoError(t, sonic.Unmarshal([]byte(`{"model":"gemini-3.8-flash","input":"hi","response_format":[{"type":"text","mime_type":"application/json"}]}`), &textList))
	assert.NoError(t, textList.ValidateTranslatable(), "a list of only text entries translates")
	var jsonText GeminiInteractionsRequest
	require.NoError(t, sonic.Unmarshal([]byte(`{"model":"gemini-3.8-flash","input":"hi","response_format":{"type":"text","mime_type":"application/json"}}`), &jsonText))
	assert.NoError(t, jsonText.ValidateTranslatable())
	assert.JSONEq(t, `{"type":"deep-research"}`, string(out.Params.ExtraParams["agent_config"].(json.RawMessage)))

	// Safety settings are Google's own policy; another provider would drop them unenforced.
	var safety GeminiInteractionsRequest
	require.NoError(t, sonic.Unmarshal([]byte(`{"model":"gemini-3.8-flash","input":"hi","safety_settings":[{"category":"HARM_CATEGORY_HARASSMENT","threshold":"BLOCK_LOW_AND_ABOVE"}]}`), &safety))
	err = safety.ValidateTranslatable()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "safety_settings")
}

func TestGeminiInteractionToBifrostResponses(t *testing.T) {
	body := `{
		"id":"v1_run","object":"interaction","model":"gemini-3.8-flash","status":"requires_action",
		"created":"2026-11-18T10:00:00Z","updated":"2026-11-18T10:00:05Z",
		"steps":[
			{"type":"thought","signature":"sig_a","summary":[{"type":"text","text":"Thinking"}]},
			{"type":"google_search_call","id":"s1","arguments":{"queries":["paris weather"]}},
			{"type":"model_output","content":[{"type":"text","text":"Checking."}]},
			{"type":"function_call","id":"call_9","name":"get_weather","arguments":{"city":"Paris"}}
		],
		"usage":{"total_input_tokens":10,"total_output_tokens":5,"total_thought_tokens":7,"total_cached_tokens":2,"total_tokens":22}
	}`
	var interaction GeminiInteraction
	require.NoError(t, sonic.Unmarshal([]byte(body), &interaction))
	resp := interaction.ToBifrostResponsesResponse()

	assert.Equal(t, "v1_run", *resp.ID)
	assert.Equal(t, "gemini-3.8-flash", resp.Model)
	assert.Equal(t, schemas.ResponsesResponseStatusCompleted, *resp.Status, "requires_action is a finished turn waiting on function results")
	require.Len(t, resp.Output, 4)
	assert.Equal(t, schemas.ResponsesMessageTypeReasoning, *resp.Output[0].Type)
	assert.Equal(t, "sig_a", *resp.Output[0].ResponsesReasoning.EncryptedContent)
	assert.Equal(t, schemas.ResponsesMessageTypeWebSearchCall, *resp.Output[1].Type, "a search with no result yet is still its item")
	assert.Equal(t, "in_progress", *resp.Output[1].Status)
	assert.Equal(t, schemas.ResponsesMessageTypeMessage, *resp.Output[2].Type)
	assert.Equal(t, "Checking.", *resp.Output[2].Content.ContentBlocks[0].Text)
	assert.Equal(t, schemas.ResponsesMessageTypeFunctionCall, *resp.Output[3].Type)
	assert.Equal(t, "call_9", *resp.Output[3].CallID)
	assert.JSONEq(t, `{"city":"Paris"}`, *resp.Output[3].Arguments)
	assert.Nil(t, resp.ProviderExtraFields["steps"], "every step here has a Responses item")

	require.NotNil(t, resp.Usage)
	assert.Equal(t, 10, resp.Usage.InputTokens)
	assert.Equal(t, 12, resp.Usage.OutputTokens, "thought tokens are output")
	assert.Equal(t, 7, resp.Usage.OutputTokensDetails.ReasoningTokens)
	assert.Equal(t, 2, resp.Usage.InputTokensDetails.CachedReadTokens)
	assert.Equal(t, 22, resp.Usage.TotalTokens)
}

func TestToGeminiInteractionFromResponses(t *testing.T) {
	resp := &schemas.BifrostResponsesResponse{
		ID: schemas.Ptr("resp_1"), Model: "gpt-5", CreatedAt: 1700000000, Status: schemas.Ptr("completed"),
		Output: []schemas.ResponsesMessage{
			{Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage), Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant), Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{{Type: schemas.ResponsesOutputMessageContentTypeText, Text: schemas.Ptr("Calling")}}}},
			{Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall), ResponsesToolMessage: &schemas.ResponsesToolMessage{CallID: schemas.Ptr("call_1"), Name: schemas.Ptr("f"), Arguments: schemas.Ptr(`{"a":1}`)}},
		},
		Usage: &schemas.ResponsesResponseUsage{InputTokens: 3, OutputTokens: 9, TotalTokens: 12, OutputTokensDetails: &schemas.ResponsesResponseOutputTokens{ReasoningTokens: 4}},
	}
	out := ToGeminiInteraction(resp)
	raw, err := providerUtils.MarshalSorted(out)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"id":"resp_1","object":"interaction","model":"gpt-5","status":"requires_action",
		"created":"2023-11-14T22:13:20Z","updated":"2023-11-14T22:13:20Z",
		"steps":[
			{"type":"model_output","content":[{"type":"text","text":"Calling"}]},
			{"type":"function_call","id":"call_1","name":"f","arguments":{"a":1}}
		],
		"usage":{"total_input_tokens":3,"total_output_tokens":5,"total_thought_tokens":4,"total_tokens":12}
	}`, string(raw))

	// Spoken or drawn output with no text keeps its media block instead of an empty step.
	media := &schemas.BifrostResponsesResponse{
		ID: schemas.Ptr("resp_2"), Model: "gpt-audio", CreatedAt: 1700000000, Status: schemas.Ptr("completed"),
		Output: []schemas.ResponsesMessage{{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage), Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
			Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{
				{Type: schemas.ResponsesInputMessageContentBlockTypeAudio, Audio: &schemas.ResponsesInputMessageContentBlockAudio{Format: "wav", Data: "UklG"}},
				{Type: schemas.ResponsesInputMessageContentBlockTypeImage, ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{ImageURL: schemas.Ptr("data:image/png;base64,QUJD")}},
			}},
		}},
	}
	raw, err = providerUtils.MarshalSorted(ToGeminiInteraction(media))
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"steps":[{"type":"model_output","content":[{"type":"audio","data":"UklG","mime_type":"audio/wav"},{"type":"image","data":"QUJD","mime_type":"image/png"}]}]`)
}

func interactionsEvents(t *testing.T, lines ...string) []*GeminiInteractionsEvent {
	t.Helper()
	events := make([]*GeminiInteractionsEvent, 0, len(lines))
	for _, line := range lines {
		event, err := ParseGeminiInteractionsEvent([]byte(line))
		require.NoError(t, err)
		events = append(events, event)
	}
	return events
}

// TestGeminiInteractionsStreamToBifrost pins the SSE mapping: steps open and close output items,
// text, thought summaries and argument fragments become their delta events, and the completed
// event carries the run's usage.
func TestGeminiInteractionsStreamToBifrost(t *testing.T) {
	events := interactionsEvents(t,
		`{"event_type":"interaction.created","interaction":{"id":"v1_s","status":"in_progress","model":"gemini-3.8-flash"}}`,
		`{"event_type":"interaction.status_update","interaction_id":"v1_s","status":"in_progress"}`,
		`{"event_type":"step.start","index":0,"step":{"type":"thought"}}`,
		`{"event_type":"step.delta","index":0,"delta":{"type":"thought_summary","content":{"type":"text","text":"Plan"}}}`,
		`{"event_type":"step.delta","index":0,"delta":{"type":"thought_signature","signature":"sig_z"}}`,
		`{"event_type":"step.stop","index":0}`,
		`{"event_type":"step.start","index":1,"step":{"type":"model_output"}}`,
		`{"event_type":"step.delta","index":1,"delta":{"type":"text","text":"Hel"}}`,
		`{"event_type":"step.delta","index":1,"delta":{"type":"text","text":"lo"}}`,
		`{"event_type":"step.stop","index":1}`,
		`{"event_type":"step.start","index":2,"step":{"type":"function_call","id":"call_5","name":"f","arguments":{}}}`,
		`{"event_type":"step.delta","index":2,"delta":{"type":"arguments_delta","arguments":"{\"a\":"}}`,
		`{"event_type":"step.delta","index":2,"delta":{"type":"arguments_delta","arguments":"1}"}}`,
		`{"event_type":"step.stop","index":2}`,
		`{"event_type":"interaction.completed","interaction":{"id":"v1_s","status":"requires_action","model":"gemini-3.8-flash","service_tier":"flex","usage":{"total_input_tokens":4,"total_output_tokens":6,"total_thought_tokens":2,"total_tokens":12}}}`,
	)
	state := NewGeminiInteractionsStreamState("gemini-3.8-flash")
	var types []schemas.ResponsesStreamResponseType
	var last *schemas.BifrostResponsesStreamResponse
	perEvent := make([]int, len(events))
	for i, event := range events {
		out, err := state.ToBifrostResponsesStream(event)
		require.Nil(t, err)
		perEvent[i] = len(out)
		for _, ev := range out {
			types = append(types, ev.Type)
			last = ev
			if ev.Type == schemas.ResponsesStreamResponseTypeOutputItemDone && ev.Item != nil && ev.Item.Type != nil {
				switch *ev.Item.Type {
				case schemas.ResponsesMessageTypeReasoning:
					assert.Equal(t, "sig_z", *ev.Item.ResponsesReasoning.EncryptedContent)
					assert.Equal(t, "Plan", ev.Item.ResponsesReasoning.Summary[0].Text)
				case schemas.ResponsesMessageTypeMessage:
					assert.Equal(t, "Hello", *ev.Item.Content.ContentBlocks[0].Text)
				case schemas.ResponsesMessageTypeFunctionCall:
					assert.Equal(t, `{"a":1}`, *ev.Item.Arguments)
					assert.Equal(t, "call_5", *ev.Item.CallID)
				}
			}
		}
	}
	assert.Equal(t, 0, perEvent[1], "a status update between steps has no Responses event")
	assert.Equal(t, []schemas.ResponsesStreamResponseType{
		schemas.ResponsesStreamResponseTypeCreated, schemas.ResponsesStreamResponseTypeInProgress,
		schemas.ResponsesStreamResponseTypeOutputItemAdded,
		schemas.ResponsesStreamResponseTypeReasoningSummaryPartAdded, schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta,
		schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta, // the signature
		schemas.ResponsesStreamResponseTypeReasoningSummaryTextDone, schemas.ResponsesStreamResponseTypeReasoningSummaryPartDone, schemas.ResponsesStreamResponseTypeOutputItemDone,
		schemas.ResponsesStreamResponseTypeOutputItemAdded,
		schemas.ResponsesStreamResponseTypeContentPartAdded, schemas.ResponsesStreamResponseTypeOutputTextDelta, schemas.ResponsesStreamResponseTypeOutputTextDelta,
		schemas.ResponsesStreamResponseTypeOutputTextDone, schemas.ResponsesStreamResponseTypeContentPartDone, schemas.ResponsesStreamResponseTypeOutputItemDone,
		schemas.ResponsesStreamResponseTypeOutputItemAdded,
		schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta, schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
		schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone, schemas.ResponsesStreamResponseTypeOutputItemDone,
		schemas.ResponsesStreamResponseTypeCompleted,
	}, types)
	require.NotNil(t, last.Response)
	assert.Equal(t, "v1_s", *last.Response.ID)
	require.NotNil(t, last.Response.ServiceTier, "the served tier reaches the terminal stream response, as on unary")
	assert.Equal(t, schemas.BifrostServiceTier("flex"), *last.Response.ServiceTier)
	assert.Equal(t, 8, last.Response.Usage.OutputTokens)
	assert.Equal(t, 12, last.Response.Usage.TotalTokens)
}

func TestGeminiInteractionsStreamError(t *testing.T) {
	state := NewGeminiInteractionsStreamState("m")
	_, err := state.ToBifrostResponsesStream(interactionsEvents(t, `{"event_type":"error","error":{"code":"resource_exhausted","message":"quota"}}`)[0])
	require.NotNil(t, err)
	assert.Equal(t, "quota", err.Error.Message)
	assert.Equal(t, "resource_exhausted", *err.Error.Code)
}

// TestBifrostToGeminiInteractionsStream pins the reverse rendering for a translated stream.
func TestBifrostToGeminiInteractionsStream(t *testing.T) {
	idx := 0
	state := NewBifrostToGeminiInteractionsStreamState()
	var got []string
	for _, ev := range []*schemas.BifrostResponsesStreamResponse{
		{Type: schemas.ResponsesStreamResponseTypeCreated, Response: &schemas.BifrostResponsesResponse{ID: schemas.Ptr("resp_9"), Model: "gpt-5"}},
		{Type: schemas.ResponsesStreamResponseTypeInProgress},
		{Type: schemas.ResponsesStreamResponseTypeOutputItemAdded, OutputIndex: &idx, Item: &schemas.ResponsesMessage{Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage), Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant)}},
		{Type: schemas.ResponsesStreamResponseTypeOutputTextDelta, OutputIndex: &idx, Delta: schemas.Ptr("Hi")},
		{Type: schemas.ResponsesStreamResponseTypeOutputItemDone, OutputIndex: &idx},
		{Type: schemas.ResponsesStreamResponseTypeCompleted, Response: &schemas.BifrostResponsesResponse{ID: schemas.Ptr("resp_9"), Model: "gpt-5", Status: schemas.Ptr("completed"), Usage: &schemas.ResponsesResponseUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}},
	} {
		for _, out := range state.ToGeminiInteractionsStreamEvents(ev) {
			raw, err := sonic.Marshal(out)
			require.NoError(t, err)
			got = append(got, string(raw))
		}
	}
	require.Len(t, got, 5)
	assert.JSONEq(t, `{"event_type":"interaction.created","event_id":"evt_1","interaction":{"id":"resp_9","object":"interaction","model":"gpt-5","status":"in_progress"}}`, got[0])
	assert.JSONEq(t, `{"event_type":"step.start","event_id":"evt_2","index":0,"step":{"type":"model_output"}}`, got[1])
	assert.JSONEq(t, `{"event_type":"step.delta","event_id":"evt_3","index":0,"delta":{"type":"text","text":"Hi"}}`, got[2])
	assert.JSONEq(t, `{"event_type":"step.stop","event_id":"evt_4","index":0}`, got[3])
	assert.Contains(t, got[4], `"event_type":"interaction.completed"`)
	assert.Contains(t, got[4], `"total_tokens":2`)
}

func TestNormalizeRawGeminiInteractionsBody(t *testing.T) {
	out, err := normalizeRawGeminiInteractionsBody([]byte(`{"model":"gemini/gemini-3.8-flash","input":"hi","fallbacks":["vertex/gemini-3.8-flash"]}`), "gemini-3.8-flash", false)
	require.Nil(t, err)
	assert.JSONEq(t, `{"model":"gemini-3.8-flash","input":"hi"}`, string(out))

	out, err = normalizeRawGeminiInteractionsBody([]byte(`{"agent":"gemini/deep-research-preview-04-2026","input":"go"}`), "deep-research-preview-04-2026", true)
	require.Nil(t, err)
	assert.JSONEq(t, `{"agent":"deep-research-preview-04-2026","input":"go"}`, string(out))
}

// newInteractionsTestServer answers Interactions calls with body and records each request.
type interactionsCall struct {
	method, path, query, apiKey, body string
}

func newInteractionsTestServer(t *testing.T, contentType string, reply func(call interactionsCall) string) (*httptest.Server, *[]interactionsCall) {
	t.Helper()
	// The handler runs on the server's goroutine; the mutex orders its append before the test's
	// reads, which happen after the response (and so after the append) has been received.
	var mu sync.Mutex
	calls := &[]interactionsCall{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		call := interactionsCall{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, apiKey: r.Header.Get("x-goog-api-key"), body: string(raw)}
		mu.Lock()
		*calls = append(*calls, call)
		mu.Unlock()
		w.Header().Set("Content-Type", contentType)
		_, _ = io.WriteString(w, reply(call))
	}))
	t.Cleanup(server.Close)
	return server, calls
}

// TestGeminiResponsesPreviousResponseIDUsesInteractions pins the egress switch: a Responses request
// naming a previous response goes to POST {base}/interactions with previous_interaction_id, and the
// interaction id comes back as the response id.
func TestGeminiResponsesPreviousResponseIDUsesInteractions(t *testing.T) {
	server, calls := newInteractionsTestServer(t, "application/json", func(interactionsCall) string {
		return `{"id":"v1_next","object":"interaction","model":"gemini-3.8-flash","status":"completed","steps":[{"type":"model_output","content":[{"type":"text","text":"Paris."}]}],"usage":{"total_input_tokens":5,"total_output_tokens":2,"total_tokens":7}}`
	})
	provider := NewGeminiProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL + "/v1beta"}}, nil)
	key := schemas.Key{Value: schemas.SecretVar{Val: "test-key"}, Models: []string{"*"}}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	resp, bifrostErr := provider.Responses(ctx, key, &schemas.BifrostResponsesRequest{
		Provider: schemas.Gemini,
		Model:    "gemini-3.8-flash",
		Input:    []schemas.ResponsesMessage{interactionsUserText("And its capital?")},
		Params:   &schemas.ResponsesParameters{PreviousResponseID: schemas.Ptr("v1_prev")},
	})
	require.Nil(t, bifrostErr)
	require.Len(t, *calls, 1)
	assert.Equal(t, "/v1beta/interactions", (*calls)[0].path)
	assert.Equal(t, "test-key", (*calls)[0].apiKey)
	assert.JSONEq(t, `{"model":"gemini-3.8-flash","input":"And its capital?","previous_interaction_id":"v1_prev"}`, (*calls)[0].body)
	assert.Equal(t, "v1_next", *resp.ID)
	assert.Equal(t, "Paris.", *resp.Output[0].Content.ContentBlocks[0].Text)
	assert.Equal(t, 7, resp.Usage.TotalTokens)
	assert.Nil(t, resp.ProviderJob, "a run that finished in the foreground is billed on its own row")
}

// TestGeminiResponsesBackgroundRecordsJob pins billing for a background run: the create reports
// the run on ProviderJob so job accounting settles it once it ends.
func TestGeminiResponsesBackgroundRecordsJob(t *testing.T) {
	server, _ := newInteractionsTestServer(t, "application/json", func(interactionsCall) string {
		return `{"id":"v1_bg","object":"interaction","agent":"deep-research-preview-04-2026","status":"in_progress","usage":{"total_input_tokens":5,"total_output_tokens":3,"total_tokens":8}}`
	})
	provider := NewGeminiProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL + "/v1beta"}}, nil)
	key := schemas.Key{Value: schemas.SecretVar{Val: "k"}, Models: []string{"*"}}
	resp, bifrostErr := provider.Responses(accountedCtx(), key, &schemas.BifrostResponsesRequest{
		Model:  "deep-research-preview-04-2026",
		Input:  []schemas.ResponsesMessage{interactionsUserText("Research X")},
		Params: &schemas.ResponsesParameters{Background: schemas.Ptr(true)},
	})
	require.Nil(t, bifrostErr)
	assert.Equal(t, schemas.ResponsesResponseStatusInProgress, *resp.Status)
	require.NotNil(t, resp.ProviderJob)
	assert.Equal(t, "v1_bg", resp.ProviderJob.ID)
	assert.True(t, resp.ProviderJob.Created)
	assert.False(t, resp.ProviderJob.Terminal)
	assert.Equal(t, "/interactions/v1_bg", resp.ProviderJob.PollPath)
	// The usage so far moves onto the run, so the create row is not billed and then billed again
	// when the run settles.
	assert.Nil(t, resp.Usage, "the create row is not billed for the run")
	require.NotNil(t, resp.ProviderJob.Usage)
	require.NotNil(t, resp.ProviderJob.Usage.LLMUsage)
	assert.Equal(t, 8, resp.ProviderJob.Usage.LLMUsage.TotalTokens)
}

// TestGeminiResponsesPlainRequestKeepsGenerateContent pins that the common case is untouched.
func TestGeminiResponsesPlainRequestKeepsGenerateContent(t *testing.T) {
	server, calls := newInteractionsTestServer(t, "application/json", func(interactionsCall) string {
		return `{"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`
	})
	provider := NewGeminiProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL + "/v1beta"}}, nil)
	key := schemas.Key{Value: schemas.SecretVar{Val: "k"}, Models: []string{"*"}}
	_, bifrostErr := provider.Responses(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), key, &schemas.BifrostResponsesRequest{
		Model: "gemini-3.8-flash",
		Input: []schemas.ResponsesMessage{interactionsUserText("hi")},
	})
	require.Nil(t, bifrostErr)
	require.Len(t, *calls, 1)
	assert.Equal(t, "/v1beta/models/gemini-3.8-flash:generateContent", (*calls)[0].path)
}

// TestGeminiResponsesLifecycle pins retrieve, cancel and delete on /interactions/{id}.
func TestGeminiResponsesLifecycle(t *testing.T) {
	server, calls := newInteractionsTestServer(t, "application/json", func(call interactionsCall) string {
		switch {
		case call.method == http.MethodDelete:
			return `{}`
		case strings.HasSuffix(call.path, "/cancel"):
			return `{"id":"v1_x","status":"cancelled"}`
		}
		return `{"id":"v1_x","status":"completed","model":"gemini-3.8-flash","steps":[{"type":"user_input","content":[{"type":"text","text":"q"}]},{"type":"model_output","content":[{"type":"text","text":"a"}]}],"usage":{"total_input_tokens":1,"total_output_tokens":1,"total_tokens":2}}`
	})
	provider := NewGeminiProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL + "/v1beta"}}, nil)
	key := schemas.Key{Value: schemas.SecretVar{Val: "k"}, Models: []string{"*"}}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	got, bifrostErr := provider.ResponsesRetrieve(ctx, key, &schemas.BifrostResponsesRetrieveRequest{ResponseID: "v1_x"})
	require.Nil(t, bifrostErr)
	require.Len(t, got.Output, 1, "user_input steps are input, not output")
	require.NotNil(t, got.ProviderJob)
	assert.False(t, got.ProviderJob.Created)
	assert.True(t, got.ProviderJob.Terminal)
	assert.Equal(t, 2, got.ProviderJob.Usage.LLMUsage.TotalTokens)

	cancelled, bifrostErr := provider.ResponsesCancel(ctx, key, &schemas.BifrostResponsesCancelRequest{ResponseID: "v1_x"})
	require.Nil(t, bifrostErr)
	assert.Equal(t, schemas.ResponsesResponseStatusCancelled, *cancelled.Status)

	deleted, bifrostErr := provider.ResponsesDelete(ctx, key, &schemas.BifrostResponsesDeleteRequest{ResponseID: "v1_x"})
	require.Nil(t, bifrostErr)
	assert.True(t, deleted.Deleted)

	items, bifrostErr := provider.ResponsesInputItems(ctx, key, &schemas.BifrostResponsesInputItemsRequest{ResponseID: "v1_x"})
	require.Nil(t, bifrostErr)
	require.Len(t, items.Data, 1)
	assert.Equal(t, "q", *items.Data[0].Content.ContentBlocks[0].Text)

	require.Len(t, *calls, 4)
	assert.Equal(t, "GET /v1beta/interactions/v1_x", (*calls)[0].method+" "+(*calls)[0].path)
	assert.Equal(t, "POST /v1beta/interactions/v1_x/cancel", (*calls)[1].method+" "+(*calls)[1].path)
	assert.Equal(t, "DELETE /v1beta/interactions/v1_x", (*calls)[2].method+" "+(*calls)[2].path)
}

// TestGeminiResponsesStreamViaInteractions pins the streamed create: stream:true in the body,
// alt=sse on the URL, Google's SSE framing parsed, and each raw event attached to one chunk for
// the native route (an event with no Responses counterpart rides a raw-only chunk).
func TestGeminiResponsesStreamViaInteractions(t *testing.T) {
	sse := strings.Join([]string{
		"event: interaction.created", `data: {"event_type":"interaction.created","interaction":{"id":"v1_s","status":"in_progress","model":"gemini-3.8-flash"}}`, "",
		"event: interaction.status_update", `data: {"event_type":"interaction.status_update","interaction_id":"v1_s","status":"in_progress"}`, "",
		"event: step.start", `data: {"event_type":"step.start","index":0,"step":{"type":"model_output"}}`, "",
		"event: step.delta", `data: {"event_type":"step.delta","index":0,"delta":{"type":"text","text":"Hi"}}`, "",
		"event: step.stop", `data: {"event_type":"step.stop","index":0}`, "",
		"event: interaction.completed", `data: {"event_type":"interaction.completed","interaction":{"id":"v1_s","status":"completed","usage":{"total_input_tokens":1,"total_output_tokens":1,"total_tokens":2}}}`, "",
		"event: done", "data: [DONE]", "",
	}, "\n")
	server, calls := newInteractionsTestServer(t, "text/event-stream", func(interactionsCall) string { return sse })
	provider := NewGeminiProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL + "/v1beta"}}, nil)
	key := schemas.Key{Value: schemas.SecretVar{Val: "k"}, Models: []string{"*"}}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyInteractionsAPI, true)
	ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)  // the native route
	ctx.SetValue(schemas.BifrostContextKeyCaptureRawResponse, true) // what core derives from the route's send-back-raw flag

	postHook := func(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return result, err
	}
	stream, bifrostErr := provider.ResponsesStream(ctx, postHook, nil, key, &schemas.BifrostResponsesRequest{
		Model: "gemini-3.8-flash", Input: []schemas.ResponsesMessage{interactionsUserText("hi")}, RawRequestBody: []byte(`{"model":"gemini-3.8-flash","input":"hi"}`),
	})
	require.Nil(t, bifrostErr)

	var rawEvents []string
	var types []schemas.ResponsesStreamResponseType
	for chunk := range stream {
		require.Nil(t, chunk.BifrostError)
		resp := chunk.BifrostResponsesStreamResponse
		require.NotNil(t, resp)
		types = append(types, resp.Type)
		if raw, ok := resp.ExtraFields.RawResponse.(string); ok {
			rawEvents = append(rawEvents, raw)
		}
	}
	require.Len(t, *calls, 1)
	assert.Equal(t, "/v1beta/interactions", (*calls)[0].path)
	assert.Equal(t, "alt=sse", (*calls)[0].query)
	assert.JSONEq(t, `{"model":"gemini-3.8-flash","input":"hi","stream":true}`, (*calls)[0].body)
	want := []string{
		`{"event_type":"interaction.created","interaction":{"id":"v1_s","status":"in_progress","model":"gemini-3.8-flash"}}`,
		`{"event_type":"interaction.status_update","interaction_id":"v1_s","status":"in_progress"}`,
		`{"event_type":"step.start","index":0,"step":{"type":"model_output"}}`,
		`{"event_type":"step.delta","index":0,"delta":{"type":"text","text":"Hi"}}`,
		`{"event_type":"step.stop","index":0}`,
		`{"event_type":"interaction.completed","interaction":{"id":"v1_s","status":"completed","usage":{"total_input_tokens":1,"total_output_tokens":1,"total_tokens":2}}}`,
	}
	assert.Equal(t, want, rawEvents, "every upstream event reaches a native caller exactly once, in order")
	assert.Contains(t, types, schemas.ResponsesStreamResponseTypeInteractionEvent)
	assert.Equal(t, schemas.ResponsesStreamResponseTypeCompleted, types[len(types)-1])
}

// BenchmarkToGeminiInteractionsRequest measures converting a multi-turn Responses request with a
// tool into an Interactions body, the work the /v1/responses -> Gemini Interactions path adds.
func BenchmarkToGeminiInteractionsRequest(b *testing.B) {
	req := &schemas.BifrostResponsesRequest{
		Model: "gemini-3.8-flash",
		Input: []schemas.ResponsesMessage{
			interactionsUserText("Weather in Paris?"),
			{Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall), ResponsesToolMessage: &schemas.ResponsesToolMessage{CallID: schemas.Ptr("call_1"), Name: schemas.Ptr("get_weather"), Arguments: schemas.Ptr(`{"city":"Paris"}`)}},
			{Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput), ResponsesToolMessage: &schemas.ResponsesToolMessage{CallID: schemas.Ptr("call_1"), Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: schemas.Ptr(`{"temp":21}`)}}},
		},
		Params: &schemas.ResponsesParameters{
			PreviousResponseID: schemas.Ptr("v1_prev"),
			Tools:              []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("get_weather"), ResponsesToolFunction: &schemas.ResponsesToolFunction{Parameters: &schemas.ToolFunctionParameters{Type: "object"}}}},
		},
	}
	b.ReportAllocs()
	for b.Loop() {
		native, err := ToGeminiInteractionsRequest(req)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := providerUtils.MarshalSorted(native); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkGeminiInteractionsStreamEvent measures parsing and converting one text delta event, the
// per-chunk cost of an Interactions stream.
func BenchmarkGeminiInteractionsStreamEvent(b *testing.B) {
	// A step takes a fixed batch of deltas before the state is replaced, so the text a step
	// accumulates stays bounded and does not grow with b.N.
	const deltasPerStep = 64
	data := []byte(`{"event_type":"step.delta","index":0,"delta":{"type":"text","text":"Hello there"}}`)
	start, _ := ParseGeminiInteractionsEvent([]byte(`{"event_type":"step.start","index":0,"step":{"type":"model_output"}}`))
	newState := func() *GeminiInteractionsStreamState {
		state := NewGeminiInteractionsStreamState("gemini-3.8-flash")
		_, _ = state.ToBifrostResponsesStream(start)
		return state
	}
	state := newState()
	n := 0
	b.ReportAllocs()
	for b.Loop() {
		if n == deltasPerStep {
			b.StopTimer()
			state, n = newState(), 0
			b.StartTimer()
		}
		event, err := ParseGeminiInteractionsEvent(data)
		if err != nil {
			b.Fatal(err)
		}
		_, _ = state.ToBifrostResponsesStream(event)
		n++
	}
}

// BenchmarkShouldUseGeminiInteractions measures the gate every Gemini and Vertex Responses request
// now passes, for the common case that stays on generateContent.
func BenchmarkShouldUseGeminiInteractions(b *testing.B) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := &schemas.BifrostResponsesRequest{Model: "gemini-3.8-flash", Params: &schemas.ResponsesParameters{MaxOutputTokens: schemas.Ptr(100)}}
	b.ReportAllocs()
	for b.Loop() {
		if ShouldUseGeminiInteractions(ctx, req) {
			b.Fatal("plain request left generateContent")
		}
	}
}

// BenchmarkShouldUseGeminiInteractionsWithTools measures the gate for a tool-calling request that
// stays on generateContent: the tool_search check passes over every tool.
func BenchmarkShouldUseGeminiInteractionsWithTools(b *testing.B) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	tools := make([]schemas.ResponsesTool, 20)
	for i := range tools {
		tools[i] = schemas.ResponsesTool{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr(fmt.Sprintf("tool_%d", i)), ResponsesToolFunction: &schemas.ResponsesToolFunction{}}
	}
	req := &schemas.BifrostResponsesRequest{Model: "gemini-3.8-flash", Params: &schemas.ResponsesParameters{Tools: tools}}
	b.ReportAllocs()
	for b.Loop() {
		if ShouldUseGeminiInteractions(ctx, req) {
			b.Fatal("plain tool request left generateContent")
		}
	}
}

// TestGeminiInteractionServerToolStepsToResponses pins the built-in tool steps the Interactions
// API documents onto their Responses items: a search is a web_search_call, code execution a
// code_interpreter_call, URL context one web_fetch_call per URL, a remote MCP call an mcp_call and
// file search a file_search_call. Call and result share one item, keyed by the call id. Steps
// with no Responses item (Google Maps) stay raw in provider_extra_fields. Citations on the text
// become annotations, and spoken output is an audio block, as on generateContent.
func TestGeminiInteractionServerToolStepsToResponses(t *testing.T) {
	body := `{
		"id":"v1_tools","object":"interaction","model":"gemini-3-flash-preview","status":"completed",
		"steps":[
			{"type":"google_search_call","id":"call_s","arguments":{"queries":["q1","q2"]},"search_type":"web_search","signature":"sg"},
			{"type":"google_search_result","call_id":"call_s","result":[{"search_suggestions":"<div/>"}],"is_error":false},
			{"type":"code_execution_call","id":"call_c","arguments":{"language":"python","code":"print(1)"}},
			{"type":"code_execution_result","call_id":"call_c","result":"1\n","is_error":false},
			{"type":"url_context_call","id":"call_u","arguments":{"urls":["https://a.example","https://b.example"]}},
			{"type":"url_context_result","call_id":"call_u","result":[{"url":"https://a.example","status":"success"},{"url":"https://b.example","status":"error"}]},
			{"type":"mcp_server_tool_call","id":"call_m","name":"roll","server_name":"dice","arguments":{"sides":6}},
			{"type":"mcp_server_tool_result","call_id":"call_m","name":"roll","server_name":"dice","result":[{"type":"text","text":"4"}]},
			{"type":"file_search_call","id":"call_f"},
			{"type":"file_search_result","call_id":"call_f"},
			{"type":"google_maps_call","id":"call_g","arguments":{}},
			{"type":"google_maps_result","call_id":"call_g","result":[]},
			{"type":"model_output","content":[
				{"type":"text","text":"Answer","annotations":[
					{"type":"url_citation","url":"https://a.example","title":"a","start_index":0,"end_index":6},
					{"type":"file_citation","document_uri":"fileSearchStores/s/documents/d","file_name":"d.pdf","start_index":0,"end_index":6}
				]},
				{"type":"audio","data":"QUJD","mime_type":"audio/wav"}
			]}
		]
	}`
	var interaction GeminiInteraction
	require.NoError(t, sonic.Unmarshal([]byte(body), &interaction))
	resp := interaction.ToBifrostResponsesResponse()

	var types []schemas.ResponsesMessageType
	for _, item := range resp.Output {
		types = append(types, *item.Type)
	}
	require.Equal(t, []schemas.ResponsesMessageType{
		schemas.ResponsesMessageTypeWebSearchCall,
		schemas.ResponsesMessageTypeCodeInterpreterCall,
		schemas.ResponsesMessageTypeWebFetchCall, schemas.ResponsesMessageTypeWebFetchCall,
		schemas.ResponsesMessageTypeMCPCall,
		schemas.ResponsesMessageTypeFileSearchCall,
		schemas.ResponsesMessageTypeMessage,
	}, types)

	search := resp.Output[0]
	assert.Equal(t, "call_s", *search.ID)
	assert.Equal(t, "completed", *search.Status)
	action := search.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction
	require.NotNil(t, action)
	assert.Equal(t, "search", action.Type)
	assert.Equal(t, []string{"q1", "q2"}, action.Queries)
	assert.Equal(t, "q1", *action.Query)

	code := resp.Output[1]
	assert.Equal(t, "call_c", *code.ID)
	assert.Equal(t, "completed", *code.Status)
	assert.Equal(t, "print(1)", *code.ResponsesToolMessage.ResponsesCodeInterpreterToolCall.Code)
	require.Len(t, code.ResponsesToolMessage.ResponsesCodeInterpreterToolCall.Outputs, 1)
	assert.Equal(t, "1\n", code.ResponsesToolMessage.ResponsesCodeInterpreterToolCall.Outputs[0].ResponsesCodeInterpreterOutputLogs.Logs)

	fetchA, fetchB := resp.Output[2], resp.Output[3]
	assert.Equal(t, "https://a.example", fetchA.ResponsesToolMessage.Action.ResponsesWebFetchToolCallAction.URL)
	assert.Equal(t, "completed", *fetchA.Status)
	assert.Equal(t, "https://b.example", fetchB.ResponsesToolMessage.Action.ResponsesWebFetchToolCallAction.URL)
	assert.Equal(t, "failed", *fetchB.Status, "a URL the tool could not read is a failed fetch")
	assert.NotEqual(t, *fetchA.ID, *fetchB.ID, "each URL is its own item")

	mcp := resp.Output[4]
	assert.Equal(t, "call_m", *mcp.ID)
	assert.Equal(t, "roll", *mcp.ResponsesToolMessage.Name)
	assert.Equal(t, "dice", *mcp.ResponsesToolMessage.ServerLabel)
	assert.JSONEq(t, `{"sides":6}`, *mcp.ResponsesToolMessage.Arguments)
	require.NotNil(t, mcp.ResponsesToolMessage.Output)
	assert.Equal(t, "4", *mcp.ResponsesToolMessage.Output.ResponsesToolCallOutputStr)
	assert.Equal(t, "completed", *mcp.Status)

	assert.Equal(t, "call_f", *resp.Output[5].ID)

	msg := resp.Output[6]
	require.Len(t, msg.Content.ContentBlocks, 2)
	text := msg.Content.ContentBlocks[0]
	require.Len(t, text.ResponsesOutputMessageContentText.Annotations, 2)
	url := text.ResponsesOutputMessageContentText.Annotations[0]
	assert.Equal(t, "url_citation", url.Type)
	assert.Equal(t, "https://a.example", *url.URL)
	assert.Equal(t, "a", *url.Title)
	assert.Equal(t, 0, *url.StartIndex)
	assert.Equal(t, 6, *url.EndIndex)
	file := text.ResponsesOutputMessageContentText.Annotations[1]
	assert.Equal(t, "file_citation", file.Type)
	assert.Equal(t, "d.pdf", *file.Filename)
	assert.Equal(t, "fileSearchStores/s/documents/d", *file.FileID)
	audio := msg.Content.ContentBlocks[1]
	assert.Equal(t, schemas.ResponsesInputMessageContentBlockTypeAudio, audio.Type)
	require.NotNil(t, audio.Audio)
	assert.Equal(t, "wav", audio.Audio.Format)
	assert.Equal(t, "QUJD", audio.Audio.Data)

	steps, ok := resp.ProviderExtraFields["steps"].([]GeminiInteractionsItem)
	require.True(t, ok, "steps with no Responses item stay raw")
	require.Len(t, steps, 2)
	assert.Equal(t, "google_maps_call", steps[0].Type)
	assert.Equal(t, "google_maps_result", steps[1].Type)
}

// TestGeminiInteractionsUsageCountsToolUseAndSearches pins usage the way Google bills it: tool
// use prompt tokens (what a code run or fetched page feeds back to the model) are input, so input
// and output add up to total_tokens, and grounding_tool_count's search count is the per-query
// search fee on Gemini 3 and later, one per request before that. Numbers are from a live
// code_execution run.
func TestGeminiInteractionsUsageCountsToolUseAndSearches(t *testing.T) {
	usage := &GeminiInteractionsUsage{
		TotalTokens: 1471, TotalInputTokens: 339, TotalOutputTokens: 218, TotalToolUseTokens: 445, TotalThoughtTokens: 469,
		GroundingToolCount: []GeminiInteractionsGroundingToolCount{{Type: "google_search", Count: 3}, {Type: "google_maps", Count: 1}},
	}
	got := usage.toResponsesUsage("gemini-3-flash-preview")
	assert.Equal(t, 784, got.InputTokens, "tool use prompt tokens are input")
	assert.Equal(t, 687, got.OutputTokens)
	assert.Equal(t, got.TotalTokens, got.InputTokens+got.OutputTokens)
	require.NotNil(t, got.ToolUsage)
	assert.Equal(t, 3, got.ToolUsage.WebSearch.NumRequests)

	older := usage.toResponsesUsage("gemini-2.5-flash")
	require.NotNil(t, older.ToolUsage)
	assert.Equal(t, 1, older.ToolUsage.WebSearch.NumRequests, "before Gemini 3 a grounded request is one search unit")

	plain := (&GeminiInteractionsUsage{TotalTokens: 3, TotalInputTokens: 1, TotalOutputTokens: 2}).toResponsesUsage("gemini-3-flash-preview")
	assert.Nil(t, plain.ToolUsage)
}

// TestGeminiInteractionRequiresContinuation pins a run Google paused for another request: it is
// incomplete, and the continuation_token the next request must send is surfaced.
func TestGeminiInteractionRequiresContinuation(t *testing.T) {
	var interaction GeminiInteraction
	require.NoError(t, sonic.Unmarshal([]byte(`{"id":"v1_long","status":"requires_continuation","continuation_token":"tok_1","model":"gemini-3-flash-preview","steps":[{"type":"model_output","content":[{"type":"text","text":"Part one"}]}]}`), &interaction))
	resp := interaction.ToBifrostResponsesResponse()
	assert.Equal(t, schemas.ResponsesResponseStatusIncomplete, *resp.Status)
	require.NotNil(t, resp.IncompleteDetails)
	assert.Equal(t, GeminiInteractionStatusRequiresContinuation, resp.IncompleteDetails.Reason)
	assert.Equal(t, "tok_1", resp.ProviderExtraFields["continuation_token"])

	state := NewGeminiInteractionsStreamState("gemini-3-flash-preview")
	var last *schemas.BifrostResponsesStreamResponse
	for _, event := range interactionsEvents(t,
		`{"event_type":"interaction.created","interaction":{"id":"v1_long","status":"in_progress"}}`,
		`{"event_type":"interaction.status_update","interaction_id":"v1_long","status":"requires_continuation","continuation_token":"tok_2"}`,
		`{"event_type":"interaction.completed","interaction":{"id":"v1_long","status":"requires_continuation","usage":{"total_tokens":3,"total_input_tokens":1,"total_output_tokens":2}}}`,
	) {
		out, err := state.ToBifrostResponsesStream(event)
		require.Nil(t, err)
		if len(out) > 0 {
			last = out[len(out)-1]
		}
	}
	assert.Equal(t, schemas.ResponsesStreamResponseTypeIncomplete, last.Type)
	assert.Equal(t, "tok_2", last.Response.ProviderExtraFields["continuation_token"], "the stream's latest token is the one to resume from")
}

// TestGeminiInteractionsStreamServerToolsAndCitations replays a live google_search stream (store
// false, trimmed). Built-in steps stream their whole call or result in one delta of their own
// type, citations arrive as text_annotation_delta, and interaction.completed carries usage but no
// steps, so the final response is built from the items the stream produced.
func TestGeminiInteractionsStreamServerToolsAndCitations(t *testing.T) {
	events := interactionsEvents(t,
		`{"interaction":{"object":"interaction","model":"gemini-3-flash-preview"},"event_type":"interaction.created"}`,
		`{"status":"in_progress","event_type":"interaction.status_update"}`,
		`{"index":0,"step":{"id":"call_1","signature":"","type":"google_search_call","search_type":"web_search"},"event_type":"step.start"}`,
		`{"index":0,"delta":{"signature":"s","type":"google_search_call","arguments":{"queries":["who won"]}},"event_type":"step.delta"}`,
		`{"index":0,"event_type":"step.stop"}`,
		`{"index":1,"step":{"call_id":"call_1","signature":"","type":"google_search_result"},"event_type":"step.start"}`,
		`{"index":1,"delta":{"signature":"s","type":"google_search_result","result":[{"search_suggestions":"<div/>"}],"is_error":false},"event_type":"step.delta"}`,
		`{"index":1,"event_type":"step.stop"}`,
		`{"index":2,"step":{"type":"thought"},"event_type":"step.start"}`,
		`{"index":2,"delta":{"signature":"sig","type":"thought_signature"},"event_type":"step.delta"}`,
		`{"index":2,"event_type":"step.stop"}`,
		`{"index":3,"step":{"type":"model_output"},"event_type":"step.start"}`,
		`{"index":3,"delta":{"text":"Kane won.","type":"text"},"event_type":"step.delta"}`,
		`{"index":3,"delta":{"annotations":[{"start_index":0,"end_index":9,"url":"https://r.example","title":"ballondor.com","type":"url_citation"}],"type":"text_annotation_delta"},"event_type":"step.delta"}`,
		`{"index":3,"event_type":"step.stop"}`,
		`{"interaction":{"status":"completed","model":"gemini-3-flash-preview","usage":{"total_tokens":2261,"total_input_tokens":985,"total_output_tokens":47,"total_tool_use_tokens":0,"total_thought_tokens":1229,"grounding_tool_count":[{"type":"google_search","count":3}]}},"event_type":"interaction.completed"}`,
	)
	state := NewGeminiInteractionsStreamState("gemini-3-flash-preview")
	var all []*schemas.BifrostResponsesStreamResponse
	for _, event := range events {
		out, err := state.ToBifrostResponsesStream(event)
		require.Nil(t, err)
		all = append(all, out...)
	}

	var searchDone, annotation *schemas.BifrostResponsesStreamResponse
	searchAdded := false
	for _, ev := range all {
		switch ev.Type {
		case schemas.ResponsesStreamResponseTypeOutputItemAdded:
			if *ev.Item.Type == schemas.ResponsesMessageTypeWebSearchCall {
				searchAdded = true
				assert.Equal(t, "in_progress", *ev.Item.Status)
			}
		case schemas.ResponsesStreamResponseTypeOutputItemDone:
			if *ev.Item.Type == schemas.ResponsesMessageTypeWebSearchCall {
				searchDone = ev
			}
		case schemas.ResponsesStreamResponseTypeOutputTextAnnotationAdded:
			annotation = ev
		}
	}
	require.True(t, searchAdded, "a search opens its item when it starts")
	require.NotNil(t, searchDone, "and closes it when its result arrives")
	assert.Equal(t, "call_1", *searchDone.Item.ID)
	assert.Equal(t, "completed", *searchDone.Item.Status)
	assert.Equal(t, []string{"who won"}, searchDone.Item.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction.Queries)
	require.NotNil(t, annotation, "a citation is streamed as an annotation")
	assert.Equal(t, "https://r.example", *annotation.Annotation.URL)

	final := all[len(all)-1]
	require.Equal(t, schemas.ResponsesStreamResponseTypeCompleted, final.Type)
	var finalTypes []schemas.ResponsesMessageType
	for _, item := range final.Response.Output {
		finalTypes = append(finalTypes, *item.Type)
	}
	assert.Equal(t, []schemas.ResponsesMessageType{schemas.ResponsesMessageTypeWebSearchCall, schemas.ResponsesMessageTypeReasoning, schemas.ResponsesMessageTypeMessage}, finalTypes,
		"interaction.completed has no steps, so the final output is what the stream produced")
	text := final.Response.Output[2].Content.ContentBlocks[0]
	assert.Equal(t, "Kane won.", *text.Text)
	require.Len(t, text.ResponsesOutputMessageContentText.Annotations, 1)
	require.NotNil(t, final.Response.Usage.ToolUsage)
	assert.Equal(t, 3, final.Response.Usage.ToolUsage.WebSearch.NumRequests)
}

// TestToGeminiInteractionsToolsComputerUse pins the computer use tool both ways: a Responses
// computer tool is Interactions computer_use with its environment, and back.
func TestToGeminiInteractionsToolsComputerUse(t *testing.T) {
	tools, err := toGeminiInteractionsTools([]schemas.ResponsesTool{{
		Type:                            schemas.ResponsesToolTypeComputerUsePreview,
		ResponsesToolComputerUsePreview: &schemas.ResponsesToolComputerUsePreview{Environment: "browser"},
	}})
	require.NoError(t, err)
	require.Len(t, tools, 1)
	assert.Equal(t, "computer_use", tools[0].Type)
	assert.Equal(t, "browser", *tools[0].Environment)

	back, err := geminiInteractionsToolsToResponses([]GeminiInteractionsTool{{Type: "computer_use", Environment: schemas.Ptr("desktop")}})
	require.NoError(t, err)
	require.Len(t, back, 1)
	assert.Equal(t, schemas.ResponsesToolTypeComputerUsePreview, back[0].Type)
	assert.Equal(t, "desktop", back[0].ResponsesToolComputerUsePreview.Environment)
}

// TestToGeminiInteractionsInputSkipsServerToolItems pins replay of a stateless turn: the built-in
// tool items a previous Interactions response returned ran on Google's side and carry no
// signature to replay, so they are dropped from the input rather than refusing the request.
func TestToGeminiInteractionsInputSkipsServerToolItems(t *testing.T) {
	assistant := schemas.ResponsesInputMessageRoleAssistant
	req := &schemas.BifrostResponsesRequest{Provider: schemas.Gemini, Model: "gemini-3-flash-preview", Input: []schemas.ResponsesMessage{
		interactionsUserText("Who won?"),
		{ID: schemas.Ptr("call_s"), Type: schemas.Ptr(schemas.ResponsesMessageTypeWebSearchCall), Status: schemas.Ptr("completed"), ResponsesToolMessage: &schemas.ResponsesToolMessage{Action: &schemas.ResponsesToolMessageActionStruct{ResponsesWebSearchToolCallAction: &schemas.ResponsesWebSearchToolCallAction{Type: "search", Queries: []string{"q"}}}}},
		{ID: schemas.Ptr("call_c"), Type: schemas.Ptr(schemas.ResponsesMessageTypeCodeInterpreterCall), ResponsesToolMessage: &schemas.ResponsesToolMessage{ResponsesCodeInterpreterToolCall: &schemas.ResponsesCodeInterpreterToolCall{Code: schemas.Ptr("1")}}},
		{Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage), Role: &assistant, Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Kane.")}},
		interactionsUserText("Since when?"),
	}}
	out, err := ToGeminiInteractionsRequest(req)
	require.NoError(t, err)
	require.NotNil(t, out.Input)
	var types []string
	for _, step := range out.Input.Items {
		types = append(types, step.Type)
	}
	assert.Equal(t, []string{"user_input", "model_output", "user_input"}, types)
}

// TestToGeminiInteractionServerToolItems pins the translated direction: an Interactions caller
// whose request ran on another provider gets that provider's built-in tool items as the steps the
// Interactions API documents, its citations as annotations and its search count as
// grounding_tool_count.
func TestToGeminiInteractionServerToolItems(t *testing.T) {
	text := schemas.ResponsesMessageContentBlock{
		Type: schemas.ResponsesOutputMessageContentTypeText, Text: schemas.Ptr("Kane."),
		ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{
			{Type: "url_citation", URL: schemas.Ptr("https://r.example"), Title: schemas.Ptr("r"), StartIndex: schemas.Ptr(0), EndIndex: schemas.Ptr(5)},
		}},
	}
	resp := &schemas.BifrostResponsesResponse{
		ID: schemas.Ptr("resp_t"), Model: "gpt-5", Status: schemas.Ptr("completed"),
		Output: []schemas.ResponsesMessage{
			{ID: schemas.Ptr("ws_1"), Type: schemas.Ptr(schemas.ResponsesMessageTypeWebSearchCall), Status: schemas.Ptr("completed"), ResponsesToolMessage: &schemas.ResponsesToolMessage{Action: &schemas.ResponsesToolMessageActionStruct{ResponsesWebSearchToolCallAction: &schemas.ResponsesWebSearchToolCallAction{Type: "search", Query: schemas.Ptr("who won")}}}},
			{ID: schemas.Ptr("ci_1"), Type: schemas.Ptr(schemas.ResponsesMessageTypeCodeInterpreterCall), Status: schemas.Ptr("completed"), ResponsesToolMessage: &schemas.ResponsesToolMessage{ResponsesCodeInterpreterToolCall: &schemas.ResponsesCodeInterpreterToolCall{
				Code: schemas.Ptr("print(2)"), Outputs: []schemas.ResponsesCodeInterpreterOutput{{ResponsesCodeInterpreterOutputLogs: &schemas.ResponsesCodeInterpreterOutputLogs{Type: "logs", Logs: "2\n"}}},
			}}},
			{ID: schemas.Ptr("mcp_1"), Type: schemas.Ptr(schemas.ResponsesMessageTypeMCPCall), Status: schemas.Ptr("completed"), ResponsesToolMessage: &schemas.ResponsesToolMessage{
				Name: schemas.Ptr("roll"), ServerLabel: schemas.Ptr("dice"), Arguments: schemas.Ptr(`{"sides":6}`),
				Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: schemas.Ptr("4")},
			}},
			{Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage), Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant), Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{text}}},
		},
		Usage: &schemas.ResponsesResponseUsage{InputTokens: 3, OutputTokens: 4, TotalTokens: 7, OutputTokensDetails: &schemas.ResponsesResponseOutputTokens{NumSearchQueries: schemas.Ptr(2)}},
	}
	raw, err := providerUtils.MarshalSorted(ToGeminiInteraction(resp))
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"id":"resp_t","object":"interaction","model":"gpt-5","status":"completed",
		"steps":[
			{"type":"google_search_call","id":"ws_1","arguments":{"queries":["who won"]}},
			{"type":"google_search_result","call_id":"ws_1","result":[]},
			{"type":"code_execution_call","id":"ci_1","arguments":{"code":"print(2)","language":"python"}},
			{"type":"code_execution_result","call_id":"ci_1","result":"2\n"},
			{"type":"mcp_server_tool_call","id":"mcp_1","name":"roll","server_name":"dice","arguments":{"sides":6}},
			{"type":"mcp_server_tool_result","call_id":"mcp_1","name":"roll","server_name":"dice","result":"4"},
			{"type":"model_output","content":[{"type":"text","text":"Kane.","annotations":[{"type":"url_citation","url":"https://r.example","title":"r","start_index":0,"end_index":5}]}]}
		],
		"usage":{"total_input_tokens":3,"total_output_tokens":4,"total_tokens":7,"grounding_tool_count":[{"type":"google_search","count":2}]}
	}`, string(raw))
}

// TestBifrostToGeminiInteractionsStreamServerTools pins a translated stream that carries a
// built-in tool item: the finished item becomes its call and result steps, each with its own step
// index, and the steps around it keep distinct indexes.
func TestBifrostToGeminiInteractionsStreamServerTools(t *testing.T) {
	state := NewBifrostToGeminiInteractionsStreamState()
	zero, one := 0, 1
	search := &schemas.ResponsesMessage{ID: schemas.Ptr("ws_1"), Type: schemas.Ptr(schemas.ResponsesMessageTypeWebSearchCall), Status: schemas.Ptr("completed"), ResponsesToolMessage: &schemas.ResponsesToolMessage{Action: &schemas.ResponsesToolMessageActionStruct{ResponsesWebSearchToolCallAction: &schemas.ResponsesWebSearchToolCallAction{Type: "search", Queries: []string{"q"}}}}}
	var events []*GeminiInteractionsEvent
	for _, ev := range []*schemas.BifrostResponsesStreamResponse{
		{Type: schemas.ResponsesStreamResponseTypeCreated, Response: &schemas.BifrostResponsesResponse{ID: schemas.Ptr("resp_s"), Model: "gpt-5"}},
		{Type: schemas.ResponsesStreamResponseTypeOutputItemAdded, OutputIndex: &zero, Item: &schemas.ResponsesMessage{ID: schemas.Ptr("ws_1"), Type: schemas.Ptr(schemas.ResponsesMessageTypeWebSearchCall), Status: schemas.Ptr("in_progress")}},
		{Type: schemas.ResponsesStreamResponseTypeOutputItemDone, OutputIndex: &zero, Item: search},
		{Type: schemas.ResponsesStreamResponseTypeOutputItemAdded, OutputIndex: &one, Item: &schemas.ResponsesMessage{Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage), Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant)}},
		{Type: schemas.ResponsesStreamResponseTypeOutputTextDelta, OutputIndex: &one, Delta: schemas.Ptr("Hi")},
		{Type: schemas.ResponsesStreamResponseTypeOutputItemDone, OutputIndex: &one},
	} {
		events = append(events, state.ToGeminiInteractionsStreamEvents(ev)...)
	}
	var got []string
	for _, ev := range events {
		switch ev.EventType {
		case GeminiInteractionsEventStepStart:
			got = append(got, fmt.Sprintf("start %d %s", *ev.Index, ev.Step.Type))
		case GeminiInteractionsEventStepDelta:
			got = append(got, fmt.Sprintf("delta %d %s", *ev.Index, ev.Delta.Type))
		case GeminiInteractionsEventStepStop:
			got = append(got, fmt.Sprintf("stop %d", *ev.Index))
		}
	}
	assert.Equal(t, []string{
		"start 0 google_search_call", "stop 0",
		"start 1 google_search_result", "stop 1",
		"start 2 model_output", "delta 2 text", "stop 2",
	}, got)
}

// TestGeminiInteractionsInputUnmarshalReplacesVariant pins that decoding into a reused input
// replaces the previous form instead of leaving the old text next to the new items.
func TestGeminiInteractionsInputUnmarshalReplacesVariant(t *testing.T) {
	var in GeminiInteractionsInput
	require.NoError(t, sonic.Unmarshal([]byte(`"old"`), &in))
	require.NoError(t, sonic.Unmarshal([]byte(`[{"type":"text","text":"new"}]`), &in))
	assert.Nil(t, in.Text, "the earlier string form must not survive an array decode")
	require.Len(t, in.Items, 1)

	require.NoError(t, sonic.Unmarshal([]byte(`"again"`), &in))
	assert.Nil(t, in.Items, "the earlier array form must not survive a string decode")
	require.NotNil(t, in.Text)
	assert.Equal(t, "again", *in.Text)
}

// TestGeminiInteractionsMCPAllowedToolsWireShape pins the native mcp_server allowlist: an array of
// {mode, tools} objects, in both directions.
func TestGeminiInteractionsMCPAllowedToolsWireShape(t *testing.T) {
	req := &schemas.BifrostResponsesRequest{
		Model: "gemini-3.8-flash",
		Input: []schemas.ResponsesMessage{interactionsUserText("hi")},
		Params: &schemas.ResponsesParameters{Tools: []schemas.ResponsesTool{{
			Type: schemas.ResponsesToolTypeMCP,
			ResponsesToolMCP: &schemas.ResponsesToolMCP{
				ServerLabel:     "tax",
				ServerURL:       schemas.Ptr("https://mcp.example.com"),
				RequireApproval: &schemas.ResponsesToolMCPAllowedToolsApprovalSetting{Setting: schemas.Ptr("never")},
				AllowedTools:    &schemas.ResponsesToolMCPAllowedTools{ToolNames: []string{"lookup"}},
			},
		}}},
	}
	native, err := ToGeminiInteractionsRequest(req)
	require.NoError(t, err)
	body := marshalInteractionsBody(t, native)
	assert.Contains(t, body, `"allowed_tools":[{"tools":["lookup"]}]`)
	assert.NotContains(t, body, `"headers"`, "no credential, no headers")

	// An authorization token goes to the server as a Bearer header; explicit headers win.
	mcpTool := req.Params.Tools[0].ResponsesToolMCP
	mcpTool.Authorization = schemas.Ptr("tok_mcp")
	native, err = ToGeminiInteractionsRequest(req)
	require.NoError(t, err)
	assert.Contains(t, marshalInteractionsBody(t, native), `"headers":{"Authorization":"Bearer tok_mcp"}`)
	callerHeaders := map[string]string{"Authorization": "Bearer explicit", "X-Tenant": "t1"}
	mcpTool.Headers = &callerHeaders
	native, err = ToGeminiInteractionsRequest(req)
	require.NoError(t, err)
	assert.Contains(t, marshalInteractionsBody(t, native), `"headers":{"Authorization":"Bearer explicit","X-Tenant":"t1"}`)
	assert.Len(t, callerHeaders, 2, "the caller's header map is not modified")

	var in GeminiInteractionsRequest
	require.NoError(t, sonic.Unmarshal([]byte(`{"model":"openai/gpt-5","input":"hi","tools":[{"type":"mcp_server","name":"tax","url":"https://mcp.example.com","allowed_tools":[{"mode":"auto","tools":["a","b"]}]}]}`), &in))
	out, convErr := in.ToBifrostResponsesRequest()
	require.NoError(t, convErr)
	require.Len(t, out.Params.Tools, 1)
	mcp := out.Params.Tools[0].ResponsesToolMCP
	require.NotNil(t, mcp)
	require.NotNil(t, mcp.AllowedTools)
	assert.Equal(t, []string{"a", "b"}, mcp.AllowedTools.ToolNames)
}

// TestToGeminiInteractionsMCPFilterAllowlistRejected pins that a filter-form MCP allowlist is
// refused instead of sending the server with no restriction at all.
func TestToGeminiInteractionsMCPFilterAllowlistRejected(t *testing.T) {
	req := &schemas.BifrostResponsesRequest{
		Model: "gemini-3.8-flash",
		Input: []schemas.ResponsesMessage{interactionsUserText("hi")},
		Params: &schemas.ResponsesParameters{Tools: []schemas.ResponsesTool{{
			Type: schemas.ResponsesToolTypeMCP,
			ResponsesToolMCP: &schemas.ResponsesToolMCP{
				ServerLabel:     "tax",
				ServerURL:       schemas.Ptr("https://mcp.example.com"),
				RequireApproval: &schemas.ResponsesToolMCPAllowedToolsApprovalSetting{Setting: schemas.Ptr("never")},
				AllowedTools:    &schemas.ResponsesToolMCPAllowedTools{Filter: &schemas.ResponsesToolMCPAllowedToolsFilter{ReadOnly: schemas.Ptr(true)}},
			},
		}}},
	}
	_, err := ToGeminiInteractionsRequest(req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "allowed_tools")

	// A filter that only names tools is the same restriction as a name list.
	req.Params.Tools[0].ResponsesToolMCP.AllowedTools = &schemas.ResponsesToolMCPAllowedTools{Filter: &schemas.ResponsesToolMCPAllowedToolsFilter{ToolNames: []string{"lookup"}}}
	native, err := ToGeminiInteractionsRequest(req)
	require.NoError(t, err)
	assert.Contains(t, marshalInteractionsBody(t, native), `"allowed_tools":[{"tools":["lookup"]}]`)
}

// TestGeminiInteractionsToolChoiceNoneKept pins that a native allowed_tools choice in mode none
// stays none on the Responses side instead of turning tool calling back on.
func TestGeminiInteractionsToolChoiceNoneKept(t *testing.T) {
	choice := geminiInteractionsToolChoiceToResponses(json.RawMessage(`{"allowed_tools":{"mode":"none","tools":["a"]}}`))
	require.NotNil(t, choice)
	require.NotNil(t, choice.ResponsesToolChoiceStr)
	assert.Equal(t, "none", *choice.ResponsesToolChoiceStr)
}

// TestGeminiInteractionsFunctionResultErrorFlag pins is_error in both directions: a failed
// Responses function_call_output is sent with is_error, and a native failed function_result
// comes back with its error set.
func TestGeminiInteractionsFunctionResultErrorFlag(t *testing.T) {
	failed := schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID: schemas.Ptr("call_1"),
			Name:   schemas.Ptr("f"),
			Error:  &schemas.ResponsesToolMessageError{ResponsesToolMessageErrorStr: schemas.Ptr("timeout")},
		},
	}
	ok := schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID: schemas.Ptr("call_2"),
			Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: schemas.Ptr("21C")},
		},
	}
	native, err := ToGeminiInteractionsRequest(&schemas.BifrostResponsesRequest{Model: "gemini-3.8-flash", Input: []schemas.ResponsesMessage{failed, ok}})
	require.NoError(t, err)
	require.NotNil(t, native.Input)
	require.Len(t, native.Input.Items, 2)
	require.NotNil(t, native.Input.Items[0].IsError)
	assert.True(t, *native.Input.Items[0].IsError)
	assert.JSONEq(t, `[{"type":"text","text":"timeout"}]`, string(native.Input.Items[0].Result), "a failure with no output carries its message")
	assert.Nil(t, native.Input.Items[1].IsError, "a successful result carries no error flag")

	msgs, convErr := geminiInteractionsStepToResponses(GeminiInteractionsItem{
		Type: GeminiInteractionsStepFunctionResult, CallID: schemas.Ptr("call_1"), Name: schemas.Ptr("f"),
		Result: json.RawMessage(`"timeout"`), IsError: schemas.Ptr(true),
	}, true)
	require.NoError(t, convErr)
	require.Len(t, msgs, 1)
	require.True(t, msgs[0].ResponsesToolMessage.Error.IsError())
	assert.Equal(t, "timeout", msgs[0].ResponsesToolMessage.Error.Text())
	assert.Equal(t, "timeout", *msgs[0].ResponsesToolMessage.Output.ResponsesToolCallOutputStr)

	msgs, convErr = geminiInteractionsStepToResponses(GeminiInteractionsItem{
		Type: GeminiInteractionsStepFunctionResult, CallID: schemas.Ptr("call_2"), Result: json.RawMessage(`"21C"`),
	}, true)
	require.NoError(t, convErr)
	assert.Nil(t, msgs[0].ResponsesToolMessage.Error)
}

// TestBifrostToGeminiInteractionsStreamCitations pins that a citation streamed by another provider
// reaches an Interactions caller as a text_annotation_delta on the text's step.
func TestBifrostToGeminiInteractionsStreamCitations(t *testing.T) {
	idx, contentIdx, annIdx := 0, 0, 0
	state := NewBifrostToGeminiInteractionsStreamState()
	var got []string
	for _, ev := range []*schemas.BifrostResponsesStreamResponse{
		{Type: schemas.ResponsesStreamResponseTypeCreated, Response: &schemas.BifrostResponsesResponse{ID: schemas.Ptr("resp_c"), Model: "gpt-5"}},
		{Type: schemas.ResponsesStreamResponseTypeOutputItemAdded, OutputIndex: &idx, Item: &schemas.ResponsesMessage{Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage), Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant)}},
		{Type: schemas.ResponsesStreamResponseTypeOutputTextDelta, OutputIndex: &idx, Delta: schemas.Ptr("Paris")},
		{Type: schemas.ResponsesStreamResponseTypeOutputTextAnnotationAdded, OutputIndex: &idx, ContentIndex: &contentIdx, AnnotationIndex: &annIdx, Annotation: &schemas.ResponsesOutputMessageContentTextAnnotation{
			Type: "url_citation", URL: schemas.Ptr("https://a.example"), Title: schemas.Ptr("a"), StartIndex: schemas.Ptr(0), EndIndex: schemas.Ptr(5),
		}},
		{Type: schemas.ResponsesStreamResponseTypeOutputItemDone, OutputIndex: &idx},
	} {
		for _, out := range state.ToGeminiInteractionsStreamEvents(ev) {
			raw, err := sonic.Marshal(out)
			require.NoError(t, err)
			got = append(got, string(raw))
		}
	}
	require.Len(t, got, 5)
	assert.JSONEq(t, `{"event_type":"step.delta","event_id":"evt_4","index":0,"delta":{"type":"text_annotation_delta","annotations":[{"type":"url_citation","url":"https://a.example","title":"a","start_index":0,"end_index":5}]}}`, got[3])
}

// TestBuildGeminiInteractionsBodyRawStream pins that a raw caller body opened as a stream is sent
// with stream:true whatever the caller's own stream field said.
func TestBuildGeminiInteractionsBodyRawStream(t *testing.T) {
	for name, body := range map[string]string{
		"omitted": `{"model":"gemini/gemini-3.8-flash","input":"hi"}`,
		"false":   `{"model":"gemini/gemini-3.8-flash","input":"hi","stream":false}`,
	} {
		t.Run(name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
			req := &schemas.BifrostResponsesRequest{Model: "gemini-3.8-flash", RawRequestBody: []byte(body)}
			out, err := buildGeminiInteractionsBody(ctx, req, true)
			require.Nil(t, err)
			assert.JSONEq(t, `{"model":"gemini-3.8-flash","input":"hi","stream":true}`, string(out))

			unary, err := buildGeminiInteractionsBody(ctx, req, false)
			require.Nil(t, err)
			assert.NotContains(t, string(unary), `"stream":true`, "a unary call keeps the caller's stream field")
		})
	}
}

// TestGeminiResponsesInteractionsLargePayloadRefused pins that a large-payload request routed to
// the Interactions API fails loudly instead of reaching Google without the streamed body.
func TestGeminiResponsesInteractionsLargePayloadRefused(t *testing.T) {
	server, calls := newInteractionsTestServer(t, "application/json", func(interactionsCall) string { return `{}` })
	provider := NewGeminiProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL + "/v1beta"}}, nil)
	key := schemas.Key{Value: schemas.SecretVar{Val: "k"}, Models: []string{"*"}}
	newCtx := func() *schemas.BifrostContext {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyInteractionsAPI, true)
		ctx.SetValue(schemas.BifrostContextKeyLargePayloadMode, true)
		ctx.SetValue(schemas.BifrostContextKeyLargePayloadReader, io.Reader(strings.NewReader(`{"model":"gemini/gemini-3.8-flash","input":[{"type":"image","data":"QUJD"}]}`)))
		return ctx
	}
	req := &schemas.BifrostResponsesRequest{Model: "gemini-3.8-flash"}

	_, bifrostErr := provider.Responses(newCtx(), key, req)
	require.NotNil(t, bifrostErr)
	assert.Contains(t, bifrostErr.Error.Message, "large payload")

	postHook := func(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return result, err
	}
	_, bifrostErr = provider.ResponsesStream(newCtx(), postHook, nil, key, req)
	require.NotNil(t, bifrostErr)
	assert.Contains(t, bifrostErr.Error.Message, "large payload")
	assert.Empty(t, *calls, "nothing is sent upstream")
}

// TestGeminiResponsesStreamPrematureEOF pins that a foreground stream cut off before
// interaction.completed ends in an error, while a background run's stream still hands off the
// run as it stands.
func TestGeminiResponsesStreamPrematureEOF(t *testing.T) {
	sse := strings.Join([]string{
		"event: interaction.created", `data: {"event_type":"interaction.created","interaction":{"id":"v1_cut","status":"in_progress","model":"gemini-3.8-flash"}}`, "",
		"event: step.start", `data: {"event_type":"step.start","index":0,"step":{"type":"model_output"}}`, "",
		"event: step.delta", `data: {"event_type":"step.delta","index":0,"delta":{"type":"text","text":"Hal"}}`, "",
		"event: interaction.status_update", `data: {"event_type":"interaction.status_update","interaction_id":"v1_cut","status":"in_progress","usage":{"total_input_tokens":5,"total_output_tokens":3,"total_tokens":8}}`, "",
	}, "\n")
	server, _ := newInteractionsTestServer(t, "text/event-stream", func(interactionsCall) string { return sse })
	provider := NewGeminiProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL + "/v1beta"}}, nil)
	key := schemas.Key{Value: schemas.SecretVar{Val: "k"}, Models: []string{"*"}}
	postHook := func(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return result, err
	}
	var handoff *schemas.BifrostResponsesStreamResponse
	collect := func(background bool) ([]schemas.ResponsesStreamResponseType, *schemas.BifrostError) {
		ctx := accountedCtx()
		ctx.SetValue(schemas.BifrostContextKeyInteractionsAPI, true)
		req := &schemas.BifrostResponsesRequest{Model: "gemini-3.8-flash", Input: []schemas.ResponsesMessage{interactionsUserText("hi")}}
		if background {
			req.Params = &schemas.ResponsesParameters{Background: schemas.Ptr(true)}
		}
		stream, bifrostErr := provider.ResponsesStream(ctx, postHook, nil, key, req)
		require.Nil(t, bifrostErr)
		var types []schemas.ResponsesStreamResponseType
		var streamErr *schemas.BifrostError
		for chunk := range stream {
			if chunk.BifrostError != nil {
				streamErr = chunk.BifrostError
				continue
			}
			if chunk.BifrostResponsesStreamResponse != nil {
				types = append(types, chunk.BifrostResponsesStreamResponse.Type)
				handoff = chunk.BifrostResponsesStreamResponse
			}
		}
		return types, streamErr
	}

	types, streamErr := collect(false)
	require.NotNil(t, streamErr, "a foreground stream that stops early is an error")
	assert.NotContains(t, types, schemas.ResponsesStreamResponseTypeCompleted)

	types, streamErr = collect(true)
	assert.Nil(t, streamErr)
	assert.NotContains(t, types, schemas.ResponsesStreamResponseTypeCompleted, "a running background run is not reported as completed")
	require.NotEmpty(t, types)
	assert.Equal(t, schemas.ResponsesStreamResponseTypeInProgress, types[len(types)-1], "the stream hands off the running run with a non-terminal event")
	// The handoff reports the run, so job accounting settles it even though the stream never saw it end.
	require.NotNil(t, handoff.Response)
	require.NotNil(t, handoff.Response.ProviderJob, "a streamed background create reports its run")
	assert.Equal(t, "v1_cut", handoff.Response.ProviderJob.ID)
	assert.True(t, handoff.Response.ProviderJob.Created)
	assert.False(t, handoff.Response.ProviderJob.Terminal)
	assert.Equal(t, "/interactions/v1_cut", handoff.Response.ProviderJob.PollPath)
	// The usage so far belongs to the run, which job accounting bills once it ends; the create row
	// carries none, or the partial usage would be billed twice.
	assert.Nil(t, handoff.Response.Usage, "the create row is not billed for the run")
	require.NotNil(t, handoff.Response.ProviderJob.Usage)
	require.NotNil(t, handoff.Response.ProviderJob.Usage.LLMUsage)
	assert.Equal(t, 8, handoff.Response.ProviderJob.Usage.LLMUsage.TotalTokens)
}

// TestGeminiInteractionsMCPRichResult pins that an MCP result with no text keeps its payload
// instead of reporting an empty success, and that the payload goes back out unchanged.
func TestGeminiInteractionsMCPRichResult(t *testing.T) {
	body := `{"id":"v1_m","object":"interaction","model":"gemini-3-flash-preview","status":"completed","steps":[
		{"type":"mcp_server_tool_call","id":"call_m","name":"chart","server_name":"viz","arguments":{}},
		{"type":"mcp_server_tool_result","call_id":"call_m","name":"chart","server_name":"viz","result":[{"type":"image","data":"QUJD","mime_type":"image/png"}]}
	]}`
	var interaction GeminiInteraction
	require.NoError(t, sonic.Unmarshal([]byte(body), &interaction))
	resp := interaction.ToBifrostResponsesResponse()
	require.Len(t, resp.Output, 1)
	out := resp.Output[0].ResponsesToolMessage.Output
	require.NotNil(t, out)
	require.NotNil(t, out.ResponsesToolCallOutputStr)
	assert.JSONEq(t, `[{"type":"image","data":"QUJD","mime_type":"image/png"}]`, *out.ResponsesToolCallOutputStr)

	steps := responsesServerToolItemToGeminiInteractionSteps(resp.Output[0])
	require.Len(t, steps, 2)
	assert.JSONEq(t, `[{"type":"image","data":"QUJD","mime_type":"image/png"}]`, string(steps[1].Result))

	text := resp.Output[0]
	text.ResponsesToolMessage = &schemas.ResponsesToolMessage{Name: schemas.Ptr("chart"), ServerLabel: schemas.Ptr("viz"), Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: schemas.Ptr("4")}}
	steps = responsesServerToolItemToGeminiInteractionSteps(text)
	require.Len(t, steps, 2)
	assert.JSONEq(t, `"4"`, string(steps[1].Result), "a text result stays a string")

	text.ResponsesToolMessage.Output = &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: schemas.Ptr("[1,2]")}
	steps = responsesServerToolItemToGeminiInteractionSteps(text)
	assert.JSONEq(t, `"[1,2]"`, string(steps[1].Result), "a JSON array of plain values is text, not content blocks")
}

// TestGeminiResponsesInputItemsPagination pins limit, after and order on the input-item list,
// with first_id, last_id and has_more taken from the page returned.
func TestGeminiResponsesInputItemsPagination(t *testing.T) {
	server, _ := newInteractionsTestServer(t, "application/json", func(interactionsCall) string {
		return `{"id":"v1_p","status":"completed","model":"gemini-3.8-flash","steps":[
			{"type":"user_input","content":[{"type":"text","text":"one"}]},
			{"type":"model_output","content":[{"type":"text","text":"a"}]},
			{"type":"user_input","content":[{"type":"text","text":"two"}]},
			{"type":"user_input","content":[{"type":"text","text":"three"}]}
		]}`
	})
	provider := NewGeminiProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL + "/v1beta"}}, nil)
	key := schemas.Key{Value: schemas.SecretVar{Val: "k"}, Models: []string{"*"}}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	texts := func(resp *schemas.BifrostResponsesInputItemsResponse) []string {
		var out []string
		for _, m := range resp.Data {
			out = append(out, *m.Content.ContentBlocks[0].Text)
		}
		return out
	}

	page, bifrostErr := provider.ResponsesInputItems(ctx, key, &schemas.BifrostResponsesInputItemsRequest{ResponseID: "v1_p", Order: "asc", Limit: schemas.Ptr(1)})
	require.Nil(t, bifrostErr)
	assert.Equal(t, []string{"one"}, texts(page))
	assert.True(t, page.HasMore)
	assert.Equal(t, page.FirstID, page.LastID)

	page, bifrostErr = provider.ResponsesInputItems(ctx, key, &schemas.BifrostResponsesInputItemsRequest{ResponseID: "v1_p", Order: "asc", After: page.LastID, Limit: schemas.Ptr(1)})
	require.Nil(t, bifrostErr)
	assert.Equal(t, []string{"two"}, texts(page))
	assert.True(t, page.HasMore)

	page, bifrostErr = provider.ResponsesInputItems(ctx, key, &schemas.BifrostResponsesInputItemsRequest{ResponseID: "v1_p", Order: "asc", After: page.LastID, Limit: schemas.Ptr(5)})
	require.Nil(t, bifrostErr)
	assert.Equal(t, []string{"three"}, texts(page))
	assert.False(t, page.HasMore)

	page, bifrostErr = provider.ResponsesInputItems(ctx, key, &schemas.BifrostResponsesInputItemsRequest{ResponseID: "v1_p", Order: "desc", Limit: schemas.Ptr(2)})
	require.Nil(t, bifrostErr)
	assert.Equal(t, []string{"three", "two"}, texts(page))
	assert.True(t, page.HasMore)

	// The Responses contract defaults to newest first.
	page, bifrostErr = provider.ResponsesInputItems(ctx, key, &schemas.BifrostResponsesInputItemsRequest{ResponseID: "v1_p", Limit: schemas.Ptr(1)})
	require.Nil(t, bifrostErr)
	assert.Equal(t, []string{"three"}, texts(page))

	// ...and to 20 items a page.
	var steps []string
	for i := 0; i < 21; i++ {
		steps = append(steps, fmt.Sprintf(`{"type":"user_input","content":[{"type":"text","text":"m%d"}]}`, i))
	}
	bigServer, _ := newInteractionsTestServer(t, "application/json", func(interactionsCall) string {
		return `{"id":"v1_big","status":"completed","model":"gemini-3.8-flash","steps":[` + strings.Join(steps, ",") + `]}`
	})
	bigProvider := NewGeminiProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: bigServer.URL + "/v1beta"}}, nil)
	page, bifrostErr = bigProvider.ResponsesInputItems(ctx, key, &schemas.BifrostResponsesInputItemsRequest{ResponseID: "v1_big"})
	require.Nil(t, bifrostErr)
	require.Len(t, page.Data, 20)
	assert.True(t, page.HasMore)
	assert.Equal(t, "m20", texts(page)[0])
	page, bifrostErr = bigProvider.ResponsesInputItems(ctx, key, &schemas.BifrostResponsesInputItemsRequest{ResponseID: "v1_big", After: page.LastID})
	require.Nil(t, bifrostErr)
	assert.Equal(t, []string{"m0"}, texts(page))
	assert.False(t, page.HasMore)
}

// TestToGeminiInteractionsRequestContinuationToken pins that a continuation token sent in
// extra_params reaches Google as the typed continuation_token field, with or without extra-params
// passthrough on.
func TestToGeminiInteractionsRequestContinuationToken(t *testing.T) {
	req := &schemas.BifrostResponsesRequest{
		Model:  "deep-research-preview-04-2026",
		Input:  []schemas.ResponsesMessage{interactionsUserText("carry on")},
		Params: &schemas.ResponsesParameters{PreviousResponseID: schemas.Ptr("v1_run"), ExtraParams: map[string]interface{}{"continuation_token": "tok_1"}},
	}
	native, err := ToGeminiInteractionsRequest(req)
	require.NoError(t, err)
	require.NotNil(t, native.ContinuationToken)
	assert.Equal(t, "tok_1", *native.ContinuationToken)
	assert.NotContains(t, native.ExtraParams, "continuation_token")

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	body, bifrostErr := buildGeminiInteractionsBody(ctx, req, false)
	require.Nil(t, bifrostErr)
	assert.Contains(t, string(body), `"continuation_token":"tok_1"`, "sent without x-bf-passthrough-extra-params")
}

// TestToGeminiInteractionsThinkingLevelClampedPerModel pins that a reasoning effort is clamped to a
// level the model accepts: gemini-3.8-flash has no "minimal", so it gets "low".
func TestToGeminiInteractionsThinkingLevelClampedPerModel(t *testing.T) {
	req := &schemas.BifrostResponsesRequest{
		Model:  "gemini-3.8-flash",
		Input:  []schemas.ResponsesMessage{interactionsUserText("hi")},
		Params: &schemas.ResponsesParameters{Reasoning: &schemas.ResponsesParametersReasoning{Effort: schemas.Ptr("minimal")}},
	}
	native, err := ToGeminiInteractionsRequest(req)
	require.NoError(t, err)
	require.NotNil(t, native.GenerationConfig)
	require.NotNil(t, native.GenerationConfig.ThinkingLevel)
	assert.Equal(t, "low", *native.GenerationConfig.ThinkingLevel)

	req.Model = "gemini-3-flash-preview"
	native, err = ToGeminiInteractionsRequest(req)
	require.NoError(t, err)
	assert.Equal(t, "minimal", *native.GenerationConfig.ThinkingLevel, "a model that accepts minimal keeps it")
}

// TestToGeminiInteractionsAllowedToolsUnrepresentableRefused pins that an allowed_tools choice
// naming a tool Interactions cannot name is refused instead of dropping the restriction.
func TestToGeminiInteractionsAllowedToolsUnrepresentableRefused(t *testing.T) {
	req := &schemas.BifrostResponsesRequest{
		Model: "gemini-3.8-flash",
		Input: []schemas.ResponsesMessage{interactionsUserText("hi")},
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeWebSearch}},
			ToolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{
				Type: schemas.ResponsesToolChoiceTypeAllowedTools, Mode: schemas.Ptr("required"),
				Tools: []schemas.ResponsesToolChoiceAllowedToolDef{{Type: "web_search"}},
			}},
		},
	}
	_, err := ToGeminiInteractionsRequest(req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "allowed_tools")

	req.Params.ToolChoice.ResponsesToolChoiceStruct.Tools = []schemas.ResponsesToolChoiceAllowedToolDef{{Type: "function", Name: schemas.Ptr("lookup")}}
	native, err := ToGeminiInteractionsRequest(req)
	require.NoError(t, err)
	assert.JSONEq(t, `{"allowed_tools":{"mode":"any","tools":["lookup"]}}`, string(native.GenerationConfig.ToolChoice))
}

// TestToGeminiInteractionsMCPApprovalRefused pins that an MCP tool is sent only when the caller
// explicitly allows it to run without approval: Interactions has no approval gate.
func TestToGeminiInteractionsMCPApprovalRefused(t *testing.T) {
	mcpReq := func(approval *schemas.ResponsesToolMCPAllowedToolsApprovalSetting) *schemas.BifrostResponsesRequest {
		return &schemas.BifrostResponsesRequest{
			Model: "gemini-3.8-flash",
			Input: []schemas.ResponsesMessage{interactionsUserText("hi")},
			Params: &schemas.ResponsesParameters{Tools: []schemas.ResponsesTool{{
				Type:             schemas.ResponsesToolTypeMCP,
				ResponsesToolMCP: &schemas.ResponsesToolMCP{ServerLabel: "tax", ServerURL: schemas.Ptr("https://mcp.example.com"), RequireApproval: approval},
			}}},
		}
	}
	for name, approval := range map[string]*schemas.ResponsesToolMCPAllowedToolsApprovalSetting{
		"unset (OpenAI default always)": nil,
		"always":                        {Setting: schemas.Ptr("always")},
		"per-tool always filter":        {Always: &schemas.ResponsesToolMCPAllowedToolsApprovalFilter{ToolNames: []string{"pay"}}},
	} {
		_, err := ToGeminiInteractionsRequest(mcpReq(approval))
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "require_approval", name)
	}
	_, err := ToGeminiInteractionsRequest(mcpReq(&schemas.ResponsesToolMCPAllowedToolsApprovalSetting{Setting: schemas.Ptr("never")}))
	require.NoError(t, err)
}

// TestGeminiInteractionsMCPNoneEntryRefused pins that a native MCP allowlist entry in mode none is
// not turned into permission to call the tool on another provider.
func TestGeminiInteractionsMCPNoneEntryRefused(t *testing.T) {
	var in GeminiInteractionsRequest
	require.NoError(t, sonic.Unmarshal([]byte(`{"model":"openai/gpt-5","input":"hi","tools":[{"type":"mcp_server","name":"tax","url":"https://mcp.example.com","allowed_tools":[{"mode":"none","tools":["lookup"]}]}]}`), &in))
	_, err := in.ToBifrostResponsesRequest()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "none")
}

// TestGeminiInteractionsGoogleSearchTypes pins the search modality in both directions: Responses
// search_content_types text/image and Interactions search_types web_search/image_search.
func TestGeminiInteractionsGoogleSearchTypes(t *testing.T) {
	req := &schemas.BifrostResponsesRequest{
		Model: "gemini-3.8-flash",
		Input: []schemas.ResponsesMessage{interactionsUserText("hi")},
		Params: &schemas.ResponsesParameters{Tools: []schemas.ResponsesTool{{
			Type: schemas.ResponsesToolTypeWebSearch, ResponsesToolWebSearch: &schemas.ResponsesToolWebSearch{SearchContentTypes: []string{"image"}},
		}}},
	}
	native, err := ToGeminiInteractionsRequest(req)
	require.NoError(t, err)
	assert.Contains(t, marshalInteractionsBody(t, native), `{"type":"google_search","search_types":["image_search"]}`)

	var in GeminiInteractionsRequest
	require.NoError(t, sonic.Unmarshal([]byte(`{"model":"openai/gpt-5","input":"hi","tools":[{"type":"google_search","search_types":["image_search","web_search"]}]}`), &in))
	out, convErr := in.ToBifrostResponsesRequest()
	require.NoError(t, convErr)
	require.NotNil(t, out.Params.Tools[0].ResponsesToolWebSearch)
	assert.Equal(t, []string{"image", "text"}, out.Params.Tools[0].ResponsesToolWebSearch.SearchContentTypes)

	require.NoError(t, sonic.Unmarshal([]byte(`{"model":"openai/gpt-5","input":"hi","tools":[{"type":"google_search","search_types":["enterprise_web_search"]}]}`), &in))
	_, convErr = in.ToBifrostResponsesRequest()
	require.Error(t, convErr, "a search type the target cannot honor is refused")
}

// TestGeminiInteractionsStreamStatusUpdateUsage pins that usage reported on a status update is kept,
// so a stream cut before interaction.completed still has the latest snapshot to log. A newer
// snapshot replaces an older one; snapshots are not added together.
func TestGeminiInteractionsStreamStatusUpdateUsage(t *testing.T) {
	events := interactionsEvents(t,
		`{"interaction":{"id":"v1_u","model":"gemini-3-flash-preview","status":"in_progress"},"event_type":"interaction.created"}`,
		`{"interaction_id":"v1_u","status":"in_progress","usage":{"total_input_tokens":100,"total_output_tokens":1,"total_tokens":101},"event_type":"interaction.status_update"}`,
		`{"interaction_id":"v1_u","status":"in_progress","usage":{"total_input_tokens":100,"total_output_tokens":9,"total_tokens":109},"event_type":"interaction.status_update"}`,
	)
	state := NewGeminiInteractionsStreamState("gemini-3-flash-preview")
	for _, event := range events {
		_, err := state.ToBifrostResponsesStream(event)
		require.Nil(t, err)
	}
	require.NotNil(t, state.Usage)
	assert.Equal(t, 109, state.Usage.TotalTokens)
}

// TestGeminiInteractionsStreamToolItemAddedIsSnapshot pins that the output_item.added event of a
// built-in tool call keeps the item as it was announced: the result that arrives later must not
// change the published event.
func TestGeminiInteractionsStreamToolItemAddedIsSnapshot(t *testing.T) {
	events := interactionsEvents(t,
		`{"interaction":{"id":"v1_c","model":"gemini-3-flash-preview"},"event_type":"interaction.created"}`,
		`{"index":0,"step":{"id":"call_c","type":"code_execution_call"},"event_type":"step.start"}`,
		`{"index":0,"delta":{"type":"code_execution_call","arguments":{"language":"python","code":"print(1)"}},"event_type":"step.delta"}`,
		`{"index":0,"event_type":"step.stop"}`,
		`{"index":1,"step":{"call_id":"call_c","type":"code_execution_result"},"event_type":"step.start"}`,
		`{"index":1,"delta":{"type":"code_execution_result","result":"1\n","is_error":false},"event_type":"step.delta"}`,
		`{"index":1,"event_type":"step.stop"}`,
	)
	state := NewGeminiInteractionsStreamState("gemini-3-flash-preview")
	var added, done *schemas.BifrostResponsesStreamResponse
	for _, event := range events {
		out, err := state.ToBifrostResponsesStream(event)
		require.Nil(t, err)
		for _, ev := range out {
			if ev.Item == nil || ev.Item.ResponsesToolMessage == nil || ev.Item.ResponsesToolMessage.ResponsesCodeInterpreterToolCall == nil {
				continue
			}
			switch ev.Type {
			case schemas.ResponsesStreamResponseTypeOutputItemAdded:
				added = ev
			case schemas.ResponsesStreamResponseTypeOutputItemDone:
				done = ev
			}
		}
	}
	require.NotNil(t, added)
	require.NotNil(t, done)
	// The result must have been applied to the call, or the snapshot check below proves nothing.
	outputs := done.Item.ResponsesToolMessage.ResponsesCodeInterpreterToolCall.Outputs
	require.Len(t, outputs, 1)
	require.NotNil(t, outputs[0].ResponsesCodeInterpreterOutputLogs)
	assert.Equal(t, "1\n", outputs[0].ResponsesCodeInterpreterOutputLogs.Logs)
	assert.Empty(t, added.Item.ResponsesToolMessage.ResponsesCodeInterpreterToolCall.Outputs, "the announced item does not gain the later result")
}

// TestNormalizeRawGeminiInteractionsBodyFallbackKind pins that the model/agent field follows the
// resolved target: an agent request that falls back to a model sends "model", and the reverse sends
// "agent". agent_config cannot go to a model, so that fallback is refused.
func TestNormalizeRawGeminiInteractionsBodyFallbackKind(t *testing.T) {
	out, err := normalizeRawGeminiInteractionsBody([]byte(`{"agent":"deep-research-preview-04-2026","input":"go","fallbacks":["gemini/gemini-3-flash-preview"]}`), "gemini-3-flash-preview", false)
	require.Nil(t, err)
	assert.JSONEq(t, `{"model":"gemini-3-flash-preview","input":"go"}`, string(out))

	out, err = normalizeRawGeminiInteractionsBody([]byte(`{"model":"gemini/gemini-3-flash-preview","input":"go"}`), "deep-research-preview-04-2026", true)
	require.Nil(t, err)
	assert.JSONEq(t, `{"agent":"deep-research-preview-04-2026","input":"go"}`, string(out))

	_, err = normalizeRawGeminiInteractionsBody([]byte(`{"agent":"deep-research-preview-04-2026","input":"go","agent_config":{"type":"deep-research"}}`), "gemini-3-flash-preview", false)
	require.NotNil(t, err)
	assert.Contains(t, err.Error.Message, "agent_config")
}

// TestGeminiInteractionsCodeExecutionImageOutput pins that a code_execution_result content array
// keeps its image as a code interpreter image output (and its text as logs), and that the image goes
// back out as an image block.
func TestGeminiInteractionsCodeExecutionImageOutput(t *testing.T) {
	body := `{"id":"v1_ci","object":"interaction","model":"gemini-3-flash-preview","status":"completed","steps":[
		{"type":"code_execution_call","id":"call_c","arguments":{"language":"python","code":"plot()"}},
		{"type":"code_execution_result","call_id":"call_c","result":[{"type":"text","text":"done\n"},{"type":"image","data":"QUJD","mime_type":"image/png"}]}
	]}`
	var interaction GeminiInteraction
	require.NoError(t, sonic.Unmarshal([]byte(body), &interaction))
	resp := interaction.ToBifrostResponsesResponse()
	require.Len(t, resp.Output, 1)
	ci := resp.Output[0].ResponsesToolMessage.ResponsesCodeInterpreterToolCall
	require.NotNil(t, ci)
	require.Len(t, ci.Outputs, 2)
	require.NotNil(t, ci.Outputs[0].ResponsesCodeInterpreterOutputLogs)
	assert.Equal(t, "done\n", ci.Outputs[0].ResponsesCodeInterpreterOutputLogs.Logs)
	require.NotNil(t, ci.Outputs[1].ResponsesCodeInterpreterOutputImage)
	assert.Equal(t, "data:image/png;base64,QUJD", ci.Outputs[1].ResponsesCodeInterpreterOutputImage.URL)

	steps := responsesServerToolItemToGeminiInteractionSteps(resp.Output[0])
	require.Len(t, steps, 2)
	assert.JSONEq(t, `[{"type":"text","text":"done\n"},{"type":"image","data":"QUJD","mime_type":"image/png"}]`, string(steps[1].Result))

	// A text-only run keeps its logs as a string result.
	logsOnly := resp.Output[0]
	logsOnly.ResponsesToolMessage = &schemas.ResponsesToolMessage{ResponsesCodeInterpreterToolCall: &schemas.ResponsesCodeInterpreterToolCall{
		Code: schemas.Ptr("print(1)"), Outputs: []schemas.ResponsesCodeInterpreterOutput{{ResponsesCodeInterpreterOutputLogs: &schemas.ResponsesCodeInterpreterOutputLogs{Type: "logs", Logs: "1\n"}}},
	}}
	steps = responsesServerToolItemToGeminiInteractionSteps(logsOnly)
	require.Len(t, steps, 2)
	assert.JSONEq(t, `"1\n"`, string(steps[1].Result))
}

// TestGeminiInteractionsMCPCaptionAndImage pins that an MCP result with a caption and an image keeps
// the whole content array, not just the caption.
func TestGeminiInteractionsMCPCaptionAndImage(t *testing.T) {
	body := `{"id":"v1_m2","object":"interaction","model":"gemini-3-flash-preview","status":"completed","steps":[
		{"type":"mcp_server_tool_call","id":"call_m","name":"chart","server_name":"viz","arguments":{}},
		{"type":"mcp_server_tool_result","call_id":"call_m","name":"chart","server_name":"viz","result":[{"type":"text","text":"Sales"},{"type":"image","data":"QUJD","mime_type":"image/png"}]}
	]}`
	var interaction GeminiInteraction
	require.NoError(t, sonic.Unmarshal([]byte(body), &interaction))
	resp := interaction.ToBifrostResponsesResponse()
	require.Len(t, resp.Output, 1)
	out := resp.Output[0].ResponsesToolMessage.Output
	require.NotNil(t, out)
	assert.JSONEq(t, `[{"type":"text","text":"Sales"},{"type":"image","data":"QUJD","mime_type":"image/png"}]`, *out.ResponsesToolCallOutputStr)
}

// TestGeminiInteractionsBearerTargetKeepsAuthorization pins the header precedence: a target that
// authenticates with its own Bearer token (Vertex) keeps it when an extra header also supplies
// x-goog-api-key, and the API key header is dropped instead.
func TestGeminiInteractionsBearerTargetKeepsAuthorization(t *testing.T) {
	// The handler runs on the server goroutine; the channel hands its capture to the test.
	captured := make(chan [2]string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured <- [2]string{r.Header.Get("Authorization"), r.Header.Get("x-goog-api-key")}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"v1_b","status":"completed"}`)
	}))
	defer server.Close()
	target := &GeminiInteractionsTarget{
		CollectionURL: server.URL + "/interactions",
		Headers:       map[string]string{"Authorization": "Bearer oauth-token"},
		ExtraHeaders:  map[string]schemas.SecretVar{"x-goog-api-key": {Val: "stray"}},
		Client:        &fasthttp.Client{},
		ProviderName:  schemas.Vertex,
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bifrostErr := InteractionsRetrieve(ctx, target, &schemas.BifrostResponsesRetrieveRequest{ResponseID: "v1_b"})
	require.Nil(t, bifrostErr)
	got := <-captured
	assert.Equal(t, "Bearer oauth-token", got[0])
	assert.Empty(t, got[1])
}

// TestGeminiInteractionsRefusesGenerateContentResponseID pins that a response id minted for a
// generateContent turn ("resp_...") is refused up front as a previous turn or a stored run, since
// no interaction exists for it on Google's side.
func TestGeminiInteractionsRefusesGenerateContentResponseID(t *testing.T) {
	_, err := ToGeminiInteractionsRequest(&schemas.BifrostResponsesRequest{
		Model:  "gemini-3.8-flash",
		Input:  []schemas.ResponsesMessage{interactionsUserText("and then?")},
		Params: &schemas.ResponsesParameters{PreviousResponseID: schemas.Ptr("resp_abc")},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "previous_response_id")

	server, calls := newInteractionsTestServer(t, "application/json", func(interactionsCall) string { return `{}` })
	provider := NewGeminiProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL + "/v1beta"}}, nil)
	key := schemas.Key{Value: schemas.SecretVar{Val: "k"}, Models: []string{"*"}}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bifrostErr := provider.ResponsesRetrieve(ctx, key, &schemas.BifrostResponsesRetrieveRequest{ResponseID: "resp_abc"})
	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.StatusCode)
	assert.Equal(t, http.StatusBadRequest, *bifrostErr.StatusCode)
	assert.Empty(t, *calls, "nothing is sent to Google")
}

// TestGeminiInteractionsFileSearchResults pins that file_search_result entries reach the
// file_search_call item, unary and streamed, and go back out as the same native entries.
func TestGeminiInteractionsFileSearchResults(t *testing.T) {
	entries := `[{"title":"guide.pdf","text":"Use the blue lever.","file_search_store":"fileSearchStores/s"}]`
	check := func(t *testing.T, item schemas.ResponsesMessage) {
		t.Helper()
		require.NotNil(t, item.ResponsesToolMessage)
		fs := item.ResponsesToolMessage.ResponsesFileSearchToolCall
		require.NotNil(t, fs)
		require.Len(t, fs.Results, 1)
		assert.Equal(t, "guide.pdf", *fs.Results[0].Filename)
		assert.Equal(t, "Use the blue lever.", *fs.Results[0].Text)
		require.NotNil(t, fs.Results[0].Attributes)
		assert.Equal(t, "fileSearchStores/s", (*fs.Results[0].Attributes)["file_search_store"])
		steps := responsesServerToolItemToGeminiInteractionSteps(item)
		require.Len(t, steps, 2)
		assert.JSONEq(t, entries, string(steps[1].Result), "the native entries go back out unchanged")
	}

	var interaction GeminiInteraction
	require.NoError(t, sonic.Unmarshal([]byte(`{"id":"v1_fs","object":"interaction","model":"gemini-3-flash-preview","status":"completed","steps":[
		{"type":"file_search_call","id":"call_f"},
		{"type":"file_search_result","call_id":"call_f","result":`+entries+`}
	]}`), &interaction))
	resp := interaction.ToBifrostResponsesResponse()
	require.Len(t, resp.Output, 1)
	check(t, resp.Output[0])

	events := interactionsEvents(t,
		`{"interaction":{"id":"v1_fs","model":"gemini-3-flash-preview"},"event_type":"interaction.created"}`,
		`{"index":0,"step":{"id":"call_f","type":"file_search_call"},"event_type":"step.start"}`,
		`{"index":0,"event_type":"step.stop"}`,
		`{"index":1,"step":{"call_id":"call_f","type":"file_search_result"},"event_type":"step.start"}`,
		`{"index":1,"delta":{"type":"file_search_result","result":`+entries+`},"event_type":"step.delta"}`,
		`{"index":1,"event_type":"step.stop"}`,
	)
	state := NewGeminiInteractionsStreamState("gemini-3-flash-preview")
	var done *schemas.ResponsesMessage
	for _, event := range events {
		out, err := state.ToBifrostResponsesStream(event)
		require.Nil(t, err)
		for _, ev := range out {
			if ev.Type == schemas.ResponsesStreamResponseTypeOutputItemDone && ev.Item != nil && ev.Item.Type != nil && *ev.Item.Type == schemas.ResponsesMessageTypeFileSearchCall {
				done = ev.Item
			}
		}
	}
	require.NotNil(t, done)
	check(t, *done)
}

// TestToGeminiInteractionsWebSearchRestrictionsRejected pins that a web_search restriction the
// interactions API cannot apply is refused rather than dropped: external_web_access false and
// domain or time range filters. An unrestricted search still maps to google_search.
func TestToGeminiInteractionsWebSearchRestrictionsRejected(t *testing.T) {
	webSearch := func(ws *schemas.ResponsesToolWebSearch) []schemas.ResponsesTool {
		return []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeWebSearch, ResponsesToolWebSearch: ws}}
	}
	for name, ws := range map[string]*schemas.ResponsesToolWebSearch{
		"external_web_access false": {ExternalWebAccess: schemas.Ptr(false)},
		"allowed domains":           {Filters: &schemas.ResponsesToolWebSearchFilters{AllowedDomains: []string{"example.com"}}},
		"blocked domains":           {Filters: &schemas.ResponsesToolWebSearchFilters{BlockedDomains: []string{"example.com"}}},
		"time range":                {Filters: &schemas.ResponsesToolWebSearchFilters{TimeRangeFilter: &schemas.Interval{}}},
	} {
		_, err := toGeminiInteractionsTools(webSearch(ws))
		require.Error(t, err, name)
	}
	for name, ws := range map[string]*schemas.ResponsesToolWebSearch{
		"external_web_access true": {ExternalWebAccess: schemas.Ptr(true)},
		"unset":                    {},
		"nil":                      nil,
	} {
		tools, err := toGeminiInteractionsTools(webSearch(ws))
		require.NoError(t, err, name)
		require.Len(t, tools, 1, name)
		assert.Equal(t, "google_search", tools[0].Type, name)
	}
}

// TestGeminiInteractionsMCPServerNeverApproval pins that a native mcp_server tool translates with
// require_approval "never": Google runs MCP tools without an approval step, and the Responses
// default of "always" would turn the call into an approval request the caller never sees.
func TestGeminiInteractionsMCPServerNeverApproval(t *testing.T) {
	var in GeminiInteractionsRequest
	require.NoError(t, sonic.Unmarshal([]byte(`{"model":"openai/gpt-5","input":"hi","tools":[{"type":"mcp_server","name":"s","url":"https://mcp.example"}]}`), &in))
	out, err := in.ToBifrostResponsesRequest()
	require.NoError(t, err)
	require.Len(t, out.Params.Tools, 1)
	mcp := out.Params.Tools[0].ResponsesToolMCP
	require.NotNil(t, mcp)
	require.NotNil(t, mcp.RequireApproval, "an unset approval defaults to always on OpenAI")
	require.NotNil(t, mcp.RequireApproval.Setting)
	assert.Equal(t, "never", *mcp.RequireApproval.Setting)

	// The translated tool goes back to the interactions API unchanged.
	_, err = toGeminiInteractionsTools(out.Params.Tools)
	require.NoError(t, err)
}

// TestGeminiResponsesBackgroundStreamEndsBeforeID pins that a background create stream that closes
// before naming its run is an error: there is no run to hand off, so an in_progress event with an
// empty id would leave the client nothing to poll.
func TestGeminiResponsesBackgroundStreamEndsBeforeID(t *testing.T) {
	server, _ := newInteractionsTestServer(t, "text/event-stream", func(interactionsCall) string { return ": keepalive\n\n" })
	provider := NewGeminiProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL + "/v1beta"}}, nil)
	key := schemas.Key{Value: schemas.SecretVar{Val: "k"}, Models: []string{"*"}}
	postHook := func(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return result, err
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyInteractionsAPI, true)
	req := &schemas.BifrostResponsesRequest{
		Model:  "gemini-3.8-flash",
		Input:  []schemas.ResponsesMessage{interactionsUserText("hi")},
		Params: &schemas.ResponsesParameters{Background: schemas.Ptr(true)},
	}
	stream, bifrostErr := provider.ResponsesStream(ctx, postHook, nil, key, req)
	require.Nil(t, bifrostErr)
	var streamErr *schemas.BifrostError
	for chunk := range stream {
		if chunk.BifrostError != nil {
			streamErr = chunk.BifrostError
			continue
		}
		if ev := chunk.BifrostResponsesStreamResponse; ev != nil && ev.Type == schemas.ResponsesStreamResponseTypeInProgress {
			id := ""
			if ev.Response != nil && ev.Response.ID != nil {
				id = *ev.Response.ID
			}
			assert.NotEmpty(t, id, "no in_progress handoff without a run id")
		}
	}
	require.NotNil(t, streamErr, "a background stream that ends before the run id is an error")
}

// TestGeminiInteractionsImageSearchQueries pins that an image search call keeps its queries apart
// from web search queries in both directions: search_type image_search maps to image_queries, and
// image_queries map back to an image_search call.
func TestGeminiInteractionsImageSearchQueries(t *testing.T) {
	step := GeminiInteractionsItem{
		Type: GeminiInteractionsStepGoogleSearchCall, ID: schemas.Ptr("s_img"),
		SearchType: schemas.Ptr("image_search"), Arguments: json.RawMessage(`{"queries":["red panda"]}`),
	}
	items := geminiServerToolCallToResponses(step)
	require.Len(t, items, 1)
	require.NotNil(t, items[0].ResponsesToolMessage)
	require.NotNil(t, items[0].ResponsesToolMessage.Action)
	action := items[0].ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction
	require.NotNil(t, action)
	assert.Equal(t, []string{"red panda"}, action.ImageQueries)
	assert.Empty(t, action.Queries, "image queries are not web queries")
	assert.Nil(t, action.Query)

	steps := responsesServerToolItemToGeminiInteractionSteps(items[0])
	require.NotEmpty(t, steps)
	require.NotNil(t, steps[0].SearchType)
	assert.Equal(t, "image_search", *steps[0].SearchType)
	assert.JSONEq(t, `{"queries":["red panda"]}`, string(steps[0].Arguments))
}

// TestGeminiInteractionsURLContextResultNoStatus pins that a url_context result entry with no
// status is not read as a failure; only a stated non-success status or is_error fails the fetch.
func TestGeminiInteractionsURLContextResultNoStatus(t *testing.T) {
	fetchStatus := func(result string, isError bool) string {
		body := `{"id":"v1_u","object":"interaction","model":"gemini-3-flash-preview","status":"completed","steps":[
			{"type":"url_context_call","id":"call_u","arguments":{"urls":["https://a.example"]}},
			{"type":"url_context_result","call_id":"call_u","result":` + result + fmt.Sprintf(`,"is_error":%t}]}`, isError)
		var interaction GeminiInteraction
		require.NoError(t, sonic.Unmarshal([]byte(body), &interaction))
		resp := interaction.ToBifrostResponsesResponse()
		for _, item := range resp.Output {
			if item.Type != nil && *item.Type == schemas.ResponsesMessageTypeWebFetchCall {
				require.NotNil(t, item.Status)
				return *item.Status
			}
		}
		t.Fatal("no web_fetch_call item")
		return ""
	}
	assert.Equal(t, "completed", fetchStatus(`[{"url":"https://a.example"}]`, false), "no status is not a failure")
	assert.Equal(t, "failed", fetchStatus(`[{"url":"https://a.example"}]`, true), "is_error still fails the fetch")
	assert.Equal(t, "failed", fetchStatus(`[{"url":"https://a.example","status":"error"}]`, false))
}

// TestGeminiResponsesBackgroundStreamStepStopUsage pins that usage reported on step.stop is kept
// as the newest snapshot, so a background stream cut after it hands off the run with that usage.
func TestGeminiResponsesBackgroundStreamStepStopUsage(t *testing.T) {
	sse := strings.Join([]string{
		"event: interaction.created", `data: {"event_type":"interaction.created","interaction":{"id":"v1_ss","status":"in_progress","model":"gemini-3.8-flash"}}`, "",
		"event: step.start", `data: {"event_type":"step.start","index":0,"step":{"type":"model_output"}}`, "",
		"event: step.delta", `data: {"event_type":"step.delta","index":0,"delta":{"type":"text","text":"Hi"}}`, "",
		"event: step.stop", `data: {"event_type":"step.stop","index":0,"usage":{"total_input_tokens":40,"total_output_tokens":10,"total_tokens":50}}`, "",
	}, "\n")
	server, _ := newInteractionsTestServer(t, "text/event-stream", func(interactionsCall) string { return sse })
	provider := NewGeminiProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL + "/v1beta"}}, nil)
	key := schemas.Key{Value: schemas.SecretVar{Val: "k"}, Models: []string{"*"}}
	postHook := func(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return result, err
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyInteractionsAPI, true)
	req := &schemas.BifrostResponsesRequest{
		Model:  "gemini-3.8-flash",
		Input:  []schemas.ResponsesMessage{interactionsUserText("hi")},
		Params: &schemas.ResponsesParameters{Background: schemas.Ptr(true)},
	}
	stream, bifrostErr := provider.ResponsesStream(ctx, postHook, nil, key, req)
	require.Nil(t, bifrostErr)
	var handoff *schemas.BifrostResponsesStreamResponse
	for chunk := range stream {
		require.Nil(t, chunk.BifrostError)
		if chunk.BifrostResponsesStreamResponse != nil {
			handoff = chunk.BifrostResponsesStreamResponse
		}
	}
	require.NotNil(t, handoff)
	require.Equal(t, schemas.ResponsesStreamResponseTypeInProgress, handoff.Type)
	require.NotNil(t, handoff.Response)
	require.NotNil(t, handoff.Response.ProviderJob)
	require.NotNil(t, handoff.Response.ProviderJob.Usage, "the step.stop usage reaches the run")
	require.NotNil(t, handoff.Response.ProviderJob.Usage.LLMUsage)
	assert.Equal(t, 50, handoff.Response.ProviderJob.Usage.LLMUsage.TotalTokens)
}

// TestGeminiInteractionsStreamDeltaForUnknownStep pins a retrieve stream resumed after step.start:
// a text delta for a step the stream never announced opens that step first, so the text is
// streamed and lands in the final output instead of being dropped.
func TestGeminiInteractionsStreamDeltaForUnknownStep(t *testing.T) {
	events := interactionsEvents(t,
		`{"interaction":{"id":"v1_r","model":"gemini-3-flash-preview","status":"in_progress"},"event_type":"interaction.created"}`,
		`{"index":0,"delta":{"type":"text","text":"Hi"},"event_type":"step.delta"}`,
		`{"index":0,"event_type":"step.stop"}`,
		`{"interaction":{"id":"v1_r","model":"gemini-3-flash-preview","status":"completed"},"event_type":"interaction.completed"}`,
	)
	state := NewGeminiInteractionsStreamState("gemini-3-flash-preview")
	var types []schemas.ResponsesStreamResponseType
	var textDelta string
	var final *schemas.BifrostResponsesResponse
	for _, event := range events {
		out, err := state.ToBifrostResponsesStream(event)
		require.Nil(t, err)
		for _, ev := range out {
			types = append(types, ev.Type)
			if ev.Type == schemas.ResponsesStreamResponseTypeOutputTextDelta && ev.Delta != nil {
				textDelta += *ev.Delta
			}
			if ev.Type == schemas.ResponsesStreamResponseTypeCompleted {
				final = ev.Response
			}
		}
	}
	assert.Equal(t, "Hi", textDelta, "the delta is streamed")
	added := -1
	for i, typ := range types {
		if typ == schemas.ResponsesStreamResponseTypeOutputItemAdded {
			added = i
			break
		}
	}
	require.NotEqual(t, -1, added, "the step is opened")
	assert.Less(t, added, slices.Index(types, schemas.ResponsesStreamResponseTypeOutputTextDelta), "the item is added before its delta")
	require.NotNil(t, final)
	require.Len(t, final.Output, 1)
	require.NotNil(t, final.Output[0].Content)
	require.Len(t, final.Output[0].Content.ContentBlocks, 1)
	require.NotNil(t, final.Output[0].Content.ContentBlocks[0].Text)
	assert.Equal(t, "Hi", *final.Output[0].Content.ContentBlocks[0].Text)
}

// TestGeminiInteractionsStreamMediaThenTextOrder pins that a model_output step keeps its parts in
// the order they arrived: an image followed by text closes as [image, text], and the text events
// carry the text part's real content index.
func TestGeminiInteractionsStreamMediaThenTextOrder(t *testing.T) {
	events := interactionsEvents(t,
		`{"interaction":{"id":"v1_m","model":"gemini-3-flash-preview","status":"in_progress"},"event_type":"interaction.created"}`,
		`{"index":0,"step":{"type":"model_output"},"event_type":"step.start"}`,
		`{"index":0,"delta":{"type":"image","data":"QUJD","mime_type":"image/png"},"event_type":"step.delta"}`,
		`{"index":0,"delta":{"type":"text","text":"cap"},"event_type":"step.delta"}`,
		`{"index":0,"event_type":"step.stop"}`,
	)
	state := NewGeminiInteractionsStreamState("gemini-3-flash-preview")
	var done *schemas.BifrostResponsesStreamResponse
	textEvents := 0
	for _, event := range events {
		out, err := state.ToBifrostResponsesStream(event)
		require.Nil(t, err)
		for _, ev := range out {
			switch ev.Type {
			case schemas.ResponsesStreamResponseTypeContentPartAdded, schemas.ResponsesStreamResponseTypeOutputTextDelta,
				schemas.ResponsesStreamResponseTypeOutputTextDone, schemas.ResponsesStreamResponseTypeContentPartDone:
				textEvents++
				require.NotNil(t, ev.ContentIndex, ev.Type)
				assert.Equal(t, 1, *ev.ContentIndex, "%s carries the text part's content index", ev.Type)
			case schemas.ResponsesStreamResponseTypeOutputItemDone:
				done = ev
			}
		}
	}
	assert.Equal(t, 4, textEvents)
	require.NotNil(t, done)
	require.NotNil(t, done.Item.Content)
	blocks := done.Item.Content.ContentBlocks
	require.Len(t, blocks, 2)
	assert.Equal(t, schemas.ResponsesInputMessageContentBlockTypeImage, blocks[0].Type, "the image came first")
	assert.Equal(t, schemas.ResponsesOutputMessageContentTypeText, blocks[1].Type)
	require.NotNil(t, blocks[1].Text)
	assert.Equal(t, "cap", *blocks[1].Text)
}

// TestGeminiInteractionsTypedRequestStripsGooglePrefix pins that the typed Interactions converter
// sends the same model id as generateContent: a google/ prefix is stripped.
func TestGeminiInteractionsTypedRequestStripsGooglePrefix(t *testing.T) {
	out, err := ToGeminiInteractionsRequest(&schemas.BifrostResponsesRequest{
		Model: "google/gemini-3.8-flash",
		Input: []schemas.ResponsesMessage{interactionsUserText("hi")},
	})
	require.NoError(t, err)
	assert.Equal(t, "gemini-3.8-flash", out.Model)
}

// TestGeminiInteractionsRetrieveStreamSeedsRunID pins that a resumed retrieve stream whose first
// event is step.start still names the requested run on its envelopes and item ids.
func TestGeminiInteractionsRetrieveStreamSeedsRunID(t *testing.T) {
	sse := strings.Join([]string{
		"event: step.start", `data: {"event_type":"step.start","index":0,"step":{"type":"model_output"}}`, "",
		"event: step.delta", `data: {"event_type":"step.delta","index":0,"delta":{"type":"text","text":"Hi"}}`, "",
		"event: step.stop", `data: {"event_type":"step.stop","index":0}`, "",
		"event: interaction.completed", `data: {"event_type":"interaction.completed","interaction":{"id":"v1_rs","status":"completed","model":"gemini-3.8-flash"}}`, "",
	}, "\n")
	server, _ := newInteractionsTestServer(t, "text/event-stream", func(interactionsCall) string { return sse })
	provider := NewGeminiProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL + "/v1beta"}}, nil)
	key := schemas.Key{Value: schemas.SecretVar{Val: "k"}, Models: []string{"*"}}
	postHook := func(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return result, err
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	stream, bifrostErr := provider.ResponsesRetrieveStream(ctx, postHook, nil, key, &schemas.BifrostResponsesRetrieveRequest{ResponseID: "v1_rs", LastEventID: schemas.Ptr("evt_3")})
	require.Nil(t, bifrostErr)
	var createdID, itemID string
	for chunk := range stream {
		require.Nil(t, chunk.BifrostError)
		ev := chunk.BifrostResponsesStreamResponse
		if ev == nil {
			continue
		}
		if ev.Type == schemas.ResponsesStreamResponseTypeCreated && ev.Response != nil && ev.Response.ID != nil {
			createdID = *ev.Response.ID
		}
		if ev.Type == schemas.ResponsesStreamResponseTypeOutputItemAdded && ev.Item != nil && ev.Item.ID != nil {
			itemID = *ev.Item.ID
		}
	}
	assert.Equal(t, "v1_rs", createdID, "response.created names the requested run")
	assert.Equal(t, "item_v1_rs_0", itemID, "item ids are built from the run id")
}

// TestGeminiInteractionsStreamStepStartContent pins that text and a thought summary sent inside
// step.start are kept: they stream as deltas and reach the final output when
// interaction.completed carries no steps.
func TestGeminiInteractionsStreamStepStartContent(t *testing.T) {
	events := interactionsEvents(t,
		`{"interaction":{"id":"v1_ssc","model":"gemini-3-flash-preview","status":"in_progress"},"event_type":"interaction.created"}`,
		`{"index":0,"step":{"type":"thought","summary":[{"type":"text","text":"Plan it"}]},"event_type":"step.start"}`,
		`{"index":0,"event_type":"step.stop"}`,
		`{"index":1,"step":{"type":"model_output","content":[{"type":"text","text":"Hel"}]},"event_type":"step.start"}`,
		`{"index":1,"delta":{"type":"text","text":"lo"},"event_type":"step.delta"}`,
		`{"index":1,"event_type":"step.stop"}`,
		`{"interaction":{"id":"v1_ssc","model":"gemini-3-flash-preview","status":"completed"},"event_type":"interaction.completed"}`,
	)
	state := NewGeminiInteractionsStreamState("gemini-3-flash-preview")
	var textDelta string
	var final *schemas.BifrostResponsesResponse
	for _, event := range events {
		out, err := state.ToBifrostResponsesStream(event)
		require.Nil(t, err)
		for _, ev := range out {
			if ev.Type == schemas.ResponsesStreamResponseTypeOutputTextDelta && ev.Delta != nil {
				textDelta += *ev.Delta
			}
			if ev.Type == schemas.ResponsesStreamResponseTypeCompleted {
				final = ev.Response
			}
		}
	}
	assert.Equal(t, "Hello", textDelta, "the start text streams before the suffix")
	require.NotNil(t, final)
	var text, summary string
	for _, item := range final.Output {
		if item.Content != nil {
			for _, block := range item.Content.ContentBlocks {
				if block.Text != nil {
					text += *block.Text
				}
			}
		}
		if item.ResponsesReasoning != nil {
			for _, s := range item.ResponsesReasoning.Summary {
				summary += s.Text
			}
		}
	}
	assert.Equal(t, "Hello", text)
	assert.Equal(t, "Plan it", summary)
}

// TestGeminiResponsesBackgroundQueuedIsNotTerminal pins that a queued run is work still to poll,
// on both create paths: the unary create records an open job, and the shared status
// classification agrees.
func TestGeminiResponsesBackgroundQueuedIsNotTerminal(t *testing.T) {
	assert.False(t, InteractionsPassthroughInfo{Status: "queued"}.terminal())
	server, _ := newInteractionsTestServer(t, "application/json", func(interactionsCall) string {
		return `{"id":"v1_q","object":"interaction","model":"gemini-3.8-flash","status":"queued"}`
	})
	provider := NewGeminiProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL + "/v1beta"}}, nil)
	key := schemas.Key{Value: schemas.SecretVar{Val: "k"}, Models: []string{"*"}}
	resp, bifrostErr := provider.Responses(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), key, &schemas.BifrostResponsesRequest{
		Model:  "gemini-3.8-flash",
		Input:  []schemas.ResponsesMessage{interactionsUserText("hi")},
		Params: &schemas.ResponsesParameters{Background: schemas.Ptr(true)},
	})
	require.Nil(t, bifrostErr)
	require.NotNil(t, resp.ProviderJob, "a queued run is recorded for polling")
	assert.False(t, resp.ProviderJob.Terminal)
}

// streamHeldOpen runs a background streamed create against an upstream that sends sse and then
// keeps the body open until the test ends. It returns every chunk, or fails the test when the
// response channel is still open after 3 seconds.
func streamHeldOpen(t *testing.T, sse string) []*schemas.BifrostStreamChunk {
	t.Helper()
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })
	provider := NewGeminiProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL + "/v1beta"}}, nil)
	key := schemas.Key{Value: schemas.SecretVar{Val: "k"}, Models: []string{"*"}}
	postHook := func(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return result, err
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyJobAccounting, true)
	ctx.SetValue(schemas.BifrostContextKeyInteractionsAPI, true)
	// The watchdog starts before dispatch, so a ResponsesStream that waits for the body to end
	// before returning fails here too, not only a returned stream that never closes.
	watchdog := time.After(3 * time.Second)
	var mu sync.Mutex
	var chunks []*schemas.BifrostStreamChunk
	var bifrostErr *schemas.BifrostError
	done := make(chan struct{})
	go func() {
		defer close(done)
		stream, err := provider.ResponsesStream(ctx, postHook, nil, key, &schemas.BifrostResponsesRequest{
			Model:  "gemini-3.8-flash",
			Input:  []schemas.ResponsesMessage{interactionsUserText("hi")},
			Params: &schemas.ResponsesParameters{Background: schemas.Ptr(true)},
		})
		if err != nil {
			mu.Lock()
			bifrostErr = err
			mu.Unlock()
			return
		}
		for chunk := range stream {
			mu.Lock()
			chunks = append(chunks, chunk)
			mu.Unlock()
		}
	}()
	select {
	case <-done:
	case <-watchdog:
		t.Fatal("the stream did not end within 3s while the upstream held the body")
	}
	mu.Lock()
	defer mu.Unlock()
	require.Nil(t, bifrostErr)
	return chunks
}

// TestGeminiInteractionsStreamEndsAfterTerminalFrame pins that the response channel closes soon
// after interaction.completed even when the upstream keeps the body open, and that the run's
// completion is what the caller received last.
func TestGeminiInteractionsStreamEndsAfterTerminalFrame(t *testing.T) {
	chunks := streamHeldOpen(t, strings.Join([]string{
		"event: interaction.created", `data: {"event_type":"interaction.created","interaction":{"id":"v1_hold","status":"in_progress","model":"gemini-3.8-flash"}}`, "",
		"event: interaction.completed", `data: {"event_type":"interaction.completed","interaction":{"id":"v1_hold","status":"completed","model":"gemini-3.8-flash"}}`, "", "",
	}, "\n"))
	require.NotEmpty(t, chunks)
	for _, chunk := range chunks {
		require.Nil(t, chunk.BifrostError)
	}
	last := chunks[len(chunks)-1].BifrostResponsesStreamResponse
	require.NotNil(t, last)
	assert.Equal(t, schemas.ResponsesStreamResponseTypeCompleted, last.Type)
	require.NotNil(t, last.Response)
	require.NotNil(t, last.Response.ID)
	assert.Equal(t, "v1_hold", *last.Response.ID)
}

// TestGeminiInteractionsStreamEndsAfterErrorEvent pins that an error event closes the response
// channel even when the upstream keeps the body open, and that the caller gets the error.
func TestGeminiInteractionsStreamEndsAfterErrorEvent(t *testing.T) {
	chunks := streamHeldOpen(t, strings.Join([]string{
		"event: interaction.created", `data: {"event_type":"interaction.created","interaction":{"id":"v1_err","status":"in_progress","model":"gemini-3.8-flash"}}`, "",
		"event: error", `data: {"event_type":"error","error":{"code":"internal","message":"model overloaded"}}`, "", "",
	}, "\n"))
	require.NotEmpty(t, chunks)
	last := chunks[len(chunks)-1]
	require.NotNil(t, last.BifrostError)
	require.NotNil(t, last.BifrostError.Error)
	assert.Equal(t, "model overloaded", last.BifrostError.Error.Message)
}

// TestGeminiResponsesBackgroundStreamContinuationRecordsJob pins that a streamed create stopping at
// requires_continuation records its run for settlement, as the unary create does, on both exits:
// a completion frame with that status, and EOF after a status update to it. The public incomplete
// event is kept and the usage moves onto the run.
func TestGeminiResponsesBackgroundStreamContinuationRecordsJob(t *testing.T) {
	usage := `"usage":{"total_input_tokens":30,"total_output_tokens":10,"total_tokens":40}`
	for name, tail := range map[string]string{
		"completion frame": "event: interaction.completed\ndata: {\"event_type\":\"interaction.completed\",\"interaction\":{\"id\":\"v1_cp\",\"status\":\"requires_continuation\",\"model\":\"gemini-3.8-flash\"," + usage + "}}\n\n",
		"status then EOF":  "event: interaction.status_update\ndata: {\"event_type\":\"interaction.status_update\",\"interaction_id\":\"v1_cp\",\"status\":\"requires_continuation\"," + usage + "}\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			sse := "event: interaction.created\ndata: {\"event_type\":\"interaction.created\",\"interaction\":{\"id\":\"v1_cp\",\"status\":\"in_progress\",\"model\":\"gemini-3.8-flash\"}}\n\n" + tail
			server, _ := newInteractionsTestServer(t, "text/event-stream", func(interactionsCall) string { return sse })
			provider := NewGeminiProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL + "/v1beta"}}, nil)
			key := schemas.Key{Value: schemas.SecretVar{Val: "k"}, Models: []string{"*"}}
			postHook := func(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
				return result, err
			}
			ctx := accountedCtx()
			ctx.SetValue(schemas.BifrostContextKeyInteractionsAPI, true)
			stream, bifrostErr := provider.ResponsesStream(ctx, postHook, nil, key, &schemas.BifrostResponsesRequest{
				Model:  "gemini-3.8-flash",
				Input:  []schemas.ResponsesMessage{interactionsUserText("hi")},
				Params: &schemas.ResponsesParameters{Background: schemas.Ptr(true)},
			})
			require.Nil(t, bifrostErr)
			var last *schemas.BifrostResponsesStreamResponse
			for chunk := range stream {
				require.Nil(t, chunk.BifrostError)
				if chunk.BifrostResponsesStreamResponse != nil {
					last = chunk.BifrostResponsesStreamResponse
				}
			}
			require.NotNil(t, last)
			assert.Equal(t, schemas.ResponsesStreamResponseTypeIncomplete, last.Type, "the checkpoint stays public")
			require.NotNil(t, last.Response)
			require.NotNil(t, last.Response.ProviderJob, "the run is recorded for settlement")
			assert.Equal(t, "v1_cp", last.Response.ProviderJob.ID)
			assert.False(t, last.Response.ProviderJob.Terminal)
			require.NotNil(t, last.Response.ProviderJob.Usage)
			assert.Equal(t, 40, last.Response.ProviderJob.Usage.LLMUsage.TotalTokens)
			assert.Nil(t, last.Response.Usage, "the usage moved onto the run")
		})
	}
}

// TestGeminiInteractionsWebSearchOpenPage pins that an OpenAI open_page action is not reported as
// a Google Search: it becomes a url_context step for its URL, and a find action, which has no
// Interactions equivalent, emits no step.
func TestGeminiInteractionsWebSearchOpenPage(t *testing.T) {
	item := func(action schemas.ResponsesWebSearchToolCallAction) schemas.ResponsesMessage {
		typ := schemas.ResponsesMessageTypeWebSearchCall
		return schemas.ResponsesMessage{ID: schemas.Ptr("ws_1"), Type: &typ, Status: schemas.Ptr("completed"), ResponsesToolMessage: &schemas.ResponsesToolMessage{
			Action: &schemas.ResponsesToolMessageActionStruct{ResponsesWebSearchToolCallAction: &action},
		}}
	}
	steps := responsesServerToolItemToGeminiInteractionSteps(item(schemas.ResponsesWebSearchToolCallAction{Type: "open_page", URL: schemas.Ptr("https://a.example/page")}))
	require.Len(t, steps, 2)
	assert.Equal(t, GeminiInteractionsStepURLContextCall, steps[0].Type)
	assert.JSONEq(t, `{"urls":["https://a.example/page"]}`, string(steps[0].Arguments))
	assert.Equal(t, GeminiInteractionsStepURLContextResult, steps[1].Type)

	steps = responsesServerToolItemToGeminiInteractionSteps(item(schemas.ResponsesWebSearchToolCallAction{Type: "find", URL: schemas.Ptr("https://a.example/page"), Pattern: schemas.Ptr("x")}))
	assert.Empty(t, steps, "a find action has no Interactions step")

	steps = responsesServerToolItemToGeminiInteractionSteps(item(schemas.ResponsesWebSearchToolCallAction{Type: "search", Query: schemas.Ptr("fx")}))
	require.NotEmpty(t, steps)
	assert.Equal(t, GeminiInteractionsStepGoogleSearchCall, steps[0].Type)
}

// TestGeminiInteractionsFileSearchMetadataFilterRefused pins that a native file_search restricted
// with metadata_filter is refused on translation instead of searching documents the caller
// excluded. An unfiltered file_search still translates.
func TestGeminiInteractionsFileSearchMetadataFilterRefused(t *testing.T) {
	var filtered GeminiInteractionsRequest
	require.NoError(t, sonic.Unmarshal([]byte(`{"model":"gemini-3.8-flash","input":"hi","tools":[{"type":"file_search","file_search_store_names":["fileSearchStores/a"],"metadata_filter":"author = \"x\""}]}`), &filtered))
	_, err := filtered.ToBifrostResponsesRequest()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "metadata_filter")

	var plain GeminiInteractionsRequest
	require.NoError(t, sonic.Unmarshal([]byte(`{"model":"gemini-3.8-flash","input":"hi","tools":[{"type":"file_search","file_search_store_names":["fileSearchStores/a"]}]}`), &plain))
	_, err = plain.ToBifrostResponsesRequest()
	require.NoError(t, err)
}

// TestGeminiInteractionsWebSearchSources pins that a translated web_search_call keeps its URL
// sources as google_search_result entries; a source with no URL is skipped and no sources stays [].
func TestGeminiInteractionsWebSearchSources(t *testing.T) {
	item := func(sources []schemas.ResponsesWebSearchToolCallActionSearchSource) schemas.ResponsesMessage {
		typ := schemas.ResponsesMessageTypeWebSearchCall
		return schemas.ResponsesMessage{ID: schemas.Ptr("ws_s"), Type: &typ, Status: schemas.Ptr("completed"), ResponsesToolMessage: &schemas.ResponsesToolMessage{
			Action: &schemas.ResponsesToolMessageActionStruct{ResponsesWebSearchToolCallAction: &schemas.ResponsesWebSearchToolCallAction{Type: "search", Query: schemas.Ptr("fx"), Sources: sources}},
		}}
	}
	steps := responsesServerToolItemToGeminiInteractionSteps(item([]schemas.ResponsesWebSearchToolCallActionSearchSource{
		{Type: "url", URL: "https://a.example", Title: schemas.Ptr("A")},
		{Type: "api", Name: "oai-weather"},
		{Type: "url", URL: "https://b.example"},
	}))
	require.Len(t, steps, 2)
	assert.JSONEq(t, `[{"url":"https://a.example","title":"A"},{"url":"https://b.example"}]`, string(steps[1].Result))

	steps = responsesServerToolItemToGeminiInteractionSteps(item(nil))
	require.Len(t, steps, 2)
	assert.JSONEq(t, `[]`, string(steps[1].Result))
}

// An Antigravity continuation needs environment, which Google takes as a top-level field. It
// arrives in ExtraParams and must become the typed field, so it reaches Google even when extra
// params do not pass through. A request without it is unchanged.
func TestGeminiInteractionsEnvironmentIsTyped(t *testing.T) {
	req := &schemas.BifrostResponsesRequest{
		Model: "gemini-3.8-flash",
		Input: []schemas.ResponsesMessage{interactionsUserText("go on")},
		Params: &schemas.ResponsesParameters{
			PreviousResponseID: schemas.Ptr("v1_agent"),
			ExtraParams:        map[string]interface{}{"environment": "remote"},
		},
	}
	body, bErr := buildGeminiInteractionsBody(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), req, false)
	require.Nil(t, bErr)
	assert.Equal(t, "remote", gjson.GetBytes(body, "environment").String(), string(body))

	req.Params.ExtraParams = nil
	body, bErr = buildGeminiInteractionsBody(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), req, false)
	require.Nil(t, bErr)
	assert.False(t, gjson.GetBytes(body, "environment").Exists(), string(body))
}

// Extra params pass through after the converter runs, so a caller's stream:false must not turn
// a streaming call into a unary body: Google picks SSE from the body's stream field.
func TestBuildGeminiInteractionsBodyTypedStreamKeepsStreamFlag(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
	req := &schemas.BifrostResponsesRequest{
		Model:  "gemini-3.8-flash",
		Input:  []schemas.ResponsesMessage{interactionsUserText("hi")},
		Params: &schemas.ResponsesParameters{ExtraParams: map[string]interface{}{"stream": false}},
	}
	out, bErr := buildGeminiInteractionsBody(ctx, req, true)
	require.Nil(t, bErr)
	assert.True(t, gjson.GetBytes(out, "stream").Bool(), string(out))

	unary, bErr := buildGeminiInteractionsBody(ctx, req, false)
	require.Nil(t, bErr)
	assert.False(t, gjson.GetBytes(unary, "stream").Bool(), "a unary body keeps the caller's field: "+string(unary))
}

// A function_result whose blocks have no Responses form still lists, with Google's result kept
// as the output text, rather than vanishing from a successful page.
func TestGeminiResponsesInputItemsKeepsUnconvertibleFunctionResult(t *testing.T) {
	server, _ := newInteractionsTestServer(t, "application/json", func(interactionsCall) string {
		return `{"id":"v1_f","status":"completed","model":"gemini-3.8-flash","steps":[
			{"type":"user_input","content":[{"type":"text","text":"one"}]},
			{"type":"function_call","id":"call_f","name":"lookup","arguments":{}},
			{"type":"function_result","call_id":"call_f","name":"lookup","result":[{"type":"function","name":"nested"}]}
		]}`
	})
	provider := NewGeminiProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL + "/v1beta"}}, nil)
	key := schemas.Key{Value: schemas.SecretVar{Val: "k"}, Models: []string{"*"}}
	page, bErr := provider.ResponsesInputItems(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), key,
		&schemas.BifrostResponsesInputItemsRequest{ResponseID: "v1_f", Order: "asc"})
	require.Nil(t, bErr)
	require.Len(t, page.Data, 2, "the function result is listed")
	result := page.Data[1]
	require.NotNil(t, result.Type)
	assert.Equal(t, schemas.ResponsesMessageTypeFunctionCallOutput, *result.Type)
	require.NotNil(t, result.ResponsesToolMessage)
	require.NotNil(t, result.CallID)
	assert.Equal(t, "call_f", *result.CallID)
	require.NotNil(t, result.Output)
	require.NotNil(t, result.Output.ResponsesToolCallOutputStr)
	assert.JSONEq(t, `[{"type":"function","name":"nested"}]`, *result.Output.ResponsesToolCallOutputStr)
}

// A stream that cannot reach Google is an upstream connection failure, which max_retries
// retries, not a Bifrost-side error, which it does not.
func TestGeminiInteractionsStreamConnectFailureIsUpstream(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()
	provider := NewGeminiProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: url + "/v1beta"}}, nil)
	key := schemas.Key{Value: schemas.SecretVar{Val: "k"}, Models: []string{"*"}}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyInteractionsAPI, true)
	postHook := func(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return result, err
	}
	_, bErr := provider.ResponsesStream(ctx, postHook, nil, key, &schemas.BifrostResponsesRequest{
		Model: "gemini-3.8-flash", Input: []schemas.ResponsesMessage{interactionsUserText("hi")},
	})
	require.NotNil(t, bErr)
	assert.False(t, bErr.IsBifrostError, "a refused connection is the upstream's failure")
	require.NotNil(t, bErr.StatusCode)
	assert.Equal(t, 502, *bErr.StatusCode)
	require.NotNil(t, bErr.Error.Type)
	assert.Equal(t, schemas.ProviderConnectionFailed, *bErr.Error.Type)
}

// A failed mcp_call with no output still reports its failure: the result step carries is_error
// and the error text. A pending call with no result stays a call alone.
func TestGeminiInteractionsFailedMCPCallWithoutOutput(t *testing.T) {
	failed := schemas.ResponsesMessage{
		Type:   schemas.Ptr(schemas.ResponsesMessageTypeMCPCall),
		ID:     schemas.Ptr("mcp_1"),
		Status: schemas.Ptr("failed"),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			Name: schemas.Ptr("lookup"), ServerLabel: schemas.Ptr("crm"), Arguments: schemas.Ptr(`{"id":7}`),
			Error: &schemas.ResponsesToolMessageError{ResponsesToolMessageErrorStr: schemas.Ptr("crm unreachable")},
		},
	}
	steps := responsesServerToolItemToGeminiInteractionSteps(failed)
	require.Len(t, steps, 2)
	assert.Equal(t, GeminiInteractionsStepMCPResult, steps[1].Type)
	require.NotNil(t, steps[1].IsError)
	assert.True(t, *steps[1].IsError)
	assert.JSONEq(t, `"crm unreachable"`, string(steps[1].Result))

	pending := failed
	pending.Status = schemas.Ptr("in_progress")
	pending.ResponsesToolMessage = &schemas.ResponsesToolMessage{Name: schemas.Ptr("lookup"), ServerLabel: schemas.Ptr("crm"), Arguments: schemas.Ptr(`{}`)}
	assert.Len(t, responsesServerToolItemToGeminiInteractionSteps(pending), 1)
}

// Spoken or drawn output that a translated stream only reveals on output_item.done streams as a
// step delta before the step closes, so a streaming caller gets it without the final snapshot.
func TestBifrostToGeminiInteractionsStreamMediaOnDone(t *testing.T) {
	s := NewBifrostToGeminiInteractionsStreamState()
	idx := 0
	msgType, role := schemas.ResponsesMessageTypeMessage, schemas.ResponsesInputMessageRoleAssistant
	item := schemas.ResponsesMessage{Type: &msgType, Role: &role, Content: &schemas.ResponsesMessageContent{}}
	var events []*GeminiInteractionsEvent
	events = append(events, s.ToGeminiInteractionsStreamEvents(&schemas.BifrostResponsesStreamResponse{Type: schemas.ResponsesStreamResponseTypeCreated, Response: &schemas.BifrostResponsesResponse{ID: schemas.Ptr("resp_a"), Model: "gpt-audio"}})...)
	events = append(events, s.ToGeminiInteractionsStreamEvents(&schemas.BifrostResponsesStreamResponse{Type: schemas.ResponsesStreamResponseTypeOutputItemAdded, OutputIndex: &idx, Item: &item})...)
	done := item
	done.Content = &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{
		{Type: schemas.ResponsesInputMessageContentBlockTypeAudio, Audio: &schemas.ResponsesInputMessageContentBlockAudio{Format: "wav", Data: "UklG"}},
	}}
	events = append(events, s.ToGeminiInteractionsStreamEvents(&schemas.BifrostResponsesStreamResponse{Type: schemas.ResponsesStreamResponseTypeOutputItemDone, OutputIndex: &idx, Item: &done})...)
	var types []string
	var media *GeminiInteractionsDelta
	for _, ev := range events {
		types = append(types, ev.EventType)
		if ev.EventType == GeminiInteractionsEventStepDelta && ev.Delta != nil && ev.Delta.Type == GeminiContentTypeAudio {
			media = ev.Delta
		}
	}
	assert.Equal(t, []string{GeminiInteractionsEventCreated, GeminiInteractionsEventStepStart, GeminiInteractionsEventStepDelta, GeminiInteractionsEventStepStop}, types)
	require.NotNil(t, media, "the audio streams as a step delta")
	require.NotNil(t, media.Data)
	assert.Equal(t, "UklG", *media.Data)
	require.NotNil(t, media.MimeType)
	assert.Equal(t, "audio/wav", *media.MimeType)
}

// TestGeminiInteractionsStreamDeltaMetadataUsage pins that a step.delta's metadata.total_usage is
// the run's newest cumulative snapshot: it replaces the last one, an empty one does not, and
// metadata of any other shape leaves the event readable.
func TestGeminiInteractionsStreamDeltaMetadataUsage(t *testing.T) {
	s := NewGeminiInteractionsStreamState("gemini-3.8-flash")
	for _, ev := range interactionsEvents(t,
		`{"event_type":"interaction.created","interaction":{"id":"v1_d","status":"in_progress","model":"gemini-3.8-flash"}}`,
		`{"event_type":"step.start","index":0,"step":{"type":"model_output"}}`,
		`{"event_type":"step.delta","index":0,"delta":{"type":"text","text":"a"},"metadata":{"total_usage":{"total_tokens":20,"total_input_tokens":10,"total_output_tokens":10}}}`,
		`{"event_type":"step.delta","index":0,"delta":{"type":"text","text":"b"},"metadata":{"total_usage":{"total_tokens":50,"total_input_tokens":10,"total_output_tokens":40}}}`,
		`{"event_type":"step.delta","index":0,"delta":{"type":"text","text":"c"},"metadata":{"total_usage":{}}}`,
		`{"event_type":"step.delta","index":0,"delta":{"type":"text","text":"d"},"metadata":"opaque"}`,
	) {
		_, bErr := s.ToBifrostResponsesStream(ev)
		require.Nil(t, bErr)
	}
	require.NotNil(t, s.Usage)
	assert.Equal(t, 50, s.Usage.TotalTokens)
	assert.Equal(t, 40, s.Usage.TotalOutputTokens)
	final := s.Handoff()
	assert.Equal(t, 50, final[len(final)-1].Response.Usage.TotalTokens)
}

// TestGeminiInteractionsAgentGenerationConfig pins that generation_config, which Google applies
// only with model, is never sent with an agent: Deep Research takes thinking summaries in
// agent_config, a default tool_choice is dropped, and any other setting is refused.
func TestGeminiInteractionsAgentGenerationConfig(t *testing.T) {
	build := func(model string, params *schemas.ResponsesParameters) (*GeminiInteractionsRequest, error) {
		return ToGeminiInteractionsRequest(&schemas.BifrostResponsesRequest{Model: model, Input: []schemas.ResponsesMessage{interactionsUserText("hi")}, Params: params})
	}
	const deepResearch, antigravity = "deep-research-preview-04-2026", "antigravity-preview-05-2026"

	for name, params := range map[string]*schemas.ResponsesParameters{
		"max_output_tokens": {MaxOutputTokens: schemas.Ptr(100)},
		"temperature":       {Temperature: schemas.Ptr(0.2)},
		"seed":              {ExtraParams: map[string]interface{}{"seed": 7}},
		"stop_sequences":    {ExtraParams: map[string]interface{}{"stop": []string{"x"}}},
		"reasoning effort":  {Reasoning: &schemas.ResponsesParametersReasoning{Effort: schemas.Ptr("high")}},
		"tool_choice":       {ToolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: schemas.Ptr("required")}},
	} {
		_, err := build(deepResearch, params)
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), name)
		_, err = build("gemini-3.8-flash", params)
		require.NoError(t, err, "a model takes "+name)
	}

	req, err := build(deepResearch, &schemas.ResponsesParameters{
		Reasoning:  &schemas.ResponsesParametersReasoning{Summary: schemas.Ptr("auto")},
		ToolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: schemas.Ptr("auto")},
	})
	require.NoError(t, err)
	assert.Nil(t, req.GenerationConfig)
	assert.JSONEq(t, `{"type":"deep-research","thinking_summaries":"auto"}`, string(req.AgentConfig))

	_, err = build(antigravity, &schemas.ResponsesParameters{Reasoning: &schemas.ResponsesParametersReasoning{Summary: schemas.Ptr("auto")}})
	require.Error(t, err, "only Deep Research takes thinking summaries")

	for _, agent := range []string{deepResearch, antigravity} {
		req, err = build(agent, &schemas.ResponsesParameters{})
		require.NoError(t, err)
		assert.Nil(t, req.GenerationConfig)
		assert.Nil(t, req.AgentConfig, "an agent request with no settings is unchanged")
	}
}

// TestGeminiInteractionsEnvironmentID pins that the sandbox id a follow-up turn sends as
// environment reaches a Responses client: unary, streamed to completion, and on a handoff.
func TestGeminiInteractionsEnvironmentID(t *testing.T) {
	unary := (&GeminiInteraction{ID: "v1_e", Status: "completed", Model: "gemini-3.8-flash", EnvironmentID: schemas.Ptr("env_1")}).ToBifrostResponsesResponse()
	assert.Equal(t, "env_1", unary.ProviderExtraFields["environment_id"])
	assert.Nil(t, (&GeminiInteraction{ID: "v1_e", Status: "completed", Model: "gemini-3.8-flash"}).ToBifrostResponsesResponse().ProviderExtraFields)

	last := func(lines ...string) *schemas.BifrostResponsesResponse {
		s := NewGeminiInteractionsStreamState("gemini-3.8-flash")
		var events []*schemas.BifrostResponsesStreamResponse
		for _, ev := range interactionsEvents(t, lines...) {
			out, bErr := s.ToBifrostResponsesStream(ev)
			require.Nil(t, bErr)
			events = append(events, out...)
		}
		if !s.Completed {
			events = s.Handoff()
		}
		return events[len(events)-1].Response
	}
	created := `{"event_type":"interaction.created","interaction":{"id":"v1_e","status":"in_progress","model":"gemini-3.8-flash","environment_id":"env_1"}}`
	assert.Equal(t, "env_1", last(created, `{"event_type":"interaction.completed","interaction":{"id":"v1_e","status":"completed"}}`).ProviderExtraFields["environment_id"])
	assert.Equal(t, "env_1", last(created).ProviderExtraFields["environment_id"], "a handoff names the sandbox")
	plain := `{"event_type":"interaction.created","interaction":{"id":"v1_e","status":"in_progress","model":"gemini-3.8-flash"}}`
	assert.NotContains(t, last(plain).ProviderExtraFields, "environment_id")
}

// TestGeminiInteractionsStreamDocumentAndVideo pins that streamed documents and videos reach the
// final output as the file blocks the unary conversion makes, from step.start, from a delta, and
// from a delta of a step a resumed stream never announced.
func TestGeminiInteractionsStreamDocumentAndVideo(t *testing.T) {
	for name, lines := range map[string][]string{
		"delta": {
			`{"event_type":"step.start","index":0,"step":{"type":"model_output"}}`,
			`{"event_type":"step.delta","index":0,"delta":{"type":"document","uri":"https://x/a.pdf","mime_type":"application/pdf"}}`,
			`{"event_type":"step.delta","index":0,"delta":{"type":"video","uri":"https://x/b.mp4","mime_type":"video/mp4"}}`,
		},
		"step.start": {
			`{"event_type":"step.start","index":0,"step":{"type":"model_output","content":[{"type":"document","uri":"https://x/a.pdf","mime_type":"application/pdf"},{"type":"video","uri":"https://x/b.mp4","mime_type":"video/mp4"}]}}`,
		},
		"unannounced step": {
			`{"event_type":"step.delta","index":0,"delta":{"type":"document","uri":"https://x/a.pdf","mime_type":"application/pdf"}}`,
			`{"event_type":"step.delta","index":0,"delta":{"type":"video","uri":"https://x/b.mp4","mime_type":"video/mp4"}}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := NewGeminiInteractionsStreamState("gemini-3.8-flash")
			var events []*schemas.BifrostResponsesStreamResponse
			for _, ev := range interactionsEvents(t, append(append([]string{
				`{"event_type":"interaction.created","interaction":{"id":"v1_f","status":"in_progress","model":"gemini-3.8-flash"}}`}, lines...),
				`{"event_type":"step.stop","index":0}`,
				`{"event_type":"interaction.completed","interaction":{"id":"v1_f","status":"completed"}}`)...) {
				out, bErr := s.ToBifrostResponsesStream(ev)
				require.Nil(t, bErr)
				events = append(events, out...)
			}
			final := events[len(events)-1].Response
			require.Len(t, final.Output, 1)
			var urls []string
			for _, b := range final.Output[0].Content.ContentBlocks {
				require.NotNil(t, b.ResponsesInputMessageContentBlockFile)
				urls = append(urls, *b.ResponsesInputMessageContentBlockFile.FileURL)
			}
			assert.Equal(t, []string{"https://x/a.pdf", "https://x/b.mp4"}, urls)
		})
	}
}

// TestBifrostToGeminiInteractionsStreamIndexMatchesFinalSteps pins that each live step index is
// that step's position in the final steps array, when a built-in tool finishes after a later item
// opened.
func TestBifrostToGeminiInteractionsStreamIndexMatchesFinalSteps(t *testing.T) {
	var search, message schemas.ResponsesMessage
	require.NoError(t, sonic.Unmarshal([]byte(`{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"q"}}`), &search))
	require.NoError(t, sonic.Unmarshal([]byte(`{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hi","annotations":[]}]}`), &message))
	opened := message
	opened.Content = &schemas.ResponsesMessageContent{}
	final := &schemas.BifrostResponsesResponse{ID: schemas.Ptr("resp_i"), Model: "gpt-5", Status: schemas.Ptr("completed"), Output: []schemas.ResponsesMessage{search, message}}

	s := NewBifrostToGeminiInteractionsStreamState()
	live := map[int]string{}
	var steps []GeminiInteractionsItem
	for _, ev := range []*schemas.BifrostResponsesStreamResponse{
		{Type: schemas.ResponsesStreamResponseTypeCreated, Response: &schemas.BifrostResponsesResponse{ID: schemas.Ptr("resp_i"), Model: "gpt-5"}},
		{Type: schemas.ResponsesStreamResponseTypeOutputItemAdded, OutputIndex: schemas.Ptr(0), Item: &search},
		{Type: schemas.ResponsesStreamResponseTypeOutputItemAdded, OutputIndex: schemas.Ptr(1), Item: &opened},
		{Type: schemas.ResponsesStreamResponseTypeOutputTextDelta, OutputIndex: schemas.Ptr(1), Delta: schemas.Ptr("hi")},
		{Type: schemas.ResponsesStreamResponseTypeOutputItemDone, OutputIndex: schemas.Ptr(0), Item: &search},
		{Type: schemas.ResponsesStreamResponseTypeOutputItemDone, OutputIndex: schemas.Ptr(1), Item: &message},
		{Type: schemas.ResponsesStreamResponseTypeCompleted, Response: final},
	} {
		for _, out := range s.ToGeminiInteractionsStreamEvents(ev) {
			if out.EventType == GeminiInteractionsEventStepStart {
				live[*out.Index] = out.Step.Type
			}
			if out.EventType == GeminiInteractionsEventCompleted {
				steps = out.Interaction.Steps
			}
		}
	}
	require.Len(t, live, 3)
	require.Len(t, steps, 3)
	for i, step := range steps {
		assert.Equal(t, live[i], step.Type, "step %d", i)
	}
	assert.Equal(t, "ws_1", *final.Output[0].ID, "the provider's response is not reordered")
}

// TestBuildGeminiInteractionsBodyUnaryNeverStreams pins that a unary create never asks Google for
// SSE, whether stream came in as an extra param or in a raw body.
func TestBuildGeminiInteractionsBodyUnaryNeverStreams(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
	typed := &schemas.BifrostResponsesRequest{
		Model: "gemini-3.8-flash", Input: []schemas.ResponsesMessage{interactionsUserText("hi")},
		Params: &schemas.ResponsesParameters{PreviousResponseID: schemas.Ptr("v1_p"), ExtraParams: map[string]interface{}{"stream": true, "custom": "kept"}},
	}
	body, bErr := buildGeminiInteractionsBody(ctx, typed, false)
	require.Nil(t, bErr)
	assert.False(t, gjson.GetBytes(body, "stream").Bool(), string(body))
	assert.Equal(t, "kept", gjson.GetBytes(body, "custom").String(), "other extra params still pass through")

	rawCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	rawCtx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
	body, bErr = buildGeminiInteractionsBody(rawCtx, &schemas.BifrostResponsesRequest{Model: "gemini-3.8-flash", RawRequestBody: []byte(`{"model":"gemini/gemini-3.8-flash","input":"hi","stream":true}`)}, false)
	require.Nil(t, bErr)
	assert.False(t, gjson.GetBytes(body, "stream").Bool(), string(body))
}

// TestGeminiResponsesInputItemsUnconvertibleResultKeepsError pins that a failed function_result
// listed as raw JSON text is still marked failed.
func TestGeminiResponsesInputItemsUnconvertibleResultKeepsError(t *testing.T) {
	server, _ := newInteractionsTestServer(t, "application/json", func(interactionsCall) string {
		return `{"id":"v1_f","status":"completed","model":"gemini-3.8-flash","steps":[
			{"type":"function_result","call_id":"call_f","name":"lookup","is_error":true,"result":[{"type":"function","name":"nested"}]},
			{"type":"function_result","call_id":"call_g","name":"lookup","result":[{"type":"function","name":"nested"}]}
		]}`
	})
	provider := NewGeminiProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL + "/v1beta"}}, nil)
	key := schemas.Key{Value: schemas.SecretVar{Val: "k"}, Models: []string{"*"}}
	page, bErr := provider.ResponsesInputItems(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), key, &schemas.BifrostResponsesInputItemsRequest{ResponseID: "v1_f", Order: "asc"})
	require.Nil(t, bErr)
	require.Len(t, page.Data, 2)
	assert.True(t, page.Data[0].ResponsesToolMessage.Error.IsError(), "the failure is kept")
	assert.JSONEq(t, `[{"type":"function","name":"nested"}]`, *page.Data[0].Output.ResponsesToolCallOutputStr)
	assert.False(t, page.Data[1].ResponsesToolMessage.Error.IsError())
}

// TestGeminiResponsesBackgroundStreamCheckpointKeepsServiceTier pins that a streamed run recorded
// at a checkpoint carries the tier Google served it at, as the unary create's record does.
func TestGeminiResponsesBackgroundStreamCheckpointKeepsServiceTier(t *testing.T) {
	record := func(tier string) *schemas.BifrostPassthroughJob {
		chunks := streamHeldOpen(t, strings.Join([]string{
			"event: interaction.created", `data: {"event_type":"interaction.created","interaction":{"id":"v1_t","status":"in_progress","model":"gemini-3.8-flash"}}`, "",
			"event: interaction.completed", `data: {"event_type":"interaction.completed","interaction":{"id":"v1_t","status":"requires_continuation","model":"gemini-3.8-flash"` + tier + `,"usage":{"total_tokens":30,"total_input_tokens":10,"total_output_tokens":20}}}`, "", "",
		}, "\n"))
		job := chunks[len(chunks)-1].BifrostResponsesStreamResponse.Response.ProviderJob
		require.NotNil(t, job)
		require.NotNil(t, job.Usage)
		return job
	}
	withTier := record(`,"service_tier":"flex"`)
	require.NotNil(t, withTier.Usage.LLMUsage)
	without := record("")
	assert.NotEqual(t, without.Usage, withTier.Usage, "the tier is part of the record")
	raw, err := sonic.Marshal(withTier.Usage)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "flex")
}

// TestGeminiInteractionsFailedMCPCallStructuredError pins that both forms of an mcp_call error
// reach the result step.
func TestGeminiInteractionsFailedMCPCallStructuredError(t *testing.T) {
	for want, errJSON := range map[string]string{
		"crm unreachable": `{"type":"http_error","code":502,"message":"crm unreachable"}`,
		"plain":           `"plain"`,
	} {
		var item schemas.ResponsesMessage
		require.NoError(t, sonic.Unmarshal([]byte(`{"type":"mcp_call","id":"mcp_1","status":"failed","name":"lookup","server_label":"crm","arguments":"{}","error":`+errJSON+`}`), &item))
		steps := responsesServerToolItemToGeminiInteractionSteps(item)
		require.Len(t, steps, 2)
		assert.JSONEq(t, `"`+want+`"`, string(steps[1].Result))
	}
}

// TestToGeminiInteractionsRestrictionsRefused pins that a restriction Google cannot apply is
// refused rather than dropped, so a caller is never given more than it asked for.
func TestToGeminiInteractionsRestrictionsRefused(t *testing.T) {
	build := func(tools string, params *schemas.ResponsesParameters) error {
		if params == nil {
			params = &schemas.ResponsesParameters{}
		}
		if tools != "" {
			require.NoError(t, sonic.Unmarshal([]byte(tools), &params.Tools))
		}
		_, err := ToGeminiInteractionsRequest(&schemas.BifrostResponsesRequest{Model: "gemini-3.8-flash", Input: []schemas.ResponsesMessage{interactionsUserText("hi")}, Params: params})
		return err
	}
	mcp := `{"type":"mcp","server_label":"crm","server_url":"https://crm.example","require_approval":"never"`
	assert.ErrorContains(t, build(`[`+mcp+`,"allowed_tools":[]}]`, nil), "allowed_tools", "an empty allowlist denies every tool")
	assert.NoError(t, build(`[`+mcp+`}]`, nil))
	assert.NoError(t, build(`[`+mcp+`,"allowed_tools":["lookup"]}]`, nil))

	assert.ErrorContains(t, build(`[{"type":"web_fetch","filters":{"allowed_domains":["a.example"]}}]`, nil), "web_fetch")
	assert.ErrorContains(t, build(`[{"type":"web_fetch","filters":{"blocked_domains":["a.example"]}}]`, nil), "web_fetch")
	assert.NoError(t, build(`[{"type":"web_fetch"}]`, nil))

	assert.ErrorContains(t, build("", &schemas.ResponsesParameters{TopP: schemas.Ptr(0.5)}), "top_p")
	assert.NoError(t, build("", &schemas.ResponsesParameters{TopP: schemas.Ptr(1.0)}), "1 is the default and changes nothing")
}

// TestGeminiInteractionsAgentProviderPrefix pins that a google/ prefix does not hide an agent: the
// prefixed and bare names pick the same endpoint and reach Google as the same agent.
func TestGeminiInteractionsAgentProviderPrefix(t *testing.T) {
	for _, model := range []string{"google/deep-research-preview-04-2026", "Google/deep-research-preview-04-2026", "deep-research-preview-04-2026"} {
		req := &schemas.BifrostResponsesRequest{Model: model, Input: []schemas.ResponsesMessage{interactionsUserText("hi")}}
		assert.True(t, ShouldUseGeminiInteractions(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), req), model)
		native, err := ToGeminiInteractionsRequest(req)
		require.NoError(t, err)
		assert.Equal(t, "deep-research-preview-04-2026", native.Agent, model)
		assert.Empty(t, native.Model, model)
		raw, rawErr := normalizeRawGeminiInteractionsBody([]byte(`{"agent":"`+model+`","input":"go"}`), model, true)
		require.Nil(t, rawErr)
		assert.Equal(t, "deep-research-preview-04-2026", gjson.GetBytes(raw, "agent").String(), model)
	}
	assert.False(t, IsGeminiInteractionsAgent("google/gemini-3.8-flash"))
}

func interactionsFunctionOutput(callID, output string) schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		Type:                 schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{CallID: &callID, Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: &output}},
	}
}

func interactionsTestProvider(server *httptest.Server) (*GeminiProvider, schemas.Key, *schemas.BifrostContext) {
	provider := NewGeminiProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL + "/v1beta"}}, nil)
	return provider, schemas.Key{Value: schemas.SecretVar{Val: "k"}, Models: []string{"*"}}, schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
}

// TestGeminiInteractionsFunctionResultNameFromHistory pins that a function_call_output, which has no
// name in the Responses API, goes out under the name of the call it answers. Google refuses a
// function_result without one.
func TestGeminiInteractionsFunctionResultNameFromHistory(t *testing.T) {
	call := schemas.ResponsesMessage{
		Type:                 schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{CallID: schemas.Ptr("call_1"), Name: schemas.Ptr("get_weather"), Arguments: schemas.Ptr(`{"c":"Rome"}`)},
	}
	native, err := ToGeminiInteractionsRequest(&schemas.BifrostResponsesRequest{
		Model: "gemini-3.8-flash",
		Input: []schemas.ResponsesMessage{interactionsUserText("weather?"), call, interactionsFunctionOutput("call_1", "31C")},
	})
	require.NoError(t, err)
	body := marshalInteractionsBody(t, native)
	assert.Equal(t, "get_weather", gjson.Get(body, "input.2.name").String(), body)

	// A name the caller sends wins over the one in the history.
	named := interactionsFunctionOutput("call_1", "31C")
	named.Name = schemas.Ptr("caller_name")
	native, err = ToGeminiInteractionsRequest(&schemas.BifrostResponsesRequest{Model: "gemini-3.8-flash", Input: []schemas.ResponsesMessage{call, named}})
	require.NoError(t, err)
	assert.Equal(t, "caller_name", gjson.Get(marshalInteractionsBody(t, native), "input.1.name").String())
}

// TestGeminiInteractionsFunctionResultNameFromPreviousTurn pins that an output whose call is in the
// stored previous turn is named from that turn, unary and streamed, and that no read is made when
// every output already has a name.
func TestGeminiInteractionsFunctionResultNameFromPreviousTurn(t *testing.T) {
	const stored = `{"id":"v1_prev","status":"requires_action","steps":[{"type":"user_input","content":[{"type":"text","text":"weather?"}]},{"type":"function_call","id":"call_1","name":"get_weather","arguments":{}}]}`
	request := func(name *string) *schemas.BifrostResponsesRequest {
		output := interactionsFunctionOutput("call_1", "31C")
		output.Name = name
		return &schemas.BifrostResponsesRequest{
			Provider: schemas.Gemini, Model: "gemini-3.8-flash", Input: []schemas.ResponsesMessage{output},
			Params: &schemas.ResponsesParameters{PreviousResponseID: schemas.Ptr("v1_prev")},
		}
	}
	t.Run("unary", func(t *testing.T) {
		server, calls := newInteractionsTestServer(t, "application/json", func(call interactionsCall) string {
			if call.method == http.MethodGet {
				return stored
			}
			return `{"id":"v1_next","status":"completed","model":"gemini-3.8-flash","steps":[{"type":"model_output","content":[{"type":"text","text":"31C"}]}]}`
		})
		provider, key, ctx := interactionsTestProvider(server)
		req := request(nil)
		_, bifrostErr := provider.Responses(ctx, key, req)
		require.Nil(t, bifrostErr)
		require.Len(t, *calls, 2)
		assert.Equal(t, http.MethodGet, (*calls)[0].method)
		assert.Equal(t, "/v1beta/interactions/v1_prev", (*calls)[0].path)
		assert.Equal(t, "get_weather", gjson.Get((*calls)[1].body, "input.0.name").String(), (*calls)[1].body)
		assert.Nil(t, req.Input[0].Name, "the caller's request is not modified")

		*calls = nil
		_, bifrostErr = provider.Responses(ctx, key, request(schemas.Ptr("get_weather")))
		require.Nil(t, bifrostErr)
		assert.Len(t, *calls, 1, "a named output needs no read")
	})
	t.Run("stream", func(t *testing.T) {
		var mu sync.Mutex
		var created string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, stored)
				return
			}
			raw, _ := io.ReadAll(r.Body)
			mu.Lock()
			created = string(raw)
			mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"event_type\":\"interaction.created\",\"interaction\":{\"id\":\"v1_n\",\"status\":\"in_progress\"}}\n\ndata: {\"event_type\":\"interaction.completed\",\"interaction\":{\"id\":\"v1_n\",\"status\":\"completed\"}}\n\n")
		}))
		t.Cleanup(server.Close)
		provider, key, ctx := interactionsTestProvider(server)
		passthrough := func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
			return result, err
		}
		stream, bifrostErr := provider.ResponsesStream(ctx, passthrough, nil, key, request(nil))
		require.Nil(t, bifrostErr)
		completed := false
		for chunk := range stream {
			require.Nil(t, chunk.BifrostError)
			if ev := chunk.BifrostResponsesStreamResponse; ev != nil && ev.Type == schemas.ResponsesStreamResponseTypeCompleted {
				completed = true
				assert.Equal(t, "v1_n", *ev.Response.ID)
			}
		}
		assert.True(t, completed)
		mu.Lock()
		defer mu.Unlock()
		assert.Equal(t, "get_weather", gjson.Get(created, "input.0.name").String(), created)
	})
}

// TestGeminiInteractionsCustomAgent pins that an agent Google hosts under a name of the caller's own
// runs as an agent: from the typed route's body, and from /v1/responses when its agent field repeats
// the model. The name sent is always the request's model, the one governance checked.
func TestGeminiInteractionsCustomAgent(t *testing.T) {
	run := func(t *testing.T, ctx *schemas.BifrostContext, req *schemas.BifrostResponsesRequest) string {
		t.Helper()
		server, calls := newInteractionsTestServer(t, "application/json", func(interactionsCall) string {
			return `{"id":"v1_a","status":"completed","agent":"data-analyst"}`
		})
		provider, key, defaultCtx := interactionsTestProvider(server)
		if ctx == nil {
			ctx = defaultCtx
		}
		_, bifrostErr := provider.Responses(ctx, key, req)
		require.Nil(t, bifrostErr)
		require.Len(t, *calls, 1)
		assert.Equal(t, "/v1beta/interactions", (*calls)[0].path)
		return (*calls)[0].body
	}
	var native GeminiInteractionsRequest
	require.NoError(t, sonic.Unmarshal([]byte(`{"agent":"gemini/data-analyst","input":"go","environment":"remote"}`), &native))
	typed, err := native.ToBifrostResponsesRequest()
	require.NoError(t, err)

	t.Run("typed body", func(t *testing.T) {
		body := run(t, nil, typed)
		assert.Equal(t, "data-analyst", gjson.Get(body, "agent").String(), body)
		assert.False(t, gjson.Get(body, "model").Exists(), body)
	})
	t.Run("native raw body", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyInteractionsAPI, true)
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
		raw := *typed
		raw.RawRequestBody = []byte(`{"agent":"gemini/data-analyst","input":"go","environment":"remote"}`)
		assert.JSONEq(t, `{"agent":"data-analyst","input":"go","environment":"remote"}`, run(t, ctx, &raw))
	})
	t.Run("alias", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{Key: "data-analyst"})
		aliased := *typed
		aliased.Model = "real-analyst"
		assert.Equal(t, "real-analyst", gjson.Get(run(t, ctx, &aliased), "agent").String())
	})
	t.Run("responses agent field", func(t *testing.T) {
		body := run(t, nil, &schemas.BifrostResponsesRequest{
			Model: "data-analyst", Input: []schemas.ResponsesMessage{interactionsUserText("go")},
			Params: &schemas.ResponsesParameters{ExtraParams: map[string]interface{}{"agent": "data-analyst"}},
		})
		assert.JSONEq(t, `{"agent":"data-analyst","input":"go"}`, body)
	})
	t.Run("an agent field naming something else is never sent", func(t *testing.T) {
		body := run(t, nil, &schemas.BifrostResponsesRequest{
			Model: "gemini-3.8-flash", Input: []schemas.ResponsesMessage{interactionsUserText("go")},
			Params: &schemas.ResponsesParameters{Background: schemas.Ptr(true), ExtraParams: map[string]interface{}{"agent": "antigravity-preview-09-2026"}},
		})
		assert.JSONEq(t, `{"model":"gemini-3.8-flash","input":"go","background":true}`, body)
	})
}

// TestGeminiInteractionsStatelessRunHasID pins that a store:false run, which Google answers without an
// id, still gets a response id and item ids, and is never handed to job accounting or continued from.
func TestGeminiInteractionsStatelessRunHasID(t *testing.T) {
	const reply = `{"status":"completed","model":"gemini-3.8-flash","steps":[{"type":"thought","signature":"s"},{"type":"model_output","content":[{"type":"text","text":"ok"}]}]}`
	var interaction GeminiInteraction
	require.NoError(t, sonic.Unmarshal([]byte(reply), &interaction))
	resp := interaction.ToBifrostResponsesResponse()
	require.NotNil(t, resp.ID)
	assert.True(t, isGenerateContentResponseID(*resp.ID), *resp.ID)
	require.Len(t, resp.Output, 2)
	for i, item := range resp.Output {
		require.NotNil(t, item.ID)
		assert.Equal(t, geminiInteractionItemID(*resp.ID, i), *item.ID)
	}

	interaction.Status = GeminiInteractionStatusRequiresContinuation
	server, _ := newInteractionsTestServer(t, "application/json", func(interactionsCall) string {
		return `{"status":"requires_continuation","model":"gemini-3.8-flash","steps":[],"usage":{"total_tokens":3,"total_input_tokens":1,"total_output_tokens":2}}`
	})
	provider, key, ctx := interactionsTestProvider(server)
	open, bifrostErr := provider.Responses(ctx, key, &schemas.BifrostResponsesRequest{
		Model: "gemini-3.8-flash", Input: []schemas.ResponsesMessage{interactionsUserText("go")},
		Params: &schemas.ResponsesParameters{PreviousResponseID: schemas.Ptr("v1_p"), Store: schemas.Ptr(false)},
	})
	require.Nil(t, bifrostErr)
	assert.Nil(t, open.ProviderJob, "a run Google did not store cannot be polled")
	assert.NotNil(t, open.Usage, "so its usage is billed on this row")

	state := NewGeminiInteractionsStreamState("gemini-3.8-flash")
	ids := map[string]bool{}
	for _, event := range interactionsEvents(t,
		`{"event_type":"interaction.created","interaction":{"status":"in_progress"}}`,
		`{"event_type":"step.start","index":0,"step":{"type":"model_output"}}`,
		`{"event_type":"step.delta","index":0,"delta":{"type":"text","text":"ok"}}`,
		`{"event_type":"step.stop","index":0}`,
		`{"event_type":"interaction.completed","interaction":{"status":"completed"}}`,
	) {
		out, streamErr := state.ToBifrostResponsesStream(event)
		require.Nil(t, streamErr)
		for _, ev := range out {
			if ev.Response != nil {
				ids[*ev.Response.ID] = true
			}
			if ev.Item != nil {
				assert.NotContains(t, *ev.Item.ID, "item__")
			}
		}
	}
	require.Len(t, ids, 1, "every envelope names the same id")
	for id := range ids {
		assert.True(t, isGenerateContentResponseID(id), id)
	}
	assert.Empty(t, state.ID, "a minted id is not a run that can be polled")
}

// TestGeminiInteractionsMCPCallName pins that an mcp_call carries the tool's own name: Google prefixes
// it with the server name, which server_label already holds.
func TestGeminiInteractionsMCPCallName(t *testing.T) {
	for native, want := range map[string]string{"gdocs:search": "search", "search": "search", "other:search": "other:search", "gdocs:": "gdocs:"} {
		items := geminiServerToolCallToResponses(GeminiInteractionsItem{Type: GeminiInteractionsStepMCPCall, ID: schemas.Ptr("c"), Name: schemas.Ptr(native), ServerName: schemas.Ptr("gdocs")})
		require.Len(t, items, 1)
		assert.Equal(t, want, *items[0].Name, native)
		assert.Equal(t, "gdocs", *items[0].ServerLabel)
	}
}

// TestGeminiInteractionsStreamStartArgumentsDelta pins that arguments that arrive whole on step.start
// stream as a delta when no fragment follows: the Anthropic stream converter reads only deltas.
func TestGeminiInteractionsStreamStartArgumentsDelta(t *testing.T) {
	deltas := func(middle ...string) (got []string) {
		s := NewGeminiInteractionsStreamState("gemini-3.8-flash")
		lines := append([]string{`{"event_type":"step.start","index":0,"step":{"type":"function_call","id":"call_1","name":"f","arguments":{"city":"Rome"}}}`}, middle...)
		for _, ev := range interactionsEvents(t, append(lines, `{"event_type":"step.stop","index":0}`)...) {
			out, bErr := s.ToBifrostResponsesStream(ev)
			require.Nil(t, bErr)
			for _, e := range out {
				if e.Type == schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta {
					got = append(got, *e.Delta)
				}
			}
		}
		return got
	}
	assert.Equal(t, []string{`{"city":"Rome"}`}, deltas())
	assert.Equal(t, []string{`{"city":"Oslo"}`}, deltas(`{"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"{\"city\":\"Oslo\"}"}}`))
}

// TestGeminiInteractionsStreamThoughtSignatureDelta pins that a thought whose summary streamed also
// streams its signature, which the log accumulator takes from deltas only, once and before the summary
// closes. A thought with no summary keeps its signature on output_item.done alone.
func TestGeminiInteractionsStreamThoughtSignatureDelta(t *testing.T) {
	signatureAt := -1
	run := func(lines ...string) (signatures []string, order []schemas.ResponsesStreamResponseType) {
		s := NewGeminiInteractionsStreamState("gemini-3.8-flash")
		for _, ev := range interactionsEvents(t, lines...) {
			out, bErr := s.ToBifrostResponsesStream(ev)
			require.Nil(t, bErr)
			for _, e := range out {
				order = append(order, e.Type)
				if e.Signature != nil {
					signatureAt = len(order) - 1
					signatures = append(signatures, *e.Signature)
					require.NotNil(t, e.ItemID)
				}
			}
		}
		return signatures, order
	}
	signatures, order := run(
		`{"event_type":"step.start","index":0,"step":{"type":"thought"}}`,
		`{"event_type":"step.delta","index":0,"delta":{"type":"thought_summary","content":{"type":"text","text":"hm"}}}`,
		`{"event_type":"step.delta","index":0,"delta":{"type":"thought_signature","signature":"sig"}}`,
		`{"event_type":"step.stop","index":0}`)
	assert.Equal(t, []string{"sig"}, signatures)
	require.GreaterOrEqual(t, signatureAt, 0)
	assert.Less(t, signatureAt, slices.Index(order, schemas.ResponsesStreamResponseTypeReasoningSummaryTextDone))

	signatures, _ = run(`{"event_type":"step.start","index":0,"step":{"type":"thought","signature":"sig"}}`, `{"event_type":"step.stop","index":0}`)
	assert.Empty(t, signatures)
}

// TestGeminiInteractionsComputerUsePromptInjectionRefused pins that a native computer_use asking for
// prompt injection detection is not translated for a provider that would not run it.
func TestGeminiInteractionsComputerUsePromptInjectionRefused(t *testing.T) {
	convert := func(tool string) error {
		var req GeminiInteractionsRequest
		require.NoError(t, sonic.Unmarshal([]byte(`{"model":"openai/gpt-5","input":"hi","tools":[`+tool+`]}`), &req))
		_, err := req.ToBifrostResponsesRequest()
		return err
	}
	err := convert(`{"type":"computer_use","enable_prompt_injection_detection":true}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "enable_prompt_injection_detection")
	assert.NoError(t, convert(`{"type":"computer_use","enable_prompt_injection_detection":false}`))
	assert.NoError(t, convert(`{"type":"computer_use"}`))
}

// TestToGeminiInteractionsHostedToolChoiceRefused pins that a tool_choice forcing a hosted tool is
// refused rather than dropped: Interactions restricts by function name only.
func TestToGeminiInteractionsHostedToolChoiceRefused(t *testing.T) {
	convert := func(choice schemas.ResponsesToolChoiceStruct) error {
		_, err := ToGeminiInteractionsRequest(&schemas.BifrostResponsesRequest{
			Model: "gemini-3.8-flash", Input: []schemas.ResponsesMessage{interactionsUserText("hi")},
			Params: &schemas.ResponsesParameters{ToolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &choice}},
		})
		return err
	}
	for _, hosted := range []schemas.ResponsesToolChoiceType{schemas.ResponsesToolChoiceTypeFileSearch, schemas.ResponsesToolChoiceTypeWebSearchPreview, schemas.ResponsesToolChoiceTypeCodeInterpreter} {
		err := convert(schemas.ResponsesToolChoiceStruct{Type: hosted})
		require.Error(t, err, hosted)
		assert.True(t, providerUtils.IsInvalidRequestError(err), hosted)
		assert.Contains(t, err.Error(), string(hosted))
	}
	assert.NoError(t, convert(schemas.ResponsesToolChoiceStruct{Type: schemas.ResponsesToolChoiceTypeNone}))
	assert.NoError(t, convert(schemas.ResponsesToolChoiceStruct{Type: schemas.ResponsesToolChoiceTypeFunction, Name: schemas.Ptr("f")}))
}

// TestGeminiInteractionsResumedCallNeedsItsOwnSnapshot pins that the cursor error of a stream resumed
// inside a function call clears only when the completion lists that call, whether or not the stored
// steps start with the user's input. Other output does not stand in for it.
func TestGeminiInteractionsResumedCallNeedsItsOwnSnapshot(t *testing.T) {
	run := func(steps string) *schemas.BifrostError {
		s := NewGeminiInteractionsStreamState("gemini-3.8-flash")
		s.ID = "v1_x"
		for _, ev := range interactionsEvents(t,
			`{"event_type":"step.delta","index":1,"delta":{"type":"arguments_delta","arguments":"\"Rome\"}"}}`,
			`{"event_type":"interaction.completed","interaction":{"id":"v1_x","status":"requires_action","steps":[`+steps+`]}}`) {
			if _, bErr := s.ToBifrostResponsesStream(ev); bErr != nil {
				return bErr
			}
		}
		return nil
	}
	const thought, call, user = `{"type":"thought","signature":"s"}`, `{"type":"function_call","id":"call_1","name":"f","arguments":{"city":"Rome"}}`, `{"type":"user_input","content":[{"type":"text","text":"hi"}]}`
	assert.Nil(t, run(thought+","+call))
	assert.Nil(t, run(user+","+thought+","+call))
	assert.NotNil(t, run(thought))
	assert.NotNil(t, run(call+","+thought), "a call at another step is not the missing one")
	assert.NotNil(t, run(user+","+call+","+thought), "the stream's indexes do not count the turn's input")
	assert.NotNil(t, run(thought+`,{"type":"function_call","arguments":{}}`), "a call without its id and name cannot be answered")
}

// TestGeminiInteractionsSearchUnitsUseRequestedModel pins that a create whose answer names no model
// counts its searches by the model requested.
func TestGeminiInteractionsSearchUnitsUseRequestedModel(t *testing.T) {
	server, _ := newInteractionsTestServer(t, "application/json", func(interactionsCall) string {
		return `{"id":"v1_s","status":"completed","steps":[],"usage":{"total_tokens":9,"total_input_tokens":4,"total_output_tokens":5,"grounding_tool_count":[{"type":"google_search","count":3}]}}`
	})
	for model, want := range map[string]int{"gemini-3.8-flash": 3, "gemini-2.5-flash": 1} {
		provider, key, ctx := interactionsTestProvider(server)
		resp, bifrostErr := provider.Responses(ctx, key, &schemas.BifrostResponsesRequest{
			Model: model, Input: []schemas.ResponsesMessage{interactionsUserText("go")}, Params: &schemas.ResponsesParameters{PreviousResponseID: schemas.Ptr("v1_p")},
		})
		require.Nil(t, bifrostErr)
		assert.Equal(t, model, resp.Model)
		require.NotNil(t, resp.Usage.OutputTokensDetails)
		require.NotNil(t, resp.Usage.OutputTokensDetails.NumSearchQueries)
		assert.Equal(t, want, *resp.Usage.OutputTokensDetails.NumSearchQueries, model)
	}
}

// TestGeminiInteractionsReplayReportsCursorErrorAtEOF pins that a replay resumed inside a call reports
// the unusable cursor even when the stream ends on a status update, with no completion frame.
func TestGeminiInteractionsReplayReportsCursorErrorAtEOF(t *testing.T) {
	server, _ := newInteractionsTestServer(t, "text/event-stream", func(interactionsCall) string {
		return "data: {\"event_type\":\"step.delta\",\"index\":0,\"delta\":{\"type\":\"arguments_delta\",\"arguments\":\"x\"}}\n\ndata: {\"event_type\":\"interaction.status_update\",\"interaction_id\":\"v1_x\",\"status\":\"requires_action\"}\n\n"
	})
	provider, key, ctx := interactionsTestProvider(server)
	passthrough := func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return result, err
	}
	// The stream may end either way for a background run; a replay is always allowed to.
	stream, bifrostErr := provider.ResponsesRetrieveStream(ctx, passthrough, nil, key, &schemas.BifrostResponsesRetrieveRequest{ResponseID: "v1_x", LastEventID: schemas.Ptr("e1")})
	require.Nil(t, bifrostErr)
	var last *schemas.BifrostStreamChunk
	for chunk := range stream {
		last = chunk
	}
	require.NotNil(t, last)
	require.NotNil(t, last.BifrostError, "the stream must not just close")
	assert.Contains(t, last.BifrostError.Error.Message, "last_event_id")
}

// TestToGeminiInteractionPendingServerTool pins that a hosted tool call still running renders as its call
// alone, and a finished one with no sources keeps its empty result.
func TestToGeminiInteractionPendingServerTool(t *testing.T) {
	steps := func(status string) (types []string) {
		item := schemas.ResponsesMessage{ID: schemas.Ptr("ws_1"), Type: schemas.Ptr(schemas.ResponsesMessageTypeWebSearchCall), Status: &status,
			ResponsesToolMessage: &schemas.ResponsesToolMessage{Action: &schemas.ResponsesToolMessageActionStruct{ResponsesWebSearchToolCallAction: &schemas.ResponsesWebSearchToolCallAction{Type: "search", Queries: []string{"q"}}}}}
		for _, step := range responsesServerToolItemToGeminiInteractionSteps(item) {
			types = append(types, step.Type)
		}
		return types
	}
	both := []string{GeminiInteractionsStepGoogleSearchCall, GeminiInteractionsStepGoogleSearchResult}
	assert.Equal(t, both[:1], steps("in_progress"))
	assert.Equal(t, both[:1], steps("searching"))
	assert.Equal(t, both, steps("completed"))
	assert.Equal(t, both, steps("failed"))
}

// interactionsStreamChunks runs a retrieve stream of frames and returns its chunks.
func interactionsStreamChunks(t *testing.T, setup func(*schemas.BifrostContext, *schemas.ProviderConfig), lastEventID string, frames ...string) []*schemas.BifrostStreamChunk {
	t.Helper()
	server, _ := newInteractionsTestServer(t, "text/event-stream", func(interactionsCall) string {
		return "data: " + strings.Join(frames, "\n\ndata: ") + "\n\n"
	})
	config := &schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL + "/v1beta"}}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	if setup != nil {
		setup(ctx, config)
	}
	provider := NewGeminiProvider(config, nil)
	passthrough := func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return result, err
	}
	req := &schemas.BifrostResponsesRetrieveRequest{ResponseID: "v1_x"}
	if lastEventID != "" {
		req.LastEventID = &lastEventID
	}
	stream, bifrostErr := provider.ResponsesRetrieveStream(ctx, passthrough, nil, schemas.Key{Value: schemas.SecretVar{Val: "k"}}, req)
	require.Nil(t, bifrostErr)
	var chunks []*schemas.BifrostStreamChunk
	for chunk := range stream {
		chunks = append(chunks, chunk)
	}
	return chunks
}

// TestGeminiInteractionsStreamEventID pins that a normalized stream carries Google's event ids, the cursor
// last_event_id takes: on the last chunk of each upstream event, so a resume skips nothing.
func TestGeminiInteractionsStreamEventID(t *testing.T) {
	chunks := interactionsStreamChunks(t, nil, "",
		`{"event_type":"interaction.created","event_id":"e1","interaction":{"id":"v1_x","status":"in_progress"}}`,
		`{"event_type":"step.start","event_id":"e2","index":0,"step":{"type":"model_output"}}`,
		`{"event_type":"step.delta","event_id":"e3","index":0,"delta":{"type":"text","text":"hi"}}`,
		`{"event_type":"step.stop","event_id":"e4","index":0}`,
		`{"event_type":"interaction.completed","event_id":"e5","interaction":{"id":"v1_x","status":"completed"}}`)
	var ids []string
	byType := map[schemas.ResponsesStreamResponseType]string{}
	for _, chunk := range chunks {
		ev := chunk.BifrostResponsesStreamResponse
		require.NotNil(t, ev)
		if ev.EventID != nil {
			ids = append(ids, *ev.EventID)
			byType[ev.Type] = *ev.EventID
		}
	}
	assert.Equal(t, []string{"e1", "e2", "e3", "e4", "e5"}, ids, "each once, in order")
	assert.Equal(t, "e1", byType[schemas.ResponsesStreamResponseTypeInProgress], "created sends two chunks; the id is on the last")
	assert.Equal(t, "e4", byType[schemas.ResponsesStreamResponseTypeOutputItemDone])

	withDefaults := (&schemas.BifrostResponsesStreamResponse{Type: schemas.ResponsesStreamResponseTypeInProgress, EventID: schemas.Ptr("e1")}).WithDefaults()
	require.NotNil(t, withDefaults.EventID, "/v1/responses sends it")
	assert.Equal(t, "e1", *withDefaults.EventID)
}

// TestGeminiInteractionsHandoffKeepsContinuationToken pins that a background run handed off mid-way names
// the checkpoint token Google sent, and no field when it sent none.
func TestGeminiInteractionsHandoffKeepsContinuationToken(t *testing.T) {
	for _, token := range []string{"tok", ""} {
		s := NewGeminiInteractionsStreamState("gemini-3.8-flash")
		for _, ev := range interactionsEvents(t, `{"event_type":"interaction.created","interaction":{"id":"v1_x","status":"in_progress"}}`,
			`{"event_type":"interaction.status_update","interaction_id":"v1_x","status":"in_progress","continuation_token":"`+token+`"}`) {
			_, bErr := s.ToBifrostResponsesStream(ev)
			require.Nil(t, bErr)
		}
		out := s.Handoff()
		got, ok := out[len(out)-1].Response.ProviderExtraFields["continuation_token"]
		assert.Equal(t, token != "", ok)
		if token != "" {
			assert.Equal(t, token, got)
		}
	}
}

// TestGeminiInteractionsFunctionResultNameByItemID pins that the previous-turn read matches calls as the
// converter does, by item id when a call has no call_id, so it reads nothing it does not need.
func TestGeminiInteractionsFunctionResultNameByItemID(t *testing.T) {
	server, calls := newInteractionsTestServer(t, "application/json", func(interactionsCall) string {
		return `{"id":"v1_n","status":"completed","model":"gemini-3.8-flash","steps":[]}`
	})
	provider, key, ctx := interactionsTestProvider(server)
	call := schemas.ResponsesMessage{ID: schemas.Ptr("call_1"), Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{Name: schemas.Ptr("get_weather"), Arguments: schemas.Ptr("{}")}}
	_, bifrostErr := provider.Responses(ctx, key, &schemas.BifrostResponsesRequest{
		Model: "gemini-3.8-flash", Input: []schemas.ResponsesMessage{call, interactionsFunctionOutput("call_1", "31C")},
		Params: &schemas.ResponsesParameters{PreviousResponseID: schemas.Ptr("v1_p")},
	})
	require.Nil(t, bifrostErr)
	require.Len(t, *calls, 1, "the call is in the input, so the previous turn is not read")
	assert.Equal(t, "get_weather", gjson.Get((*calls)[0].body, "input.1.name").String())
}

// TestGeminiInteractionsRawCaptureIsNotNativeReplay pins that capturing raw payloads for logging does not
// change a normalized stream: the cursor error still reaches the caller, and a frame with no Responses
// event adds no chunk. Its raw bytes ride the next chunk, so the log still has them. A native replay,
// which sends Google's frames, keeps every frame as its own chunk and reports no error.
func TestGeminiInteractionsRawCaptureIsNotNativeReplay(t *testing.T) {
	frames := []string{
		`{"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"x"}}`,
		`{"event_type":"interaction.status_update","interaction_id":"v1_x","status":"requires_action"}`,
		`{"event_type":"interaction.completed","interaction":{"id":"v1_x","status":"requires_action"}}`,
	}
	capture := func(native bool) func(*schemas.BifrostContext, *schemas.ProviderConfig) {
		return func(ctx *schemas.BifrostContext, config *schemas.ProviderConfig) {
			ctx.SetValue(schemas.BifrostContextKeyCaptureRawResponse, true)
			config.SendBackRawResponse = true
			if native {
				ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
			}
		}
	}
	chunks := interactionsStreamChunks(t, capture(false), "e1", frames...)
	require.NotEmpty(t, chunks)
	last := chunks[len(chunks)-1]
	require.NotNil(t, last.BifrostError)
	assert.Contains(t, last.BifrostError.Error.Message, "last_event_id")
	for _, chunk := range chunks {
		if ev := chunk.BifrostResponsesStreamResponse; ev != nil {
			assert.NotEqual(t, schemas.ResponsesStreamResponseTypeInteractionEvent, ev.Type)
		}
	}

	chunks = interactionsStreamChunks(t, capture(true), "e1", frames...)
	var raws []string
	for _, chunk := range chunks {
		require.Nil(t, chunk.BifrostError)
		if raw, ok := chunk.BifrostResponsesStreamResponse.ExtraFields.RawResponse.(string); ok {
			raws = append(raws, raw)
		}
	}
	assert.Equal(t, frames, raws, "every native frame, once")

	chunks = interactionsStreamChunks(t, capture(false), "", `{"event_type":"interaction.created","interaction":{"id":"v1_x","status":"in_progress"}}`,
		frames[1], `{"event_type":"interaction.completed","interaction":{"id":"v1_x","status":"completed"}}`)
	var captured strings.Builder
	for _, chunk := range chunks {
		if raw, ok := chunk.BifrostResponsesStreamResponse.ExtraFields.RawResponse.(string); ok {
			captured.WriteString(raw)
		}
	}
	assert.Contains(t, captured.String(), "status_update", "the log keeps the frame no chunk stands for")
}

// TestGeminiInteractionsStreamUsageWithoutTotal pins that a usage snapshot without total_tokens counts by
// its parts, as the unary conversion does, and that an empty one replaces nothing.
func TestGeminiInteractionsStreamUsageWithoutTotal(t *testing.T) {
	run := func(final string) int {
		s := NewGeminiInteractionsStreamState("gemini-3.8-flash")
		total := 0
		for _, ev := range interactionsEvents(t,
			`{"event_type":"interaction.status_update","interaction_id":"v1_x","status":"in_progress","usage":{"total_tokens":10,"total_input_tokens":10}}`,
			`{"event_type":"interaction.completed","interaction":{"id":"v1_x","status":"completed","usage":`+final+`}}`) {
			out, bErr := s.ToBifrostResponsesStream(ev)
			require.Nil(t, bErr)
			if len(out) > 0 && out[len(out)-1].Response != nil && out[len(out)-1].Response.Usage != nil {
				total = out[len(out)-1].Response.Usage.TotalTokens
			}
		}
		return total
	}
	assert.Equal(t, 15, run(`{"total_input_tokens":10,"total_output_tokens":5}`))
	assert.Equal(t, 10, run(`{}`))

	job := (&GeminiInteractionsTarget{}).interactionJob(&schemas.BifrostResponsesResponse{ID: schemas.Ptr("v1_x"), Model: "gemini-3.8-flash"}, GeminiInteractionStatusCompleted, &GeminiInteractionsUsage{TotalInputTokens: 10, TotalOutputTokens: 5}, nil, true)
	require.NotNil(t, job.Usage, "a run is billed by its parts too")
	assert.Equal(t, 15, job.Usage.LLMUsage.TotalTokens)
}

// TestGeminiInteractionsCodeExecutionArtifactKept pins that a code run's output with no Responses form,
// such as a document, stays under provider_extra_fields. Text and images keep their typed outputs.
func TestGeminiInteractionsCodeExecutionArtifactKept(t *testing.T) {
	convert := func(result string) *schemas.BifrostResponsesResponse {
		var interaction GeminiInteraction
		require.NoError(t, sonic.Unmarshal([]byte(`{"id":"v1_c","status":"completed","steps":[{"type":"code_execution_call","id":"c","arguments":{"code":"x"}},{"type":"code_execution_result","call_id":"c","result":`+result+`}]}`), &interaction))
		return interaction.ToBifrostResponsesResponse()
	}
	resp := convert(`[{"type":"text","text":"ok"},{"type":"document","uri":"https://x/report.pdf","mime_type":"application/pdf"}]`)
	raw, err := sonic.Marshal(resp.ProviderExtraFields["steps"])
	require.NoError(t, err)
	assert.Contains(t, string(raw), "report.pdf")
	assert.Nil(t, convert(`[{"type":"text","text":"ok"},{"type":"image","data":"QQ==","mime_type":"image/png"}]`).ProviderExtraFields)
	assert.Nil(t, convert(`"ok"`).ProviderExtraFields)
}

// TestToGeminiInteractionFileSearchNilAttributes pins that a result whose attributes point at a nil map
// still renders.
func TestToGeminiInteractionFileSearchNilAttributes(t *testing.T) {
	var attributes map[string]any
	steps := responsesServerToolItemToGeminiInteractionSteps(schemas.ResponsesMessage{ID: schemas.Ptr("fs"), Type: schemas.Ptr(schemas.ResponsesMessageTypeFileSearchCall),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{ResponsesFileSearchToolCall: &schemas.ResponsesFileSearchToolCall{Results: []schemas.ResponsesFileSearchToolCallResult{{Attributes: &attributes, Text: schemas.Ptr("found")}}}}})
	require.Len(t, steps, 2)
	assert.JSONEq(t, `[{"text":"found"}]`, string(steps[1].Result))
}

// TestGeminiInteractionsResumedPastServerToolCall pins that a stream resumed between a built-in call and its
// result reports the cursor, since the result alone makes no item, unless the completion lists the call.
func TestGeminiInteractionsResumedPastServerToolCall(t *testing.T) {
	const result = `{"event_type":"step.start","index":1,"step":{"type":"file_search_result","call_id":"fs_1","result":[{"text":"x"}]}}`
	run := func(raw bool, steps string, lines ...string) *schemas.BifrostError {
		s := NewGeminiInteractionsStreamState("gemini-3.8-flash")
		s.ID, s.RawReplay = "v1_x", raw
		lines = append(lines, result, `{"event_type":"step.stop","index":1}`, `{"event_type":"interaction.completed","interaction":{"id":"v1_x","status":"completed","steps":[`+steps+`]}}`)
		for _, ev := range interactionsEvents(t, lines...) {
			if _, bErr := s.ToBifrostResponsesStream(ev); bErr != nil {
				return bErr
			}
		}
		return nil
	}
	bErr := run(false, "")
	require.NotNil(t, bErr)
	assert.Contains(t, bErr.Error.Message, "last_event_id")
	assert.Nil(t, run(true, ""), "a raw replay keeps every frame")
	assert.Nil(t, run(false, `{"type":"file_search_call","id":"fs_1"},{"type":"file_search_result","call_id":"fs_1","result":[]}`))
	assert.Nil(t, run(false, "", `{"event_type":"step.start","index":0,"step":{"type":"file_search_call","id":"fs_1"}}`, `{"event_type":"step.stop","index":0}`))
}

// TestGeminiInteractionsContinuationTokenIsNativeOnly pins that a checkpoint token, which no other provider
// can resume from, keeps a request from being translated.
func TestGeminiInteractionsContinuationTokenIsNativeOnly(t *testing.T) {
	err := (&GeminiInteractionsRequest{Model: "openai/gpt-5", ContinuationToken: schemas.Ptr("tok")}).ValidateTranslatable()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "continuation_token")
	assert.NoError(t, (&GeminiInteractionsRequest{Model: "openai/gpt-5", ContinuationToken: schemas.Ptr("")}).ValidateTranslatable())
}

// TestToGeminiInteractionsMaxToolCallsRefused pins that a cap on built-in tool calls, which Interactions
// cannot apply, is refused beside a built-in tool. It caps nothing on functions, so it is ignored there.
func TestToGeminiInteractionsMaxToolCallsRefused(t *testing.T) {
	convert := func(tools ...schemas.ResponsesTool) error {
		_, err := ToGeminiInteractionsRequest(&schemas.BifrostResponsesRequest{
			Model: "gemini-3.8-flash", Input: []schemas.ResponsesMessage{interactionsUserText("hi")},
			Params: &schemas.ResponsesParameters{MaxToolCalls: schemas.Ptr(1), Tools: tools},
		})
		return err
	}
	err := convert(schemas.ResponsesTool{Type: schemas.ResponsesToolTypeWebSearch})
	require.Error(t, err)
	assert.True(t, providerUtils.IsInvalidRequestError(err))
	assert.Contains(t, err.Error(), "max_tool_calls")
	assert.NoError(t, convert(schemas.ResponsesTool{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("f")}))
	assert.NoError(t, convert())
	// Deferred functions bring a server search with them, unless the caller runs its own.
	deferred := schemas.ResponsesTool{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("f"), DeferLoading: schemas.Ptr(true)}
	err = convert(deferred)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "max_tool_calls")
	assert.NoError(t, convert(deferred, schemas.ResponsesTool{Type: schemas.ResponsesToolTypeToolSearch, ResponsesToolToolSearch: &schemas.ResponsesToolToolSearch{Execution: schemas.Ptr(schemas.ToolSearchExecutionClient)}}))
}

// TestGeminiInteractionsValidatesBeforeNameLookup pins that a request Google would never be sent is refused
// before the previous turn is read to name its function results.
func TestGeminiInteractionsValidatesBeforeNameLookup(t *testing.T) {
	server, calls := newInteractionsTestServer(t, "application/json", func(interactionsCall) string { return `{}` })
	provider, key, ctx := interactionsTestProvider(server)
	req := &schemas.BifrostResponsesRequest{
		Model: "gemini-3.8-flash", Input: []schemas.ResponsesMessage{interactionsFunctionOutput("call_1", "31C")},
		Params: &schemas.ResponsesParameters{PreviousResponseID: schemas.Ptr("v1_p"), TopP: schemas.Ptr(0.5)},
	}
	_, bifrostErr := provider.Responses(ctx, key, req)
	require.NotNil(t, bifrostErr)
	assert.Contains(t, bifrostErr.Error.Message, "top_p")
	_, bifrostErr = provider.ResponsesStream(ctx, nil, nil, key, req)
	require.NotNil(t, bifrostErr)
	assert.Contains(t, bifrostErr.Error.Message, "top_p")
	assert.Empty(t, *calls)
}

// TestGeminiInteractionsStreamEndsAfterOversizedLine pins that a line the reader cannot hold ends the stream
// with an error even when the upstream keeps the body open.
func TestGeminiInteractionsStreamEndsAfterOversizedLine(t *testing.T) {
	chunks := streamHeldOpen(t, "data: {\"event_type\":\"interaction.created\",\"interaction\":{\"id\":\"v1_big\",\"status\":\"in_progress\"}}\n\ndata: "+strings.Repeat("x", 64<<20))
	require.NotEmpty(t, chunks)
	require.NotNil(t, chunks[len(chunks)-1].BifrostError)
}

// TestGeminiInteractionsCompletionStillRunning pins that a completion frame whose run is still going hands
// the run off, as a stream that ends early does: it is not reported as completed, its usage moves onto the
// run, and job accounting learns of it.
func TestGeminiInteractionsCompletionStillRunning(t *testing.T) {
	for _, status := range []string{GeminiInteractionStatusInProgress, "queued"} {
		chunks := streamHeldOpen(t, strings.Join([]string{
			`data: {"event_type":"interaction.created","interaction":{"id":"v1_run","status":"in_progress","model":"gemini-3.8-flash"}}`, "",
			`data: {"event_type":"interaction.completed","interaction":{"id":"v1_run","status":"` + status + `","usage":{"total_tokens":4,"total_input_tokens":4}}}`, "", "",
		}, "\n"))
		require.NotEmpty(t, chunks)
		last := chunks[len(chunks)-1].BifrostResponsesStreamResponse
		require.NotNil(t, last)
		assert.Equal(t, schemas.ResponsesStreamResponseTypeInProgress, last.Type)
		assert.Equal(t, schemas.ResponsesResponseStatusInProgress, *last.Response.Status)
		assert.Nil(t, last.Response.CompletedAt)
		assert.Nil(t, last.Response.Usage)
		require.NotNil(t, last.Response.ProviderJob)
		assert.Equal(t, "v1_run", last.Response.ProviderJob.ID)
		assert.True(t, last.Response.ProviderJob.Created)
		assert.False(t, last.Response.ProviderJob.Terminal)
	}
}

// TestToGeminiInteractionAnthropicToolDetails pins that Anthropic's own tool details survive translation: a
// shell run is bash, a failed fetch is an error, and a file search keeps its queries.
func TestToGeminiInteractionAnthropicToolDetails(t *testing.T) {
	steps := func(itemType schemas.ResponsesMessageType, tool *schemas.ResponsesToolMessage) []GeminiInteractionsItem {
		return responsesServerToolItemToGeminiInteractionSteps(schemas.ResponsesMessage{ID: schemas.Ptr("t"), Type: &itemType, Status: schemas.Ptr("completed"), ResponsesToolMessage: tool})
	}
	for name, language := range map[string]string{"bash_code_execution": "bash", "code_execution": "python", "": "python"} {
		tool := &schemas.ResponsesToolMessage{ResponsesCodeInterpreterToolCall: &schemas.ResponsesCodeInterpreterToolCall{Code: schemas.Ptr("ls")}}
		if name != "" {
			tool.ResponsesCodeExecutionCall = &schemas.ResponsesCodeExecutionCall{ToolName: name}
		}
		got := steps(schemas.ResponsesMessageTypeCodeInterpreterCall, tool)
		assert.Equal(t, language, gjson.GetBytes(got[0].Arguments, "language").String(), name)
	}
	fetch := func(code *string) string {
		tool := &schemas.ResponsesToolMessage{Action: &schemas.ResponsesToolMessageActionStruct{ResponsesWebFetchToolCallAction: &schemas.ResponsesWebFetchToolCallAction{Type: "fetch", URL: "https://x"}}}
		tool.ResponsesWebFetchCall = &schemas.ResponsesWebFetchCall{ErrorCode: code}
		return gjson.GetBytes(steps(schemas.ResponsesMessageTypeWebFetchCall, tool)[1].Result, "0.status").String()
	}
	assert.Equal(t, "error", fetch(schemas.Ptr("url_not_accessible")))
	assert.Equal(t, "success", fetch(nil))
	search := steps(schemas.ResponsesMessageTypeFileSearchCall, &schemas.ResponsesToolMessage{ResponsesFileSearchToolCall: &schemas.ResponsesFileSearchToolCall{Queries: []string{"heron"}}})
	assert.JSONEq(t, `{"queries":["heron"]}`, string(search[0].Arguments))
}

// TestToGeminiInteractionAnthropicMCP pins how Anthropic's MCP items translate: the call and its result are
// separate items, so the result answers the original call, its text blocks are the result, and an error is
// reported beside output. An item that holds both keeps its pair.
func TestToGeminiInteractionAnthropicMCP(t *testing.T) {
	steps := func(id string, tool *schemas.ResponsesToolMessage) []GeminiInteractionsItem {
		return responsesServerToolItemToGeminiInteractionSteps(schemas.ResponsesMessage{ID: &id, Type: schemas.Ptr(schemas.ResponsesMessageTypeMCPCall), Status: schemas.Ptr("completed"), ResponsesToolMessage: tool})
	}
	text := func(s string) *schemas.ResponsesToolMessageOutputStruct {
		return &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: &s}
	}
	call := steps("mcptoolu_1", &schemas.ResponsesToolMessage{Name: schemas.Ptr("search"), ServerLabel: schemas.Ptr("docs"), Arguments: schemas.Ptr("{}")})
	require.Len(t, call, 1)
	result := steps("msg_1", &schemas.ResponsesToolMessage{CallID: schemas.Ptr("mcptoolu_1"), Output: &schemas.ResponsesToolMessageOutputStruct{
		ResponsesFunctionToolCallOutputBlocks: []schemas.ResponsesMessageContentBlock{{Type: schemas.ResponsesOutputMessageContentTypeText, Text: schemas.Ptr("found")}}}})
	require.Len(t, result, 1)
	assert.Equal(t, GeminiInteractionsStepMCPResult, result[0].Type)
	assert.Equal(t, *call[0].ID, *result[0].CallID)
	assert.JSONEq(t, `[{"type":"text","text":"found"}]`, string(result[0].Result))

	both := steps("mcp_1", &schemas.ResponsesToolMessage{Name: schemas.Ptr("search"), CallID: schemas.Ptr("mcp_1"), Output: text("part"), Error: &schemas.ResponsesToolMessageError{ResponsesToolMessageErrorStr: schemas.Ptr("boom")}})
	require.Len(t, both, 2)
	assert.JSONEq(t, `"part"`, string(both[1].Result))
	require.NotNil(t, both[1].IsError)
	assert.True(t, *both[1].IsError)
	ok := steps("mcp_2", &schemas.ResponsesToolMessage{Name: schemas.Ptr("search"), Output: text("fine")})
	require.Len(t, ok, 2)
	assert.Nil(t, ok[1].IsError)
}

// TestBifrostToGeminiInteractionsStreamCompactedOutput pins that a final response that omits a finished tool,
// as Anthropic's does a web fetch, keeps the tool's steps at the indexes they streamed under.
func TestBifrostToGeminiInteractionsStreamCompactedOutput(t *testing.T) {
	var fetch, message schemas.ResponsesMessage
	require.NoError(t, sonic.Unmarshal([]byte(`{"type":"web_fetch_call","id":"wf_1","status":"completed","action":{"type":"fetch","url":"https://x"}}`), &fetch))
	require.NoError(t, sonic.Unmarshal([]byte(`{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hi","annotations":[]}]}`), &message))
	opened := message
	opened.Content = &schemas.ResponsesMessageContent{}
	run := func(finalItem schemas.ResponsesMessage) (live map[int]string, steps []GeminiInteractionsItem) {
		s := NewBifrostToGeminiInteractionsStreamState()
		live = map[int]string{}
		final := &schemas.BifrostResponsesResponse{ID: schemas.Ptr("resp_i"), Model: "claude", Status: schemas.Ptr("completed"), Output: []schemas.ResponsesMessage{finalItem}}
		for _, ev := range []*schemas.BifrostResponsesStreamResponse{
			{Type: schemas.ResponsesStreamResponseTypeOutputItemAdded, OutputIndex: schemas.Ptr(0), Item: &fetch},
			{Type: schemas.ResponsesStreamResponseTypeOutputItemDone, OutputIndex: schemas.Ptr(0), Item: &fetch},
			{Type: schemas.ResponsesStreamResponseTypeOutputItemAdded, OutputIndex: schemas.Ptr(1), Item: &opened},
			{Type: schemas.ResponsesStreamResponseTypeOutputItemDone, OutputIndex: schemas.Ptr(1), Item: &message},
			{Type: schemas.ResponsesStreamResponseTypeCompleted, Response: final},
		} {
			for _, out := range s.ToGeminiInteractionsStreamEvents(ev) {
				if out.EventType == GeminiInteractionsEventStepStart {
					live[*out.Index] = out.Step.Type
				}
				if out.EventType == GeminiInteractionsEventCompleted {
					steps = out.Interaction.Steps
				}
			}
		}
		assert.Len(t, final.Output, 1, "the provider's response is not changed")
		return live, steps
	}
	live, steps := run(message)
	require.Len(t, live, 3)
	require.Len(t, steps, 3)
	for i, step := range steps {
		assert.Equal(t, live[i], step.Type, "step %d", i)
	}
	// With another id on the final item nothing tells which item it is, so the tool is still kept.
	renamed := message
	renamed.ID = schemas.Ptr("msg_other")
	_, steps = run(renamed)
	require.Len(t, steps, 3)
	assert.Equal(t, GeminiInteractionsStepModelOutput, steps[2].Type)
}

// TestBifrostToGeminiInteractionsStreamEmptyOutput pins that a final response with no output keeps the tools
// that finished on the stream.
func TestBifrostToGeminiInteractionsStreamEmptyOutput(t *testing.T) {
	var fetch schemas.ResponsesMessage
	require.NoError(t, sonic.Unmarshal([]byte(`{"type":"web_fetch_call","id":"wf_1","status":"completed","action":{"type":"fetch","url":"https://x"}}`), &fetch))
	s := NewBifrostToGeminiInteractionsStreamState()
	var steps []GeminiInteractionsItem
	for _, ev := range []*schemas.BifrostResponsesStreamResponse{
		{Type: schemas.ResponsesStreamResponseTypeOutputItemAdded, OutputIndex: schemas.Ptr(0), Item: &fetch},
		{Type: schemas.ResponsesStreamResponseTypeOutputItemDone, OutputIndex: schemas.Ptr(0), Item: &fetch},
		{Type: schemas.ResponsesStreamResponseTypeCompleted, Response: &schemas.BifrostResponsesResponse{ID: schemas.Ptr("r"), Status: schemas.Ptr("completed")}},
	} {
		for _, out := range s.ToGeminiInteractionsStreamEvents(ev) {
			if out.EventType == GeminiInteractionsEventCompleted {
				steps = out.Interaction.Steps
			}
		}
	}
	require.Len(t, steps, 2)
	assert.Equal(t, GeminiInteractionsStepURLContextCall, steps[0].Type)
	assert.Equal(t, GeminiInteractionsStepURLContextResult, steps[1].Type)
}

// TestToGeminiInteractionsExtraModelReserved pins that an extra parameter cannot replace the model routing chose.
func TestToGeminiInteractionsExtraModelReserved(t *testing.T) {
	out, err := ToGeminiInteractionsRequest(&schemas.BifrostResponsesRequest{
		Model: "gemini-3.8-flash", Input: []schemas.ResponsesMessage{interactionsUserText("hi")},
		Params: &schemas.ResponsesParameters{ExtraParams: map[string]interface{}{"model": "gemini-3.8-pro", "labels": "kept"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "gemini-3.8-flash", out.Model)
	assert.NotContains(t, out.ExtraParams, "model")
	assert.Contains(t, out.ExtraParams, "labels")
}

// TestGeminiInteractionsCodeInterpreterContainer pins that a code interpreter that depends on a container is
// refused, since code_execution has none, and that code_execution translates with the container OpenAI requires.
func TestGeminiInteractionsCodeInterpreterContainer(t *testing.T) {
	convert := func(container interface{}) error {
		tool := schemas.ResponsesTool{Type: schemas.ResponsesToolTypeCodeInterpreter}
		if container != nil {
			tool.ResponsesToolCodeInterpreter = &schemas.ResponsesToolCodeInterpreter{Container: container}
		}
		_, err := ToGeminiInteractionsRequest(&schemas.BifrostResponsesRequest{Model: "gemini-3.8-flash", Input: []schemas.ResponsesMessage{interactionsUserText("hi")}, Params: &schemas.ResponsesParameters{Tools: []schemas.ResponsesTool{tool}}})
		return err
	}
	for name, c := range map[string]interface{}{"none": nil, "auto": map[string]interface{}{"type": "auto"}, "no files": map[string]interface{}{"type": "auto", "file_ids": []interface{}{}}} {
		assert.NoError(t, convert(c), name)
	}
	for name, c := range map[string]interface{}{"id": "cntr_1", "files": map[string]interface{}{"type": "auto", "file_ids": []interface{}{"file_1"}}} {
		err := convert(c)
		require.Error(t, err, name)
		_, invalid := providerUtils.AsBifrostBadRequestError(err)
		assert.True(t, invalid, name)
	}

	var native GeminiInteractionsRequest
	require.NoError(t, sonic.Unmarshal([]byte(`{"model":"openai/gpt-5","input":"hi","tools":[{"type":"code_execution"}]}`), &native))
	translated, err := native.ToBifrostResponsesRequest()
	require.NoError(t, err)
	raw, err := sonic.Marshal(translated.Params.Tools[0])
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"code_interpreter","container":{"type":"auto"}}`, string(raw))
	_, err = ToGeminiInteractionsRequest(translated)
	assert.NoError(t, err, "it goes back to Google")
}

// TestGeminiInteractionsOpenStepsCloseInOrder pins that steps a stream leaves open close in the order they
// opened, so a built-in call closes before the result that fills it.
func TestGeminiInteractionsOpenStepsCloseInOrder(t *testing.T) {
	for range 50 {
		s := NewGeminiInteractionsStreamState("gemini-3.8-flash")
		var last *schemas.BifrostResponsesResponse
		for _, ev := range interactionsEvents(t,
			`{"event_type":"step.start","index":0,"step":{"type":"code_execution_call","id":"ce","arguments":{"code":"x","language":"python"}}}`,
			`{"event_type":"step.start","index":1,"step":{"type":"code_execution_result","call_id":"ce","result":"42"}}`,
			`{"event_type":"step.start","index":2,"step":{"type":"code_execution_call","id":"cf","arguments":{"code":"y","language":"python"}}}`,
			`{"event_type":"step.start","index":3,"step":{"type":"model_output","content":[{"type":"text","text":"a"}]}}`,
			`{"event_type":"step.start","index":4,"step":{"type":"code_execution_call","id":"cg","arguments":{"code":"z","language":"python"}}}`,
			`{"event_type":"interaction.completed","interaction":{"id":"v1_x","status":"completed"}}`) {
			out, bErr := s.ToBifrostResponsesStream(ev)
			require.Nil(t, bErr)
			for _, e := range out {
				if e.Type == schemas.ResponsesStreamResponseTypeCompleted {
					last = e.Response
				}
			}
		}
		require.NotNil(t, last)
		var ids []string
		for _, item := range last.Output {
			ids = append(ids, *item.ID)
			if *item.ID == "ce" {
				require.Len(t, item.ResponsesCodeInterpreterToolCall.Outputs, 1)
				assert.Equal(t, "completed", *item.Status)
			}
		}
		require.Len(t, ids, 4)
		assert.Equal(t, []string{"ce", "cf", "cg"}, slices.DeleteFunc(ids, func(id string) bool { return !strings.HasPrefix(id, "c") || len(id) != 2 }))
	}
}

// TestGeminiInteractionsResumedResultNeedsItsSnapshot pins that a completion listing a built-in call without its
// result does not stand in for the result a resumed stream could not place.
func TestGeminiInteractionsResumedResultNeedsItsSnapshot(t *testing.T) {
	run := func(steps string) *schemas.BifrostError {
		s := NewGeminiInteractionsStreamState("gemini-3.8-flash")
		s.ID = "v1_x"
		for _, ev := range interactionsEvents(t, `{"event_type":"step.start","index":1,"step":{"type":"file_search_result","call_id":"fs_1","result":[{"text":"x"}]}}`, `{"event_type":"step.stop","index":1}`,
			`{"event_type":"interaction.completed","interaction":{"id":"v1_x","status":"completed","steps":[`+steps+`]}}`) {
			if _, bErr := s.ToBifrostResponsesStream(ev); bErr != nil {
				return bErr
			}
		}
		return nil
	}
	assert.NotNil(t, run(`{"type":"file_search_call","id":"fs_1"}`))
	assert.Nil(t, run(`{"type":"file_search_call","id":"fs_1"},{"type":"file_search_result","call_id":"fs_1","result":[{"text":"x"}]}`))
}

// TestGeminiInteractionsStreamSummaryItems pins that each thought_summary delta is a summary item of its own, as
// Google defines it and the unary conversion returns it.
func TestGeminiInteractionsStreamSummaryItems(t *testing.T) {
	s := NewGeminiInteractionsStreamState("gemini-3.8-flash")
	var added, done []int
	var texts []string
	var final *schemas.BifrostResponsesResponse
	for _, ev := range interactionsEvents(t,
		`{"event_type":"step.start","index":0,"step":{"type":"thought"}}`,
		`{"event_type":"step.delta","index":0,"delta":{"type":"thought_summary","content":{"type":"text","text":"one"}}}`,
		`{"event_type":"step.delta","index":0,"delta":{"type":"thought_summary","content":{"type":"text","text":"two"}}}`,
		`{"event_type":"step.delta","index":0,"delta":{"type":"thought_signature","signature":"sig"}}`,
		`{"event_type":"step.stop","index":0}`,
		`{"event_type":"interaction.completed","interaction":{"id":"v1_x","status":"completed"}}`) {
		out, bErr := s.ToBifrostResponsesStream(ev)
		require.Nil(t, bErr)
		for _, e := range out {
			switch e.Type {
			case schemas.ResponsesStreamResponseTypeReasoningSummaryPartAdded:
				added = append(added, *e.SummaryIndex)
			case schemas.ResponsesStreamResponseTypeReasoningSummaryTextDone:
				done = append(done, *e.SummaryIndex)
				texts = append(texts, *e.Text)
			case schemas.ResponsesStreamResponseTypeCompleted:
				final = e.Response
			}
		}
	}
	assert.Equal(t, []int{0, 1}, added)
	assert.Equal(t, []int{0, 1}, done)
	assert.Equal(t, []string{"one", "two"}, texts)
	require.NotNil(t, final)
	require.Len(t, final.Output[0].Summary, 2)
	assert.Equal(t, "two", final.Output[0].Summary[1].Text)
	assert.Equal(t, "sig", *final.Output[0].ResponsesReasoning.EncryptedContent)
}

// TestGeminiInteractionsStreamTextAroundMedia pins that text before and after media in one step stays two parts,
// each in its place and with its own citations, as the unary conversion returns them.
func TestGeminiInteractionsStreamTextAroundMedia(t *testing.T) {
	s := NewGeminiInteractionsStreamState("gemini-3.8-flash")
	text := func(v string) string {
		return `{"event_type":"step.delta","index":0,"delta":{"type":"text","text":"` + v + `"}}`
	}
	const cite = `{"event_type":"step.delta","index":0,"delta":{"type":"text_annotation_delta","annotations":[{"type":"url_citation","url":"https://%s","start_index":0,"end_index":1}]}}`
	var added, done []int
	var texts []string
	deltas := map[int]string{}
	annotated := map[int]int{}
	var final *schemas.BifrostResponsesResponse
	for _, ev := range interactionsEvents(t, `{"event_type":"step.start","index":0,"step":{"type":"model_output"}}`,
		text("A"), text("a"), fmt.Sprintf(cite, "one"),
		`{"event_type":"step.delta","index":0,"delta":{"type":"image","data":"QQ==","mime_type":"image/png"}}`,
		text("B"), fmt.Sprintf(cite, "two"), `{"event_type":"step.stop","index":0}`,
		`{"event_type":"interaction.completed","interaction":{"id":"v1_x","status":"completed"}}`) {
		out, bErr := s.ToBifrostResponsesStream(ev)
		require.Nil(t, bErr)
		for _, e := range out {
			switch e.Type {
			case schemas.ResponsesStreamResponseTypeContentPartAdded:
				added = append(added, *e.ContentIndex)
			case schemas.ResponsesStreamResponseTypeOutputTextDelta:
				deltas[*e.ContentIndex] += *e.Delta
			case schemas.ResponsesStreamResponseTypeOutputTextAnnotationAdded:
				assert.Zero(t, *e.AnnotationIndex)
				annotated[*e.ContentIndex]++
			case schemas.ResponsesStreamResponseTypeOutputTextDone:
				done, texts = append(done, *e.ContentIndex), append(texts, *e.Text)
			case schemas.ResponsesStreamResponseTypeCompleted:
				final = e.Response
			}
		}
	}
	assert.Equal(t, []int{0, 2}, added)
	assert.Equal(t, []int{0, 2}, done)
	assert.Equal(t, []string{"Aa", "B"}, texts)
	assert.Equal(t, map[int]string{0: "Aa", 2: "B"}, deltas)
	assert.Equal(t, map[int]int{0: 1, 2: 1}, annotated)
	require.NotNil(t, final)
	blocks := final.Output[0].Content.ContentBlocks
	require.Len(t, blocks, 3)
	assert.Equal(t, "Aa", *blocks[0].Text)
	assert.Len(t, blocks[0].Annotations, 1)
	assert.NotNil(t, blocks[1].ResponsesInputMessageContentBlockImage)
	assert.Equal(t, "B", *blocks[2].Text)
	assert.Len(t, blocks[2].Annotations, 1)
}

// TestGeminiInteractionsLargePayloadRefusedBeforeLookup pins that a continuation that cannot be sent reads
// nothing for its function results first.
func TestGeminiInteractionsLargePayloadRefusedBeforeLookup(t *testing.T) {
	server, calls := newInteractionsTestServer(t, "application/json", func(interactionsCall) string {
		return `{"id":"v1_n","status":"completed","steps":[]}`
	})
	provider, key, ctx := interactionsTestProvider(server)
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadMode, true)
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadReader, io.Reader(strings.NewReader("{}")))
	_, bErr := provider.Responses(ctx, key, &schemas.BifrostResponsesRequest{
		Model: "gemini-3.8-flash", Input: []schemas.ResponsesMessage{interactionsFunctionOutput("call_1", "31C")},
		Params: &schemas.ResponsesParameters{PreviousResponseID: schemas.Ptr("v1_p")},
	})
	require.NotNil(t, bErr)
	assert.Contains(t, bErr.Error.Message, "large payload")
	assert.Empty(t, *calls)
}

// TestGeminiInteractionsStreamEndsAtDoneMarker pins that a [DONE] before any completion ends the stream even
// though the upstream keeps the body open.
func TestGeminiInteractionsStreamEndsAtDoneMarker(t *testing.T) {
	chunks := streamHeldOpen(t, "data: {\"event_type\":\"interaction.created\",\"interaction\":{\"id\":\"v1_done\",\"status\":\"in_progress\"}}\n\ndata: [DONE]\n\n")
	require.NotEmpty(t, chunks)
	last := chunks[len(chunks)-1].BifrostResponsesStreamResponse
	require.NotNil(t, last)
	assert.Equal(t, schemas.ResponsesStreamResponseTypeInProgress, last.Type)
}

// TestGeminiInteractionsCodeLanguage pins that a shell run keeps its language through a Responses item.
func TestGeminiInteractionsCodeLanguage(t *testing.T) {
	for _, language := range []string{"bash", "python", ""} {
		items := geminiServerToolCallToResponses(GeminiInteractionsItem{Type: GeminiInteractionsStepCodeExecutionCall, ID: schemas.Ptr("ce"), Arguments: json.RawMessage(`{"code":"ls","language":"` + language + `"}`)})
		require.Len(t, items, 1)
		want := language
		if want == "" {
			want = "python"
		}
		steps := responsesServerToolItemToGeminiInteractionSteps(items[0])
		assert.Equal(t, want, gjson.GetBytes(steps[0].Arguments, "language").String())
	}
}

// TestGeminiInteractionsMCPAsFunctionSteps pins the steps Google reports an MCP server call it ran as: a
// function_call named "server:tool" and the function_result that answers it. They are one mcp_call, never a call
// for the client to run. A call no result answers stays a function call, whatever its name.
func TestGeminiInteractionsMCPAsFunctionSteps(t *testing.T) {
	const call = `{"type":"function_call","id":"call_1","name":"gdocs:search","arguments":{"query":"x"}}`
	const result = `{"type":"function_result","call_id":"call_1","name":"gdocs:search","result":[{"type":"text","text":"found"}]}`
	const answer = `{"type":"model_output","content":[{"type":"text","text":"done"}]}`
	check := func(output []schemas.ResponsesMessage) {
		t.Helper()
		require.Len(t, output, 2)
		require.Equal(t, schemas.ResponsesMessageTypeMCPCall, *output[0].Type)
		assert.Equal(t, "completed", *output[0].Status)
		assert.Equal(t, "search", *output[0].Name)
		assert.Equal(t, "gdocs", *output[0].ServerLabel)
		assert.JSONEq(t, `{"query":"x"}`, *output[0].Arguments)
		require.NotNil(t, output[0].Output)
		assert.Contains(t, *output[0].Output.ResponsesToolCallOutputStr, "found")
	}
	unary := func(steps ...string) []schemas.ResponsesMessage {
		var native GeminiInteraction
		require.NoError(t, sonic.Unmarshal([]byte(`{"id":"v1_x","status":"completed","steps":[`+strings.Join(steps, ",")+`]}`), &native))
		before := len(native.Steps)
		out := native.ToBifrostResponsesResponse().Output
		assert.Equal(t, GeminiInteractionsStepFunctionCall, native.Steps[0].Type, "the interaction is not modified")
		assert.Len(t, native.Steps, before)
		return out
	}
	check(unary(call, result, answer))
	open := unary(call)
	require.Len(t, open, 1)
	assert.Equal(t, schemas.ResponsesMessageTypeFunctionCall, *open[0].Type)
	assert.Equal(t, "gdocs:search", *open[0].Name)
	plain := unary(strings.ReplaceAll(call, "gdocs:", ""), strings.ReplaceAll(result, "gdocs:", ""))
	require.Len(t, plain, 1)
	assert.Equal(t, schemas.ResponsesMessageTypeFunctionCall, *plain[0].Type, "a call without a server in its name is the caller's")

	stream := func(lines ...string) (types []schemas.ResponsesStreamResponseType, output []schemas.ResponsesMessage) {
		s := NewGeminiInteractionsStreamState("gemini-3.8-flash")
		for _, ev := range interactionsEvents(t, lines...) {
			out, bErr := s.ToBifrostResponsesStream(ev)
			require.Nil(t, bErr)
			for _, e := range out {
				types = append(types, e.Type)
				if e.Response != nil && e.Type == schemas.ResponsesStreamResponseTypeCompleted {
					output = e.Response.Output
				}
			}
		}
		return types, output
	}
	const completed = `{"event_type":"interaction.completed","interaction":{"id":"v1_x","status":"completed"}}`
	start := func(i, step string) string {
		return `{"event_type":"step.start","index":` + i + `,"step":` + step + `}`
	}
	stop := func(i string) string { return `{"event_type":"step.stop","index":` + i + `}` }
	for name, lines := range map[string][]string{
		"whole steps": {start("0", call), stop("0"), start("1", result), stop("1"), start("2", answer), stop("2"), completed},
		"fragments": {
			start("0", `{"type":"function_call","id":"call_1","name":"gdocs:search","arguments":{}}`),
			`{"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"{\"query\":"}}`,
			`{"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"\"x\"}"}}`, stop("0"),
			start("1", `{"type":"function_result","call_id":"call_1","name":"gdocs:search"}`),
			`{"event_type":"step.delta","index":1,"delta":{"type":"function_result","result":[{"type":"text","text":"found"}]}}`, stop("1"),
			start("2", answer), stop("2"), completed},
	} {
		types, output := stream(lines...)
		assert.NotContains(t, types, schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone, name)
		check(output)
	}
	types, output := stream(start("0", call), stop("0"), completed)
	assert.Contains(t, types, schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone)
	require.Len(t, output, 1)
	assert.Equal(t, schemas.ResponsesMessageTypeFunctionCall, *output[0].Type)
	assert.JSONEq(t, `{"query":"x"}`, *output[0].Arguments)

	s := NewGeminiInteractionsStreamState("gemini-3.8-flash")
	s.ID = "v1_x"
	for _, ev := range interactionsEvents(t, start("0", call), stop("0")) {
		_, bErr := s.ToBifrostResponsesStream(ev)
		require.Nil(t, bErr)
	}
	handed := s.Handoff()
	assert.Empty(t, handed[len(handed)-1].Response.Output, "the result may still come")
}

// TestGeminiInteractionsHandoffLeavesPartialCallOpen pins that a run handed off between a function call's
// argument fragments does not report the call done, while a call that stopped stays in the output.
func TestGeminiInteractionsHandoffLeavesPartialCallOpen(t *testing.T) {
	lines := []string{
		`{"event_type":"interaction.created","interaction":{"id":"v1_x","status":"in_progress"}}`,
		`{"event_type":"step.start","index":0,"step":{"type":"function_call","id":"c0","name":"f","arguments":{}}}`,
		`{"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"{}"}}`,
		`{"event_type":"step.stop","index":0}`,
		`{"event_type":"step.start","index":1,"step":{"type":"function_call","id":"c1","name":"f","arguments":{}}}`,
		`{"event_type":"step.delta","index":1,"delta":{"type":"arguments_delta","arguments":"{\"city\":"}}`,
	}
	check := func(out []*schemas.BifrostResponsesStreamResponse) {
		t.Helper()
		for _, ev := range out {
			assert.NotEqual(t, schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone, ev.Type)
			assert.NotEqual(t, schemas.ResponsesStreamResponseTypeOutputItemDone, ev.Type)
		}
		last := out[len(out)-1]
		require.NotNil(t, last.Response)
		assert.Equal(t, "v1_x", *last.Response.ID)
		require.Len(t, last.Response.Output, 1)
		assert.Equal(t, "c0", *last.Response.Output[0].CallID)
	}
	for _, completion := range []string{"", `{"event_type":"interaction.completed","interaction":{"id":"v1_x","status":"in_progress"}}`} {
		s := NewGeminiInteractionsStreamState("gemini-3.8-flash")
		for _, ev := range interactionsEvents(t, lines...) {
			_, bErr := s.ToBifrostResponsesStream(ev)
			require.Nil(t, bErr)
		}
		if completion == "" {
			check(s.Handoff())
			continue
		}
		out, bErr := s.ToBifrostResponsesStream(interactionsEvents(t, completion)[0])
		require.Nil(t, bErr)
		check(out)
	}
}

// TestGeminiInteractionsStreamKeepsCodeArtifact pins that a streamed code run's document reaches the final
// response, as the unary conversion keeps it, when the completion lists no steps.
func TestGeminiInteractionsStreamKeepsCodeArtifact(t *testing.T) {
	run := func(result string) map[string]interface{} {
		s := NewGeminiInteractionsStreamState("gemini-3.8-flash")
		var extra map[string]interface{}
		for _, ev := range interactionsEvents(t,
			`{"event_type":"step.start","index":0,"step":{"type":"code_execution_call","id":"ce","arguments":{"code":"x","language":"python"}}}`, `{"event_type":"step.stop","index":0}`,
			`{"event_type":"step.start","index":1,"step":{"type":"code_execution_result","call_id":"ce","result":`+result+`}}`, `{"event_type":"step.stop","index":1}`,
			`{"event_type":"interaction.completed","interaction":{"id":"v1_x","status":"completed"}}`) {
			out, bErr := s.ToBifrostResponsesStream(ev)
			require.Nil(t, bErr)
			for _, e := range out {
				if e.Type == schemas.ResponsesStreamResponseTypeCompleted {
					extra = e.Response.ProviderExtraFields
				}
			}
		}
		return extra
	}
	steps, ok := run(`[{"type":"text","text":"ok"},{"type":"document","data":"JVBERg==","mime_type":"application/pdf"}]`)["steps"].([]GeminiInteractionsItem)
	require.True(t, ok)
	require.Len(t, steps, 1)
	assert.Equal(t, "application/pdf", gjson.GetBytes(steps[0].Result, "1.mime_type").String())
	assert.NotContains(t, run(`"plain logs"`), "steps")
}

// TestGeminiInteractionsRawCaptureAtStreamEnd pins that raw capture keeps the frames a stream ends on: a
// status update no chunk stands for, and an error event.
func TestGeminiInteractionsRawCaptureAtStreamEnd(t *testing.T) {
	setup := func(capture bool) func(*schemas.BifrostContext, *schemas.ProviderConfig) {
		return func(ctx *schemas.BifrostContext, config *schemas.ProviderConfig) {
			ctx.SetValue(schemas.BifrostContextKeyCaptureRawResponse, capture)
			config.SendBackRawResponse = capture
		}
	}
	const created = `{"event_type":"interaction.created","interaction":{"id":"v1_x","status":"in_progress"}}`
	const checkpoint = `{"event_type":"interaction.status_update","interaction_id":"v1_x","status":"requires_continuation","continuation_token":"tok"}`
	chunks := interactionsStreamChunks(t, setup(true), "", created, checkpoint)
	last := chunks[len(chunks)-1].BifrostResponsesStreamResponse
	require.NotNil(t, last)
	assert.Equal(t, checkpoint, last.ExtraFields.RawResponse)
	assert.Nil(t, interactionsStreamChunks(t, setup(false), "", created, checkpoint)[len(chunks)-1].BifrostResponsesStreamResponse.ExtraFields.RawResponse)

	const failure = `{"event_type":"error","event_id":"e9","error":{"code":"internal","message":"boom"}}`
	for _, capture := range []bool{true, false} {
		chunks = interactionsStreamChunks(t, setup(capture), "", created, failure)
		bErr := chunks[len(chunks)-1].BifrostError
		require.NotNil(t, bErr)
		assert.Equal(t, "boom", bErr.Error.Message)
		if !capture {
			assert.Nil(t, bErr.ExtraFields.RawResponse)
			continue
		}
		raw, err := sonic.Marshal(bErr.ExtraFields.RawResponse)
		require.NoError(t, err)
		assert.JSONEq(t, failure, string(raw))
	}
}

// TestParseInteractionsPassthroughNestedUsageWithoutTotal pins that a completion envelope's usage counts by
// its parts when it leaves out total_tokens, and that an empty one does not replace the outer usage.
func TestParseInteractionsPassthroughNestedUsageWithoutTotal(t *testing.T) {
	total := func(body string) int {
		usage := ParseInteractionsPassthrough([]byte(body)).Usage
		if usage == nil {
			return 0
		}
		return usage.LLMUsage.TotalTokens
	}
	assert.Equal(t, 15, total(`{"interaction":{"id":"r","status":"completed","usage":{"total_input_tokens":10,"total_output_tokens":5}}}`))
	assert.Equal(t, 20, total(`{"interaction":{"id":"r","status":"completed","usage":{"total_tokens":20,"total_input_tokens":10,"total_output_tokens":5}}}`))
	assert.Equal(t, 7, total(`{"usage":{"total_tokens":7,"total_input_tokens":7},"interaction":{"id":"r","status":"completed","usage":{}}}`))
}

// TestGeminiInteractionsStreamStartTextIsOneDelta pins that the text blocks of a step.start stream as one
// delta: the native frame rides that chunk, so a change to the text reaches every block of it.
func TestGeminiInteractionsStreamStartTextIsOneDelta(t *testing.T) {
	for _, step := range []string{
		`{"type":"model_output","content":[{"type":"text","text":"a"},{"type":"image","data":"QQ==","mime_type":"image/png"},{"type":"text","text":"b"}]}`,
		`{"type":"thought","summary":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}`,
	} {
		s := NewGeminiInteractionsStreamState("gemini-3.8-flash")
		var deltas []string
		for _, ev := range interactionsEvents(t, `{"event_type":"step.start","index":0,"step":`+step+`}`) {
			out, bErr := s.ToBifrostResponsesStream(ev)
			require.Nil(t, bErr)
			for _, e := range out {
				if e.Delta != nil {
					deltas = append(deltas, *e.Delta)
				}
			}
		}
		assert.Equal(t, []string{"ab"}, deltas, step)
	}
}

// TestGeminiInteractionsComputerUseExclusionsRefused pins that a native computer_use that excludes
// actions is not translated for a provider that would offer them.
func TestGeminiInteractionsComputerUseExclusionsRefused(t *testing.T) {
	translate := func(tool string) error {
		var native GeminiInteractionsRequest
		require.NoError(t, sonic.Unmarshal([]byte(`{"model":"gpt-5","input":"hi","tools":[`+tool+`]}`), &native))
		_, err := native.ToBifrostResponsesRequest()
		return err
	}
	assert.ErrorContains(t, translate(`{"type":"computer_use","environment":"browser","excluded_predefined_functions":["drag_and_drop"]}`), "excluded_predefined_functions")
	assert.NoError(t, translate(`{"type":"computer_use","environment":"browser","excluded_predefined_functions":[]}`))
	assert.NoError(t, translate(`{"type":"computer_use"}`))
}

// TestGeminiInteractionsModalityUsage pins that the modality breakdown Google reports reaches the
// Responses usage, the streamed usage and the job's usage, so audio is priced as audio.
func TestGeminiInteractionsModalityUsage(t *testing.T) {
	body := `{"total_tokens":30,"total_input_tokens":12,"total_output_tokens":18,
		"input_tokens_by_modality":[{"modality":"text","tokens":4},{"modality":"AUDIO","tokens":8}],
		"output_tokens_by_modality":[{"modality":"audio","tokens":15},{"modality":"image","tokens":3}]}`
	var usage GeminiInteractionsUsage
	require.NoError(t, sonic.Unmarshal([]byte(body), &usage))
	got := usage.toResponsesUsage("gemini-3.8-flash")
	assert.Equal(t, 12, got.InputTokens)
	assert.Equal(t, 18, got.OutputTokens)
	assert.Equal(t, 4, got.InputTokensDetails.TextTokens)
	assert.Equal(t, 8, got.InputTokensDetails.AudioTokens)
	assert.Equal(t, 15, got.OutputTokensDetails.AudioTokens)
	require.NotNil(t, got.OutputTokensDetails.ImageTokens)
	assert.Equal(t, 3, *got.OutputTokensDetails.ImageTokens)

	target := &GeminiInteractionsTarget{}
	job := target.interactionJob(&schemas.BifrostResponsesResponse{ID: schemas.Ptr("v1_m"), Model: "gemini-3.8-flash"}, "completed", &usage, nil, false)
	require.NotNil(t, job.Usage)
	require.NotNil(t, job.Usage.LLMUsage.PromptTokensDetails)
	assert.Equal(t, 8, job.Usage.LLMUsage.PromptTokensDetails.AudioTokens)
	require.NotNil(t, job.Usage.LLMUsage.CompletionTokensDetails)
	assert.Equal(t, 15, job.Usage.LLMUsage.CompletionTokensDetails.AudioTokens)

	var passthrough geminiInteractionsUsage
	require.NoError(t, sonic.Unmarshal([]byte(body), &passthrough))
	assert.Equal(t, 8, interactionsUsage(&passthrough, nil, "gemini-3.8-flash").LLMUsage.PromptTokensDetails.AudioTokens, "a passthrough read of the same run agrees")
}

// TestGeminiInteractionsStreamKeepsServedTier pins that a tier reported before the completion frame
// survives a frame that omits it, and that the default tier stays unset.
func TestGeminiInteractionsStreamKeepsServedTier(t *testing.T) {
	final := func(tier string) *schemas.BifrostResponsesResponse {
		s := NewGeminiInteractionsStreamState("gemini-3.8-flash")
		var last *schemas.BifrostResponsesResponse
		for _, ev := range interactionsEvents(t,
			`{"event_type":"interaction.created","interaction":{"id":"v1_t","status":"in_progress","service_tier":"`+tier+`"}}`,
			`{"event_type":"interaction.completed","interaction":{"id":"v1_t","status":"completed"}}`) {
			out, bErr := s.ToBifrostResponsesStream(ev)
			require.Nil(t, bErr)
			last = out[len(out)-1].Response
		}
		return last
	}
	require.NotNil(t, final("flex").ServiceTier)
	assert.EqualValues(t, "flex", *final("flex").ServiceTier)
	assert.Nil(t, final("standard").ServiceTier)
}

// TestGeminiInteractionsStreamFunctionCallStartArguments pins that arguments a step.start carries
// are kept when no fragment follows, and that fragments win when they do.
func TestGeminiInteractionsStreamFunctionCallStartArguments(t *testing.T) {
	final := func(deltas ...string) string {
		s := NewGeminiInteractionsStreamState("gemini-3.8-flash")
		lines := append([]string{
			`{"event_type":"interaction.created","interaction":{"id":"v1_c","status":"in_progress"}}`,
			`{"event_type":"step.start","index":0,"step":{"type":"function_call","id":"call_1","name":"f","arguments":{"city":"Rome"}}}`}, deltas...)
		lines = append(lines, `{"event_type":"step.stop","index":0}`, `{"event_type":"interaction.completed","interaction":{"id":"v1_c","status":"requires_action"}}`)
		var last *schemas.BifrostResponsesResponse
		for _, ev := range interactionsEvents(t, lines...) {
			out, bErr := s.ToBifrostResponsesStream(ev)
			require.Nil(t, bErr)
			if len(out) > 0 {
				last = out[len(out)-1].Response
			}
		}
		require.Len(t, last.Output, 1)
		return *last.Output[0].Arguments
	}
	assert.JSONEq(t, `{"city":"Rome"}`, final())
	assert.JSONEq(t, `{"city":"Oslo"}`, final(`{"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"{\"city\":\"Oslo\"}"}}`))
}

// TestGeminiInteractionsStreamResumedInsideFunctionCall pins that a stream resumed past a call's
// start never emits a call with no id, name or leading arguments. The normalized route reports the
// cursor as unusable; a raw replay is left alone, and a completion frame that lists the steps
// still answers in full.
func TestGeminiInteractionsStreamResumedInsideFunctionCall(t *testing.T) {
	run := func(raw bool, completed string) ([]*schemas.BifrostResponsesStreamResponse, *schemas.BifrostError) {
		s := NewGeminiInteractionsStreamState("gemini-3.8-flash")
		s.ID, s.RawReplay = "v1_x", raw
		var all []*schemas.BifrostResponsesStreamResponse
		for _, ev := range interactionsEvents(t,
			`{"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"\"Rome\"}"}}`,
			`{"event_type":"step.stop","index":0}`, completed) {
			out, bErr := s.ToBifrostResponsesStream(ev)
			all = append(all, out...)
			if bErr != nil {
				return all, bErr
			}
		}
		return all, nil
	}
	const bare = `{"event_type":"interaction.completed","interaction":{"id":"v1_x","status":"requires_action"}}`
	events, bErr := run(false, bare)
	require.NotNil(t, bErr)
	assert.Contains(t, bErr.Error.Message, "last_event_id")
	for _, ev := range events {
		assert.NotEqual(t, schemas.ResponsesStreamResponseTypeOutputItemAdded, ev.Type, "no call without its identity")
	}
	_, bErr = run(true, bare)
	assert.Nil(t, bErr, "a raw replay keeps every frame")
	events, bErr = run(false, `{"event_type":"interaction.completed","interaction":{"id":"v1_x","status":"requires_action","steps":[{"type":"function_call","id":"call_1","name":"f","arguments":{"city":"Rome"}}]}}`)
	require.Nil(t, bErr)
	assert.Equal(t, "call_1", *events[len(events)-1].Response.Output[0].CallID)
}

// TestGeminiInteractionsSearchSuggestions pins that Google's search widget markup reaches a
// Responses caller, unary and streamed.
func TestGeminiInteractionsSearchSuggestions(t *testing.T) {
	var interaction GeminiInteraction
	require.NoError(t, sonic.Unmarshal([]byte(`{"id":"v1_s","status":"completed","model":"gemini-3.8-flash","steps":[
		{"type":"google_search_call","id":"c","arguments":{"queries":["q"]}},
		{"type":"google_search_result","call_id":"c","result":[{"search_suggestions":"<div/>"},{"url":"https://a.example"}]}]}`), &interaction))
	assert.Equal(t, []string{"<div/>"}, interaction.ToBifrostResponsesResponse().ProviderExtraFields["search_suggestions"])

	s := NewGeminiInteractionsStreamState("gemini-3.8-flash")
	var last *schemas.BifrostResponsesResponse
	for _, ev := range interactionsEvents(t,
		`{"event_type":"interaction.created","interaction":{"id":"v1_s","status":"in_progress"}}`,
		`{"event_type":"step.start","index":0,"step":{"type":"google_search_call","id":"c"}}`,
		`{"event_type":"step.stop","index":0}`,
		`{"event_type":"step.start","index":1,"step":{"type":"google_search_result","call_id":"c"}}`,
		`{"event_type":"step.delta","index":1,"delta":{"type":"google_search_result","result":[{"search_suggestions":"<div/>"}]}}`,
		`{"event_type":"step.stop","index":1}`,
		`{"event_type":"interaction.completed","interaction":{"id":"v1_s","status":"completed"}}`) {
		out, bErr := s.ToBifrostResponsesStream(ev)
		require.Nil(t, bErr)
		if len(out) > 0 {
			last = out[len(out)-1].Response
		}
	}
	assert.Equal(t, []string{"<div/>"}, last.ProviderExtraFields["search_suggestions"])
}

// TestGeminiResponsesBackgroundCreateSearchUnitsUseRequestModel pins that a create response naming
// neither model nor agent prices its run with the request's model, so a Gemini 3+ run counts each
// search query rather than one unit.
func TestGeminiResponsesBackgroundCreateSearchUnitsUseRequestModel(t *testing.T) {
	server, _ := newInteractionsTestServer(t, "application/json", func(interactionsCall) string {
		return `{"id":"v1_su","object":"interaction","status":"in_progress","usage":{"total_tokens":20,"total_input_tokens":10,"total_output_tokens":10,"grounding_tool_count":[{"type":"google_search","count":3}]}}`
	})
	provider := NewGeminiProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL + "/v1beta"}}, nil)
	key := schemas.Key{Value: schemas.SecretVar{Val: "k"}, Models: []string{"*"}}
	resp, bifrostErr := provider.Responses(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), key, &schemas.BifrostResponsesRequest{
		Model:  "gemini-3-flash-preview",
		Input:  []schemas.ResponsesMessage{interactionsUserText("hi")},
		Params: &schemas.ResponsesParameters{Background: schemas.Ptr(true)},
	})
	require.Nil(t, bifrostErr)
	assert.Equal(t, "gemini-3-flash-preview", resp.Model)
	require.NotNil(t, resp.ProviderJob)
	require.NotNil(t, resp.ProviderJob.Usage)
	require.NotNil(t, resp.ProviderJob.Usage.LLMUsage.ToolUsage)
	assert.Equal(t, 3, resp.ProviderJob.Usage.LLMUsage.ToolUsage.WebSearch.NumRequests)
}

// TestInteractionsRetrieveStreamReportsTheRun pins that a replay reports the run it read, as the
// unary read does, and keeps its usage for the caller, and that a replay the caller abandons
// leaves no usage behind for the request to be charged with.
func TestInteractionsRetrieveStreamReportsTheRun(t *testing.T) {
	server, _ := newInteractionsTestServer(t, "text/event-stream", func(interactionsCall) string {
		return "event: interaction.created\ndata: {\"event_type\":\"interaction.created\",\"interaction\":{\"id\":\"v1_rp\",\"status\":\"in_progress\",\"model\":\"gemini-3.8-flash\"}}\n\n" +
			"event: interaction.completed\ndata: {\"event_type\":\"interaction.completed\",\"interaction\":{\"id\":\"v1_rp\",\"status\":\"completed\",\"model\":\"gemini-3.8-flash\",\"usage\":{\"total_tokens\":30,\"total_input_tokens\":10,\"total_output_tokens\":20}}}\n\n"
	})
	provider := NewGeminiProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL + "/v1beta"}}, nil)
	key := schemas.Key{Value: schemas.SecretVar{Val: "k"}, Models: []string{"*"}}
	postHook := func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return result, err
	}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	stream, bErr := provider.ResponsesRetrieveStream(ctx, postHook, func(context.Context) {}, key, &schemas.BifrostResponsesRetrieveRequest{ResponseID: "v1_rp"})
	require.Nil(t, bErr)
	var last *schemas.BifrostResponsesResponse
	for chunk := range stream {
		require.Nil(t, chunk.BifrostError)
		if chunk.BifrostResponsesStreamResponse != nil && chunk.BifrostResponsesStreamResponse.Response != nil {
			last = chunk.BifrostResponsesStreamResponse.Response
		}
	}
	require.NotNil(t, last.ProviderJob)
	assert.Equal(t, "v1_rp", last.ProviderJob.ID)
	assert.True(t, last.ProviderJob.Terminal)
	assert.False(t, last.ProviderJob.Created, "a replay did not start the run")
	require.NotNil(t, last.Usage, "the caller still sees the run's usage")
	assert.Equal(t, 30, last.Usage.TotalTokens)
	assert.Nil(t, ctx.Value(schemas.BifrostContextKeyStreamAccumulatedUsage))
}

// accountedCtx is a request context whose runs job accounting settles.
func accountedCtx() *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyJobAccounting, true)
	return ctx
}

// TestGeminiInteractionsUsageStaysWithoutAccounting pins that a background create keeps the usage it reported when
// nothing settles its run, on a unary create and a stream's handoff.
func TestGeminiInteractionsUsageStaysWithoutAccounting(t *testing.T) {
	const run = `{"id":"v1_bg","status":"in_progress","model":"gemini-3.8-flash","usage":{"total_tokens":8,"total_input_tokens":8}}`
	background := &schemas.BifrostResponsesRequest{Model: "gemini-3.8-flash", Input: []schemas.ResponsesMessage{interactionsUserText("hi")}, Params: &schemas.ResponsesParameters{Background: schemas.Ptr(true)}}
	postHook := func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return result, err
	}
	for _, accounted := range []bool{true, false} {
		server, _ := newInteractionsTestServer(t, "application/json", func(interactionsCall) string { return run })
		provider, key, ctx := interactionsTestProvider(server)
		ctx.SetValue(schemas.BifrostContextKeyJobAccounting, accounted)
		resp, bErr := provider.Responses(ctx, key, background)
		require.Nil(t, bErr)
		assert.Equal(t, accounted, resp.Usage == nil)

		server, _ = newInteractionsTestServer(t, "text/event-stream", func(interactionsCall) string {
			return `data: {"event_type":"interaction.created","interaction":` + run + "}\n\n"
		})
		provider, key, ctx = interactionsTestProvider(server)
		ctx.SetValue(schemas.BifrostContextKeyJobAccounting, accounted)
		stream, bErr := provider.ResponsesStream(ctx, postHook, func(context.Context) {}, key, background)
		require.Nil(t, bErr)
		var last *schemas.BifrostResponsesResponse
		for chunk := range stream {
			if ev := chunk.BifrostResponsesStreamResponse; ev != nil && ev.Response != nil {
				last = ev.Response
			}
		}
		require.NotNil(t, last)
		assert.Equal(t, accounted, last.Usage == nil)
	}
}

// TestGeminiInteractionsInterruptedRunReported pins that a background stream that ends in an error, because the
// caller left or Google sent one, reports the run it started, so the run is settled once and not billed on the
// request. A foreground stream, and one nothing settles, bill what they used.
func TestGeminiInteractionsInterruptedRunReported(t *testing.T) {
	run := func(background, accounted bool, frame string) *schemas.BifrostError {
		var mu sync.Mutex
		var last *schemas.BifrostError
		release := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"event_type\":\"interaction.created\",\"interaction\":{\"id\":\"v1_gone\",\"status\":\"in_progress\",\"usage\":{\"total_tokens\":8,\"total_input_tokens\":8}}}\n\n"+frame)
			w.(http.Flusher).Flush()
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}))
		defer server.Close()
		defer close(release)
		provider, key, ctx := interactionsTestProvider(server)
		ctx.SetValue(schemas.BifrostContextKeyInteractionsAPI, true)
		if accounted {
			ctx.SetValue(schemas.BifrostContextKeyJobAccounting, true)
		}
		postHook := func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
			if err != nil {
				mu.Lock()
				last = err
				mu.Unlock()
			}
			return result, err
		}
		stream, bErr := provider.ResponsesStream(ctx, postHook, func(context.Context) {}, key, &schemas.BifrostResponsesRequest{
			Model: "gemini-3.8-flash", Input: []schemas.ResponsesMessage{interactionsUserText("hi")}, Params: &schemas.ResponsesParameters{Background: &background}})
		require.Nil(t, bErr)
		seen := 0
		for range stream {
			if seen++; seen == 2 && frame == "" { // created and in_progress: the run is known
				ctx.Cancel()
			}
		}
		mu.Lock()
		defer mu.Unlock()
		require.NotNil(t, last)
		return last
	}
	const failure = "data: {\"event_type\":\"error\",\"error\":{\"code\":\"internal\",\"message\":\"boom\"}}\n\n"
	for _, frame := range []string{"", failure} {
		err := run(true, true, frame)
		require.NotNil(t, err.ExtraFields.ProviderJob, frame)
		assert.Equal(t, "v1_gone", err.ExtraFields.ProviderJob.ID)
		assert.True(t, err.ExtraFields.ProviderJob.Created)
		assert.False(t, err.ExtraFields.ProviderJob.Terminal)
		assert.Equal(t, "/interactions/v1_gone", err.ExtraFields.ProviderJob.PollPath)
		assert.Nil(t, err.ExtraFields.BilledUsage)
	}
	for name, err := range map[string]*schemas.BifrostError{"foreground": run(false, true, ""), "nothing settles it": run(true, false, "")} {
		assert.Nil(t, err.ExtraFields.ProviderJob, name)
		require.NotNil(t, err.ExtraFields.BilledUsage, name)
		assert.Equal(t, 8, err.ExtraFields.BilledUsage.TotalTokens, name)
	}
}

// TestInteractionJobKeepsSearchQueriesWithoutAModel pins that a read naming no model reports how
// many searches ran, so settlement can count them by the model the create named.
func TestInteractionJobKeepsSearchQueriesWithoutAModel(t *testing.T) {
	usage := &GeminiInteractionsUsage{TotalTokens: 20, TotalInputTokens: 10, TotalOutputTokens: 10, GroundingToolCount: []GeminiInteractionsGroundingToolCount{{Type: "google_search", Count: 3}}}
	target := &GeminiInteractionsTarget{}
	assert.Equal(t, 3, target.interactionJob(&schemas.BifrostResponsesResponse{ID: schemas.Ptr("v1_q")}, "completed", usage, nil, false).SearchQueries)
	assert.Zero(t, target.interactionJob(&schemas.BifrostResponsesResponse{ID: schemas.Ptr("v1_q"), Model: "gemini-3.8-flash"}, "completed", usage, nil, false).SearchQueries, "a named model already decided the count")
	info := ParseInteractionsPassthrough([]byte(`{"id":"v1_q","status":"completed","usage":{"total_tokens":20,"total_input_tokens":10,"total_output_tokens":10,"grounding_tool_count":[{"type":"google_search","count":3}]}}`))
	job := interactionsRequest{method: "GET", path: "/v1beta/interactions/v1_q"}.job(info)
	require.NotNil(t, job)
	assert.Equal(t, 3, job.SearchQueries, "a poll agrees")
}

// TestGeminiResponsesStreamViaInteractionsTagsRawFormat pins that each raw Interactions event
// attached to a chunk declares its format, so a post-hook that redacts the normalized text can
// rewrite the frame the native route replays to the client.
func TestGeminiResponsesStreamViaInteractionsTagsRawFormat(t *testing.T) {
	sse := strings.Join([]string{
		"event: interaction.created", `data: {"event_type":"interaction.created","interaction":{"id":"v1_f","status":"in_progress","model":"gemini-3.8-flash"}}`, "",
		"event: step.start", `data: {"event_type":"step.start","index":0,"step":{"type":"model_output"}}`, "",
		"event: step.delta", `data: {"event_type":"step.delta","index":0,"delta":{"type":"text","text":"Hi"}}`, "",
		"event: interaction.completed", `data: {"event_type":"interaction.completed","interaction":{"id":"v1_f","status":"completed"}}`, "",
	}, "\n")
	server, _ := newInteractionsTestServer(t, "text/event-stream", func(interactionsCall) string { return sse })
	provider := NewGeminiProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL + "/v1beta"}}, nil)
	key := schemas.Key{Value: schemas.SecretVar{Val: "k"}, Models: []string{"*"}}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyInteractionsAPI, true)
	ctx.SetValue(schemas.BifrostContextKeyCaptureRawResponse, true)
	postHook := func(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return result, err
	}
	stream, bifrostErr := provider.ResponsesStream(ctx, postHook, nil, key, &schemas.BifrostResponsesRequest{Model: "gemini-3.8-flash", Input: []schemas.ResponsesMessage{interactionsUserText("hi")}})
	require.Nil(t, bifrostErr)
	tagged := 0
	for chunk := range stream {
		resp := chunk.BifrostResponsesStreamResponse
		if resp == nil || resp.ExtraFields.RawResponse == nil {
			continue
		}
		assert.Equal(t, schemas.RawResponseFormatGeminiInteractionsStreamEvent, resp.ExtraFields.RawResponseFormat, "raw on a %s chunk", resp.Type)
		tagged++
	}
	assert.Equal(t, 4, tagged)
}

// toolSearchTestTools is a request that searches a deferred library on Google's side: a tool_search,
// a deferred function, a deferred remote MCP server and a function loaded up front.
func toolSearchTestTools() []schemas.ResponsesTool {
	never := "never"
	return []schemas.ResponsesTool{
		{Type: schemas.ResponsesToolTypeToolSearch},
		{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("get_weather"), Description: schemas.Ptr("Weather for a city."), DeferLoading: schemas.Ptr(true),
			ResponsesToolFunction: &schemas.ResponsesToolFunction{Parameters: &schemas.ToolFunctionParameters{Type: "object"}}},
		{Type: schemas.ResponsesToolTypeMCP, DeferLoading: schemas.Ptr(true), ResponsesToolMCP: &schemas.ResponsesToolMCP{
			ServerLabel: "dice", ServerURL: schemas.Ptr("https://m.example/mcp"),
			RequireApproval: &schemas.ResponsesToolMCPAllowedToolsApprovalSetting{Setting: &never},
		}},
		{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("now"), ResponsesToolFunction: &schemas.ResponsesToolFunction{}},
	}
}

// TestToGeminiInteractionsToolSearchTools pins tool retrieval on the request: a Responses
// tool_search is the Interactions tool_search, and defer_loading reaches the function and
// mcp_server tools it withholds. A tool loaded up front carries no defer_loading.
func TestToGeminiInteractionsToolSearchTools(t *testing.T) {
	out, err := ToGeminiInteractionsRequest(&schemas.BifrostResponsesRequest{
		Model:  "gemini-3.8-flash",
		Input:  []schemas.ResponsesMessage{interactionsUserText("Weather in Paris?")},
		Params: &schemas.ResponsesParameters{Tools: toolSearchTestTools()},
	})
	require.NoError(t, err)
	raw, err := providerUtils.MarshalSorted(out.Tools)
	require.NoError(t, err)
	assert.JSONEq(t, `[
		{"type":"tool_search"},
		{"type":"function","name":"get_weather","description":"Weather for a city.","defer_loading":true,"parameters":{"type":"object","properties":{}}},
		{"type":"mcp_server","name":"dice","url":"https://m.example/mcp","defer_loading":true},
		{"type":"function","name":"now"}
	]`, string(raw))
}

// TestToGeminiInteractionsDeferredToolsNeedSearch pins the deferral rules Google enforces: a
// deferred tool without a tool_search gets one on a model that serves tool retrieval (Google
// refuses defer_loading alone), and loads up front on an older model or beside a search the
// caller runs itself, since Google refuses defer_loading with a client tool_search.
func TestToGeminiInteractionsDeferredToolsNeedSearch(t *testing.T) {
	deferred := schemas.ResponsesTool{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("get_weather"), DeferLoading: schemas.Ptr(true), ResponsesToolFunction: &schemas.ResponsesToolFunction{}}
	toolsJSON := func(model string, tools ...schemas.ResponsesTool) string {
		t.Helper()
		out, err := ToGeminiInteractionsRequest(&schemas.BifrostResponsesRequest{
			Model: model, Input: []schemas.ResponsesMessage{interactionsUserText("hi")},
			Params: &schemas.ResponsesParameters{PreviousResponseID: schemas.Ptr("v1_prev"), Tools: tools},
		})
		require.NoError(t, err)
		raw, err := providerUtils.MarshalSorted(out.Tools)
		require.NoError(t, err)
		return string(raw)
	}

	assert.JSONEq(t, `[{"type":"function","name":"get_weather","defer_loading":true},{"type":"tool_search"}]`, toolsJSON("gemini-3.8-flash", deferred))
	assert.JSONEq(t, `[{"type":"function","name":"get_weather","defer_loading":true},{"type":"tool_search"}]`, toolsJSON("gemini-4-neon", deferred))
	assert.JSONEq(t, `[{"type":"function","name":"get_weather"}]`, toolsJSON("gemini-3-flash-preview", deferred),
		"a model without tool retrieval loads the tool up front, as before")

	client := schemas.ResponsesTool{Type: schemas.ResponsesToolTypeToolSearch, Description: schemas.Ptr("Find tools."),
		ResponsesToolToolSearch: &schemas.ResponsesToolToolSearch{Execution: schemas.Ptr("client"), Parameters: &schemas.ToolFunctionParameters{Type: "object"}}}
	assert.JSONEq(t, `[
		{"type":"tool_search","execution":"client","name":"tool_search","description":"Find tools.","parameters":{"type":"object","properties":{}}},
		{"type":"function","name":"get_weather"}
	]`, toolsJSON("gemini-3.8-flash", client, deferred), "a client search takes a name and keeps every tool loaded")
}

// TestSupportsGeminiToolSearch pins the models tool retrieval is added for: Gemini 3.6 and later.
func TestSupportsGeminiToolSearch(t *testing.T) {
	for model, want := range map[string]bool{
		"gemini-3.6-flash": true, "gemini-3.8-flash": true, "gemini-3.10-pro": true, "gemini-4-neon": true, "gemini-4-argon": true,
		"models/gemini-3.6-flash": true, "gemini-3.5-flash": false, "gemini-3-flash-preview": false, "gemini-3.1-pro-preview": false,
		"gemini-2.5-pro": false, "deep-research-pro-preview": false, "": false,
	} {
		assert.Equal(t, want, supportsGeminiToolSearch(model), model)
	}
}

// TestShouldUseGeminiInteractionsToolSearch pins routing: generateContent has no tool search, so a
// request carrying a tool_search Google runs leaves it. A client search and a plain deferred tool
// stay where they were (the gateway runs the search for a deferred tool on generateContent).
func TestShouldUseGeminiInteractionsToolSearch(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	request := func(tools ...schemas.ResponsesTool) *schemas.BifrostResponsesRequest {
		return &schemas.BifrostResponsesRequest{Model: "gemini-3.8-flash", Params: &schemas.ResponsesParameters{Tools: tools}}
	}
	assert.True(t, ShouldUseGeminiInteractions(ctx, request(toolSearchTestTools()...)))
	assert.False(t, ShouldUseGeminiInteractions(ctx, request(schemas.ResponsesTool{Type: schemas.ResponsesToolTypeToolSearch, ResponsesToolToolSearch: &schemas.ResponsesToolToolSearch{Execution: schemas.Ptr("client")}})))
	assert.False(t, ShouldUseGeminiInteractions(ctx, request(schemas.ResponsesTool{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("f"), DeferLoading: schemas.Ptr(true)})))
}

const toolSearchInteractionBody = `{
	"id":"v1_ts","object":"interaction","model":"gemini-3.8-flash","status":"requires_action",
	"steps":[
		{"type":"user_input","content":[{"type":"text","text":"Weather in Paris?"}]},
		{"type":"tool_search_call","id":"ts_1","arguments":{"function_names":["get_weather"]},"signature":"sig_call"},
		{"type":"tool_search_result","call_id":"ts_1","signature":"sig_result","result":[{"type":"function","name":"get_weather","description":"Weather for a city.","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}]},
		{"type":"function_call","id":"fc_1","name":"get_weather","arguments":{"city":"Paris"}}
	],
	"usage":{"total_input_tokens":40,"total_output_tokens":8,"total_tool_use_tokens":12,"total_tokens":60}
}`

// TestGeminiInteractionToolSearchStepsToResponses pins the search steps onto the items OpenAI
// uses for its own hosted search: tool_search_call names what the model looked for, and
// tool_search_output lists the declarations it loaded, both execution "server". Each keeps the
// step's signature as encrypted_content, which a stateless replay must send back.
func TestGeminiInteractionToolSearchStepsToResponses(t *testing.T) {
	var interaction GeminiInteraction
	require.NoError(t, sonic.Unmarshal([]byte(toolSearchInteractionBody), &interaction))
	resp := interaction.ToBifrostResponsesResponse()

	require.Len(t, resp.Output, 3)
	assert.Nil(t, resp.ProviderExtraFields, "the search steps map, so none stay raw")
	call, err := sonic.Marshal(resp.Output[0])
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"tool_search_call","id":"ts_1","call_id":"ts_1","execution":"server","status":"completed",
		"arguments":{"function_names":["get_weather"]},"encrypted_content":"sig_call"}`, string(call))
	output, err := sonic.Marshal(resp.Output[1])
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"tool_search_output","id":"item_v1_ts_2","call_id":"ts_1","execution":"server","status":"completed",
		"tools":[{"type":"function","name":"get_weather","description":"Weather for a city.","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}],
		"encrypted_content":"sig_result"}`, string(output))
	assert.Equal(t, schemas.ResponsesMessageTypeFunctionCall, *resp.Output[2].Type)
	assert.Equal(t, 52, resp.Usage.InputTokens, "the search's own model call is billed as input")
}

// TestToGeminiInteractionsInputReplaysToolSearch pins a stateless follow-up: the search items a
// client got back (decoded as the client sends them) replay as the same steps with the signatures
// Google issued. A search another provider ran carries no signature Google can verify, so it is
// dropped as other built-in tool history is.
func TestToGeminiInteractionsInputReplaysToolSearch(t *testing.T) {
	var interaction GeminiInteraction
	require.NoError(t, sonic.Unmarshal([]byte(toolSearchInteractionBody), &interaction))
	output, err := sonic.Marshal(interaction.ToBifrostResponsesResponse().Output)
	require.NoError(t, err)
	history := `[{"type":"message","role":"user","content":"Weather in Paris?"},` +
		`{"type":"tool_search_call","id":"tsc_openai","call_id":"call_o","execution":"server","status":"completed","arguments":{"query":"weather"}},` +
		string(output[1:len(output)-1]) +
		`,{"type":"function_call_output","call_id":"fc_1","output":"21C"}]`
	var input []schemas.ResponsesMessage
	require.NoError(t, sonic.Unmarshal([]byte(history), &input))

	out, err := ToGeminiInteractionsRequest(&schemas.BifrostResponsesRequest{
		Model: "gemini-3.8-flash", Input: input,
		Params: &schemas.ResponsesParameters{Store: schemas.Ptr(false), Tools: toolSearchTestTools()},
	})
	require.NoError(t, err)
	steps, err := providerUtils.MarshalSorted(out.Input.Items)
	require.NoError(t, err)
	assert.JSONEq(t, `[
		{"type":"user_input","content":[{"type":"text","text":"Weather in Paris?"}]},
		{"type":"tool_search_call","id":"ts_1","arguments":{"function_names":["get_weather"]},"signature":"sig_call"},
		{"type":"tool_search_result","call_id":"ts_1","signature":"sig_result","result":[{"type":"function","name":"get_weather","description":"Weather for a city.","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}]},
		{"type":"function_call","id":"fc_1","name":"get_weather","arguments":{"city":"Paris"}},
		{"type":"function_result","call_id":"fc_1","name":"get_weather","result":[{"type":"text","text":"21C"}]}
	]`, string(steps))
}

// TestGeminiResponsesClientToolSearchViaInteractions pins a search the caller runs itself, end to
// end through the provider: the client tool_search is sent with a name, the caller's earlier search
// replays as the function_call and function_result Google documents for it, and the model's next
// call to the search comes back as a client tool_search_call the caller dispatches.
func TestGeminiResponsesClientToolSearchViaInteractions(t *testing.T) {
	server, calls := newInteractionsTestServer(t, "application/json", func(interactionsCall) string {
		return `{"id":"v1_c","object":"interaction","model":"gemini-3.8-flash","status":"requires_action","steps":[
			{"type":"function_call","id":"call_2","name":"search_tool_registry","arguments":{"query":"currency"}}],
			"usage":{"total_input_tokens":5,"total_output_tokens":2,"total_tokens":7}}`
	})
	provider := NewGeminiProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL + "/v1beta"}}, nil)
	key := schemas.Key{Value: schemas.SecretVar{Val: "k"}, Models: []string{"*"}}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	var input []schemas.ResponsesMessage
	require.NoError(t, sonic.Unmarshal([]byte(`[
		{"type":"tool_search_call","call_id":"call_1","execution":"client","status":"completed","arguments":{"query":"weather"}},
		{"type":"tool_search_output","call_id":"call_1","execution":"client","status":"completed","tools":[{"type":"function","name":"get_weather","parameters":{"type":"object"}}]}
	]`), &input))
	resp, bifrostErr := provider.Responses(ctx, key, &schemas.BifrostResponsesRequest{
		Provider: schemas.Gemini, Model: "gemini-3.8-flash", Input: input,
		Params: &schemas.ResponsesParameters{PreviousResponseID: schemas.Ptr("v1_prev"), Tools: []schemas.ResponsesTool{{
			Type: schemas.ResponsesToolTypeToolSearch, Name: schemas.Ptr("search_tool_registry"), Description: schemas.Ptr("Find tools."),
			ResponsesToolToolSearch: &schemas.ResponsesToolToolSearch{Execution: schemas.Ptr("client"), Parameters: &schemas.ToolFunctionParameters{Type: "object"}},
		}}},
	})
	require.Nil(t, bifrostErr)
	require.Len(t, *calls, 1)
	assert.JSONEq(t, `{"model":"gemini-3.8-flash","previous_interaction_id":"v1_prev",
		"tools":[{"type":"tool_search","execution":"client","name":"search_tool_registry","description":"Find tools.","parameters":{"type":"object","properties":{}}}],
		"input":[
			{"type":"function_call","id":"call_1","name":"search_tool_registry","arguments":{"query":"weather"}},
			{"type":"function_result","call_id":"call_1","name":"search_tool_registry","result":[{"type":"function","name":"get_weather","parameters":{"type":"object"}}]}
		]}`, (*calls)[0].body)

	require.Len(t, resp.Output, 1)
	item, err := sonic.Marshal(resp.Output[0])
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"tool_search_call","id":"call_2","call_id":"call_2","execution":"client","status":"completed","arguments":{"query":"currency"}}`, string(item))
}

// TestGeminiInteractionsClientSearchStartArguments pins that a client search whose arguments arrive whole on
// step.start sends them on its completed item only: the item is a tool_search_call, so no function-call
// event belongs to it. A function call sends them as a delta.
func TestGeminiInteractionsClientSearchStartArguments(t *testing.T) {
	run := func(name string) (types []schemas.ResponsesStreamResponseType, arguments string) {
		s := NewGeminiInteractionsStreamState("gemini-3.8-flash")
		s.ClientToolSearchName = "find_tools"
		for _, ev := range interactionsEvents(t, `{"event_type":"step.start","index":0,"step":{"type":"function_call","id":"c","name":"`+name+`","arguments":{"query":"fx"}}}`, `{"event_type":"step.stop","index":0}`) {
			out, bErr := s.ToBifrostResponsesStream(ev)
			require.Nil(t, bErr)
			for _, e := range out {
				types = append(types, e.Type)
				if e.Type == schemas.ResponsesStreamResponseTypeOutputItemDone && e.Item.Arguments != nil {
					arguments = *e.Item.Arguments
				}
			}
		}
		return types, arguments
	}
	types, arguments := run("find_tools")
	assert.NotContains(t, types, schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta)
	assert.NotContains(t, types, schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone)
	assert.JSONEq(t, `{"query":"fx"}`, arguments)
	types, _ = run("other")
	assert.Contains(t, types, schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta)
}

// TestGeminiInteractionsDeferralWithBothSearches pins that a request declaring a server and a client
// tool_search loads its tools up front: Google refuses a deferred tool beside a client search.
func TestGeminiInteractionsDeferralWithBothSearches(t *testing.T) {
	server := GeminiInteractionsTool{Type: geminiInteractionsToolSearch}
	client := GeminiInteractionsTool{Type: geminiInteractionsToolSearch, Execution: schemas.Ptr(schemas.ToolSearchExecutionClient), Name: schemas.Ptr("find")}
	deferred := func(tools ...GeminiInteractionsTool) bool {
		fn := GeminiInteractionsTool{Type: "function", Name: schemas.Ptr("f"), DeferLoading: schemas.Ptr(true)}
		out := applyGeminiInteractionsDeferredTools(append(tools, fn), "gemini-3.8-flash", false)
		last := out[len(tools)]
		return last.DeferLoading != nil && *last.DeferLoading
	}
	assert.True(t, deferred(server))
	assert.False(t, deferred(server, client))
	assert.False(t, deferred(client, server))
}

// TestGeminiInteractionsNamedResultIsNotASearch pins that a result naming another function stays a function
// result even when its body lists declarations. An unnamed one is still read as the search's answer.
func TestGeminiInteractionsNamedResultIsNotASearch(t *testing.T) {
	const declarations = `[{"type":"function","name":"f","parameters":{"type":"object"}}]`
	isSearch := func(name *string) bool {
		calls := map[string]bool{}
		ref := &calls
		_, ok := geminiClientToolSearchStepToResponses(GeminiInteractionsItem{Type: GeminiInteractionsStepFunctionResult, CallID: schemas.Ptr("c"), Name: name, Result: json.RawMessage(declarations)}, "find_tools", ref)
		return ok
	}
	assert.False(t, isSearch(schemas.Ptr("list_tools")))
	assert.True(t, isSearch(schemas.Ptr("find_tools")))
	assert.True(t, isSearch(nil))
}

// TestGeminiToolSearchOutputNames pins that a loaded declaration carries the name the caller declared:
// Google lists a function under its internal "default_api:" namespace. An MCP server's prefix stays.
func TestGeminiToolSearchOutputNames(t *testing.T) {
	got := geminiToolSearchResultToResponsesTools(json.RawMessage(`[{"type":"function","name":"default_api:convert_currency","parameters":{"type":"object"}},{"name":"default_api:get_time"},{"type":"function","name":"gdocs:search"}]`))
	assert.JSONEq(t, `[{"type":"function","name":"convert_currency","parameters":{"type":"object"}},{"type":"function","name":"get_time"},{"type":"function","name":"gdocs:search"}]`, string(got))

	untouched := json.RawMessage(`[{"type":"function","name":"convert_currency","b":1,"a":2}]`)
	assert.Equal(t, string(untouched), string(geminiToolSearchResultToResponsesTools(untouched)), "already right, so the bytes are kept")
}

// TestGeminiInteractionsStreamToolSearch pins the search on a stream: each search step is one
// whole item, opened and closed when its step ends, with its signature; a call to the caller's own
// search streams as a client tool_search_call with no argument deltas; and the final response
// lists them in order.
func TestGeminiInteractionsStreamToolSearch(t *testing.T) {
	events := interactionsEvents(t,
		`{"event_type":"interaction.created","interaction":{"id":"v1_s","model":"gemini-3.8-flash","status":"in_progress"}}`,
		`{"event_type":"step.start","index":0,"step":{"type":"tool_search_call","id":"ts_1"}}`,
		`{"event_type":"step.delta","index":0,"delta":{"type":"tool_search_call","arguments":{"function_names":["get_weather"]},"signature":"sig_call"}}`,
		`{"event_type":"step.stop","index":0}`,
		`{"event_type":"step.start","index":1,"step":{"type":"tool_search_result","call_id":"ts_1","signature":"sig_result"}}`,
		`{"event_type":"step.delta","index":1,"delta":{"type":"tool_search_result","result":[{"type":"function","name":"get_weather"}]}}`,
		`{"event_type":"step.stop","index":1}`,
		`{"event_type":"step.start","index":2,"step":{"type":"function_call","id":"call_2","name":"search_tool_registry"}}`,
		`{"event_type":"step.delta","index":2,"delta":{"type":"arguments_delta","arguments":"{\"query\":\"fx\"}"}}`,
		`{"event_type":"step.stop","index":2}`,
		`{"event_type":"interaction.completed","interaction":{"id":"v1_s","status":"requires_action","usage":{"total_input_tokens":1,"total_output_tokens":1,"total_tokens":2}}}`,
	)
	state := NewGeminiInteractionsStreamState("gemini-3.8-flash")
	state.ClientToolSearchName = "search_tool_registry"
	var all []*schemas.BifrostResponsesStreamResponse
	for _, event := range events {
		out, err := state.ToBifrostResponsesStream(event)
		require.Nil(t, err)
		all = append(all, out...)
	}
	var done []string
	for _, ev := range all {
		assert.NotEqual(t, schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta, ev.Type, "a client search carries its arguments whole")
		if ev.Type == schemas.ResponsesStreamResponseTypeOutputItemDone {
			raw, err := sonic.Marshal(ev.Item)
			require.NoError(t, err)
			done = append(done, string(raw))
		}
	}
	require.Len(t, done, 3)
	assert.JSONEq(t, `{"type":"tool_search_call","id":"ts_1","call_id":"ts_1","execution":"server","status":"completed","arguments":{"function_names":["get_weather"]},"encrypted_content":"sig_call"}`, done[0])
	assert.JSONEq(t, `{"type":"tool_search_output","id":"item_v1_s_1","call_id":"ts_1","execution":"server","status":"completed","tools":[{"type":"function","name":"get_weather"}],"encrypted_content":"sig_result"}`, done[1])
	assert.JSONEq(t, `{"type":"tool_search_call","id":"call_2","call_id":"call_2","execution":"client","status":"completed","arguments":{"query":"fx"}}`, done[2])

	final := all[len(all)-1]
	require.Equal(t, schemas.ResponsesStreamResponseTypeCompleted, final.Type)
	var types []schemas.ResponsesMessageType
	for _, item := range final.Response.Output {
		types = append(types, *item.Type)
	}
	assert.Equal(t, []schemas.ResponsesMessageType{schemas.ResponsesMessageTypeToolSearchCall, schemas.ResponsesMessageTypeToolSearchOutput, schemas.ResponsesMessageTypeToolSearchCall}, types)
}

// TestGeminiInteractionsRequestToolSearchToBifrost pins the /genai route's translated copy: a native
// tool_search, deferred function and deferred mcp_server reach the Responses request, and a search
// in the input history becomes its tool_search items, so a fallback to another provider keeps the
// deferral and the routing copy no longer drops them.
func TestGeminiInteractionsRequestToolSearchToBifrost(t *testing.T) {
	var native GeminiInteractionsRequest
	require.NoError(t, sonic.Unmarshal([]byte(`{
		"model":"gemini-3.8-flash",
		"tools":[
			{"type":"tool_search"},
			{"type":"function","name":"get_weather","description":"Weather.","short_description":"Weather","defer_loading":true,"parameters":{"type":"object"}},
			{"type":"mcp_server","name":"dice","url":"https://m.example/mcp","defer_loading":true}
		],
		"input":[
			{"type":"user_input","content":[{"type":"text","text":"Weather?"}]},
			{"type":"tool_search_call","id":"ts_1","arguments":{"function_names":["get_weather"]},"signature":"sig_call"},
			{"type":"tool_search_result","call_id":"ts_1","signature":"sig_result","result":[{"type":"function","name":"get_weather"}]}
		]
	}`), &native))
	req, err := native.ToBifrostResponsesRequest()
	require.NoError(t, err)

	require.Len(t, req.Params.Tools, 3)
	assert.Equal(t, schemas.ResponsesToolTypeToolSearch, req.Params.Tools[0].Type)
	assert.False(t, req.Params.Tools[0].IsClientToolSearch())
	assert.True(t, *req.Params.Tools[1].DeferLoading)
	assert.True(t, *req.Params.Tools[2].DeferLoading)

	require.Len(t, req.Input, 3)
	assert.Equal(t, schemas.ResponsesMessageTypeToolSearchCall, *req.Input[1].Type)
	assert.Equal(t, "sig_call", *req.Input[1].ResponsesReasoning.EncryptedContent)
	assert.Equal(t, schemas.ResponsesMessageTypeToolSearchOutput, *req.Input[2].Type)
	assert.JSONEq(t, `[{"type":"function","name":"get_weather"}]`, string(req.Input[2].ToolSearchOutputTools))

	req.Params.Tools = req.Params.Tools[:2] // a translated mcp_server carries no require_approval to send back
	back, err := ToGeminiInteractionsRequest(req)
	require.NoError(t, err)
	steps, err := providerUtils.MarshalSorted(back.Input.Items[1:])
	require.NoError(t, err)
	assert.JSONEq(t, `[
		{"type":"tool_search_call","id":"ts_1","arguments":{"function_names":["get_weather"]},"signature":"sig_call"},
		{"type":"tool_search_result","call_id":"ts_1","signature":"sig_result","result":[{"type":"function","name":"get_weather"}]}
	]`, string(steps), "a Gemini fallback gets the steps back unchanged")
}

// TestToGeminiInteractionToolSearchItems pins an Interactions caller served by another provider:
// that provider's tool search items come back as the search steps, unary and streamed, and a
// client search as the function call the caller answers.
func TestToGeminiInteractionToolSearchItems(t *testing.T) {
	var output []schemas.ResponsesMessage
	require.NoError(t, sonic.Unmarshal([]byte(`[
		{"type":"tool_search_call","id":"tsc_1","call_id":"call_s","execution":"server","status":"completed","arguments":{"query":"weather"}},
		{"type":"tool_search_output","id":"tso_1","call_id":"call_s","execution":"server","status":"completed","tools":[{"type":"function","name":"get_weather"}]},
		{"type":"tool_search_call","id":"tsc_2","call_id":"call_c","execution":"client","status":"completed","arguments":{"query":"fx"}}
	]`), &output))
	interaction := ToGeminiInteraction(&schemas.BifrostResponsesResponse{ID: schemas.Ptr("resp_1"), Model: "gpt-5.5", Status: schemas.Ptr("completed"), Output: output})
	steps, err := providerUtils.MarshalSorted(interaction.Steps)
	require.NoError(t, err)
	want := `[
		{"type":"tool_search_call","id":"call_s","arguments":{"query":"weather"}},
		{"type":"tool_search_result","call_id":"call_s","result":[{"type":"function","name":"get_weather"}]},
		{"type":"function_call","id":"call_c","name":"tool_search","arguments":{"query":"fx"}}
	]`
	assert.JSONEq(t, want, string(steps))
	assert.Equal(t, GeminiInteractionStatusRequiresAction, interaction.Status, "the caller owes the client search an answer")

	state := NewBifrostToGeminiInteractionsStreamState()
	var started []GeminiInteractionsItem
	for i := range output {
		idx := i
		for _, ev := range []*schemas.BifrostResponsesStreamResponse{
			{Type: schemas.ResponsesStreamResponseTypeOutputItemAdded, OutputIndex: &idx, Item: &output[i]},
			{Type: schemas.ResponsesStreamResponseTypeOutputItemDone, OutputIndex: &idx, Item: &output[i]},
		} {
			for _, event := range state.ToGeminiInteractionsStreamEvents(ev) {
				if event.EventType == GeminiInteractionsEventStepStart {
					started = append(started, *event.Step)
				}
				// A client search call streams its arguments as a delta; rebuild the step from it.
				if event.EventType == GeminiInteractionsEventStepDelta && event.Delta.Type == geminiInteractionsDeltaArguments {
					var fragment string
					require.NoError(t, sonic.Unmarshal(event.Delta.Arguments, &fragment))
					started[len(started)-1].Arguments = json.RawMessage(fragment)
				}
			}
		}
	}
	streamed, err := providerUtils.MarshalSorted(started)
	require.NoError(t, err)
	assert.JSONEq(t, want, string(streamed))
}

// TestGeminiInteractionsRequestClientSearchHistoryToBifrost pins a translated follow-up that
// answers the caller's own search: the function_call to the client tool_search and its
// function_result listing the declarations it found become client tool_search items, instead of
// the result's declarations being refused as content blocks. A Gemini fallback gets the steps back.
func TestGeminiInteractionsRequestClientSearchHistoryToBifrost(t *testing.T) {
	var native GeminiInteractionsRequest
	require.NoError(t, sonic.Unmarshal([]byte(`{
		"model":"gemini-3.8-flash",
		"tools":[
			{"type":"tool_search","execution":"client","name":"find_tools","description":"Find tools.","parameters":{"type":"object","properties":{"query":{"type":"string"}}}}
		],
		"input":[
			{"type":"user_input","content":[{"type":"text","text":"Convert 5 USD"}]},
			{"type":"function_call","id":"call_c","name":"find_tools","arguments":{"query":"fx"}},
			{"type":"function_result","call_id":"call_c","name":"find_tools","result":[{"type":"function","name":"get_fx","description":"FX rate.","parameters":{"type":"object"}}]}
		]
	}`), &native))
	req, err := native.ToBifrostResponsesRequest()
	require.NoError(t, err)
	require.Len(t, req.Input, 3)
	call, result := req.Input[1], req.Input[2]
	require.Equal(t, schemas.ResponsesMessageTypeToolSearchCall, *call.Type)
	assert.True(t, call.IsClientToolSearchItem())
	assert.Equal(t, "call_c", *call.CallID)
	assert.JSONEq(t, `{"query":"fx"}`, *call.Arguments)
	require.Equal(t, schemas.ResponsesMessageTypeToolSearchOutput, *result.Type)
	assert.True(t, result.IsClientToolSearchItem())
	assert.Equal(t, "call_c", *result.CallID)
	assert.JSONEq(t, `[{"type":"function","name":"get_fx","description":"FX rate.","parameters":{"type":"object"}}]`, string(result.ToolSearchOutputTools))

	back, err := ToGeminiInteractionsRequest(req)
	require.NoError(t, err)
	steps, err := providerUtils.MarshalSorted(back.Input.Items[1:])
	require.NoError(t, err)
	assert.JSONEq(t, `[
		{"type":"function_call","id":"call_c","name":"find_tools","arguments":{"query":"fx"}},
		{"type":"function_result","call_id":"call_c","name":"find_tools","result":[{"type":"function","name":"get_fx","description":"FX rate.","parameters":{"type":"object"}}]}
	]`, string(steps))
}

// TestToGeminiInteractionClientSearchStreamsArguments pins that a translated client search call
// streams its arguments as an arguments delta, as Google streams a function call, so a reader that
// builds the call from deltas keeps the query.
func TestToGeminiInteractionClientSearchStreamsArguments(t *testing.T) {
	var item schemas.ResponsesMessage
	require.NoError(t, sonic.Unmarshal([]byte(`{"type":"tool_search_call","id":"tsc_2","call_id":"call_c","execution":"client","status":"completed","arguments":{"query":"fx"}}`), &item))
	state := NewBifrostToGeminiInteractionsStreamState()
	state.ClientToolSearchName = "find_tools"
	idx := 0
	var events []*GeminiInteractionsEvent
	for _, ev := range []*schemas.BifrostResponsesStreamResponse{
		{Type: schemas.ResponsesStreamResponseTypeOutputItemAdded, OutputIndex: &idx, Item: &item},
		{Type: schemas.ResponsesStreamResponseTypeOutputItemDone, OutputIndex: &idx, Item: &item},
	} {
		events = append(events, state.ToGeminiInteractionsStreamEvents(ev)...)
	}
	reader := NewGeminiInteractionsStreamState("gemini-3.8-flash")
	reader.ClientToolSearchName = "find_tools"
	var done *schemas.ResponsesMessage
	for _, event := range events {
		raw, err := sonic.Marshal(event)
		require.NoError(t, err)
		parsed, err := ParseGeminiInteractionsEvent(raw)
		require.NoError(t, err)
		out, bifrostErr := reader.ToBifrostResponsesStream(parsed)
		require.Nil(t, bifrostErr)
		for _, ev := range out {
			if ev.Type == schemas.ResponsesStreamResponseTypeOutputItemDone {
				done = ev.Item
			}
		}
	}
	require.NotNil(t, done)
	require.Equal(t, schemas.ResponsesMessageTypeToolSearchCall, *done.Type)
	require.NotNil(t, done.Arguments)
	assert.JSONEq(t, `{"query":"fx"}`, *done.Arguments)
}

// TestGeminiInteractionsRequestClientSearchFailedResult pins that a failed answer to the caller's
// own search stays a failure: the client tool_search_output is incomplete and keeps the error text
// in memory without sending an error field on the Responses wire, and a Gemini fallback gets back a
// function_result with is_error and the original text.
func TestGeminiInteractionsRequestClientSearchFailedResult(t *testing.T) {
	var native GeminiInteractionsRequest
	require.NoError(t, sonic.Unmarshal([]byte(`{
		"model":"gemini-3.8-flash",
		"tools":[{"type":"tool_search","execution":"client","name":"find_tools","parameters":{"type":"object"}}],
		"input":[
			{"type":"user_input","content":[{"type":"text","text":"Convert 5 USD"}]},
			{"type":"function_call","id":"call_c","name":"find_tools","arguments":{"query":"fx"}},
			{"type":"function_result","call_id":"call_c","name":"find_tools","is_error":true,"result":"registry offline"}
		]
	}`), &native))
	req, err := native.ToBifrostResponsesRequest()
	require.NoError(t, err)
	require.Len(t, req.Input, 3)
	result := req.Input[2]
	require.Equal(t, schemas.ResponsesMessageTypeToolSearchOutput, *result.Type)
	require.NotNil(t, result.Status)
	assert.Equal(t, "incomplete", *result.Status)
	require.NotNil(t, result.Error)
	require.NotNil(t, result.Error.ResponsesToolMessageErrorStr)
	assert.Equal(t, "registry offline", *result.Error.ResponsesToolMessageErrorStr)
	wire, err := sonic.Marshal(result)
	require.NoError(t, err)
	assert.NotContains(t, string(wire), `"error"`, "tool_search_output has no error field on the Responses wire")

	back, err := ToGeminiInteractionsRequest(req)
	require.NoError(t, err)
	steps, err := providerUtils.MarshalSorted(back.Input.Items[2:])
	require.NoError(t, err)
	assert.JSONEq(t, `[{"type":"function_result","call_id":"call_c","name":"find_tools","is_error":true,"result":"registry offline"}]`, string(steps))
}

// A client tool_search on the generateContent path goes upstream as a function named after the
// search, its history as that function's call and response, and the model's call to it comes
// back as a client tool_search_call, unary and streamed. Before, the tool and its history were
// dropped, so the model could not call the search the caller declared.
func TestGeminiResponsesGenerateContentClientToolSearch(t *testing.T) {
	var tools []schemas.ResponsesTool
	require.NoError(t, sonic.Unmarshal([]byte(`[
		{"type":"function","name":"get_weather","defer_loading":true,"parameters":{"type":"object","properties":{"city":{"type":"string"}}}},
		{"type":"tool_search","execution":"client","description":"Find tools by keyword"}
	]`), &tools))
	var input []schemas.ResponsesMessage
	require.NoError(t, sonic.Unmarshal([]byte(`[
		{"type":"message","role":"user","content":"Forecast for Rome, then convert 5 USD"},
		{"type":"tool_search_call","execution":"client","call_id":"ts_1","arguments":{"query":"forecast"},"status":"completed"},
		{"type":"tool_search_output","execution":"client","call_id":"ts_1","status":"completed","tools":[{"type":"function","name":"get_forecast","description":"Daily forecast","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}]}
	]`), &input))
	newRequest := func() *schemas.BifrostResponsesRequest {
		return &schemas.BifrostResponsesRequest{Provider: schemas.Gemini, Model: "gemini-2.5-flash", Input: input, Params: &schemas.ResponsesParameters{Tools: tools}}
	}
	callPart := `{"functionCall":{"id":"call_9","name":"tool_search","args":{"query":"currency"}}}`

	checkUpstream := func(t *testing.T, body string) {
		t.Helper()
		decls := gjson.Get(body, "tools.#.functionDeclarations|@flatten")
		names := []string{}
		for _, d := range decls.Array() {
			names = append(names, d.Get("name").String())
		}
		assert.ElementsMatch(t, []string{"get_weather", "tool_search", "get_forecast"}, names, body)
		search := gjson.Get(body, `tools.#.functionDeclarations|@flatten|#(name=="tool_search")`)
		assert.Equal(t, "Find tools by keyword", search.Get("description").String(), body)
		assert.Equal(t, "string", search.Get("parametersJsonSchema.properties.query.type").String(), "a search without parameters gets a query schema: "+body)
		call := gjson.Get(body, `contents.#.parts|@flatten|#(functionCall.name=="tool_search")`)
		require.True(t, call.Exists(), "the client search call replays as a functionCall: "+body)
		assert.Equal(t, "forecast", call.Get("functionCall.args.query").String(), body)
		result := gjson.Get(body, `contents.#.parts|@flatten|#(functionResponse.name=="tool_search")`)
		require.True(t, result.Exists(), "the client search output replays as a functionResponse: "+body)
		assert.Contains(t, result.Get("functionResponse").Raw, "get_forecast", body)
	}
	checkCall := func(t *testing.T, item *schemas.ResponsesMessage) {
		t.Helper()
		require.NotNil(t, item.Type)
		assert.Equal(t, schemas.ResponsesMessageTypeToolSearchCall, *item.Type)
		require.NotNil(t, item.Execution)
		assert.Equal(t, schemas.ToolSearchExecutionClient, *item.Execution)
		assert.Nil(t, item.Name)
		require.NotNil(t, item.CallID)
		assert.Equal(t, "call_9", *item.CallID)
		require.NotNil(t, item.Arguments)
		assert.JSONEq(t, `{"query":"currency"}`, *item.Arguments)
	}

	t.Run("unary", func(t *testing.T) {
		var body string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			body = string(raw)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[`+callPart+`]},"finishReason":"STOP"}],"modelVersion":"gemini-2.5-flash"}`)
		}))
		defer server.Close()
		provider := NewGeminiProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL + "/v1beta"}}, nil)
		key := schemas.Key{Value: schemas.SecretVar{Val: "test-key"}, Models: []string{"*"}}
		req := newRequest()
		resp, bErr := provider.Responses(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), key, req)
		require.Nil(t, bErr)
		checkUpstream(t, body)
		assert.Equal(t, schemas.ResponsesToolTypeToolSearch, req.Params.Tools[1].Type, "the caller's request is not rewritten")
		assert.Equal(t, schemas.ResponsesMessageTypeToolSearchCall, *req.Input[1].Type, "the caller's history is not rewritten")
		var calls []schemas.ResponsesMessage
		for _, item := range resp.Output {
			if item.Type != nil && (*item.Type == schemas.ResponsesMessageTypeToolSearchCall || *item.Type == schemas.ResponsesMessageTypeFunctionCall) {
				calls = append(calls, item)
			}
		}
		require.Len(t, calls, 1)
		checkCall(t, &calls[0])
	})

	t.Run("stream", func(t *testing.T) {
		var body string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			body = string(raw)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, `data: {"candidates":[{"content":{"role":"model","parts":[`+callPart+`]},"finishReason":"STOP"}],"modelVersion":"gemini-2.5-flash"}`+"\n\n")
		}))
		defer server.Close()
		provider := NewGeminiProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL + "/v1beta"}}, nil)
		key := schemas.Key{Value: schemas.SecretVar{Val: "test-key"}, Models: []string{"*"}}
		postHook := func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
			return result, err
		}
		stream, bErr := provider.ResponsesStream(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), postHook, func(context.Context) {}, key, newRequest())
		require.Nil(t, bErr)
		var added, done []*schemas.ResponsesMessage
		var argEvents int
		for chunk := range stream {
			require.Nil(t, chunk.BifrostError)
			ev := chunk.BifrostResponsesStreamResponse
			if ev == nil {
				continue
			}
			switch ev.Type {
			case schemas.ResponsesStreamResponseTypeOutputItemAdded:
				added = append(added, ev.Item)
			case schemas.ResponsesStreamResponseTypeOutputItemDone:
				done = append(done, ev.Item)
			case schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta, schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone:
				argEvents++
			}
		}
		checkUpstream(t, body)
		require.Len(t, added, 1)
		assert.Equal(t, schemas.ResponsesMessageTypeToolSearchCall, *added[0].Type)
		assert.Zero(t, argEvents, "a client search call carries its arguments whole, as the Interactions stream does")
		require.Len(t, done, 1)
		checkCall(t, done[0])
	})
}

// A function a client search loaded that has the search's own name would be read back as the
// search, so the caller could never dispatch it; it is refused rather than dropped.
func TestGeminiResponsesClientToolSearchLoadedNameCollision(t *testing.T) {
	var tools []schemas.ResponsesTool
	require.NoError(t, sonic.Unmarshal([]byte(`[{"type":"tool_search","execution":"client"}]`), &tools))
	var input []schemas.ResponsesMessage
	require.NoError(t, sonic.Unmarshal([]byte(`[
		{"type":"message","role":"user","content":"hi"},
		{"type":"tool_search_call","execution":"client","call_id":"ts_1","arguments":{"query":"x"}},
		{"type":"tool_search_output","execution":"client","call_id":"ts_1","tools":[{"type":"function","name":"tool_search","parameters":{"type":"object"}}]}
	]`), &input))
	_, err := ToGeminiResponsesRequest(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
		&schemas.BifrostResponsesRequest{Provider: schemas.Gemini, Model: "gemini-2.5-flash", Input: input, Params: &schemas.ResponsesParameters{Tools: tools}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tool_search")
}

// TestGeminiInteractionsClientToolSearchDescription pins that a client tool_search always reaches
// the Interactions API with the description Google requires: the caller's, or a default.
func TestGeminiInteractionsClientToolSearchDescription(t *testing.T) {
	for decl, want := range map[string]string{
		`{"type":"tool_search","execution":"client"}`:                            geminiClientToolSearchDescription,
		`{"type":"tool_search","execution":"client","description":""}`:           geminiClientToolSearchDescription,
		`{"type":"tool_search","execution":"client","description":"Find tools"}`: "Find tools",
	} {
		var tool schemas.ResponsesTool
		require.NoError(t, sonic.Unmarshal([]byte(decl), &tool))
		native := toGeminiInteractionsToolSearchTool(tool)
		require.NotNil(t, native.Description, decl)
		assert.Equal(t, want, *native.Description, decl)
	}
	var server schemas.ResponsesTool
	require.NoError(t, sonic.Unmarshal([]byte(`{"type":"tool_search"}`), &server))
	assert.Nil(t, toGeminiInteractionsToolSearchTool(server).Description, "a server search takes no settings")
}

// TestGeminiInteractionsClientSearchContinuationWithoutName pins a continuation that answers the
// previous turn's client search with only call_id and the found definitions: it is the search's
// output, though this request holds neither the call nor the tool's name. An ordinary function
// result is untouched.
func TestGeminiInteractionsClientSearchContinuationWithoutName(t *testing.T) {
	convert := func(result string) schemas.ResponsesMessage {
		var native GeminiInteractionsRequest
		require.NoError(t, sonic.Unmarshal([]byte(`{"model":"gpt-5","previous_interaction_id":"resp_prev",
			"tools":[{"type":"tool_search","execution":"client","name":"find_tools","description":"d"}],
			"input":[{"type":"function_result","call_id":"call_9","result":`+result+`}]}`), &native))
		req, err := native.ToBifrostResponsesRequest()
		require.NoError(t, err)
		require.Len(t, req.Input, 1)
		return req.Input[0]
	}
	found := convert(`[{"type":"function","name":"convert_currency","parameters":{"type":"object"}}]`)
	assert.Equal(t, schemas.ResponsesMessageTypeToolSearchOutput, *found.Type)
	assert.True(t, found.IsClientToolSearchItem())
	assert.Equal(t, "call_9", *found.CallID)
	assert.Contains(t, string(found.ToolSearchOutputTools), "convert_currency")
	for _, result := range []string{`"ok"`, `[]`, `[{"type":"text","text":"42"}]`} {
		assert.Equal(t, schemas.ResponsesMessageTypeFunctionCallOutput, *convert(result).Type, result)
	}
}

// TestGeminiInteractionsOrdinaryCallKeepsItsResult pins that a result of a call the request holds is that call's,
// though it lists function definitions and leaves out its name.
func TestGeminiInteractionsOrdinaryCallKeepsItsResult(t *testing.T) {
	var native GeminiInteractionsRequest
	require.NoError(t, sonic.Unmarshal([]byte(`{"model":"gpt-5","tools":[{"type":"tool_search","execution":"client","name":"find_tools","description":"d"}],"input":[
		{"type":"function_call","id":"c","name":"list_tools","arguments":{}},
		{"type":"function_result","call_id":"c","result":[{"type":"function","name":"f","parameters":{"type":"object"}}]},
		{"type":"function_call","id":"s","name":"find_tools","arguments":{}},
		{"type":"function_result","call_id":"s","result":[{"type":"function","name":"g","parameters":{"type":"object"}}]}]}`), &native))
	req, err := native.ToBifrostResponsesRequest()
	require.NoError(t, err)
	require.Len(t, req.Input, 4)
	assert.Equal(t, schemas.ResponsesMessageTypeFunctionCallOutput, *req.Input[1].Type)
	assert.Equal(t, schemas.ResponsesMessageTypeToolSearchOutput, *req.Input[3].Type)
}

// TestToGeminiInteractionHostedSearchPairs pins that a hosted search's output, which OpenAI gives an id of its own
// and no call_id, answers the call before it, unary and streamed. Outputs that name their call keep it.
func TestToGeminiInteractionHostedSearchPairs(t *testing.T) {
	item := func(raw string) schemas.ResponsesMessage {
		var m schemas.ResponsesMessage
		require.NoError(t, sonic.Unmarshal([]byte(raw), &m))
		return m
	}
	output := []schemas.ResponsesMessage{
		item(`{"type":"tool_search_call","id":"tsc_1","execution":"server","call_id":null,"status":"completed","arguments":{"paths":["a"]}}`),
		item(`{"type":"tool_search_output","id":"tso_1","execution":"server","call_id":null,"status":"completed","tools":[]}`),
		item(`{"type":"tool_search_call","id":"tsc_2","execution":"server","call_id":null,"status":"completed","arguments":{}}`),
		item(`{"type":"tool_search_output","id":"tso_2","execution":"server","call_id":null,"status":"completed","tools":[]}`),
		item(`{"type":"tool_search_call","id":"tsc_3","execution":"server","call_id":"x","status":"completed","arguments":{}}`),
		item(`{"type":"tool_search_output","id":"tso_3","execution":"server","call_id":"x","status":"completed","tools":[]}`),
	}
	check := func(steps []GeminiInteractionsItem) {
		t.Helper()
		require.Len(t, steps, 6)
		for i, id := range []string{"tsc_1", "tsc_2", "x"} {
			require.NotNil(t, steps[2*i].ID)
			assert.Equal(t, id, *steps[2*i].ID)
			require.NotNil(t, steps[2*i+1].CallID)
			assert.Equal(t, id, *steps[2*i+1].CallID)
		}
	}
	final := &schemas.BifrostResponsesResponse{ID: schemas.Ptr("r"), Status: schemas.Ptr("completed"), Output: output}
	check(ToGeminiInteraction(final).Steps)
	assert.Nil(t, output[1].CallID, "the provider's response is not modified")

	s := NewBifrostToGeminiInteractionsStreamState()
	var live []GeminiInteractionsItem
	for i := range output {
		for _, typ := range []schemas.ResponsesStreamResponseType{schemas.ResponsesStreamResponseTypeOutputItemAdded, schemas.ResponsesStreamResponseTypeOutputItemDone} {
			for _, ev := range s.ToGeminiInteractionsStreamEvents(&schemas.BifrostResponsesStreamResponse{Type: typ, OutputIndex: schemas.Ptr(i), Item: &output[i]}) {
				if ev.EventType == GeminiInteractionsEventStepStart {
					live = append(live, *ev.Step)
				}
			}
		}
	}
	check(live)
	for _, ev := range s.ToGeminiInteractionsStreamEvents(&schemas.BifrostResponsesStreamResponse{Type: schemas.ResponsesStreamResponseTypeCompleted, Response: final}) {
		check(ev.Interaction.Steps)
	}
}

// TestGeminiClientSearchFailureAndMessageSurvive pins two search outcomes that carry no tools: a
// failure still replays as one after a Responses JSON round trip, which keeps only its status, and
// a no-match message reaches Google again instead of becoming an empty list.
func TestGeminiClientSearchFailureAndMessageSurvive(t *testing.T) {
	step := func(item schemas.ResponsesMessage) GeminiInteractionsItem {
		out, ok, err := responsesToolSearchItemToGeminiInteractionStep(item, "find_tools")
		require.NoError(t, err)
		require.True(t, ok)
		return out
	}
	var failed, empty schemas.ResponsesMessage
	require.NoError(t, sonic.Unmarshal([]byte(`{"type":"tool_search_output","execution":"client","call_id":"c","status":"incomplete","tools":[]}`), &failed))
	require.NoError(t, sonic.Unmarshal([]byte(`{"type":"tool_search_output","execution":"client","call_id":"c","status":"completed","tools":[]}`), &empty))
	require.NotNil(t, step(failed).IsError)
	assert.True(t, *step(failed).IsError)
	assert.JSONEq(t, `"tool search failed"`, string(step(failed).Result))
	assert.Nil(t, step(empty).IsError, "a search that found nothing succeeded")
	assert.JSONEq(t, `[]`, string(step(empty).Result))

	var native GeminiInteractionsRequest
	require.NoError(t, sonic.Unmarshal([]byte(`{"model":"gemini-3.8-flash","tools":[{"type":"tool_search","execution":"client","name":"find_tools","description":"d"}],"input":[
		{"type":"function_call","id":"c","name":"find_tools","arguments":{}},
		{"type":"function_result","call_id":"c","name":"find_tools","result":"No match. Try billing or weather."}]}`), &native))
	req, err := native.ToBifrostResponsesRequest()
	require.NoError(t, err)
	back, err := ToGeminiInteractionsRequest(req)
	require.NoError(t, err)
	assert.JSONEq(t, `"No match. Try billing or weather."`, string(back.Input.Items[1].Result))
	wire, err := sonic.Marshal(req.Input[1])
	require.NoError(t, err)
	assert.NotContains(t, string(wire), `"output"`, "tool_search_output has no output field on the Responses wire")

	lowered, err := ToGeminiResponsesRequest(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), &schemas.BifrostResponsesRequest{
		Provider: schemas.Gemini, Model: "gemini-2.5-flash", Input: req.Input, Params: req.Params})
	require.NoError(t, err)
	body, err := sonic.Marshal(lowered)
	require.NoError(t, err)
	assert.Contains(t, string(body), "No match. Try billing or weather.", "generateContent gets the message too")
}

// TestGeminiInteractionsRawClientSearchNameEscaped pins that a raw body's client search is found by
// its decoded type, however the bytes spell it.
func TestGeminiInteractionsRawClientSearchNameEscaped(t *testing.T) {
	assert.Equal(t, "find_tools", geminiInteractionsClientToolSearchName([]byte(`{"model":"m","tools":[{"type":"tool`+"\\"+`u005fsearch","execution":"client","name":"find_tools"}]}`)))
	assert.Empty(t, geminiInteractionsClientToolSearchName([]byte(`{"model":"m","tools":[{"type":"function","name":"f"}]}`)))
}

// TestToGeminiResponsesRequestLeavesContextAlone pins that the converter has no side effects on the
// caller's context: the provider records the search name it needs for the response.
func TestToGeminiResponsesRequestLeavesContextAlone(t *testing.T) {
	var tools []schemas.ResponsesTool
	require.NoError(t, sonic.Unmarshal([]byte(`[{"type":"tool_search","execution":"client"}]`), &tools))
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, err := ToGeminiResponsesRequest(ctx, &schemas.BifrostResponsesRequest{Provider: schemas.Gemini, Model: "gemini-2.5-flash", Input: []schemas.ResponsesMessage{interactionsUserText("hi")}, Params: &schemas.ResponsesParameters{Tools: tools}})
	require.NoError(t, err)
	assert.Empty(t, geminiClientToolSearchFrom(ctx))
}

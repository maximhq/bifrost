package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestToOpenAIDecisionRequestSendsOnlyText pins how the Typesafe-only parts of
// a normalized request reach OpenAI: a structured input, instruction, or
// description becomes sorted JSON text, a predicate's criteria are folded into
// its instructions, and an unlabelled level is labelled by its index. Names,
// boolean choices, and the safety identifier pass through.
func TestToOpenAIDecisionRequestSendsOnlyText(t *testing.T) {
	request := &schemas.BifrostDecisionRequest{
		Provider:         schemas.OpenAI,
		Model:            "gpt-6-luna",
		Input:            schemas.DecisionInput{Structured: map[string]any{"ticket": "refund", "id": 42}},
		SafetyIdentifier: schemas.Ptr("user-1"),
		Questions: []schemas.DecisionQuestion{
			{
				Type:         schemas.DecisionTypePredicate,
				Name:         schemas.Ptr("angry"),
				Instructions: &schemas.DecisionText{Structured: map[string]any{"goal": "judge tone"}},
				Criteria:     &schemas.DecisionCriteria{True: schemas.NewDecisionText("upset"), False: schemas.NewDecisionText("calm")},
			},
			{
				Type:         schemas.DecisionTypeChoice,
				Instructions: schemas.NewDecisionText("Refund?"),
				Choices: []schemas.DecisionChoice{
					{Value: schemas.DecisionScalar{Bool: schemas.Ptr(true)}, Description: &schemas.DecisionText{Structured: []any{"refund", "now"}}},
					{Value: schemas.DecisionScalar{Bool: schemas.Ptr(false)}},
				},
			},
			{
				Type:         schemas.DecisionTypeScore,
				Name:         schemas.Ptr("severity"),
				Instructions: schemas.NewDecisionText("Rate"),
				Levels:       []schemas.DecisionLevel{{Description: schemas.NewDecisionText("cosmetic")}, {Label: "Blocked"}},
			},
		},
	}

	native, err := ToOpenAIDecisionRequest(request)
	require.NoError(t, err)
	body, err := json.Marshal(native)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"model": "gpt-6-luna",
		"input": "{\"id\":42,\"ticket\":\"refund\"}",
		"questions": [
			{"type": "predicate", "name": "angry", "instructions": "{\"goal\":\"judge tone\"} Meaning (answer: meaning): true=upset, false=calm"},
			{"type": "choice", "instructions": "Refund?", "choices": [{"value": true, "description": "[\"refund\",\"now\"]"}, {"value": false}]},
			{"type": "score", "name": "severity", "instructions": "Rate", "levels": [{"label": "0", "description": "cosmetic"}, {"label": "Blocked"}]}
		],
		"safety_identifier": "user-1"
	}`, string(body))
	assert.NotNil(t, request.Questions[0].Criteria, "the caller's request is not modified")
}

// TestToOpenAIDecisionRequestKeepsMessages pins that input messages, inline
// images included, reach OpenAI unchanged.
func TestToOpenAIDecisionRequestKeepsMessages(t *testing.T) {
	messages := []schemas.DecisionInputMessage{{Type: schemas.Ptr("message"), Role: "user", Content: schemas.DecisionInputContent{Parts: []schemas.DecisionInputPart{
		{Type: schemas.DecisionInputPartTypeText, Text: schemas.Ptr("Damaged?")},
		{Type: schemas.DecisionInputPartTypeImage, ImageURL: schemas.Ptr("https://example.com/box.png"), Detail: schemas.Ptr("high")},
	}}}}
	native, err := ToOpenAIDecisionRequest(&schemas.BifrostDecisionRequest{
		Model:     "gpt-6-luna",
		Input:     schemas.DecisionInput{Messages: messages},
		Questions: []schemas.DecisionQuestion{{Type: schemas.DecisionTypePredicate, Instructions: schemas.NewDecisionText("Is the box damaged?")}},
	})
	require.NoError(t, err)
	assert.Equal(t, messages, native.Input.Messages)
	body, err := json.Marshal(native)
	require.NoError(t, err)
	assert.Contains(t, string(body), `{"type":"input_image","image_url":"https://example.com/box.png","detail":"high"}`, "the image detail reaches OpenAI")
	assert.Contains(t, string(body), `{"type":"message","role":"user"`)
}

// TestToOpenAIDecisionRequestRejections pins the requests OpenAI cannot serve:
// each is a 400 for this attempt, so a fallback can take it.
func TestToOpenAIDecisionRequestRejections(t *testing.T) {
	text := schemas.DecisionInput{Text: schemas.Ptr("state")}
	cases := map[string]struct {
		request *schemas.BifrostDecisionRequest
		wantSub string
	}{
		"null input": {
			request: &schemas.BifrostDecisionRequest{Questions: []schemas.DecisionQuestion{{Type: schemas.DecisionTypePredicate}}},
			wantSub: "require an input",
		},
		"no questions": {
			request: &schemas.BifrostDecisionRequest{Input: text},
			wantSub: "at least one question",
		},
		"audio part": {
			request: &schemas.BifrostDecisionRequest{
				Input: schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{{Role: "user", Content: schemas.DecisionInputContent{Parts: []schemas.DecisionInputPart{
					{Type: schemas.DecisionInputPartTypeText, Text: schemas.Ptr("listen")},
					{Type: "input_audio"},
				}}}}},
				Questions: []schemas.DecisionQuestion{{Type: schemas.DecisionTypePredicate}},
			},
			wantSub: `not "input_audio"`,
		},
		"unsupported type": {
			request: &schemas.BifrostDecisionRequest{Input: text, Questions: []schemas.DecisionQuestion{{Type: "ranking"}}},
			wantSub: "unsupported type",
		},
		"choice without choices": {
			request: &schemas.BifrostDecisionRequest{Input: text, Questions: []schemas.DecisionQuestion{{Type: schemas.DecisionTypeChoice}}},
			wantSub: "requires choices",
		},
		"score without levels": {
			request: &schemas.BifrostDecisionRequest{Input: text, Questions: []schemas.DecisionQuestion{{Type: schemas.DecisionTypeScore}}},
			wantSub: "requires levels",
		},
		"numeric choice": {
			request: &schemas.BifrostDecisionRequest{Input: text, Questions: []schemas.DecisionQuestion{{Type: schemas.DecisionTypeChoice, Choices: []schemas.DecisionChoice{{Value: schemas.DecisionScalar{Num: schemas.Ptr(1.0)}}}}}},
			wantSub: "string or a boolean",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ToOpenAIDecisionRequest(tc.request)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantSub)
		})
	}
}

// TestOpenAIDecisionResponseToBifrost pins how OpenAI's answers are matched
// back: by position, an unnamed answer taking its question's name, a refusal
// kept as it is, a score answer given the legend
// OpenAI does not send, and usage mapped with its cached tokens. The payload is
// synthetic.
func TestOpenAIDecisionResponseToBifrost(t *testing.T) {
	var native OpenAIDecisionResponse
	require.NoError(t, json.Unmarshal([]byte(`{
		"id": "dec_1",
		"model": "gpt-6-luna-2026-09-01",
		"answers": [
			{"type": "predicate", "probability": 0.92},
			{"type": "refusal", "name": "declined"},
			{"type": "score", "name": "urgency", "score": 1.5, "probabilities": [{"value": 1, "label": "medium", "probability": 0.5}, {"value": 2, "label": "high", "probability": 0.5}]}
		],
		"usage": {"input_tokens": 120, "input_tokens_details": {"cached_tokens": 30}, "output_tokens": 4, "total_tokens": 124}
	}`), &native))
	request := &schemas.BifrostDecisionRequest{Questions: []schemas.DecisionQuestion{
		{Type: schemas.DecisionTypePredicate, Name: schemas.Ptr("angry")},
		{Type: schemas.DecisionTypeChoice, Name: schemas.Ptr("declined")},
		{Type: schemas.DecisionTypeScore, Name: schemas.Ptr("urgency"), Levels: []schemas.DecisionLevel{
			{Label: "low", Description: schemas.NewDecisionText("can wait")},
			{Label: "medium"},
			{Label: "high", Description: schemas.NewDecisionText("today")},
		}},
	}}

	response, err := native.ToBifrostDecisionResponse(request)
	require.NoError(t, err)
	assert.Empty(t, response.ID, "OpenAI's Decision object has no id")
	require.Len(t, response.Answers, 3)
	assert.Equal(t, "angry", *response.Answers[0].Name)
	assert.Equal(t, 0.92, *response.Answers[0].Probability)
	assert.Equal(t, schemas.DecisionTypeRefusal, response.Answers[1].Type)
	assert.Equal(t, map[string]any{"0": "low: can wait", "1": "medium", "2": "high: today"}, response.Answers[2].Legend)
	assert.Equal(t, 120, response.Usage.PromptTokens)
	assert.Equal(t, 4, response.Usage.CompletionTokens)
	require.NotNil(t, response.Usage.PromptTokensDetails)
	assert.Equal(t, 30, response.Usage.PromptTokensDetails.CachedReadTokens)
}

// TestOpenAIDecisionResponseMismatches pins that an answer that cannot belong
// to its question is an error, so a fallback can take the request rather than
// answers being misattributed.
func TestOpenAIDecisionResponseMismatches(t *testing.T) {
	request := &schemas.BifrostDecisionRequest{Questions: []schemas.DecisionQuestion{{Type: schemas.DecisionTypePredicate, Name: schemas.Ptr("angry")}}}
	cases := map[string]string{
		`[]`: "0 decision answers for 1 questions",
		`[{"type":"predicate","name":"calm","probability":0.1}]`: "named \"calm\"",
		`[{"type":"choice","name":"angry","choice":"yes"}]`:      "as \"choice\"",
		`[{"type":"ranking","name":"angry","order":["a"]}]`:      "as \"ranking\"",
	}
	for answers, wantSub := range cases {
		var native OpenAIDecisionResponse
		require.NoError(t, json.Unmarshal([]byte(`{"answers":`+answers+`}`), &native))
		_, err := native.ToBifrostDecisionResponse(request)
		require.Error(t, err, answers)
		assert.Contains(t, err.Error(), wantSub)
	}
}

// TestOpenAIProviderDecisionGating pins which models are sent to OpenAI's
// decisions endpoint: a datasheet supports_decisions row decides in either
// direction, and with no row the name-based fallback serves gpt-6-luna. Any
// other model is reported as unsupported, so core emulates it.
func TestOpenAIProviderDecisionGating(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, r.URL.Path+" "+string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"dec_1","model":"m","answers":[{"type":"predicate","name":"q","probability":0.5}],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}`))
	}))
	defer server.Close()

	rows := map[string]bool{"gpt-7-decider": true, "gpt-6-luna-blocked": false}
	schemas.SetCapabilityResolver(func(_ schemas.ModelProvider, model string) *schemas.ModelCapabilities {
		if supports, ok := rows[model]; ok {
			return &schemas.ModelCapabilities{SupportsDecisions: &supports}
		}
		return nil
	})
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })

	provider := NewOpenAIProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL}}, testNoopLogger{})
	key := schemas.Key{Value: *schemas.NewSecretVar("test-key")}
	decide := func(model string) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		return provider.Decision(ctx, key, &schemas.BifrostDecisionRequest{
			Provider:  schemas.OpenAI,
			Model:     model,
			Input:     schemas.DecisionInput{Text: schemas.Ptr("state")},
			Questions: []schemas.DecisionQuestion{{Type: schemas.DecisionTypePredicate, Name: schemas.Ptr("q"), Instructions: schemas.NewDecisionText("Q?")}},
		})
	}

	for _, model := range []string{"gpt-7-decider", "gpt-6-luna"} {
		resp, bifrostErr := decide(model)
		require.Nil(t, bifrostErr, model)
		require.Len(t, resp.Answers, 1)
	}
	for _, model := range []string{"gpt-6-luna-blocked", "gpt-4o-mini"} {
		_, bifrostErr := decide(model)
		require.NotNil(t, bifrostErr, model)
		require.NotNil(t, bifrostErr.Error.Code)
		assert.Equal(t, "unsupported_operation", *bifrostErr.Error.Code, model)
	}

	require.Len(t, calls, 2, "only the decisions models reach the endpoint")
	for _, call := range calls {
		assert.True(t, strings.HasPrefix(call, "/v1/decisions "), call)
	}
}

// TestOpenAIDecisionRequestKeepsUnknownFields pins that a top-level field the
// request does not model is kept, compacted, for the provider wire, and that
// the route's fallbacks stay off it.
func TestOpenAIDecisionRequestKeepsUnknownFields(t *testing.T) {
	var request OpenAIDecisionRequest
	require.NoError(t, json.Unmarshal([]byte(`{
		"model": "gpt-6-luna",
		"input": "state",
		"questions": [{"type": "predicate", "instructions": "Q?"}],
		"fallbacks": ["typesafe/jev-1.13.0"],
		"future_option": { "mode": "strict" }
	}`), &request))
	assert.Equal(t, []string{"typesafe/jev-1.13.0"}, request.Fallbacks)
	assert.Equal(t, json.RawMessage(`{"mode":"strict"}`), request.ExtraParams["future_option"])
	assert.NotContains(t, request.ExtraParams, "fallbacks")

	converted, err := request.ToBifrostDecisionRequest()
	require.NoError(t, err)
	upstream, err := ToOpenAIDecisionRequest(converted)
	require.NoError(t, err)
	body, err := json.Marshal(upstream)
	require.NoError(t, err)
	assert.NotContains(t, string(body), "fallbacks", "fallbacks are Bifrost routing, never sent to OpenAI")
}

// TestOpenAIDecisionRequestToBifrost pins the route's request conversion:
// OpenAI's questions map onto the shared ones unchanged, and a model without a
// provider prefix is OpenAI's.
func TestOpenAIDecisionRequestToBifrost(t *testing.T) {
	var request OpenAIDecisionRequest
	require.NoError(t, json.Unmarshal([]byte(`{
		"model": "gpt-6-luna",
		"input": "state",
		"safety_identifier": "user-1",
		"questions": [
			{"type": "predicate", "name": "angry", "instructions": "Upset?"},
			{"type": "choice", "instructions": "Refund?", "choices": [{"value": true, "description": "yes"}, {"value": false}]},
			{"type": "score", "instructions": "Rate", "levels": [{"label": "low"}, {"label": "high", "description": "today"}]}
		]
	}`), &request))
	converted, err := request.ToBifrostDecisionRequest()
	require.NoError(t, err)
	assert.Equal(t, schemas.OpenAI, converted.Provider)
	assert.Equal(t, "gpt-6-luna", converted.Model)
	assert.Equal(t, "state", *converted.Input.Text)
	assert.Equal(t, "user-1", *converted.SafetyIdentifier)
	assert.Equal(t, []schemas.DecisionQuestion{
		{Type: schemas.DecisionTypePredicate, Name: schemas.Ptr("angry"), Instructions: schemas.NewDecisionText("Upset?")},
		{Type: schemas.DecisionTypeChoice, Instructions: schemas.NewDecisionText("Refund?"), Choices: []schemas.DecisionChoice{
			{Value: schemas.DecisionScalar{Bool: schemas.Ptr(true)}, Description: schemas.NewDecisionText("yes")},
			{Value: schemas.DecisionScalar{Bool: schemas.Ptr(false)}},
		}},
		{Type: schemas.DecisionTypeScore, Instructions: schemas.NewDecisionText("Rate"), Levels: []schemas.DecisionLevel{
			{Label: "low"},
			{Label: "high", Description: schemas.NewDecisionText("today")},
		}},
	}, converted.Questions)

	request.Model = "typesafe/jev-1.13.0"
	converted, err = request.ToBifrostDecisionRequest()
	require.NoError(t, err)
	assert.Equal(t, schemas.Typesafe, converted.Provider)
	assert.Equal(t, "jev-1.13.0", converted.Model)
}

// TestOpenAIDecisionRequestRejectsWhatOpenAIDoesNotDefine pins that the route
// takes exactly OpenAI's request: Typesafe's criteria and state, a structured
// or missing input, and any other field OpenAI's question, choice, or level
// does not define are rejected by name rather than dropped or interpreted.
func TestOpenAIDecisionRequestRejectsWhatOpenAIDoesNotDefine(t *testing.T) {
	const predicate = `{"type": "predicate", "instructions": "Q?"}`
	for name, tc := range map[string]struct {
		body    string
		wantSub string
	}{
		"criteria":     {`{"model":"m","input":"s","questions":[{"type":"predicate","instructions":"Q?","criteria":{"true":"yes"}}]}`, `question 0 has "criteria"`},
		"choice field": {`{"model":"m","input":"s","questions":[{"type":"choice","instructions":"Q?","choices":[{"value":"a","weight":2}]}]}`, `choice 0 of question 0 has "weight"`},
		"level field":  {`{"model":"m","input":"s","questions":[{"type":"score","instructions":"Q?","levels":[{"label":"a","rank":1}]}]}`, `level 0 of question 0 has "rank"`},
		"state":        {`{"model":"m","state":"s","input":"s","questions":[` + predicate + `]}`, "state is not part of OpenAI's Decisions request"},
		"state only":   {`{"model":"m","state":"s","questions":[` + predicate + `]}`, "input is required"},
		"null input":   {`{"model":"m","input":null,"questions":[` + predicate + `]}`, "input is required"},
		"object input": {`{"model":"m","input":{"ticket":"refund"},"questions":[` + predicate + `]}`, "string or a list of messages"},
		"no questions": {`{"model":"m","input":"s"}`, "questions are required"},
	} {
		t.Run(name, func(t *testing.T) {
			var request OpenAIDecisionRequest
			require.NoError(t, json.Unmarshal([]byte(tc.body), &request))
			_, err := request.ToBifrostDecisionRequest()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantSub)
		})
	}

	var structured OpenAIDecisionRequest
	assert.Error(t, json.Unmarshal([]byte(`{"model":"m","input":"s","questions":[{"type":"predicate","instructions":{"goal":"tone"}}]}`), &structured), "OpenAI's instructions are text")
}

// TestToOpenAIDecisionResponse pins the route's response: the normalized
// response with only OpenAI's differences applied. No id, even when the
// serving provider returned one, and without changing the response the caller
// holds; answers as they are; usage under OpenAI's names with Laya's fields;
// Laya's routing and Bifrost's extra_fields as they are, as on the other OpenAI
// routes; and an empty list rather than null when there are no answers.
func TestToOpenAIDecisionResponse(t *testing.T) {
	truncated := true
	normalized := &schemas.BifrostDecisionResponse{
		ID:      "dec_1",
		Model:   "jev-1.13.0",
		Answers: []schemas.DecisionAnswer{{Type: schemas.DecisionTypePredicate, Name: schemas.Ptr("q"), Probability: schemas.Ptr(0.3)}},
		Usage:   &schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12, Truncated: &truncated},
		Routing: json.RawMessage(`{"model":"english"}`),
	}
	normalized.ExtraFields.Provider = schemas.Typesafe
	normalized.ExtraFields.Latency = 120
	body, err := json.Marshal(ToOpenAIDecisionResponse(normalized))
	require.NoError(t, err)
	var wire map[string]any
	require.NoError(t, json.Unmarshal(body, &wire))
	assert.NotContains(t, wire, "id")
	assert.Equal(t, "dec_1", normalized.ID, "the caller's response keeps its id")
	assert.Equal(t, "jev-1.13.0", wire["model"])
	assert.Equal(t, []any{map[string]any{"type": "predicate", "name": "q", "probability": 0.3}}, wire["answers"])
	assert.Equal(t, map[string]any{"input_tokens": 10.0, "output_tokens": 2.0, "total_tokens": 12.0, "truncated": true}, wire["usage"])
	assert.Equal(t, map[string]any{"model": "english"}, wire["routing"])
	extra, _ := wire["extra_fields"].(map[string]any)
	assert.Equal(t, "typesafe", extra["provider"])
	assert.Equal(t, 120.0, extra["latency"])

	empty, err := json.Marshal(ToOpenAIDecisionResponse(&schemas.BifrostDecisionResponse{Model: "m"}))
	require.NoError(t, err)
	var emptyWire map[string]any
	require.NoError(t, json.Unmarshal(empty, &emptyWire))
	assert.Equal(t, []any{}, emptyWire["answers"])
	assert.Nil(t, ToOpenAIDecisionResponse(nil))
}

// TestOpenAIDecisionAnswerWire pins OpenAI's answer shape on the
// /openai/v1/decisions route: name is always present, null for an unnamed
// question (a refusal included), and written after type; a named answer is
// written as it is. The payloads are synthetic.
func TestOpenAIDecisionAnswerWire(t *testing.T) {
	for name, tc := range map[string]struct {
		answer schemas.DecisionAnswer
		want   string
	}{
		"named":   {schemas.DecisionAnswer{Type: schemas.DecisionTypePredicate, Name: schemas.Ptr("q"), Probability: schemas.Ptr(0.5)}, `{"type":"predicate","name":"q","probability":0.5}`},
		"unnamed": {schemas.DecisionAnswer{Type: schemas.DecisionTypePredicate, Probability: schemas.Ptr(0.5)}, `{"type":"predicate","name":null,"probability":0.5}`},
		"refusal": {schemas.DecisionAnswer{Type: schemas.DecisionTypeRefusal}, `{"type":"refusal","name":null}`},
	} {
		t.Run(name, func(t *testing.T) {
			body, err := json.Marshal(OpenAIDecisionAnswer{DecisionAnswer: tc.answer})
			require.NoError(t, err)
			assert.Equal(t, tc.want, string(body))
		})
	}
}

// TestOpenAIDecisionUsageKeepsTokenDetails pins that OpenAI's token details,
// cached input tokens among them, survive normalization and rendering back.
func TestOpenAIDecisionUsageKeepsTokenDetails(t *testing.T) {
	var usage OpenAIDecisionUsage
	require.NoError(t, json.Unmarshal([]byte(`{"input_tokens":120,"input_tokens_details":{"cached_tokens":30},"output_tokens":0,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":120}`), &usage))
	normalized := usage.ToBifrostLLMUsage()
	require.NotNil(t, normalized.PromptTokensDetails)
	assert.Equal(t, 30, normalized.PromptTokensDetails.CachedReadTokens)

	body, err := json.Marshal(toOpenAIDecisionUsage(normalized))
	require.NoError(t, err)
	var wire map[string]any
	require.NoError(t, json.Unmarshal(body, &wire))
	assert.Equal(t, 30.0, wire["input_tokens_details"].(map[string]any)["cached_tokens"])

	plain, err := json.Marshal(toOpenAIDecisionUsage(&schemas.BifrostLLMUsage{PromptTokens: 3, TotalTokens: 3}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"input_tokens":3,"output_tokens":0,"total_tokens":3}`, string(plain), "absent details are left out")
}

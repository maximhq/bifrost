package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// orderedDecisionRequest builds an ordered request that exercises every
// question type, an unnamed question, a boolean choice, labeled levels, and an
// inline image. The payload is synthetic.
func orderedDecisionRequest() *schemas.BifrostDecisionRequest {
	return &schemas.BifrostDecisionRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-6-luna",
		Input: &schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{{
			Role: "user",
			Content: schemas.DecisionInputContent{Parts: []schemas.DecisionInputPart{
				{Type: schemas.DecisionInputPartTypeText, Text: schemas.Ptr("Inspect the product in this photo.")},
				{Type: schemas.DecisionInputPartTypeImage, ImageURL: schemas.Ptr("data:image/png;base64,AAAA")},
			}},
		}}},
		OrderedQuestions: []schemas.DecisionOrderedQuestion{
			{Type: schemas.DecisionOrderedKindPredicate, Name: schemas.Ptr("visible_damage"), Instructions: "Is the product damaged?"},
			{
				Type:         schemas.DecisionOrderedKindChoice,
				Instructions: "Is the box intact?",
				Choices: []schemas.DecisionChoiceOption{
					{Value: schemas.DecisionScalar{Bool: schemas.Ptr(true)}, Description: schemas.Ptr("Yes.")},
					{Value: schemas.DecisionScalar{Bool: schemas.Ptr(false)}},
				},
			},
			{
				Type:         schemas.DecisionOrderedKindScore,
				Name:         schemas.Ptr("severity"),
				Instructions: "How severe is the damage?",
				Levels: []schemas.DecisionScoreLevel{
					{Label: "Cosmetic", Description: schemas.Ptr("Appearance only.")},
					{Label: "Blocked"},
				},
			},
		},
		SafetyIdentifier: schemas.Ptr("alex-1234"),
	}
}

// TestToOpenAIDecisionRequestPreservesOrderedWire pins that the wire request
// keeps question order, omits absent names, sends a boolean choice as a JSON
// boolean, keeps level labels, and forwards the image and safety identifier.
func TestToOpenAIDecisionRequestPreservesOrderedWire(t *testing.T) {
	wire, err := ToOpenAIDecisionRequest(orderedDecisionRequest())
	require.NoError(t, err)

	body, err := json.Marshal(wire)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"model": "gpt-6-luna",
		"input": [{"role": "user", "content": [
			{"type": "input_text", "text": "Inspect the product in this photo."},
			{"type": "input_image", "image_url": "data:image/png;base64,AAAA"}
		]}],
		"questions": [
			{"type": "predicate", "name": "visible_damage", "instructions": "Is the product damaged?"},
			{"type": "choice", "instructions": "Is the box intact?", "choices": [{"value": true, "description": "Yes."}, {"value": false}]},
			{"type": "score", "name": "severity", "instructions": "How severe is the damage?", "levels": [{"label": "Cosmetic", "description": "Appearance only."}, {"label": "Blocked"}]}
		],
		"safety_identifier": "alex-1234"
	}`, string(body))
}

// TestToOpenAIDecisionRequestRejectsMalformedRequests pins the local 400s:
// the wrong form, missing required fields, and duplicate names.
// Several unnamed questions are valid because a name is optional.
func TestToOpenAIDecisionRequestRejectsMalformedRequests(t *testing.T) {
	mutate := func(edit func(*schemas.BifrostDecisionRequest)) *schemas.BifrostDecisionRequest {
		request := orderedDecisionRequest()
		edit(request)
		return request
	}
	cases := map[string]*schemas.BifrostDecisionRequest{
		"nil request": nil,
		"map form only": {
			Model:     "gpt-6-luna",
			Questions: map[string]schemas.DecisionQuestion{"q": {Kind: schemas.DecisionKindNoul}},
		},
		"both forms": mutate(func(r *schemas.BifrostDecisionRequest) {
			r.Questions = map[string]schemas.DecisionQuestion{"q": {Kind: schemas.DecisionKindNoul}}
		}),
		"missing input": mutate(func(r *schemas.BifrostDecisionRequest) { r.Input = nil }),
		"empty input": mutate(func(r *schemas.BifrostDecisionRequest) {
			r.Input = &schemas.DecisionInput{}
		}),
		"input with both text and messages": mutate(func(r *schemas.BifrostDecisionRequest) {
			r.Input = &schemas.DecisionInput{Text: schemas.Ptr("a"), Messages: []schemas.DecisionInputMessage{{Role: "user"}}}
		}),
		"choice with both string and boolean": mutate(func(r *schemas.BifrostDecisionRequest) {
			r.OrderedQuestions[1].Choices[0].Value = schemas.DecisionScalar{Str: schemas.Ptr("a"), Bool: schemas.Ptr(true)}
		}),
		"no questions": mutate(func(r *schemas.BifrostDecisionRequest) { r.OrderedQuestions = nil }),
		"unsupported type": mutate(func(r *schemas.BifrostDecisionRequest) {
			r.OrderedQuestions[0].Type = "ranking"
		}),
		"refusal is not a question type": mutate(func(r *schemas.BifrostDecisionRequest) {
			r.OrderedQuestions[0].Type = schemas.DecisionOrderedKindRefusal
		}),
		"duplicate names": mutate(func(r *schemas.BifrostDecisionRequest) {
			r.OrderedQuestions[2].Name = r.OrderedQuestions[0].Name
		}),
		"choice without choices": mutate(func(r *schemas.BifrostDecisionRequest) {
			r.OrderedQuestions[1].Choices = nil
		}),
		"choice with a numeric value": mutate(func(r *schemas.BifrostDecisionRequest) {
			r.OrderedQuestions[1].Choices[0].Value = schemas.DecisionScalar{Num: schemas.Ptr(1.0)}
		}),
		"score without levels": mutate(func(r *schemas.BifrostDecisionRequest) {
			r.OrderedQuestions[2].Levels = nil
		}),
	}
	for name, request := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ToOpenAIDecisionRequest(request)
			assert.Error(t, err)
		})
	}

	t.Run("several unnamed questions are valid", func(t *testing.T) {
		request := orderedDecisionRequest()
		request.OrderedQuestions = []schemas.DecisionOrderedQuestion{
			{Type: schemas.DecisionOrderedKindPredicate, Instructions: "first?"},
			{Type: schemas.DecisionOrderedKindPredicate, Instructions: "second?"},
		}
		_, err := ToOpenAIDecisionRequest(request)
		assert.NoError(t, err)
	})
}

// TestDecisionRejectsAmbiguousInputWith400 pins that Go values the wire format
// cannot express reach the caller as a 400, not an internal marshal error.
func TestDecisionRejectsAmbiguousInputWith400(t *testing.T) {
	provider := NewOpenAIProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: "http://127.0.0.1:1"}}, testNoopLogger{})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: *schemas.NewSecretVar("test-key")}

	cases := map[string]func(*schemas.BifrostDecisionRequest){
		"text and messages": func(r *schemas.BifrostDecisionRequest) {
			r.Input = &schemas.DecisionInput{Text: schemas.Ptr("a"), Messages: []schemas.DecisionInputMessage{{Role: "user"}}}
		},
		"string and boolean choice": func(r *schemas.BifrostDecisionRequest) {
			r.OrderedQuestions[1].Choices[0].Value = schemas.DecisionScalar{Str: schemas.Ptr("a"), Bool: schemas.Ptr(true)}
		},
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			request := orderedDecisionRequest()
			edit(request)
			_, bifrostErr := provider.Decision(ctx, key, request)
			require.NotNil(t, bifrostErr)
			require.NotNil(t, bifrostErr.StatusCode)
			assert.Equal(t, http.StatusBadRequest, *bifrostErr.StatusCode)
		})
	}
}

// openAIDecisionResponseBody is a synthetic upstream reply: ordered answers
// with an unnamed answer, a refusal, an unfamiliar future answer type, and a
// field outside the modelled shape.
const openAIDecisionResponseBody = `{
	"id": "dec_1",
	"model": "gpt-6-luna",
	"answers": [
		{"type": "predicate", "name": "visible_damage", "probability": 0.92},
		{"type": "choice", "choice": true, "probabilities": [{"value": true, "probability": 0.8}, {"value": false, "probability": 0.2}], "confidence": 0.8},
		{"type": "refusal", "name": "severity"},
		{"type": "ranking", "name": "future", "order": ["a"]}
	],
	"usage": {"input_tokens": 120, "output_tokens": 0, "total_tokens": 120},
	"future_field": {"kept": true}
}`

// TestDecisionNativeRequestAndResponse runs the provider end to end against a
// stub upstream: the request goes to /v1/decisions with bearer auth and the
// ordered body, and the reply keeps answer order, the refusal, the unfamiliar
// answer, usage, and the verbatim native body.
func TestDecisionNativeRequestAndResponse(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1/decisions", r.URL.Path)
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(openAIDecisionResponseBody))
	}))
	defer server.Close()

	provider := NewOpenAIProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL}}, testNoopLogger{})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	response, bifrostErr := provider.Decision(ctx, schemas.Key{Value: *schemas.NewSecretVar("test-key")}, orderedDecisionRequest())
	require.Nil(t, bifrostErr)
	require.NotNil(t, response)

	var sent struct {
		Questions []map[string]any `json:"questions"`
	}
	require.NoError(t, json.Unmarshal(gotBody, &sent))
	require.Len(t, sent.Questions, 3)
	assert.Equal(t, "predicate", sent.Questions[0]["type"])
	assert.Equal(t, "score", sent.Questions[2]["type"])

	assert.Equal(t, "dec_1", response.ID)
	assert.Equal(t, "gpt-6-luna", response.Model)
	assert.True(t, response.UsesOrderedForm())
	assert.Empty(t, response.Answers, "an ordered request must not populate the map form")
	require.Len(t, response.OrderedAnswers, 4)
	assert.Equal(t, schemas.DecisionOrderedKindPredicate, response.OrderedAnswers[0].Type)
	assert.Nil(t, response.OrderedAnswers[1].Name)
	require.NotNil(t, response.OrderedAnswers[1].Choice.Bool)
	assert.Equal(t, schemas.DecisionOrderedKindRefusal, response.OrderedAnswers[2].Type)
	assert.False(t, response.OrderedAnswers[3].IsRecognized())

	require.NotNil(t, response.Usage)
	assert.Equal(t, 120, response.Usage.PromptTokens)
	assert.Equal(t, 120, response.Usage.TotalTokens)

	assert.JSONEq(t, openAIDecisionResponseBody, string(response.NativeResponse), "the native body must be relayed verbatim")
}

// TestDecisionNativeUpstreamError pins that an upstream error is surfaced with
// its status and message rather than swallowed.
func TestDecisionNativeUpstreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"Decision API is not enabled for this user.","type":"invalid_request_error"}}`))
	}))
	defer server.Close()

	provider := NewOpenAIProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL}}, testNoopLogger{})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	response, bifrostErr := provider.Decision(ctx, schemas.Key{Value: *schemas.NewSecretVar("test-key")}, orderedDecisionRequest())
	assert.Nil(t, response)
	require.NotNil(t, bifrostErr)
	require.NotNil(t, bifrostErr.StatusCode)
	assert.Equal(t, http.StatusForbidden, *bifrostErr.StatusCode)
	require.NotNil(t, bifrostErr.Error)
	assert.Contains(t, bifrostErr.Error.Message, "not enabled")
}

// TestDecisionLeavesEveryOtherRequestUnsupported pins that only a decisions
// model goes upstream. A request for a chat model, in either form, is reported
// unsupported without any upstream call, so core emulates a map-shaped one and
// rejects an ordered one.
func TestDecisionLeavesEveryOtherRequestUnsupported(t *testing.T) {
	var upstreamCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	provider := NewOpenAIProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL}}, testNoopLogger{})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: *schemas.NewSecretVar("test-key")}

	chatModel := orderedDecisionRequest()
	chatModel.Model = "gpt-4o"
	mapShaped := &schemas.BifrostDecisionRequest{
		Model:     "gpt-4o",
		State:     "hello",
		Questions: map[string]schemas.DecisionQuestion{"q": {Kind: schemas.DecisionKindNoul}},
	}

	for name, request := range map[string]*schemas.BifrostDecisionRequest{"ordered request for a chat model": chatModel, "map-shaped request for a chat model": mapShaped} {
		t.Run(name, func(t *testing.T) {
			response, bifrostErr := provider.Decision(ctx, key, request)
			assert.Nil(t, response)
			require.NotNil(t, bifrostErr)
			require.NotNil(t, bifrostErr.Error)
			require.NotNil(t, bifrostErr.Error.Code)
			assert.Equal(t, "unsupported_operation", *bifrostErr.Error.Code)
		})
	}
	assert.Zero(t, upstreamCalls)
}

// TestDecisionHonorsAllowedRequests pins that a custom provider that does not
// allow decisions is refused rather than sent upstream.
func TestDecisionHonorsAllowedRequests(t *testing.T) {
	var upstreamCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
	}))
	defer server.Close()

	provider := NewOpenAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL},
		CustomProviderConfig: &schemas.CustomProviderConfig{
			CustomProviderKey: "hawk",
			BaseProviderType:  schemas.OpenAI,
			AllowedRequests:   &schemas.AllowedRequests{ChatCompletion: true},
		},
	}, testNoopLogger{})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	response, bifrostErr := provider.Decision(ctx, schemas.Key{Value: *schemas.NewSecretVar("test-key")}, orderedDecisionRequest())
	assert.Nil(t, response)
	require.NotNil(t, bifrostErr)
	assert.Zero(t, upstreamCalls)
}

// TestDecisionsOnlyCustomProviderRejectsChatAndResponses pins that a custom
// OpenAI-backed provider cannot turn a Decisions-only route into chat or
// Responses emulation.
func TestDecisionsOnlyCustomProviderRejectsChatAndResponses(t *testing.T) {
	provider := NewOpenAIProvider(&schemas.ProviderConfig{
		CustomProviderConfig: &schemas.CustomProviderConfig{
			CustomProviderKey: "openai-decisions",
			BaseProviderType:  schemas.OpenAI,
			AllowedRequests:   &schemas.AllowedRequests{Decision: true},
		},
	}, testNoopLogger{})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: *schemas.NewSecretVar("test-key")}

	_, chatErr := provider.ChatCompletion(ctx, key, &schemas.BifrostChatRequest{Model: "gpt-4o"})
	require.NotNil(t, chatErr)
	require.NotNil(t, chatErr.Error)
	require.NotNil(t, chatErr.Error.Code)
	assert.Equal(t, "unsupported_operation", *chatErr.Error.Code)
	assert.Equal(t, schemas.ModelProvider("openai-decisions"), chatErr.ExtraFields.Provider)

	_, responsesErr := provider.Responses(ctx, key, &schemas.BifrostResponsesRequest{Model: "gpt-4o"})
	require.NotNil(t, responsesErr)
	require.NotNil(t, responsesErr.Error)
	require.NotNil(t, responsesErr.Error.Code)
	assert.Equal(t, "unsupported_operation", *responsesErr.Error.Code)
	assert.Equal(t, schemas.ModelProvider("openai-decisions"), responsesErr.ExtraFields.Provider)
}

// TestOpenAIDecisionRequestUnmarshalCapturesExtensionsAndFallbacks pins that
// decoding the route body keeps the modelled fields typed, keeps fallbacks for
// gateway routing, and captures an unknown future field verbatim instead of
// rejecting or dropping it. The payload is synthetic.
func TestOpenAIDecisionRequestUnmarshalCapturesExtensionsAndFallbacks(t *testing.T) {
	body := []byte(`{
		"model": "gpt-6-luna",
		"input": "I was charged twice for my order.",
		"questions": [{"type": "predicate", "name": "is_frustrated", "instructions": "Is the customer frustrated?"}],
		"safety_identifier": "alex-1234",
		"fallbacks": ["openai/gpt-6-luna-2026-09-01"],
		"future_option": {"mode": "strict", "limit": 3}
	}`)

	var request OpenAIDecisionRequest
	require.NoError(t, json.Unmarshal(body, &request))

	assert.Equal(t, "gpt-6-luna", request.Model)
	require.NotNil(t, request.Input)
	require.NotNil(t, request.Input.Text)
	require.Len(t, request.Questions, 1)
	assert.Equal(t, []string{"openai/gpt-6-luna-2026-09-01"}, request.Fallbacks)
	require.Len(t, request.ExtraParams, 1, "only unmodelled fields belong in ExtraParams")
	assert.JSONEq(t, `{"mode":"strict","limit":3}`, string(request.ExtraParams["future_option"].(json.RawMessage)))
}

// TestOpenAIDecisionRequestToBifrostDecisionRequest pins that the route's
// request becomes an ordered request, that a bare model defaults to OpenAI, and
// that a provider prefix is honored.
func TestOpenAIDecisionRequestToBifrostDecisionRequest(t *testing.T) {
	ordered := orderedDecisionRequest()
	request := &OpenAIDecisionRequest{
		Model:            "gpt-6-luna",
		Input:            ordered.Input,
		Questions:        ordered.OrderedQuestions,
		SafetyIdentifier: ordered.SafetyIdentifier,
		Fallbacks:        []string{"openai/gpt-6-luna-2026-09-01"},
		ExtraParams:      map[string]interface{}{"future_option": json.RawMessage(`true`)},
	}

	converted := request.ToBifrostDecisionRequest(nil)
	assert.Equal(t, schemas.OpenAI, converted.Provider)
	assert.Equal(t, "gpt-6-luna", converted.Model)
	assert.True(t, converted.UsesOrderedForm())
	assert.False(t, converted.UsesMapForm())
	assert.Equal(t, ordered.OrderedQuestions, converted.OrderedQuestions)
	assert.Equal(t, ordered.SafetyIdentifier, converted.SafetyIdentifier)
	assert.Equal(t, []schemas.Fallback{{Provider: schemas.OpenAI, Model: "gpt-6-luna-2026-09-01"}}, converted.Fallbacks)
	assert.Equal(t, request.ExtraParams, converted.ExtraParams)

	request.Model = "azure/gpt-6-luna"
	converted = request.ToBifrostDecisionRequest(nil)
	assert.Equal(t, schemas.Azure, converted.Provider)
	assert.Equal(t, "gpt-6-luna", converted.Model)
}

// TestToOpenAIDecisionResponseBuildsWireShape pins the typed builder used when
// no native body is available: answer order, the refusal, and an unfamiliar
// answer type survive, and usage is emitted in the endpoint's token fields.
func TestToOpenAIDecisionResponseBuildsWireShape(t *testing.T) {
	var native OpenAIDecisionResponse
	require.NoError(t, json.Unmarshal([]byte(openAIDecisionResponseBody), &native))
	bifrostResponse := native.ToBifrostDecisionResponse()

	body, err := json.Marshal(ToOpenAIDecisionResponse(bifrostResponse))
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"id": "dec_1",
		"model": "gpt-6-luna",
		"answers": [
			{"type": "predicate", "name": "visible_damage", "probability": 0.92},
			{"type": "choice", "choice": true, "probabilities": [{"value": true, "probability": 0.8}, {"value": false, "probability": 0.2}], "confidence": 0.8},
			{"type": "refusal", "name": "severity"},
			{"type": "ranking", "name": "future", "order": ["a"]}
		],
		"usage": {"input_tokens": 120, "input_tokens_details": null, "output_tokens": 0, "output_tokens_details": null, "total_tokens": 120}
	}`, string(body))

	empty, err := json.Marshal(ToOpenAIDecisionResponse(&schemas.BifrostDecisionResponse{Model: "gpt-6-luna"}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"model": "gpt-6-luna", "answers": []}`, string(empty), "answers must be an array, never null")
	assert.Nil(t, ToOpenAIDecisionResponse(nil))
}

// TestDecisionNativeForwardsExtensionsButNotFallbacks pins the upstream body
// for a route request: an unknown field captured from the client reaches
// OpenAI under the passthrough flag the route sets, and the gateway-only
// fallbacks never do.
func TestDecisionNativeForwardsExtensionsButNotFallbacks(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(openAIDecisionResponseBody))
	}))
	defer server.Close()

	var routeRequest OpenAIDecisionRequest
	require.NoError(t, json.Unmarshal([]byte(`{
		"model": "gpt-6-luna",
		"input": "I was charged twice for my order.",
		"questions": [{"type": "predicate", "name": "is_frustrated", "instructions": "Is the customer frustrated?"}],
		"fallbacks": ["openai/gpt-6-luna-2026-09-01"],
		"future_option": {"mode": "strict"}
	}`), &routeRequest))

	provider := NewOpenAIProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL}}, testNoopLogger{})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)

	_, bifrostErr := provider.Decision(ctx, schemas.Key{Value: *schemas.NewSecretVar("test-key")}, routeRequest.ToBifrostDecisionRequest(ctx))
	require.Nil(t, bifrostErr)

	var sent map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(gotBody, &sent))
	assert.JSONEq(t, `{"mode":"strict"}`, string(sent["future_option"]))
	assert.NotContains(t, sent, "fallbacks")
	assert.Contains(t, sent, "questions")
}

// mapDecisionRequest builds a map-shaped request for a decisions model with
// one question of each kind, including noul criteria, structured
// instructions, and a structured choice description. The payload is synthetic.
func mapDecisionRequest() *schemas.BifrostDecisionRequest {
	return &schemas.BifrostDecisionRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-6-luna",
		State:    "I was double charged. Refund me today or I cancel.",
		Questions: map[string]schemas.DecisionQuestion{
			"urgency": {Kind: schemas.DecisionKindScore, Instructions: "How urgent?", Criteria: []interface{}{"can wait", "soon", "today"}},
			"is_frustrated": {
				Kind:         schemas.DecisionKindNoul,
				Instructions: "Is the customer frustrated?",
				Criteria:     map[string]interface{}{"true": "clearly upset", "false": "calm"},
			},
			"category": {
				Kind:         schemas.DecisionKindChoice,
				Instructions: map[string]interface{}{"question": "Ticket category", "rule": "pick one"},
				Criteria:     map[string]interface{}{"bug": "defects", "billing": map[string]interface{}{"definition": "charges and refunds"}},
			},
		},
	}
}

// mapDecisionUpstreamBody is a synthetic decisions reply to the converted
// mapDecisionRequest, answering in the converted question order.
const mapDecisionUpstreamBody = `{
	"model": "gpt-6-luna",
	"answers": [
		{"type": "choice", "name": "category", "choice": "billing", "probabilities": [{"value": "billing", "probability": 0.98}, {"value": "bug", "probability": 0.02}], "confidence": 0.98},
		{"type": "predicate", "name": "is_frustrated", "probability": 0.97},
		{"type": "score", "name": "urgency", "score": 1.95, "probabilities": [{"value": 0, "label": "0", "probability": 0.01}, {"value": 1, "label": "1", "probability": 0.03}, {"value": 2, "label": "2", "probability": 0.96}], "confidence": 0.96}
	],
	"usage": {"input_tokens": 180, "output_tokens": 0, "total_tokens": 180}
}`

// TestDecisionMapFormServedNatively pins that a map-shaped request for a
// decisions model is converted and sent to the decisions endpoint, not
// emulated: questions sorted by name, noul criteria folded into a predicate's
// instructions, structured text rendered as sorted JSON, and score levels
// labelled by index. The ordered answers come back as the map answers, with
// no ordered answers or native body left on the response.
func TestDecisionMapFormServedNatively(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(mapDecisionUpstreamBody))
	}))
	defer server.Close()

	provider := NewOpenAIProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL}}, testNoopLogger{})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	response, bifrostErr := provider.Decision(ctx, schemas.Key{Value: *schemas.NewSecretVar("test-key")}, mapDecisionRequest())
	require.Nil(t, bifrostErr)
	assert.Equal(t, "/v1/decisions", gotPath)
	assert.JSONEq(t, `{
		"model": "gpt-6-luna",
		"input": "I was double charged. Refund me today or I cancel.",
		"questions": [
			{"type": "choice", "name": "category", "instructions": "{\"question\":\"Ticket category\",\"rule\":\"pick one\"}",
			 "choices": [{"value": "billing", "description": "{\"definition\":\"charges and refunds\"}"}, {"value": "bug", "description": "defects"}]},
			{"type": "predicate", "name": "is_frustrated", "instructions": "Is the customer frustrated? Meaning (answer: meaning): true=clearly upset, false=calm"},
			{"type": "score", "name": "urgency", "instructions": "How urgent?",
			 "levels": [{"label": "0", "description": "can wait"}, {"label": "1", "description": "soon"}, {"label": "2", "description": "today"}]}
		]
	}`, string(gotBody))

	require.NotNil(t, response)
	assert.Empty(t, response.OrderedAnswers, "a map-shaped request must not return ordered answers")
	assert.Empty(t, response.NativeResponse, "the native body answers the ordered form, not the caller's map form")
	require.Len(t, response.Answers, 3)
	assert.Equal(t, schemas.DecisionAnswer{Kind: schemas.DecisionKindNoul, Value: 0.97}, response.Answers["is_frustrated"])
	assert.Equal(t, schemas.DecisionAnswer{
		Kind:          schemas.DecisionKindChoice,
		Value:         "billing",
		Confidence:    schemas.Ptr(0.98),
		Probabilities: map[string]float64{"billing": 0.98, "bug": 0.02},
	}, response.Answers["category"])
	assert.Equal(t, schemas.DecisionAnswer{
		Kind:          schemas.DecisionKindScore,
		Value:         1.95,
		Confidence:    schemas.Ptr(0.96),
		Probabilities: map[string]float64{"0": 0.01, "1": 0.03, "2": 0.96},
		Legend:        map[string]any{"0": "can wait", "1": "soon", "2": "today"},
	}, response.Answers["urgency"])
	require.NotNil(t, response.Usage)
	assert.Equal(t, 180, response.Usage.PromptTokens)
}

// TestDecisionMapFormIgnoresRawBodyAndRefusesExtensions pins the two inputs a
// map-shaped request must never forward as is: its raw body, which is the map
// form, is replaced by the converted request; and native extensions asked to
// pass through are refused with a 400 before any upstream call.
func TestDecisionMapFormIgnoresRawBodyAndRefusesExtensions(t *testing.T) {
	var upstreamCalls int
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(mapDecisionUpstreamBody))
	}))
	defer server.Close()

	provider := NewOpenAIProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL}}, testNoopLogger{})
	key := schemas.Key{Value: *schemas.NewSecretVar("test-key")}

	t.Run("raw body is not forwarded", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
		request := mapDecisionRequest()
		request.RawRequestBody = []byte(`{"model":"gpt-6-luna","state":"x","questions":{"q":{"kind":"noul"}}}`)

		_, bifrostErr := provider.Decision(ctx, key, request)
		require.Nil(t, bifrostErr)
		assert.NotContains(t, string(gotBody), `"state"`)
		assert.Contains(t, string(gotBody), `"input"`)
	})

	t.Run("extensions are refused", func(t *testing.T) {
		upstreamCalls = 0
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
		request := mapDecisionRequest()
		request.ExtraParams = map[string]interface{}{"images": json.RawMessage(`["data:image/png;base64,AAAA"]`)}

		response, bifrostErr := provider.Decision(ctx, key, request)
		assert.Nil(t, response)
		require.NotNil(t, bifrostErr)
		require.NotNil(t, bifrostErr.StatusCode)
		assert.Equal(t, http.StatusBadRequest, *bifrostErr.StatusCode)
		assert.Contains(t, bifrostErr.Error.Message, "images")
		assert.Zero(t, upstreamCalls)
	})
}

// TestDecisionMapFormRefusalFails pins that a refused question fails a
// map-shaped request instead of returning a map with that answer missing,
// since every requested map question must be answered.
func TestDecisionMapFormRefusalFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-6-luna","answers":[
			{"type":"choice","name":"category","choice":"billing","probabilities":[{"value":"billing","probability":1}],"confidence":1},
			{"type":"refusal","name":"is_frustrated"},
			{"type":"score","name":"urgency","score":2,"probabilities":[{"value":2,"label":"2","probability":1}],"confidence":1}
		],"usage":{"input_tokens":120,"output_tokens":7,"total_tokens":127}}`))
	}))
	defer server.Close()

	provider := NewOpenAIProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL}}, testNoopLogger{})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	response, bifrostErr := provider.Decision(ctx, schemas.Key{Value: *schemas.NewSecretVar("test-key")}, mapDecisionRequest())
	assert.Nil(t, response)
	require.NotNil(t, bifrostErr)
	assert.Contains(t, bifrostErr.Error.Message, "declined to answer question \"is_frustrated\"")
	require.NotNil(t, bifrostErr.ExtraFields.BilledUsage, "a paid reply that fails conversion must still be billed")
	assert.Equal(t, 120, bifrostErr.ExtraFields.BilledUsage.PromptTokens)
	assert.Equal(t, 127, bifrostErr.ExtraFields.BilledUsage.TotalTokens)
}

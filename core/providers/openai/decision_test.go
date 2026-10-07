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

// TestDecisionLeavesEveryOtherRequestUnsupported pins that only the ordered
// form of a decisions model goes upstream. A map-shaped request, or an ordered
// request for a chat model, is reported unsupported without any upstream call,
// so core keeps emulating the former and rejects the latter.
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
		Model:     "gpt-6-luna",
		State:     "hello",
		Questions: map[string]schemas.DecisionQuestion{"q": {Kind: schemas.DecisionKindNoul}},
	}

	for name, request := range map[string]*schemas.BifrostDecisionRequest{"ordered request for a chat model": chatModel, "map-shaped request": mapShaped} {
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

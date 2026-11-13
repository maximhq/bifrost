package sarvam_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/sarvam"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// sarvamDecisionResponseBody is a Sarvam /v2/systemone reply for sarvam-prism.
const sarvamDecisionResponseBody = `{"model":"sarvam-prism","answers":{"is_frustrated":{"type":"noul","noul":0.9665},"category":{"type":"choice","choice":"billing","confidence":0.8,"probabilities":{"billing":0.8,"bug":0.15,"other":0.05}},"urgency":{"type":"score","score":2.1,"confidence":0.7,"probabilities":{"0":0.1,"1":0.2,"2":0.7},"legend":{"0":"can wait","1":"soon","2":"today"}}},"usage":{"input_tokens":47,"output_tokens":0}}`

func newSarvamDecisionRequest(input schemas.DecisionInput) *schemas.BifrostDecisionRequest {
	return &schemas.BifrostDecisionRequest{
		Provider: schemas.Sarvam,
		Model:    "sarvam-prism",
		Input:    input,
		Questions: []schemas.DecisionQuestion{
			{Type: schemas.DecisionTypePredicate, Name: schemas.Ptr("is_frustrated"), Instructions: schemas.NewDecisionText("Is the customer frustrated?")},
		},
	}
}

func sarvamUserMessage(parts ...schemas.DecisionInputPart) schemas.DecisionInputMessage {
	return schemas.DecisionInputMessage{Role: "user", Content: schemas.DecisionInputContent{Parts: parts}}
}

func sarvamTextPart(text string) schemas.DecisionInputPart {
	return schemas.DecisionInputPart{Type: schemas.DecisionInputPartTypeText, Text: schemas.NewDecisionText(text)}
}

// An input Sarvam can read sends the state and questions Sarvam validates: the
// input as state, each question keyed by name with its native type, and no
// assets map.
func TestToSarvamDecisionRequest_WireBody(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		input     schemas.DecisionInput
		wantState string
	}{
		"text":       {input: schemas.DecisionInput{Text: schemas.Ptr("The sky is blue.")}, wantState: `"The sky is blue."`},
		"structured": {input: schemas.DecisionInput{Structured: map[string]any{"ticket": "A-4021"}}, wantState: `{"ticket":"A-4021"}`},
		"messages": {
			input:     schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{sarvamUserMessage(sarvamTextPart("hi"))}},
			wantState: `[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			native, err := sarvam.ToSarvamDecisionRequest(newSarvamDecisionRequest(tc.input))
			require.NoError(t, err)
			body, err := providerUtils.MarshalProviderRequest(native)
			require.NoError(t, err)

			var wire map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(body, &wire))
			require.JSONEq(t, tc.wantState, string(wire["state"]), "state")
			require.JSONEq(t, `{"is_frustrated":{"type":"noul","instructions":"Is the customer frustrated?"}}`, string(wire["questions"]), "questions")
			require.NotContains(t, wire, "assets", "Sarvam reads no assets")
		})
	}
}

// Sarvam reads text only and has no box questions, so an image sent by
// reference, an empty input, and a box question are each a local 400.
func TestToSarvamDecisionRequest_Rejections(t *testing.T) {
	t.Parallel()
	boxRequest := newSarvamDecisionRequest(schemas.DecisionInput{Text: schemas.Ptr("hi")})
	boxRequest.Questions = []schemas.DecisionQuestion{
		{Type: schemas.DecisionTypeBox, Name: schemas.Ptr("box"), Instructions: schemas.NewDecisionText("Where?")},
	}
	for name, request := range map[string]*schemas.BifrostDecisionRequest{
		"asset image": newSarvamDecisionRequest(schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{
			sarvamUserMessage(sarvamTextPart("see r1"), schemas.DecisionInputPart{Type: schemas.DecisionInputPartTypeImage, ID: schemas.Ptr("r1"), Data: schemas.DecisionData{File: []byte{0x89, 0x50}}}),
		}}),
		"empty input": newSarvamDecisionRequest(schemas.DecisionInput{}),
		"empty text":  newSarvamDecisionRequest(schemas.DecisionInput{Text: schemas.Ptr("   ")}),
		"empty messages": newSarvamDecisionRequest(schemas.DecisionInput{
			Messages: []schemas.DecisionInputMessage{},
		}),
		"box question": boxRequest,
	} {
		_, err := sarvam.ToSarvamDecisionRequest(request)
		require.Error(t, err, name)
		_, isBadRequest := providerUtils.AsBifrostBadRequestError(err)
		require.True(t, isBadRequest, "%s: want a 400, got %v", name, err)
	}
}

func newSarvamDecisionProvider(t *testing.T, baseURL string) *sarvam.SarvamProvider {
	t.Helper()
	provider, err := sarvam.NewSarvamProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: baseURL, DefaultRequestTimeoutInSeconds: 30},
	}, bifrost.NewNoOpLogger())
	require.NoError(t, err)
	return provider
}

// A decision posts to /v2/systemone with Bearer auth and exactly the System One
// body, and every answer type maps back in question order.
func TestSarvamDecision_PostsToSystemOne(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var gotPath, gotAuth string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotPath, gotAuth, gotBody = r.URL.Path, r.Header.Get("Authorization"), body
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, sarvamDecisionResponseBody)
	}))
	defer server.Close()

	provider := newSarvamDecisionProvider(t, server.URL)
	key := schemas.Key{Value: *schemas.NewSecretVar("sarvam-token")}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	request := newSarvamDecisionRequest(schemas.DecisionInput{Text: schemas.Ptr("Customer: I was double charged.")})
	request.Questions = []schemas.DecisionQuestion{
		{Type: schemas.DecisionTypePredicate, Name: schemas.Ptr("is_frustrated"), Instructions: schemas.NewDecisionText("Is the customer frustrated?")},
		{Type: schemas.DecisionTypeChoice, Name: schemas.Ptr("category"), Instructions: schemas.NewDecisionText("Pick"), Choices: []schemas.DecisionChoice{
			{Value: schemas.DecisionScalar{Str: schemas.Ptr("billing")}, Description: schemas.NewDecisionText("charges")},
			{Value: schemas.DecisionScalar{Str: schemas.Ptr("bug")}, Description: schemas.NewDecisionText("defects")},
			{Value: schemas.DecisionScalar{Str: schemas.Ptr("other")}, Description: schemas.NewDecisionText("anything else")},
		}},
		{Type: schemas.DecisionTypeScore, Name: schemas.Ptr("urgency"), Instructions: schemas.NewDecisionText("Rate"), Levels: []schemas.DecisionLevel{
			{Label: "0", Description: schemas.NewDecisionText("can wait")},
			{Label: "1", Description: schemas.NewDecisionText("soon")},
			{Label: "2", Description: schemas.NewDecisionText("today")},
		}},
	}

	resp, bifrostErr := provider.Decision(ctx, key, request)
	require.Nil(t, bifrostErr)

	mu.Lock()
	path, auth, body := gotPath, gotAuth, gotBody
	mu.Unlock()
	require.Equal(t, "/v2/systemone", path)
	require.Equal(t, "Bearer sarvam-token", auth)

	var top map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &top))
	require.ElementsMatch(t, []string{"state", "model", "questions"}, sarvamKeysOf(top))
	require.Equal(t, `"sarvam-prism"`, string(top["model"]))

	var questions map[string]map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(top["questions"], &questions))
	require.Len(t, questions, 3)
	require.Equal(t, `"noul"`, string(questions["is_frustrated"]["type"]))
	require.Equal(t, `"choice"`, string(questions["category"]["type"]))
	require.Equal(t, `"score"`, string(questions["urgency"]["type"]))

	require.Equal(t, "sarvam-prism", resp.Model)
	require.Len(t, resp.Answers, 3)
	require.NotNil(t, resp.Answers[0].Probability)
	require.Equal(t, 0.9665, *resp.Answers[0].Probability)
	require.NotNil(t, resp.Answers[1].Choice)
	require.Equal(t, "billing", *resp.Answers[1].Choice.Str)
	require.NotNil(t, resp.Answers[2].Score)
	require.Equal(t, 2.1, *resp.Answers[2].Score)
	require.NotNil(t, resp.Usage)
	require.Equal(t, 47, resp.Usage.PromptTokens)
	require.Equal(t, 0, resp.Usage.CompletionTokens)
}

func sarvamKeysOf(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}

// Sarvam's own errors reach the caller with their status, message, code, and
// param intact.
func TestSarvamDecision_PassesErrorsThrough(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = fmt.Fprint(w, `{"error":{"message":"questions.is_frustrated.noul.instruction: Extra inputs are not permitted","code":"unprocessable_entity_error","request_id":"req-1","param":"questions.is_frustrated.noul.instruction"}}`)
	}))
	defer server.Close()

	provider := newSarvamDecisionProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bifrostErr := provider.Decision(ctx, schemas.Key{}, newSarvamDecisionRequest(schemas.DecisionInput{Text: schemas.Ptr("hi")}))
	require.NotNil(t, bifrostErr)
	require.Equal(t, schemas.Ptr(http.StatusUnprocessableEntity), bifrostErr.StatusCode)
	require.NotNil(t, bifrostErr.Error)
	require.Contains(t, bifrostErr.Error.Message, "Extra inputs are not permitted")
	require.NotNil(t, bifrostErr.Error.Code)
	require.Equal(t, "unprocessable_entity_error", *bifrostErr.Error.Code)
	require.Equal(t, "questions.is_frustrated.noul.instruction", bifrostErr.Error.Param)
	require.False(t, bifrostErr.IsBifrostError)
}

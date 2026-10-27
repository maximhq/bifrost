package vllm_test

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
	"github.com/maximhq/bifrost/core/providers/typesafe"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/providers/vllm"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// vllmDecisionResponseBody is a vLLM /v1/systemone reply: Typesafe's answers plus
// vLLM's id, object, created and diagnostics, and a noul answer with probabilities.
const vllmDecisionResponseBody = `{"answers":{"is_red":{"type":"noul","noul":0.96,"confidence":0.13,"probabilities":{"yes":0.96,"no":0.04}}},"created":1792773649,"diagnostics":{"is_red":{"argmax_is_label":false,"label_mass":0.13}},"id":"decision-8abc2a9a80e3d7e2","model":"clef-flash","object":"structured_decision","usage":{"input_tokens":35,"output_tokens":1}}`

func newVLLMDecisionRequest(input schemas.DecisionInput) *schemas.BifrostDecisionRequest {
	return &schemas.BifrostDecisionRequest{
		Provider: schemas.VLLM,
		Model:    "clef-flash",
		Input:    input,
		Questions: []schemas.DecisionQuestion{
			{Type: schemas.DecisionTypePredicate, Name: schemas.Ptr("is_red"), Instructions: schemas.NewDecisionText("Is the image mostly red?")},
		},
	}
}

func vllmUserMessage(parts ...schemas.DecisionInputPart) schemas.DecisionInputMessage {
	return schemas.DecisionInputMessage{Role: "user", Content: schemas.DecisionInputContent{Parts: parts}}
}

func vllmTextPart(text string) schemas.DecisionInputPart {
	return schemas.DecisionInputPart{Type: schemas.DecisionInputPartTypeText, Text: schemas.NewDecisionText(text)}
}

func vllmImagePart(url string) schemas.DecisionInputPart {
	return schemas.DecisionInputPart{Type: schemas.DecisionInputPartTypeImage, ImageURL: schemas.Ptr(url)}
}

func encodeVLLMDecision(t *testing.T, request *schemas.BifrostDecisionRequest) map[string]json.RawMessage {
	t.Helper()
	native, err := vllm.ToVLLMDecisionRequest(request)
	require.NoError(t, err)
	data, err := providerUtils.MarshalProviderRequest(native)
	require.NoError(t, err)
	var top map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &top))
	return top
}

// An input without images sends exactly Typesafe's body, so questions and state
// validate as they do there, whatever shape the input takes.
func TestToVLLMDecisionRequest_MatchesTypesafeWithoutImages(t *testing.T) {
	t.Parallel()
	for name, input := range map[string]schemas.DecisionInput{
		"text":       {Text: schemas.Ptr("The sky is blue.")},
		"structured": {Structured: map[string]any{"ticket": "A-4021"}},
		"messages":   {Messages: []schemas.DecisionInputMessage{vllmUserMessage(vllmTextPart("hi"))}},
	} {
		request := newVLLMDecisionRequest(input)
		native, err := vllm.ToVLLMDecisionRequest(request)
		require.NoError(t, err, name)
		got, err := providerUtils.MarshalProviderRequest(native)
		require.NoError(t, err, name)
		typesafeNative, err := typesafe.ToTypesafeDecisionRequest(request)
		require.NoError(t, err, name)
		want, err := providerUtils.MarshalProviderRequest(typesafeNative)
		require.NoError(t, err, name)
		require.JSONEq(t, string(want), string(got), name)
	}
}

// Image parts move, in order, into the top-level images array with their URLs
// as given; the text stays in the state.
func TestToVLLMDecisionRequest_MovesImagesToImagesArray(t *testing.T) {
	t.Parallel()
	top := encodeVLLMDecision(t, newVLLMDecisionRequest(schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{
		vllmUserMessage(vllmTextPart("Look at these"), vllmImagePart("data:image/png;base64,iVBORw0KGgo=")),
		vllmUserMessage(vllmImagePart("https://example.com/red.png")),
	}}))
	require.JSONEq(t, `["data:image/png;base64,iVBORw0KGgo=","https://example.com/red.png"]`, string(top["images"]))
	require.Contains(t, string(top["state"]), "Look at these")
	require.NotContains(t, string(top["state"]), "iVBORw0KGgo=")
	require.NotContains(t, string(top["state"]), "example.com")
	require.NotContains(t, top, "assets")
}

// vLLM ignores assets and answers a null state, so an image sent by reference, an
// empty input, and an image part without a URL are each a local 400.
func TestToVLLMDecisionRequest_Rejections(t *testing.T) {
	t.Parallel()
	for name, input := range map[string]schemas.DecisionInput{
		"asset image": {Messages: []schemas.DecisionInputMessage{
			vllmUserMessage(vllmTextPart("see r1"), schemas.DecisionInputPart{Type: schemas.DecisionInputPartTypeImage, ID: schemas.Ptr("r1"), Data: []byte{0x89, 0x50}}),
		}},
		"empty input": {},
		"image without url": {Messages: []schemas.DecisionInputMessage{
			vllmUserMessage(vllmTextPart("hi"), schemas.DecisionInputPart{Type: schemas.DecisionInputPartTypeImage}),
		}},
	} {
		_, err := vllm.ToVLLMDecisionRequest(newVLLMDecisionRequest(input))
		require.Error(t, err, name)
		_, isBadRequest := providerUtils.AsBifrostBadRequestError(err)
		require.True(t, isBadRequest, "%s: want a 400, got %v", name, err)
	}
}

// vLLM rejects a score question whose level descriptions repeat, so a level with no
// description is sent as its label, or its index when it has no label either.
func TestToVLLMDecisionRequest_DistinctScoreLevels(t *testing.T) {
	t.Parallel()
	request := newVLLMDecisionRequest(schemas.DecisionInput{Text: schemas.Ptr("hi")})
	request.Questions = []schemas.DecisionQuestion{
		{Type: schemas.DecisionTypeScore, Name: schemas.Ptr("by_index"), Instructions: schemas.NewDecisionText("rate"), Levels: []schemas.DecisionLevel{{Label: "0"}, {Label: "1"}, {Label: "2"}}},
		{Type: schemas.DecisionTypeScore, Name: schemas.Ptr("by_name"), Instructions: schemas.NewDecisionText("rate"), Levels: []schemas.DecisionLevel{{Label: "low"}, {Label: "high", Description: schemas.NewDecisionText("needs a reply today")}}},
		{Type: schemas.DecisionTypeScore, Name: schemas.Ptr("unlabelled"), Instructions: schemas.NewDecisionText("rate"), Levels: []schemas.DecisionLevel{{}, {}}},
	}
	top := encodeVLLMDecision(t, request)
	var questions map[string]struct {
		Criteria []any `json:"criteria"`
	}
	require.NoError(t, json.Unmarshal(top["questions"], &questions))
	require.Equal(t, []any{"0", "1", "2"}, questions["by_index"].Criteria)
	require.Equal(t, []any{"low", "high: needs a reply today"}, questions["by_name"].Criteria)
	require.Equal(t, []any{"0", "1"}, questions["unlabelled"].Criteria)
}

func newVLLMDecisionProvider(t *testing.T) *vllm.VLLMProvider {
	t.Helper()
	provider, err := vllm.NewVLLMProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{DefaultRequestTimeoutInSeconds: 30},
	}, bifrost.NewNoOpLogger())
	require.NoError(t, err)
	return provider
}

func vllmKey(url string) schemas.Key {
	return schemas.Key{VLLMKeyConfig: &schemas.VLLMKeyConfig{URL: *schemas.NewSecretVar(url)}}
}

// A decision posts to the key's vLLM URL at /v1/systemone with Bearer auth, sends
// exactly the System One body, and maps vLLM's answers, usage and id.
func TestVLLMDecision_PostsToSystemOne(t *testing.T) {
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
		_, _ = fmt.Fprint(w, vllmDecisionResponseBody)
	}))
	defer server.Close()

	provider := newVLLMDecisionProvider(t)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := vllmKey(server.URL)
	key.Value = *schemas.NewSecretVar("vllm-token")

	resp, bifrostErr := provider.Decision(ctx, key, newVLLMDecisionRequest(schemas.DecisionInput{Text: schemas.Ptr("Look at the image.")}))
	require.Nil(t, bifrostErr)

	mu.Lock()
	path, auth, body := gotPath, gotAuth, gotBody
	mu.Unlock()
	require.Equal(t, "/v1/systemone", path)
	require.Equal(t, "Bearer vllm-token", auth)
	var top map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &top))
	require.ElementsMatch(t, []string{"state", "model", "questions"}, keysOf(top))
	require.Equal(t, "decision-8abc2a9a80e3d7e2", resp.ID)
	require.Equal(t, "clef-flash", resp.Model)
	require.Len(t, resp.Answers, 1)
	require.NotNil(t, resp.Answers[0].Probability)
	require.Equal(t, 0.96, *resp.Answers[0].Probability)
	require.NotNil(t, resp.Answers[0].Confidence)
	require.Len(t, resp.Answers[0].Probabilities, 2)
	require.NotNil(t, resp.Usage)
	require.Equal(t, 35, resp.Usage.PromptTokens)
	require.Equal(t, 1, resp.Usage.CompletionTokens)

	_, bifrostErr = provider.Decision(ctx, vllmKey(server.URL), newVLLMDecisionRequest(schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{
		vllmUserMessage(vllmTextPart("Look at the image."), vllmImagePart("data:image/png;base64,AAAA")),
	}}))
	require.Nil(t, bifrostErr)
	mu.Lock()
	auth, body = gotAuth, gotBody
	mu.Unlock()
	require.Empty(t, auth, "a keyless vLLM must not get an Authorization header")
	top = nil
	require.NoError(t, json.Unmarshal(body, &top))
	require.ElementsMatch(t, []string{"state", "model", "questions", "images"}, keysOf(top))
}

func keysOf(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}

// vLLM's own errors reach the caller with their status and message, in the
// OpenAI-shaped form it uses for validation and unknown models, the string form
// of its 401, and the FastAPI form of a missing route.
func TestVLLMDecision_PassesEngineErrorsThrough(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		status int
		body   string
		want   string
	}{
		"too many options": {status: http.StatusBadRequest, body: `{"error":{"code":400,"message":"question 'many': at most 26 options for this model","param":null,"type":"BadRequestError"}}`, want: "question 'many': at most 26 options for this model"},
		"no images":        {status: http.StatusBadRequest, body: `{"error":{"code":400,"message":"this model does not take image inputs","param":null,"type":"BadRequestError"}}`, want: "this model does not take image inputs"},
		"unknown model":    {status: http.StatusNotFound, body: "{\"error\":{\"code\":404,\"message\":\"The model `x` does not exist. Valid aliases: clef-flash.\",\"param\":\"model\",\"type\":\"NotFoundError\"}}", want: "The model `x` does not exist. Valid aliases: clef-flash."},
		"no auth":          {status: http.StatusUnauthorized, body: `{"error":"Unauthorized"}`, want: "Unauthorized"},
		"missing route":    {status: http.StatusNotFound, body: `{"detail":"Not Found"}`, want: "Not Found"},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(tc.status)
			_, _ = fmt.Fprint(w, tc.body)
		}))
		provider := newVLLMDecisionProvider(t)
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		_, bifrostErr := provider.Decision(ctx, vllmKey(server.URL), newVLLMDecisionRequest(schemas.DecisionInput{Text: schemas.Ptr("hi")}))
		server.Close()
		require.NotNil(t, bifrostErr, name)
		require.Equal(t, schemas.Ptr(tc.status), bifrostErr.StatusCode, name)
		require.NotNil(t, bifrostErr.Error, name)
		require.Equal(t, tc.want, bifrostErr.Error.Message, name)
		require.False(t, bifrostErr.IsBifrostError, name)
	}
}

// A key without vllm_key_config.url cannot reach any server.
func TestVLLMDecision_NoBaseURL(t *testing.T) {
	t.Parallel()
	provider := newVLLMDecisionProvider(t)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bifrostErr := provider.Decision(ctx, schemas.Key{}, newVLLMDecisionRequest(schemas.DecisionInput{Text: schemas.Ptr("hi")}))
	require.NotNil(t, bifrostErr)
	require.Contains(t, bifrostErr.Error.Message, "vllm_key_config.url")
}

func BenchmarkVLLMDecisionRequest(b *testing.B) {
	request := newVLLMDecisionRequest(schemas.DecisionInput{Text: schemas.Ptr("Customer: I was double charged and nobody replied.")})
	request.Questions = append(request.Questions,
		schemas.DecisionQuestion{Type: schemas.DecisionTypeChoice, Name: schemas.Ptr("category"), Instructions: schemas.NewDecisionText("Pick"), Choices: []schemas.DecisionChoice{{Value: schemas.DecisionScalar{Str: schemas.Ptr("billing")}}, {Value: schemas.DecisionScalar{Str: schemas.Ptr("bug")}}}},
		schemas.DecisionQuestion{Type: schemas.DecisionTypeScore, Name: schemas.Ptr("urgency"), Instructions: schemas.NewDecisionText("Rate"), Levels: []schemas.DecisionLevel{{Label: "low"}, {Label: "high"}}},
	)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		native, err := vllm.ToVLLMDecisionRequest(request)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := providerUtils.MarshalProviderRequest(native); err != nil {
			b.Fatal(err)
		}
	}
}

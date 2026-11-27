package sgl_test

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
	"github.com/maximhq/bifrost/core/providers/sgl"
	"github.com/maximhq/bifrost/core/providers/typesafe"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// sglDecisionResponseBody is an SGLang /v1/systemone reply for a Clef checkpoint.
const sglDecisionResponseBody = `{"answers":{"is_red":{"type":"noul","noul":0.98}},"model":"clef-flash","usage":{"input_tokens":215,"output_tokens":0}}`

func newSGLDecisionRequest(input schemas.DecisionInput) *schemas.BifrostDecisionRequest {
	return &schemas.BifrostDecisionRequest{
		Provider: schemas.SGL,
		Model:    "clef-flash",
		Input:    input,
		Questions: []schemas.DecisionQuestion{
			{Type: schemas.DecisionTypePredicate, Name: schemas.Ptr("is_red"), Instructions: schemas.NewDecisionText("Is the image mostly red?")},
		},
	}
}

func sglUserMessage(parts ...schemas.DecisionInputPart) schemas.DecisionInputMessage {
	return schemas.DecisionInputMessage{Role: "user", Content: schemas.DecisionInputContent{Parts: parts}}
}

func sglTextPart(text string) schemas.DecisionInputPart {
	return schemas.DecisionInputPart{Type: schemas.DecisionInputPartTypeText, Text: schemas.NewDecisionText(text)}
}

func sglImagePart(url string) schemas.DecisionInputPart {
	return schemas.DecisionInputPart{Type: schemas.DecisionInputPartTypeImage, ImageURL: schemas.Ptr(url)}
}

func encodeSGLDecision(t *testing.T, request *schemas.BifrostDecisionRequest) map[string]json.RawMessage {
	t.Helper()
	native, err := sgl.ToSGLDecisionRequest(request)
	require.NoError(t, err)
	data, err := providerUtils.MarshalProviderRequest(native)
	require.NoError(t, err)
	var top map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &top))
	return top
}

// An input without images sends exactly Typesafe's body, so questions and state
// validate as they do there, whatever shape the input takes.
func TestToSGLDecisionRequest_MatchesTypesafeWithoutImages(t *testing.T) {
	t.Parallel()
	for name, input := range map[string]schemas.DecisionInput{
		"text":       {Text: schemas.Ptr("The sky is blue.")},
		"structured": {Structured: map[string]any{"ticket": "A-4021"}},
		"messages":   {Messages: []schemas.DecisionInputMessage{sglUserMessage(sglTextPart("hi"))}},
	} {
		request := newSGLDecisionRequest(input)
		native, err := sgl.ToSGLDecisionRequest(request)
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
func TestToSGLDecisionRequest_MovesImagesToImagesArray(t *testing.T) {
	t.Parallel()
	top := encodeSGLDecision(t, newSGLDecisionRequest(schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{
		sglUserMessage(sglTextPart("Look at these"), sglImagePart("data:image/png;base64,iVBORw0KGgo=")),
		sglUserMessage(sglImagePart("https://example.com/red.png")),
	}}))
	require.JSONEq(t, `["data:image/png;base64,iVBORw0KGgo=","https://example.com/red.png"]`, string(top["images"]))
	require.Contains(t, string(top["state"]), "Look at these")
	require.NotContains(t, string(top["state"]), "iVBORw0KGgo=")
	require.NotContains(t, top, "assets")
}

// SGLang ignores assets and rejects a null state, so an image sent by reference, an
// empty input, and an image part without a URL are each a local 400.
func TestToSGLDecisionRequest_Rejections(t *testing.T) {
	t.Parallel()
	for name, input := range map[string]schemas.DecisionInput{
		"asset image": {Messages: []schemas.DecisionInputMessage{
			sglUserMessage(sglTextPart("see r1"), schemas.DecisionInputPart{Type: schemas.DecisionInputPartTypeImage, ID: schemas.Ptr("r1"), Data: schemas.DecisionData{File: []byte{0x89, 0x50}}}),
		}},
		"empty input": {},
		"image without url": {Messages: []schemas.DecisionInputMessage{
			sglUserMessage(sglTextPart("hi"), schemas.DecisionInputPart{Type: schemas.DecisionInputPartTypeImage}),
		}},
	} {
		_, err := sgl.ToSGLDecisionRequest(newSGLDecisionRequest(input))
		require.Error(t, err, name)
		_, isBadRequest := providerUtils.AsBifrostBadRequestError(err)
		require.True(t, isBadRequest, "%s: want a 400, got %v", name, err)
	}
}

func newSGLDecisionProvider(t *testing.T, baseURL string) *sgl.SGLProvider {
	t.Helper()
	provider, err := sgl.NewSGLProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: baseURL, DefaultRequestTimeoutInSeconds: 30},
	}, bifrost.NewNoOpLogger())
	require.NoError(t, err)
	return provider
}

// A decision posts to the key's SGLang URL at /v1/systemone, falling back to the
// provider's base URL, with Bearer auth only when the key has a value, and sends
// exactly the System One body.
func TestSGLDecision_PostsToSystemOne(t *testing.T) {
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
		_, _ = fmt.Fprint(w, sglDecisionResponseBody)
	}))
	defer server.Close()

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	// Per-key URL on a provider with no base URL.
	provider := newSGLDecisionProvider(t, "")
	key := schemas.Key{SGLKeyConfig: &schemas.SGLKeyConfig{URL: *schemas.NewSecretVar(server.URL)}}
	key.Value = *schemas.NewSecretVar("sgl-token")
	resp, bifrostErr := provider.Decision(ctx, key, newSGLDecisionRequest(schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{
		sglUserMessage(sglTextPart("Look at the image."), sglImagePart("data:image/png;base64,AAAA")),
	}}))
	require.Nil(t, bifrostErr)
	mu.Lock()
	path, auth, body := gotPath, gotAuth, gotBody
	mu.Unlock()
	require.Equal(t, "/v1/systemone", path)
	require.Equal(t, "Bearer sgl-token", auth)
	var top map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &top))
	require.ElementsMatch(t, []string{"state", "model", "questions", "images"}, sglKeysOf(top))
	require.Equal(t, "clef-flash", resp.Model)
	require.Len(t, resp.Answers, 1)
	require.NotNil(t, resp.Answers[0].Probability)
	require.Equal(t, 0.98, *resp.Answers[0].Probability)
	require.Nil(t, resp.Answers[0].Confidence)
	require.NotNil(t, resp.Usage)
	require.Equal(t, 215, resp.Usage.PromptTokens)

	// Provider base URL with a keyless key.
	provider = newSGLDecisionProvider(t, server.URL)
	_, bifrostErr = provider.Decision(ctx, schemas.Key{}, newSGLDecisionRequest(schemas.DecisionInput{Text: schemas.Ptr("hi")}))
	require.Nil(t, bifrostErr)
	mu.Lock()
	path, auth, body = gotPath, gotAuth, gotBody
	mu.Unlock()
	require.Equal(t, "/v1/systemone", path)
	require.Empty(t, auth, "a keyless SGLang must not get an Authorization header")
	top = nil
	require.NoError(t, json.Unmarshal(body, &top))
	require.ElementsMatch(t, []string{"state", "model", "questions"}, sglKeysOf(top))
}

func sglKeysOf(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}

// SGLang's own errors reach the caller with their status and message: a 422
// validation detail array flattened to "path: msg", a 400 with a top-level
// message, and the string form of its 401.
func TestSGLDecision_PassesEngineErrorsThrough(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		status int
		body   string
		want   string
	}{
		"too many levels": {status: http.StatusUnprocessableEntity, body: `{"detail":[{"ctx":{"actual_length":11,"field_type":"List","max_length":10},"loc":["body","questions","eleven","score","criteria"],"msg":"List should have at most 10 items after validation, not 11","type":"too_long"}]}`, want: "questions.eleven.score.criteria: List should have at most 10 items after validation, not 11"},
		"temperature":     {status: http.StatusUnprocessableEntity, body: `{"detail":[{"ctx":{"error":{}},"loc":["body","temperature"],"msg":"Value error, temperature is not part of this API, use /v1/decisions for it","type":"value_error"}]}`, want: "temperature: Value error, temperature is not part of this API, use /v1/decisions for it"},
		"image fetch":     {status: http.StatusBadRequest, body: `{"code":400,"message":"image 0 could not be read: 403 Client Error: Forbidden for url: https://example.com/red.png","object":"error","param":null,"type":"BadRequest"}`, want: "image 0 could not be read: 403 Client Error: Forbidden for url: https://example.com/red.png"},
		"no auth":         {status: http.StatusUnauthorized, body: `{"error":"Unauthorized"}`, want: "Unauthorized"},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(tc.status)
			_, _ = fmt.Fprint(w, tc.body)
		}))
		provider := newSGLDecisionProvider(t, server.URL)
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		_, bifrostErr := provider.Decision(ctx, schemas.Key{}, newSGLDecisionRequest(schemas.DecisionInput{Text: schemas.Ptr("hi")}))
		server.Close()
		require.NotNil(t, bifrostErr, name)
		require.Equal(t, schemas.Ptr(tc.status), bifrostErr.StatusCode, name)
		require.NotNil(t, bifrostErr.Error, name)
		require.Equal(t, tc.want, bifrostErr.Error.Message, name)
		require.False(t, bifrostErr.IsBifrostError, name)
	}
}

// Without a per-key URL or a provider base URL there is no server to reach.
func TestSGLDecision_NoBaseURL(t *testing.T) {
	t.Parallel()
	provider := newSGLDecisionProvider(t, "")
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bifrostErr := provider.Decision(ctx, schemas.Key{}, newSGLDecisionRequest(schemas.DecisionInput{Text: schemas.Ptr("hi")}))
	require.NotNil(t, bifrostErr)
	require.Contains(t, bifrostErr.Error.Message, "sgl_key_config.url")
}

func BenchmarkSGLDecisionRequest(b *testing.B) {
	request := newSGLDecisionRequest(schemas.DecisionInput{Text: schemas.Ptr("Customer: I was double charged and nobody replied.")})
	request.Questions = append(request.Questions,
		schemas.DecisionQuestion{Type: schemas.DecisionTypeChoice, Name: schemas.Ptr("category"), Instructions: schemas.NewDecisionText("Pick"), Choices: []schemas.DecisionChoice{{Value: schemas.DecisionScalar{Str: schemas.Ptr("billing")}}, {Value: schemas.DecisionScalar{Str: schemas.Ptr("bug")}}}},
		schemas.DecisionQuestion{Type: schemas.DecisionTypeScore, Name: schemas.Ptr("urgency"), Instructions: schemas.NewDecisionText("Rate"), Levels: []schemas.DecisionLevel{{Label: "low"}, {Label: "high"}}},
	)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		native, err := sgl.ToSGLDecisionRequest(request)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := providerUtils.MarshalProviderRequest(native); err != nil {
			b.Fatal(err)
		}
	}
}

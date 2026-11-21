package nace_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/nace"
	"github.com/maximhq/bifrost/core/providers/typesafe"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// fixture returns a Nace provider whose base URL is a test server that runs handler. The extra
// headers try to override the client identity headers.
func fixture(t testing.TB, handler http.HandlerFunc) *nace.NaceProvider {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	provider, err := nace.NewNaceProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{
		BaseURL: server.URL, AllowPrivateNetwork: true,
		ExtraHeaders: map[string]schemas.SecretVar{"X-Nace-Client": *schemas.NewSecretVar("override"), "User-Agent": *schemas.NewSecretVar("override")},
	}}, bifrost.NewNoOpLogger(), "1.15.1\n")
	require.NoError(t, err)
	return provider
}

// observedRequest transfers fixture observations to the test goroutine before the response.
type observedRequest struct {
	path, method string
	header       http.Header
	body         []byte
	err          error
}

func observeRequest(r *http.Request) observedRequest {
	body, err := io.ReadAll(r.Body)
	return observedRequest{r.URL.Path, r.Method, r.Header.Clone(), body, err}
}

func takeRequest(t testing.TB, observations <-chan observedRequest) observedRequest {
	t.Helper()
	select {
	case got := <-observations:
		require.NoError(t, got.err)
		return got
	default:
		t.Fatal("expected an upstream request")
		return observedRequest{}
	}
}

// key returns a test key that allows every model.
func key() schemas.Key {
	return schemas.Key{Value: *schemas.NewSecretVar("test-key"), Models: []string{"*"}}
}

// ctx returns a Bifrost context without a deadline.
func ctx() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), time.Time{})
}

// request builds a drex-v2.0 decision request from a native state and System One questions.
func request(t testing.TB, state any, questions map[string]typesafe.TypesafeQuestion) *schemas.BifrostDecisionRequest {
	t.Helper()
	normalized, err := typesafe.ToBifrostDecisionQuestions(questions)
	require.NoError(t, err)
	return &schemas.BifrostDecisionRequest{Provider: schemas.Nace, Model: "drex-v2.0", Input: typesafe.ToBifrostDecisionInput(state), Questions: normalized}
}

// TestDecision checks the wire format and answer conversion for each question type and for a mixed
// request.
func TestDecision(t *testing.T) {
	for _, test := range []struct {
		name      string
		state     any
		questions string
		answers   string
	}{
		{"predicate", "hello", `{"q":{"type":"noul","instructions":"Is this a greeting?"}}`, `{"q":{"type":"noul","noul":0.9,"confidence":0.9}}`},
		{"choice-null-description", "hello", `{"q":{"type":"choice","criteria":{"greeting":null,"other":"other"}}}`, `{"q":{"type":"choice","choice":"greeting","confidence":0.8,"probabilities":{"greeting":0.9,"other":0.1}}}`},
		{"score", "hello", `{"q":{"type":"score","criteria":["low","high"]}}`, `{"q":{"type":"score","score":0.8,"confidence":0.8,"legend":["low","high"],"probabilities":{"0":0.2,"1":0.8}}}`},
		{"criteria-only-json", map[string]any{"message": "hello"}, `{"q":{"type":"noul","criteria":{"true":"greeting","false":null}}}`, `{"q":{"type":"noul","noul":0.9,"confidence":0.9}}`},
		{"mixed", []string{"hello"}, `{"predicate-id":{"type":"noul","instructions":"Greeting?"},"choice-id":{"type":"choice","criteria":{"yes":null,"no":null}},"score-id":{"type":"score","criteria":["low","high"]}}`, `{"predicate-id":{"type":"noul","noul":0.9,"confidence":0.9},"choice-id":{"type":"choice","choice":"yes","confidence":0.8,"probabilities":{"yes":0.9,"no":0.1}},"score-id":{"type":"score","score":0.8,"confidence":0.8,"legend":["low","high"],"probabilities":{"0":0.2,"1":0.8}}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var questions map[string]typesafe.TypesafeQuestion
			require.NoError(t, json.Unmarshal([]byte(test.questions), &questions))
			req := request(t, test.state, questions)
			observations := make(chan observedRequest, 1)
			provider := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				observations <- observeRequest(r)
				w.Header().Set("X-Request-ID", "req_test")
				_, _ = fmt.Fprintf(w, `{"model":"drex-v2.0","request_id":"req_test","answers":%s,"usage":{"input_tokens":42,"output_tokens":0}}`, test.answers)
			})
			context := ctx()
			context.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{"x-nace-client": {"context-override"}, "user-agent": {"context-override"}})
			response, err := provider.Decision(context, key(), req)
			require.Nil(t, err)
			got := takeRequest(t, observations)
			require.Equal(t, "/v1/systemone", got.path)
			require.Equal(t, "POST", got.method)
			require.Equal(t, "Bearer test-key", got.header.Get("Authorization"))
			require.Equal(t, "bifrost/1.15.1", got.header.Get("X-Nace-Client"))
			require.Equal(t, "bifrost/1.15.1", got.header.Get("User-Agent"))
			var sent typesafe.TypesafeDecisionRequest
			require.NoError(t, json.Unmarshal(got.body, &sent))
			require.Equal(t, questions, sent.Questions)
			require.Equal(t, "drex-v2.0", sent.Model)
			expected, marshalErr := json.Marshal(test.state)
			require.NoError(t, marshalErr)
			actual, marshalErr := json.Marshal(sent.State)
			require.NoError(t, marshalErr)
			require.JSONEq(t, string(expected), string(actual))
			require.Equal(t, "drex-v2.0", response.Model)
			require.Equal(t, "req_test", response.ID)
			require.Equal(t, 42, response.Usage.PromptTokens)
			require.Equal(t, 42, response.Usage.TotalTokens)
			require.Len(t, response.Answers, len(questions))
			for i, answer := range response.Answers {
				require.Equal(t, req.Questions[i].Name, answer.Name)
				require.Equal(t, req.Questions[i].Type, answer.Type)
				require.NotNil(t, answer.Confidence)
				if answer.Type == schemas.DecisionTypePredicate {
					require.NotNil(t, answer.Probability)
					require.Equal(t, 0.9, *answer.Probability)
				}
				if answer.Type == schemas.DecisionTypeScore {
					require.NotNil(t, answer.Score)
					require.Equal(t, 0.8, *answer.Score)
					require.Equal(t, map[string]any{"0": "low", "1": "high"}, answer.Legend)
					require.Equal(t, []schemas.DecisionProbability{
						{Value: schemas.DecisionScalar{Num: schemas.Ptr(0.0)}, Label: schemas.Ptr("0"), Probability: 0.2},
						{Value: schemas.DecisionScalar{Num: schemas.Ptr(1.0)}, Label: schemas.Ptr("1"), Probability: 0.8},
					}, answer.Probabilities)
				}
				if answer.Type == schemas.DecisionTypeChoice {
					require.NotNil(t, answer.Choice)
					chosen := "greeting"
					if test.name == "mixed" {
						chosen = "yes"
					}
					require.Equal(t, schemas.Ptr(chosen), answer.Choice.Str)
					want := []schemas.DecisionProbability{
						{Value: schemas.DecisionScalar{Str: schemas.Ptr("greeting")}, Probability: 0.9},
						{Value: schemas.DecisionScalar{Str: schemas.Ptr("other")}, Probability: 0.1},
					}
					if test.name == "mixed" {
						want = []schemas.DecisionProbability{
							{Value: schemas.DecisionScalar{Str: schemas.Ptr("no")}, Probability: 0.1},
							{Value: schemas.DecisionScalar{Str: schemas.Ptr("yes")}, Probability: 0.9},
						}
					}
					require.Equal(t, want, answer.Probabilities)
				}
			}
		})
	}
}

// TestDecisionErrors checks that Drex error statuses, messages, types and Retry-After reach the
// caller, and that the provider does not retry.
func TestDecisionErrors(t *testing.T) {
	for _, score := range []bool{false, true} {
		for _, status := range []int{401, 402, 422, 429, 500, 529} {
			t.Run(fmt.Sprintf("score=%v/status=%d", score, status), func(t *testing.T) {
				var calls atomic.Int32
				provider := fixture(t, func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					w.Header().Set("Retry-After", "2")
					w.Header().Set("X-Request-ID", "req_error")
					w.WriteHeader(status)
					_, _ = fmt.Fprint(w, `{"error":{"type":"rate_limit_error","message":"try later","issues":[]},"request_id":"req_error"}`)
				})
				question := typesafe.TypesafeQuestion{Type: "noul", Instructions: "Greeting?"}
				if score {
					question = typesafe.TypesafeQuestion{Type: "score", Criteria: []any{"low", "high"}}
				}
				context := ctx()
				response, err := provider.Decision(context, key(), request(t, "hello", map[string]typesafe.TypesafeQuestion{"q": question}))
				require.Nil(t, response)
				require.NotNil(t, err)
				require.Equal(t, status, *err.StatusCode)
				require.Equal(t, "try later", err.Error.Message)
				require.Equal(t, "rate_limit_error", *err.Error.Type)
				require.EqualValues(t, 2000, err.ExtraFields.RetryAfter)
				require.EqualValues(t, 1, calls.Load(), "provider must not retry")
				headers := context.Value(schemas.BifrostContextKeyProviderResponseHeaders).(map[string]string)
				found := false
				for k, v := range headers {
					if strings.EqualFold(k, "retry-after") {
						require.Equal(t, "2", v)
						found = true
					}
				}
				require.True(t, found)
			})
		}
	}
}

// BenchmarkDecision measures a predicate decision, which uses the shared handler, and a score
// decision, which uses the native handler.
func BenchmarkDecision(b *testing.B) {
	for _, kind := range []string{"predicate", "score"} {
		b.Run(kind, func(b *testing.B) {
			answer := `{"type":"noul","noul":0.9,"confidence":0.9}`
			question := typesafe.TypesafeQuestion{Type: "noul", Instructions: "Greeting?"}
			if kind == "score" {
				question = typesafe.TypesafeQuestion{Type: "score", Criteria: []any{"low", "high"}}
				answer = `{"type":"score","score":0.8,"confidence":0.8,"legend":["low","high"]}`
			}
			provider := fixture(b, func(w http.ResponseWriter, r *http.Request) {
				_, _ = fmt.Fprintf(w, `{"model":"drex-v2.0","answers":{"q":%s},"usage":{"input_tokens":3}}`, answer)
			})
			req, context, credential := request(b, "hello", map[string]typesafe.TypesafeQuestion{"q": question}), ctx(), key()
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				_, err := provider.Decision(context, credential, req)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// TestNaceImages checks which models accept inline images, that images reach Drex in order as a
// top-level array, and that a rejected request makes no upstream call.
func TestNaceImages(t *testing.T) {
	for _, test := range []struct {
		model     string
		images    []string
		wantError bool
	}{
		{"drex-max", []string{"data:image/png;base64,aGVsbG8=", "d29ybGQ="}, false},
		{"drex-max-v1.1", []string{"aGVsbG8="}, false},
		{"drex-max-finance", []string{"aGVsbG8="}, false},
		{"drex-v1.0", []string{"aGVsbG8="}, false},
		{"drex-v1.1", []string{"aGVsbG8="}, false},
		{"nacedm-latest", []string{"aGVsbG8="}, false},
		{"drex-v2.0", []string{"aGVsbG8="}, true},
		{"drex-v1.5", []string{"aGVsbG8="}, true},
		{"drex-latest", []string{"aGVsbG8="}, false},
		{"drex-max", []string{"https://example.com/image.png"}, true},
		{"drex-max", []string{"bad base64"}, true},
		{"drex-max", []string{"aGVsbG8=", "aGVsbG8=", "aGVsbG8=", "aGVsbG8=", "aGVsbG8=", "aGVsbG8=", "aGVsbG8=", "aGVsbG8=", "aGVsbG8="}, true},
	} {
		t.Run(fmt.Sprintf("%s/%v", test.model, test.images), func(t *testing.T) {
			var calls atomic.Int32
			observations := make(chan observedRequest, 1)
			provider := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				observations <- observeRequest(r)
				_, _ = fmt.Fprintf(w, `{"model":%q,"request_id":"req_images","answers":{"q":{"type":"noul","noul":0.9,"confidence":0.9}},"usage":{"input_tokens":200,"image_tokens":150}}`, test.model)
			})
			req := request(t, "hello", map[string]typesafe.TypesafeQuestion{"q": {Type: "noul", Instructions: "Greeting?"}})
			req.Model = test.model
			parts := []schemas.DecisionInputPart{{Type: schemas.DecisionInputPartTypeText, Text: &schemas.DecisionText{Text: schemas.Ptr("hello")}}}
			for _, image := range test.images {
				parts = append(parts, schemas.DecisionInputPart{Type: schemas.DecisionInputPartTypeImage, ImageURL: schemas.Ptr(image)})
			}
			req.Input = schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{{Role: "user", Content: schemas.DecisionInputContent{Parts: parts}}}}
			before, marshalErr := json.Marshal(req)
			require.NoError(t, marshalErr)
			response, err := provider.Decision(ctx(), key(), req)
			after, marshalErr := json.Marshal(req)
			require.NoError(t, marshalErr)
			require.JSONEq(t, string(before), string(after))
			if test.wantError {
				require.NotNil(t, err)
				require.Equal(t, 400, *err.StatusCode)
				require.Zero(t, calls.Load())
				return
			}
			require.Nil(t, err)
			require.EqualValues(t, 1, calls.Load())
			got := takeRequest(t, observations)
			var body struct {
				Images []string `json:"images"`
				Assets any      `json:"assets"`
			}
			require.NoError(t, json.Unmarshal(got.body, &body))
			require.Equal(t, test.images, body.Images)
			require.Nil(t, body.Assets)
			require.Equal(t, 150, response.Usage.PromptTokensDetails.ImageTokens)
			require.Equal(t, 200, response.Usage.TotalTokens)
		})
	}
}

// TestNaceListModels checks the GET /v1/models request headers and the conversion of the Drex
// catalog.
func TestNaceListModels(t *testing.T) {
	observations := make(chan observedRequest, 1)
	provider := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		observations <- observeRequest(r)
		_, _ = fmt.Fprint(w, `{"models":[{"name":"drex-v2.0","description":"Text"},{"name":"drex-max","description":"Images"},{"name":"drex-latest","alias_for":"drex-v2.0"},{"name":"drex-v1.0","description":"Retired on 2026-10-21.","alias_for":"drex-max"}]}`)
	})
	for _, unfiltered := range []bool{false, true} {
		credential := key()
		credential.Models = []string{"drex-max"}
		response, err := provider.ListModels(ctx(), []schemas.Key{credential}, &schemas.BifrostListModelsRequest{Unfiltered: unfiltered})
		require.Nil(t, err)
		got := takeRequest(t, observations)
		require.Equal(t, "GET", got.method)
		require.Equal(t, "/v1/models", got.path)
		require.Equal(t, "bifrost/1.15.1", got.header.Get("X-Nace-Client"))
		require.Equal(t, "bifrost/1.15.1", got.header.Get("User-Agent"))
		require.Equal(t, "Bearer test-key", got.header.Get("Authorization"))
		if unfiltered {
			require.Len(t, response.Data, 4)
		} else {
			require.Len(t, response.Data, 1)
			require.Equal(t, "nace/drex-max", response.Data[0].ID)
		}
	}
	for _, test := range []struct {
		id, price string
		limit     int
		image     bool
	}{
		{"drex-v2.0", "0.00000006", 131072, false}, {"drex-v1.5", "0.00000005", 131072, false}, {"drex-latest", "0.00000006", 131072, false},
		{"drex-max", "0.00000003", 262144, true}, {"drex-max-v1.1", "0.00000003", 262144, true}, {"drex-max-finance", "0.00000003", 262144, true},
		{"drex-v1.0", "0.00000003", 262144, true}, {"drex-v1.1", "0.00000005", 131072, false}, {"nacedm-latest", "0.00000006", 131072, false},
	} {
		t.Run(test.id, func(t *testing.T) {
			model := nace.ToBifrostModel(nace.NaceModel{Name: test.id}, schemas.Nace)
			require.Equal(t, test.price, *model.Pricing.Prompt)
			require.Equal(t, "0", *model.Pricing.Completion)
			require.Equal(t, test.limit, *model.MaxInputTokens)
			require.Equal(t, []string{"decisions"}, model.SupportedMethods)
			require.Equal(t, test.image, len(model.Architecture.InputModalities) == 2)
		})
	}
	retired := nace.ToBifrostModel(nace.NaceModel{Name: "drex-v1.0", Description: "Retired on 2026-10-21.", AliasFor: schemas.Ptr("drex-max")}, schemas.Nace)
	require.True(t, retired.IsDeprecated)
	require.Equal(t, "0.00000003", *retired.Pricing.Prompt)
	unknown := nace.ToBifrostModel(nace.NaceModel{Name: "future-model"}, schemas.Nace)
	require.Nil(t, unknown.Pricing)
}

// TestToNaceDecisionRequestStructuredText checks that structured instructions and criteria become
// sorted JSON text. Null predicate and choice descriptions stay null; a null score level becomes
// its index.
func TestToNaceDecisionRequestStructuredText(t *testing.T) {
	for _, kind := range []string{"noul", "choice", "score"} {
		t.Run(kind, func(t *testing.T) {
			description := map[string]any{"z": []any{"last", nil}, "a": map[string]any{"note": "first"}}
			question := typesafe.TypesafeQuestion{Type: kind, Instructions: description}
			switch kind {
			case "noul":
				question.Criteria = map[string]any{"true": description, "false": nil}
			case "choice":
				question.Criteria = map[string]any{"yes": description, "no": nil}
			case "score":
				question.Criteria = []any{description, "plain", nil}
			}
			req := request(t, "hello", map[string]typesafe.TypesafeQuestion{"q": question})
			before, err := json.Marshal(req)
			require.NoError(t, err)
			native, err := nace.ToNaceDecisionRequest(req)
			require.NoError(t, err)
			sent := native.Questions["q"]
			text, ok := sent.Instructions.(string)
			require.True(t, ok)
			require.Equal(t, "{\n  \"a\": {\n    \"note\": \"first\"\n  },\n  \"z\": [\n    \"last\",\n    null\n  ]\n}", text)
			switch kind {
			case "noul", "choice":
				criteria := sent.Criteria.(map[string]any)
				if kind == "noul" {
					require.Equal(t, text, criteria["true"])
					require.Nil(t, criteria["false"])
				} else {
					require.Equal(t, text, criteria["yes"])
					require.Nil(t, criteria["no"])
				}
			case "score":
				require.Equal(t, []any{text, "plain", "2"}, sent.Criteria)
			}
			after, err := json.Marshal(req)
			require.NoError(t, err)
			require.JSONEq(t, string(before), string(after))
		})
	}
}

// TestDecisionHTMLRawResponseOwnsBuffer checks that the raw body of a non-JSON error stays valid
// after the pooled response is released.
func TestDecisionHTMLRawResponseOwnsBuffer(t *testing.T) {
	html := "<html><body>Drex temporarily unavailable</body></html>"
	for _, score := range []bool{false, true} {
		t.Run(fmt.Sprintf("score=%v", score), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(http.StatusBadGateway)
				_, _ = fmt.Fprint(w, html)
			}))
			defer server.Close()
			// Direct provider calls use config defaults; core resolves per-request overrides.
			provider, providerErr := nace.NewNaceProvider(&schemas.ProviderConfig{
				NetworkConfig:       schemas.NetworkConfig{BaseURL: server.URL, AllowPrivateNetwork: true},
				SendBackRawResponse: true,
			}, bifrost.NewNoOpLogger(), "1.15.1")
			require.NoError(t, providerErr)
			question := typesafe.TypesafeQuestion{Type: "noul", Instructions: "Greeting?"}
			if score {
				question = typesafe.TypesafeQuestion{Type: "score", Criteria: []any{"low", "high"}}
			}
			context := ctx()
			_, err := provider.Decision(context, key(), request(t, "hello", map[string]typesafe.TypesafeQuestion{"q": question}))
			require.NotNil(t, err)
			require.Equal(t, http.StatusBadGateway, *err.StatusCode)
			raw := func() string {
				switch v := err.ExtraFields.RawResponse.(type) {
				case string:
					return v
				case json.RawMessage:
					return string(v)
				default:
					t.Fatalf("unexpected raw response type %T", v)
					return ""
				}
			}
			require.Equal(t, html, raw())
			encoded, marshalErr := json.Marshal(err)
			require.NoError(t, marshalErr, "HTML errors must remain JSON-serializable")
			var wire struct {
				ExtraFields struct {
					RawResponse string `json:"raw_response"`
				} `json:"extra_fields"`
			}
			require.NoError(t, json.Unmarshal(encoded, &wire))
			require.Equal(t, html, wire.ExtraFields.RawResponse)
			// Reuse fasthttp buffers after the provider has released its response.
			for i := 0; i < 64; i++ {
				pooled := fasthttp.AcquireResponse()
				pooled.SetBodyString(strings.Repeat("x", len(html)))
				fasthttp.ReleaseResponse(pooled)
			}
			require.Equal(t, html, raw(), "raw error must outlive the pooled response")
		})
	}
}

// Tool images must retain conversation order and tool-call identity without changing the input.
func TestNaceImagesInToolOutput(t *testing.T) {
	req := routerShapedDecisionRequest()
	req.Model = "drex-max-v1.1"
	req.Input = schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{
		{Role: "user", Content: schemas.DecisionInputContent{Parts: []schemas.DecisionInputPart{
			{Type: schemas.DecisionInputPartTypeText, Text: schemas.NewDecisionText("Compare these images")},
			{Type: schemas.DecisionInputPartTypeImage, ImageURL: schemas.Ptr("aGVsbG8=")},
		}}},
		{Type: schemas.Ptr("function_call"), CallID: schemas.Ptr("call_1"), Name: schemas.Ptr("lookup"), Arguments: schemas.Ptr("{}")},
		{Type: schemas.Ptr("function_call_output"), CallID: schemas.Ptr("call_1"), Output: &schemas.DecisionInputContent{Parts: []schemas.DecisionInputPart{
			{Type: schemas.DecisionInputPartTypeText, Text: schemas.NewDecisionText("Found another image")},
			{Type: schemas.DecisionInputPartTypeImage, ImageURL: schemas.Ptr("d29ybGQ=")},
		}}},
	}}
	before, err := json.Marshal(req)
	require.NoError(t, err)
	native, err := nace.ToNaceDecisionRequest(req)
	require.NoError(t, err)
	require.Equal(t, []string{"aGVsbG8=", "d29ybGQ="}, native.Images)
	state, err := json.Marshal(native.State)
	require.NoError(t, err)
	require.Contains(t, string(state), "call_1")
	require.Contains(t, string(state), "Found another image")
	require.NotContains(t, string(state), "d29ybGQ=")
	after, err := json.Marshal(req)
	require.NoError(t, err)
	require.JSONEq(t, string(before), string(after))
}

func TestNaceImageSizeLimit(t *testing.T) {
	for _, size := range []int{16*1024*1024 - 1, 16 * 1024 * 1024, 16*1024*1024 + 1, 16*1024*1024 + 2} {
		for _, raw := range []bool{false, true} {
			t.Run(fmt.Sprintf("bytes=%d/raw=%v", size, raw), func(t *testing.T) {
				part := schemas.DecisionInputPart{Type: schemas.DecisionInputPartTypeImage}
				if raw {
					part.Data.File = make([]byte, size)
				} else {
					part.ImageURL = schemas.Ptr(base64.StdEncoding.EncodeToString(make([]byte, size)))
				}
				req := routerShapedDecisionRequest()
				req.Model = "drex-max"
				req.Input = schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{{Role: "user", Content: schemas.DecisionInputContent{Parts: []schemas.DecisionInputPart{part}}}}}
				_, err := nace.ToNaceDecisionRequest(req)
				if size > 16*1024*1024 {
					require.ErrorContains(t, err, "at most 16 MiB")
				} else {
					require.NoError(t, err)
				}
			})
		}
	}
}

func TestNaceCatalogAliasRetargeting(t *testing.T) {
	observed := make(chan string, 1)
	provider := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_, _ = fmt.Fprint(w, `{"models":[{"name":"drex-latest","alias_for":"drex-max-v1.1"}]}`)
			return
		}
		var sent struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		observed <- sent.Model
		_, _ = fmt.Fprint(w, `{"model":"drex-max-v1.1","answers":{"complexity_tier":{"type":"choice","choice":"SIMPLE"}},"usage":{"input_tokens":1}}`)
	})
	catalog, bErr := provider.ListModels(ctx(), []schemas.Key{key()}, &schemas.BifrostListModelsRequest{})
	require.Nil(t, bErr)
	require.Len(t, catalog.Data, 1)
	require.Contains(t, catalog.Data[0].Architecture.InputModalities, "image")
	req := routerShapedDecisionRequest()
	req.Model = strings.TrimPrefix(catalog.Data[0].ID, "nace/")
	req.Input = schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{{Role: "user", Content: schemas.DecisionInputContent{Parts: []schemas.DecisionInputPart{{Type: schemas.DecisionInputPartTypeImage, ImageURL: schemas.Ptr("aGVsbG8=")}}}}}}
	_, bErr = provider.Decision(ctx(), key(), req)
	require.Nil(t, bErr)
	select {
	case model := <-observed:
		require.Equal(t, "drex-latest", model, "upstream resolves the current alias")
	default:
		t.Fatal("advertised image-capable alias never reached upstream")
	}
}

func TestNaceBackfilledModels(t *testing.T) {
	provider := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"models":[{"name":"drex-max"}]}`)
	})
	credential := key()
	credential.Models = []string{"drex-max", "drex-max-v1.1", "custom-max", "future-model"}
	credential.Aliases = schemas.KeyAliases{"custom-max": {ModelID: "drex-max-v1.1"}}
	catalog, err := provider.ListModels(ctx(), []schemas.Key{credential}, &schemas.BifrostListModelsRequest{})
	require.Nil(t, err)
	require.Len(t, catalog.Data, 4)
	for _, model := range catalog.Data {
		if model.ID == "nace/future-model" {
			require.Nil(t, model.Pricing)
			continue
		}
		require.Equal(t, []string{"decisions"}, model.SupportedMethods)
		require.Contains(t, model.Architecture.InputModalities, "image")
		require.Equal(t, 262144, *model.MaxInputTokens)
		require.Equal(t, "0.00000003", *model.Pricing.Prompt)
		if model.ID == "nace/custom-max" {
			require.Equal(t, "drex-max-v1.1", *model.Alias)
		}
	}
}

// BenchmarkToNaceDecisionRequest measures request conversion with plain and structured Complexity
// Router instructions.
func BenchmarkToNaceDecisionRequest(b *testing.B) {
	for _, structured := range []bool{false, true} {
		b.Run(fmt.Sprintf("structured=%v", structured), func(b *testing.B) {
			req := routerShapedDecisionRequest()
			if !structured {
				req.Questions[0].Instructions = schemas.NewDecisionText("Classify complexity")
				for i := range req.Questions[0].Choices {
					req.Questions[0].Choices[i].Description = schemas.NewDecisionText("tier description")
				}
			}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := nace.ToNaceDecisionRequest(req); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// TestNacePinnedImageCapability checks that pinned text-only IDs reject images locally.
// Mutable aliases are validated by the upstream that resolves them, including before listing.
func TestNacePinnedImageCapability(t *testing.T) {
	for _, model := range []string{"drex-max", "drex-max-v1.1", "drex-max-finance", "drex-v2.0", "drex-v1.5"} {
		t.Run(model, func(t *testing.T) {
			entry := nace.ToBifrostModel(nace.NaceModel{Name: model}, schemas.Nace)
			req := routerShapedDecisionRequest()
			req.Model = model
			req.Input = schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{{Role: "user", Content: schemas.DecisionInputContent{Parts: []schemas.DecisionInputPart{{Type: schemas.DecisionInputPartTypeImage, ImageURL: schemas.Ptr("aGVsbG8=")}}}}}}
			_, err := nace.ToNaceDecisionRequest(req)
			require.Equal(t, len(entry.Architecture.InputModalities) == 2, err == nil)
		})
	}
}

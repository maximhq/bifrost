package nace_test

import (
	"context"
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

func key() schemas.Key {
	return schemas.Key{Value: *schemas.NewSecretVar("test-key"), Models: []string{"*"}}
}
func ctx() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), time.Time{})
}

func request(t testing.TB, state any, questions map[string]typesafe.TypesafeQuestion) *schemas.BifrostDecisionRequest {
	t.Helper()
	normalized, err := typesafe.ToBifrostDecisionQuestions(questions)
	require.NoError(t, err)
	return &schemas.BifrostDecisionRequest{Provider: schemas.Nace, Model: "drex-v2.0", Input: typesafe.ToBifrostDecisionInput(state), Questions: normalized}
}

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
			provider := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/v1/systemone", r.URL.Path)
				require.Equal(t, "POST", r.Method)
				require.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
				require.Equal(t, "bifrost/1.15.1", r.Header.Get("X-Nace-Client"))
				require.Equal(t, "bifrost/1.15.1", r.UserAgent())
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				var sent typesafe.TypesafeDecisionRequest
				require.NoError(t, json.Unmarshal(body, &sent))
				require.Equal(t, questions, sent.Questions)
				require.Equal(t, "drex-v2.0", sent.Model)
				expected, err := json.Marshal(test.state)
				require.NoError(t, err)
				actual, err := json.Marshal(sent.State)
				require.NoError(t, err)
				require.JSONEq(t, string(expected), string(actual))
				w.Header().Set("X-Request-ID", "req_test")
				_, _ = fmt.Fprintf(w, `{"model":"drex-v2.0","request_id":"req_test","answers":%s,"usage":{"input_tokens":42,"output_tokens":0}}`, test.answers)
			})
			context := ctx()
			context.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{"x-nace-client": {"context-override"}, "user-agent": {"context-override"}})
			response, err := provider.Decision(context, key(), req)
			require.Nil(t, err)
			require.Equal(t, "drex-v2.0", response.Model)
			require.Equal(t, "req_test", response.ID)
			require.Equal(t, 42, response.Usage.PromptTokens)
			require.Equal(t, 42, response.Usage.TotalTokens)
			require.Len(t, response.Answers, len(questions))
			for i, answer := range response.Answers {
				require.Equal(t, req.Questions[i].Name, answer.Name)
				require.NotNil(t, answer.Confidence)
				if answer.Type == schemas.DecisionTypePredicate {
					require.NotNil(t, answer.Probability)
					require.Equal(t, 0.9, *answer.Probability)
				}
				if answer.Type == schemas.DecisionTypeScore {
					require.Equal(t, map[string]any{"0": "low", "1": "high"}, answer.Legend)
					require.Equal(t, []schemas.DecisionProbability{
						{Value: schemas.DecisionScalar{Num: schemas.Ptr(0.0)}, Label: schemas.Ptr("0"), Probability: 0.2},
						{Value: schemas.DecisionScalar{Num: schemas.Ptr(1.0)}, Label: schemas.Ptr("1"), Probability: 0.8},
					}, answer.Probabilities)
				}
				if answer.Type == schemas.DecisionTypeChoice {
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
		{"drex-v1.1", []string{"aGVsbG8="}, true},
		{"nacedm-latest", []string{"aGVsbG8="}, true},
		{"drex-v2.0", []string{"aGVsbG8="}, true},
		{"drex-v1.5", []string{"aGVsbG8="}, true},
		{"drex-latest", []string{"aGVsbG8="}, true},
		{"drex-max", []string{"https://example.com/image.png"}, true},
		{"drex-max", []string{"bad base64"}, true},
		{"drex-max", []string{"aGVsbG8=", "aGVsbG8=", "aGVsbG8=", "aGVsbG8=", "aGVsbG8=", "aGVsbG8=", "aGVsbG8=", "aGVsbG8=", "aGVsbG8="}, true},
	} {
		t.Run(fmt.Sprintf("%s/%v", test.model, test.images), func(t *testing.T) {
			var calls atomic.Int32
			provider := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var body struct {
					Images []string `json:"images"`
					Assets any      `json:"assets"`
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				require.Equal(t, test.images, body.Images)
				require.Nil(t, body.Assets)
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
			require.Equal(t, 150, response.Usage.PromptTokensDetails.ImageTokens)
			require.Equal(t, 200, response.Usage.TotalTokens)
		})
	}
}

func TestNaceListModels(t *testing.T) {
	provider := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "GET", r.Method)
		require.Equal(t, "/v1/models", r.URL.Path)
		require.Equal(t, "bifrost/1.15.1", r.Header.Get("X-Nace-Client"))
		require.Equal(t, "bifrost/1.15.1", r.UserAgent())
		require.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		_, _ = fmt.Fprint(w, `{"models":[{"name":"drex-v2.0","description":"Text"},{"name":"drex-max","description":"Images"},{"name":"drex-latest","alias_for":"drex-v2.0"},{"name":"drex-v1.0","description":"Retired on 2026-10-21.","alias_for":"drex-max"}]}`)
	})
	for _, unfiltered := range []bool{false, true} {
		credential := key()
		credential.Models = []string{"drex-max"}
		response, err := provider.ListModels(ctx(), []schemas.Key{credential}, &schemas.BifrostListModelsRequest{Unfiltered: unfiltered})
		require.Nil(t, err)
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

func TestNaceAliasImageCapability(t *testing.T) {
	for _, model := range []string{"drex-v1.0", "drex-v1.1", "drex-latest", "nacedm-latest", "drex-max", "drex-max-v1.1", "drex-max-finance", "drex-v2.0"} {
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

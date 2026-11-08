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
				fmt.Fprintf(w, `{"model":"drex-v2.0","request_id":"req_test","answers":%s,"usage":{"input_tokens":42,"output_tokens":0}}`, test.answers)
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
				if answer.Type == schemas.DecisionTypeScore {
					require.Equal(t, map[string]any{"0": "low", "1": "high"}, answer.Legend)
					require.Len(t, answer.Probabilities, 2)
				}
				if answer.Type == schemas.DecisionTypeChoice {
					require.Len(t, answer.Probabilities, 2)
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
					fmt.Fprint(w, `{"error":{"type":"rate_limit_error","message":"try later","issues":[]},"request_id":"req_error"}`)
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
				fmt.Fprintf(w, `{"model":"drex-v2.0","answers":{"q":%s},"usage":{"input_tokens":3}}`, answer)
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

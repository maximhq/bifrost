package bifrost

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

type astraUpstreamRequest struct {
	Method  string
	Path    string
	Body    []byte
	ReadErr error
}

// TestGPT6AstraMaxReasoningEffortReachesOpenAIUpstream exercises the public
// Responses API through provider selection, OpenAI conversion, JSON encoding,
// and the real HTTP client. The fake upstream makes the silently rewritten
// request observable without requiring a real OpenAI API key.
func TestGPT6AstraMaxReasoningEffortReachesOpenAIUpstream(t *testing.T) {
	requests := make(chan astraUpstreamRequest, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		requests <- astraUpstreamRequest{
			Method:  r.Method,
			Path:    r.URL.Path,
			Body:    body,
			ReadErr: err,
		}
		writeJSON(w, http.StatusOK, `{"id":"resp_astra_1","object":"response","created_at":1,"status":"completed","model":"gpt-6-astra","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	}))
	t.Cleanup(upstream.Close)

	// This is the capability information currently available to the Go runtime:
	// the datasheet says Astra supports reasoning, but does not publish its
	// reasoning_effort_levels ladder.
	schemas.SetCapabilityResolver(func(provider schemas.ModelProvider, model string) *schemas.ModelCapabilities {
		if provider == schemas.OpenAI && model == "gpt-6-astra" {
			return &schemas.ModelCapabilities{SupportsReasoning: schemas.Ptr(true)}
		}
		return nil
	})
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 1, upstream.URL)
	account.configs[schemas.OpenAI].NetworkConfig.MaxRetries = 0
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{{
		ID:     "astra-e2e-key",
		Value:  *schemas.NewSecretVar("sk-local-astra-e2e"),
		Models: schemas.WhiteList{"gpt-6-astra"},
		Weight: 100,
	}})
	client := newStreamTestClient(t, account)

	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(5*time.Second))
	response, bifrostErr := client.ResponsesRequest(ctx, &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-6-astra",
		Input: []schemas.ResponsesMessage{{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{
				ContentStr: schemas.Ptr("Say hello"),
			},
		}},
		Params: &schemas.ResponsesParameters{
			Reasoning: &schemas.ResponsesParametersReasoning{
				Effort: schemas.Ptr(schemas.ReasoningEffortMax),
			},
		},
	})
	if bifrostErr != nil {
		t.Fatalf("ResponsesRequest failed before reaching the upstream: %v", bifrostErr)
	}
	if response == nil {
		t.Fatal("ResponsesRequest returned a nil response")
	}

	observed := <-requests
	if observed.ReadErr != nil {
		t.Fatalf("read upstream request body: %v", observed.ReadErr)
	}
	if observed.Method != http.MethodPost || observed.Path != "/v1/responses" {
		t.Fatalf("upstream request = %s %s, want POST /v1/responses", observed.Method, observed.Path)
	}

	var wire struct {
		Model     string `json:"model"`
		Reasoning struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
	}
	if err := json.Unmarshal(observed.Body, &wire); err != nil {
		t.Fatalf("decode upstream request body %q: %v", observed.Body, err)
	}
	if wire.Model != "gpt-6-astra" {
		t.Fatalf("upstream model = %q, want gpt-6-astra", wire.Model)
	}
	if wire.Reasoning.Effort != schemas.ReasoningEffortMax {
		t.Fatalf("upstream reasoning.effort = %q, want %q; Bifrost silently changed the requested effort on the wire",
			wire.Reasoning.Effort, schemas.ReasoningEffortMax)
	}
}

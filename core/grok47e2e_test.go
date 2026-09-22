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

// TestGrok47XHighReasoningEffortReachesXAIUpstream exercises the public
// Responses API through provider selection, xAI/OpenAI conversion, JSON
// encoding, and the real HTTP client. The local upstream makes a silent effort
// downgrade observable without requiring an xAI API key.
func TestGrok47XHighReasoningEffortReachesXAIUpstream(t *testing.T) {
	requests := make(chan []byte, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read upstream request body: %v", err)
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			t.Errorf("upstream request = %s %s, want POST /v1/responses", r.Method, r.URL.Path)
		}
		requests <- body
		writeJSON(w, http.StatusOK, `{"id":"resp_grok_47_1","object":"response","created_at":1,"status":"completed","model":"grok-4.7","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	}))
	t.Cleanup(upstream.Close)

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.XAI, 1, 1, upstream.URL)
	account.configs[schemas.XAI].NetworkConfig.MaxRetries = 0
	account.SetKeysForProvider(schemas.XAI, []schemas.Key{{
		ID:     "grok-47-repro-key",
		Value:  *schemas.NewSecretVar("sk-local-grok-47-repro"),
		Models: schemas.WhiteList{"grok-4.7"},
		Weight: 100,
	}})
	client := newStreamTestClient(t, account)

	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(5*time.Second))
	response, bifrostErr := client.ResponsesRequest(ctx, &schemas.BifrostResponsesRequest{
		Provider: schemas.XAI,
		Model:    "grok-4.7",
		Input: []schemas.ResponsesMessage{{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{
				ContentStr: schemas.Ptr("Say hello"),
			},
		}},
		Params: &schemas.ResponsesParameters{
			Reasoning: &schemas.ResponsesParametersReasoning{
				Effort: schemas.Ptr(schemas.ReasoningEffortXHigh),
			},
		},
	})
	if bifrostErr != nil {
		t.Fatalf("ResponsesRequest failed before reaching the upstream: %v", bifrostErr)
	}
	if response == nil {
		t.Fatal("ResponsesRequest returned a nil response")
	}

	var wire struct {
		Model     string `json:"model"`
		Reasoning struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
	}
	if err := json.Unmarshal(<-requests, &wire); err != nil {
		t.Fatalf("decode upstream request: %v", err)
	}
	if wire.Model != "grok-4.7" {
		t.Fatalf("upstream model = %q, want grok-4.7", wire.Model)
	}
	if wire.Reasoning.Effort != schemas.ReasoningEffortXHigh {
		t.Fatalf("upstream reasoning.effort = %q, want %q; Bifrost silently changed the requested effort on the wire",
			wire.Reasoning.Effort, schemas.ReasoningEffortXHigh)
	}
}

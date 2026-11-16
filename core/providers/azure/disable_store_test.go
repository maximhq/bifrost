package azure_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/providers/azure"
	"github.com/maximhq/bifrost/core/schemas"
)

// newDisableStoreTestProvider creates an Azure provider with the given disable_store setting and a key whose
// endpoint is the test server.
func newDisableStoreTestProvider(t *testing.T, endpoint string, disableStore bool) (*azure.AzureProvider, schemas.Key) {
	t.Helper()
	provider, err := azure.NewAzureProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{DefaultRequestTimeoutInSeconds: 30},
		OpenAIConfig:  &schemas.OpenAIConfig{DisableStore: disableStore},
	}, routingTestLogger{})
	if err != nil {
		t.Fatalf("failed to create Azure provider: %v", err)
	}
	return provider, schemas.Key{
		Value:          *schemas.NewSecretVar("test-key"),
		Models:         []string{"*"},
		AzureKeyConfig: &schemas.AzureKeyConfig{Endpoint: *schemas.NewSecretVar(endpoint)},
	}
}

// TestAzureDisableStoreForcesStoreFalse verifies that openai_config.disable_store forces store=false on Azure
// Responses API requests, including when a passthrough extra_params store would otherwise override the typed field,
// and that the caller's request is not rewritten.
func TestAzureDisableStoreForcesStoreFalse(t *testing.T) {
	passthroughPostHook := func(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		return result, err
	}

	for _, disableStore := range []bool{true, false} {
		for _, streaming := range []bool{false, true} {
			for _, mode := range []string{"typed", "passthrough"} {
				t.Run(fmt.Sprintf("disable_store=%v/stream=%v/%s", disableStore, streaming, mode), func(t *testing.T) {
					var body []byte
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						body, _ = io.ReadAll(r.Body)
						if streaming {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"sequence_number\":0,\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":1,\"status\":\"completed\",\"model\":\"gpt-4o\",\"output\":[]}}\n\n")
							return
						}
						w.Header().Set("Content-Type", "application/json")
						_, _ = fmt.Fprint(w, `{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"gpt-4o","output":[]}`)
					}))
					defer server.Close()

					provider, key := newDisableStoreTestProvider(t, server.URL, disableStore)
					ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
					req := &schemas.BifrostResponsesRequest{
						Provider: schemas.Azure,
						Model:    "gpt-4o",
						Input:    []schemas.ResponsesMessage{llmtests.CreateBasicResponsesMessage("hello")},
						Params:   &schemas.ResponsesParameters{Store: schemas.Ptr(true)},
					}
					if mode == "passthrough" {
						ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
						req.Params.ExtraParams = map[string]interface{}{"store": true}
					}

					if streaming {
						stream, bErr := provider.ResponsesStream(ctx, passthroughPostHook, nil, key, req)
						if bErr != nil {
							t.Fatalf("ResponsesStream returned error: %v", llmtests.GetErrorMessage(bErr))
						}
						for range stream {
						}
					} else if _, bErr := provider.Responses(ctx, key, req); bErr != nil {
						t.Fatalf("Responses returned error: %v", llmtests.GetErrorMessage(bErr))
					}

					want := `"store":true`
					if disableStore {
						want = `"store":false`
					}
					if !strings.Contains(string(body), want) || strings.Count(string(body), `"store":`) != 1 {
						t.Fatalf("expected upstream body to contain exactly one %s, got %s", want, body)
					}
					if !*req.Params.Store {
						t.Fatalf("caller's Params.Store was rewritten to false")
					}
					if v, ok := req.Params.ExtraParams["store"]; ok && v != true {
						t.Fatalf("caller's ExtraParams store was rewritten to %v", v)
					}
				})
			}
		}
	}
}

// TestAzureDisableStoreLeavesChatCompletionsUnchanged verifies that disable_store only applies to the Responses API:
// Azure's chat completions endpoint also serves non-OpenAI models, so the client's store value is sent as is.
func TestAzureDisableStoreLeavesChatCompletionsUnchanged(t *testing.T) {
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"chatcmpl_1","object":"chat.completion","created":1,"model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	provider, key := newDisableStoreTestProvider(t, server.URL, true)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := &schemas.BifrostChatRequest{
		Provider: schemas.Azure,
		Model:    "gpt-4o",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}}},
		Params:   &schemas.ChatParameters{Store: schemas.Ptr(true)},
	}

	if _, bErr := provider.ChatCompletion(ctx, key, req); bErr != nil {
		t.Fatalf("ChatCompletion returned error: %v", llmtests.GetErrorMessage(bErr))
	}

	if !strings.Contains(string(body), `"store":true`) {
		t.Fatalf("expected chat completions body to keep the client's store=true, got %s", body)
	}
}

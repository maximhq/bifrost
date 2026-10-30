package openai

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// BenchmarkBooleanSchemaProviderRequests measures the provider request path,
// including conversion, serialization, and loopback HTTP transport. The same benchmark can
// run before this option existed: the configuration is decoded from JSON rather
// than referring to the new field, so before/after results use identical inputs.
func BenchmarkBooleanSchemaProviderRequests(b *testing.B) {
	const booleanParams = `{"type":"object","properties":{"state":true,"denied":false,"options":{"type":"array","items":true},"flag":{"type":"boolean","enum":[true,false],"default":true}},"additionalProperties":false}`
	const objectParams = `{"type":"object","properties":{"state":{},"denied":{"not":{}},"options":{"type":"array","items":{}},"flag":{"type":"boolean","enum":[true,false],"default":true}},"additionalProperties":false}`
	for _, mode := range []string{"chat", "responses"} {
		for _, scenario := range []struct {
			name    string
			enabled bool
			params  string
		}{
			{"disabled", false, booleanParams},
			{"enabled_unchanged", true, objectParams},
			{"rewritten", true, booleanParams},
		} {
			b.Run(mode+"/"+scenario.name, func(b *testing.B) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					w.Header().Set("Content-Type", "application/json")
					if mode == "responses" {
						_, _ = io.WriteString(w, `{"id":"resp_bench","object":"response","created_at":1,"status":"completed","model":"acme-model","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
					} else {
						_, _ = io.WriteString(w, `{"id":"chatcmpl_bench","object":"chat.completion","created":1,"model":"acme-model","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
					}
				}))
				b.Cleanup(server.Close)
				var custom schemas.CustomProviderConfig
				if err := json.Unmarshal([]byte(fmt.Sprintf(`{"base_provider_type":"openai","boolean_schema_nodes_unsupported":%t}`, scenario.enabled)), &custom); err != nil {
					b.Fatal(err)
				}
				custom.CustomProviderKey = "acme"
				provider := NewOpenAIProvider(&schemas.ProviderConfig{
					NetworkConfig:        schemas.NetworkConfig{BaseURL: server.URL, DefaultRequestTimeoutInSeconds: 10, AllowPrivateNetwork: true},
					CustomProviderConfig: &custom,
				}, testNoopLogger{})
				var params schemas.ToolFunctionParameters
				if err := json.Unmarshal([]byte(scenario.params), &params); err != nil {
					b.Fatal(err)
				}
				chat := &schemas.BifrostChatRequest{
					Provider: "acme",
					Model:    "acme-model",
					Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}},
					Params:   &schemas.ChatParameters{Tools: []schemas.ChatTool{{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: "set_state", Parameters: &params}}}},
				}
				responses := chat.ToResponsesRequest()
				key := schemas.Key{Value: *schemas.NewSecretVar("test-key")}
				b.ReportAllocs()
				for b.Loop() {
					ctx := schemas.NewBifrostContext(b.Context(), schemas.NoDeadline)
					var err *schemas.BifrostError
					if mode == "responses" {
						_, err = provider.Responses(ctx, key, responses)
					} else {
						_, err = provider.ChatCompletion(ctx, key, chat)
					}
					if err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

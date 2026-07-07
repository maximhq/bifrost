package azure

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/valyala/fasthttp"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

type noopAzureTestLogger struct{}

func (noopAzureTestLogger) Debug(string, ...any)                   {}
func (noopAzureTestLogger) Info(string, ...any)                    {}
func (noopAzureTestLogger) Warn(string, ...any)                    {}
func (noopAzureTestLogger) Error(string, ...any)                   {}
func (noopAzureTestLogger) Fatal(string, ...any)                   {}
func (noopAzureTestLogger) SetLevel(schemas.LogLevel)              {}
func (noopAzureTestLogger) SetOutputType(schemas.LoggerOutputType) {}
func (noopAzureTestLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

func TestShouldRouteAzureResponsesThroughChatCompletion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		model string
		want  bool
	}{
		{name: "model router", model: "azure/model-router", want: true},
		{name: "model router with suffix", model: "azure/model-router-v2", want: true},
		{name: "openai model", model: "gpt-4o", want: false},
		{name: "anthropic model", model: "claude-sonnet-4-5", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := schemas.IsAzureModelRouterFamily(tt.model); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAzureResponsesRoutesModelRouterThroughChatCompletions(t *testing.T) {
	t.Parallel()

	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","created":123,"model":"azure/model-router","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer server.Close()

	provider := &AzureProvider{
		client:          &fasthttp.Client{},
		streamingClient: &fasthttp.Client{},
		logger:          noopAzureTestLogger{},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{
		Value: *schemas.NewSecretVar("test-api-key"),
		AzureKeyConfig: &schemas.AzureKeyConfig{
			Endpoint: *schemas.NewSecretVar(server.URL),
		},
	}

	resp, err := provider.Responses(ctx, key, &schemas.BifrostResponsesRequest{
		Provider: schemas.Azure,
		Model:    "azure/model-router",
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hello")},
		}},
	})
	if err != nil {
		t.Fatalf("Responses returned error: %v", err)
	}

	if gotPath != "/openai/v1/chat/completions" {
		t.Fatalf("got path %q, want %q", gotPath, "/openai/v1/chat/completions")
	}

	if resp == nil || len(resp.Output) == 0 {
		t.Fatalf("expected fallback response output, got %+v", resp)
	}
	if resp.Output[0].Content == nil {
		t.Fatalf("unexpected response output: %+v", resp.Output[0])
	}
	if resp.Output[0].Content.ContentStr != nil {
		if *resp.Output[0].Content.ContentStr != "hello" {
			t.Fatalf("unexpected response output: %+v", resp.Output[0])
		}
		return
	}
	if len(resp.Output[0].Content.ContentBlocks) == 0 || resp.Output[0].Content.ContentBlocks[0].Text == nil || *resp.Output[0].Content.ContentBlocks[0].Text != "hello" {
		t.Fatalf("unexpected response output: %+v", resp.Output[0])
	}
}

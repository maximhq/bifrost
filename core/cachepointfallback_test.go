package bifrost

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureBody records each request body the server receives, then replies with status and body.
func captureBody(mu *sync.Mutex, bodies *[]string, status int, reply string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		*bodies = append(*bodies, string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}
}

// TestChatCachePoint_StrippedForPrimaryKeptForBedrockFallback runs an OpenAI -> Bedrock fallback end to end.
func TestChatCachePoint_StrippedForPrimaryKeptForBedrockFallback(t *testing.T) {
	var mu sync.Mutex
	var openaiBodies, bedrockBodies []string

	primary := httptest.NewServer(captureBody(&mu, &openaiBodies, http.StatusInternalServerError,
		`{"error":{"message":"boom","type":"server_error"}}`))
	defer primary.Close()
	fallback := httptest.NewTLSServer(captureBody(&mu, &bedrockBodies, http.StatusOK,
		`{"output":{"message":{"role":"assistant","content":[{"text":"hello"}]}},"stopReason":"end_turn","usage":{"inputTokens":1,"outputTokens":1,"totalTokens":2}}`))
	defer fallback.Close()

	account := NewMockAccount()
	account.AddProviderWithBaseURL(schemas.OpenAI, 1, 1, primary.URL)
	account.configs[schemas.OpenAI].NetworkConfig.MaxRetries = 0
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
		{ID: "openai-key", Value: *schemas.NewSecretVar("sk-test"), Models: schemas.WhiteList{"*"}, Weight: 100},
	})
	account.AddProviderWithBaseURL(schemas.Bedrock, 1, 1, "")
	account.configs[schemas.Bedrock].NetworkConfig.MaxRetries = 0
	account.configs[schemas.Bedrock].NetworkConfig.InsecureSkipVerify = true
	account.SetKeysForProvider(schemas.Bedrock, []schemas.Key{{
		ID: "bedrock-key", Models: schemas.WhiteList{"*"}, Weight: 100,
		BedrockKeyConfig: &schemas.BedrockKeyConfig{
			AccessKey: *schemas.NewSecretVar("AKIATEST"),
			SecretKey: *schemas.NewSecretVar("secret"),
			Region:    schemas.NewSecretVar("us-east-1"),
			Endpoints: &schemas.BedrockEndpoints{Runtime: schemas.NewSecretVar(strings.TrimPrefix(fallback.URL, "https://"))},
		},
	}})
	client := newStreamTestClient(t, account)

	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input: []schemas.ChatMessage{{
			Role: schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
				{Type: schemas.ChatContentBlockTypeText, Text: schemas.Ptr("stable prefix")},
				{CachePoint: &schemas.CachePoint{Type: "default"}},
			}},
		}},
		Fallbacks: []schemas.Fallback{{Provider: schemas.Bedrock, Model: "anthropic.claude-3-5-haiku-20241022-v1:0"}},
	}

	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(10*time.Second))
	resp, bifrostErr := client.ChatCompletionRequest(ctx, req)
	if bifrostErr != nil && bifrostErr.Error != nil {
		t.Fatalf("fallback failed: %s", bifrostErr.Error.Message)
	}
	require.NotNil(t, resp)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, openaiBodies, 1, "primary was not attempted")
	require.Len(t, bedrockBodies, 1, "fallback was not attempted")
	assert.NotContains(t, openaiBodies[0], "cachePoint", "primary wire body leaked the Bedrock marker")
	assert.Contains(t, bedrockBodies[0], "cachePoint", "fallback lost the caller's cachePoint")
	assert.Len(t, req.Input[0].Content.ContentBlocks, 2, "caller's request was mutated")
}

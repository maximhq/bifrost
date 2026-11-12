package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConvertRequestsToJSONLRequiresCustomID(t *testing.T) {
	for _, customID := range []string{"", "   "} {
		_, err := ConvertRequestsToJSONL([]schemas.BatchRequestItem{{
			CustomID: customID,
			Method:   "POST",
			URL:      string(schemas.BatchEndpointChatCompletions),
			Body:     map[string]interface{}{"model": "gpt-4o-mini"},
		}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "batch request item 0: custom_id is required")
	}
}

func TestConvertRequestsToJSONLPreservesCustomID(t *testing.T) {
	data, err := ConvertRequestsToJSONL([]schemas.BatchRequestItem{{
		CustomID: "request-1",
		Method:   "POST",
		URL:      string(schemas.BatchEndpointEmbeddings),
		Body:     map[string]interface{}{"model": "text-embedding-3-small", "input": "hello"},
	}})
	require.NoError(t, err)

	line := strings.TrimSpace(string(data))
	var decoded schemas.BatchRequestItem
	require.NoError(t, sonic.UnmarshalString(line, &decoded))
	assert.Equal(t, "request-1", decoded.CustomID)
}

// BenchmarkOpenAIFileUpload measures what FileUpload allocates to send a 17 MB file. Before the
// file was streamed, the provider wrote the whole multipart body into a bytes.Buffer and copied it
// into the request, ~3x the file per upload. The upstream drains the body without keeping it.
func BenchmarkOpenAIFileUpload(b *testing.B) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"file-abc","object":"file","bytes":1,"created_at":1,"filename":"batch.jsonl","purpose":"batch"}`))
	}))
	b.Cleanup(upstream.Close)
	provider := NewOpenAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: upstream.URL, DefaultRequestTimeoutInSeconds: 60, AllowPrivateNetwork: true},
	}, nil)
	key := schemas.Key{Value: *schemas.NewSecretVar("sk-test"), Models: []string{"*"}}
	request := &schemas.BifrostFileUploadRequest{
		Provider: schemas.OpenAI,
		File:     []byte(strings.Repeat("a", 17<<20)),
		Filename: "batch.jsonl",
		Purpose:  "batch",
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		if _, bErr := provider.FileUpload(ctx, key, request); bErr != nil {
			b.Fatal(bErr.Error.Message)
		}
	}
}

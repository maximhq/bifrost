package vertex

import (
	"bytes"
	"context"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parseVertexJSONLLines splits the JSONL output into one decoded map per line.
func parseVertexJSONLLines(t *testing.T, data []byte) []map[string]interface{} {
	t.Helper()
	var lines []map[string]interface{}
	for _, raw := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		var m map[string]interface{}
		require.NoError(t, sonic.Unmarshal(raw, &m))
		lines = append(lines, m)
	}
	return lines
}

// TestVertexConvertRequestsToJSONL verifies inline batch requests are converted into Vertex's
// native {"request": {contents...}} shape (via the shared Gemini converter), and that each
// custom_id is carried in the request labels for round-trip correlation.
func TestVertexConvertRequestsToJSONL(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	t.Run("ConvertsOpenAIMessagesAndCarriesCustomIDLabel", func(t *testing.T) {
		requests := []schemas.BatchRequestItem{
			{
				CustomID: "req-1",
				Body: map[string]interface{}{
					"messages": []interface{}{
						map[string]interface{}{"role": "user", "content": "Hello"},
					},
				},
			},
		}

		data, err := vertexConvertRequestsToJSONL(ctx, requests, "gemini-1.5-flash")
		require.NoError(t, err)

		lines := parseVertexJSONLLines(t, data)
		require.Len(t, lines, 1)

		request, ok := lines[0]["request"].(map[string]interface{})
		require.True(t, ok, "each line must be wrapped in a top-level \"request\" key")

		// OpenAI "messages" must have become Gemini "contents", not passed through verbatim.
		assert.NotContains(t, request, "messages")
		contents, ok := request["contents"].([]interface{})
		require.True(t, ok, "converted request must contain Gemini \"contents\"")
		require.Len(t, contents, 1)

		labels, ok := request["labels"].(map[string]interface{})
		require.True(t, ok, "custom_id must be carried in request labels")
		assert.Equal(t, "req-1", labels[vertexBatchCustomIDLabel])
	})

	t.Run("OmitsLabelsWhenNoCustomID", func(t *testing.T) {
		requests := []schemas.BatchRequestItem{
			{
				Body: map[string]interface{}{
					"messages": []interface{}{
						map[string]interface{}{"role": "user", "content": "Hi"},
					},
				},
			},
		}

		data, err := vertexConvertRequestsToJSONL(ctx, requests, "gemini-1.5-flash")
		require.NoError(t, err)

		lines := parseVertexJSONLLines(t, data)
		require.Len(t, lines, 1)
		request := lines[0]["request"].(map[string]interface{})
		assert.NotContains(t, request, "labels")
	})

	t.Run("PassesThroughNativeBodyVerbatim", func(t *testing.T) {
		requests := []schemas.BatchRequestItem{
			{
				CustomID: "req-native",
				Body: map[string]interface{}{
					"contents": []interface{}{
						map[string]interface{}{
							"role":  "user",
							"parts": []interface{}{map[string]interface{}{"text": "Native"}},
						},
					},
					"tools":         []interface{}{map[string]interface{}{"googleSearch": map[string]interface{}{}}},
					"cachedContent": "cachedContents/abc",
					"labels":        map[string]interface{}{"team": "research"},
				},
			},
		}

		data, err := vertexConvertRequestsToJSONL(ctx, requests, "gemini-1.5-flash")
		require.NoError(t, err)

		lines := parseVertexJSONLLines(t, data)
		require.Len(t, lines, 1)
		request := lines[0]["request"].(map[string]interface{})

		// Native fields outside the Gemini batch struct must survive the conversion.
		assert.Contains(t, request, "tools")
		assert.Equal(t, "cachedContents/abc", request["cachedContent"])
		require.Contains(t, request, "contents")

		// Caller labels are preserved and the custom_id label is merged in.
		labels := request["labels"].(map[string]interface{})
		assert.Equal(t, "research", labels["team"])
		assert.Equal(t, "req-native", labels[vertexBatchCustomIDLabel])
	})

	t.Run("FallsBackToParamsWhenBodyNil", func(t *testing.T) {
		requests := []schemas.BatchRequestItem{
			{
				CustomID: "req-params",
				Params: map[string]interface{}{
					"contents": []interface{}{
						map[string]interface{}{
							"role":  "user",
							"parts": []interface{}{map[string]interface{}{"text": "Native"}},
						},
					},
				},
			},
		}

		data, err := vertexConvertRequestsToJSONL(ctx, requests, "gemini-1.5-flash")
		require.NoError(t, err)

		lines := parseVertexJSONLLines(t, data)
		require.Len(t, lines, 1)
		request := lines[0]["request"].(map[string]interface{})
		contents, ok := request["contents"].([]interface{})
		require.True(t, ok)
		require.Len(t, contents, 1)
	})

	t.Run("EmitsOneLinePerRequest", func(t *testing.T) {
		requests := []schemas.BatchRequestItem{
			{CustomID: "a", Body: map[string]interface{}{"messages": []interface{}{map[string]interface{}{"role": "user", "content": "1"}}}},
			{CustomID: "b", Body: map[string]interface{}{"messages": []interface{}{map[string]interface{}{"role": "user", "content": "2"}}}},
		}

		data, err := vertexConvertRequestsToJSONL(ctx, requests, "gemini-1.5-flash")
		require.NoError(t, err)

		lines := parseVertexJSONLLines(t, data)
		require.Len(t, lines, 2)
		assert.Equal(t, "a", lines[0]["request"].(map[string]interface{})["labels"].(map[string]interface{})[vertexBatchCustomIDLabel])
		assert.Equal(t, "b", lines[1]["request"].(map[string]interface{})["labels"].(map[string]interface{})[vertexBatchCustomIDLabel])
	})

	t.Run("ErrorsWhenItemHasNoBody", func(t *testing.T) {
		requests := []schemas.BatchRequestItem{{CustomID: "empty"}}
		_, err := vertexConvertRequestsToJSONL(ctx, requests, "gemini-1.5-flash")
		require.Error(t, err)
	})

	t.Run("AnthropicConvertsOpenAIBodyAndUsesNativeCustomID", func(t *testing.T) {
		requests := []schemas.BatchRequestItem{
			{
				CustomID: "req-1",
				Body: map[string]interface{}{
					// A system-role message marks this as an OpenAI body needing conversion.
					"messages": []interface{}{
						map[string]interface{}{"role": "system", "content": "You are helpful"},
						map[string]interface{}{"role": "user", "content": "Hello"},
					},
					"max_tokens": 50,
				},
			},
		}

		data, err := vertexConvertRequestsToJSONL(ctx, requests, "claude-3-5-haiku")
		require.NoError(t, err)

		lines := parseVertexJSONLLines(t, data)
		require.Len(t, lines, 1)

		// custom_id is a native top-level field for Claude batch, not Gemini labels.
		assert.Equal(t, "req-1", lines[0]["custom_id"])

		request, ok := lines[0]["request"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "vertex-2023-10-16", request["anthropic_version"])
		assert.Contains(t, request, "messages")
		assert.NotContains(t, request, "labels")
		assert.NotContains(t, request, "model") // model lives at the job level
	})

	t.Run("AnthropicPassesThroughNativeBodyAndDropsModel", func(t *testing.T) {
		requests := []schemas.BatchRequestItem{
			{
				CustomID: "req-native",
				Body: map[string]interface{}{
					"messages":   []interface{}{map[string]interface{}{"role": "user", "content": "Hi"}},
					"system":     "be brief",
					"max_tokens": 64,
					"model":      "claude-3-5-haiku",
				},
			},
		}

		data, err := vertexConvertRequestsToJSONL(ctx, requests, "claude-3-5-haiku")
		require.NoError(t, err)

		lines := parseVertexJSONLLines(t, data)
		require.Len(t, lines, 1)
		assert.Equal(t, "req-native", lines[0]["custom_id"])

		request := lines[0]["request"].(map[string]interface{})
		assert.Equal(t, "vertex-2023-10-16", request["anthropic_version"])
		assert.Equal(t, "be brief", request["system"])
		assert.EqualValues(t, 64, request["max_tokens"])
		assert.NotContains(t, request, "model")
	})

	t.Run("AnthropicPreservesExistingAnthropicVersion", func(t *testing.T) {
		requests := []schemas.BatchRequestItem{
			{
				CustomID: "req-ver",
				Body: map[string]interface{}{
					"messages":          []interface{}{map[string]interface{}{"role": "user", "content": "Hi"}},
					"max_tokens":        16,
					"anthropic_version": "vertex-2023-10-16",
				},
			},
		}

		data, err := vertexConvertRequestsToJSONL(ctx, requests, "claude-3-5-haiku")
		require.NoError(t, err)

		lines := parseVertexJSONLLines(t, data)
		request := lines[0]["request"].(map[string]interface{})
		assert.Equal(t, "vertex-2023-10-16", request["anthropic_version"])
	})
}

// TestToVertexBatchCreateRequestPublisher verifies the batch job model is resolved to the
// correct publisher path per model family, and that already-qualified paths pass through.
func TestToVertexBatchCreateRequestPublisher(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	cases := []struct {
		name     string
		model    string
		expected string
	}{
		{"AnthropicBareName", "claude-3-5-haiku", "publishers/anthropic/models/claude-3-5-haiku"},
		{"GeminiBareName", "gemini-1.5-flash", "publishers/google/models/gemini-1.5-flash"},
		{"AlreadyQualifiedPassesThrough", "publishers/anthropic/models/claude-3-5-haiku", "publishers/anthropic/models/claude-3-5-haiku"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := &schemas.BifrostBatchCreateRequest{Model: schemas.Ptr(tc.model)}
			out := ToVertexBatchCreateRequest(ctx, req, "job", "gs://in/input.jsonl", "gs://out/")
			assert.Equal(t, tc.expected, out.Model)
		})
	}
}

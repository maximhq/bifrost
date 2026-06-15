package vertex

import (
	"bytes"
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

		data, err := vertexConvertRequestsToJSONL(requests)
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

		data, err := vertexConvertRequestsToJSONL(requests)
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

		data, err := vertexConvertRequestsToJSONL(requests)
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

		data, err := vertexConvertRequestsToJSONL(requests)
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

		data, err := vertexConvertRequestsToJSONL(requests)
		require.NoError(t, err)

		lines := parseVertexJSONLLines(t, data)
		require.Len(t, lines, 2)
		assert.Equal(t, "a", lines[0]["request"].(map[string]interface{})["labels"].(map[string]interface{})[vertexBatchCustomIDLabel])
		assert.Equal(t, "b", lines[1]["request"].(map[string]interface{})["labels"].(map[string]interface{})[vertexBatchCustomIDLabel])
	})

	t.Run("ErrorsWhenItemHasNoBody", func(t *testing.T) {
		requests := []schemas.BatchRequestItem{{CustomID: "empty"}}
		_, err := vertexConvertRequestsToJSONL(requests)
		require.Error(t, err)
	})
}

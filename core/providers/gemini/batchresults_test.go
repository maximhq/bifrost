package gemini

import (
	"testing"

	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGeminiBatchOutput locks in the fix for issue #3951: the generativelanguage batch
// REST API reports output under the Operation's `response` field (mirrored in
// `metadata.output`), never under `dest`. Inline responses are nested one level deep
// (response.inlinedResponses.inlinedResponses). geminiBatchOutput must read the real
// fields so completed results are no longer silently dropped.
func TestGeminiBatchOutput(t *testing.T) {
	t.Run("InlineUnderResponse", func(t *testing.T) {
		raw := `{
  "name": "batches/abc123",
  "metadata": {
    "@type": "type.googleapis.com/google.ai.generativelanguage.v1beta.GenerateContentBatch",
    "name": "batches/abc123",
    "state": "BATCH_STATE_SUCCEEDED",
    "batchStats": {"requestCount": "2", "successfulRequestCount": "2", "pendingRequestCount": "0"}
  },
  "done": true,
  "response": {
    "@type": "type.googleapis.com/google.ai.generativelanguage.v1beta.GenerateContentBatchOutput",
    "inlinedResponses": {
      "inlinedResponses": [
        {"metadata": {"key": "req-1"}, "response": {"candidates": [{"content": {"parts": [{"text": "4"}]}, "finishReason": "STOP"}], "usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 1, "totalTokenCount": 6}}},
        {"metadata": {"key": "req-2"}, "response": {"candidates": [{"content": {"parts": [{"text": "Paris"}]}, "finishReason": "STOP"}], "usageMetadata": {"promptTokenCount": 4, "candidatesTokenCount": 1, "totalTokenCount": 5}}}
      ]
    }
  }
}`
		var resp GeminiBatchJobResponse
		require.NoError(t, sonic.Unmarshal([]byte(raw), &resp))

		fileName, inlined := geminiBatchOutput(&resp)
		assert.Empty(t, fileName)
		require.Len(t, inlined, 2)

		require.NotNil(t, inlined[0].Metadata)
		assert.Equal(t, "req-1", inlined[0].Metadata.Key)
		require.NotNil(t, inlined[0].Response)
		require.Len(t, inlined[0].Response.Candidates, 1)
		require.NotNil(t, inlined[0].Response.Candidates[0].Content)
		require.Len(t, inlined[0].Response.Candidates[0].Content.Parts, 1)
		assert.Equal(t, "4", inlined[0].Response.Candidates[0].Content.Parts[0].Text)

		require.NotNil(t, inlined[1].Metadata)
		assert.Equal(t, "req-2", inlined[1].Metadata.Key)
	})

	t.Run("FileUnderResponse", func(t *testing.T) {
		raw := `{
  "name": "batches/file1",
  "metadata": {"name": "batches/file1", "state": "BATCH_STATE_SUCCEEDED", "batchStats": {"requestCount": "10", "successfulRequestCount": "10", "pendingRequestCount": "0"}},
  "done": true,
  "response": {"@type": "type.googleapis.com/google.ai.generativelanguage.v1beta.GenerateContentBatchOutput", "responsesFile": "files/batch-out-1"}
}`
		var resp GeminiBatchJobResponse
		require.NoError(t, sonic.Unmarshal([]byte(raw), &resp))

		fileName, inlined := geminiBatchOutput(&resp)
		assert.Equal(t, "files/batch-out-1", fileName)
		assert.Empty(t, inlined)
	})

	t.Run("MetadataOutputFallback", func(t *testing.T) {
		// No top-level response; output only present under metadata.output.
		raw := `{
  "name": "batches/meta1",
  "metadata": {
    "name": "batches/meta1",
    "state": "BATCH_STATE_SUCCEEDED",
    "batchStats": {"requestCount": "1", "successfulRequestCount": "1", "pendingRequestCount": "0"},
    "output": {"inlinedResponses": {"inlinedResponses": [{"metadata": {"key": "only-1"}, "response": {"candidates": [{"content": {"parts": [{"text": "hey"}]}, "finishReason": "STOP"}]}}]}}
  },
  "done": true
}`
		var resp GeminiBatchJobResponse
		require.NoError(t, sonic.Unmarshal([]byte(raw), &resp))

		fileName, inlined := geminiBatchOutput(&resp)
		assert.Empty(t, fileName)
		require.Len(t, inlined, 1)
		require.NotNil(t, inlined[0].Metadata)
		assert.Equal(t, "only-1", inlined[0].Metadata.Key)
	})

	t.Run("IgnoresDestField", func(t *testing.T) {
		// The legacy dest field must never be read: only response/metadata.output count.
		resp := &GeminiBatchJobResponse{
			Name: "batches/dest1",
			Dest: &GeminiBatchDest{FileName: "files/should-be-ignored"},
		}
		fileName, inlined := geminiBatchOutput(resp)
		assert.Empty(t, fileName)
		assert.Empty(t, inlined)
	})

	t.Run("NilResponse", func(t *testing.T) {
		fileName, inlined := geminiBatchOutput(nil)
		assert.Empty(t, fileName)
		assert.Empty(t, inlined)
	})
}

// TestGeminiInlineResponseToBatchResultItem verifies the per-response conversion used by
// the batch results path for inline batches.
func TestGeminiInlineResponseToBatchResultItem(t *testing.T) {
	t.Run("SuccessWithMetadataKey", func(t *testing.T) {
		inline := GeminiInlinedResponse{
			Metadata: &GeminiBatchMetadata{Key: "row-9"},
			Response: &GenerateContentResponse{
				Candidates: []*Candidate{{
					Content:      &Content{Parts: []*Part{{Text: "hi"}}},
					FinishReason: FinishReasonStop,
				}},
				UsageMetadata: &GenerateContentResponseUsageMetadata{
					PromptTokenCount:     2,
					CandidatesTokenCount: 3,
					TotalTokenCount:      5,
				},
			},
		}

		item := geminiInlineResponseToBatchResultItem(inline, "request-0")
		assert.Equal(t, "row-9", item.CustomID)
		assert.Nil(t, item.Error)
		require.NotNil(t, item.Response)
		assert.Equal(t, 200, item.Response.StatusCode)
		assert.Equal(t, "hi", item.Response.Body["text"])
		assert.Equal(t, "STOP", item.Response.Body["finish_reason"])

		usage, ok := item.Response.Body["usage"].(map[string]interface{})
		require.True(t, ok)
		assert.EqualValues(t, 2, usage["prompt_tokens"])
		assert.EqualValues(t, 3, usage["completion_tokens"])
		assert.EqualValues(t, 5, usage["total_tokens"])
	})

	t.Run("ErrorUsesFallbackID", func(t *testing.T) {
		inline := GeminiInlinedResponse{
			Error: &GeminiBatchErrorInfo{Code: 429, Message: "rate limited"},
		}

		item := geminiInlineResponseToBatchResultItem(inline, "request-3")
		assert.Equal(t, "request-3", item.CustomID)
		assert.Nil(t, item.Response)
		require.NotNil(t, item.Error)
		assert.Equal(t, "429", item.Error.Code)
		assert.Equal(t, "rate limited", item.Error.Message)
	})
}

// TestGeminiGenerateContentToBatchResultBody verifies the flattening of a Gemini
// GenerateContentResponse into the compact batch result body shared by inline and
// file-based result paths.
func TestGeminiGenerateContentToBatchResultBody(t *testing.T) {
	t.Run("TextAndUsage", func(t *testing.T) {
		resp := &GenerateContentResponse{
			Candidates: []*Candidate{{
				Content:      &Content{Parts: []*Part{{Text: "foo"}, {Text: "bar"}}},
				FinishReason: FinishReasonStop,
			}},
			UsageMetadata: &GenerateContentResponseUsageMetadata{
				PromptTokenCount:     1,
				CandidatesTokenCount: 2,
				TotalTokenCount:      3,
			},
		}

		body := geminiGenerateContentToBatchResultBody(resp)
		assert.Equal(t, "foobar", body["text"])
		assert.Equal(t, "STOP", body["finish_reason"])
		_, ok := body["usage"].(map[string]interface{})
		assert.True(t, ok)
	})

	t.Run("NoUsageNoText", func(t *testing.T) {
		resp := &GenerateContentResponse{
			Candidates: []*Candidate{{FinishReason: FinishReasonMaxTokens}},
		}

		body := geminiGenerateContentToBatchResultBody(resp)
		assert.Equal(t, "MAX_TOKENS", body["finish_reason"])
		_, hasText := body["text"]
		assert.False(t, hasText)
		_, hasUsage := body["usage"]
		assert.False(t, hasUsage)
	})
}

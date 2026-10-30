package gemini

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

func TestGeminiFileResultToBatchResultItem(t *testing.T) {
	t.Run("NativeGenerateContentResponse", func(t *testing.T) {
		var line GeminiBatchFileResultLine
		require.NoError(t, sonic.Unmarshal([]byte(`{"key":"request-1","response":{"candidates":[{"content":{"parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":2,"totalTokenCount":9}}}`), &line))

		item, err := geminiFileResultToBatchResultItem(line, "fallback")
		require.NoError(t, err)
		assert.Equal(t, "request-1", item.CustomID)
		require.NotNil(t, item.Response)
		assert.Equal(t, 200, item.Response.StatusCode)
		assert.Equal(t, "hello", item.Response.Body["text"])
		usage, ok := item.Response.Body["usage"].(map[string]interface{})
		require.True(t, ok)
		assert.EqualValues(t, 7, usage["prompt_tokens"])
		assert.EqualValues(t, 2, usage["completion_tokens"])
		assert.EqualValues(t, 9, usage["total_tokens"])
	})

	t.Run("CompatibilityEnvelope", func(t *testing.T) {
		var line GeminiBatchFileResultLine
		require.NoError(t, sonic.Unmarshal([]byte(`{"custom_id":"request-2","response":{"status_code":200,"body":{"model":"gemini-test","usage":{"promptTokens":3,"completionTokens":1,"totalTokens":4}}}}`), &line))

		item, err := geminiFileResultToBatchResultItem(line, "fallback")
		require.NoError(t, err)
		require.NotNil(t, item.Response)
		assert.Equal(t, 200, item.Response.StatusCode)
		usage, ok := item.Response.Body["usage"].(map[string]interface{})
		require.True(t, ok)
		assert.EqualValues(t, 3, usage["prompt_tokens"])
		assert.EqualValues(t, 1, usage["completion_tokens"])
		assert.EqualValues(t, 4, usage["total_tokens"])
	})

	t.Run("RejectsMissingResponseAndError", func(t *testing.T) {
		item, err := geminiFileResultToBatchResultItem(GeminiBatchFileResultLine{Key: "request-3"}, "fallback")
		require.Error(t, err)
		assert.Empty(t, item)
		assert.Contains(t, err.Error(), "neither response nor error")

		var nullResponse GeminiBatchFileResultLine
		require.NoError(t, sonic.Unmarshal([]byte(`{"key":"request-4","response":null}`), &nullResponse))
		item, err = geminiFileResultToBatchResultItem(nullResponse, "fallback")
		require.Error(t, err)
		assert.Empty(t, item)
	})
}

func TestParseGeminiBatchResultsJSONLUsesSourceLineIndex(t *testing.T) {
	body := []byte("{malformed}\n" +
		`{"response":{"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1,"totalTokenCount":3}}}` + "\n")

	results, parseErrors := parseGeminiBatchResultsJSONL(body, nil)
	require.Len(t, parseErrors, 1)
	require.Len(t, results, 1)
	assert.Equal(t, "request-1", results[0].CustomID)
}

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

// TestGeminiBatchResult_ThinkingTokens verifies that thinking tokens (thoughtsTokenCount)
// are added to completion_tokens and surfaced under completion_tokens_details.reasoning_tokens
// in both file-based and inline batch result processing.
func TestGeminiBatchResult_ThinkingTokens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                 string
		promptTokens         int32
		candidatesTokens     int32
		thoughtsTokens       int32
		totalTokens          int32
		cachedTokens         int32
		wantPromptTokens     int
		wantCompletionTokens int
		wantTotalTokens      int
		wantReasoningTokens  int
		wantCachedTokens     int
	}{
		{
			name:                 "WithThinkingTokens",
			promptTokens:         100,
			candidatesTokens:     50,
			thoughtsTokens:       400,
			totalTokens:          550,
			wantPromptTokens:     100,
			wantCompletionTokens: 450, // 50 candidates + 400 thoughts
			wantTotalTokens:      550,
			wantReasoningTokens:  400,
		},
		{
			name:                 "WithThinkingTokensAndCachedTokens",
			promptTokens:         120,
			candidatesTokens:     60,
			thoughtsTokens:       300,
			totalTokens:          480,
			cachedTokens:         40,
			wantPromptTokens:     120,
			wantCompletionTokens: 360, // 60 candidates + 300 thoughts
			wantTotalTokens:      480,
			wantReasoningTokens:  300,
			wantCachedTokens:     40,
		},
		{
			name:                 "ZeroThinkingTokensUnchanged",
			promptTokens:         10,
			candidatesTokens:     20,
			thoughtsTokens:       0,
			totalTokens:          30,
			wantPromptTokens:     10,
			wantCompletionTokens: 20,
			wantTotalTokens:      30,
			wantReasoningTokens:  0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resp := &GenerateContentResponse{
				Candidates: []*Candidate{{
					Content:      &Content{Parts: []*Part{{Text: "result"}}},
					FinishReason: FinishReasonStop,
				}},
				UsageMetadata: &GenerateContentResponseUsageMetadata{
					PromptTokenCount:        tt.promptTokens,
					CandidatesTokenCount:    tt.candidatesTokens,
					ThoughtsTokenCount:      tt.thoughtsTokens,
					TotalTokenCount:         tt.totalTokens,
					CachedContentTokenCount: tt.cachedTokens,
				},
			}

			// Test direct body builder
			body := geminiGenerateContentToBatchResultBody(resp)
			usage, ok := body["usage"].(map[string]interface{})
			require.True(t, ok)
			assert.EqualValues(t, tt.wantPromptTokens, usage["prompt_tokens"])
			assert.EqualValues(t, tt.wantCompletionTokens, usage["completion_tokens"])
			assert.EqualValues(t, tt.wantTotalTokens, usage["total_tokens"])

			if tt.wantReasoningTokens > 0 {
				details, ok := usage["completion_tokens_details"].(map[string]interface{})
				require.True(t, ok, "completion_tokens_details should be present")
				assert.EqualValues(t, tt.wantReasoningTokens, details["reasoning_tokens"])
			} else {
				assert.Nil(t, usage["completion_tokens_details"])
			}

			if tt.wantCachedTokens > 0 {
				details, ok := usage["prompt_tokens_details"].(map[string]interface{})
				require.True(t, ok, "prompt_tokens_details should be present")
				assert.EqualValues(t, tt.wantCachedTokens, details["cached_tokens"])
			}

			// Test inline batch response path
			inline := GeminiInlinedResponse{
				Metadata: &GeminiBatchMetadata{Key: "custom-key"},
				Response: resp,
			}
			inlineItem := geminiInlineResponseToBatchResultItem(inline, "fallback")
			require.NotNil(t, inlineItem.Response)
			inlineUsage, ok := inlineItem.Response.Body["usage"].(map[string]interface{})
			require.True(t, ok)
			assert.EqualValues(t, tt.wantCompletionTokens, inlineUsage["completion_tokens"])
		})
	}

	t.Run("NilUsageMetadata", func(t *testing.T) {
		resp := &GenerateContentResponse{
			Candidates: []*Candidate{{FinishReason: FinishReasonStop}},
		}
		body := geminiGenerateContentToBatchResultBody(resp)
		assert.Nil(t, body["usage"])
	})
}

// TestGeminiResourcePath verifies URI resource path formatting for Gemini models and batch operations.
func TestGeminiResourcePath(t *testing.T) {
	t.Parallel()

	valid := []struct{ id, collection, want string }{
		{"abc123", "batches", "batches/abc123"},
		{"batches/abc123", "batches", "batches/abc123"},
		{"files/abc", "files", "files/abc"},
		{"cachedContents/abc", "cachedContents", "cachedContents/abc"},
	}
	for _, tc := range valid {
		got, bifrostErr := geminiResourcePath(tc.id, tc.collection, "id")
		if bifrostErr != nil || got != tc.want {
			t.Fatalf("geminiResourcePath(%q) = %q, %v; want %q", tc.id, got, bifrostErr, tc.want)
		}
	}

	for _, id := range []string{"", "..", "../files/abc", "batches/../files/abc", "abc?x=1", "abc#", "abc%2Fdef", "files/abc"} {
		if got, bifrostErr := geminiResourcePath(id, "batches", "batch_id"); bifrostErr == nil {
			t.Fatalf("geminiResourcePath(%q) = %q, want error", id, got)
		}
	}
}

// A 200 that is not a batch object (no metadata) must become an error, not a nil dereference.
func TestGeminiBatchResponsesWithoutMetadataDoNotPanic(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/batches/no-metadata"):
			_, _ = w.Write([]byte(`{"files":[{"name":"files/abc"}]}`))
		case strings.HasSuffix(r.URL.Path, "/batches/no-stats"):
			_, _ = w.Write([]byte(`{"name":"batches/no-stats","metadata":{"name":"batches/no-stats","state":"BATCH_STATE_PENDING","createTime":"2026-07-01T00:00:00Z"}}`))
		default:
			_, _ = w.Write([]byte(`{"operations":[{"name":"batches/a"},{"name":"batches/b","metadata":{"state":"BATCH_STATE_SUCCEEDED","createTime":"2026-07-01T00:00:00Z"}}]}`))
		}
	}))
	defer ts.Close()

	provider := NewGeminiProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: ts.URL + "/v1beta"},
	}, testNoopLogger{})
	keys := []schemas.Key{{Value: *schemas.NewSecretVar("dummy-key")}}
	newCtx := func() *schemas.BifrostContext {
		return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	}

	_, bifrostErr := provider.BatchRetrieve(newCtx(), keys, &schemas.BifrostBatchRetrieveRequest{Provider: schemas.Gemini, BatchID: "no-metadata"})
	require.NotNil(t, bifrostErr)
	assert.Contains(t, bifrostErr.Error.Message, "missing metadata")

	_, bifrostErr = provider.BatchResults(newCtx(), keys, &schemas.BifrostBatchResultsRequest{Provider: schemas.Gemini, BatchID: "no-metadata"})
	require.NotNil(t, bifrostErr)
	assert.Contains(t, bifrostErr.Error.Message, "missing metadata")

	retrieved, bifrostErr := provider.BatchRetrieve(newCtx(), keys, &schemas.BifrostBatchRetrieveRequest{Provider: schemas.Gemini, BatchID: "no-stats"})
	require.Nil(t, bifrostErr)
	assert.Equal(t, "batches/no-stats", retrieved.ID)
	assert.Equal(t, 0, retrieved.RequestCounts.Total)

	listed, bifrostErr := provider.BatchList(newCtx(), keys, &schemas.BifrostBatchListRequest{Provider: schemas.Gemini})
	require.Nil(t, bifrostErr)
	require.Len(t, listed.Data, 2)
	assert.Equal(t, "batches/a", listed.Data[0].ID)
	assert.Equal(t, schemas.BatchStatusCompleted, listed.Data[1].Status)
}

// A raw GenAI batch carries generationConfig per inline request, and the model only in
// the URL. Each item's thinkingBudget has to be translated for gemini-4 before the batch
// is submitted, or Google rejects the whole job.
func TestGeminiBatchCreateRewritesInlineThinkingBudgetForGemini4(t *testing.T) {
	t.Parallel()

	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"batches/x","metadata":{"name":"batches/x","state":"BATCH_STATE_PENDING","createTime":"2026-07-01T00:00:00Z"}}`))
	}))
	defer ts.Close()

	provider := NewGeminiProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: ts.URL + "/v1beta"},
	}, testNoopLogger{})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
	raw := `{"batch":{"displayName":"argon","inputConfig":{"requests":{"requests":[` +
		`{"request":{"contents":[{"parts":[{"text":"a"}]}],"generationConfig":{"thinkingConfig":{"includeThoughts":true,"thinkingBudget":4096}}},"metadata":{"key":"1"}},` +
		`{"request":{"contents":[{"parts":[{"text":"b"}]}]},"metadata":{"key":"2"}}]}}}}`
	_, _ = provider.BatchCreate(ctx, schemas.Key{Value: *schemas.NewSecretVar("dummy-key")}, &schemas.BifrostBatchCreateRequest{
		Provider:       schemas.Gemini,
		Model:          schemas.Ptr("gemini-4-argon"),
		RawRequestBody: []byte(raw),
	})

	want := `{"batch":{"displayName":"argon","inputConfig":{"requests":{"requests":[` +
		`{"request":{"contents":[{"parts":[{"text":"a"}]}],"generationConfig":{"thinkingConfig":{"includeThoughts":true,"thinkingLevel":"medium"}}},"metadata":{"key":"1"}},` +
		`{"request":{"contents":[{"parts":[{"text":"b"}]}]},"metadata":{"key":"2"}}]}}}}`
	assert.Equal(t, want, string(gotBody))
}

const embedBatchCreateResponse = `{"name":"batches/x","metadata":{"name":"batches/x","state":"BATCH_STATE_PENDING","createTime":"2026-07-01T00:00:00Z","batchStats":{"requestCount":"2","pendingRequestCount":"2"}}}`

// newEmbedBatchTestProvider returns a provider pointed at a stub that records the path and body of the create call.
func newEmbedBatchTestProvider(t *testing.T) (*GeminiProvider, *string, *[]byte) {
	t.Helper()
	var gotPath string
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(embedBatchCreateResponse))
	}))
	t.Cleanup(ts.Close)
	provider := NewGeminiProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: ts.URL + "/v1beta"},
	}, testNoopLogger{})
	return provider, &gotPath, &gotBody
}

func TestGeminiBatchCreateEmbeddingsFileInput(t *testing.T) {
	t.Parallel()
	provider, gotPath, gotBody := newEmbedBatchTestProvider(t)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	resp, err := provider.BatchCreate(ctx, schemas.Key{Value: *schemas.NewSecretVar("dummy-key")}, &schemas.BifrostBatchCreateRequest{
		Provider:    schemas.Gemini,
		Model:       schemas.Ptr("gemini/gemini-embedding-2"),
		Endpoint:    schemas.BatchEndpointEmbeddings,
		InputFileID: "abc123",
		DisplayName: schemas.Ptr("titles"),
	})
	require.Nil(t, err)
	require.NotNil(t, resp)

	assert.Equal(t, "/v1beta/models/gemini-embedding-2:asyncBatchEmbedContent", *gotPath)
	assert.JSONEq(t, `{"batch":{"displayName":"titles","inputConfig":{"fileName":"files/abc123"}}}`, string(*gotBody))
	assert.Equal(t, "batches/x", resp.ID)
}

func TestGeminiBatchCreateEmbeddingsInlineRequests(t *testing.T) {
	t.Parallel()
	provider, gotPath, gotBody := newEmbedBatchTestProvider(t)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	_, err := provider.BatchCreate(ctx, schemas.Key{Value: *schemas.NewSecretVar("dummy-key")}, &schemas.BifrostBatchCreateRequest{
		Provider: schemas.Gemini,
		Model:    schemas.Ptr("gemini-embedding-2"),
		Endpoint: schemas.BatchEndpointEmbeddings,
		Requests: []schemas.BatchRequestItem{
			{CustomID: "p0", Body: map[string]interface{}{"input": "red shoes", "dimensions": float64(768)}},
			{CustomID: "p1", Body: map[string]interface{}{"input": []interface{}{"blue hat"}}},
		},
		DisplayName: schemas.Ptr("titles"),
	})
	require.Nil(t, err)

	assert.Equal(t, "/v1beta/models/gemini-embedding-2:asyncBatchEmbedContent", *gotPath)
	var sent struct {
		Batch struct {
			DisplayName string `json:"displayName"`
			InputConfig struct {
				Requests struct {
					Requests []struct {
						Request  GeminiEmbeddingRequest `json:"request"`
						Metadata struct {
							Key string `json:"key"`
						} `json:"metadata"`
					} `json:"requests"`
				} `json:"requests"`
			} `json:"inputConfig"`
		} `json:"batch"`
	}
	require.NoError(t, sonic.Unmarshal(*gotBody, &sent))
	assert.Equal(t, "titles", sent.Batch.DisplayName)
	items := sent.Batch.InputConfig.Requests.Requests
	require.Len(t, items, 2)
	assert.Equal(t, "models/gemini-embedding-2", items[0].Request.Model)
	assert.Equal(t, "models/gemini-embedding-2", items[1].Request.Model)
	assert.Equal(t, "p0", items[0].Metadata.Key)
	assert.Equal(t, "red shoes", items[0].Request.Content.Parts[0].Text)
	require.NotNil(t, items[0].Request.OutputDimensionality)
	assert.Equal(t, 768, *items[0].Request.OutputDimensionality)
	assert.Equal(t, "p1", items[1].Metadata.Key)
	assert.Equal(t, "blue hat", items[1].Request.Content.Parts[0].Text)
	assert.Nil(t, items[1].Request.OutputDimensionality)
}

func TestGeminiBatchCreateEmbeddingsRequiresModel(t *testing.T) {
	t.Parallel()
	provider, gotPath, _ := newEmbedBatchTestProvider(t)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	_, err := provider.BatchCreate(ctx, schemas.Key{Value: *schemas.NewSecretVar("dummy-key")}, &schemas.BifrostBatchCreateRequest{
		Provider:    schemas.Gemini,
		Endpoint:    schemas.BatchEndpointEmbeddings,
		InputFileID: "abc123",
	})
	require.NotNil(t, err)
	assert.Contains(t, err.Error.Message, "model is required")
	assert.Equal(t, http.StatusBadRequest, err.EffectiveHTTPStatus())
	assert.Empty(t, *gotPath, "no request should reach Gemini")
}

func TestGeminiBatchCreateChatStillUsesBatchGenerateContent(t *testing.T) {
	t.Parallel()
	provider, gotPath, _ := newEmbedBatchTestProvider(t)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	_, _ = provider.BatchCreate(ctx, schemas.Key{Value: *schemas.NewSecretVar("dummy-key")}, &schemas.BifrostBatchCreateRequest{
		Provider:    schemas.Gemini,
		Model:       schemas.Ptr("gemini-2.5-flash"),
		Endpoint:    schemas.BatchEndpointChatCompletions,
		InputFileID: "abc123",
	})
	assert.Equal(t, "/v1beta/models/gemini-2.5-flash:batchGenerateContent", *gotPath)
}

func TestGeminiEmbeddingBatchResults(t *testing.T) {
	t.Parallel()

	t.Run("FileLine", func(t *testing.T) {
		var line GeminiBatchFileResultLine
		require.NoError(t, sonic.Unmarshal([]byte(`{"key":"p0","response":{"embedding":{"values":[0.5,-0.25,1]}}}`), &line))
		item, err := geminiFileResultToBatchResultItem(line, "fallback")
		require.NoError(t, err)
		assert.Equal(t, "p0", item.CustomID)
		require.NotNil(t, item.Response)
		assert.Equal(t, 200, item.Response.StatusCode)
		assert.Equal(t, "list", item.Response.Body["object"])
		data, ok := item.Response.Body["data"].([]map[string]interface{})
		require.True(t, ok)
		require.Len(t, data, 1)
		assert.Equal(t, []float64{0.5, -0.25, 1}, data[0]["embedding"])
		assert.Equal(t, 0, data[0]["index"])
	})

	t.Run("FileLineWithoutResponseWrapper", func(t *testing.T) {
		var line GeminiBatchFileResultLine
		require.NoError(t, sonic.Unmarshal([]byte(`{"key":"p4","embedding":{"values":[2,3]},"usageMetadata":{"promptTokenCount":4}}`), &line))
		item, err := geminiFileResultToBatchResultItem(line, "fallback")
		require.NoError(t, err)
		assert.Equal(t, "p4", item.CustomID)
		require.NotNil(t, item.Response)
		data, ok := item.Response.Body["data"].([]map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, []float64{2, 3}, data[0]["embedding"])
		usage, ok := item.Response.Body["usage"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, 4, usage["prompt_tokens"])
	})

	t.Run("InlineResponse", func(t *testing.T) {
		var inline GeminiInlinedResponse
		require.NoError(t, sonic.Unmarshal([]byte(`{"metadata":{"key":"p1"},"response":{"embedding":{"values":[1,2]}}}`), &inline))
		require.NotNil(t, inline.Response)
		require.NotNil(t, inline.Response.Embedding)
		item := geminiInlineResponseToBatchResultItem(inline, "fallback")
		assert.Equal(t, "p1", item.CustomID)
		require.NotNil(t, item.Response)
		data, ok := item.Response.Body["data"].([]map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, []float64{1, 2}, data[0]["embedding"])
	})

	t.Run("InlineResponseKeepsUsage", func(t *testing.T) {
		var inline GeminiInlinedResponse
		require.NoError(t, sonic.Unmarshal([]byte(`{"metadata":{"key":"p3"},"response":{"embedding":{"values":[1]},"usageMetadata":{"promptTokenCount":5}}}`), &inline))
		item := geminiInlineResponseToBatchResultItem(inline, "fallback")
		require.NotNil(t, item.Response)
		usage, ok := item.Response.Body["usage"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, 5, usage["prompt_tokens"])
		assert.Equal(t, 5, usage["total_tokens"])
	})

	t.Run("InlineGenerateContentUnchanged", func(t *testing.T) {
		var inline GeminiInlinedResponse
		require.NoError(t, sonic.Unmarshal([]byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"hello"}]},"finishReason":"STOP"}]}}`), &inline))
		require.NotNil(t, inline.Response)
		assert.Nil(t, inline.Response.Embedding)
		item := geminiInlineResponseToBatchResultItem(inline, "r0")
		require.NotNil(t, item.Response)
		assert.Equal(t, "hello", item.Response.Body["text"])
	})

	t.Run("InlineError", func(t *testing.T) {
		var inline GeminiInlinedResponse
		require.NoError(t, sonic.Unmarshal([]byte(`{"metadata":{"key":"p2"},"error":{"code":400,"message":"bad"}}`), &inline))
		item := geminiInlineResponseToBatchResultItem(inline, "r0")
		require.NotNil(t, item.Error)
		assert.Equal(t, "400", item.Error.Code)
	})
}

func TestGeminiBatchResultsEndpoint(t *testing.T) {
	t.Parallel()
	embedding := schemas.BatchResultItem{CustomID: "p0", Response: &schemas.BatchResultResponse{
		StatusCode: 200,
		Body:       geminiEmbeddingToBatchResultBody(&GeminiEmbeddingResponse{Embedding: &GeminiEmbedding{Values: []float64{1}}}),
	}}
	chat := schemas.BatchResultItem{CustomID: "c0", Response: &schemas.BatchResultResponse{StatusCode: 200, Body: map[string]interface{}{"text": "hi"}}}

	assert.Equal(t, schemas.BatchEndpointEmbeddings, geminiBatchResultsEndpoint([]schemas.BatchResultItem{embedding}))
	assert.Equal(t, schemas.BatchEndpointChatCompletions, geminiBatchResultsEndpoint([]schemas.BatchResultItem{chat}))
	assert.Equal(t, schemas.BatchEndpointChatCompletions, geminiBatchResultsEndpoint(nil))
}

func BenchmarkGeminiInlinedResponseUnmarshal(b *testing.B) {
	chat := []byte(`{"metadata":{"key":"r0"},"response":{"candidates":[{"content":{"parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":2,"totalTokenCount":9}}}`)
	embed := []byte(`{"metadata":{"key":"p0"},"response":{"embedding":{"values":[0.1,0.2,0.3,0.4,0.5,0.6,0.7,0.8]}}}`)
	for name, payload := range map[string][]byte{"chat": chat, "embedding": embed} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				var inline GeminiInlinedResponse
				if err := sonic.Unmarshal(payload, &inline); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestGeminiBatchCreateEmbeddingsRejectsConflictingItemModel(t *testing.T) {
	t.Parallel()
	provider, gotPath, _ := newEmbedBatchTestProvider(t)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	_, err := provider.BatchCreate(ctx, schemas.Key{Value: *schemas.NewSecretVar("dummy-key")}, &schemas.BifrostBatchCreateRequest{
		Provider: schemas.Gemini,
		Model:    schemas.Ptr("gemini-embedding-2"),
		Endpoint: schemas.BatchEndpointEmbeddings,
		Requests: []schemas.BatchRequestItem{{CustomID: "p0", Body: map[string]interface{}{
			"model":   "models/gemini-embedding-001",
			"content": map[string]interface{}{"parts": []interface{}{map[string]interface{}{"text": "hi"}}},
		}}},
	})
	require.NotNil(t, err)
	assert.Contains(t, err.Error.Message, "does not match the batch model")
	assert.Empty(t, *gotPath, "no request should reach Gemini")
}

func TestGeminiEmbeddingBatchUsageFallsBackToPromptTokens(t *testing.T) {
	t.Parallel()
	body := geminiEmbeddingToBatchResultBody(&GeminiEmbeddingResponse{
		Embedding:     &GeminiEmbedding{Values: []float64{1}},
		UsageMetadata: &GenerateContentResponseUsageMetadata{PromptTokenCount: 12},
	})
	usage, ok := body["usage"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, 12, usage["prompt_tokens"])
	assert.Equal(t, 12, usage["total_tokens"])
}

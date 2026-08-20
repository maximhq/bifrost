package routing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testEmbeddingSemanticConfig() *complexity.SemanticConfig {
	return &complexity.SemanticConfig{
		Provider:       "openai",
		EmbeddingModel: "text-embedding-3-small",
		Timeout:        100 * time.Millisecond,
	}
}

func embeddingResponse(data schemas.EmbeddingStruct, totalTokens int) *schemas.BifrostEmbeddingResponse {
	return &schemas.BifrostEmbeddingResponse{
		Data:  []schemas.EmbeddingData{{Embedding: data}},
		Usage: &schemas.BifrostLLMUsage{TotalTokens: totalTokens},
	}
}

func TestGenerateEmbeddingDecodesAllEncodings(t *testing.T) {
	str := "[0.1,0.2]"
	tests := []struct {
		name string
		data schemas.EmbeddingStruct
		want []float32
	}{
		{name: "string encoded", data: schemas.EmbeddingStruct{EmbeddingStr: &str}, want: []float32{0.1, 0.2}},
		{name: "float64 array", data: schemas.EmbeddingStruct{EmbeddingArray: []float64{0.5, 1.5}}, want: []float32{0.5, 1.5}},
		{name: "2d array flattened", data: schemas.EmbeddingStruct{Embedding2DArray: [][]float64{{1, 2}, {3}}}, want: []float32{1, 2, 3}},
		{name: "int8 promoted", data: schemas.EmbeddingStruct{EmbeddingInt8Array: []int8{-1, 2}}, want: []float32{-1, 2}},
		{name: "int32 promoted", data: schemas.EmbeddingStruct{EmbeddingInt32Array: []int32{7, 8}}, want: []float32{7, 8}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plugin := &RoutingPlugin{}
			plugin.SetEmbeddingRequestExecutor(func(ctx *schemas.BifrostContext, req *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
				return embeddingResponse(tt.data, 42), nil
			})

			ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
			defer ctx.Cancel()
			vector, tokens, err := plugin.generateEmbedding(ctx, testEmbeddingSemanticConfig(), "hello", requestEmbeddingTimeout(testEmbeddingSemanticConfig()))
			require.NoError(t, err)
			assert.Equal(t, tt.want, vector)
			assert.Equal(t, 42, tokens)
		})
	}
}

func TestGenerateEmbeddingRequestShape(t *testing.T) {
	plugin := &RoutingPlugin{}
	var gotReq *schemas.BifrostEmbeddingRequest
	var gotSkip any
	var gotDeadline time.Time
	var hasDeadline bool
	plugin.SetEmbeddingRequestExecutor(func(ctx *schemas.BifrostContext, req *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		gotReq = req
		gotSkip = ctx.Value(schemas.BifrostContextKeySkipPluginPipeline)
		gotDeadline, hasDeadline = ctx.Deadline()
		return embeddingResponse(schemas.EmbeddingStruct{EmbeddingArray: []float64{1}}, 1), nil
	})

	// Give the caller a deadline far beyond the configured semantic timeout, so
	// an implementation that simply inherited the caller's budget would be
	// caught: the two are distinguishable only when they differ.
	cfg := testEmbeddingSemanticConfig()
	before := time.Now()
	callerDeadline := before.Add(50 * cfg.Timeout)
	ctx := schemas.NewBifrostContext(t.Context(), callerDeadline)
	defer ctx.Cancel()
	_, _, err := plugin.generateEmbedding(ctx, cfg, "classify me", requestEmbeddingTimeout(cfg))
	require.NoError(t, err)

	require.NotNil(t, gotReq)
	assert.Equal(t, schemas.ModelProvider("openai"), gotReq.Provider)
	assert.Equal(t, "text-embedding-3-small", gotReq.Model)
	require.NotNil(t, gotReq.Input)
	require.NotNil(t, gotReq.Input.Text)
	assert.Equal(t, "classify me", *gotReq.Input.Text)

	// The internal request must skip the plugin pipeline (anti-recursion) and
	// carry the configured hard timeout, not the caller's deadline.
	assert.Equal(t, true, gotSkip)
	require.True(t, hasDeadline, "embedding context must carry a deadline")
	assert.Greater(t, gotDeadline.Sub(before), time.Duration(0))
	assert.LessOrEqual(t, gotDeadline.Sub(before), cfg.Timeout+50*time.Millisecond,
		"embedding deadline must come from the configured semantic timeout")
	assert.True(t, gotDeadline.Before(callerDeadline),
		"embedding deadline must not inherit the caller's larger budget")
}

func TestGenerateEmbeddingsBatchesAndRestoresInputOrder(t *testing.T) {
	plugin := &RoutingPlugin{}
	plugin.SetEmbeddingRequestExecutor(func(_ *schemas.BifrostContext, req *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		require.NotNil(t, req.Input)
		assert.Nil(t, req.Input.Text)
		assert.Equal(t, []string{"first", "second"}, req.Input.Texts)
		return &schemas.BifrostEmbeddingResponse{
			Data: []schemas.EmbeddingData{
				{Index: 1, Embedding: schemas.EmbeddingStruct{EmbeddingArray: []float64{0, 1}}},
				{Index: 0, Embedding: schemas.EmbeddingStruct{EmbeddingArray: []float64{1, 0}}},
			},
			Usage: &schemas.BifrostLLMUsage{TotalTokens: 7},
		}, nil
	})

	ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
	defer ctx.Cancel()
	embeddings, tokens, err := plugin.generateEmbeddings(ctx, testEmbeddingSemanticConfig(), []string{"first", "second"}, requestEmbeddingTimeout(testEmbeddingSemanticConfig()))
	require.NoError(t, err)
	assert.Equal(t, [][]float32{{1, 0}, {0, 1}}, embeddings)
	assert.Equal(t, 7, tokens)
}

func TestGenerateEmbeddingsSignalsUnsupportedBatchShape(t *testing.T) {
	plugin := &RoutingPlugin{}
	plugin.SetEmbeddingRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		// This is the shape produced by a single-input-only model such as
		// Bedrock Titan when it receives multiple texts.
		return embeddingResponse(schemas.EmbeddingStruct{EmbeddingArray: []float64{1, 0}}, 3), nil
	})

	ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
	defer ctx.Cancel()
	_, _, err := plugin.generateEmbeddings(ctx, testEmbeddingSemanticConfig(), []string{"first", "second"}, requestEmbeddingTimeout(testEmbeddingSemanticConfig()))
	require.Error(t, err)
	assert.True(t, errors.Is(err, complexity.ErrBatchEmbeddingsUnsupported))
}

func TestGenerateEmbeddingTimeoutCancelsCall(t *testing.T) {
	plugin := &RoutingPlugin{}
	plugin.SetEmbeddingRequestExecutor(func(ctx *schemas.BifrostContext, req *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		// Simulate a slow provider: honor context cancellation like the real
		// client does.
		select {
		case <-ctx.Done():
			return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: ctx.Err().Error()}}
		case <-time.After(5 * time.Second):
			return embeddingResponse(schemas.EmbeddingStruct{EmbeddingArray: []float64{1}}, 1), nil
		}
	})

	cfg := testEmbeddingSemanticConfig()
	cfg.Timeout = 20 * time.Millisecond

	ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
	defer ctx.Cancel()
	start := time.Now()
	_, _, err := plugin.generateEmbedding(ctx, cfg, "slow", requestEmbeddingTimeout(cfg))
	require.Error(t, err)
	// Tolerant of scheduler jitter, but still tight enough that only the 20ms
	// configuration can satisfy it — a second-scale budget would not.
	assert.Less(t, time.Since(start), 500*time.Millisecond, "call must be bounded by the configured timeout")
	// A blown budget must stay distinguishable from a provider or config
	// failure: callers log the two differently, and a string-flattened
	// *BifrostError cannot be told apart after the fact.
	require.ErrorIs(t, err, ErrEmbeddingTimeout)
	assert.Contains(t, err.Error(), "20ms", "the timeout error should name the budget it exceeded")
}

// TestGenerateEmbeddingDistinguishesTimeoutFromOtherFailures guards the tag
// against the easy over-broad implementation: an ordinary provider error must
// not read as a timeout just because it arrived through the same executor.
func TestGenerateEmbeddingDistinguishesTimeoutFromOtherFailures(t *testing.T) {
	tests := []struct {
		name        string
		bifrostErr  *schemas.BifrostError
		wantTimeout bool
	}{
		{
			name:        "provider rejection",
			bifrostErr:  &schemas.BifrostError{Error: &schemas.ErrorField{Message: "invalid api key"}},
			wantTimeout: false,
		},
		{
			name:        "client-reported timeout",
			bifrostErr:  &schemas.BifrostError{Type: schemas.Ptr(schemas.RequestTimedOut), Error: &schemas.ErrorField{Message: "request timed out"}},
			wantTimeout: true,
		},
		{
			name:        "gateway timeout status",
			bifrostErr:  &schemas.BifrostError{StatusCode: schemas.Ptr(504), Error: &schemas.ErrorField{Message: "gateway timeout"}},
			wantTimeout: true,
		},
		{
			name:        "caller cancellation",
			bifrostErr:  &schemas.BifrostError{StatusCode: schemas.Ptr(499), Error: &schemas.ErrorField{Message: "request cancelled"}},
			wantTimeout: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plugin := &RoutingPlugin{}
			plugin.SetEmbeddingRequestExecutor(func(ctx *schemas.BifrostContext, req *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
				return nil, tt.bifrostErr
			})

			ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
			defer ctx.Cancel()
			_, _, err := plugin.generateEmbedding(ctx, testEmbeddingSemanticConfig(), "classify me", requestEmbeddingTimeout(testEmbeddingSemanticConfig()))
			require.Error(t, err)
			assert.Equal(t, tt.wantTimeout, errors.Is(err, ErrEmbeddingTimeout))
		})
	}
}

// TestWarmupEmbedsDoNotInheritTheRequestTimeout is a regression guard: warmup
// used to run through semantic.Timeout, the hot-path budget (1500ms by default).
// A 32-exemplar batch cannot finish in that window, so every warmup failed with
// a 504 and semantic routing silently served its fallback forever.
func TestWarmupEmbedsDoNotInheritTheRequestTimeout(t *testing.T) {
	semantic := testEmbeddingSemanticConfig()
	semantic.Timeout = 10 * time.Millisecond

	t.Run("batch warmup", func(t *testing.T) {
		plugin := &RoutingPlugin{}
		var deadline time.Time
		plugin.SetEmbeddingRequestExecutor(func(ctx *schemas.BifrostContext, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
			deadline, _ = ctx.Deadline()
			return &schemas.BifrostEmbeddingResponse{
				Data: []schemas.EmbeddingData{
					{Index: 0, Embedding: schemas.EmbeddingStruct{EmbeddingArray: []float64{1}}},
					{Index: 1, Embedding: schemas.EmbeddingStruct{EmbeddingArray: []float64{2}}},
				},
				Usage: &schemas.BifrostLLMUsage{TotalTokens: 2},
			}, nil
		})

		before := time.Now()
		_, err := plugin.embedComplexityTexts(context.Background(), semantic, []string{"a", "b"})
		require.NoError(t, err)
		assert.Greater(t, deadline.Sub(before), time.Second, "warmup batch must not run on the hot-path budget")
	})

	t.Run("single-input warmup fallback", func(t *testing.T) {
		plugin := &RoutingPlugin{}
		var deadline time.Time
		plugin.SetEmbeddingRequestExecutor(func(ctx *schemas.BifrostContext, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
			deadline, _ = ctx.Deadline()
			return embeddingResponse(schemas.EmbeddingStruct{EmbeddingArray: []float64{1}}, 1), nil
		})

		// A plain context is what the classifier passes during warmup.
		before := time.Now()
		_, err := plugin.embedComplexityText(context.Background(), semantic, "exemplar")
		require.NoError(t, err)
		assert.Greater(t, deadline.Sub(before), time.Second, "warmup fallback must not run on the hot-path budget")
	})

	t.Run("request classification still honors the configured budget", func(t *testing.T) {
		plugin := &RoutingPlugin{}
		var deadline time.Time
		plugin.SetEmbeddingRequestExecutor(func(ctx *schemas.BifrostContext, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
			deadline, _ = ctx.Deadline()
			return embeddingResponse(schemas.EmbeddingStruct{EmbeddingArray: []float64{1}}, 1), nil
		})

		requestCtx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
		defer requestCtx.Cancel()
		before := time.Now()
		_, err := plugin.embedComplexityText(requestCtx, semantic, "classify me")
		require.NoError(t, err)
		assert.LessOrEqual(t, deadline.Sub(before), 100*time.Millisecond, "a live request must stay on its configured budget")
	})
}

func TestGenerateEmbeddingGuards(t *testing.T) {
	okExecutor := func(ctx *schemas.BifrostContext, req *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		return embeddingResponse(schemas.EmbeddingStruct{EmbeddingArray: []float64{1}}, 1), nil
	}

	t.Run("nil executor", func(t *testing.T) {
		plugin := &RoutingPlugin{}
		ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
		defer ctx.Cancel()
		_, _, err := plugin.generateEmbedding(ctx, testEmbeddingSemanticConfig(), "x", requestEmbeddingTimeout(testEmbeddingSemanticConfig()))
		require.ErrorContains(t, err, "executor is not configured")
	})

	t.Run("nil semantic config", func(t *testing.T) {
		plugin := &RoutingPlugin{}
		plugin.SetEmbeddingRequestExecutor(okExecutor)
		ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
		defer ctx.Cancel()
		_, _, err := plugin.generateEmbedding(ctx, nil, "x", requestEmbeddingTimeout(testEmbeddingSemanticConfig()))
		require.ErrorContains(t, err, "not configured")
	})

	t.Run("empty response data", func(t *testing.T) {
		plugin := &RoutingPlugin{}
		plugin.SetEmbeddingRequestExecutor(func(ctx *schemas.BifrostContext, req *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
			return &schemas.BifrostEmbeddingResponse{}, nil
		})
		ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
		defer ctx.Cancel()
		_, _, err := plugin.generateEmbedding(ctx, testEmbeddingSemanticConfig(), "x", requestEmbeddingTimeout(testEmbeddingSemanticConfig()))
		require.ErrorContains(t, err, "no embeddings returned")
	})

	t.Run("unset executor after set", func(t *testing.T) {
		plugin := &RoutingPlugin{}
		plugin.SetEmbeddingRequestExecutor(okExecutor)
		plugin.SetEmbeddingRequestExecutor(nil)
		ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
		defer ctx.Cancel()
		_, _, err := plugin.generateEmbedding(ctx, testEmbeddingSemanticConfig(), "x", requestEmbeddingTimeout(testEmbeddingSemanticConfig()))
		require.ErrorContains(t, err, "executor is not configured")
	})
}

func TestEmbedComplexityTextRecordsRoutingUsage(t *testing.T) {
	plugin := &RoutingPlugin{}
	plugin.SetEmbeddingRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		return embeddingResponse(schemas.EmbeddingStruct{EmbeddingArray: []float64{1, 0}}, 42), nil
	})

	cfg := testEmbeddingSemanticConfig()
	cfg.CountTowardBudgets = true

	ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
	defer ctx.Cancel()
	_, err := plugin.embedComplexityText(ctx, cfg, "classify me")
	require.NoError(t, err)

	usage, ok := schemas.RoutingDebugFromContext(ctx)
	require.True(t, ok, "classification embed must record usage on the request context")
	require.Len(t, usage.Calls, 1)
	call := usage.Calls[0]
	assert.Equal(t, "openai", *call.ProviderUsed)
	assert.Equal(t, "text-embedding-3-small", *call.ModelUsed)
	assert.Equal(t, 42, *call.InputTokens)
	assert.True(t, call.CountTowardBudgets)
}

// Classification runs once per top-level request, so recordRoutingEmbedUsage
// is called at most once in practice. This pins what happens if a caller
// defensively records again anyway: it appends rather than merging or
// overwriting, so a hypothetical double-call double-bills a small embedding
// call instead of silently dropping one — the same safe-by-default choice
// AppendGuardrailJudgeCallOnContext makes for guardrail judge calls.
func TestRecordRoutingEmbedUsageAppendsRatherThanReplaces(t *testing.T) {
	ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
	defer ctx.Cancel()
	cfg := testEmbeddingSemanticConfig()

	recordRoutingEmbedUsage(ctx, cfg, 20)
	recordRoutingEmbedUsage(ctx, cfg, 7)

	usage, ok := schemas.RoutingDebugFromContext(ctx)
	require.True(t, ok)
	require.Len(t, usage.Calls, 2)
	assert.Equal(t, 20, *usage.Calls[0].InputTokens)
	assert.Equal(t, 7, *usage.Calls[1].InputTokens)
}

func TestRecordRoutingLLMUsageAppendsOwnCall(t *testing.T) {
	ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
	defer ctx.Cancel()
	cfg := &complexity.LLMConfig{
		Provider:           schemas.OpenAI,
		Model:              "gpt-4o-mini",
		CountTowardBudgets: true,
	}

	recordRoutingLLMUsage(ctx, cfg, 20, 4)

	usage, ok := schemas.RoutingDebugFromContext(ctx)
	require.True(t, ok)
	require.Len(t, usage.Calls, 1)
	call := usage.Calls[0]
	assert.Equal(t, "openai", *call.ProviderUsed)
	assert.Equal(t, "gpt-4o-mini", *call.ModelUsed)
	assert.Equal(t, 20, *call.InputTokens)
	require.NotNil(t, call.OutputTokens, "non-nil output tokens select chat pricing")
	assert.Equal(t, 4, *call.OutputTokens)
	assert.True(t, call.CountTowardBudgets)
}

// TestRecordRoutingUsageAppendsBothSemanticAndLLMCalls pins the fix for the
// bug where a request that classifies via semantic and then falls back to the
// llm classifier lost the embedding's usage: both calls must be observable
// afterward, not just the one that wrote last.
func TestRecordRoutingUsageAppendsBothSemanticAndLLMCalls(t *testing.T) {
	ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
	defer ctx.Cancel()
	semanticCfg := testEmbeddingSemanticConfig()
	semanticCfg.CountTowardBudgets = true
	llmCfg := &complexity.LLMConfig{
		Provider:           schemas.Anthropic,
		Model:              "claude-haiku-4-5",
		CountTowardBudgets: true,
	}

	recordRoutingEmbedUsage(ctx, semanticCfg, 12)
	recordRoutingLLMUsage(ctx, llmCfg, 40, 8)

	usage, ok := schemas.RoutingDebugFromContext(ctx)
	require.True(t, ok)
	require.Len(t, usage.Calls, 2, "both the embed and the llm classification must survive")
	assert.Equal(t, "openai", *usage.Calls[0].ProviderUsed)
	assert.Nil(t, usage.Calls[0].OutputTokens, "an embed call never carries output tokens")
	assert.Equal(t, "anthropic", *usage.Calls[1].ProviderUsed)
	require.NotNil(t, usage.Calls[1].OutputTokens)
	assert.Equal(t, 8, *usage.Calls[1].OutputTokens)
}

// A provider that reports a negative token count must not have it recorded:
// the stamp feeds cost calculation and warmup budget attribution, where a
// negative count would subtract from billed usage.
func TestEmbedComplexityTextDropsNegativeProviderUsage(t *testing.T) {
	plugin := &RoutingPlugin{}
	plugin.SetEmbeddingRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		return embeddingResponse(schemas.EmbeddingStruct{EmbeddingArray: []float64{1, 0}}, -42), nil
	})

	cfg := testEmbeddingSemanticConfig()
	cfg.CountTowardBudgets = true

	ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
	defer ctx.Cancel()
	_, err := plugin.embedComplexityText(ctx, cfg, "classify me")
	require.NoError(t, err)

	usage, ok := schemas.RoutingDebugFromContext(ctx)
	require.True(t, ok)
	require.Len(t, usage.Calls, 1)
	assert.Equal(t, 0, *usage.Calls[0].InputTokens, "negative provider usage must not reach budget accounting")
}

// warmupObservation captures one WarmupEmbedUsageObserver invocation.
type warmupObservation struct {
	Provider    string
	Model       string
	InputTokens int
}

func TestEmbedComplexityTextWarmupPathObservesInsteadOfRecording(t *testing.T) {
	plugin := &RoutingPlugin{}
	plugin.SetEmbeddingRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		return embeddingResponse(schemas.EmbeddingStruct{EmbeddingArray: []float64{1, 0}}, 42), nil
	})
	var observed []warmupObservation
	plugin.SetWarmupEmbedUsageObserver(func(provider, model string, inputTokens int) {
		observed = append(observed, warmupObservation{provider, model, inputTokens})
	})

	// Warmup's single-input fallback runs on plain background contexts, never a
	// *schemas.BifrostContext — its embeds go to the warmup observer, not to
	// request attribution.
	_, err := plugin.embedComplexityText(t.Context(), testEmbeddingSemanticConfig(), "warmup exemplar")
	require.NoError(t, err)
	require.Len(t, observed, 1)
	assert.Equal(t, warmupObservation{"openai", "text-embedding-3-small", 42}, observed[0])
}

func TestEmbedComplexityTextWarmupPathWithoutObserver(t *testing.T) {
	plugin := &RoutingPlugin{}
	plugin.SetEmbeddingRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		return embeddingResponse(schemas.EmbeddingStruct{EmbeddingArray: []float64{1, 0}}, 42), nil
	})

	// No observer wired (SDK usage, or before the server wires it): the warmup
	// path must still work and must not panic.
	_, err := plugin.embedComplexityText(t.Context(), testEmbeddingSemanticConfig(), "warmup exemplar")
	require.NoError(t, err)
}

func TestEmbedComplexityTextsObservesWarmupNeverRecordsRoutingUsage(t *testing.T) {
	plugin := &RoutingPlugin{}
	plugin.SetEmbeddingRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		return &schemas.BifrostEmbeddingResponse{
			Data: []schemas.EmbeddingData{
				{Index: 0, Embedding: schemas.EmbeddingStruct{EmbeddingArray: []float64{1, 0}}},
				{Index: 1, Embedding: schemas.EmbeddingStruct{EmbeddingArray: []float64{0, 1}}},
			},
			Usage: &schemas.BifrostLLMUsage{TotalTokens: 7},
		}, nil
	})
	var observed []warmupObservation
	plugin.SetWarmupEmbedUsageObserver(func(provider, model string, inputTokens int) {
		observed = append(observed, warmupObservation{provider, model, inputTokens})
	})

	// Batch embeds are warmup-only: even on a request context they observe as
	// warmup and never attribute usage — only per-request classification
	// embeds do.
	ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
	defer ctx.Cancel()
	_, err := plugin.embedComplexityTexts(ctx, testEmbeddingSemanticConfig(), []string{"a", "b"})
	require.NoError(t, err)
	_, hasRoutingDebug := schemas.RoutingDebugFromContext(ctx)
	assert.False(t, hasRoutingDebug)
	require.Len(t, observed, 1)
	assert.Equal(t, warmupObservation{"openai", "text-embedding-3-small", 7}, observed[0])
}

// TestEmbedComplexityTextsSettlesUsageOnUnsupportedBatch covers the shape a
// single-input-only model returns for a multi-text batch. Warmup recovers by
// re-embedding each phrase on its own, so this call's tokens are the ones that
// would silently vanish: the provider billed for them and the run still
// succeeds.
func TestEmbedComplexityTextsSettlesUsageOnUnsupportedBatch(t *testing.T) {
	plugin := &RoutingPlugin{}
	plugin.SetEmbeddingRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		return embeddingResponse(schemas.EmbeddingStruct{EmbeddingArray: []float64{1, 0}}, 5), nil
	})
	var observed []warmupObservation
	plugin.SetWarmupEmbedUsageObserver(func(provider, model string, inputTokens int) {
		observed = append(observed, warmupObservation{provider, model, inputTokens})
	})

	_, err := plugin.embedComplexityTexts(t.Context(), testEmbeddingSemanticConfig(), []string{"a", "b"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, complexity.ErrBatchEmbeddingsUnsupported))
	require.Len(t, observed, 1)
	assert.Equal(t, warmupObservation{"openai", "text-embedding-3-small", 5}, observed[0])
}

// A call that never reached the provider consumed nothing, so it must not reach
// the observer at all — that would inflate the warmup embedding call counter
// with attempts the provider never saw.
func TestEmbedComplexityTextsSkipsUsageWhenTheCallNeverReachedTheProvider(t *testing.T) {
	plugin := &RoutingPlugin{}
	var observed []warmupObservation
	plugin.SetWarmupEmbedUsageObserver(func(provider, model string, inputTokens int) {
		observed = append(observed, warmupObservation{provider, model, inputTokens})
	})

	_, err := plugin.embedComplexityTexts(t.Context(), testEmbeddingSemanticConfig(), []string{"a", "b"})
	require.ErrorIs(t, err, ErrEmbeddingRequestExecutorNotConfigured)
	assert.Empty(t, observed)
}

func TestRequestClassificationEmbedDoesNotObserveWarmup(t *testing.T) {
	plugin := &RoutingPlugin{}
	plugin.SetEmbeddingRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		return embeddingResponse(schemas.EmbeddingStruct{EmbeddingArray: []float64{1, 0}}, 42), nil
	})
	var observed []warmupObservation
	plugin.SetWarmupEmbedUsageObserver(func(provider, model string, inputTokens int) {
		observed = append(observed, warmupObservation{provider, model, inputTokens})
	})

	// A classification embed on a request context records usage for the
	// RoutingDebug stamp; it must NOT also fire the warmup observer, or the
	// request phase would double-count in telemetry.
	ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
	defer ctx.Cancel()
	_, err := plugin.embedComplexityText(ctx, testEmbeddingSemanticConfig(), "classify me")
	require.NoError(t, err)
	_, hasRoutingDebug := schemas.RoutingDebugFromContext(ctx)
	assert.True(t, hasRoutingDebug)
	assert.Empty(t, observed)
}

// TestRequestClassificationEmbedRecordsUsageWhenProviderReturnsNoVectors keeps
// the request side consistent: a classification that failed on response shape
// still cost the caller tokens, so it reaches the RoutingDebug stamp rather
// than disappearing because no tier came back.
func TestRequestClassificationEmbedRecordsUsageWhenProviderReturnsNoVectors(t *testing.T) {
	plugin := &RoutingPlugin{}
	plugin.SetEmbeddingRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		return &schemas.BifrostEmbeddingResponse{
			Data:  nil,
			Usage: &schemas.BifrostLLMUsage{TotalTokens: 42},
		}, nil
	})
	var observed []warmupObservation
	plugin.SetWarmupEmbedUsageObserver(func(provider, model string, inputTokens int) {
		observed = append(observed, warmupObservation{provider, model, inputTokens})
	})

	ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
	defer ctx.Cancel()
	_, err := plugin.embedComplexityText(ctx, testEmbeddingSemanticConfig(), "classify me")
	require.Error(t, err)

	usage, ok := schemas.RoutingDebugFromContext(ctx)
	require.True(t, ok, "a billed classification embed must be recorded for the stamp")
	require.Len(t, usage.Calls, 1)
	assert.Equal(t, 42, *usage.Calls[0].InputTokens)
	assert.Empty(t, observed, "a request embed must never fire the warmup observer")
}

// TestEmbedComplexityTextSkipsUsageBeforeProviderResponds keeps the two tests
// above from becoming "settle on every failure": a call that never reached the
// provider has nothing to bill.
func TestEmbedComplexityTextSkipsUsageBeforeProviderResponds(t *testing.T) {
	plugin := &RoutingPlugin{}
	var observed []warmupObservation
	plugin.SetWarmupEmbedUsageObserver(func(provider, model string, inputTokens int) {
		observed = append(observed, warmupObservation{provider, model, inputTokens})
	})

	_, err := plugin.embedComplexityText(t.Context(), testEmbeddingSemanticConfig(), "warm me")
	require.ErrorIs(t, err, ErrEmbeddingRequestExecutorNotConfigured)
	assert.Empty(t, observed)
}

func TestStampRoutingDebug(t *testing.T) {
	newCtxWithUsage := func(t *testing.T, countTowardBudgets bool) *schemas.BifrostContext {
		t.Helper()
		ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
		t.Cleanup(ctx.Cancel)
		provider, model, inputTokens := "openai", "text-embedding-3-small", 42
		require.True(t, schemas.AppendRoutingCallOnContext(ctx, schemas.BifrostRoutingCall{
			ProviderUsed:       &provider,
			ModelUsed:          &model,
			InputTokens:        &inputTokens,
			CountTowardBudgets: countTowardBudgets,
		}))
		return ctx
	}
	newChatResult := func() *schemas.BifrostResponse {
		return &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{}}
	}

	t.Run("stamps regardless of budget flag", func(t *testing.T) {
		for _, flag := range []bool{false, true} {
			result := newChatResult()
			stampRoutingDebug(newCtxWithUsage(t, flag), result, schemas.ChatCompletionRequest, false)

			rd := result.GetExtraFields().RoutingDebug
			require.NotNil(t, rd, "routing debug must be stamped whenever a routing embed ran (flag=%v)", flag)
			require.Len(t, rd.Calls, 1)
			call := rd.Calls[0]
			require.NotNil(t, call.ProviderUsed)
			assert.Equal(t, "openai", *call.ProviderUsed)
			require.NotNil(t, call.ModelUsed)
			assert.Equal(t, "text-embedding-3-small", *call.ModelUsed)
			require.NotNil(t, call.InputTokens)
			assert.Equal(t, 42, *call.InputTokens)
			assert.Equal(t, flag, call.CountTowardBudgets)
		}
	})

	t.Run("stream stamps only the final chunk", func(t *testing.T) {
		ctx := newCtxWithUsage(t, false)

		intermediate := newChatResult()
		stampRoutingDebug(ctx, intermediate, schemas.ChatCompletionStreamRequest, false)
		assert.Nil(t, intermediate.GetExtraFields().RoutingDebug)

		final := newChatResult()
		stampRoutingDebug(ctx, final, schemas.ChatCompletionStreamRequest, true)
		assert.NotNil(t, final.GetExtraFields().RoutingDebug)
		_, usageRemainsAvailable := schemas.RoutingDebugFromContext(ctx)
		assert.True(t, usageRemainsAvailable, "no-response consumers read the context snapshot")
	})

	t.Run("no usage recorded means no stamp", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
		defer ctx.Cancel()
		result := newChatResult()
		stampRoutingDebug(ctx, result, schemas.ChatCompletionRequest, false)
		assert.Nil(t, result.GetExtraFields().RoutingDebug)
	})

	t.Run("nil result leaves usage pending", func(t *testing.T) {
		ctx := newCtxWithUsage(t, true)
		stampRoutingDebug(ctx, nil, schemas.ChatCompletionRequest, false)
		_, usageStillPending := schemas.RoutingDebugFromContext(ctx)
		assert.True(t, usageStillPending)
	})

	t.Run("retry and fallback attempts do not restamp usage", func(t *testing.T) {
		for _, attempt := range []struct {
			name          string
			retryNumber   int
			fallbackIndex int
		}{
			{name: "retry", retryNumber: 1},
			{name: "fallback", fallbackIndex: 1},
		} {
			t.Run(attempt.name, func(t *testing.T) {
				ctx := newCtxWithUsage(t, true)
				ctx.SetValue(schemas.BifrostContextKeyNumberOfRetries, attempt.retryNumber)
				ctx.SetValue(schemas.BifrostContextKeyFallbackIndex, attempt.fallbackIndex)
				result := newChatResult()

				stampRoutingDebug(ctx, result, schemas.ChatCompletionRequest, false)

				assert.Nil(t, result.GetExtraFields().RoutingDebug)
			})
		}
	})

	// The stamp feeds cost calculation, so a negative token count must never
	// leave this function — it would price to a negative routing charge and
	// subtract from the request's budget attribution.
	t.Run("negative input tokens are rejected", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
		defer ctx.Cancel()
		recordRoutingEmbedUsage(ctx, testEmbeddingSemanticConfig(), -42)

		result := newChatResult()
		stampRoutingDebug(ctx, result, schemas.ChatCompletionRequest, false)

		rd := result.GetExtraFields().RoutingDebug
		require.NotNil(t, rd, "the embed still ran, so it stays observable")
		require.Len(t, rd.Calls, 1)
		require.NotNil(t, rd.Calls[0].InputTokens)
		assert.Equal(t, 0, *rd.Calls[0].InputTokens)
	})
}

func TestCanClassifySemantically(t *testing.T) {
	executor := func(ctx *schemas.BifrostContext, req *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		return nil, nil
	}

	tests := []struct {
		name     string
		wired    bool
		semantic *complexity.SemanticConfig
		want     bool
	}{
		{name: "fully configured", wired: true, semantic: testEmbeddingSemanticConfig(), want: true},
		{name: "executor missing", wired: false, semantic: testEmbeddingSemanticConfig(), want: false},
		{name: "semantic nil", wired: true, semantic: nil, want: false},
		{name: "provider missing", wired: true, semantic: &complexity.SemanticConfig{EmbeddingModel: "m"}, want: false},
		{name: "model missing", wired: true, semantic: &complexity.SemanticConfig{Provider: "openai"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plugin := &RoutingPlugin{}
			if tt.wired {
				plugin.SetEmbeddingRequestExecutor(executor)
			}
			assert.Equal(t, tt.want, plugin.CanClassifySemantically(tt.semantic))
		})
	}
}

// settleRecorder records governance billing calls so the routing seam can be
// asserted without a budget ledger.
type settleRecorder struct {
	*MockGovernance
	calls []int
}

func (r *settleRecorder) AttributeRoutingEmbeddingCost(_ schemas.ModelProvider, _ string, inputTokens int) {
	r.calls = append(r.calls, inputTokens)
}

func TestSettleWarmupEmbedUsageRoutesBillingThroughGovernance(t *testing.T) {
	recorder := &settleRecorder{MockGovernance: NewMockGovernance()}
	plugin := &RoutingPlugin{governance: recorder}

	observed := 0
	observer := WarmupEmbedUsageObserver(func(_, _ string, inputTokens int) { observed += inputTokens })
	plugin.warmupEmbedUsageObserver.Store(&observer)

	cfg := testEmbeddingSemanticConfig()
	plugin.settleWarmupEmbedUsage(cfg, 500)
	assert.Equal(t, 500, observed, "observer sees usage regardless of the budget flag")
	assert.Empty(t, recorder.calls, "count_toward_budgets off must not bill governance")

	cfg.CountTowardBudgets = true
	plugin.settleWarmupEmbedUsage(cfg, 700)
	assert.Equal(t, []int{700}, recorder.calls, "count_toward_budgets on bills through governance")

	plugin.settleWarmupEmbedUsage(nil, 900)
	assert.Equal(t, []int{700}, recorder.calls, "nil semantic config settles nothing")
}

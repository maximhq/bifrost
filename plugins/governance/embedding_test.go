package governance

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
	"github.com/maximhq/bifrost/plugins/governance/complexity"
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
			plugin := &GovernancePlugin{}
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
	plugin := &GovernancePlugin{}
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
	plugin := &GovernancePlugin{}
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
	plugin := &GovernancePlugin{}
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
	plugin := &GovernancePlugin{}
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
			plugin := &GovernancePlugin{}
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
		plugin := &GovernancePlugin{}
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
		plugin := &GovernancePlugin{}
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
		plugin := &GovernancePlugin{}
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
		plugin := &GovernancePlugin{}
		ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
		defer ctx.Cancel()
		_, _, err := plugin.generateEmbedding(ctx, testEmbeddingSemanticConfig(), "x", requestEmbeddingTimeout(testEmbeddingSemanticConfig()))
		require.ErrorContains(t, err, "executor is not configured")
	})

	t.Run("nil semantic config", func(t *testing.T) {
		plugin := &GovernancePlugin{}
		plugin.SetEmbeddingRequestExecutor(okExecutor)
		ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
		defer ctx.Cancel()
		_, _, err := plugin.generateEmbedding(ctx, nil, "x", requestEmbeddingTimeout(testEmbeddingSemanticConfig()))
		require.ErrorContains(t, err, "not configured")
	})

	t.Run("empty response data", func(t *testing.T) {
		plugin := &GovernancePlugin{}
		plugin.SetEmbeddingRequestExecutor(func(ctx *schemas.BifrostContext, req *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
			return &schemas.BifrostEmbeddingResponse{}, nil
		})
		ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
		defer ctx.Cancel()
		_, _, err := plugin.generateEmbedding(ctx, testEmbeddingSemanticConfig(), "x", requestEmbeddingTimeout(testEmbeddingSemanticConfig()))
		require.ErrorContains(t, err, "no embeddings returned")
	})

	t.Run("unset executor after set", func(t *testing.T) {
		plugin := &GovernancePlugin{}
		plugin.SetEmbeddingRequestExecutor(okExecutor)
		plugin.SetEmbeddingRequestExecutor(nil)
		ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
		defer ctx.Cancel()
		_, _, err := plugin.generateEmbedding(ctx, testEmbeddingSemanticConfig(), "x", requestEmbeddingTimeout(testEmbeddingSemanticConfig()))
		require.ErrorContains(t, err, "executor is not configured")
	})
}

func TestEmbedComplexityTextRecordsRoutingUsage(t *testing.T) {
	plugin := &GovernancePlugin{}
	plugin.SetEmbeddingRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		return embeddingResponse(schemas.EmbeddingStruct{EmbeddingArray: []float64{1, 0}}, 42), nil
	})

	cfg := testEmbeddingSemanticConfig()
	cfg.CountTowardBudgets = true

	ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
	defer ctx.Cancel()
	_, err := plugin.embedComplexityText(ctx, cfg, "classify me")
	require.NoError(t, err)

	usage, ok := ctx.Value(routingEmbedUsageContextKey).(*routingEmbedUsage)
	require.True(t, ok, "classification embed must record usage on the request context")
	assert.Equal(t, "openai", usage.Provider)
	assert.Equal(t, "text-embedding-3-small", usage.Model)
	assert.Equal(t, 42, usage.InputTokens)
	assert.True(t, usage.CountTowardBudgets)
}

// TestEmbedComplexityTextAccumulatesUsageAcrossFallbackAttempts models a
// request that fails over: core hands the same context to tryRequest for each
// fallback, the pre-hooks re-run, and classification embeds again. Only one
// record survives to the RoutingDebug stamp, so it has to carry every attempt's
// tokens or the provider bills for embeds nothing prices.
func TestEmbedComplexityTextAccumulatesUsageAcrossFallbackAttempts(t *testing.T) {
	plugin := &GovernancePlugin{}
	plugin.SetEmbeddingRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		return embeddingResponse(schemas.EmbeddingStruct{EmbeddingArray: []float64{1, 0}}, 20), nil
	})

	cfg := testEmbeddingSemanticConfig()
	cfg.CountTowardBudgets = true

	ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
	defer ctx.Cancel()
	for range 3 {
		_, err := plugin.embedComplexityText(ctx, cfg, "classify me")
		require.NoError(t, err)
	}

	usage, ok := ctx.Value(routingEmbedUsageContextKey).(*routingEmbedUsage)
	require.True(t, ok)
	assert.Equal(t, 60, usage.InputTokens, "every attempt's embed must be priced, not just the last")
	assert.Equal(t, "openai", usage.Provider)
	assert.True(t, usage.CountTowardBudgets)
}

// A semantic config reloaded between attempts embeds through different rates,
// and one record cannot describe both — the newer provider/model must win and
// start a fresh count rather than billing older tokens at the new rate.
func TestEmbedComplexityTextRestartsUsageWhenTheEmbeddingModelChanges(t *testing.T) {
	plugin := &GovernancePlugin{}
	plugin.SetEmbeddingRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		return embeddingResponse(schemas.EmbeddingStruct{EmbeddingArray: []float64{1, 0}}, 20), nil
	})

	ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
	defer ctx.Cancel()
	_, err := plugin.embedComplexityText(ctx, testEmbeddingSemanticConfig(), "classify me")
	require.NoError(t, err)

	reloaded := testEmbeddingSemanticConfig()
	reloaded.EmbeddingModel = "text-embedding-3-large"
	_, err = plugin.embedComplexityText(ctx, reloaded, "classify me")
	require.NoError(t, err)

	usage, ok := ctx.Value(routingEmbedUsageContextKey).(*routingEmbedUsage)
	require.True(t, ok)
	assert.Equal(t, "text-embedding-3-large", usage.Model)
	assert.Equal(t, 20, usage.InputTokens)
}

// A provider that reports a negative token count must not have it recorded:
// the stamp feeds cost calculation and warmup budget attribution, where a
// negative count would subtract from billed usage.
func TestEmbedComplexityTextDropsNegativeProviderUsage(t *testing.T) {
	plugin := &GovernancePlugin{}
	plugin.SetEmbeddingRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		return embeddingResponse(schemas.EmbeddingStruct{EmbeddingArray: []float64{1, 0}}, -42), nil
	})

	cfg := testEmbeddingSemanticConfig()
	cfg.CountTowardBudgets = true

	ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
	defer ctx.Cancel()
	_, err := plugin.embedComplexityText(ctx, cfg, "classify me")
	require.NoError(t, err)

	usage, ok := ctx.Value(routingEmbedUsageContextKey).(*routingEmbedUsage)
	require.True(t, ok)
	assert.Equal(t, 0, usage.InputTokens, "negative provider usage must not reach budget accounting")
}

// warmupObservation captures one WarmupEmbedUsageObserver invocation.
type warmupObservation struct {
	Provider    string
	Model       string
	InputTokens int
}

func TestEmbedComplexityTextWarmupPathObservesInsteadOfRecording(t *testing.T) {
	plugin := &GovernancePlugin{}
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
	plugin := &GovernancePlugin{}
	plugin.SetEmbeddingRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		return embeddingResponse(schemas.EmbeddingStruct{EmbeddingArray: []float64{1, 0}}, 42), nil
	})

	// No observer wired (SDK usage, or before the server wires it): the warmup
	// path must still work and must not panic.
	_, err := plugin.embedComplexityText(t.Context(), testEmbeddingSemanticConfig(), "warmup exemplar")
	require.NoError(t, err)
}

func TestEmbedComplexityTextsObservesWarmupNeverRecordsRoutingUsage(t *testing.T) {
	plugin := &GovernancePlugin{}
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
	assert.Nil(t, ctx.Value(routingEmbedUsageContextKey))
	require.Len(t, observed, 1)
	assert.Equal(t, warmupObservation{"openai", "text-embedding-3-small", 7}, observed[0])
}

// TestEmbedComplexityTextsSettlesUsageOnUnsupportedBatch covers the shape a
// single-input-only model returns for a multi-text batch. Warmup recovers by
// re-embedding each phrase on its own, so this call's tokens are the ones that
// would silently vanish: the provider billed for them and the run still
// succeeds.
func TestEmbedComplexityTextsSettlesUsageOnUnsupportedBatch(t *testing.T) {
	plugin := &GovernancePlugin{}
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
	plugin := &GovernancePlugin{}
	var observed []warmupObservation
	plugin.SetWarmupEmbedUsageObserver(func(provider, model string, inputTokens int) {
		observed = append(observed, warmupObservation{provider, model, inputTokens})
	})

	_, err := plugin.embedComplexityTexts(t.Context(), testEmbeddingSemanticConfig(), []string{"a", "b"})
	require.ErrorIs(t, err, ErrEmbeddingRequestExecutorNotConfigured)
	assert.Empty(t, observed)
}

func TestRequestClassificationEmbedDoesNotObserveWarmup(t *testing.T) {
	plugin := &GovernancePlugin{}
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
	assert.NotNil(t, ctx.Value(routingEmbedUsageContextKey))
	assert.Empty(t, observed)
}

// TestEmbedComplexityTextSettlesUsageWhenProviderReturnsNoVectors is the
// single-input side of the same rule the batch path already follows. Warmup
// reaches this path through its fallback after a provider refuses a batch, so a
// deployment on a single-input-only model settles every warmup embed here.
func TestEmbedComplexityTextSettlesUsageWhenProviderReturnsNoVectors(t *testing.T) {
	plugin, store := newWarmupBudgetFixture(t)
	plugin.SetEmbeddingRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		return &schemas.BifrostEmbeddingResponse{
			Data:  nil,
			Usage: &schemas.BifrostLLMUsage{TotalTokens: 1000},
		}, nil
	})
	var observed []warmupObservation
	plugin.SetWarmupEmbedUsageObserver(func(provider, model string, inputTokens int) {
		observed = append(observed, warmupObservation{provider, model, inputTokens})
	})

	cfg := testEmbeddingSemanticConfig()
	cfg.CountTowardBudgets = true

	// A plain context is warmup's single-input fallback.
	_, err := plugin.embedComplexityText(t.Context(), cfg, "warm me")
	require.Error(t, err)

	require.Len(t, observed, 1)
	assert.Equal(t, warmupObservation{"openai", "text-embedding-3-small", 1000}, observed[0])
	// 1000 tokens × $0.00000002/token (text-embedding-3-small in testdata).
	usage := store.GetGovernanceData(context.Background()).Budgets["provider-budget"].CurrentUsage
	assert.InDelta(t, 0.00002, usage, 1e-12)
}

// TestRequestClassificationEmbedRecordsUsageWhenProviderReturnsNoVectors keeps
// the request side consistent: a classification that failed on response shape
// still cost the caller tokens, so it reaches the RoutingDebug stamp rather
// than disappearing because no tier came back.
func TestRequestClassificationEmbedRecordsUsageWhenProviderReturnsNoVectors(t *testing.T) {
	plugin := &GovernancePlugin{}
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

	usage, ok := ctx.Value(routingEmbedUsageContextKey).(*routingEmbedUsage)
	require.True(t, ok, "a billed classification embed must be recorded for the stamp")
	assert.Equal(t, 42, usage.InputTokens)
	assert.Empty(t, observed, "a request embed must never fire the warmup observer")
}

// TestEmbedComplexityTextSkipsUsageBeforeProviderResponds keeps the two tests
// above from becoming "settle on every failure": a call that never reached the
// provider has nothing to bill.
func TestEmbedComplexityTextSkipsUsageBeforeProviderResponds(t *testing.T) {
	plugin := &GovernancePlugin{}
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
		ctx.SetValue(routingEmbedUsageContextKey, &routingEmbedUsage{
			Provider:           "openai",
			Model:              "text-embedding-3-small",
			InputTokens:        42,
			CountTowardBudgets: countTowardBudgets,
		})
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
			require.NotNil(t, rd.ProviderUsed)
			assert.Equal(t, "openai", *rd.ProviderUsed)
			require.NotNil(t, rd.ModelUsed)
			assert.Equal(t, "text-embedding-3-small", *rd.ModelUsed)
			require.NotNil(t, rd.InputTokens)
			assert.Equal(t, 42, *rd.InputTokens)
			assert.Equal(t, flag, rd.CountTowardBudgets)
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
	})

	t.Run("no usage recorded means no stamp", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
		defer ctx.Cancel()
		result := newChatResult()
		stampRoutingDebug(ctx, result, schemas.ChatCompletionRequest, false)
		assert.Nil(t, result.GetExtraFields().RoutingDebug)
	})

	t.Run("nil result is a no-op", func(t *testing.T) {
		stampRoutingDebug(newCtxWithUsage(t, true), nil, schemas.ChatCompletionRequest, false)
	})

	// The stamp feeds cost calculation, so a negative token count must never
	// leave this function — it would price to a negative routing charge and
	// subtract from the request's budget attribution.
	t.Run("negative input tokens are rejected", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
		defer ctx.Cancel()
		ctx.SetValue(routingEmbedUsageContextKey, &routingEmbedUsage{
			Provider:           "openai",
			Model:              "text-embedding-3-small",
			InputTokens:        -42,
			CountTowardBudgets: true,
		})

		result := newChatResult()
		stampRoutingDebug(ctx, result, schemas.ChatCompletionRequest, false)

		rd := result.GetExtraFields().RoutingDebug
		require.NotNil(t, rd, "the embed still ran, so it stays observable")
		require.NotNil(t, rd.InputTokens)
		assert.Equal(t, 0, *rd.InputTokens)
	})
}

// newOfflinePricingCatalog builds a ModelCatalog from the committed pricing
// testdata via a file:// URL (no network). The testdata includes
// text-embedding-3-small at $0.00000002 per input token.
func newOfflinePricingCatalog(t *testing.T) *modelcatalog.ModelCatalog {
	t.Helper()
	abs, err := filepath.Abs("../../framework/modelcatalog/datasheet/testdata/pricing.json")
	require.NoError(t, err)
	ds := datasheet.New(nil, NewMockLogger(), datasheet.Config{URL: "file://" + abs})
	require.NoError(t, ds.LoadFromURLIntoMemory(context.Background()))
	return modelcatalog.NewTestCatalogWithDatasheet(ds)
}

// newWarmupBudgetFixture wires a plugin over a store whose "openai" provider
// carries a provider-level budget — the admin-owned ledger warmup embeds are
// attributed to when count_toward_budgets is on.
func newWarmupBudgetFixture(t *testing.T) (*GovernancePlugin, GovernanceStore) {
	t.Helper()
	logger := NewMockLogger()
	budget := buildBudgetWithUsage("provider-budget", 1000.0, 0.0, "1d")
	budgetID := budget.ID
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		Budgets:   []configstoreTables.TableBudget{*budget},
		Providers: []configstoreTables.TableProvider{{Name: "openai", BudgetID: &budgetID}},
	}, nil)
	require.NoError(t, err)
	plugin := &GovernancePlugin{
		ctx:          context.Background(),
		store:        store,
		modelCatalog: newOfflinePricingCatalog(t),
		logger:       logger,
	}
	return plugin, store
}

func TestSettleWarmupEmbedUsageAttributesProviderBudget(t *testing.T) {
	plugin, store := newWarmupBudgetFixture(t)
	cfg := testEmbeddingSemanticConfig()
	cfg.CountTowardBudgets = true

	plugin.settleWarmupEmbedUsage(cfg, 1000)

	// 1000 tokens × $0.00000002/token (text-embedding-3-small in testdata).
	usage := store.GetGovernanceData(context.Background()).Budgets["provider-budget"].CurrentUsage
	assert.InDelta(t, 0.00002, usage, 1e-12)
}

// TestEmbedComplexityTextsSettlesUsageWhenProviderReturnsNoVectors covers the
// one post-response failure that used to drop its tokens: a provider that
// reports usage but returns an empty Data array. The call completed and was
// billed, so it must reach both the observer and, with count_toward_budgets on,
// the provider budget — exactly like the vector-count mismatch beside it.
func TestEmbedComplexityTextsSettlesUsageWhenProviderReturnsNoVectors(t *testing.T) {
	plugin, store := newWarmupBudgetFixture(t)
	plugin.SetEmbeddingRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		return &schemas.BifrostEmbeddingResponse{
			Data:  nil,
			Usage: &schemas.BifrostLLMUsage{TotalTokens: 1000},
		}, nil
	})

	var observedProvider, observedModel string
	var observedTokens int
	var observedCalls int
	plugin.SetWarmupEmbedUsageObserver(func(provider, model string, inputTokens int) {
		observedCalls++
		observedProvider, observedModel, observedTokens = provider, model, inputTokens
	})

	cfg := testEmbeddingSemanticConfig()
	cfg.CountTowardBudgets = true

	_, err := plugin.embedComplexityTexts(t.Context(), cfg, []string{"first", "second"})
	require.Error(t, err, "an empty Data array is still a rejected response shape")

	require.Equal(t, 1, observedCalls, "the observer must see the billed call exactly once")
	assert.Equal(t, "openai", observedProvider)
	assert.Equal(t, "text-embedding-3-small", observedModel)
	assert.Equal(t, 1000, observedTokens)

	// 1000 tokens × $0.00000002/token (text-embedding-3-small in testdata).
	usage := store.GetGovernanceData(context.Background()).Budgets["provider-budget"].CurrentUsage
	assert.InDelta(t, 0.00002, usage, 1e-12)
}

func TestSettleWarmupEmbedUsageFlagOffLeavesBudgetsUntouched(t *testing.T) {
	plugin, store := newWarmupBudgetFixture(t)

	// count_toward_budgets defaults to off: warmup cost stays telemetry-only.
	plugin.settleWarmupEmbedUsage(testEmbeddingSemanticConfig(), 1000)

	usage := store.GetGovernanceData(context.Background()).Budgets["provider-budget"].CurrentUsage
	assert.Equal(t, 0.0, usage)
}

func TestSettleWarmupEmbedUsageWithoutStoreOrCatalog(t *testing.T) {
	// Bare plugin (no store, no catalog, no observer): flag on must be a
	// harmless no-op, not a panic — SDK callers may never wire these.
	plugin := &GovernancePlugin{}
	cfg := testEmbeddingSemanticConfig()
	cfg.CountTowardBudgets = true
	plugin.settleWarmupEmbedUsage(cfg, 1000)
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
			plugin := &GovernancePlugin{}
			if tt.wired {
				plugin.SetEmbeddingRequestExecutor(executor)
			}
			assert.Equal(t, tt.want, plugin.CanClassifySemantically(tt.semantic))
		})
	}
}

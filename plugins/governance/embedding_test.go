package governance

import (
	"errors"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
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
			vector, tokens, err := plugin.generateEmbedding(ctx, testEmbeddingSemanticConfig(), "hello")
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
	_, _, err := plugin.generateEmbedding(ctx, cfg, "classify me")
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
	embeddings, tokens, err := plugin.generateEmbeddings(ctx, testEmbeddingSemanticConfig(), []string{"first", "second"})
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
	_, _, err := plugin.generateEmbeddings(ctx, testEmbeddingSemanticConfig(), []string{"first", "second"})
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
	_, _, err := plugin.generateEmbedding(ctx, cfg, "slow")
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
			_, _, err := plugin.generateEmbedding(ctx, testEmbeddingSemanticConfig(), "classify me")
			require.Error(t, err)
			assert.Equal(t, tt.wantTimeout, errors.Is(err, ErrEmbeddingTimeout))
		})
	}
}

func TestGenerateEmbeddingGuards(t *testing.T) {
	okExecutor := func(ctx *schemas.BifrostContext, req *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		return embeddingResponse(schemas.EmbeddingStruct{EmbeddingArray: []float64{1}}, 1), nil
	}

	t.Run("nil executor", func(t *testing.T) {
		plugin := &GovernancePlugin{}
		ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
		defer ctx.Cancel()
		_, _, err := plugin.generateEmbedding(ctx, testEmbeddingSemanticConfig(), "x")
		require.ErrorContains(t, err, "executor is not configured")
	})

	t.Run("nil semantic config", func(t *testing.T) {
		plugin := &GovernancePlugin{}
		plugin.SetEmbeddingRequestExecutor(okExecutor)
		ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
		defer ctx.Cancel()
		_, _, err := plugin.generateEmbedding(ctx, nil, "x")
		require.ErrorContains(t, err, "not configured")
	})

	t.Run("empty response data", func(t *testing.T) {
		plugin := &GovernancePlugin{}
		plugin.SetEmbeddingRequestExecutor(func(ctx *schemas.BifrostContext, req *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
			return &schemas.BifrostEmbeddingResponse{}, nil
		})
		ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
		defer ctx.Cancel()
		_, _, err := plugin.generateEmbedding(ctx, testEmbeddingSemanticConfig(), "x")
		require.ErrorContains(t, err, "no embeddings returned")
	})

	t.Run("unset executor after set", func(t *testing.T) {
		plugin := &GovernancePlugin{}
		plugin.SetEmbeddingRequestExecutor(okExecutor)
		plugin.SetEmbeddingRequestExecutor(nil)
		ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
		defer ctx.Cancel()
		_, _, err := plugin.generateEmbedding(ctx, testEmbeddingSemanticConfig(), "x")
		require.ErrorContains(t, err, "executor is not configured")
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
			plugin := &GovernancePlugin{}
			if tt.wired {
				plugin.SetEmbeddingRequestExecutor(executor)
			}
			assert.Equal(t, tt.want, plugin.CanClassifySemantically(tt.semantic))
		})
	}
}

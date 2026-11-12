package evals

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/maximhq/bifrost/core/schemas"
)

// EmbeddingExecutor mirrors bifrost.Client.EmbeddingRequest.
type EmbeddingExecutor func(ctx *schemas.BifrostContext, req *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError)

// NewBifrostEmbedder returns an Embedder that calls embedding models through the
// gateway. Like the judge, each call is tagged as evaluation traffic and pinned to the
// eval key the runner placed the job on, and is refused without one.
func NewBifrostEmbedder(embed EmbeddingExecutor, opts BifrostJudgeOptions) Embedder {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultJudgeTimeout
	}
	return func(ctx context.Context, provider, model string, texts []string) (EmbeddingResponse, error) {
		if embed == nil {
			return EmbeddingResponse{}, errors.New("no embedding client is configured")
		}
		keyID := KeyIDFromContext(ctx)
		if keyID == "" {
			return EmbeddingResponse{}, fmt.Errorf("%w: an evaluation model call must run on an eval key", ErrNoEvalKey)
		}
		req := &schemas.BifrostEmbeddingRequest{
			Provider: schemas.ModelProvider(provider),
			Model:    model,
			Input:    make([]schemas.EmbeddingInputItem, len(texts)),
		}
		for i := range texts {
			req.Input[i] = schemas.EmbeddingInputItem{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &texts[i]}}}
		}

		bifrostCtx, cancel := newEvalRequestContext(ctx, keyID, timeout, opts.PrepareContext)
		defer cancel()
		response, bifrostErr := embed(bifrostCtx, req)
		if bifrostErr != nil {
			return EmbeddingResponse{}, errors.New(bifrostErr.GetErrorString())
		}
		if response == nil {
			return EmbeddingResponse{}, errors.New("the embedding model returned no response")
		}

		out := EmbeddingResponse{Vectors: make([][]float64, 0, len(response.Data))}
		// Providers may return the vectors out of order; Index says which input each is for.
		data := slices.Clone(response.Data)
		slices.SortStableFunc(data, func(a, b schemas.EmbeddingData) int { return a.Index - b.Index })
		for _, item := range data {
			switch {
			case len(item.Embedding.EmbeddingArray) > 0:
				out.Vectors = append(out.Vectors, item.Embedding.EmbeddingArray)
			case len(item.Embedding.Embedding2DArray) > 0:
				out.Vectors = append(out.Vectors, item.Embedding.Embedding2DArray[0])
			default:
				return EmbeddingResponse{}, errors.New("the embedding model returned a format other than float vectors")
			}
		}
		if usage := response.Usage; usage != nil {
			out.Usage = &Usage{
				PromptTokens:     max(usage.PromptTokens, 0),
				CompletionTokens: max(usage.CompletionTokens, 0),
				TotalTokens:      max(usage.TotalTokens, 0),
			}
			if usage.Cost != nil && usage.Cost.TotalCost > 0 {
				out.Cost = new(usage.Cost.TotalCost)
			}
		}
		return out, nil
	}
}
